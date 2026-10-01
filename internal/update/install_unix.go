//go:build darwin || linux

package update

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func installCapability(executable, tempDir string) (string, error) {
	real, info, err := realExecutable(executable)
	if err != nil {
		return capabilityFailure("check update executable", err)
	}
	if !info.Mode().IsRegular() {
		return "unsupported-platform", nil
	}

	target := real
	if runtime.GOOS == "darwin" {
		target = macBundleTarget(real)
		if target == "" {
			return "unsupported-platform", nil
		}
		if reason, err := macDiskImageCapability(real); reason != "" || err != nil {
			return reason, err
		}
		if reason, err := bundlePermissions(target); reason != "" || err != nil {
			return reason, err
		}
	} else if linuxSystemTarget(real) {
		return "unsupported-platform", nil
	}

	parent := filepath.Dir(target)
	if reason, err := directoryPermissions(parent); reason != "" || err != nil {
		return reason, err
	}
	if reason, err := probeSibling(parent); reason != "" || err != nil {
		return reason, err
	}
	return filesystemCapability(parent, tempDir)
}

func macBundleTarget(executable string) string {
	parts := strings.Split(filepath.Clean(executable), string(os.PathSeparator))
	for _, part := range parts {
		if part == "AppTranslocation" {
			return ""
		}
	}
	for i, part := range parts {
		if strings.HasSuffix(part, ".app") {
			return string(os.PathSeparator) + filepath.Join(parts[1:i+1]...)
		}
	}
	return ""
}

func macDiskImageCapability(executable string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "info", "-plist").Output()
	if err != nil {
		return "", fmt.Errorf("inspect mounted disk images: %w", err)
	}
	return diskImageCapability(executable, data)
}

func diskImageCapability(executable string, data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var started, ended, images, wantImages bool
	reason := ""
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if !started || !ended || !images || wantImages {
				return "", fmt.Errorf("mounted disk image metadata is not a complete plist")
			}
			return reason, nil
		}
		if err != nil {
			return "", fmt.Errorf("parse mounted disk images: %w", err)
		}
		if end, ok := token.(xml.EndElement); ok && end.Name.Local == "plist" {
			ended = true
		}
		if text, ok := token.(xml.CharData); ok && (!started || ended) && strings.TrimSpace(string(text)) != "" {
			return "", fmt.Errorf("unexpected text outside disk image plist")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !started {
			if start.Name.Local != "plist" {
				return "", fmt.Errorf("unexpected disk image metadata root %q", start.Name.Local)
			}
			started = true
			continue
		}
		if ended {
			return "", fmt.Errorf("multiple roots in disk image metadata")
		}
		if wantImages {
			if start.Name.Local != "array" {
				return "", fmt.Errorf("disk image list is not an array")
			}
			images, wantImages = true, false
		}
		if start.Name.Local != "key" {
			continue
		}
		var key string
		if err := decoder.DecodeElement(&key, &start); err != nil {
			return "", fmt.Errorf("parse disk image key: %w", err)
		}
		if key == "images" {
			wantImages = true
			continue
		}
		if key != "mount-point" {
			continue
		}
		var point struct {
			XMLName xml.Name `xml:"string"`
			Value   string   `xml:",chardata"`
		}
		if err := decoder.Decode(&point); err != nil {
			return "", fmt.Errorf("parse disk image mount point: %w", err)
		}
		if !filepath.IsAbs(point.Value) {
			return "", fmt.Errorf("invalid disk image mount point %q", point.Value)
		}
		mount, err := filepath.EvalSymlinks(point.Value)
		if err != nil {
			return "", fmt.Errorf("resolve disk image mount point: %w", err)
		}
		if pathWithin(executable, mount) {
			reason = "unsupported-platform"
		}
	}
}

func linuxSystemTarget(executable string) bool {
	for _, root := range []string{"/usr", "/bin", "/sbin", "/opt"} {
		if pathWithin(executable, root) {
			return true
		}
	}
	return false
}

func pathWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

func directoryPermissions(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return capabilityFailure("stat update directory", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("update directory %q is not a directory", path)
	}
	// RemoveAll needs to list and traverse every bundle directory and remove
	// its entries. Mode checks also honor read-only installs when running root.
	mode := info.Mode().Perm()
	if mode&0o444 == 0 || mode&0o222 == 0 || mode&0o111 == 0 {
		return "not-writable", nil
	}
	// access(2): read, write and directory search permissions.
	if err := syscall.Access(path, 4|2|1); err != nil {
		return capabilityFailure("access update directory", err)
	}
	return "", nil
}

func bundlePermissions(bundle string) (string, error) {
	reason := ""
	err := filepath.WalkDir(bundle, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil // Symlinks are unlinked, not followed, by RemoveAll.
		}
		var err error
		reason, err = directoryPermissions(path)
		if reason != "" {
			return fs.SkipAll
		}
		return err
	})
	if err != nil {
		return capabilityFailure("inspect update bundle", err)
	}
	return reason, nil
}

func filesystemCapability(parent, tempDir string) (string, error) {
	var target, staging syscall.Stat_t
	if err := syscall.Stat(parent, &target); err != nil {
		return "", fmt.Errorf("stat update target filesystem: %w", err)
	}
	if err := syscall.Stat(tempDir, &staging); err != nil {
		return "", fmt.Errorf("stat update staging filesystem: %w", err)
	}
	if staging.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return "", fmt.Errorf("update staging directory %q is not a directory", tempDir)
	}
	return deviceCapability(uint64(target.Dev), uint64(staging.Dev)), nil
}

func deviceCapability(target, staging uint64) string {
	if target != staging {
		return "cross-filesystem"
	}
	return ""
}
