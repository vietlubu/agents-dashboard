// Package sleep applies selected sleep-prevention scopes continuously or while a coding
// agent is active. Agent mode sleeps after sessions stop and the user has stepped away.
//
// Agent activity requires both a running process and a recently written session file or
// database, so an editor left open on an idle session does not hold the machine awake.
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

// LidState separates a successful private lid request from the observed shared policy.
// Known=false means the effective policy could not be read, not that sleep is blocked.
type LidState struct {
	PrivateAPI bool `json:"privateApi"`
	Requested  bool `json:"requested"`
	Known      bool `json:"known"`
	Effective  bool `json:"effective"`
}

// LidObserver lets the controller reconcile native policy changes without polling the UI.
// Implementations invoke the notifier outside their own locks and clear it on shutdown.
type LidObserver interface {
	LidState() LidState
	SetLidNotifier(func())
}

// ClamshellController detects and explicitly restores a legacy machine-wide sleep flag.
// It never enables lid prevention; normal assertions must not alter system policy.
type ClamshellController interface {
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
	Mode           string   `json:"mode"`
	Supported      bool     `json:"supported"`
	KeepingAwake   bool     `json:"keepingAwake"`
	LidSupported   bool     `json:"lidSupported"`
	LidControl     LidState `json:"lidControl"`
	Active         bool     `json:"active"`
	ActiveSessions int64    `json:"activeSessions"`
	Agents         string   `json:"agents"`
	LastWriteMs    int64    `json:"lastWriteMs"`
	IdleMs         int64    `json:"idleMs"`
	SleepAtMs      int64    `json:"sleepAtMs"`
	Held           HeldSpec `json:"held"`
	Clamshell      bool     `json:"clamshell"`
	// Detail names the current phase: disabled, unsupported, always, active, grace,
	// waiting-user, sleeping, blocked or idle. It is meant for a status line, not for logic.
	Detail string `json:"detail"`
	Error  string `json:"error"`
}
