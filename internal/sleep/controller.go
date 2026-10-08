package sleep

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

// DefaultTick is how often the controller reconciles the OS state. Short enough that an
// agent that starts working is noticed quickly, long enough that the per-tick process
// listing is negligible.
const DefaultTick = 15 * time.Second

// Deps is what the controller needs. The interfaces are injected so the state machine can
// be exercised with fakes, and so a server build can run it with no-ops.
type Deps struct {
	Cfg            *config.Config
	Activity       ActivityProvider
	Inhibitor      Inhibitor
	Sleeper        Sleeper
	DisplaySleeper DisplaySleeper
	Screensaver    ScreensaverStarter
	Idler          Idler
	Media          MediaWatcher
	Supported      bool
	LidSupported   bool
	Log            *slog.Logger
	OnStatus       func(Status)
	Tick           time.Duration
}

// Controller owns the keep-awake state machine.
//
// It is deliberately decoupled from the sync loop. Agent mode uses session activity
// and the process list; always mode holds the selected scopes without observing agents.
type Controller struct {
	cfg            *config.Config
	activity       ActivityProvider
	inhibitor      Inhibitor
	sleeper        Sleeper
	lidObserver    LidObserver
	displaySleeper DisplaySleeper
	screensaver    ScreensaverStarter
	idler          Idler
	media          MediaWatcher
	supported      bool
	lidSupported   bool
	log            *slog.Logger
	onStatus       func(Status)
	tick           time.Duration

	// Reconcile and explicit Sleep must not race OS release/reapply operations.
	operationMu sync.Mutex
	mu          sync.Mutex
	status      Status
	held        InhibitSpec
	idleSince   time.Time
	seenActive  bool
	// A committed non-agent mode must invalidate history even if kicks coalesce.
	resetHistory bool
	clamshell    bool
	clamshellErr string
	lastErr      string

	stop     chan struct{}
	done     chan struct{}
	kick     chan struct{}
	stopOnce sync.Once
}

// New builds a controller.
func New(d Deps) *Controller {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	tick := d.Tick
	if tick <= 0 {
		tick = DefaultTick
	}
	c := &Controller{
		cfg:            d.Cfg,
		activity:       d.Activity,
		inhibitor:      d.Inhibitor,
		sleeper:        d.Sleeper,
		displaySleeper: d.DisplaySleeper,
		screensaver:    d.Screensaver,
		idler:          d.Idler,
		media:          d.Media,
		supported:      d.Supported,
		lidSupported:   d.LidSupported,
		log:            log,
		onStatus:       d.OnStatus,
		tick:           tick,
		status:         Status{Mode: config.SleepModeOff, Supported: d.Supported, LidSupported: d.LidSupported},
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
		kick:           make(chan struct{}, 1),
	}
	if cc, ok := d.Inhibitor.(ClamshellController); ok {
		c.clamshell = cc.ClamshellActive()
		c.status.Clamshell = c.clamshell
	}
	if observer, ok := d.Inhibitor.(LidObserver); ok {
		c.lidObserver = observer
		c.status.LidControl = observer.LidState()
		observer.SetLidNotifier(c.Kick)
	}
	return c
}

// Start launches the loop and returns immediately. The first tick runs at once so the
// status is correct as soon as the app is up.
func (c *Controller) Start(ctx context.Context) {
	go c.loop(ctx)
}

// Stop ends the loop, waits for it, and releases every assertion so the machine is left in
// the state the user's own power settings describe.
func (c *Controller) Stop() {
	c.stopOnce.Do(func() { close(c.stop) })
	<-c.done
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	if c.lidObserver != nil {
		c.lidObserver.SetLidNotifier(nil)
	}
	c.setHeld(InhibitSpec{})

	// publish samples actual state, including a failed native lid release.
	c.mu.Lock()
	st := c.status
	c.mu.Unlock()
	st.SleepAtMs = 0
	c.publish(st)
}

// Kick asks for an immediate reconcile, used when a setting or tray toggle changes.
func (c *Controller) Kick() {
	if c.cfg.Snapshot().SleepMode != config.SleepModeAgent {
		c.mu.Lock()
		c.resetHistory = true
		c.mu.Unlock()
	}
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Supported reports whether this platform can control sleep at all.
func (c *Controller) Supported() bool { return c.supported }

// RestoreClamshell explicitly clears a legacy machine-wide sleep-disabled flag.
func (c *Controller) RestoreClamshell() error {
	cc, ok := c.inhibitor.(ClamshellController)
	if !ok {
		return errUnsupported
	}
	if err := cc.RestoreClamshell(); err != nil {
		c.mu.Lock()
		c.clamshellErr = err.Error()
		c.mu.Unlock()
		c.Kick()
		return err
	}
	c.mu.Lock()
	c.clamshell = false
	c.clamshellErr = ""
	c.mu.Unlock()
	c.Kick()
	return nil
}

// SleepNow puts the machine to sleep immediately, regardless of the automatic state. Any
// keep-awake assertion is released first so a blocking inhibitor cannot veto the explicit
// request; the next tick re-applies it according to the selected mode.
func (c *Controller) SleepNow() error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	if c.sleeper == nil {
		return errUnsupported
	}
	// The clamshell flag blocks every sleep request, the user's own included, so it has to
	// go before an explicit sleep can take effect.
	if c.clamshellActive() {
		if err := c.RestoreClamshell(); err != nil {
			return err
		}
	}
	if err := c.setHeld(InhibitSpec{}); err != nil {
		return err
	}
	return c.sleeper.Sleep()
}

// DisplaySleepNow turns the display off now without suspending the machine.
func (c *Controller) DisplaySleepNow() error {
	if c.displaySleeper == nil {
		return errUnsupported
	}
	return c.displaySleeper.DisplaySleep()
}

// ScreensaverNow switches the session to the screensaver now.
func (c *Controller) ScreensaverNow() error {
	if c.screensaver == nil {
		return errUnsupported
	}
	return c.screensaver.StartScreensaver()
}

// ActiveNow reports whether an agent is working right now, independently of the sleep
// prevention mode. The menu bar uses it to decide whether an explicit Sleep now needs
// confirmation before it interrupts a running session.
func (c *Controller) ActiveNow(ctx context.Context) bool {
	if c.activity == nil {
		return false
	}
	act, err := c.activity.Observe(ctx, c.cfg.Snapshot().SleepActiveWindow)
	if err != nil {
		return false
	}
	return act.Active
}

// Status returns a copy of the current state.
func (c *Controller) Status() Status {
	lid := LidState{}
	if c.lidObserver != nil {
		lid = c.lidObserver.LidState()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked(c.status, lid)
}

func (c *Controller) loop(ctx context.Context) {
	defer close(c.done)
	c.tickOnce(ctx)
	timer := time.NewTicker(c.tick)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case <-timer.C:
			c.tickOnce(ctx)
		case <-c.kick:
			c.tickOnce(ctx)
		}
	}
}

func (c *Controller) tickOnce(ctx context.Context) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	snap := c.cfg.Snapshot()
	mode := snap.SleepMode
	if !config.ValidSleepMode(mode) {
		mode = config.SleepModeOff
	}
	st := Status{
		Mode:         mode,
		Supported:    c.supported,
		LidSupported: c.lidSupported,
	}

	if !c.supported {
		c.setHeld(InhibitSpec{})
		st.Detail = "unsupported"
		c.publish(st)
		return
	}
	if mode == config.SleepModeOff {
		c.setHeld(InhibitSpec{})
		c.mu.Lock()
		c.seenActive = false
		c.idleSince = time.Time{}
		c.resetHistory = false
		c.mu.Unlock()
		st.Detail = "disabled"
		c.publish(st)
		return
	}

	spec := InhibitSpec{
		System:  snap.PreventSystemSleep,
		Display: snap.PreventDisplaySleep,
		Lid:     snap.PreventLidClosedSleep && c.lidSupported,
	}
	if mode == config.SleepModeAlways {
		c.mu.Lock()
		c.seenActive = false
		c.idleSince = time.Time{}
		c.resetHistory = false
		c.mu.Unlock()
		c.setHeld(spec)
		st.Detail = "always"
		c.publish(st)
		return
	}

	act, err := c.activity.Observe(ctx, snap.SleepActiveWindow)
	if err != nil {
		c.log.Warn("observe agent activity", "error", err)
	}
	c.mu.Lock()
	if c.resetHistory {
		c.seenActive = false
		c.idleSince = time.Time{}
		c.resetHistory = false
		c.mu.Unlock()
		// This observation started before the committed policy change. The queued kick
		// will observe again; stale activity must not seed a new countdown.
		st.Detail = "idle"
		c.publish(st)
		return
	}
	st.Active = act.Active
	st.ActiveSessions = act.ActiveSessions
	st.LastWriteMs = act.LastWriteMs
	st.Agents = strings.Join(act.Processes, ", ")

	if act.Active {
		c.seenActive = true
		c.idleSince = time.Time{}
		c.mu.Unlock()
		c.setHeld(spec)
		st.Detail = "active"
		c.publish(st)
		return
	}

	seen := c.seenActive
	if seen && c.idleSince.IsZero() {
		c.idleSince = time.Now()
	}
	idleSince := c.idleSince
	c.mu.Unlock()

	if !seen {
		// Nothing has run yet: stay out of the way instead of sleeping a machine that
		// never had an agent working, and hold nothing.
		c.setHeld(InhibitSpec{})
		st.Detail = "idle"
		c.publish(st)
		return
	}

	due := idleSince.Add(snap.SleepAfter)
	if time.Now().Before(due) {
		// Grace period: keep the assertions so the OS cannot sleep before the delay the
		// user configured.
		c.setHeld(spec)
		st.SleepAtMs = due.UnixMilli()
		st.Detail = "grace"
		c.publish(st)
		return
	}

	// Delay elapsed. Only force sleep once the user has stepped away too.
	idle, ok := c.idler.Idle()
	st.IdleMs = idle.Milliseconds()
	if !ok || idle < snap.SleepAfter {
		c.setHeld(InhibitSpec{})
		st.SleepAtMs = due.UnixMilli()
		st.Detail = "waiting-user"
		c.publish(st)
		return
	}

	// Input idle is not the same as being away: media playback produces no input at all,
	// so the idle timer runs while the user is watching. Step back instead of cutting a
	// video short, and restart the countdown so the machine sleeps a full delay after
	// playback ends. The dashboard's own assertions are released, leaving the playing app
	// and the operating system's power settings in charge of keeping the machine up.
	if snap.WaitForMedia && c.media != nil {
		if m, measured := c.media.MediaPlaying(); measured && m.Playing {
			c.mu.Lock()
			c.idleSince = time.Now()
			c.mu.Unlock()
			c.setHeld(InhibitSpec{})
			st.Media = true
			st.MediaSource = strings.Join(m.Sources, ", ")
			st.Detail = "media"
			c.publish(st)
			return
		}
	}

	if err := c.setHeld(InhibitSpec{}); err != nil {
		st.Detail = "blocked"
		c.publish(st)
		return
	}
	st.Detail = "sleeping"
	c.publish(st)
	if err := c.sleeper.Sleep(); err != nil {
		c.log.Warn("put machine to sleep", "error", err)
		c.mu.Lock()
		c.lastErr = err.Error()
		// Re-arm the countdown so a failed sleep is retried after another full delay
		// rather than every tick.
		c.idleSince = time.Now()
		st.SleepAtMs = c.idleSince.Add(snap.SleepAfter).UnixMilli()
		c.mu.Unlock()
		st.Detail = "grace"
		c.publish(st)
	}
}

func (c *Controller) clamshellActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clamshell
}

// setHeld remembers successful requests; native lid policy is observed again even when
// the requested spec is unchanged, because powerd can overwrite the shared mask.
func (c *Controller) setHeld(spec InhibitSpec) error {
	c.mu.Lock()
	unchanged := c.held == spec
	c.mu.Unlock()
	// Native policy may drift, and Release must also clean a partially applied request.
	if unchanged && !spec.Empty() && c.lidObserver == nil {
		return nil
	}

	var err error
	if c.inhibitor == nil {
		if spec.Empty() {
			return nil
		}
		err = errUnsupported
	} else if spec.Empty() {
		err = c.inhibitor.Release()
	} else {
		err = c.inhibitor.Apply(spec)
	}

	c.mu.Lock()
	if err != nil {
		// Keep the previous held state so the next tick retries instead of silently
		// believing the assertion is in force.
		c.lastErr = err.Error()
	} else {
		c.held = spec
		c.lastErr = ""
	}
	c.mu.Unlock()
	if err != nil {
		c.log.Warn("sleep inhibitor", "error", err)
	}
	return err
}

func (c *Controller) publish(st Status) {
	lid := LidState{}
	if c.lidObserver != nil {
		lid = c.lidObserver.LidState()
	}
	c.mu.Lock()
	st = c.statusLocked(st, lid)
	changed := c.status != st
	c.status = st
	onStatus := c.onStatus
	c.mu.Unlock()
	if changed && onStatus != nil {
		onStatus(st)
	}
}

// statusLocked gives event payloads and RPC reads the same actual-state semantics.
func (c *Controller) statusLocked(st Status, lid LidState) Status {
	st.Held = HeldSpec{System: c.held.System, Display: c.held.Display, Lid: c.held.Lid}
	st.LidControl = lid
	if lid.PrivateAPI {
		st.Held.Lid = lid.Requested && lid.Known && lid.Effective
	}
	st.KeepingAwake = st.Held.System || st.Held.Display || st.Held.Lid
	st.Clamshell = c.clamshell
	st.Error = c.lastErr
	if c.clamshellErr != "" {
		st.Error = c.clamshellErr
	}
	return st
}
