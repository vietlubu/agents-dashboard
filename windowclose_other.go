//go:build !darwin || server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// setDockIconVisible is a no-op outside macOS, where the Dock concept does not exist.
func setDockIconVisible(bool) {}

// installWindowCloseBehavior is a no-op outside macOS: closing the window keeps each
// platform's own behaviour (quit on last window).
func installWindowCloseBehavior(*application.WebviewWindow) {}
