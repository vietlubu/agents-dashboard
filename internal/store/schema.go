package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// schemaVersion is bumped whenever schemaV1 (or a later step) changes shape.
const schemaVersion = 1

// schemaV1 is applied statement by statement so a failure names the offending table.
//
// Deviations from the approved plan's DDL, and why:
//   - rollup_session IS kept (see rollups.go): the Sessions page reads it directly so the
//     page cost does not grow with the event table. Its range filter applies to
//     updated_at (session active within the range) while its totals are lifetime sums.
//   - rollup_hourly_hm from the plan is dropped: the realtime views read a bounded
//     window straight from usage_events (idx_events_ts), which needs no extra table and
//     has no timezone skew between the bucket key and the rollup key.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS usage_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_key TEXT NOT NULL UNIQUE,
  harness TEXT NOT NULL,
  source_file TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  project TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL DEFAULT '',
  agent_type TEXT NOT NULL DEFAULT 'main',
  agent_name TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL DEFAULT 'unknown',
  ts INTEGER NOT NULL,
  local_day TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL,
  cost_source TEXT NOT NULL DEFAULT 'unavailable',
  latency_ms INTEGER,
  ttft_ms INTEGER,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON usage_events(ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_harness_ts ON usage_events(harness, ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_model_ts ON usage_events(model, ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_project_ts ON usage_events(project, ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_session ON usage_events(harness, session_id);
CREATE INDEX IF NOT EXISTS idx_events_source ON usage_events(source_file);
CREATE INDEX IF NOT EXISTS idx_events_cost_source ON usage_events(cost_source, model);

CREATE TABLE IF NOT EXISTS sessions (
  harness TEXT NOT NULL,
  session_id TEXT NOT NULL,
  parent_session_id TEXT NOT NULL DEFAULT '',
  project TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  agent_type TEXT NOT NULL DEFAULT 'main',
  agent_name TEXT NOT NULL DEFAULT '',
  source_file TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (harness, session_id)
);
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);

CREATE TABLE IF NOT EXISTS scan_state (
  key TEXT PRIMARY KEY,
  harness TEXT NOT NULL,
  kind TEXT NOT NULL,
  mtime INTEGER NOT NULL DEFAULT 0,
  size INTEGER NOT NULL DEFAULT 0,
  inode INTEGER NOT NULL DEFAULT 0,
  offset INTEGER NOT NULL DEFAULT 0,
  watermark INTEGER NOT NULL DEFAULT 0,
  parser_version INTEGER NOT NULL DEFAULT 0,
  last_scanned_at INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_scan_state_harness ON scan_state(harness);

CREATE TABLE IF NOT EXISTS scan_roots (
  harness TEXT NOT NULL,
  path TEXT NOT NULL,
  PRIMARY KEY (harness, path)
);

CREATE TABLE IF NOT EXISTS model_prices (
  model_key TEXT PRIMARY KEY,
  input_per_m REAL NOT NULL DEFAULT 0,
  output_per_m REAL NOT NULL DEFAULT 0,
  cache_read_per_m REAL NOT NULL DEFAULT 0,
  cache_write_per_m REAL NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'manual',
  updated_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS price_rules (
  model_key TEXT PRIMARY KEY,
  input_mult REAL NOT NULL DEFAULT 1,
  output_mult REAL NOT NULL DEFAULT 1,
  cache_read_mult REAL NOT NULL DEFAULT 1,
  cache_write_mult REAL NOT NULL DEFAULT 1,
  disabled INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS rollup_daily_hm (
  day TEXT NOT NULL, harness TEXT NOT NULL, model TEXT NOT NULL, agent_type TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0, reported_cost_usd REAL NOT NULL DEFAULT 0,
  cost_unavailable INTEGER NOT NULL DEFAULT 0,
  event_count INTEGER NOT NULL DEFAULT 0,
  latency_sum_ms INTEGER NOT NULL DEFAULT 0, latency_count INTEGER NOT NULL DEFAULT 0,
  ttft_sum_ms INTEGER NOT NULL DEFAULT 0, ttft_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, harness, model, agent_type)
);

CREATE TABLE IF NOT EXISTS rollup_daily_hp (
  day TEXT NOT NULL, harness TEXT NOT NULL, project TEXT NOT NULL, agent_type TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0, reported_cost_usd REAL NOT NULL DEFAULT 0,
  cost_unavailable INTEGER NOT NULL DEFAULT 0,
  event_count INTEGER NOT NULL DEFAULT 0,
  latency_sum_ms INTEGER NOT NULL DEFAULT 0, latency_count INTEGER NOT NULL DEFAULT 0,
  ttft_sum_ms INTEGER NOT NULL DEFAULT 0, ttft_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, harness, project, agent_type)
);

CREATE TABLE IF NOT EXISTS rollup_session (
  harness TEXT NOT NULL, session_id TEXT NOT NULL,
  project TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
  agent_type TEXT NOT NULL DEFAULT 'main', agent_name TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0, reported_cost_usd REAL NOT NULL DEFAULT 0,
  event_count INTEGER NOT NULL DEFAULT 0,
  latency_sum_ms INTEGER NOT NULL DEFAULT 0, latency_count INTEGER NOT NULL DEFAULT 0,
  ttft_sum_ms INTEGER NOT NULL DEFAULT 0, ttft_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (harness, session_id)
);
CREATE INDEX IF NOT EXISTS idx_rollup_session_updated ON rollup_session(updated_at DESC);

CREATE TABLE IF NOT EXISTS sync_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0,
  trigger TEXT NOT NULL,
  files_walked INTEGER NOT NULL DEFAULT 0, files_changed INTEGER NOT NULL DEFAULT 0,
  events_inserted INTEGER NOT NULL DEFAULT 0, events_updated INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

func migrate(ctx context.Context, w *sql.DB) error {
	var version int
	if err := w.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version >= schemaVersion {
		return nil
	}
	for _, stmt := range strings.Split(schemaV1, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := w.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema (%s): %w", firstLine(stmt), err)
		}
	}
	if _, err := w.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
