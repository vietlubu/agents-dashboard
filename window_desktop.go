//go:build !server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

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
