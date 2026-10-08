//go:build windows

package sleep

import "unsafe"

// windowsMediaWatcher is best-effort. The authoritative list of power requests
// (`powercfg /requests`) needs administrator rights, so this uses the shell's notification
// state instead, which every full-screen Direct3D application and presentation sets.
//
// Known gap: a muted video playing in a window produces no signal here, so it can still be
// interrupted by auto-sleep.
type windowsMediaWatcher struct{}

// Shell notification states. QUNS_NOT_PRESENT (1) means the screen is locked or the
// screensaver is up, which is not playback; the three below all mean a full-screen
// application or presentation owns the desktop.
const (
	qunsBusy                 = 2
	qunsRunningD3DFullScreen = 3
	qunsPresentationMode     = 4
)

func (windowsMediaWatcher) MediaPlaying() (Media, bool) {
	var state uint32
	if r, _, _ := procSHQueryUserNotificationState.Call(uintptr(unsafe.Pointer(&state))); r != 0 {
		return Media{}, false
	}
	var out Media
	switch state {
	case qunsRunningD3DFullScreen:
		addMediaSource(&out, "fullscreen video")
	case qunsPresentationMode:
		addMediaSource(&out, "presentation mode")
	case qunsBusy:
		addMediaSource(&out, "fullscreen application")
	}
	return out, true
}
