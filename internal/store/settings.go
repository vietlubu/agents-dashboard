package store

import (
	"context"
	"database/sql"
	"strconv"
)

// Setting keys persisted in the settings table. The service layer owns defaults and
// validation; the store is a typed key/value bag.
const (
	SettingTZ             = "tz"
	SettingIdleIntervalS  = "idle_interval_seconds"
	SettingBurstIntervalS = "burst_interval_seconds"
	SettingBurstWindowS   = "burst_window_seconds"
	SettingConcurrency    = "concurrency"
	SettingAutoSyncPrices = "auto_sync_prices"
	SettingLastPriceSync  = "last_price_sync"
	SettingServerHost     = "server_host"
	SettingServerPort     = "server_port"
	SettingTheme          = "theme"
	SettingLocale         = "locale"

	// Sleep control. Disabled by default: keeping a machine awake is an opt-in power
	// behaviour, not something a usage tracker should do unasked.
	SettingSleepMode             = "sleep_mode"
	SettingSleepAfterS           = "sleep_after_seconds"
	SettingSleepActiveWindowS    = "sleep_active_window_seconds"
	SettingPreventSystemSleep    = "prevent_system_sleep"
	SettingPreventDisplaySleep   = "prevent_display_sleep"
	SettingPreventLidClosedSleep = "prevent_lid_closed_sleep"
	// SettingWaitForMedia defers auto-sleep while audio or video is playing.
	SettingWaitForMedia = "sleep_wait_for_media"
)

// GetSetting reads one setting.
func (d *DB) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.r.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting writes one setting.
func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.w.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// AllSettings returns every stored setting.
func (d *DB) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SettingInt parses an integer setting, returning def when absent or invalid.
func (d *DB) SettingInt(ctx context.Context, key string, def int64) (int64, error) {
	v, ok, err := d.GetSetting(ctx, key)
	if err != nil || !ok {
		return def, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def, nil
	}
	return n, nil
}

// SettingBool parses a boolean setting, returning def when absent or invalid.
func (d *DB) SettingBool(ctx context.Context, key string, def bool) (bool, error) {
	v, ok, err := d.GetSetting(ctx, key)
	if err != nil || !ok {
		return def, err
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, nil
	}
	return b, nil
}
