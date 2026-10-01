package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB owns the dashboard's own database. Two handles: a single-connection writer
// (SQLite allows one writer) and a small read pool, so UI reads do not queue behind
// a scan's write transaction.
type DB struct {
	w      *sql.DB
	r      *sql.DB
	dbPath string
}

const writerPragmas = "_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=temp_store(FILE)" +
	"&_pragma=mmap_size(0)" +
	// A scan writes in batches; capping the WAL keeps the on-disk footprint close to the
	// database size instead of letting the log grow with the scan.
	"&_pragma=journal_size_limit(16777216)"

const readerPragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=query_only(1)" +
	"&_pragma=mmap_size(0)"

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path string) (*DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	writer, err := sql.Open("sqlite", "file:"+dsnPath(path)+"?"+writerPragmas)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	if err := writer.Ping(); err != nil {
		writer.Close()
		return nil, fmt.Errorf("ping writer: %w", err)
	}
	if err := migrate(context.Background(), writer); err != nil {
		writer.Close()
		return nil, err
	}

	reader, err := sql.Open("sqlite", "file:"+dsnPath(path)+"?mode=ro&"+readerPragmas)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("open reader: %w", err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	if err := reader.Ping(); err != nil {
		reader.Close()
		writer.Close()
		return nil, fmt.Errorf("ping reader: %w", err)
	}

	return &DB{w: writer, r: reader, dbPath: path}, nil
}

// dsnPath makes a filesystem path safe to embed in a file: URI.
func dsnPath(path string) string {
	if path == ":memory:" {
		return ":memory:"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return abs
}

// Path is the on-disk location of the database.
func (d *DB) Path() string { return d.dbPath }

func (d *DB) Close() error {
	var err error
	if d.r != nil {
		if cerr := d.r.Close(); cerr != nil {
			err = cerr
		}
	}
	if d.w != nil {
		if cerr := d.w.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

// Writer returns the single-writer handle. Use it for every mutation, including
// rollup refreshes and PRAGMA maintenance.
func (d *DB) Writer() *sql.DB { return d.w }

// Reader returns the read-only pool used by all query paths.
func (d *DB) Reader() *sql.DB { return d.r }

// WithTx runs fn inside a write transaction.
func (d *DB) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Optimize runs SQLite's own statistics/plan maintenance. Cheap when nothing changed.
func (d *DB) Optimize(ctx context.Context) error {
	_, err := d.w.ExecContext(ctx, "PRAGMA optimize")
	return err
}

// Vacuum compacts the file. Only called from the destructive "delete all data" path.
func (d *DB) Vacuum(ctx context.Context) error {
	_, err := d.w.ExecContext(ctx, "VACUUM")
	return err
}

// FileSize returns the database file size in bytes (including the WAL).
func (d *DB) FileSize() int64 {
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(d.dbPath + suffix); err == nil {
			total += fi.Size()
		}
	}
	return total
}
