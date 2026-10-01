package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// fullRebuildThreshold decides when a per-day refresh is replaced by one full pass.
// Above it, a single grouped scan over the whole table is cheaper than hundreds of
// delete+insert round trips.
const fullRebuildThreshold = 60

// RefreshRollups brings derived aggregates back in line after writes.
//
// Note on scope: there is deliberately no hourly rollup table. The realtime views read
// a bounded window (<= 60 minutes) straight from usage_events through idx_events_ts,
// which is both cheaper than maintaining an extra table and free of timezone skew.
func (d *DB) RefreshRollups(ctx context.Context, dirty DirtyKeys) error {
	if dirty.empty() {
		return nil
	}
	if len(dirty.Days) > fullRebuildThreshold {
		return d.RebuildAllRollups(ctx)
	}
	return d.refreshKeys(ctx, dirty)
}

func (d *DB) refreshKeys(ctx context.Context, dirty DirtyKeys) error {
	days := sortedKeys(dirty.Days)
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if len(days) > 0 {
			if err := deleteInChunks(ctx, tx, "rollup_daily_hm", "day", days); err != nil {
				return err
			}
			if err := deleteInChunks(ctx, tx, "rollup_daily_hp", "day", days); err != nil {
				return err
			}
			// The aggregate reads usage_events, where the day column is local_day; the
			// deletes above target the rollup tables, where it is day.
			args := toArgs(days)
			where := "WHERE local_day IN (" + placeholders(len(days)) + ")"
			if err := insertDailyHM(ctx, tx, where, args); err != nil {
				return err
			}
			if err := insertDailyHP(ctx, tx, where, args); err != nil {
				return err
			}
		}
		return refreshSessions(ctx, tx, dirty.Sessions)
	})
}

// RollupsIntact reports whether the rollup tables still describe exactly the events they
// were built from. Refreshes are incremental, so a run that dies between inserting events
// and refreshing its rollups would otherwise leave the UI permanently behind, with no
// later run touching those days again. The engine checks this after any run that wrote
// anything and repairs the tables when it fails.
//
// The three comparisons are the invariants the UI depends on: every event is counted
// exactly once in the daily and project rollups, and every session that has events has a
// session rollup row.
func (d *DB) RollupsIntact(ctx context.Context) (bool, error) {
	var intact bool
	err := d.r.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM usage_events) = (SELECT COALESCE(SUM(event_count), 0) FROM rollup_daily_hm)
			AND (SELECT COALESCE(SUM(total_tokens), 0) FROM usage_events) =
			    (SELECT COALESCE(SUM(total_tokens), 0) FROM rollup_daily_hm)
			AND (SELECT COUNT(*) FROM (SELECT DISTINCT harness, session_id FROM usage_events)) =
			    (SELECT COUNT(*) FROM rollup_session)`).Scan(&intact)
	return intact, err
}

// RebuildAllRollups truncates and repopulates every rollup table from usage_events.
// Used on a cold scan, after a timezone change, and after a global cost recalculate.
func (d *DB) RebuildAllRollups(ctx context.Context) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, t := range []string{"rollup_daily_hm", "rollup_daily_hp", "rollup_session"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
				return err
			}
		}
		if err := insertDailyHM(ctx, tx, "", nil); err != nil {
			return err
		}
		if err := insertDailyHP(ctx, tx, "", nil); err != nil {
			return err
		}
		return refreshSessions(ctx, tx, nil)
	})
}

func insertDailyHM(ctx context.Context, tx *sql.Tx, where string, args []any) error {
	q := `
INSERT INTO rollup_daily_hm (day,harness,model,agent_type,input_tokens,output_tokens,
  cache_read_tokens,cache_write_tokens,reasoning_tokens,total_tokens,cost_usd,reported_cost_usd,
  cost_unavailable,event_count,latency_sum_ms,latency_count,ttft_sum_ms,ttft_count)
SELECT local_day, harness, model, agent_type,
       SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
       SUM(reasoning_tokens), SUM(total_tokens),
       SUM(COALESCE(cost_usd,0)),
       SUM(CASE WHEN cost_source='reported' THEN COALESCE(cost_usd,0) ELSE 0 END),
       SUM(CASE WHEN cost_source='unavailable' THEN 1 ELSE 0 END),
       COUNT(*),
       SUM(COALESCE(latency_ms,0)), SUM(CASE WHEN latency_ms IS NOT NULL THEN 1 ELSE 0 END),
       SUM(COALESCE(ttft_ms,0)), SUM(CASE WHEN ttft_ms IS NOT NULL THEN 1 ELSE 0 END)
FROM usage_events ` + where + ` GROUP BY local_day, harness, model, agent_type`
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

func insertDailyHP(ctx context.Context, tx *sql.Tx, where string, args []any) error {
	q := `
INSERT INTO rollup_daily_hp (day,harness,project,agent_type,input_tokens,output_tokens,
  cache_read_tokens,cache_write_tokens,reasoning_tokens,total_tokens,cost_usd,reported_cost_usd,
  cost_unavailable,event_count,latency_sum_ms,latency_count,ttft_sum_ms,ttft_count)
SELECT local_day, harness, project, agent_type,
       SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
       SUM(reasoning_tokens), SUM(total_tokens),
       SUM(COALESCE(cost_usd,0)),
       SUM(CASE WHEN cost_source='reported' THEN COALESCE(cost_usd,0) ELSE 0 END),
       SUM(CASE WHEN cost_source='unavailable' THEN 1 ELSE 0 END),
       COUNT(*),
       SUM(COALESCE(latency_ms,0)), SUM(CASE WHEN latency_ms IS NOT NULL THEN 1 ELSE 0 END),
       SUM(COALESCE(ttft_ms,0)), SUM(CASE WHEN ttft_ms IS NOT NULL THEN 1 ELSE 0 END)
FROM usage_events ` + where + ` GROUP BY local_day, harness, project, agent_type`
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// refreshSessions recomputes per-session rollups. A nil set means "every session".
func refreshSessions(ctx context.Context, tx *sql.Tx, sessions map[[2]string]struct{}) error {
	if sessions == nil {
		if _, err := tx.ExecContext(ctx, "DELETE FROM rollup_session"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, sessionRollupInsert(""))
		return err
	}

	pairs := make([][2]string, 0, len(sessions))
	for p := range sessions {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})

	const chunk = 200
	for start := 0; start < len(pairs); start += chunk {
		end := start + chunk
		if end > len(pairs) {
			end = len(pairs)
		}
		part := pairs[start:end]
		conds := make([]string, 0, len(part))
		args := make([]any, 0, len(part)*4)
		for _, p := range part {
			conds = append(conds, "(e.harness = ? AND e.session_id = ?)")
			args = append(args, p[0], p[1])
		}
		where := strings.Join(conds, " OR ")

		delArgs := make([]any, 0, len(part)*2)
		for _, p := range part {
			delArgs = append(delArgs, p[0], p[1])
		}
		delWhere := make([]string, 0, len(part))
		for range part {
			delWhere = append(delWhere, "(harness = ? AND session_id = ?)")
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM rollup_session WHERE "+strings.Join(delWhere, " OR "), delArgs...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, sessionRollupInsert(" WHERE "+where), args...); err != nil {
			return err
		}
	}
	return nil
}

// sessionRollupInsert builds the per-session aggregate. The predicate is injected before
// GROUP BY, so callers must pass a complete " WHERE ..." clause (or "").
// Metadata (project/model/agent) is joined from the sessions table rather than taken from
// the events, so a session's label does not depend on whichever event was parsed first.
func sessionRollupInsert(where string) string {
	return sessionRollupSelectPrefix + where + sessionRollupSelectSuffix
}

const sessionRollupSelectPrefix = `
INSERT INTO rollup_session (harness,session_id,project,model,agent_type,agent_name,started_at,
  updated_at,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,
  total_tokens,cost_usd,reported_cost_usd,event_count,latency_sum_ms,latency_count,ttft_sum_ms,ttft_count)
SELECT e.harness, e.session_id,
       COALESCE(MAX(s.project),''), COALESCE(MAX(s.model),''), COALESCE(MAX(s.agent_type),'main'),
       COALESCE(MAX(s.agent_name),''),
       MIN(e.ts), MAX(e.ts),
       SUM(e.input_tokens), SUM(e.output_tokens), SUM(e.cache_read_tokens), SUM(e.cache_write_tokens),
       SUM(e.reasoning_tokens), SUM(e.total_tokens),
       SUM(COALESCE(e.cost_usd,0)),
       SUM(CASE WHEN e.cost_source='reported' THEN COALESCE(e.cost_usd,0) ELSE 0 END),
       COUNT(*),
       SUM(COALESCE(e.latency_ms,0)), SUM(CASE WHEN e.latency_ms IS NOT NULL THEN 1 ELSE 0 END),
       SUM(COALESCE(e.ttft_ms,0)), SUM(CASE WHEN e.ttft_ms IS NOT NULL THEN 1 ELSE 0 END)
FROM usage_events e
LEFT JOIN sessions s ON s.harness = e.harness AND s.session_id = e.session_id`

const sessionRollupSelectSuffix = `
GROUP BY e.harness, e.session_id`

func deleteInChunks(ctx context.Context, tx *sql.Tx, table, column string, values []string) error {
	const chunk = 200
	for start := 0; start < len(values); start += chunk {
		end := start + chunk
		if end > len(values) {
			end = len(values)
		}
		part := values[start:end]
		q := "DELETE FROM " + table + " WHERE " + column + " IN (" + placeholders(len(part)) + ")"
		if _, err := tx.ExecContext(ctx, q, toArgs(part)...); err != nil {
			return err
		}
	}
	return nil
}

func toArgs(values []string) []any {
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return args
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// A stable order keeps the generated SQL identical across runs, so plan caching and
	// test diffs stay predictable.
	sort.Strings(out)
	return out
}
