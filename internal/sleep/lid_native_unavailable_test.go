//go:build !darwin || !cgo

package sleep

import (
	"errors"
	"testing"
)

func TestUnavailableLidGuardianIsHandled(t *testing.T) {
	handled, err := RunLidGuardian([]string{lidGuardianArgument})
	if !handled || !errors.Is(err, errUnsupported) {
		t.Fatalf("handled=%v error=%v", handled, err)
	}
}
