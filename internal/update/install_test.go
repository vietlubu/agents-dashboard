//go:build darwin || linux || windows

package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func capabilityExecutable(t *testing.T, root string) string {
	t.Helper()
	executable := filepath.Join(root, "agents-dashboard")
	if runtime.GOOS == "darwin" {
		executable = filepath.Join(root, "agents-dashboard.app", "Contents", "MacOS", "agents-dashboard")
	}
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	return executable
}

func capabilityWant(t *testing.T, executable, staging, want string) {
	t.Helper()
	reason, err := InstallCapability(executable, staging)
	if err != nil || reason != want {
		t.Fatalf("InstallCapability(%q, %q) = %q, %v; want %q", executable, staging, reason, err, want)
	}
}

func capabilityMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, info.Mode().Perm()); err != nil {
			t.Errorf("restore fixture permissions: %v", err)
		}
	})
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestInstallCapabilityWritable(t *testing.T) {
	root := t.TempDir()
	executable := capabilityExecutable(t, root)
	// Staging on the same filesystem is sufficient; Windows does not inspect it.
	capabilityWant(t, executable, root, "")
	data, err := os.ReadFile(executable)
	if err != nil || string(data) != "old executable" {
		t.Fatalf("preflight changed executable: %q, %v", data, err)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if matched, _ := filepath.Match(".agents-dashboard-update-*", entry.Name()); matched {
			t.Errorf("preflight left probe %q", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallCapabilityResolvesSymlink(t *testing.T) {
	root := t.TempDir()
	install := filepath.Join(root, "installation")
	executable := capabilityExecutable(t, install)
	link := filepath.Join(root, "launcher")
	if err := os.Symlink(executable, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	capabilityWant(t, link, root, "")
	if runtime.GOOS != "windows" {
		capabilityMode(t, install, 0o555)
		// The writable launcher directory must not hide a read-only real install.
		capabilityWant(t, link, root, "not-writable")
	}
}

func TestInstallCapabilityReadOnly(t *testing.T) {
	root := t.TempDir()
	install := filepath.Join(root, "installation")
	executable := capabilityExecutable(t, install)
	if runtime.GOOS == "windows" {
		capabilityMode(t, executable, 0o444)
	} else {
		capabilityMode(t, install, 0o555)
	}
	capabilityWant(t, executable, root, "not-writable")
}

func TestInstallCapabilityRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	capabilityWant(t, root, root, "unsupported-platform")
}

func TestInstallCapabilityMissingExecutable(t *testing.T) {
	root := t.TempDir()
	if reason, err := InstallCapability(filepath.Join(root, "missing"), root); err == nil || reason != "" {
		t.Fatalf("missing executable = %q, %v; want unexpected error", reason, err)
	}
}
