//go:build !darwin || !cgo

package sleep

func nativeLidAvailable() bool                         { return false }
func prepareNativeGuardian()                           {}
func nativeSetLid(bool) (bool, error)                  { return false, errUnsupported }
func nativeReadLid() (bool, error)                     { return false, errUnsupported }
func nativeWatchLid() (<-chan struct{}, func(), error) { return nil, nil, errUnsupported }
