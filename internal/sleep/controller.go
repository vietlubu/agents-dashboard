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
	Supported      bool
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
	displaySleeper DisplaySleeper
	screensaver    ScreensaverStarter
	idler          Idler
	supported      bool
	log            *slog.Logger
	onStatus       func(Status)
	tick           time.Duration

	mu         sync.Mutex
	status     Status
	held       InhibitSpec
	idleSince  time.Time
	seenActive bool
	// A committed non-agent mode must invalidate history even if kicks coalesce.
	resetHistory bool
	clamshell    bool
	// clamshellFailed latches after a failed restore so a declined administrator prompt
	// is not repeated on every tick.
	clamshellFailed bool
	lastErr         string

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
		supported:      d.Supported,
		log:            log,
		onStatus:       d.OnStatus,
		tick:           tick,
		status:         Status{Mode: config.SleepModeOff, Supported: d.Supported},
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
		kick:           make(chan struct{}, 1),
	}
	if cc, ok := d.Inhibitor.(ClamshellController); ok {
		c.clamshell = cc.ClamshellActive()
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
	c.setHeld(InhibitSpec{})

	// Reflect the release in the published status: nothing else will run afterwards.
	c.mu.Lock()
	st := c.status
	c.mu.Unlock()
	st.Held = HeldSpec{}
	st.KeepingAwake = false
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

// ClamshellSupported reports whether the platform can keep the machine running with the
// lid closed beyond AC power.
func (c *Controller) ClamshellSupported() bool {
	_, ok := c.inhibitor.(ClamshellController)
	return ok
}

// RequestClamshell asks the platform to keep the machine running with the lid closed. On
// macOS this raises one administrator prompt; the caller decides whether to offer it.
func (c *Controller) RequestClamshell() error {
	cc, ok := c.inhibitor.(ClamshellController)
	if !ok {
		return errUnsupported
	}
	if err := cc.RequestClamshell(); err != nil {
		return err
	}
	c.mu.Lock()
	c.clamshell = true
	c.clamshellFailed = false
	c.mu.Unlock()
	c.Kick()
	return nil
}

// RestoreClamshell undoes RequestClamshell, restoring the system's own lid behaviour.
func (c *Controller) RestoreClamshell() error {
	cc, ok := c.inhibitor.(ClamshellController)
	if !ok {
		return errUnsupported
	}
	if err := cc.RestoreClamshell(); err != nil {
		return err
	}
	c.mu.Lock()
	c.clamshell = false
	c.clamshellFailed = false
	c.mu.Unlock()
	c.Kick()
	return nil
}

// SleepNow puts the machine to sleep immediately, regardless of the automatic state. Any
// keep-awake assertion is released first so a blocking inhibitor cannot veto the explicit
// request; the next tick re-applies it according to the selected mode.
func (c *Controller) SleepNow() error {
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
	c.setHeld(InhibitSpec{})
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
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
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
	snap := c.cfg.Snapshot()
	mode := snap.SleepMode
	if !config.ValidSleepMode(mode) {
		mode = config.SleepModeOff
	}
	c.syncClamshell(mode != config.SleepModeOff && snap.PreventLidClosedSleep)
	st := Status{
		Mode:      mode,
		Supported: c.supported,
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
		Lid:     snap.PreventLidClosedSleep,
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

	c.setHeld(InhibitSpec{})
	st.Detail = "sleeping"
	c.publish(st)
	if err := c.sleeper.Sleep(); err != nil {
		c.log.Warn("put machine to sleep", "error", err)
		c.mu.Lock()
		c.lastErr = err.Error()
		// Re-arm the countdown so a failed sleep is retried after another full delay
		// rather than every tick.
		c.idleSince = time.Now()
		c.mu.Unlock()
	}
}

// syncClamshell clears the machine-wide clamshell flag once the setting that asked for it is
// off. Without this the flag outlives the setting and blocks every sleep, including the
// user's own.
func (c *Controller) syncClamshell(enabled bool) {
	if !c.clamshellActive() || enabled || c.clamshellFailed {
		return
	}
	if err := c.RestoreClamshell(); err != nil {
		c.log.Warn("restore clamshell", "error", err)
		c.mu.Lock()
		c.clamshellFailed = true
		c.mu.Unlock()
	}
}

func (c *Controller) clamshellActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clamshell
}

// setHeld reconciles the OS with spec, remembering what was asked for so a repeated tick
// is a no-op.
func (c *Controller) setHeld(spec InhibitSpec) {
	c.mu.Lock()
	if c.held == spec {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	var err error
	if spec.Empty() {
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
}

func (c *Controller) publish(st Status) {
	c.mu.Lock()
	st.Held = HeldSpec{System: c.held.System, Display: c.held.Display, Lid: c.held.Lid}
	st.KeepingAwake = !c.held.Empty()
	st.Clamshell = c.clamshell
	st.Error = c.lastErr
	changed := c.status != st
	c.status = st
	onStatus := c.onStatus
	c.mu.Unlock()
	if changed && onStatus != nil {
		onStatus(st)
	}
}
