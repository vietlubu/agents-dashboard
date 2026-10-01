//go:build darwin || linux

package update

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallCapabilityStagingFailures(t *testing.T) {
	root := t.TempDir()
	executable := capabilityExecutable(t, root)
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte("staging file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, staging := range []string{filepath.Join(root, "missing"), file} {
		if reason, err := InstallCapability(executable, staging); reason != "" || err == nil {
			t.Errorf("invalid staging %q = %q, %v; want unexpected error", staging, reason, err)
		}
	}
	if reason, err := filesystemCapability(filepath.Join(root, "missing-parent"), root); reason != "" || err == nil {
		t.Errorf("missing target filesystem = %q, %v; want unexpected error", reason, err)
	}
}

func TestDeviceCapability(t *testing.T) {
	if got := deviceCapability(7, 7); got != "" {
		t.Errorf("same device = %q", got)
	}
	if got := deviceCapability(7, 8); got != "cross-filesystem" {
		t.Errorf("different devices = %q", got)
	}
}

func TestLinuxSystemTargets(t *testing.T) {
	for _, path := range []string{"/usr/bin/app", "/usr/local/bin/app", "/bin/app", "/sbin/app", "/opt/app", "/usr", "/bin", "/sbin", "/opt"} {
		if !linuxSystemTarget(path) {
			t.Errorf("system target %q permitted", path)
		}
	}
	for _, path := range []string{"/home/user/app", "/usr-local/app", "/optical/app", "/binary/app", "/sbin-user/app"} {
		if linuxSystemTarget(path) {
			t.Errorf("portable target %q blocked", path)
		}
	}
	if runtime.GOOS != "linux" {
		return
	}
	root := t.TempDir()
	link := filepath.Join(root, "system-launcher")
	if err := os.Symlink("/usr/bin/env", link); err != nil {
		t.Fatal(err)
	}
	capabilityWant(t, "/usr/bin/env", root, "unsupported-platform")
	capabilityWant(t, link, root, "unsupported-platform")
}

func TestReadOnlyExecutableCanBeReplacedOnUnix(t *testing.T) {
	root := t.TempDir()
	executable := capabilityExecutable(t, root)
	capabilityMode(t, executable, 0o555)
	// Unlinking a regular file needs a writable parent, not writable file bytes.
	capabilityWant(t, executable, root, "")
}

func TestMacBundleTarget(t *testing.T) {
	for _, tc := range []struct {
		executable string
		want       string
	}{
		{"/Applications/App.app/Contents/MacOS/App", "/Applications/App.app"},
		{"/Users/user/My App.app/Contents/MacOS/App", "/Users/user/My App.app"},
		{"/Applications/Outer.app/Contents/Inner.app/Contents/MacOS/App", "/Applications/Outer.app"},
		{"/Users/user/App", ""},
		{"/Volumes/Installer/App.app/Contents/MacOS/App", "/Volumes/Installer/App.app"},
		{"/private/var/folders/id/AppTranslocation/id/d/App.app/Contents/MacOS/App", ""},
		{"/Users/user/AppTranslocation/App.app/Contents/MacOS/App", ""},
	} {
		if got := macBundleTarget(tc.executable); got != tc.want {
			t.Errorf("macBundleTarget(%q) = %q; want %q", tc.executable, got, tc.want)
		}
	}
}

func TestMacBundleInstallCapability(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS bundle installation")
	}
	t.Run("standalone", func(t *testing.T) {
		root := t.TempDir()
		executable := filepath.Join(root, "standalone")
		if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		capabilityWant(t, executable, root, "unsupported-platform")
	})
	t.Run("translocated", func(t *testing.T) {
		root := t.TempDir()
		executable := capabilityExecutable(t, filepath.Join(root, "AppTranslocation", "id", "d"))
		capabilityWant(t, executable, root, "unsupported-platform")
	})
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{
		{"read-only-directory", 0o555},
		{"unsearchable-directory", 0o666},
		{"unreadable-directory", 0o333},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			executable := capabilityExecutable(t, root)
			resources := filepath.Join(root, "Agents Dashboard.app", "Contents", "Resources")
			if err := os.Mkdir(resources, 0o755); err != nil {
				t.Fatal(err)
			}
			capabilityMode(t, resources, tc.mode)
			capabilityWant(t, executable, root, "not-writable")
		})
	}
	t.Run("bundle-symlink-not-followed", func(t *testing.T) {
		root := t.TempDir()
		executable := capabilityExecutable(t, root)
		outside := filepath.Join(root, "outside")
		if err := os.Mkdir(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		capabilityMode(t, outside, 0o555)
		link := filepath.Join(root, "Agents Dashboard.app", "Contents", "LinkedResources")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		capabilityWant(t, executable, root, "")
	})
}

func TestDiskImageCapability(t *testing.T) {
	root := t.TempDir()
	mount := filepath.Join(root, "custom-image-mount")
	inside := capabilityExecutable(t, mount)
	outside := capabilityExecutable(t, filepath.Join(root, "custom-image-mount-portable"))
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(mount)); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`<plist><dict><key>images</key><array><dict><key>system-entities</key><array><dict><key>mount-point</key><string>` + escaped.String() + `</string></dict></array></dict></array></dict></plist>`)
	for _, tc := range []struct {
		executable string
		want       string
	}{
		{inside, "unsupported-platform"},
		{outside, ""},
		{"/Volumes/Portable/App.app/Contents/MacOS/App", ""},
	} {
		// Match the production caller's resolved executable, including /var
		// versus /private/var on macOS. The external path is a parser fixture.
		executable := tc.executable
		if executable != "/Volumes/Portable/App.app/Contents/MacOS/App" {
			var err error
			executable, err = filepath.EvalSymlinks(executable)
			if err != nil {
				t.Fatal(err)
			}
		}
		reason, err := diskImageCapability(executable, metadata)
		if err != nil || reason != tc.want {
			t.Errorf("diskImageCapability(%q) = %q, %v; want %q", executable, reason, err, tc.want)
		}
	}
	if reason, err := diskImageCapability(inside, []byte(`<plist><dict><key>images</key><array/></dict></plist>`)); err != nil || reason != "" {
		t.Errorf("no mounted images = %q, %v", reason, err)
	}
}

func TestDiskImageCapabilityMalformedMetadata(t *testing.T) {
	for _, metadata := range []string{
		"",
		"not XML",
		`<unexpected/>`,
		`<plist><dict/></plist>`,
		`<plist><dict><key>images</key><string>not an array</string></dict></plist>`,
		`<plist><dict><key>images</key><array><dict><key>mount-point</key><integer>1</integer></dict></array></dict></plist>`,
		`<plist><dict><key>images</key><array><dict><key>mount-point</key><string>relative</string></dict></array></dict></plist>`,
		`<plist><dict><key>images</key><array><dict><key>mount-point</key><string>`,
	} {
		if reason, err := diskImageCapability("/Applications/App.app/Contents/MacOS/App", []byte(metadata)); err == nil || reason != "" {
			t.Errorf("malformed metadata %q = %q, %v; want unexpected error", metadata, reason, err)
		}
	}
}
