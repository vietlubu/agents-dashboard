//go:build windows

package sleep

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// PlatformSupported reports that Windows can control idle system and display sleep.
func PlatformSupported() bool { return true }

func platformLidSupported() bool { return false }

func defaultInhibitor() Inhibitor           { return newWindowsInhibitor() }
func defaultSleeper() Sleeper               { return windowsSleeper{} }
func defaultDisplaySleeper() DisplaySleeper { return windowsDisplaySleeper{} }
func defaultScreensaver() ScreensaverStarter {
	return windowsScreensaver{}
}
func defaultIdler() Idler               { return windowsIdler{} }
func defaultMediaWatcher() MediaWatcher { return windowsMediaWatcher{} }

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	powrprof = syscall.NewLazyDLL("powrprof.dll")

	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
	procGetTickCount            = kernel32.NewProc("GetTickCount")
	procGetLastInputInfo        = user32.NewProc("GetLastInputInfo")
	procSendMessageW            = user32.NewProc("SendMessageW")
	procSetSuspendState         = powrprof.NewProc("SetSuspendState")

	// Shell state, used to spot full-screen playback without the administrator rights
	// `powercfg /requests` would need.
	procSHQueryUserNotificationState = shell32.NewProc("SHQueryUserNotificationState")
)

const (
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
	esContinuous      = 0x80000000

	hwndBroadcast  = 0xFFFF
	wmSysCommand   = 0x0112
	scMonitorPower = 0xF170
	scScreenSave   = 0xF140
)

// --- keep-awake ---------------------------------------------------------------

// windowsInhibitor applies execution state on a dedicated, permanently locked OS thread.
// SetThreadExecutionState is per-thread, so the thread that set it must stay alive; the
// manager goroutine exists only to hold that thread.
type windowsInhibitor struct {
	mu      sync.Mutex
	spec    InhibitSpec
	updates chan uintptr
	once    sync.Once
}

func newWindowsInhibitor() *windowsInhibitor {
	return &windowsInhibitor{updates: make(chan uintptr, 1)}
}

func (i *windowsInhibitor) start() {
	i.once.Do(func() {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			for state := range i.updates {
				_, _, _ = procSetThreadExecutionState.Call(state)
			}
		}()
	})
}

func (i *windowsInhibitor) Apply(spec InhibitSpec) error {
	i.mu.Lock()
	if spec == i.spec {
		i.mu.Unlock()
		return nil
	}
	i.spec = spec
	i.mu.Unlock()
	i.start()
	i.push(spec)
	return nil
}

func (i *windowsInhibitor) Release() error {
	i.mu.Lock()
	i.spec = InhibitSpec{}
	i.mu.Unlock()
	i.start()
	i.push(InhibitSpec{})
	return nil
}

// push sends the execution state to the manager thread. The channel is size one, so a
// burst of changes collapses to the newest state instead of queueing.
func (i *windowsInhibitor) push(spec InhibitSpec) {
	state := uintptr(esContinuous)
	if spec.System || spec.Lid {
		state |= esSystemRequired
	}
	if spec.Display {
		state |= esDisplayRequired
	}
	select {
	case i.updates <- state:
	default:
		select {
		case <-i.updates:
		default:
		}
		i.updates <- state
	}
}

// --- sleep --------------------------------------------------------------------

type windowsSleeper struct{}

func (windowsSleeper) Sleep() error {
	r, _, err := procSetSuspendState.Call(0, 0, 0)
	if r == 0 {
		return fmt.Errorf("SetSuspendState: %w", err)
	}
	return nil
}

// --- display sleep ------------------------------------------------------------

type windowsDisplaySleeper struct{}

// DisplaySleep broadcasts SC_MONITORPOWER with the "off" argument, which every top-level
// window honours, turning the display off without suspending the machine.
func (windowsDisplaySleeper) DisplaySleep() error {
	procSendMessageW.Call(hwndBroadcast, wmSysCommand, scMonitorPower, 2)
	return nil
}

// --- screensaver --------------------------------------------------------------

type windowsScreensaver struct{}

// StartScreensaver broadcasts SC_SCREENSAVE, which starts the user's configured
// screensaver if one is enabled.
func (windowsScreensaver) StartScreensaver() error {
	procSendMessageW.Call(hwndBroadcast, wmSysCommand, scScreenSave, 0)
	return nil
}

// --- user idle ----------------------------------------------------------------

type windowsIdler struct{}

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

func (windowsIdler) Idle() (time.Duration, bool) {
	var li lastInputInfo
	li.cbSize = uint32(unsafe.Sizeof(li))
	if r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); r == 0 {
		return 0, false
	}
	tick, _, _ := procGetTickCount.Call()
	return time.Duration(uint32(tick)-li.dwTime) * time.Millisecond, true
}
