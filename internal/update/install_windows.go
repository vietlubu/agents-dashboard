//go:build windows

package update

import "path/filepath"

func installCapability(executable, tempDir string) (string, error) {
	real, info, err := realExecutable(executable)
	if err != nil {
		return capabilityFailure("check update executable", err)
	}
	if !info.Mode().IsRegular() {
		return "unsupported-platform", nil
	}
	if info.Mode().Perm()&0o222 == 0 {
		return "not-writable", nil
	}
	// Wails can copy the staged payload across Windows volumes.
	return probeSibling(filepath.Dir(real))
}
