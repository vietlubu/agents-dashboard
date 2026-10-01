//go:build server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// openMainWindow does nothing in server mode. The HTTP server serves the same frontend to
// whatever browser connects, and each connection is tracked internally as a browser
// window, so there is no native window to create.
func openMainWindow(*application.App) {}
