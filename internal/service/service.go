// Package service exposes the dashboard to the frontend. Every exported method on a
// service struct becomes a typed JavaScript binding, so the signatures here are the
// frontend API: parameters and results are plain JSON-serialisable types only (no
// time.Time, no interface{}, no maps with struct values).
package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/pricing"
	"github.com/vietlubu/agents-dashboard/internal/sleep"
	"github.com/vietlubu/agents-dashboard/internal/store"
	syncengine "github.com/vietlubu/agents-dashboard/internal/sync"
	"github.com/vietlubu/agents-dashboard/internal/version"
)

// Event names emitted to the frontend. They are registered in main so the generated
// TypeScript API is typed per event.
const (
	EventSyncState     = syncengine.EventState
	EventSyncProgress  = syncengine.EventProgress
	EventSyncDone      = syncengine.EventDone
	EventSyncError     = syncengine.EventError
	EventDataChanged   = syncengine.EventData
	EventPricingSynced = "pricing:synced"
	EventSettingsSaved = "settings:saved"
	EventAppUpdate     = "app:update"
	EventSleepStatus   = "sleep:status"
)

// Deps is what every service needs. One instance is shared so all services see the same
// database, configuration and scan engine.
type Deps struct {
	DB                    *store.DB
	Cfg                   *config.Config
	Engine                *syncengine.Engine
	Scheduler             *syncengine.Scheduler
	Sleep                 *sleep.Controller
	Catalog               *pricing.Catalog
	Log                   *slog.Logger
	Emit                  func(name string, payload any)
	Updater               *updater.Updater
	UpdaterDisabledReason string
}

// loc is the configured timezone, used by every read path so day buckets agree with what
// the scan wrote.
func (d *Deps) loc() *time.Location {
	loc := d.Cfg.Snapshot().Location
	if loc == nil {
		return time.UTC
	}
	return loc
}

func (d *Deps) emit(name string, payload any) {
	if d.Emit != nil {
		d.Emit(name, payload)
	}
}

func (d *Deps) log() *slog.Logger {
	if d.Log == nil {
		return slog.Default()
	}
	return d.Log
}

// SyncStatePayload is the scan-loop state event. The event shape belongs to the sync
// package (its producer); it is aliased here so the binding surface stays in one place.
type SyncStatePayload = syncengine.StateEvent

// DataChangedPayload is the "refetch what you are showing" signal.
type DataChangedPayload = syncengine.DataChangedEvent

// PricingSyncedPayload reports the outcome of a catalog sync.
type PricingSyncedPayload struct {
	Source  string `json:"source"`
	Models  int    `json:"models"`
	Skipped int    `json:"skipped"`
}

// AppService owns the process lifecycle: it starts the scan loop when the application is
// up, and reports startup warnings exactly once.
type AppService struct {
	deps *Deps

	updateOperation   sync.Mutex
	updateMu          sync.Mutex
	updateStatus      UpdateStatus
	updateCancel      context.CancelFunc
	updateDone        chan struct{}
	installCapability func() (string, error)
}

// NewAppService builds the lifecycle service.
func NewAppService(deps *Deps) *AppService {
	return newAppService(deps, currentInstallCapability)
}

// ServiceStartup runs once the application is running. The scan starts here rather than
// before the window exists, so the UI is already listening for progress events.
func (s *AppService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	deps := s.deps
	if deps.Scheduler != nil {
		deps.Scheduler.Start(ctx)
	}
	if deps.Sleep != nil {
		deps.Sleep.Start(ctx)
	}
	if deps.Cfg != nil {
		go s.autoSyncPrices(ctx)
	}
	s.setUpdateStatus(s.UpdateStatus())
	s.startUpdateLoop(ctx)
	return nil
}

// ServiceShutdown stops the background update, scan and sleep loops. Stopping the sleep
// controller also releases every keep-awake assertion it held.
func (s *AppService) ServiceShutdown() error {
	s.stopUpdateLoop()
	if s.deps.Scheduler != nil {
		s.deps.Scheduler.Stop()
	}
	if s.deps.Sleep != nil {
		s.deps.Sleep.Stop()
	}
	return nil
}

// Warnings returns startup warnings (for example an unusable timezone) so the UI can show
// them instead of them being lost in a log file.
func (s *AppService) Warnings() []string {
	warnings := s.deps.Cfg.Warnings
	if warnings == nil {
		return []string{}
	}
	return warnings
}

// Version returns the build version, for the about panel.
func (s *AppService) Version() string { return version.Version }

// autoSyncPrices pulls a price catalog at most once a day, when the user has left the
// setting on and no catalog has been fetched recently. A failure is logged and ignored:
// without prices every event simply stays "unavailable".
func (s *AppService) autoSyncPrices(ctx context.Context) {
	deps := s.deps
	if !deps.Cfg.Snapshot().AutoSyncPrices {
		return
	}
	last, err := s.lastPriceSync(ctx)
	if err != nil {
		return
	}
	if last > 0 && time.Since(time.UnixMilli(last)) < 24*time.Hour {
		return
	}
	result, err := deps.Catalog.SyncFrom(ctx, deps.DB, pricing.SourceModelsDev)
	if err != nil {
		deps.log().Warn("automatic price sync failed", "error", err)
		return
	}
	deps.log().Info("automatic price sync complete", "models", result.Models)
	deps.emit(EventPricingSynced, PricingSyncedPayload{
		Source: result.Source, Models: result.Models, Skipped: result.Skipped,
	})
	if _, err := deps.Catalog.Recalculate(ctx, deps.DB, nil); err == nil {
		_ = deps.DB.RebuildAllRollups(ctx)
		deps.emit(EventDataChanged, DataChangedPayload{})
	}
}

func (s *AppService) lastPriceSync(ctx context.Context) (int64, error) {
	return s.deps.DB.SettingInt(ctx, store.SettingLastPriceSync, 0)
}
