package service

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
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

func TestConcurrentSettingsUpdatesEmitPersistedMode(t *testing.T) {
	svc, cfg := newSettingsFixture(t)
	var emittedMu sync.Mutex
	var lastMode string
	svc.deps.Emit = func(name string, payload any) {
		if name != EventSettingsSaved {
			return
		}
		settings := payload.(store.Settings)
		// Allow a competing request to run before this notification is consumed.
		runtime.Gosched()
		persisted, ok, err := svc.deps.DB.GetSetting(context.Background(), store.SettingSleepMode)
		if err != nil || !ok || persisted != settings.SleepMode {
			t.Errorf("published mode %q disagrees with persisted mode %q (%v)", settings.SleepMode, persisted, err)
		}
		if mode := cfg.Snapshot().SleepMode; mode != settings.SleepMode {
			t.Errorf("published mode %q disagrees with runtime mode %q", settings.SleepMode, mode)
		}
		emittedMu.Lock()
		lastMode = settings.SleepMode
		emittedMu.Unlock()
	}
	var requests sync.WaitGroup
	for i := range 24 {
		requests.Add(1)
		go func(i int) {
			defer requests.Done()
			modes := [...]string{config.SleepModeOff, config.SleepModeAgent, config.SleepModeAlways}
			if _, err := svc.Update(store.SettingsPatch{SleepMode: modes[i%len(modes)]}); err != nil {
				t.Errorf("concurrent update: %v", err)
			}
		}(i)
	}
	requests.Wait()
	settings, err := svc.Get()
	if err != nil {
		t.Fatal(err)
	}
	if settings.SleepMode != lastMode {
		t.Errorf("last notification %q disagrees with final settings %q", lastMode, settings.SleepMode)
	}
}

func TestSleepSettingsRoundTrip(t *testing.T) {
	svc, cfg := newSettingsFixture(t)
	for _, mode := range []string{config.SleepModeAgent, config.SleepModeAlways, config.SleepModeOff} {
		t.Run(mode, func(t *testing.T) {
			updated, err := svc.Update(store.SettingsPatch{
				SleepMode:             mode,
				PreventSystemSleep:    boolPtr(false),
				PreventDisplaySleep:   boolPtr(true),
				PreventLidClosedSleep: boolPtr(true),
				WaitForMedia:          boolPtr(true),
				SleepAfterS:           600,
				SleepActiveWindowS:    45,
			})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if updated.SleepMode != mode || updated.PreventSystemSleep || !updated.PreventDisplaySleep || !updated.PreventLidClosedSleep {
				t.Errorf("sleep settings = %+v, want %s, system off, display and lid on", updated, mode)
			}
			if !updated.WaitForMedia {
				t.Errorf("WaitForMedia = false, want the persisted true: %+v", updated)
			}
			if updated.SleepAfterS != 600 || updated.SleepActiveWindowS != 45 {
				t.Errorf("durations = %d/%d, want 600/45", updated.SleepAfterS, updated.SleepActiveWindowS)
			}
			snap := cfg.Snapshot()
			if snap.SleepMode != mode || snap.SleepAfter != 600*time.Second || snap.SleepActiveWindow != 45*time.Second {
				t.Errorf("config snapshot = %+v, want the persisted values", snap)
			}
			if snap.PreventSystemSleep || !snap.PreventDisplaySleep || !snap.PreventLidClosedSleep {
				t.Errorf("config targets = %+v, want system off, display and lid on", snap)
			}
			if !snap.WaitForMedia {
				t.Errorf("config snapshot = %+v, want WaitForMedia from the persisted settings", snap)
			}
			got, err := svc.Get()
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got != updated {
				t.Errorf("Get = %+v, want %+v", got, updated)
			}
			stored, ok, err := svc.deps.DB.GetSetting(context.Background(), store.SettingSleepMode)
			if err != nil || !ok || stored != mode {
				t.Errorf("persisted mode = %q, %v, %v, want %q", stored, ok, err, mode)
			}
			persistedMedia, ok, err := svc.deps.DB.GetSetting(context.Background(), store.SettingWaitForMedia)
			if err != nil || !ok || persistedMedia != "true" {
				t.Errorf("persisted sleep_wait_for_media = %q, %v, %v, want true", persistedMedia, ok, err)
			}
			unchanged, err := svc.Update(store.SettingsPatch{})
			if err != nil {
				t.Fatalf("empty update: %v", err)
			}
			if unchanged != updated || cfg.Snapshot().SleepMode != mode {
				t.Errorf("empty update changed settings: %+v", unchanged)
			}
			updated, err = svc.Update(store.SettingsPatch{
				PreventDisplaySleep:   boolPtr(false),
				PreventLidClosedSleep: boolPtr(false),
			})
			if err != nil {
				t.Fatalf("scope-only update: %v", err)
			}
			if updated.SleepMode != mode || updated.PreventSystemSleep || updated.PreventDisplaySleep || updated.PreventLidClosedSleep {
				t.Errorf("scope-only update did not retain mode or persist false: %+v", updated)
			}
			if updated.SleepAfterS != 600 || updated.SleepActiveWindowS != 45 {
				t.Errorf("scope-only update changed durations: %+v", updated)
			}
			if !updated.WaitForMedia {
				t.Errorf("scope-only update cleared WaitForMedia: %+v", updated)
			}
			snap = cfg.Snapshot()
			if snap.SleepMode != mode || snap.PreventSystemSleep || snap.PreventDisplaySleep || snap.PreventLidClosedSleep {
				t.Errorf("scope-only config snapshot = %+v", snap)
			}
		})
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

func TestSleepModeDefaultsAndOverlay(t *testing.T) {
	for _, mode := range []string{config.SleepModeOff, config.SleepModeAgent, config.SleepModeAlways} {
		t.Run(mode, func(t *testing.T) {
			svc, _ := newSettingsFixture(t)
			t.Setenv("AGENTS_DASHBOARD_HOME", t.TempDir())
			t.Setenv("AGENTS_DASHBOARD_TZ", "UTC")
			t.Setenv("AGENTS_DASHBOARD_SLEEP_MODE", mode)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			svc.deps.Cfg = cfg
			for _, stored := range []string{"", "invalid", config.SleepModeOff, config.SleepModeAgent, config.SleepModeAlways} {
				t.Run("stored_"+stored, func(t *testing.T) {
					// Restore the environment defaults before each overlay.
					cfg.Apply(config.Mutable{SleepMode: mode})
					if stored != "" {
						if err := svc.deps.DB.SetSetting(context.Background(), store.SettingSleepMode, stored); err != nil {
							t.Fatalf("seed mode: %v", err)
						}
					}
					want := mode
					if config.ValidSleepMode(stored) {
						want = stored
					}
					got, err := svc.Get()
					if err != nil {
						t.Fatalf("get: %v", err)
					}
					if got.SleepMode != want {
						t.Errorf("Get mode = %q, want %q", got.SleepMode, want)
					}
					// The startup hydration path uses a partial update to apply all settings.
					got, err = svc.Update(store.SettingsPatch{})
					if err != nil {
						t.Fatalf("hydrate: %v", err)
					}
					if got.SleepMode != want || cfg.Snapshot().SleepMode != want {
						t.Errorf("hydrated mode = %q / %q, want %q", got.SleepMode, cfg.Snapshot().SleepMode, want)
					}
				})
			}
		})
	}
	svc, _ := newSettingsFixture(t)
	got, err := svc.Get()
	if err != nil {
		t.Fatalf("get empty default: %v", err)
	}
	if got.SleepMode != config.SleepModeOff {
		t.Errorf("empty default mode = %q, want off", got.SleepMode)
	}
	if err := svc.deps.DB.SetSetting(context.Background(), store.SettingSleepMode, "invalid"); err != nil {
		t.Fatalf("seed invalid mode: %v", err)
	}
	got, err = svc.Get()
	if err != nil || got.SleepMode != config.SleepModeOff {
		t.Errorf("invalid overlay on empty default = %q, %v, want off", got.SleepMode, err)
	}
}

func TestSleepSettingsInvalidModeDoesNotWrite(t *testing.T) {
	svc, cfg := newSettingsFixture(t)
	if _, err := svc.Update(store.SettingsPatch{SleepMode: config.SleepModeAgent}); err != nil {
		t.Fatalf("seed mode: %v", err)
	}
	before, err := svc.Get()
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	storedBefore, err := svc.deps.DB.AllSettings(context.Background())
	if err != nil {
		t.Fatalf("stored before: %v", err)
	}
	snapBefore := cfg.Snapshot()
	_, err = svc.Update(store.SettingsPatch{
		SleepMode:          "invalid",
		Theme:              "dark",
		PreventSystemSleep: boolPtr(false),
		SleepAfterS:        600,
	})
	if err == nil || err.Error() != "invalid sleep mode: invalid" {
		t.Fatalf("update error = %v, want invalid sleep mode: invalid", err)
	}
	after, err := svc.Get()
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after != before || cfg.Snapshot() != snapBefore {
		t.Errorf("invalid patch changed settings/config: %+v / %+v", after, cfg.Snapshot())
	}
	storedAfter, err := svc.deps.DB.AllSettings(context.Background())
	if err != nil {
		t.Fatalf("stored after: %v", err)
	}
	if len(storedAfter) != len(storedBefore) {
		t.Errorf("invalid patch changed stored key count: %v", storedAfter)
	}
	for key, value := range storedBefore {
		if got, ok := storedAfter[key]; !ok || got != value {
			t.Errorf("invalid patch changed %q: %q, %v; want %q", key, got, ok, value)
		}
	}
}

func TestSleepStatusWithoutController(t *testing.T) {
	status := NewSleepService(&Deps{}).Status()
	if status.Mode != config.SleepModeOff || status.Supported || status.Detail != "unsupported" {
		t.Errorf("Status = %+v, want off and unsupported", status)
	}
}
