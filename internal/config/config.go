// Package config holds process-level defaults (paths, intervals, timezone) that are
// resolved from the environment and then overlayed by user settings from the store.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults. Kept here so both Load and tests share one source of truth.
const (
	DefaultIdleInterval  = 60 * time.Second
	DefaultBurstInterval = 10 * time.Second
	DefaultBurstWindow   = 5 * time.Minute
	DefaultServerHost    = "localhost"
	DefaultServerPort    = 8080
)

// Mutable holds every field the user can change at runtime from the settings page.
// Guarded by Config.mu: the scheduler reads intervals every loop, so a settings
// change must not require a restart.
type Mutable struct {
	TZ             string
	Location       *time.Location
	IdleInterval   time.Duration
	BurstInterval  time.Duration
	BurstWindow    time.Duration
	Concurrency    int
	ServerHost     string
	ServerPort     int
	AutoSyncPrices bool
}

// Config is the process configuration.
type Config struct {
	Home   string
	DBPath string

	// Warnings collected while loading (currently: an unparsable TZ). The settings
	// page surfaces them once instead of failing the process.
	Warnings []string

	mu      sync.RWMutex
	mutable Mutable
}

// Load resolves defaults and environment overrides, creating the data directory.
func Load() (*Config, error) {
	home, err := HomeDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", home, err)
	}

	c := &Config{
		Home:   home,
		DBPath: filepath.Join(home, "dashboard.db"),
	}

	warnings := []string{}

	// Timezone: env override, else the system zone by name.
	//
	// The name matters as much as the location: the frontend resolves day boundaries with
	// Intl, which accepts IANA names only. time.Local.String() returns "Local", which Intl
	// rejects, so the system zone is detected by name instead of taken from time.Local.
	tz, loc, warning := resolveTimezone(os.Getenv("AGENTS_DASHBOARD_TZ"))
	if warning != "" {
		warnings = append(warnings, warning)
	}

	c.mutable = Mutable{
		TZ:             tz,
		Location:       loc,
		IdleInterval:   envDuration("AGENTS_DASHBOARD_IDLE_INTERVAL", DefaultIdleInterval),
		BurstInterval:  envDuration("AGENTS_DASHBOARD_BURST_INTERVAL", DefaultBurstInterval),
		BurstWindow:    DefaultBurstWindow,
		Concurrency:    envInt("AGENTS_DASHBOARD_CONCURRENCY", defaultConcurrency()),
		ServerHost:     envString("AGENTS_DASHBOARD_SERVER_HOST", DefaultServerHost),
		ServerPort:     envInt("AGENTS_DASHBOARD_SERVER_PORT", DefaultServerPort),
		AutoSyncPrices: envBool("AGENTS_DASHBOARD_AUTO_SYNC_PRICES", true),
	}
	if c.mutable.Concurrency < 1 {
		c.mutable.Concurrency = 1
	}
	if c.mutable.ServerPort < 1 || c.mutable.ServerPort > 65535 {
		warnings = append(warnings, fmt.Sprintf("port %d is out of range; using %d", c.mutable.ServerPort, DefaultServerPort))
		c.mutable.ServerPort = DefaultServerPort
	}
	c.Warnings = warnings

	return c, nil
}

// Snapshot returns a copy of the mutable configuration.
func (c *Config) Snapshot() Mutable {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mutable
}

// Apply overlays persisted user settings. Empty/zero values keep the current value.
func (c *Config) Apply(m Mutable) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m.TZ != "" {
		loc, err := time.LoadLocation(m.TZ)
		if err == nil {
			c.mutable.TZ = m.TZ
			c.mutable.Location = loc
		}
	}
	if m.IdleInterval > 0 {
		c.mutable.IdleInterval = m.IdleInterval
	}
	if m.BurstInterval > 0 {
		c.mutable.BurstInterval = m.BurstInterval
	}
	if m.BurstWindow > 0 {
		c.mutable.BurstWindow = m.BurstWindow
	}
	if m.Concurrency > 0 {
		c.mutable.Concurrency = m.Concurrency
	}
	if m.ServerHost != "" {
		c.mutable.ServerHost = m.ServerHost
	}
	if m.ServerPort > 0 {
		c.mutable.ServerPort = m.ServerPort
	}
	c.mutable.AutoSyncPrices = m.AutoSyncPrices
}

// resolveTimezone returns the IANA zone name and its location: an explicit name when given,
// otherwise the system zone read from /etc/localtime, otherwise UTC.
func resolveTimezone(requested string) (string, *time.Location, string) {
	if requested != "" {
		if loc, err := time.LoadLocation(requested); err == nil {
			return requested, loc, ""
		}
		name, loc := systemTimezone()
		return name, loc, fmt.Sprintf("timezone %q is not available; using %s", requested, name)
	}
	name, loc := systemTimezone()
	return name, loc, ""
}

// systemTimezone detects the machine's zone by name. macOS and Linux both expose it as the
// target of /etc/localtime; when that cannot be read the fallback is UTC, which every
// consumer (including Intl) accepts.
func systemTimezone() (string, *time.Location) {
	const fallback = "UTC"
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if idx := strings.Index(target, "zoneinfo/"); idx >= 0 {
			name := target[idx+len("zoneinfo/"):]
			if loc, err := time.LoadLocation(name); err == nil {
				return name, loc
			}
		}
	}
	loc, err := time.LoadLocation(fallback)
	if err != nil {
		return fallback, time.UTC
	}
	return fallback, loc
}

func defaultConcurrency() int {
	n := runtime.NumCPU() / 2
	if n < 2 {
		n = 2
	}
	if n > 4 {
		n = 4
	}
	return n
}

func envString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
