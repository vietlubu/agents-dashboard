//go:build darwin

package sleep

import (
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

func defaultInhibitor() Inhibitor           { return newDarwinInhibitor() }
func defaultSleeper() Sleeper               { return darwinSleeper{} }
func defaultDisplaySleeper() DisplaySleeper { return darwinDisplaySleeper{} }
func defaultScreensaver() ScreensaverStarter {
	return darwinScreensaver{}
}
func defaultIdler() Idler { return darwinIdler{} }

// --- keep-awake ---------------------------------------------------------------

// darwinInhibitor manages one long-lived caffeinate child. caffeinate holds power
// assertions for as long as it runs, so a spec change is a kill-and-restart.
type darwinInhibitor struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	flags     string
	clamshell bool
}

func newDarwinInhibitor() *darwinInhibitor {
	return &darwinInhibitor{clamshell: sleepDisabled()}
}

func (i *darwinInhibitor) Apply(spec InhibitSpec) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	flags := caffeinateFlags(spec, i.clamshell)
	if flags == i.flags && (i.cmd != nil || flags == "") {
		return nil
	}
	i.stopLocked()
	i.flags = flags
	if flags == "" {
		return nil
	}
	cmd := exec.Command("caffeinate", strings.Fields(flags)...)
	if err := cmd.Start(); err != nil {
		i.flags = ""
		return fmt.Errorf("start caffeinate: %w", err)
	}
	i.cmd = cmd
	// Reap the child whenever it exits so it never becomes a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}

func (i *darwinInhibitor) Release() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.stopLocked()
	i.flags = ""
	return nil
}

func (i *darwinInhibitor) stopLocked() {
	if i.cmd != nil && i.cmd.Process != nil {
		_ = i.cmd.Process.Kill()
	}
	i.cmd = nil
}

// caffeinateFlags maps a spec to caffeinate's short flags. When the clamshell assertion is
// already set by pmset, lid-close sleep is handled at the kernel level and needs no -s.
func caffeinateFlags(spec InhibitSpec, clamshell bool) string {
	var parts []string
	if spec.System {
		parts = append(parts, "-i")
	}
	if spec.Display {
		parts = append(parts, "-d")
	}
	if spec.Lid && !clamshell {
		parts = append(parts, "-s")
	}
	return strings.Join(parts, " ")
}

func (i *darwinInhibitor) RequestClamshell() error {
	if err := runAdminPmset("1"); err != nil {
		return err
	}
	i.mu.Lock()
	i.clamshell = true
	i.mu.Unlock()
	return nil
}

func (i *darwinInhibitor) RestoreClamshell() error {
	if err := runAdminPmset("0"); err != nil {
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

// runAdminPmset sets the kernel SleepDisabled flag, which is the only way to keep a Mac
// running with the lid closed on battery. It raises one administrator prompt.
func runAdminPmset(value string) error {
	script := fmt.Sprintf(`do shell script "/usr/bin/pmset -a disablesleep %s" with administrator privileges`, value)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pmset disablesleep %s: %w: %s", value, err, strings.TrimSpace(string(out)))
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
