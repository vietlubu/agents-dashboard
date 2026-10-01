package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"
)

// ScanState is the per-file (or per-foreign-database) incremental cursor.
type ScanState struct {
	Key           string
	Harness       string
	Kind          string // file | sqlite
	Mtime         int64
	Size          int64
	Inode         int64
	Offset        int64 // JSONL byte offset just past the last complete line
	Watermark     int64 // foreign-database rowid watermark
	ParserVersion int64
	LastScannedAt int64
	Err           string
}

// Unchanged reports whether a file's signature still matches what was parsed.
func (s ScanState) Unchanged(inode, mtime, size, parserVersion int64) bool {
	return s.Inode == inode && s.Mtime == mtime && s.Size == size && s.ParserVersion == parserVersion
}

// LoadScanStates returns every stored cursor for a harness, keyed by ScanState.Key.
func (d *DB) LoadScanStates(ctx context.Context, harness string) (map[string]ScanState, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT key,harness,kind,mtime,size,inode,offset,watermark,parser_version,last_scanned_at,error
		 FROM scan_state WHERE harness = ?`, harness)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ScanState{}
	for rows.Next() {
		var s ScanState
		if err := rows.Scan(&s.Key, &s.Harness, &s.Kind, &s.Mtime, &s.Size, &s.Inode, &s.Offset,
			&s.Watermark, &s.ParserVersion, &s.LastScannedAt, &s.Err); err != nil {
			return nil, err
		}
		out[s.Key] = s
	}
	return out, rows.Err()
}

const upsertScanStateSQL = `
INSERT INTO scan_state (key,harness,kind,mtime,size,inode,offset,watermark,parser_version,last_scanned_at,error)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(key) DO UPDATE SET
  harness=excluded.harness, kind=excluded.kind, mtime=excluded.mtime, size=excluded.size,
  inode=excluded.inode, offset=excluded.offset, watermark=excluded.watermark,
  parser_version=excluded.parser_version, last_scanned_at=excluded.last_scanned_at,
  error=excluded.error`

// SaveScanStates persists cursors. Only call it after the corresponding events were
// committed: a stored cursor newer than the events would silently lose usage.
func (d *DB) SaveScanStates(ctx context.Context, states []ScanState) error {
	if len(states) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, upsertScanStateSQL)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, s := range states {
			if s.Key == "" {
				continue
			}
			if s.LastScannedAt == 0 {
				s.LastScannedAt = now
			}
			if _, err := stmt.ExecContext(ctx, s.Key, s.Harness, s.Kind, s.Mtime, s.Size, s.Inode,
				s.Offset, s.Watermark, s.ParserVersion, s.LastScannedAt, s.Err); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneScanStates drops file cursors whose path no longer exists, so a deleted session
// cannot keep a row forever. Foreign-database cursors are left alone: they are not
// path-existence driven.
func (d *DB) PruneScanStates(ctx context.Context, harness string) (int64, error) {
	states, err := d.LoadScanStates(ctx, harness)
	if err != nil {
		return 0, err
	}
	var removed int64
	for key, s := range states {
		if s.Kind != "file" {
			continue
		}
		if len(key) <= len("file:") {
			continue
		}
		if _, err := os.Stat(filepath.FromSlash(key[len("file:"):])); err == nil {
			continue
		}
		if _, err := d.w.ExecContext(ctx, `DELETE FROM scan_state WHERE key = ?`, key); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// ScanRoots returns the user-configured extra roots for a harness.
func (d *DB) ScanRoots(ctx context.Context, harness string) ([]ScanRoot, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT harness, path FROM scan_roots WHERE harness = ? ORDER BY path`, harness)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScanRoot
	for rows.Next() {
		var r ScanRoot
		if err := rows.Scan(&r.Harness, &r.Path); err != nil {
			return nil, err
		}
		if _, err := os.Stat(r.Path); err == nil {
			r.Exists = true
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AllScanRoots returns every configured extra root.
func (d *DB) AllScanRoots(ctx context.Context) ([]ScanRoot, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT harness, path FROM scan_roots ORDER BY harness, path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScanRoot{}
	for rows.Next() {
		var r ScanRoot
		if err := rows.Scan(&r.Harness, &r.Path); err != nil {
			return nil, err
		}
		if _, err := os.Stat(r.Path); err == nil {
			r.Exists = true
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddScanRoot records an extra root for a harness.
func (d *DB) AddScanRoot(ctx context.Context, harness, path string) error {
	_, err := d.w.ExecContext(ctx,
		`INSERT INTO scan_roots (harness, path) VALUES (?, ?) ON CONFLICT(harness, path) DO NOTHING`,
		harness, path)
	return err
}

// RemoveScanRoot forgets an extra root.
func (d *DB) RemoveScanRoot(ctx context.Context, harness, path string) error {
	_, err := d.w.ExecContext(ctx, `DELETE FROM scan_roots WHERE harness = ? AND path = ?`, harness, path)
	return err
}
