//go:build darwin

package sleep

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PlatformSupported reports that macOS can control sleep.
func PlatformSupported() bool { return true }

func platformLidSupported() bool { return nativeLidAvailable() }

func defaultInhibitor() Inhibitor           { return newDarwinInhibitor() }
func defaultSleeper() Sleeper               { return darwinSleeper{} }
func defaultDisplaySleeper() DisplaySleeper { return darwinDisplaySleeper{} }
func defaultScreensaver() ScreensaverStarter {
	return darwinScreensaver{}
}
func defaultIdler() Idler               { return darwinIdler{} }
func defaultMediaWatcher() MediaWatcher { return darwinMediaWatcher{} }

// --- keep-awake ---------------------------------------------------------------

// darwinInhibitor keeps idle assertions independent of private lid control.
// Replacements start first; a failed change leaves the previous child intact.
type darwinInhibitor struct {
	opMu      sync.Mutex
	mu        sync.Mutex
	cmd       *exec.Cmd
	flags     string
	clamshell bool
	lid       *darwinLidControl
}

func newDarwinInhibitor() *darwinInhibitor {
	return &darwinInhibitor{
		clamshell: sleepDisabled(),
		lid: newDarwinLidControl(darwinLidHooks{
			available: nativeLidAvailable(), set: nativeSetLid,
			read: nativeReadLid, watch: nativeWatchLid, start: startLidGuardian,
		}),
	}
}

func (i *darwinInhibitor) Apply(spec InhibitSpec) error {
	i.opMu.Lock()
	defer i.opMu.Unlock()
	flags := caffeinateFlags(spec)
	// Even failed lid cleanup must not leave idle assertions after dashboard exit.
	args := append(strings.Fields(flags), "-w", strconv.Itoa(os.Getpid()))
	i.mu.Lock()
	needsReplacement := flags != "" && (flags != i.flags || i.cmd == nil)
	i.mu.Unlock()
	var replacement *exec.Cmd
	if needsReplacement {
		replacement = exec.Command("caffeinate", args...)
		if err := replacement.Start(); err != nil {
			return fmt.Errorf("start caffeinate: %w", err)
		}
	}
	if i.lid != nil {
		if err := i.lid.reconcile(spec.Lid); err != nil {
			if replacement != nil {
				_ = replacement.Process.Kill()
				_ = replacement.Wait()
			}
			return err
		}
	} else if spec.Lid {
		if replacement != nil {
			_ = replacement.Process.Kill()
			_ = replacement.Wait()
		}
		return errUnsupported
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if replacement == nil && flags != "" && i.cmd == nil {
		replacement = exec.Command("caffeinate", args...)
		if err := replacement.Start(); err != nil {
			return fmt.Errorf("start caffeinate: %w", err)
		}
	}
	if flags == i.flags && (i.cmd != nil || flags == "") {
		return nil
	}
	if err := i.stopLocked(); err != nil {
		if replacement != nil {
			_ = replacement.Process.Kill()
			_ = replacement.Wait()
		}
		return err
	}
	i.cmd, i.flags = replacement, flags
	if replacement != nil {
		go func() {
			_ = replacement.Wait()
			i.mu.Lock()
			if i.cmd == replacement {
				i.cmd = nil
				i.flags = ""
			}
			i.mu.Unlock()
		}()
	}
	return nil
}

func (i *darwinInhibitor) Release() error {
	i.opMu.Lock()
	defer i.opMu.Unlock()
	if i.lid != nil {
		if err := i.lid.release(); err != nil {
			return err
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.stopLocked()
}

func (i *darwinInhibitor) stopLocked() error {
	if i.cmd != nil && i.cmd.Process != nil {
		if err := i.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop caffeinate: %w", err)
		}
	}
	i.cmd, i.flags = nil, ""
	return nil
}

func (i *darwinInhibitor) LidState() LidState {
	if i.lid == nil {
		return LidState{PrivateAPI: nativeLidAvailable()}
	}
	return i.lid.status()
}

func (i *darwinInhibitor) SetLidNotifier(notifier func()) {
	if i.lid != nil {
		i.lid.setNotifier(notifier)
	}
}

// caffeinateFlags holds only idle system/display assertions. It must never add
// a CPU/demand-sleep veto, including when private lid control is requested.
func caffeinateFlags(spec InhibitSpec) string {
	var parts []string
	if spec.System {
		parts = append(parts, "-i")
	}
	if spec.Display {
		parts = append(parts, "-d")
	}
	return strings.Join(parts, " ")
}

func (i *darwinInhibitor) RestoreClamshell() error {
	if err := restoreAdminPmset(); err != nil {
		return err
	}
	i.mu.Lock()
	i.clamshell = false
	i.mu.Unlock()
	return nil
}

func (i *darwinInhibitor) ClamshellActive() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.clamshell
}

// restoreAdminPmset only clears the legacy SleepDisabled flag on explicit recovery.
// Normal mode and scope changes never call this administrator operation.
func restoreAdminPmset() error {
	const script = `do shell script "/usr/bin/pmset -a disablesleep 0" with administrator privileges`
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore pmset disablesleep: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var sleepDisabledRe = regexp.MustCompile(`(?m)^\s*SleepDisabled\s+(\d+)`)

// sleepDisabled reports whether the kernel SleepDisabled flag is currently set, so a
// restart does not assume the previous process left it off.
func sleepDisabled() bool {
	out, err := exec.Command("pmset", "-g").Output()
	if err != nil {
		return false
	}
	m := sleepDisabledRe.FindStringSubmatch(string(out))
	return m != nil && m[1] == "1"
}

// --- sleep --------------------------------------------------------------------

type darwinSleeper struct{}

func (darwinSleeper) Sleep() error {
	if _, err := exec.LookPath("pmset"); err == nil {
		if out, err := exec.Command("pmset", "sleepnow").CombinedOutput(); err == nil {
			return nil
		} else {
			if _, perr := exec.LookPath("osascript"); perr != nil {
				return fmt.Errorf("pmset sleepnow: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
	}
	out, err := exec.Command("osascript", "-e", `tell application "System Events" to sleep`).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sleep: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- display sleep ------------------------------------------------------------

type darwinDisplaySleeper struct{}

// DisplaySleep turns the display off now, leaving the machine running. pmset is present on
// every macOS install.
func (darwinDisplaySleeper) DisplaySleep() error {
	out, err := exec.Command("pmset", "displaysleepnow").CombinedOutput()
	if err != nil {
		return fmt.Errorf("pmset displaysleepnow: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- screensaver --------------------------------------------------------------

type darwinScreensaver struct{}

// saverPaths are the known locations of the screensaver engine. Newer macOS releases moved
// it out of /Applications, so the fallbacks are tried in order.
var saverPaths = []string{
	"/System/Library/CoreServices/ScreenSaverEngine.app",
	"/Applications/ScreenSaverEngine.app",
}

// StartScreensaver launches the screensaver engine. It tries the app paths directly before
// asking LaunchServices by bundle identifier, which is the most portable last resort.
func (darwinScreensaver) StartScreensaver() error {
	for _, p := range saverPaths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := exec.Command("open", p).Run(); err == nil {
			return nil
		}
	}
	if err := exec.Command("open", "-b", "com.apple.ScreenSaver.Engine").Run(); err == nil {
		return nil
	}
	return fmt.Errorf("start screensaver: ScreenSaverEngine not found")
}

// --- user idle ----------------------------------------------------------------

type darwinIdler struct{}

var hidIdleRe = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// Idle reads the HID idle time from the IOHIDSystem registry node, which counts since the
// last keyboard, mouse or trackpad input.
func (darwinIdler) Idle() (time.Duration, bool) {
	out, err := exec.Command("ioreg", "-c", "IOHIDSystem").Output()
	if err != nil {
		return 0, false
	}
	m := hidIdleRe.FindStringSubmatch(string(out))
	if m == nil {
		return 0, false
	}
	ns, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(ns), true
}
