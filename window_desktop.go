//go:build !server

package main

import (
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/service"
	appupdate "github.com/vietlubu/agents-dashboard/internal/update"
	"github.com/vietlubu/agents-dashboard/internal/version"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
)

// openMainWindow creates the desktop window. In server mode this file is not compiled:
// server builds create browser windows internally, and asking for a native window would
// only log a warning.
func openMainWindow(app *application.App) {
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Agents Dashboard",
		Width:            1440,
		Height:           900,
		MinWidth:         1024,
		MinHeight:        700,
		BackgroundColour: application.NewRGB(20, 22, 26),
		URL:              "/",
	})
	window.Center()
	window.Show()
}

// configureUpdater leaves development and unsupported builds offline. Release
// checks use the CalVer adapter; downloads are only initiated by AppService consent.
func configureUpdater(app *application.App, deps *service.Deps) error {
	deps.Updater = nil
	if version.Version == "dev" {
		deps.UpdaterDisabledReason = "development"
		return nil
	}
	if _, err := version.Compare(version.Version, version.Version); err != nil {
		return fmt.Errorf("configure updater version: %w", err)
	}
	if _, err := appupdate.DesktopAsset(runtime.GOOS, runtime.GOARCH); err != nil {
		deps.UpdaterDisabledReason = "unsupported-platform"
		return nil
	}
	provider, err := appupdate.NewGitHubProvider(&http.Client{Timeout: 5 * time.Minute}, "")
	if err != nil {
		return fmt.Errorf("configure update provider: %w", err)
	}
	if err := app.Updater.Init(updater.Config{
		CurrentVersion: version.Version,
		Providers:      []updater.Provider{provider},
		Window:         updater.WindowNone,
	}); err != nil {
		return fmt.Errorf("initialize updater: %w", err)
	}
	deps.Updater = app.Updater
	deps.UpdaterDisabledReason = ""
	return nil
}
