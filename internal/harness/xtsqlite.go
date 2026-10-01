package harness

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// XDBSpec describes a foreign (harness-owned) database to read.
type XDBSpec struct {
	Path    string
	Timeout time.Duration
}

// OpenReadOnly opens a harness database read-only. The dashboard never writes to,
// locks for write, or creates indexes in another tool's database: those files are live
// state for a running agent, and any write could corrupt it or block the agent.
func OpenReadOnly(spec XDBSpec) (*sql.DB, error) {
	if spec.Timeout <= 0 {
		spec.Timeout = 5 * time.Second
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)&_pragma=query_only(1)",
		strings.ReplaceAll(spec.Path, "?", "%3F"), spec.Timeout.Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// IsBusy reports whether an error means the database is momentarily locked by the
// harness that owns it. Callers must then keep the previous cursor and retry on the
// next sync, because a partially consumed query cannot be resumed safely.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked") ||
		errors.Is(err, os.ErrDeadlineExceeded)
}

// FileSignature returns inode, modification time (ms) and size for a path.
func FileSignature(path string) (inode, mtime, size int64, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, err
	}
	return FileInode(fi), fi.ModTime().UnixMilli(), fi.Size(), nil
}

// TableExists reports whether a table is present in an opened database.
func TableExists(db *sql.DB, name string) bool {
	var found string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, name).Scan(&found)
	return err == nil && found != ""
}
