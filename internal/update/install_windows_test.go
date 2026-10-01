//go:build windows

package update

import (
	"path/filepath"
	"testing"
)

func TestWindowsCapabilityDoesNotRestrictStagingVolume(t *testing.T) {
	root := t.TempDir()
	executable := capabilityExecutable(t, root)
	// No second mounted volume is needed: an unrelated staging path must not
	// affect Windows capability, because Wails owns the cross-volume fallback.
	capabilityWant(t, executable, filepath.Join(root, "unrelated-staging"), "")
}
