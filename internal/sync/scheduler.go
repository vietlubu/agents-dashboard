package sync

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

// Scheduler runs the scan loop.
//
// Cost model, which is the whole reason the loop is adaptive rather than a fixed fast
// poll: an idle pass only stats every session file (the parse is skipped because
// (inode, mtime, size) is unchanged), so it costs single-digit milliseconds no matter how
// much history exists. While something is actually being written, a short burst interval
// keeps the UI current; when the burst window expires it falls back to the idle interval.
type Scheduler struct {
	engine *Engine
	cfg    *config.Config
	log    *slog.Logger

	mu         sync.Mutex
	running    bool
	nextRunAt  int64
	intervalMs int64
	burst      bool
	burstUntil time.Time

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// Status is the scheduler's externally visible state.
type Status struct {
	Running    bool        `json:"running"`
	NextRunAt  int64       `json:"nextRunAt"`
	IntervalMs int64       `json:"intervalMs"`
	Burst      bool        `json:"burst"`
	Last       *RunSummary `json:"last,omitempty"`
}

// NewScheduler builds a scheduler for an engine.
func NewScheduler(engine *Engine, cfg *config.Config, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		engine: engine,
		cfg:    cfg,
		log:    log,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start launches the loop and returns immediately. The first scan runs straight away so
// the UI fills in as soon as the window is up.
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

// Stop ends the loop and waits for it to finish.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

// TriggerNow starts an immediate scan when nothing is running. It is what the refresh
// button and "add scan root" use.
func (s *Scheduler) TriggerNow(trigger string) bool {
	if s.engine.Running() {
		return false
	}
	go func() {
		if _, err := s.engine.Run(context.Background(), trigger); err != nil && err != ErrAlreadyRunning {
			s.log.Warn("manual sync failed", "error", err)
		}
	}()
	return true
}

// Status reports the loop's state for the UI.
func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Running:    s.running,
		NextRunAt:  s.nextRunAt,
		IntervalMs: s.intervalMs,
		Burst:      s.burst,
		Last:       s.engine.LastRun(),
	}
}

func (s *Scheduler) loop(ctx context.Context) {
	defer close(s.done)

	// First scan: startup.
	s.runOnce(ctx, "startup")

	for {
		wait := s.waitDuration()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.stop:
			timer.Stop()
			return
		case <-timer.C:
		}
		s.runOnce(ctx, "scheduled")
	}
}

func (s *Scheduler) runOnce(ctx context.Context, trigger string) {
	mutable := s.cfg.Snapshot()

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	summary, err := s.engine.Run(ctx, trigger)
	if err != nil && err != ErrAlreadyRunning {
		s.log.Warn("sync failed", "trigger", trigger, "error", err)
	}

	changed := summary.EventsInserted+summary.EventsUpdated > 0
	interval := mutable.IdleInterval
	burst := false
	if changed {
		// Data is moving; poll faster for a bounded window.
		s.mu.Lock()
		s.burstUntil = time.Now().Add(mutable.BurstWindow)
		s.mu.Unlock()
	}
	s.mu.Lock()
	if time.Now().Before(s.burstUntil) {
		interval = mutable.BurstInterval
		burst = true
	}
	s.running = false
	s.intervalMs = interval.Milliseconds()
	s.nextRunAt = time.Now().Add(interval).UnixMilli()
	s.burst = burst
	s.mu.Unlock()
}

func (s *Scheduler) waitDuration() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextRunAt == 0 {
		return time.Second
	}
	wait := time.Until(time.UnixMilli(s.nextRunAt))
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}
