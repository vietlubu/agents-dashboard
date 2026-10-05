// Package sleep keeps the machine awake while a coding-agent session is working and puts
// it to sleep once every session has stopped and the user has stepped away.
//
// Two signals decide "a session is active": an agent process is running AND its session
// file or database was written recently. Requiring both means an editor left open on an
// idle session does not hold the machine awake forever.
package sleep

import (
	"context"
	"errors"
	"time"
)

// errUnsupported is returned when a platform-specific capability is not available.
var errUnsupported = errors.New("not supported on this platform")

// InhibitSpec is the set of sleeps to hold off. Each field maps to one menu toggle.
type InhibitSpec struct {
	System  bool // prevent idle system sleep
	Display bool // prevent display sleep
	Lid     bool // prevent system sleep when the lid is closed
}

// Empty reports whether nothing is being held.
func (s InhibitSpec) Empty() bool { return !s.System && !s.Display && !s.Lid }

// HeldSpec is the JSON view of the currently held assertions.
type HeldSpec struct {
	System  bool `json:"system"`
	Display bool `json:"display"`
	Lid     bool `json:"lid"`
}

// Inhibitor reconciles the operating system's keep-awake assertions with a spec. Apply is
// idempotent: called with the spec already in force it makes no change.
type Inhibitor interface {
	Apply(spec InhibitSpec) error
	Release() error
}

// ClamshellController is implemented by inhibitors that can keep the machine running with
// the lid closed beyond AC power. It is optional and platform-specific (macOS pmset).
type ClamshellController interface {
	RequestClamshell() error
	RestoreClamshell() error
	ClamshellActive() bool
}

// Sleeper puts the machine to sleep for real.
type Sleeper interface {
	Sleep() error
}

// DisplaySleeper turns the display off now without putting the machine to sleep. The
// display wakes again on the next keypress or pointer move.
type DisplaySleeper interface {
	DisplaySleep() error
}

// ScreensaverStarter switches the session to the screensaver now.
type ScreensaverStarter interface {
	StartScreensaver() error
}

// Idler reports how long the user has been idle and whether it could be measured. A false
// ok means the platform cannot tell, and the controller then declines to force sleep so it
// never suspends a machine someone is using.
type Idler interface {
	Idle() (time.Duration, bool)
}

// ProcessLister lists the command names of the running processes it can see. It is only
// used to tell whether a coding agent is still running.
type ProcessLister interface {
	Running() ([]string, error)
}

// Activity is one observation of agent activity.
type Activity struct {
	LastWriteMs    int64
	AgentRunning   bool
	ActiveSessions int64
	Active         bool
	Processes      []string
}

// ActivityProvider collapses the process and file/database signals into one observation.
type ActivityProvider interface {
	Observe(ctx context.Context, window time.Duration) (Activity, error)
}

// Status is the controller's externally visible state. Every field is JSON-safe and
// comparable, so the service can hand it straight to the frontend and the controller can
// cheaply detect a change before emitting an event.
type Status struct {
	Enabled        bool     `json:"enabled"`
	Supported      bool     `json:"supported"`
	KeepingAwake   bool     `json:"keepingAwake"`
	Active         bool     `json:"active"`
	ActiveSessions int64    `json:"activeSessions"`
	Agents         string   `json:"agents"`
	LastWriteMs    int64    `json:"lastWriteMs"`
	IdleMs         int64    `json:"idleMs"`
	SleepAtMs      int64    `json:"sleepAtMs"`
	Held           HeldSpec `json:"held"`
	Clamshell      bool     `json:"clamshell"`
	// Detail names the current phase: disabled, unsupported, active, grace,
	// waiting-user, sleeping or idle. It is meant for a status line, not for logic.
	Detail string `json:"detail"`
	Error  string `json:"error"`
}
