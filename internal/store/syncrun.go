package store

import (
	"context"
	"time"
)

// StartSyncRun records the beginning of a sync and returns its id.
func (d *DB) StartSyncRun(ctx context.Context, trigger string) (int64, error) {
	res, err := d.w.ExecContext(ctx,
		`INSERT INTO sync_runs (started_at, trigger) VALUES (?, ?)`, time.Now().UnixMilli(), trigger)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishSyncRun records the outcome of a sync.
func (d *DB) FinishSyncRun(ctx context.Context, id, startedAt int64, walked, changed, inserted, updated int, errText string) error {
	_, err := d.w.ExecContext(ctx,
		`UPDATE sync_runs SET finished_at=?, files_walked=?, files_changed=?, events_inserted=?,
		 events_updated=?, duration_ms=?, error=? WHERE id=?`,
		time.Now().UnixMilli(), walked, changed, inserted, updated,
		time.Since(time.UnixMilli(startedAt)).Milliseconds(), errText, id)
	return err
}

// SyncHistory returns the most recent sync runs, newest first.
func (d *DB) SyncHistory(ctx context.Context, limit int) ([]SyncRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.r.QueryContext(ctx,
		`SELECT id,started_at,finished_at,trigger,files_walked,files_changed,events_inserted,
		        events_updated,duration_ms,error
		 FROM sync_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncRun{}
	for rows.Next() {
		var r SyncRun
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Trigger, &r.FilesWalked,
			&r.FilesChanged, &r.EventsInserted, &r.EventsUpdated, &r.DurationMs, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestSyncRun returns the newest recorded run.
func (d *DB) LatestSyncRun(ctx context.Context) (SyncRun, bool, error) {
	runs, err := d.SyncHistory(ctx, 1)
	if err != nil || len(runs) == 0 {
		return SyncRun{}, false, err
	}
	return runs[0], true, nil
}

// TrimSyncHistory keeps the log bounded; the table is diagnostics only.
func (d *DB) TrimSyncHistory(ctx context.Context, keep int) error {
	if keep <= 0 {
		keep = 200
	}
	_, err := d.w.ExecContext(ctx,
		`DELETE FROM sync_runs WHERE id NOT IN (SELECT id FROM sync_runs ORDER BY id DESC LIMIT ?)`, keep)
	return err
}
