package store

import (
	"context"
	"database/sql"
)

// Session is one harness session's identity and static metadata.
type Session struct {
	Harness         string
	SessionID       string
	ParentSessionID string
	Project         string
	Model           string
	AgentType       string
	AgentName       string
	SourceFile      string
	StartedAt       int64
	UpdatedAt       int64
}

const upsertSessionSQL = `
INSERT INTO sessions (harness,session_id,parent_session_id,project,model,agent_type,agent_name,
  source_file,started_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(harness,session_id) DO UPDATE SET
  parent_session_id=COALESCE(NULLIF(excluded.parent_session_id,''), sessions.parent_session_id),
  project=COALESCE(NULLIF(excluded.project,''), sessions.project),
  model=COALESCE(NULLIF(excluded.model,''), sessions.model),
  agent_type=COALESCE(NULLIF(excluded.agent_type,''), sessions.agent_type),
  agent_name=COALESCE(NULLIF(excluded.agent_name,''), sessions.agent_name),
  source_file=COALESCE(NULLIF(excluded.source_file,''), sessions.source_file),
  started_at=CASE
    WHEN sessions.started_at = 0 THEN excluded.started_at
    WHEN excluded.started_at = 0 THEN sessions.started_at
    WHEN excluded.started_at < sessions.started_at THEN excluded.started_at
    ELSE sessions.started_at END,
  updated_at=MAX(sessions.updated_at, excluded.updated_at)`

// UpsertSessions records session metadata, keeping the earliest start and latest update.
func (d *DB) UpsertSessions(ctx context.Context, sessions []Session) error {
	if len(sessions) == 0 {
		return nil
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, upsertSessionSQL)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, s := range sessions {
			if s.Harness == "" || s.SessionID == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, s.Harness, s.SessionID, s.ParentSessionID, s.Project,
				s.Model, s.AgentType, s.AgentName, s.SourceFile, s.StartedAt, s.UpdatedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// KnownSessionIDs returns the session ids already recorded for a harness, so an
// adapter can decide whether it still needs to read a session's header.
func (d *DB) KnownSessionIDs(ctx context.Context, harness string) (map[string]struct{}, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT session_id FROM sessions WHERE harness = ?`, harness)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// SessionDetail returns one session's identity row.
func (d *DB) SessionDetail(ctx context.Context, harness, sessionID string) (Session, bool, error) {
	var s Session
	err := d.r.QueryRowContext(ctx,
		`SELECT harness, session_id, parent_session_id, project, model, agent_type, agent_name,
		        source_file, started_at, updated_at
		 FROM sessions WHERE harness = ? AND session_id = ?`, harness, sessionID).
		Scan(&s.Harness, &s.SessionID, &s.ParentSessionID, &s.Project, &s.Model, &s.AgentType,
			&s.AgentName, &s.SourceFile, &s.StartedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return s, true, nil
}

// PruneEmptySessions deletes sessions that have no usage events behind them. They can
// only come from a transcript that never recorded a counted turn, and a row whose
// timestamps are zero matches no date range, so keeping them is noise rather than data.
func (d *DB) PruneEmptySessions(ctx context.Context) (int64, error) {
	res, err := d.w.ExecContext(ctx, `
		DELETE FROM sessions
		WHERE NOT EXISTS (
			SELECT 1 FROM usage_events e
			WHERE e.harness = sessions.harness AND e.session_id = sessions.session_id
		)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SessionCount returns how many sessions a harness has recorded.
func (d *DB) SessionCount(ctx context.Context, harness string) (int64, error) {
	var n int64
	err := d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE harness = ?`, harness).Scan(&n)
	return n, err
}
