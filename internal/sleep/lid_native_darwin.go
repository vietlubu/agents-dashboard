//go:build darwin && cgo

package sleep

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include "lid_native_darwin.h"
*/
import "C"

import (
	"fmt"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
)

func nativeLidAvailable() bool { return true }

// A broken acknowledgement pipe must return EPIPE, not terminate the helper
// with SIGPIPE before its deferred native cleanup runs.
func prepareNativeGuardian() { signal.Ignore(syscall.SIGPIPE) }

func nativeSetLid(enabled bool) (applied bool, err error) {
	transport := lidTransport{
		open: func() uint32 { return uint32(C.dashboard_lid_open()) },
		call: func(connection uint32, selector uint32, input uint64, count uint32) uint32 {
			return uint32(C.dashboard_lid_call(C.uint32_t(connection), C.uint32_t(selector), C.uint64_t(input), C.uint32_t(count)))
		},
		close: func(connection uint32) uint32 { return uint32(C.dashboard_lid_close(C.uint32_t(connection))) },
	}
	return transport.set(enabled)
}

func nativeReadLid() (bool, error) {
	var effective C.int
	if result := uint32(C.dashboard_lid_read(&effective)); result != 0 {
		return false, fmt.Errorf("read AppleClamshellCausesSleep: IOReturn 0x%08x", result)
	}
	return effective != 0, nil
}

func nativeWatchLid() (<-chan struct{}, func(), error) {
	events := make(chan struct{}, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		var result C.uint32_t
		watch := C.dashboard_lid_watch_start(&result)
		if watch == nil {
			ready <- fmt.Errorf("observe lid power policy: IOReturn 0x%08x", uint32(result))
			return
		}
		defer C.dashboard_lid_watch_stop(watch)
		ready <- nil
		for {
			select {
			case <-stop:
				return
			default:
			}
			if C.dashboard_lid_watch_poll(watch) != 0 {
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}
	}()
	if err := <-ready; err != nil {
		return nil, nil, err
	}
	var once sync.Once
	return events, func() { once.Do(func() { close(stop); <-done }) }, nil
}
