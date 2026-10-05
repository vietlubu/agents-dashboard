//go:build server

package main

import (
	"github.com/vietlubu/agents-dashboard/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// openMainWindow does nothing in server mode. The HTTP server serves the same frontend to
// whatever browser connects, and each connection is tracked internally as a browser
// window, so there is no native window to create.
func openMainWindow(*application.App) {}

// setupTray does nothing in server mode: a headless server has no menu bar.
func setupTray(*application.App, *service.SettingsService, *service.SleepService) {}

// configureUpdater never initializes the desktop engine or requests an update feed.
func configureUpdater(_ *application.App, deps *service.Deps) error {
	deps.Updater = nil
	deps.UpdaterDisabledReason = "server"
	return nil
}
