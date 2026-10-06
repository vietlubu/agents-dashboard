//go:build darwin

package sleep

import (
	"errors"
	"fmt"
	"sync"
)

type darwinLidHooks struct {
	available bool
	set       func(bool) (applied bool, err error)
	read      func() (bool, error)
	watch     func() (<-chan struct{}, func(), error)
	start     func() (*lidGuardian, error)
}

// Operations are serialized by darwinInhibitor. Observation and cached status
// have their own locks so callbacks never wait for a native operation to finish.
type darwinLidControl struct {
	mu           sync.Mutex
	hooks        darwinLidHooks
	state        LidState
	guardian     *lidGuardian
	watchStop    func()
	watchDone    chan struct{}
	dirty        bool
	epoch        uint64
	version      uint64
	cleanupErr   error
	notifyMu     sync.Mutex
	notifier     func()
	notifyEpoch  uint64
	notifyActive bool
	callbacks    sync.WaitGroup
}

func newDarwinLidControl(hooks darwinLidHooks) *darwinLidControl {
	return &darwinLidControl{hooks: hooks, state: LidState{PrivateAPI: hooks.available}}
}

func (l *darwinLidControl) status() LidState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *darwinLidControl) setNotifier(notifier func()) {
	l.notifyMu.Lock()
	l.notifier = notifier
	l.notifyMu.Unlock()
	if notifier == nil {
		l.callbacks.Wait()
	}
}

func (l *darwinLidControl) notify(epoch uint64) {
	l.notifyMu.Lock()
	callback := l.notifier
	if !l.notifyActive || l.notifyEpoch != epoch || callback == nil {
		l.notifyMu.Unlock()
		return
	}
	l.callbacks.Add(1)
	l.notifyMu.Unlock()
	defer l.callbacks.Done()
	callback()
}

func (l *darwinLidControl) invalidate(epoch uint64) {
	l.mu.Lock()
	if epoch != l.epoch || l.watchStop == nil {
		l.mu.Unlock()
		return
	}
	l.state.Known = false
	l.state.Effective = false
	l.dirty = true
	l.version++
	l.mu.Unlock()
	l.notify(epoch)
}

func (l *darwinLidControl) startWatch() error {
	l.mu.Lock()
	if l.watchStop != nil {
		l.mu.Unlock()
		return nil
	}
	l.mu.Unlock()
	events, stop, err := l.hooks.watch()
	if err != nil {
		return fmt.Errorf("start lid observation: %w", err)
	}
	l.mu.Lock()
	l.epoch++
	epoch := l.epoch
	done := make(chan struct{})
	l.watchStop, l.watchDone = stop, done
	l.mu.Unlock()
	l.notifyMu.Lock()
	l.notifyEpoch, l.notifyActive = epoch, true
	l.notifyMu.Unlock()
	go func() {
		for {
			select {
			case <-done:
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				l.invalidate(epoch)
			}
		}
	}()
	return nil
}

func (l *darwinLidControl) stopWatch() {
	// Wait out a callback before invalidating its generation. A queued old event
	// cannot notify after this returns, even if a new observation starts later.
	l.notifyMu.Lock()
	l.notifyActive = false
	l.notifyMu.Unlock()
	l.callbacks.Wait()
	l.mu.Lock()
	stop, done := l.watchStop, l.watchDone
	l.watchStop, l.watchDone = nil, nil
	l.epoch++
	l.dirty = false
	l.mu.Unlock()
	if stop != nil {
		close(done)
		stop()
	}
}

func (l *darwinLidControl) startGuardian() error {
	g, err := l.hooks.start()
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.guardian = g
	epoch := l.epoch
	l.mu.Unlock()
	go func() {
		<-g.done
		l.mu.Lock()
		current := l.guardian == g
		l.mu.Unlock()
		if current {
			l.invalidate(epoch)
		}
	}()
	return nil
}

func (l *darwinLidControl) detachGuardian() *lidGuardian {
	l.mu.Lock()
	g := l.guardian
	l.guardian = nil
	l.mu.Unlock()
	return g
}

func (l *darwinLidControl) observe() (bool, error) {
	l.mu.Lock()
	version := l.version
	l.mu.Unlock()
	effective, err := l.hooks.read()
	l.mu.Lock()
	l.state.Known = err == nil && version == l.version
	l.state.Effective = l.state.Known && effective
	observed := l.state.Effective
	l.mu.Unlock()
	return observed, err
}

func (l *darwinLidControl) reconcile(want bool) error {
	if !want {
		return l.release()
	}
	if !l.hooks.available {
		return errUnsupported
	}
	l.mu.Lock()
	requested, g := l.state.Requested, l.guardian
	l.mu.Unlock()
	if requested && (g == nil || !g.alive() || g.broken) {
		if err := l.release(); err != nil {
			return err
		}
		requested, g = false, nil
	}
	if err := l.startWatch(); err != nil {
		return err
	}
	if g == nil {
		if err := l.startGuardian(); err != nil {
			l.stopWatch()
			return err
		}
		l.mu.Lock()
		g = l.guardian
		l.mu.Unlock()
	}
	effective, readErr := l.observe()
	l.mu.Lock()
	dirty := l.dirty
	l.mu.Unlock()
	if !requested || readErr != nil || !effective || dirty {
		applied, err := g.exchange(true)
		if applied {
			l.mu.Lock()
			l.state.Requested = true
			l.mu.Unlock()
		}
		if err != nil {
			l.mu.Lock()
			l.state.Known, l.state.Effective = false, false
			l.mu.Unlock()
			if !requested {
				return errors.Join(err, l.release())
			}
			return err
		}
		l.mu.Lock()
		l.state.Requested = true
		l.dirty = false
		l.cleanupErr = nil
		l.mu.Unlock()
		_, readErr = l.observe()
	}
	if readErr != nil {
		if !requested {
			return errors.Join(readErr, l.release())
		}
		return readErr
	}
	return nil
}

func (l *darwinLidControl) release() error {
	l.mu.Lock()
	requested, g := l.state.Requested, l.guardian
	l.mu.Unlock()
	resetConfirmed := !requested
	var resetErr error
	if requested {
		if g != nil && g.alive() && !g.broken {
			applied, err := g.exchange(false)
			if applied {
				resetConfirmed, resetErr = true, err
			} else if err != nil && g.alive() && !g.broken {
				return err
			}
		}
		if !resetConfirmed && (g == nil || !g.alive() || g.broken) {
			if g != nil {
				// EOF cleanup must finish before fallback, preventing a delayed enable.
				l.detachGuardian()
				cleanup := g.stop()
				if g.alive() {
					l.mu.Lock()
					l.guardian = g
					l.mu.Unlock()
					return cleanup
				}
				g = nil
				resetConfirmed = cleanup == nil
			}
			if !resetConfirmed {
				// Only a previously acknowledged enable permits this fallback.
				applied, err := l.hooks.set(false)
				if !applied {
					if err == nil {
						err = errors.New("lid reset was not applied")
					}
					l.mu.Lock()
					l.state.Known, l.state.Effective = false, false
					l.mu.Unlock()
					return fmt.Errorf("reset lid after guardian loss: %w", err)
				}
				resetConfirmed = true
				if err != nil {
					resetErr = fmt.Errorf("close lid reset connection: %w", err)
				}
			}
		}
		if resetConfirmed {
			l.mu.Lock()
			l.state.Requested = false
			l.cleanupErr = nil
			l.mu.Unlock()
		}
	}
	var err error
	if g != nil {
		// Retire before closing the pipe: normal EOF is not unexpected guardian loss.
		l.detachGuardian()
		err = g.stop()
		if g.alive() {
			l.mu.Lock()
			l.guardian = g
			l.mu.Unlock()
			return errors.Join(err, resetErr)
		}
		if err != nil && !requested {
			l.mu.Lock()
			l.cleanupErr = fmt.Errorf("lid cleanup outcome unknown: %w", err)
			l.state.Known, l.state.Effective = false, false
			l.mu.Unlock()
		}
	}
	l.mu.Lock()
	uncertain := l.cleanupErr
	l.mu.Unlock()
	if uncertain != nil {
		return errors.Join(uncertain, resetErr)
	}
	l.stopWatch()
	if requested && resetConfirmed {
		// Freeze callbacks before the final sample of aggregate policy.
		_, _ = l.observe()
	}
	return errors.Join(err, resetErr)
}
