package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// InstallCapability checks whether the Wails helper can replace this installation.
// tempDir must be the staging filesystem (os.TempDir in production). An empty
// reason permits installation; errors are unexpected preflight failures.
func InstallCapability(executable, tempDir string) (reason string, err error) {
	return installCapability(executable, tempDir)
}

func realExecutable(executable string) (string, os.FileInfo, error) {
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return "", nil, fmt.Errorf("resolve update executable: %w", err)
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", nil, fmt.Errorf("resolve update executable: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", nil, fmt.Errorf("stat update executable: %w", err)
	}
	return real, info, nil
}

func probeSibling(parent string) (string, error) {
	info, err := os.Stat(parent)
	if err != nil {
		return capabilityFailure("stat update parent", err)
	}
	// A privileged process must not bypass an explicitly read-only install.
	if !info.IsDir() {
		return "", fmt.Errorf("update parent %q is not a directory", parent)
	}
	if info.Mode().Perm()&0o222 == 0 {
		return "not-writable", nil
	}
	file, err := os.CreateTemp(parent, ".agents-dashboard-update-*")
	if err != nil {
		return capabilityFailure("probe update parent", err)
	}
	closeErr := file.Close()
	removeErr := os.Remove(file.Name())
	if closeErr != nil {
		return "", fmt.Errorf("close update probe: %w", errors.Join(closeErr, removeErr))
	}
	if removeErr != nil {
		return capabilityFailure("remove update probe", removeErr)
	}
	return "", nil
}

func capabilityFailure(operation string, err error) (string, error) {
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS) {
		return "not-writable", nil
	}
	return "", fmt.Errorf("%s: %w", operation, err)
}
