package sleep

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

type fakeInhibitor struct {
	applied    []InhibitSpec
	released   int
	clamshell  bool
	restores   int
	applyErr   error
	releaseErr error
}

func (f *fakeInhibitor) Apply(s InhibitSpec) error {
	f.applied = append(f.applied, s)
	return f.applyErr
}

func (f *fakeInhibitor) Release() error {
	f.released++
	return f.releaseErr
}

func (f *fakeInhibitor) RequestClamshell() error { f.clamshell = true; return nil }

func (f *fakeInhibitor) RestoreClamshell() error { f.clamshell = false; f.restores++; return nil }

func (f *fakeInhibitor) ClamshellActive() bool { return f.clamshell }

type fakeSleeper struct{ sleeps int }

func (f *fakeSleeper) Sleep() error {
	f.sleeps++
	return nil
}

type fakeDisplaySleeper struct{ calls int }

func (f *fakeDisplaySleeper) DisplaySleep() error {
	f.calls++
	return nil
}

type fakeScreensaver struct{ calls int }

func (f *fakeScreensaver) StartScreensaver() error {
	f.calls++
	return nil
}

type fakeIdler struct {
	idle time.Duration
	ok   bool
}

func (f fakeIdler) Idle() (time.Duration, bool) { return f.idle, f.ok }

type fakeActivity struct {
	act     Activity
	started chan struct{}
	resume  <-chan struct{}
}

func (f *fakeActivity) Observe(context.Context, time.Duration) (Activity, error) {
	observation := f.act
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.resume != nil {
		<-f.resume
	}
	return observation, nil
}

// newTestConfig builds a config in agent mode with the provided delay.
func newTestConfig(after, activeWindow time.Duration) *config.Config {
	cfg := &config.Config{}
	cfg.Apply(config.Mutable{
		SleepMode:             config.SleepModeAgent,
		SleepAfter:            after,
		SleepActiveWindow:     activeWindow,
		PreventSystemSleep:    true,
		PreventDisplaySleep:   true,
		PreventLidClosedSleep: false,
	})
	return cfg
}

func newTestController(cfg *config.Config, act *fakeActivity, inh *fakeInhibitor, slp *fakeSleeper, idler Idler) *Controller {
	return New(Deps{
		Cfg:       cfg,
		Activity:  act,
		Inhibitor: inh,
		Sleeper:   slp,
		Idler:     idler,
		Supported: true,
		Log:       nil,
		Tick:      time.Hour, // the loop is not used; tickOnce is called directly
	})
}

func TestCoalescedSleepModesDiscardAgentHistory(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		oldActive bool
	}{
		{config.SleepModeOff, false}, {config.SleepModeOff, true},
		{config.SleepModeAlways, false}, {config.SleepModeAlways, true},
	} {
		name := tc.mode
		if tc.oldActive {
			name += "/stale-active"
		}
		t.Run(name, func(t *testing.T) {
			cfg := newTestConfig(time.Second, time.Minute)
			started, resume := make(chan struct{}), make(chan struct{})
			act := &fakeActivity{act: Activity{Active: tc.oldActive}, started: started, resume: resume}
			slp := &fakeSleeper{}
			c := newTestController(cfg, act, &fakeInhibitor{}, slp, fakeIdler{idle: time.Hour, ok: true})
			c.seenActive = true
			c.idleSince = time.Now().Add(-time.Minute)
			done := make(chan struct{})
			go func() { c.tickOnce(context.Background()); close(done) }()
			<-started
			act.act.Active = false
			// Commit both modes while the agent observation is still in flight.
			snap := cfg.Snapshot()
			snap.SleepMode = tc.mode
			cfg.Apply(snap)
			c.Kick()
			snap.SleepMode = config.SleepModeAgent
			cfg.Apply(snap)
			c.Kick()
			close(resume)
			<-done
			st := c.Status()
			if st.Detail != "idle" || st.SleepAtMs != 0 || slp.sleeps != 0 {
				t.Fatalf("coalesced modes revived old activity: status=%+v, sleeps=%d", st, slp.sleeps)
			}
			act.started, act.resume = nil, nil
			c.tickOnce(context.Background())
			if st := c.Status(); st.Detail != "idle" || st.SleepAtMs != 0 || slp.sleeps != 0 {
				t.Fatalf("stale observation seeded another countdown: status=%+v, sleeps=%d", st, slp.sleeps)
			}
			act.act.Active = true
			c.tickOnce(context.Background())
			if st := c.Status(); st.Detail != "active" || !st.KeepingAwake {
				t.Errorf("new activity did not resume inhibition: %+v", st)
			}
		})
	}
}

func TestHoldsWhileActive(t *testing.T) {
	cfg := newTestConfig(time.Hour, time.Minute)
	act := &fakeActivity{act: Activity{Active: true, AgentRunning: true, Processes: []string{"claude"}}}
	inh := &fakeInhibitor{}
	slp := &fakeSleeper{}
	c := newTestController(cfg, act, inh, slp, fakeIdler{})

	c.tickOnce(context.Background())

	if len(inh.applied) != 1 {
		t.Fatalf("Apply calls = %d, want 1", len(inh.applied))
	}
	got := inh.applied[0]
	if !got.System || !got.Display || got.Lid {
		t.Errorf("held spec = %+v, want system+display only", got)
	}
	st := c.Status()
	if !st.KeepingAwake || !st.Active || st.Detail != "active" {
		t.Errorf("status = %+v, want keepingAwake active", st)
	}
	if slp.sleeps != 0 {
		t.Errorf("sleeper called %d times while active", slp.sleeps)
	}
}

func TestGracePeriodHoldsThenSleeps(t *testing.T) {
	// A one-millisecond delay makes the grace period elapse almost immediately.
	cfg := newTestConfig(time.Millisecond, time.Millisecond)
	act := &fakeActivity{act: Activity{Active: true}}
	inh := &fakeInhibitor{}
	slp := &fakeSleeper{}
	c := newTestController(cfg, act, inh, slp, fakeIdler{idle: time.Hour, ok: true})

	// First tick observes activity and starts holding.
	c.tickOnce(context.Background())
	if len(inh.applied) != 1 || !c.Status().KeepingAwake {
		t.Fatalf("expected to hold while active, got %+v", c.Status())
	}

	// The agent stops: the next tick starts the countdown (still in its grace period).
	act.act = Activity{Active: false}
	c.tickOnce(context.Background())
	if c.Status().Detail != "grace" || slp.sleeps != 0 {
		t.Fatalf("expected grace period, got %+v", c.Status())
	}

	// The delay elapses and the user is away, so the machine sleeps.
	time.Sleep(5 * time.Millisecond)
	c.tickOnce(context.Background())

	if slp.sleeps != 1 {
		t.Fatalf("sleeper called %d times, want 1", slp.sleeps)
	}
	if c.Status().KeepingAwake {
		t.Errorf("still holding the machine awake after sleep")
	}
}

func TestDoesNotSleepWhileUserActive(t *testing.T) {
	cfg := newTestConfig(time.Millisecond, time.Millisecond)
	act := &fakeActivity{act: Activity{Active: true}}
	inh := &fakeInhibitor{}
	slp := &fakeSleeper{}
	// The user is not idle: the delay has passed but the machine must stay up.
	c := newTestController(cfg, act, inh, slp, fakeIdler{idle: 0, ok: true})

	c.tickOnce(context.Background())
	act.act = Activity{Active: false}
	c.tickOnce(context.Background())
	time.Sleep(5 * time.Millisecond)
	c.tickOnce(context.Background())

	if slp.sleeps != 0 {
		t.Errorf("sleeper called %d times while the user was active", slp.sleeps)
	}
	if c.Status().Detail != "waiting-user" {
		t.Errorf("detail = %q, want waiting-user", c.Status().Detail)
	}
}

func TestDoesNotSleepWhenUserIdleUnmeasurable(t *testing.T) {
	cfg := newTestConfig(time.Millisecond, time.Millisecond)
	act := &fakeActivity{act: Activity{Active: true}}
	inh := &fakeInhibitor{}
	slp := &fakeSleeper{}
	c := newTestController(cfg, act, inh, slp, fakeIdler{ok: false})

	c.tickOnce(context.Background())
	act.act = Activity{Active: false}
	c.tickOnce(context.Background())
	time.Sleep(5 * time.Millisecond)
	c.tickOnce(context.Background())

	if slp.sleeps != 0 {
		t.Errorf("sleeper called %d times when user idle was unmeasurable", slp.sleeps)
	}
}

func TestDisabledReleases(t *testing.T) {
	cfg := newTestConfig(time.Hour, time.Minute)
	act := &fakeActivity{act: Activity{Active: true}}
	inh := &fakeInhibitor{}
	c := newTestController(cfg, act, inh, &fakeSleeper{}, fakeIdler{})

	c.tickOnce(context.Background())
	if !c.Status().KeepingAwake {
		t.Fatalf("expected to hold while active")
	}

	cfg.Apply(config.Mutable{SleepMode: config.SleepModeOff, PreventSystemSleep: true, PreventDisplaySleep: true})
	c.tickOnce(context.Background())

	if c.Status().KeepingAwake {
		t.Errorf("still holding after the feature was disabled")
	}
	if inh.released == 0 {
		t.Errorf("inhibitor was never released")
	}
}

func TestUnsupportedNeverHolds(t *testing.T) {
	cfg := newTestConfig(time.Hour, time.Minute)
	act := &fakeActivity{act: Activity{Active: true}}
	inh := &fakeInhibitor{}
	c := New(Deps{
		Cfg: cfg, Activity: act, Inhibitor: inh, Sleeper: &fakeSleeper{},
		Idler: fakeIdler{}, Supported: false, Tick: time.Hour,
	})

	c.tickOnce(context.Background())

	if len(inh.applied) != 0 {
		t.Errorf("applied an assertion on an unsupported platform")
	}
	if c.Status().Detail != "unsupported" {
		t.Errorf("detail = %q, want unsupported", c.Status().Detail)
	}
}

func TestOneShotActions(t *testing.T) {
	for _, mode := range []string{config.SleepModeAgent, config.SleepModeAlways} {
		t.Run(mode, func(t *testing.T) {
			cfg := newTestConfig(time.Hour, time.Minute)
			cfg.Apply(config.Mutable{SleepMode: mode, PreventSystemSleep: true})
			act := &fakeActivity{act: Activity{Active: true}}
			inh := &fakeInhibitor{}
			slp := &fakeSleeper{}
			disp := &fakeDisplaySleeper{}
			scr := &fakeScreensaver{}
			c := New(Deps{
				Cfg: cfg, Activity: act, Inhibitor: inh, Sleeper: slp,
				DisplaySleeper: disp, Screensaver: scr, Idler: fakeIdler{}, Supported: true, Tick: time.Hour,
			})

			// Hold an assertion first so SleepNow has one to release.
			c.tickOnce(context.Background())
			if !c.Status().KeepingAwake {
				t.Fatalf("expected to hold while active")
			}

			if err := c.SleepNow(); err != nil {
				t.Fatalf("SleepNow: %v", err)
			}
			if slp.sleeps != 1 {
				t.Errorf("sleeper calls = %d, want 1", slp.sleeps)
			}
			if inh.released == 0 {
				t.Errorf("SleepNow did not release the keep-awake assertion")
			}
			c.tickOnce(context.Background())
			if !c.Status().KeepingAwake || len(inh.applied) != 2 || c.Status().Mode != mode {
				t.Fatalf("next tick did not reapply mode %s: %+v", mode, c.Status())
			}

			if err := c.DisplaySleepNow(); err != nil || disp.calls != 1 {
				t.Errorf("DisplaySleepNow err = %v, calls = %d", err, disp.calls)
			}
			if err := c.ScreensaverNow(); err != nil || scr.calls != 1 {
				t.Errorf("ScreensaverNow err = %v, calls = %d", err, scr.calls)
			}
		})
	}
}

func TestClearsClamshellWhenLidSettingOff(t *testing.T) {
	for _, mode := range []string{config.SleepModeAgent, config.SleepModeAlways} {
		t.Run(mode, func(t *testing.T) {
			cfg := newTestConfig(time.Hour, time.Minute)
			cfg.Apply(config.Mutable{SleepMode: mode, PreventLidClosedSleep: true})
			inh := &fakeInhibitor{clamshell: true} // armed by an earlier session
			c := newTestController(cfg, &fakeActivity{}, inh, &fakeSleeper{}, fakeIdler{})

			c.tickOnce(context.Background())
			if inh.restores != 0 || !c.Status().Clamshell {
				t.Fatalf("restores = %d, clamshell = %v, want the flag left alone", inh.restores, c.Status().Clamshell)
			}
			// The user turns the lid setting off: the machine-wide flag must not outlive it.
			cfg.Apply(config.Mutable{SleepMode: mode, PreventLidClosedSleep: false})
			c.tickOnce(context.Background())

			if inh.restores != 1 || inh.clamshell || c.Status().Clamshell {
				t.Fatalf("restores = %d, clamshell = %v, want the flag cleared", inh.restores, inh.clamshell)
			}
		})
	}
}

func TestClearsClamshellWhenFeatureDisabled(t *testing.T) {
	cfg := newTestConfig(time.Hour, time.Minute)
	cfg.Apply(config.Mutable{SleepMode: config.SleepModeOff, PreventLidClosedSleep: true})
	inh := &fakeInhibitor{clamshell: true}
	c := newTestController(cfg, &fakeActivity{}, inh, &fakeSleeper{}, fakeIdler{})

	c.tickOnce(context.Background())

	if inh.restores != 1 || inh.clamshell {
		t.Fatalf("restores = %d, clamshell = %v, want the flag cleared", inh.restores, inh.clamshell)
	}
}

func TestSleepNowClearsClamshellFirst(t *testing.T) {
	for _, mode := range []string{config.SleepModeAgent, config.SleepModeAlways} {
		t.Run(mode, func(t *testing.T) {
			cfg := newTestConfig(time.Hour, time.Minute)
			cfg.Apply(config.Mutable{SleepMode: mode, PreventLidClosedSleep: true})
			inh := &fakeInhibitor{clamshell: true}
			slp := &fakeSleeper{}
			c := New(Deps{
				Cfg: cfg, Activity: &fakeActivity{}, Inhibitor: inh, Sleeper: slp,
				Idler: fakeIdler{}, Supported: true, Tick: time.Hour,
			})

			if err := c.SleepNow(); err != nil {
				t.Fatalf("SleepNow: %v", err)
			}
			if inh.restores != 1 || inh.clamshell {
				t.Errorf("restores = %d, clamshell = %v, want the flag cleared before sleeping", inh.restores, inh.clamshell)
			}
			if slp.sleeps != 1 {
				t.Errorf("sleeper calls = %d, want 1", slp.sleeps)
			}
		})
	}
}

func TestOneShotActionsUnsupported(t *testing.T) {
	c := New(Deps{Cfg: newTestConfig(time.Hour, time.Minute), Supported: true, Tick: time.Hour})
	if err := c.SleepNow(); err == nil {
		t.Errorf("SleepNow with no sleeper = nil, want error")
	}
	if err := c.DisplaySleepNow(); err == nil {
		t.Errorf("DisplaySleepNow with no display sleeper = nil, want error")
	}
	if err := c.ScreensaverNow(); err == nil {
		t.Errorf("ScreensaverNow with no screensaver = nil, want error")
	}
}

func TestActiveNowIgnoresMode(t *testing.T) {
	for _, mode := range []string{config.SleepModeOff, config.SleepModeAgent, config.SleepModeAlways} {
		t.Run(mode, func(t *testing.T) {
			cfg := newTestConfig(time.Hour, time.Minute)
			cfg.Apply(config.Mutable{SleepMode: mode})
			act := &fakeActivity{act: Activity{Active: true}}
			c := New(Deps{Cfg: cfg, Activity: act})
			if !c.ActiveNow(context.Background()) {
				t.Fatal("ActiveNow ignored active agent")
			}
			act.act = Activity{}
			if c.ActiveNow(context.Background()) {
				t.Fatal("ActiveNow reported inactive agent as active")
			}
		})
	}
}

func TestStopReleasesAndStopsLoop(t *testing.T) {
	for _, mode := range []string{config.SleepModeAgent, config.SleepModeAlways} {
		t.Run(mode, func(t *testing.T) {
			cfg := newTestConfig(time.Hour, time.Minute)
			cfg.Apply(config.Mutable{SleepMode: mode, PreventSystemSleep: true})
			act := &fakeActivity{act: Activity{Active: true}}
			inh := &fakeInhibitor{}
			c := newTestController(cfg, act, inh, &fakeSleeper{}, fakeIdler{})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Start(ctx)
			// Give the first tick a moment to hold the machine awake.
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) && !c.Status().KeepingAwake {
				time.Sleep(5 * time.Millisecond)
			}
			if !c.Status().KeepingAwake {
				t.Fatalf("loop did not hold the machine awake")
			}

			c.Stop()
			if c.Status().KeepingAwake {
				t.Errorf("still holding after Stop")
			}
			if inh.released == 0 {
				t.Errorf("inhibitor was never released on Stop")
			}
		})
	}
}

func TestAlwaysHoldsWithoutAgentActivity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		system    bool
		supported bool
		detail    string
	}{
		{"system", true, true, "always"},
		{"empty scopes", false, true, "always"},
		{"unsupported", true, false, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newTestConfig(time.Millisecond, time.Minute)
			cfg.Apply(config.Mutable{SleepMode: config.SleepModeAlways, PreventSystemSleep: tc.system})
			inh := &fakeInhibitor{}
			slp := &fakeSleeper{}
			c := New(Deps{Cfg: cfg, Inhibitor: inh, Sleeper: slp, Supported: tc.supported})
			c.seenActive = true
			c.idleSince = time.Now().Add(-time.Hour)
			c.tickOnce(context.Background())
			c.tickOnce(context.Background())
			st := c.Status()
			wantHeld := tc.system && tc.supported
			if st.Mode != config.SleepModeAlways || st.Detail != tc.detail || st.KeepingAwake != wantHeld || st.SleepAtMs != 0 || slp.sleeps != 0 {
				t.Fatalf("status = %+v, sleeps = %d", st, slp.sleeps)
			}
			if wantHeld {
				if len(inh.applied) != 1 || inh.applied[0] != (InhibitSpec{System: true}) || st.Held != (HeldSpec{System: true}) {
					t.Fatalf("applied = %+v, held = %+v", inh.applied, st.Held)
				}
			} else if len(inh.applied) != 0 || st.Held != (HeldSpec{}) {
				t.Fatalf("unexpected inhibition: %+v", st)
			}
			if tc.supported && (c.seenActive || !c.idleSince.IsZero()) {
				t.Fatal("always retained agent history")
			}
		})
	}
}

func TestSleepModeTransitions(t *testing.T) {
	for _, mode := range []string{config.SleepModeOff, config.SleepModeAlways} {
		t.Run(mode, func(t *testing.T) {
			cfg := newTestConfig(time.Millisecond, time.Minute)
			act := &fakeActivity{act: Activity{Active: true}}
			inh := &fakeInhibitor{}
			slp := &fakeSleeper{}
			c := newTestController(cfg, act, inh, slp, fakeIdler{idle: time.Hour, ok: true})
			c.tickOnce(context.Background())
			act.act = Activity{}
			c.tickOnce(context.Background())
			if c.Status().Detail != "grace" || c.Status().SleepAtMs == 0 {
				t.Fatalf("expected initial grace: %+v", c.Status())
			}
			c.idleSince = time.Now().Add(-time.Hour)
			cfg.Apply(config.Mutable{SleepMode: mode, PreventSystemSleep: true})
			c.tickOnce(context.Background())
			if c.seenActive || !c.idleSince.IsZero() || c.Status().SleepAtMs != 0 {
				t.Fatalf("mode %s retained history: %+v", mode, c.Status())
			}
			cfg.Apply(config.Mutable{SleepMode: config.SleepModeAgent, PreventSystemSleep: true})
			c.tickOnce(context.Background())
			if c.Status().Detail != "idle" || c.Status().KeepingAwake || c.Status().SleepAtMs != 0 || slp.sleeps != 0 {
				t.Fatalf("old deadline revived: %+v, sleeps = %d", c.Status(), slp.sleeps)
			}
			act.act = Activity{Active: true}
			c.tickOnce(context.Background())
			if !c.Status().KeepingAwake || c.Status().Detail != "active" {
				t.Fatalf("new activity not held: %+v", c.Status())
			}
		})
	}
}

func TestAlwaysReportsHeldAndRetriesErrors(t *testing.T) {
	cfg := newTestConfig(time.Hour, time.Minute)
	cfg.Apply(config.Mutable{SleepMode: config.SleepModeAlways, PreventSystemSleep: true})
	inh := &fakeInhibitor{applyErr: errors.New("apply failed")}
	c := New(Deps{Cfg: cfg, Inhibitor: inh, Supported: true})
	c.tickOnce(context.Background())
	if st := c.Status(); st.KeepingAwake || st.Held != (HeldSpec{}) || st.Error != "apply failed" {
		t.Fatalf("failed assertion reported held: %+v", st)
	}
	inh.applyErr = nil
	c.tickOnce(context.Background())
	if st := c.Status(); !st.KeepingAwake || st.Error != "" || len(inh.applied) != 2 {
		t.Fatalf("apply not retried: %+v", st)
	}
	inh.releaseErr = errors.New("release failed")
	cfg.Apply(config.Mutable{SleepMode: config.SleepModeAlways})
	c.tickOnce(context.Background())
	if st := c.Status(); !st.KeepingAwake || st.Error != "release failed" || st.SleepAtMs != 0 {
		t.Fatalf("release failure lost actual held state: %+v", st)
	}
	inh.releaseErr = nil
	c.tickOnce(context.Background())
	if st := c.Status(); st.KeepingAwake || st.Error != "" || inh.released != 2 {
		t.Fatalf("release not retried: %+v", st)
	}
}

func TestEmptyInternalSleepModeIsOff(t *testing.T) {
	c := New(Deps{Cfg: &config.Config{}, Inhibitor: &fakeInhibitor{}, Supported: true})
	c.seenActive = true
	c.idleSince = time.Now().Add(-time.Hour)
	c.tickOnce(context.Background())
	if st := c.Status(); st.Mode != config.SleepModeOff || st.Detail != "disabled" || st.SleepAtMs != 0 || c.seenActive || !c.idleSince.IsZero() {
		t.Fatalf("empty mode not disabled: %+v", st)
	}
}
