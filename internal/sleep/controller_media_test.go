package sleep

import (
	"context"
	"testing"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

type fakeMedia struct {
	media Media
	ok    bool
	calls int
}

func (f *fakeMedia) MediaPlaying() (Media, bool) {
	f.calls++
	return f.media, f.ok
}

// mediaDelay is long enough that the grace period never elapses by accident, so each tick
// below is a deliberate step of the state machine rather than a race against the clock.
const mediaDelay = 50 * time.Millisecond

// newMediaController builds an agent-mode controller whose countdown has already elapsed
// after one active observation, so a single tick decides whether to sleep.
func newMediaController(t *testing.T, media *fakeMedia) (*Controller, *fakeSleeper, *fakeInhibitor) {
	t.Helper()
	cfg := newTestConfig(mediaDelay, time.Millisecond)
	act := &fakeActivity{act: Activity{Active: true}}
	inh := &fakeInhibitor{}
	slp := &fakeSleeper{}
	c := New(Deps{
		Cfg: cfg, Activity: act, Inhibitor: inh, Sleeper: slp,
		Idler: fakeIdler{idle: time.Hour, ok: true}, Media: media, Supported: true, Tick: time.Hour,
	})
	// The agent works, then stops, and the machine sits idle past the delay.
	c.tickOnce(context.Background())
	act.act = Activity{Active: false}
	c.tickOnce(context.Background())
	time.Sleep(2 * mediaDelay)
	return c, slp, inh
}

func TestMediaPlaybackDefersSleep(t *testing.T) {
	media := &fakeMedia{media: Media{Playing: true, Sources: []string{"audio output"}}, ok: true}
	c, slp, inh := newMediaController(t, media)

	c.tickOnce(context.Background())

	st := c.Status()
	if slp.sleeps != 0 {
		t.Fatalf("sleeper called %d times while media was playing", slp.sleeps)
	}
	if st.Detail != "media" || !st.Media || st.MediaSource != "audio output" {
		t.Fatalf("status = %+v, want detail media with its source", st)
	}
	if st.KeepingAwake {
		t.Errorf("the dashboard kept its own assertion while media was playing")
	}
	if inh.released == 0 {
		t.Errorf("media playback did not release the keep-awake assertion")
	}

	// Playback ends: the countdown restarts, so the machine sleeps a full delay later
	// rather than instantly.
	media.media = Media{}
	c.tickOnce(context.Background())
	if st := c.Status(); st.Detail != "grace" || st.Media || slp.sleeps != 0 {
		t.Fatalf("status after playback stopped = %+v, sleeps=%d, want a fresh grace period", st, slp.sleeps)
	}
	time.Sleep(2 * mediaDelay)
	c.tickOnce(context.Background())
	if slp.sleeps != 1 {
		t.Fatalf("sleeper called %d times, want 1 after the restart elapsed", slp.sleeps)
	}
}

func TestMediaPlayingWithoutSources(t *testing.T) {
	// A watcher that can see media but reports none playing must not defer anything.
	media := &fakeMedia{ok: true}
	c, slp, _ := newMediaController(t, media)
	c.tickOnce(context.Background())
	if slp.sleeps != 1 {
		t.Fatalf("sleeper called %d times, want 1 when nothing is playing", slp.sleeps)
	}
	if st := c.Status(); st.Media || st.Detail != "sleeping" {
		t.Fatalf("status = %+v, want sleeping", st)
	}
}

func TestMediaUnmeasurableKeepsPreviousBehaviour(t *testing.T) {
	// ok=false means the platform cannot see playback; deferring sleep forever would be
	// worse than the original bug, so the machine still sleeps.
	media := &fakeMedia{ok: false}
	c, slp, _ := newMediaController(t, media)
	c.tickOnce(context.Background())
	if slp.sleeps != 1 {
		t.Fatalf("sleeper called %d times, want 1 when media is unmeasurable", slp.sleeps)
	}
	if st := c.Status(); st.Media {
		t.Errorf("unmeasurable media reported as playing: %+v", st)
	}
}

func TestMediaIgnoredWhenDisabled(t *testing.T) {
	media := &fakeMedia{media: Media{Playing: true, Sources: []string{"audio output"}}, ok: true}
	c, slp, _ := newMediaController(t, media)
	snap := c.cfg.Snapshot()
	snap.WaitForMedia = false
	c.cfg.Apply(snap)

	c.tickOnce(context.Background())

	if slp.sleeps != 1 {
		t.Fatalf("sleeper called %d times, want 1 when sleep_wait_for_media is off", slp.sleeps)
	}
	if media.calls != 0 {
		t.Errorf("media probed %d times while the setting was off", media.calls)
	}
}

// The probe runs only where the decision is made: while the user is demonstrably active
// there is nothing to defer, and spawning a helper process every tick would be waste.
func TestMediaProbeOnlyRunsAtTheSleepDecision(t *testing.T) {
	media := &fakeMedia{media: Media{Playing: true}, ok: true}
	cfg := newTestConfig(mediaDelay, time.Millisecond)
	act := &fakeActivity{act: Activity{Active: true}}
	c := New(Deps{
		Cfg: cfg, Activity: act, Inhibitor: &fakeInhibitor{}, Sleeper: &fakeSleeper{},
		Idler: fakeIdler{idle: 0, ok: true}, Media: media, Supported: true, Tick: time.Hour,
	})
	c.tickOnce(context.Background())
	act.act = Activity{Active: false}
	c.tickOnce(context.Background())
	time.Sleep(2 * mediaDelay)
	c.tickOnce(context.Background())

	if st := c.Status(); st.Detail != "waiting-user" {
		t.Fatalf("status = %+v, want waiting-user", st)
	}
	if media.calls != 0 {
		t.Errorf("media probed %d times while the user was active", media.calls)
	}
}

// An explicit Sleep now from the tray is a user decision: media playback must not veto it.
func TestExplicitSleepNowIgnoresMedia(t *testing.T) {
	media := &fakeMedia{media: Media{Playing: true, Sources: []string{"audio output"}}, ok: true}
	cfg := newTestConfig(time.Hour, time.Minute)
	slp := &fakeSleeper{}
	c := New(Deps{
		Cfg: cfg, Activity: &fakeActivity{act: Activity{Active: true}}, Inhibitor: &fakeInhibitor{},
		Sleeper: slp, Idler: fakeIdler{idle: time.Hour, ok: true}, Media: media, Supported: true, Tick: time.Hour,
	})
	if err := c.SleepNow(); err != nil {
		t.Fatalf("SleepNow: %v", err)
	}
	if slp.sleeps != 1 {
		t.Fatalf("sleeper called %d times, want 1", slp.sleeps)
	}
}

// Always mode never sleeps on its own, so media must not change it.
func TestMediaDoesNotAffectAlwaysMode(t *testing.T) {
	media := &fakeMedia{media: Media{Playing: true}, ok: true}
	cfg := newTestConfig(mediaDelay, time.Millisecond)
	cfg.Apply(config.Mutable{
		SleepMode: config.SleepModeAlways, PreventSystemSleep: true, PreventDisplaySleep: true,
		WaitForMedia: true,
	})
	inh := &fakeInhibitor{}
	slp := &fakeSleeper{}
	c := New(Deps{
		Cfg: cfg, Activity: &fakeActivity{}, Inhibitor: inh, Sleeper: slp,
		Idler: fakeIdler{idle: time.Hour, ok: true}, Media: media, Supported: true, Tick: time.Hour,
	})
	c.tickOnce(context.Background())
	if st := c.Status(); st.Detail != "always" || !st.KeepingAwake || slp.sleeps != 0 {
		t.Fatalf("status = %+v, sleeps = %d", st, slp.sleeps)
	}
	if media.calls != 0 {
		t.Errorf("media probed %d times in always mode", media.calls)
	}
}
