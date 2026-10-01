//go:build !darwin && !linux && !windows

package update

import "testing"

func TestInstallCapabilityUnsupportedPlatform(t *testing.T) {
	if reason, err := InstallCapability("unused", "unused"); reason != "unsupported-platform" || err != nil {
		t.Fatalf("unsupported platform = %q, %v", reason, err)
	}
}
