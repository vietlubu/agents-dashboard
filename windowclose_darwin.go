//go:build darwin && !server

package main

/*
#cgo CFLAGS: -mmacosx-version-min=10.13 -x objective-c
#cgo LDFLAGS: -framework Cocoa

#include <Cocoa/Cocoa.h>

// agentsSetActivationPolicy switches between a regular app (Dock tile + application menu)
// and an accessory app (menu bar status item only). Both calls are hopped to the main
// thread, and returning to regular also brings the app forward so the window reappears in
// front of whatever was focused.
static void agentsSetActivationPolicy(int policy) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp setActivationPolicy:(NSApplicationActivationPolicy)policy];
        if (policy == NSApplicationActivationPolicyRegular) {
            [NSApp activateIgnoringOtherApps:YES];
        }
    });
}
*/
import "C"

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// setDockIconVisible shows or hides the Dock tile. Hiding switches to the accessory policy,
// which also drops the application menu but keeps the menu bar status item.
func setDockIconVisible(visible bool) {
	policy := C.NSApplicationActivationPolicyAccessory
	if visible {
		policy = C.NSApplicationActivationPolicyRegular
	}
	C.agentsSetActivationPolicy(C.int(policy))
}

// installWindowCloseBehavior turns the window's close button into "hide to the menu bar":
// the event is cancelled (so the window is not destroyed), the Dock icon disappears, and
// the app keeps running so the tray can bring the window back.
func installWindowCloseBehavior(window *application.WebviewWindow) {
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		setDockIconVisible(false)
		window.Hide()
	})
}
