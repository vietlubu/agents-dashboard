package config

import (
	"os"
	"path/filepath"
	"strings"
)

// HomeDir is the application data directory: <user config dir>/agent-dashboard,
// overridable with AGENT_DASHBOARD_HOME.
func HomeDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("AGENT_DASHBOARD_HOME")); v != "" {
		return ExpandTilde(v), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "agent-dashboard"), nil
}

// ExpandTilde resolves a leading ~ using the current user's home directory.
func ExpandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// CommaPaths splits a comma-separated list of paths, trimming blanks and expanding ~.
// Returns nil for an empty input rather than a slice with one empty element.
func CommaPaths(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, ExpandTilde(part))
	}
	return out
}
