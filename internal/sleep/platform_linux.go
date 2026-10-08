//go:build linux

package sleep

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PlatformSupported reports that Linux can control sleep on a best-effort basis. Display
// sleep is not covered; see the package documentation.
func PlatformSupported() bool { return true }

func platformLidSupported() bool { return true }

func defaultInhibitor() Inhibitor           { return &linuxInhibitor{} }
func defaultSleeper() Sleeper               { return linuxSleeper{} }
func defaultDisplaySleeper() DisplaySleeper { return linuxDisplaySleeper{} }
func defaultScreensaver() ScreensaverStarter {
	return linuxScreensaver{}
}
func defaultIdler() Idler               { return linuxIdler{} }
func defaultMediaWatcher() MediaWatcher { return linuxMediaWatcher{} }

// --- keep-awake ---------------------------------------------------------------

// linuxInhibitor runs systemd-inhibit with a blocking lock for as long as a spec is held.
// It needs systemd (systemd-inhibit); without it the feature degrades to no-op.
type linuxInhibitor struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	spec InhibitSpec
}

func (i *linuxInhibitor) Apply(spec InhibitSpec) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if spec == i.spec {
		return nil
	}
	i.stopLocked()
	i.spec = spec
	if spec.Empty() {
		return nil
	}
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		i.spec = InhibitSpec{}
		return fmt.Errorf("systemd-inhibit not available: %w", err)
	}
	what := "idle:sleep"
	if spec.Lid {
		what += ":handle-lid-switch"
	}
	cmd := exec.Command("systemd-inhibit",
		"--what="+what,
		"--mode=block",
		"--who=Agents Dashboard",
		"--why=sleep prevention enabled",
		"sleep", "infinity")
	if err := cmd.Start(); err != nil {
		i.spec = InhibitSpec{}
		return fmt.Errorf("start systemd-inhibit: %w", err)
	}
	i.cmd = cmd
	go func() { _ = cmd.Wait() }()
	return nil
}

func (i *linuxInhibitor) Release() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.stopLocked()
	i.spec = InhibitSpec{}
	return nil
}

func (i *linuxInhibitor) stopLocked() {
	if i.cmd != nil && i.cmd.Process != nil {
		_ = i.cmd.Process.Kill()
	}
	i.cmd = nil
}

// --- sleep --------------------------------------------------------------------

type linuxSleeper struct{}

func (linuxSleeper) Sleep() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not available: %w", err)
	}
	out, err := exec.Command("systemctl", "suspend").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl suspend: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- display sleep ------------------------------------------------------------

type linuxDisplaySleeper struct{}

// DisplaySleep turns the display off now on X11 through DPMS. Under Wayland there is no
// portable equivalent, so this is best-effort and reports failure when xset is absent.
func (linuxDisplaySleeper) DisplaySleep() error {
	if _, err := exec.LookPath("xset"); err != nil {
		return fmt.Errorf("xset not available: %w", err)
	}
	out, err := exec.Command("xset", "dpms", "force", "off").CombinedOutput()
	if err != nil {
		return fmt.Errorf("xset dpms force off: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- screensaver --------------------------------------------------------------

type linuxScreensaver struct{}

// StartScreensaver activates the session screensaver, trying the freedesktop helper first
// and then the GNOME-specific command and D-Bus method.
func (linuxScreensaver) StartScreensaver() error {
	if _, err := exec.LookPath("xdg-screensaver"); err == nil {
		if err := exec.Command("xdg-screensaver", "activate").Run(); err == nil {
			return nil
		}
	}
	if _, err := exec.LookPath("gnome-screensaver-command"); err == nil {
		if err := exec.Command("gnome-screensaver-command", "-a").Run(); err == nil {
			return nil
		}
	}
	if _, err := exec.LookPath("gdbus"); err == nil {
		if err := exec.Command("gdbus", "call", "--session",
			"--dest", "org.gnome.ScreenSaver",
			"--object-path", "/org/gnome/ScreenSaver",
			"--method", "org.gnome.ScreenSaver.SetActive", "true").Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("start screensaver: no supported helper found")
}

// --- user idle ----------------------------------------------------------------

type linuxIdler struct{}

var gdbusUintRe = regexp.MustCompile(`\(uint32\s+(\d+)`)

// Idle is best-effort: xprintidle on X11, then the GNOME screensaver D-Bus interface. On a
// session where neither is available it reports "unmeasurable" and the controller then
// declines to force sleep.
func (linuxIdler) Idle() (time.Duration, bool) {
	if _, err := exec.LookPath("xprintidle"); err == nil {
		if out, err := exec.Command("xprintidle").Output(); err == nil {
			if ms, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
				return time.Duration(ms) * time.Millisecond, true
			}
		}
	}
	if _, err := exec.LookPath("gdbus"); err == nil {
		out, err := exec.Command("gdbus", "call", "--session",
			"--dest", "org.gnome.ScreenSaver",
			"--object-path", "/org/gnome/ScreenSaver",
			"--method", "org.gnome.ScreenSaver.GetSessionIdleTime").Output()
		if err == nil {
			if m := gdbusUintRe.FindStringSubmatch(string(out)); m != nil {
				if ms, err := strconv.ParseInt(m[1], 10, 64); err == nil {
					return time.Duration(ms) * time.Millisecond, true
				}
			}
		}
	}
	return 0, false
}
