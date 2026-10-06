package service

import (
	"context"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/sleep"
	"github.com/vietlubu/agents-dashboard/internal/store"
)

// SleepService exposes the sleep controller and today's usage to the UI and the menu bar.
type SleepService struct {
	deps *Deps
}

// NewSleepService builds the sleep service.
func NewSleepService(deps *Deps) *SleepService { return &SleepService{deps: deps} }

// Status reports the controller state. With no controller (a build without the desktop
// window) it reports the feature as unsupported rather than erroring.
func (s *SleepService) Status() sleep.Status {
	if s.deps.Sleep == nil {
		return sleep.Status{Mode: config.SleepModeOff, Supported: false, Detail: "unsupported"}
	}
	return s.deps.Sleep.Status()
}

// ClamshellSupported reports whether the platform can keep the machine running with the
// lid closed beyond AC power. Only macOS can, through an administrator prompt.
func (s *SleepService) ClamshellSupported() bool {
	return s.deps.Sleep != nil && s.deps.Sleep.ClamshellSupported()
}

// RequestClamshell raises the one administrator prompt needed to keep the machine running
// with the lid closed. It is only ever called from an explicit user action.
func (s *SleepService) RequestClamshell() error {
	if s.deps.Sleep == nil {
		return nil
	}
	return s.deps.Sleep.RequestClamshell()
}

// RestoreClamshell undoes RequestClamshell, restoring the system's own lid behaviour.
func (s *SleepService) RestoreClamshell() error {
	if s.deps.Sleep == nil {
		return nil
	}
	return s.deps.Sleep.RestoreClamshell()
}

// AgentActive reports whether an agent is working right now, independently of the
// auto-sleep setting. The menu bar asks for confirmation before an explicit sleep when it
// returns true.
func (s *SleepService) AgentActive() bool {
	if s.deps.Sleep == nil {
		return false
	}
	return s.deps.Sleep.ActiveNow(context.Background())
}

// SleepNow puts the machine to sleep immediately. The menu bar asks for confirmation first
// when an agent is still working; the service itself only carries out the request.
func (s *SleepService) SleepNow() error {
	if s.deps.Sleep == nil {
		return nil
	}
	return s.deps.Sleep.SleepNow()
}

// DisplaySleepNow turns the display off now without suspending the machine.
func (s *SleepService) DisplaySleepNow() error {
	if s.deps.Sleep == nil {
		return nil
	}
	return s.deps.Sleep.DisplaySleepNow()
}

// ScreensaverNow switches the session to the screensaver now.
func (s *SleepService) ScreensaverNow() error {
	if s.deps.Sleep == nil {
		return nil
	}
	return s.deps.Sleep.ScreensaverNow()
}

// Today returns today's token totals in the configured timezone, for the menu bar's usage
// section and any UI status line.
func (s *SleepService) Today() (store.Totals, error) {
	loc := s.deps.loc()
	now := time.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UnixMilli()
	to := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc).UnixMilli()
	return s.deps.DB.Totals(context.Background(), store.RangeQuery{FromMs: from, ToMs: to}, loc)
}
