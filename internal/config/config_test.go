package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAndEnvOverrides(t *testing.T) {
	t.Setenv("AGENTS_DASHBOARD_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("AGENTS_DASHBOARD_TZ", "Asia/Ho_Chi_Minh")
	t.Setenv("AGENTS_DASHBOARD_IDLE_INTERVAL", "90s")
	t.Setenv("AGENTS_DASHBOARD_CONCURRENCY", "3")
	t.Setenv("AGENTS_DASHBOARD_SERVER_PORT", "9999")
	t.Setenv("AGENTS_DASHBOARD_AUTO_SYNC_PRICES", "false")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := c.Snapshot()
	if s.TZ != "Asia/Ho_Chi_Minh" {
		t.Errorf("TZ = %q", s.TZ)
	}
	if got := s.Location.String(); got != "Asia/Ho_Chi_Minh" {
		t.Errorf("Location = %q", got)
	}
	if s.IdleInterval != 90*time.Second {
		t.Errorf("IdleInterval = %v", s.IdleInterval)
	}
	if s.Concurrency != 3 {
		t.Errorf("Concurrency = %d", s.Concurrency)
	}
	if s.ServerPort != 9999 {
		t.Errorf("ServerPort = %d", s.ServerPort)
	}
	if s.AutoSyncPrices {
		t.Error("AutoSyncPrices should be false")
	}
	if filepath.Base(c.DBPath) != "dashboard.db" {
		t.Errorf("DBPath = %q", c.DBPath)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", c.Warnings)
	}
}

func TestLoadFallsBackOnUnknownTimezone(t *testing.T) {
	t.Setenv("AGENTS_DASHBOARD_HOME", t.TempDir())
	t.Setenv("AGENTS_DASHBOARD_TZ", "Not/AZone")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", c.Warnings)
	}
	// The fallback is the detected system zone by name, not time.Local: the name is what the
	// frontend needs, and time.Local.String() ("Local") is not usable there.
	tz := c.Snapshot().TZ
	if tz == "Not/AZone" || tz == "" {
		t.Errorf("TZ = %q, want a usable fallback", tz)
	}
	if _, err := time.LoadLocation(tz); err != nil {
		t.Errorf("fallback zone %q does not load: %v", tz, err)
	}
}

func TestLoadRejectsOutOfRangePort(t *testing.T) {
	t.Setenv("AGENTS_DASHBOARD_HOME", t.TempDir())
	t.Setenv("AGENTS_DASHBOARD_SERVER_PORT", "70000")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Snapshot().ServerPort != DefaultServerPort {
		t.Errorf("ServerPort = %d, want %d", c.Snapshot().ServerPort, DefaultServerPort)
	}
	if len(c.Warnings) != 1 {
		t.Errorf("expected one warning, got %v", c.Warnings)
	}
}

// Apply must actually take effect because the scheduler reads intervals on every loop.
func TestApplyOverridesTakeEffect(t *testing.T) {
	t.Setenv("AGENTS_DASHBOARD_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c.Apply(Mutable{TZ: "UTC", IdleInterval: 5 * time.Minute, Concurrency: 1, ServerHost: "0.0.0.0"})
	s := c.Snapshot()
	if s.TZ != "UTC" || s.Location.String() != "UTC" {
		t.Errorf("TZ = %q / %v", s.TZ, s.Location)
	}
	if s.IdleInterval != 5*time.Minute {
		t.Errorf("IdleInterval = %v", s.IdleInterval)
	}
	if s.Concurrency != 1 || s.ServerHost != "0.0.0.0" {
		t.Errorf("Concurrency = %d, ServerHost = %q", s.Concurrency, s.ServerHost)
	}
	// Zero values must not clobber the previous value.
	c.Apply(Mutable{})
	if c.Snapshot().TZ != "UTC" {
		t.Errorf("empty Apply cleared TZ: %q", c.Snapshot().TZ)
	}
}

// The default zone must be a name Intl accepts; time.Local.String() returns "Local", which
// the frontend cannot use to compute day boundaries.
func TestDefaultTimezoneIsAnIANAName(t *testing.T) {
	t.Setenv("AGENTS_DASHBOARD_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tz := c.Snapshot().TZ
	if tz == "" || tz == "Local" {
		t.Fatalf("TZ = %q, want an IANA zone name", tz)
	}
	if _, err := time.LoadLocation(tz); err != nil {
		t.Errorf("TZ %q does not load: %v", tz, err)
	}
	if c.Snapshot().Location == nil {
		t.Error("Location is nil")
	}
}

func TestSystemTimezoneFallsBackToUTC(t *testing.T) {
	name, loc := systemTimezone()
	if name == "" || loc == nil {
		t.Fatalf("systemTimezone = %q, %v", name, loc)
	}
	// Whatever it detected, the value must be usable by every consumer.
	if _, err := time.LoadLocation(name); err != nil {
		t.Errorf("detected zone %q does not load: %v", name, err)
	}
}

func TestCommaPaths(t *testing.T) {
	home := ExpandTilde("~")
	got := CommaPaths(" ~/a ,, /b ,")
	want := []string{filepath.Join(home, "a"), "/b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q want %q", i, got[i], want[i])
		}
	}
	if len(CommaPaths("")) != 0 {
		t.Error("empty input must yield no paths")
	}
}

func TestSleepModeConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		want    string
		warning string
	}{
		{name: "default", want: SleepModeOff},
		{name: "off", mode: SleepModeOff, want: SleepModeOff},
		{name: "agent", mode: SleepModeAgent, want: SleepModeAgent},
		{name: "always", mode: SleepModeAlways, want: SleepModeAlways},
		{name: "invalid", mode: "sometimes", want: SleepModeOff, warning: `sleep mode "sometimes" is invalid; using off`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENTS_DASHBOARD_HOME", t.TempDir())
			t.Setenv("AGENTS_DASHBOARD_TZ", "UTC")
			t.Setenv("AGENTS_DASHBOARD_SERVER_PORT", "8080")
			t.Setenv("AGENTS_DASHBOARD_SLEEP_MODE", tc.mode)
			// The retired boolean environment setting must not enable sleep prevention.
			t.Setenv("AGENTS_DASHBOARD_SLEEP_ENABLED", "true")
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Snapshot().SleepMode; got != tc.want {
				t.Errorf("SleepMode = %q, want %q", got, tc.want)
			}
			if tc.warning == "" {
				if len(cfg.Warnings) != 0 {
					t.Errorf("unexpected warnings: %v", cfg.Warnings)
				}
			} else if len(cfg.Warnings) != 1 || cfg.Warnings[0] != tc.warning {
				t.Errorf("warnings = %v, want %q", cfg.Warnings, tc.warning)
			}
			if tc.mode == SleepModeAlways {
				cfg.Apply(Mutable{})
				if got := cfg.Snapshot().SleepMode; got != SleepModeAlways {
					t.Errorf("empty Apply changed mode to %q", got)
				}
				cfg.Apply(Mutable{SleepMode: "invalid"})
				if got := cfg.Snapshot().SleepMode; got != SleepModeAlways {
					t.Errorf("invalid Apply changed mode to %q", got)
				}
				cfg.Apply(Mutable{SleepMode: SleepModeOff})
				if got := cfg.Snapshot().SleepMode; got != SleepModeOff {
					t.Errorf("Apply off left mode %q", got)
				}
			}
		})
	}
	for _, mode := range []string{"", "invalid", "AGENT", " always "} {
		if ValidSleepMode(mode) {
			t.Errorf("ValidSleepMode(%q) = true", mode)
		}
	}
}
