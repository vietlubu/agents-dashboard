package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/store"
)

func boolPtr(b bool) *bool { return &b }

func newSettingsFixture(t *testing.T) (*SettingsService, *config.Config) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := &config.Config{}
	cfg.Apply(config.Mutable{
		TZ:                  "UTC",
		SleepAfter:          5 * time.Minute,
		SleepActiveWindow:   2 * time.Minute,
		PreventSystemSleep:  true,
		PreventDisplaySleep: true,
	})
	deps := &Deps{DB: db, Cfg: cfg}
	return NewSettingsService(deps), cfg
}

func TestSleepSettingsRoundTrip(t *testing.T) {
	svc, cfg := newSettingsFixture(t)

	updated, err := svc.Update(store.SettingsPatch{
		SleepEnabled:          boolPtr(true),
		PreventSystemSleep:    boolPtr(false),
		PreventDisplaySleep:   boolPtr(true),
		PreventLidClosedSleep: boolPtr(true),
		SleepAfterS:           600,
		SleepActiveWindowS:    45,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated.SleepEnabled || updated.PreventSystemSleep || !updated.PreventLidClosedSleep {
		t.Errorf("toggles = %+v, want enabled, system off, lid on", updated)
	}
	if updated.SleepAfterS != 600 || updated.SleepActiveWindowS != 45 {
		t.Errorf("durations = %d/%d, want 600/45", updated.SleepAfterS, updated.SleepActiveWindowS)
	}

	// The running configuration must see the change without a restart.
	snap := cfg.Snapshot()
	if !snap.SleepEnabled || snap.SleepAfter != 600*time.Second || snap.SleepActiveWindow != 45*time.Second {
		t.Errorf("config snapshot = %+v, want the persisted values", snap)
	}
	if snap.PreventSystemSleep || !snap.PreventLidClosedSleep {
		t.Errorf("config toggles = %+v, want system off, lid on", snap)
	}

	// A fresh read must return the persisted values, not the defaults.
	got, err := svc.Get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != updated {
		t.Errorf("Get = %+v, want %+v", got, updated)
	}
}

func TestSleepSettingsRejectTooShort(t *testing.T) {
	svc, _ := newSettingsFixture(t)

	updated, err := svc.Update(store.SettingsPatch{SleepAfterS: 5, SleepActiveWindowS: 1})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.SleepAfterS != 300 || updated.SleepActiveWindowS != 120 {
		t.Errorf("durations = %d/%d, want the defaults kept (300/120)", updated.SleepAfterS, updated.SleepActiveWindowS)
	}
}
