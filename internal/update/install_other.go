//go:build !darwin && !linux && !windows

package update

func installCapability(executable, tempDir string) (string, error) {
	return "unsupported-platform", nil
}
