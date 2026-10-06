package sleep

import "fmt"

const lidSelector = uint32(12)

// The same transport used by the native adapter can be exercised without IOKit.
type lidTransport struct {
	open  func() uint32
	call  func(connection uint32, selector uint32, input uint64, count uint32) uint32
	close func(uint32) uint32
}

// set reports scalar application separately from connection cleanup errors.
func (t lidTransport) set(enabled bool) (applied bool, err error) {
	connection := t.open()
	if connection == 0 {
		return false, fmt.Errorf("IOPMFindPowerManagement: no connection")
	}
	input := uint64(0)
	if enabled {
		input = 1
	}
	result := t.call(connection, lidSelector, input, 1)
	closed := t.close(connection)
	if result != 0 {
		return false, fmt.Errorf("IOConnectCallScalarMethod selector 12: IOReturn 0x%08x", result)
	}
	if closed != 0 {
		return true, fmt.Errorf("IOServiceClose: IOReturn 0x%08x", closed)
	}
	return true, nil
}
