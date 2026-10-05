package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/pricing"
	"github.com/vietlubu/agents-dashboard/internal/store"
)

// SettingsService owns everything the user can change: scan roots, intervals, timezone,
// prices and price rules, and the destructive reset.
type SettingsService struct {
	deps *Deps
}

// NewSettingsService builds the settings service.
func NewSettingsService(deps *Deps) *SettingsService { return &SettingsService{deps: deps} }

// Get returns the effective settings: persisted values where present, otherwise the
// process defaults.
func (s *SettingsService) Get() (store.Settings, error) {
	ctx := context.Background()
	db := s.deps.DB
	cfg := s.deps.Cfg.Snapshot()

	out := store.Settings{
		TZ:             cfg.TZ,
		IdleIntervalS:  int64(cfg.IdleInterval / time.Second),
		BurstIntervalS: int64(cfg.BurstInterval / time.Second),
		BurstWindowS:   int64(cfg.BurstWindow / time.Second),
		Concurrency:    int64(cfg.Concurrency),
		AutoSyncPrices: cfg.AutoSyncPrices,
		ServerHost:     cfg.ServerHost,
		ServerPort:     int64(cfg.ServerPort),
		Theme:          "system",
		Locale:         "en",

		SleepEnabled:          cfg.SleepEnabled,
		SleepAfterS:           int64(cfg.SleepAfter / time.Second),
		SleepActiveWindowS:    int64(cfg.SleepActiveWindow / time.Second),
		PreventSystemSleep:    cfg.PreventSystemSleep,
		PreventDisplaySleep:   cfg.PreventDisplaySleep,
		PreventLidClosedSleep: cfg.PreventLidClosedSleep,
	}

	if v, ok, err := db.GetSetting(ctx, store.SettingTZ); err != nil {
		return out, err
	} else if ok {
		out.TZ = v
	}
	if v, err := db.SettingInt(ctx, store.SettingIdleIntervalS, out.IdleIntervalS); err != nil {
		return out, err
	} else {
		out.IdleIntervalS = v
	}
	if v, err := db.SettingInt(ctx, store.SettingBurstIntervalS, out.BurstIntervalS); err != nil {
		return out, err
	} else {
		out.BurstIntervalS = v
	}
	if v, err := db.SettingInt(ctx, store.SettingBurstWindowS, out.BurstWindowS); err != nil {
		return out, err
	} else {
		out.BurstWindowS = v
	}
	if v, err := db.SettingInt(ctx, store.SettingConcurrency, out.Concurrency); err != nil {
		return out, err
	} else {
		out.Concurrency = v
	}
	if v, err := db.SettingBool(ctx, store.SettingAutoSyncPrices, out.AutoSyncPrices); err != nil {
		return out, err
	} else {
		out.AutoSyncPrices = v
	}
	if v, err := db.SettingInt(ctx, store.SettingLastPriceSync, 0); err != nil {
		return out, err
	} else {
		out.LastPriceSync = v
	}
	if v, ok, err := db.GetSetting(ctx, store.SettingServerHost); err != nil {
		return out, err
	} else if ok {
		out.ServerHost = v
	}
	if v, err := db.SettingInt(ctx, store.SettingServerPort, out.ServerPort); err != nil {
		return out, err
	} else {
		out.ServerPort = v
	}
	if v, ok, err := db.GetSetting(ctx, store.SettingTheme); err != nil {
		return out, err
	} else if ok {
		out.Theme = v
	}
	if v, ok, err := db.GetSetting(ctx, store.SettingLocale); err != nil {
		return out, err
	} else if ok {
		out.Locale = v
	}
	if v, err := db.SettingBool(ctx, store.SettingSleepEnabled, out.SleepEnabled); err != nil {
		return out, err
	} else {
		out.SleepEnabled = v
	}
	if v, err := db.SettingInt(ctx, store.SettingSleepAfterS, out.SleepAfterS); err != nil {
		return out, err
	} else {
		out.SleepAfterS = v
	}
	if v, err := db.SettingInt(ctx, store.SettingSleepActiveWindowS, out.SleepActiveWindowS); err != nil {
		return out, err
	} else {
		out.SleepActiveWindowS = v
	}
	if v, err := db.SettingBool(ctx, store.SettingPreventSystemSleep, out.PreventSystemSleep); err != nil {
		return out, err
	} else {
		out.PreventSystemSleep = v
	}
	if v, err := db.SettingBool(ctx, store.SettingPreventDisplaySleep, out.PreventDisplaySleep); err != nil {
		return out, err
	} else {
		out.PreventDisplaySleep = v
	}
	if v, err := db.SettingBool(ctx, store.SettingPreventLidClosedSleep, out.PreventLidClosedSleep); err != nil {
		return out, err
	} else {
		out.PreventLidClosedSleep = v
	}
	return out, nil
}

// Update applies a partial settings change and returns the new effective settings.
//
// A timezone change is not a cosmetic edit: every event's local day bucket and therefore
// every rollup row is keyed on it, so the day buckets are rewritten and the rollups
// rebuilt before the call returns. The frontend shows a rebuilding state for the duration.
func (s *SettingsService) Update(patch store.SettingsPatch) (store.Settings, error) {
	ctx := context.Background()
	db := s.deps.DB

	current, err := s.Get()
	if err != nil {
		return store.Settings{}, err
	}

	if patch.TZ != "" && patch.TZ != current.TZ {
		if _, err := time.LoadLocation(patch.TZ); err != nil {
			return store.Settings{}, errors.New("unknown timezone: " + patch.TZ)
		}
		if err := db.SetSetting(ctx, store.SettingTZ, patch.TZ); err != nil {
			return store.Settings{}, err
		}
	}

	setInt := func(key string, value, minValue int64) error {
		if value < minValue {
			return nil
		}
		return db.SetSetting(ctx, key, strconv.FormatInt(value, 10))
	}
	if err := setInt(store.SettingIdleIntervalS, patch.IdleIntervalS, 1); err != nil {
		return store.Settings{}, err
	}
	if err := setInt(store.SettingBurstIntervalS, patch.BurstIntervalS, 1); err != nil {
		return store.Settings{}, err
	}
	if err := setInt(store.SettingConcurrency, patch.Concurrency, 1); err != nil {
		return store.Settings{}, err
	}
	if patch.AutoSyncPrices != nil {
		if err := db.SetSetting(ctx, store.SettingAutoSyncPrices,
			strconv.FormatBool(*patch.AutoSyncPrices)); err != nil {
			return store.Settings{}, err
		}
	}
	if patch.ServerHost != "" {
		if err := db.SetSetting(ctx, store.SettingServerHost, patch.ServerHost); err != nil {
			return store.Settings{}, err
		}
	}
	if patch.ServerPort > 0 && patch.ServerPort <= 65535 {
		if err := db.SetSetting(ctx, store.SettingServerPort,
			strconv.FormatInt(patch.ServerPort, 10)); err != nil {
			return store.Settings{}, err
		}
	}
	if patch.Theme != "" {
		if err := db.SetSetting(ctx, store.SettingTheme, patch.Theme); err != nil {
			return store.Settings{}, err
		}
	}
	if patch.Locale != "" {
		if err := db.SetSetting(ctx, store.SettingLocale, patch.Locale); err != nil {
			return store.Settings{}, err
		}
	}

	setBool := func(key string, value *bool) error {
		if value == nil {
			return nil
		}
		return db.SetSetting(ctx, key, strconv.FormatBool(*value))
	}
	if err := setBool(store.SettingSleepEnabled, patch.SleepEnabled); err != nil {
		return store.Settings{}, err
	}
	if err := setBool(store.SettingPreventSystemSleep, patch.PreventSystemSleep); err != nil {
		return store.Settings{}, err
	}
	if err := setBool(store.SettingPreventDisplaySleep, patch.PreventDisplaySleep); err != nil {
		return store.Settings{}, err
	}
	if err := setBool(store.SettingPreventLidClosedSleep, patch.PreventLidClosedSleep); err != nil {
		return store.Settings{}, err
	}
	// A sleep delay shorter than 30s would fight the machine's own idle timers; the active
	// window shorter than 15s would flicker between active and idle.
	if err := setInt(store.SettingSleepAfterS, patch.SleepAfterS, 30); err != nil {
		return store.Settings{}, err
	}
	if err := setInt(store.SettingSleepActiveWindowS, patch.SleepActiveWindowS, 15); err != nil {
		return store.Settings{}, err
	}

	updated, err := s.Get()
	if err != nil {
		return store.Settings{}, err
	}
	s.applyToConfig(updated)
	// Reconcile the keep-awake state immediately instead of waiting for the next tick.
	if s.deps.Sleep != nil {
		s.deps.Sleep.Kick()
	}
	s.deps.emit(EventSettingsSaved, updated)

	if updated.TZ != current.TZ {
		if err := s.rebuildForTimezone(ctx, updated.TZ); err != nil {
			return updated, err
		}
	}
	return updated, nil
}

// applyToConfig pushes the persisted settings into the running configuration, so an
// interval change takes effect on the next loop without a restart.
func (s *SettingsService) applyToConfig(settings store.Settings) {
	s.deps.Cfg.Apply(config.Mutable{
		TZ:                    settings.TZ,
		IdleInterval:          time.Duration(settings.IdleIntervalS) * time.Second,
		BurstInterval:         time.Duration(settings.BurstIntervalS) * time.Second,
		BurstWindow:           time.Duration(settings.BurstWindowS) * time.Second,
		Concurrency:           int(settings.Concurrency),
		ServerHost:            settings.ServerHost,
		ServerPort:            int(settings.ServerPort),
		AutoSyncPrices:        settings.AutoSyncPrices,
		SleepEnabled:          settings.SleepEnabled,
		SleepAfter:            time.Duration(settings.SleepAfterS) * time.Second,
		SleepActiveWindow:     time.Duration(settings.SleepActiveWindowS) * time.Second,
		PreventSystemSleep:    settings.PreventSystemSleep,
		PreventDisplaySleep:   settings.PreventDisplaySleep,
		PreventLidClosedSleep: settings.PreventLidClosedSleep,
	})
}

// rebuildForTimezone rewrites every event's local day and rebuilds the rollups.
func (s *SettingsService) rebuildForTimezone(ctx context.Context, tz string) error {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return err
	}
	if err := s.deps.DB.RecomputeLocalDays(ctx, loc, nil); err != nil {
		return err
	}
	if err := s.deps.DB.RebuildAllRollups(ctx); err != nil {
		return err
	}
	s.deps.emit(EventDataChanged, DataChangedPayload{})
	return nil
}

// ScanRoots lists the user-added extra roots for every harness.
func (s *SettingsService) ScanRoots() ([]store.ScanRoot, error) {
	return s.deps.DB.AllScanRoots(context.Background())
}

// AddScanRoot registers an extra root and immediately scans it, so its data appears
// without waiting for the next scheduled pass.
func (s *SettingsService) AddScanRoot(harness, path string) ([]store.ScanRoot, error) {
	ctx := context.Background()
	if harness == "" {
		return nil, errors.New("a harness is required")
	}
	if path == "" {
		return nil, errors.New("a path is required")
	}
	if err := s.deps.DB.AddScanRoot(ctx, harness, path); err != nil {
		return nil, err
	}
	if s.deps.Scheduler != nil {
		s.deps.Scheduler.TriggerNow("scan-root-added")
	}
	return s.deps.DB.AllScanRoots(ctx)
}

// RemoveScanRoot forgets an extra root.
func (s *SettingsService) RemoveScanRoot(harness, path string) ([]store.ScanRoot, error) {
	ctx := context.Background()
	if err := s.deps.DB.RemoveScanRoot(ctx, harness, path); err != nil {
		return nil, err
	}
	return s.deps.DB.AllScanRoots(ctx)
}

// Prices lists the price table.
func (s *SettingsService) Prices() ([]store.ModelPrice, error) {
	return s.deps.DB.ModelPrices(context.Background())
}

// SetPrice writes a manual price. A manual row is never overwritten by a catalog sync.
func (s *SettingsService) SetPrice(p store.ModelPrice) ([]store.ModelPrice, error) {
	ctx := context.Background()
	p.Source = "manual"
	if err := s.deps.DB.UpsertModelPrice(ctx, p, true); err != nil {
		return nil, err
	}
	if _, err := s.reprice(ctx); err != nil {
		return nil, err
	}
	return s.deps.DB.ModelPrices(ctx)
}

// DeletePrice removes a price row.
func (s *SettingsService) DeletePrice(modelKey string) ([]store.ModelPrice, error) {
	ctx := context.Background()
	if err := s.deps.DB.DeleteModelPrice(ctx, modelKey); err != nil {
		return nil, err
	}
	if _, err := s.reprice(ctx); err != nil {
		return nil, err
	}
	return s.deps.DB.ModelPrices(ctx)
}

// PriceRules lists the per-model multipliers.
func (s *SettingsService) PriceRules() ([]store.PriceRule, error) {
	return s.deps.DB.PriceRules(context.Background())
}

// SetPriceRule writes a per-model multiplier rule.
func (s *SettingsService) SetPriceRule(r store.PriceRule) ([]store.PriceRule, error) {
	ctx := context.Background()
	if err := s.deps.DB.UpsertPriceRule(ctx, r); err != nil {
		return nil, err
	}
	if _, err := s.reprice(ctx); err != nil {
		return nil, err
	}
	return s.deps.DB.PriceRules(ctx)
}

// SyncPrices downloads a price catalog and re-prices every estimated event.
//
// A failure here is reported to the caller but is not fatal to the application: with no
// catalog the affected events stay "unavailable" and the UI shows a dash.
func (s *SettingsService) SyncPrices(source string) (pricing.SyncResult, error) {
	ctx := context.Background()
	result, err := s.deps.Catalog.SyncFrom(ctx, s.deps.DB, source)
	if err != nil {
		return result, err
	}
	if err := s.deps.DB.SetSetting(ctx, store.SettingLastPriceSync,
		strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
		return result, err
	}
	if _, err := s.reprice(ctx); err != nil {
		return result, err
	}
	s.deps.emit(EventPricingSynced, PricingSyncedPayload{
		Source: result.Source, Models: result.Models, Skipped: result.Skipped,
	})
	return result, nil
}

// RecalculateCosts re-prices every event whose cost came from the price table.
func (s *SettingsService) RecalculateCosts() (int64, error) {
	return s.reprice(context.Background())
}

// reprice reloads the catalog, re-resolves estimated costs and rebuilds the rollups. Any
// price change has to go through here, because a cost change invalidates an unknown set of
// rollup rows.
func (s *SettingsService) reprice(ctx context.Context) (int64, error) {
	if err := s.deps.Catalog.Reload(ctx, s.deps.DB); err != nil {
		return 0, err
	}
	updated, err := s.deps.Catalog.Recalculate(ctx, s.deps.DB, nil)
	if err != nil {
		return updated, err
	}
	if err := s.deps.DB.RebuildAllRollups(ctx); err != nil {
		return updated, err
	}
	s.deps.emit(EventDataChanged, DataChangedPayload{})
	return updated, nil
}

// DeleteAllData wipes every derived row. The next scan rebuilds from the session files,
// which are never modified.
func (s *SettingsService) DeleteAllData() error {
	ctx := context.Background()
	if err := s.deps.DB.TruncateAll(ctx); err != nil {
		return err
	}
	if err := s.deps.DB.Vacuum(ctx); err != nil {
		return err
	}
	s.deps.emit(EventDataChanged, DataChangedPayload{})
	if s.deps.Scheduler != nil {
		s.deps.Scheduler.TriggerNow("data-reset")
	}
	return nil
}

// Stats returns the database summary shown on the settings page.
func (s *SettingsService) Stats() (store.Stats, error) {
	return s.deps.Engine.Stats(context.Background())
}
