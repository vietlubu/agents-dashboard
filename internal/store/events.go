package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Event is one normalized usage record ready to persist.
type Event struct {
	EventKey   string
	Harness    string
	SourceFile string
	SessionID  string
	Project    string
	Model      string
	Provider   string
	AgentType  string
	AgentName  string
	Outcome    string
	// TS is epoch milliseconds (UTC). Day/hour buckets are derived in the configured
	// location by the store, so callers cannot disagree with the rollup keys.
	TS int64

	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
	Total      int64

	// CostUSD nil means "no priced cost"; CostSource is reported|estimated|unavailable.
	// A reported zero must survive as a real cost, so the pointer is load-bearing.
	CostUSD    *float64
	CostSource string

	LatencyMs *int64
	TTFTMs    *int64
}

// DirtyKeys names the rollup rows invalidated by a write.
type DirtyKeys struct {
	Days     map[string]struct{}
	Sessions map[[2]string]struct{}
}

// NewDirtyKeys returns an empty dirty set for a caller that accumulates over a scan.
func NewDirtyKeys() DirtyKeys { return newDirtyKeys() }

func newDirtyKeys() DirtyKeys {
	return DirtyKeys{
		Days:     map[string]struct{}{},
		Sessions: map[[2]string]struct{}{},
	}
}

// Merge folds another set into this one.
func (d *DirtyKeys) Merge(other DirtyKeys) {
	for k := range other.Days {
		d.Days[k] = struct{}{}
	}
	for k := range other.Sessions {
		d.Sessions[k] = struct{}{}
	}
}

func (d *DirtyKeys) empty() bool {
	return len(d.Days) == 0 && len(d.Sessions) == 0
}

const insertEventColumns = `(event_key,harness,source_file,session_id,project,model,provider,agent_type,
  agent_name,outcome,ts,local_day,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
  reasoning_tokens,total_tokens,cost_usd,cost_source,latency_ms,ttft_ms,created_at)`

// insertConflictClause keeps the row with the larger total, so a streamed partial record
// never replaces the completed one, and never moves the timestamp.
const insertConflictClause = `
ON CONFLICT(event_key) DO UPDATE SET
  input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
  cache_read_tokens=excluded.cache_read_tokens, cache_write_tokens=excluded.cache_write_tokens,
  reasoning_tokens=excluded.reasoning_tokens, total_tokens=excluded.total_tokens,
  cost_usd=excluded.cost_usd, cost_source=excluded.cost_source,
  source_file=excluded.source_file, project=excluded.project, model=excluded.model,
  provider=excluded.provider, outcome=excluded.outcome, agent_name=excluded.agent_name,
  latency_ms=COALESCE(usage_events.latency_ms, excluded.latency_ms),
  ttft_ms=COALESCE(usage_events.ttft_ms, excluded.ttft_ms)
WHERE excluded.total_tokens > usage_events.total_tokens`

// eventColumnsPerRow is the parameter count of one row of insertEventColumns.
const eventColumnsPerRow = 23

// maxInsertParams bounds one INSERT statement. SQLite's default parameter limit is 32766;
// staying well under it keeps the statement valid across builds.
const maxInsertParams = 10000

// insertRowsPerStatement is how many events go into one multi-row INSERT. Batching matters
// for more than speed: a per-row Exec makes database/sql allocate a fresh argument slice
// every time, which was the single largest source of allocation during a cold scan.
const insertRowsPerStatement = maxInsertParams / eventColumnsPerRow

// InsertEvents upserts a batch. A conflicting event_key keeps the row with the larger
// total (streaming duplicates collapse to the completed record) and never moves ts.
// The returned DirtyKeys name every rollup row the batch invalidated, and the count is how
// many events were new rather than updates.
func (d *DB) InsertEvents(ctx context.Context, evs []Event, loc *time.Location) (DirtyKeys, int, error) {
	dirty := newDirtyKeys()
	if len(evs) == 0 {
		return dirty, 0, nil
	}

	existing, err := d.existingKeys(ctx, evs)
	if err != nil {
		return dirty, 0, err
	}

	now := time.Now().UnixMilli()
	inserted := 0
	for _, e := range evs {
		if _, seen := existing[e.EventKey]; !seen {
			inserted++
		}
	}

	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for start := 0; start < len(evs); start += insertRowsPerStatement {
			end := start + insertRowsPerStatement
			if end > len(evs) {
				end = len(evs)
			}
			if err := insertEventChunk(ctx, tx, evs[start:end], loc, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return newDirtyKeys(), 0, err
	}

	for i := range evs {
		e := &evs[i]
		dirty.Days[LocalDay(e.TS, loc)] = struct{}{}
		dirty.Sessions[[2]string{e.Harness, e.SessionID}] = struct{}{}
	}
	return dirty, inserted, nil
}

// insertEventChunk writes up to insertRowsPerStatement events in one statement.
func insertEventChunk(ctx context.Context, tx *sql.Tx, evs []Event, loc *time.Location, now int64) error {
	var sb strings.Builder
	sb.Grow(len("INSERT INTO usage_events ") + len(insertEventColumns) + len(insertConflictClause) +
		len(evs)*32)
	sb.WriteString("INSERT INTO usage_events ")
	sb.WriteString(insertEventColumns)
	sb.WriteString(" VALUES ")
	args := make([]any, 0, len(evs)*eventColumnsPerRow)
	for i := range evs {
		e := &evs[i]
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			e.EventKey, e.Harness, e.SourceFile, e.SessionID, e.Project, e.Model, e.Provider,
			e.AgentType, e.AgentName, e.Outcome, e.TS, LocalDay(e.TS, loc),
			e.Input, e.Output, e.CacheRead, e.CacheWrite, e.Reasoning, e.Total,
			e.CostUSD, e.CostSource, e.LatencyMs, e.TTFTMs, now,
		)
	}
	sb.WriteString(insertConflictClause)
	if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert %d events: %w", len(evs), err)
	}
	return nil
}

// existingKeys returns the subset of event keys already present, in chunks so the
// IN list never grows unbounded.
func (d *DB) existingKeys(ctx context.Context, evs []Event) (map[string]struct{}, error) {
	existing := make(map[string]struct{}, len(evs))
	const chunk = 400
	keys := make([]string, 0, len(evs))
	for _, e := range evs {
		keys = append(keys, e.EventKey)
	}
	for start := 0; start < len(keys); start += chunk {
		end := start + chunk
		if end > len(keys) {
			end = len(keys)
		}
		part := keys[start:end]
		args := make([]any, len(part))
		for i, k := range part {
			args[i] = k
		}
		query := "SELECT event_key FROM usage_events WHERE event_key IN (" + placeholders(len(part)) + ")"
		rows, err := d.r.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return nil, err
			}
			existing[k] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return existing, nil
}

// DeleteEventsBySource removes every event parsed from one file. Used when a session
// file was truncated or replaced, so the offsets no longer describe the file's content.
// The dirty keys of the removed rows are returned so rollups can be refreshed.
func (d *DB) DeleteEventsBySource(ctx context.Context, harness, sourceFile string) (DirtyKeys, error) {
	dirty := newDirtyKeys()

	rows, err := d.r.QueryContext(ctx,
		`SELECT DISTINCT local_day, harness, session_id FROM usage_events
		 WHERE harness = ? AND source_file = ?`, harness, sourceFile)
	if err != nil {
		return dirty, err
	}
	for rows.Next() {
		var day, h, session string
		if err := rows.Scan(&day, &h, &session); err != nil {
			rows.Close()
			return dirty, err
		}
		dirty.Days[day] = struct{}{}
		dirty.Sessions[[2]string{h, session}] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return dirty, err
	}
	rows.Close()

	if _, err := d.w.ExecContext(ctx,
		`DELETE FROM usage_events WHERE harness = ? AND source_file = ?`, harness, sourceFile); err != nil {
		return dirty, err
	}
	return dirty, nil
}

// ClearHarness drops everything derived from one harness. Used when a parser version
// changes, because stored byte offsets no longer describe what the parser now reads.
func (d *DB) ClearHarness(ctx context.Context, harness string) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`DELETE FROM usage_events WHERE harness = ?`,
			`DELETE FROM sessions WHERE harness = ?`,
			`DELETE FROM scan_state WHERE harness = ?`,
			`DELETE FROM rollup_daily_hm WHERE harness = ?`,
			`DELETE FROM rollup_daily_hp WHERE harness = ?`,
			`DELETE FROM rollup_session WHERE harness = ?`,
		} {
			if _, err := tx.ExecContext(ctx, stmt, harness); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecomputeLocalDays rewrites local_day for every event in the given location. Called
// only when the configured timezone changes, which invalidates all day buckets.
func (d *DB) RecomputeLocalDays(ctx context.Context, loc *time.Location, progress func(done, total int64)) error {
	var total int64
	if err := d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`).Scan(&total); err != nil {
		return err
	}
	if total == 0 {
		return nil
	}

	var lastID int64
	var done int64
	const batch = 5000
	for {
		rows, err := d.r.QueryContext(ctx,
			`SELECT id, ts FROM usage_events WHERE id > ? ORDER BY id LIMIT ?`, lastID, batch)
		if err != nil {
			return err
		}
		type pair struct {
			id int64
			ts int64
		}
		var batchRows []pair
		for rows.Next() {
			var p pair
			if err := rows.Scan(&p.id, &p.ts); err != nil {
				rows.Close()
				return err
			}
			batchRows = append(batchRows, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(batchRows) == 0 {
			break
		}

		err = d.WithTx(ctx, func(tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(ctx, `UPDATE usage_events SET local_day = ? WHERE id = ?`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, p := range batchRows {
				if _, err := stmt.ExecContext(ctx, LocalDay(p.ts, loc), p.id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}

		lastID = batchRows[len(batchRows)-1].id
		done += int64(len(batchRows))
		if progress != nil {
			progress(done, total)
		}
	}
	return nil
}

// TruncateAll wipes every derived table. Used by "delete all data".
func (d *DB) TruncateAll(ctx context.Context) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{
			"usage_events", "sessions", "scan_state", "rollup_daily_hm",
			"rollup_daily_hp", "rollup_session", "sync_runs",
		} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		return nil
	})
}

// LocalDay formats an epoch-millisecond timestamp as YYYY-MM-DD in loc.
func LocalDay(tsMs int64, loc *time.Location) string {
	return time.UnixMilli(tsMs).In(loc).Format("2006-01-02")
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
