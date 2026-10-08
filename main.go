// Command agents-dashboard is a local usage tracker for coding agents. It reads the session
// files that Claude Code, Codex, OpenCode, Pi, omp and Freebuff already write, stores normalized
// usage in its own SQLite database, and serves a dashboard from that database.
//
// The same code builds as a desktop application or, with `-tags server`, as a headless
// HTTP server serving the identical frontend.
package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/pricing"
	"github.com/vietlubu/agents-dashboard/internal/service"
	"github.com/vietlubu/agents-dashboard/internal/sleep"
	"github.com/vietlubu/agents-dashboard/internal/store"
	syncengine "github.com/vietlubu/agents-dashboard/internal/sync"
	"github.com/vietlubu/agents-dashboard/internal/version"
)

//go:embed all:frontend/dist
var assets embed.FS

// maxLogBytes bounds the log file. It is checked at startup rather than during writes: one
// rotated file is enough history for a local diagnostics log, and a rotating writer would
// be more machinery than this deserves.
const maxLogBytes = 5 << 20

func init() {
	// Registering the event payload types gives the generated TypeScript bindings a typed
	// listener for each event instead of an untyped callback.
	application.RegisterEvent[syncengine.StateEvent](service.EventSyncState)
	application.RegisterEvent[syncengine.ProgressEvent](service.EventSyncProgress)
	application.RegisterEvent[syncengine.DoneEvent](service.EventSyncDone)
	application.RegisterEvent[syncengine.ErrorEvent](service.EventSyncError)
	application.RegisterEvent[syncengine.DataChangedEvent](service.EventDataChanged)
	application.RegisterEvent[service.PricingSyncedPayload](service.EventPricingSynced)
	application.RegisterEvent[store.Settings](service.EventSettingsSaved)
	application.RegisterEvent[service.UpdateStatus](service.EventAppUpdate)
	application.RegisterEvent[sleep.Status](service.EventSleepStatus)
}

func main() {
	if handled, err := sleep.RunLidGuardian(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}

	logger, closeLog := newLogger(cfg)
	defer closeLog()

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Error("cannot open database", "path", cfg.DBPath, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	catalog := pricing.NewCatalog(logger)
	if err := catalog.Reload(ctx, db); err != nil {
		logger.Warn("cannot load price catalog", "error", err)
	}

	// The engine is created with a nil emitter and gets one once the application exists:
	// the window has to be listening before progress events are useful, and the app is
	// only constructible after the services are known.
	engine := syncengine.New(db, cfg, catalog, logger, nil)
	scheduler := syncengine.NewScheduler(engine, cfg, logger)

	var app *application.App
	platform := sleep.DefaultPlatform()
	controller := sleep.New(sleep.Deps{
		Cfg:            cfg,
		Activity:       sleep.NewMonitor(db, engine.LastChangedAt, platform.Processes),
		Inhibitor:      platform.Inhibitor,
		Sleeper:        platform.Sleeper,
		DisplaySleeper: platform.DisplaySleeper,
		Screensaver:    platform.Screensaver,
		Idler:          platform.Idler,
		Media:          platform.Media,
		Supported:      platform.Supported,
		LidSupported:   platform.LidSupported,
		Log:            logger,
		OnStatus: func(st sleep.Status) {
			if app != nil {
				app.Event.Emit(service.EventSleepStatus, st)
			}
		},
	})
	deps := &service.Deps{
		DB:        db,
		Cfg:       cfg,
		Engine:    engine,
		Scheduler: scheduler,
		Sleep:     controller,
		Catalog:   catalog,
		Log:       logger,
		Emit: func(name string, payload any) {
			if app != nil {
				app.Event.Emit(name, payload)
			}
		},
	}

	settings := service.NewSettingsService(deps)
	sleepSvc := service.NewSleepService(deps)
	appSvc := service.NewAppService(deps)

	app = application.New(application.Options{
		Name:        "agents-dashboard",
		Description: "Local agent usage tracker",
		Services: []application.Service{
			application.NewService(appSvc),
			application.NewService(service.NewDashboardService(deps)),
			application.NewService(service.NewEventsService(deps)),
			application.NewService(service.NewMetaService(deps)),
			application.NewService(settings),
			application.NewService(sleepSvc),
			application.NewService(service.NewSyncService(deps)),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Server: application.ServerOptions{
			Host: cfg.Snapshot().ServerHost,
			Port: cfg.Snapshot().ServerPort,
		},
		Mac: application.MacOptions{
			// Closing the window hides the app into the menu bar (see
			// installWindowCloseBehavior), so it must not terminate with the last window.
			// Quit is explicit: the tray's Quit item or Cmd+Q.
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})
	engine.SetEmitter(deps.Emit)
	if err := configureUpdater(app, deps); err != nil {
		logger.Error("cannot configure updater", "version", version.Version, "error", err)
		os.Exit(1)
	}

	// Persisted settings are applied before the first scan so a configured timezone buckets
	// the very first day correctly. A failure here is logged, not fatal: the defaults apply.
	if err := applyPersistedSettings(settings); err != nil {
		logger.Warn("cannot apply saved settings", "error", err)
	}

	openMainWindow(app)
	setupTray(app, settings, sleepSvc)

	app.OnShutdown(func() {
		scheduler.Stop()
		controller.Stop()
	})

	if err := app.Run(); err != nil {
		logger.Error("application exited", "version", version.Version, "error", err)
		os.Exit(1)
	}
}

// applyPersistedSettings pushes the stored settings into the running configuration.
func applyPersistedSettings(s *service.SettingsService) error {
	stored, err := s.Get()
	if err != nil {
		return err
	}
	sync := stored.AutoSyncPrices
	_, err = s.Update(store.SettingsPatch{
		TZ:             stored.TZ,
		IdleIntervalS:  stored.IdleIntervalS,
		BurstIntervalS: stored.BurstIntervalS,
		Concurrency:    stored.Concurrency,
		AutoSyncPrices: &sync,
		ServerHost:     stored.ServerHost,
		ServerPort:     stored.ServerPort,
		Theme:          stored.Theme,
		Locale:         stored.Locale,
	})
	return err
}

// newLogger writes structured logs to the data directory and mirrors warnings and above to
// stderr, so a terminal launch shows what is happening while the file keeps the detail.
func newLogger(cfg *config.Config) (*slog.Logger, func()) {
	path := filepath.Join(cfg.Home, "agents-dashboard.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})), func() {}
	}

	fileHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
	ttyHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})
	return slog.New(&fanout{handlers: []slog.Handler{ttyHandler, fileHandler}}), func() { f.Close() }
}

// fanout sends each record to every handler that accepts it.
type fanout struct {
	handlers []slog.Handler
}

func (f *fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *fanout) Handle(ctx context.Context, record slog.Record) error {
	for _, h := range f.handlers {
		if h.Enabled(ctx, record.Level) {
			_ = h.Handle(ctx, record.Clone())
		}
	}
	return nil
}

func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &fanout{handlers: next}
}

func (f *fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &fanout{handlers: next}
}
