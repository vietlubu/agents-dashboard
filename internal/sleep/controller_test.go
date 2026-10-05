package sleep

import (
	"context"
	"testing"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

type fakeInhibitor struct {
	applied  []InhibitSpec
	released int
}

func (f *fakeInhibitor) Apply(s InhibitSpec) error {
	f.applied = append(f.applied, s)
	return nil
}

func (f *fakeInhibitor) Release() error {
	f.released++
	return nil
}

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

type fakeActivity struct{ act Activity }

func (f *fakeActivity) Observe(context.Context, time.Duration) (Activity, error) { return f.act, nil }

// newTestConfig builds a config with the sleep feature enabled and the provided delay.
func newTestConfig(after, activeWindow time.Duration) *config.Config {
	cfg := &config.Config{}
	cfg.Apply(config.Mutable{
		SleepEnabled:          true,
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

	cfg.Apply(config.Mutable{SleepEnabled: false, PreventSystemSleep: true, PreventDisplaySleep: true})
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
	cfg := newTestConfig(time.Hour, time.Minute)
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

	if err := c.DisplaySleepNow(); err != nil || disp.calls != 1 {
		t.Errorf("DisplaySleepNow err = %v, calls = %d", err, disp.calls)
	}
	if err := c.ScreensaverNow(); err != nil || scr.calls != 1 {
		t.Errorf("ScreensaverNow err = %v, calls = %d", err, scr.calls)
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

func TestActiveNowIgnoresEnabledFlag(t *testing.T) {
	cfg := newTestConfig(time.Hour, time.Minute)
	cfg.Apply(config.Mutable{SleepEnabled: false})
	act := &fakeActivity{act: Activity{Active: true}}
	c := New(Deps{
		Cfg: cfg, Activity: act, Inhibitor: &fakeInhibitor{}, Sleeper: &fakeSleeper{},
		Idler: fakeIdler{}, Supported: true, Tick: time.Hour,
	})

	if !c.ActiveNow(context.Background()) {
		t.Errorf("ActiveNow = false while an agent is active and the feature is off, want true")
	}
}

func TestStopReleasesAndStopsLoop(t *testing.T) {
	cfg := newTestConfig(time.Hour, time.Minute)
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
}
