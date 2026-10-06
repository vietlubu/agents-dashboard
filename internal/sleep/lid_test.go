package sleep

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

type observedInhibitor struct {
	mu         sync.Mutex
	state      LidState
	notify     func()
	applyErr   error
	releaseErr error
}

func (f *observedInhibitor) Apply(spec InhibitSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A request can be acknowledged before effective shared policy is observed.
	f.state.Requested = spec.Lid
	return f.applyErr
}

func (f *observedInhibitor) Release() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return f.releaseErr
	}
	f.state.Requested = false
	return nil
}

func (f *observedInhibitor) LidState() LidState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *observedInhibitor) SetLidNotifier(notify func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notify = notify
}

func (f *observedInhibitor) observe(known, effective bool) {
	f.mu.Lock()
	f.state.Known, f.state.Effective = known, effective
	notify := f.notify
	f.mu.Unlock()
	if notify != nil {
		notify()
	}
}

func observedController(mode string, inhibitor *observedInhibitor, activity *fakeActivity, sleeper *fakeSleeper) (*Controller, *config.Config) {
	cfg := newTestConfig(time.Second, time.Minute)
	cfg.Apply(config.Mutable{SleepMode: mode, PreventLidClosedSleep: true})
	return New(Deps{Cfg: cfg, Inhibitor: inhibitor, Activity: activity, Sleeper: sleeper,
		Idler: fakeIdler{idle: time.Hour, ok: true}, Supported: true, LidSupported: true}), cfg
}

func TestNativeLidStatusSeparatesRequestFromSharedPolicy(t *testing.T) {
	inh := &observedInhibitor{state: LidState{PrivateAPI: true, Known: true, Effective: true}}
	c, cfg := observedController(config.SleepModeAlways, inh, nil, &fakeSleeper{})
	if c.Status().Held.Lid {
		t.Fatal("pre-existing shared policy was attributed to this app before it requested anything")
	}
	c.tickOnce(context.Background())
	if st := c.Status(); !st.Held.Lid || !st.KeepingAwake || !st.LidControl.Requested {
		t.Fatalf("acknowledged and observed lid request was not reported: %+v", st)
	}
	inh.observe(true, false)
	if st := c.Status(); st.Held.Lid || st.KeepingAwake || !st.LidControl.Requested || !st.LidControl.Known {
		t.Fatalf("an overwritten shared policy still claims actual inhibition: %+v", st)
	}
	inh.observe(false, false)
	if st := c.Status(); st.Held.Lid || !st.LidControl.Requested || st.LidControl.Known {
		t.Fatalf("unknown readback was treated as successful lid inhibition: %+v", st)
	}
	// Another owner can keep effective policy disabled after our own request is released.
	inh.observe(true, true)
	cfg.Apply(config.Mutable{SleepMode: config.SleepModeOff})
	c.tickOnce(context.Background())
	if st := c.Status(); st.Held.Lid || st.KeepingAwake || st.LidControl.Requested || !st.LidControl.Effective {
		t.Fatalf("off mode incorrectly claimed the aggregate policy as its own: %+v", st)
	}
}

func TestPartiallyAppliedLidRequestIsReleasedWhenOff(t *testing.T) {
	inh := &observedInhibitor{state: LidState{PrivateAPI: true}, applyErr: errors.New("readback unavailable")}
	c, cfg := observedController(config.SleepModeAlways, inh, nil, &fakeSleeper{})
	c.tickOnce(context.Background())
	if st := c.Status(); !st.LidControl.Requested || st.Held.Lid || st.Error == "" {
		t.Fatalf("partial request/observation failure was hidden: %+v", st)
	}
	cfg.Apply(config.Mutable{SleepMode: config.SleepModeOff})
	c.tickOnce(context.Background())
	if st := c.Status(); st.LidControl.Requested || st.Held.Lid || st.Error != "" {
		t.Fatalf("off skipped cleanup because no complete spec was held: %+v", st)
	}
}

func TestSleepDeclinesWhenLidReleaseFails(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		name := "explicit"
		mode := config.SleepModeAlways
		if automatic {
			name, mode = "automatic", config.SleepModeAgent
		}
		t.Run(name, func(t *testing.T) {
			inh := &observedInhibitor{state: LidState{PrivateAPI: true, Known: true, Effective: true}}
			act, slp := &fakeActivity{act: Activity{Active: true}}, &fakeSleeper{}
			c, _ := observedController(mode, inh, act, slp)
			c.tickOnce(context.Background())
			inh.releaseErr = errors.New("native lid reset failed")
			if automatic {
				act.act.Active = false
				c.idleSince = time.Now().Add(-time.Hour)
				c.tickOnce(context.Background())
			} else if err := c.SleepNow(); !errors.Is(err, inh.releaseErr) {
				t.Fatalf("manual Sleep ignored release error: %v", err)
			}
			if st := c.Status(); slp.sleeps != 0 || !st.Held.Lid || st.Error == "" || st.Detail == "sleeping" {
				t.Fatalf("failed release still slept or reported success: sleeps=%d, status=%+v", slp.sleeps, st)
			}
			inh.releaseErr = nil
			if automatic {
				c.tickOnce(context.Background())
			} else if err := c.SleepNow(); err != nil {
				t.Fatal(err)
			}
			if slp.sleeps != 1 || c.Status().Held.Lid {
				t.Fatalf("successful retry did not release before sleeping: sleeps=%d, status=%+v", slp.sleeps, c.Status())
			}
		})
	}
}

func TestStopKeepsFailedNativeReleaseVisible(t *testing.T) {
	inh := &observedInhibitor{state: LidState{PrivateAPI: true, Known: true, Effective: true}}
	c, _ := observedController(config.SleepModeAlways, inh, nil, &fakeSleeper{})
	c.tickOnce(context.Background())
	inh.releaseErr = errors.New("native reset refused")
	close(c.done) // Direct-tick fixture has no background loop to join.
	c.Stop()
	if st := c.Status(); !st.Held.Lid || !st.KeepingAwake || st.Error == "" {
		t.Fatalf("shutdown falsely reported that failed lid cleanup succeeded: %+v", st)
	}
}
