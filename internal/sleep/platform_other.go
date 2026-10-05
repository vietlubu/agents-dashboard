//go:build !darwin && !linux && !windows

package sleep

import "time"

// PlatformSupported reports that sleep control is unavailable on this platform.
func PlatformSupported() bool { return false }

func defaultInhibitor() Inhibitor { return noopInhibitor{} }
func defaultSleeper() Sleeper     { return noopSleeper{} }
func defaultIdler() Idler         { return noopIdler{} }

type noopInhibitor struct{}

func (noopInhibitor) Apply(InhibitSpec) error { return nil }
func (noopInhibitor) Release() error          { return nil }

type noopSleeper struct{}

func (noopSleeper) Sleep() error { return errUnsupported }

type noopIdler struct{}

func (noopIdler) Idle() (time.Duration, bool) { return 0, false }
