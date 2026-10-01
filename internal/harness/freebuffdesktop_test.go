package harness

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

type fbThreadRow struct {
	id, projectPath, model, lastOutcome string
	created, updated                    int64
}

type fbMessageRow struct {
	threadID    string
	requestID   string
	role        string
	metricsJSON string
	ts          int64
}

// freebuffDesktopFixture builds a desktop-v2.db with the subset of the app's schema the
// adapter reads, under the real per-project directory layout.
func freebuffDesktopFixture(t *testing.T, slug string, threads []fbThreadRow, messages []fbMessageRow) (root, dbPath string) {
	t.Helper()
	home := t.TempDir()
	root = filepath.Join(home, ".config", "freebuff-desktop", "projects")
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath = filepath.Join(dir, "desktop-v2.db")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE projects (
		id TEXT PRIMARY KEY, root_path TEXT NOT NULL, default_branch TEXT NOT NULL DEFAULT 'main',
		created_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create projects: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY, project_id TEXT NOT NULL DEFAULT '', project_path TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '', model TEXT, agent_mode TEXT NOT NULL DEFAULT 'build',
		last_turn_outcome TEXT, created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create threads: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE messages (
		seq INTEGER PRIMARY KEY AUTOINCREMENT, thread_id TEXT NOT NULL, request_id TEXT,
		role TEXT NOT NULL, parts_json TEXT NOT NULL DEFAULT '[]',
		attachments_json TEXT NOT NULL DEFAULT '[]', metrics_json TEXT NOT NULL DEFAULT '{}',
		ts INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create messages: %v", err)
	}
	for _, th := range threads {
		if _, err := db.Exec(
			`INSERT INTO threads (id, project_path, model, last_turn_outcome, created_at, updated_at)
			 VALUES (?,?,?,?,?,?)`,
			th.id, th.projectPath, th.model, th.lastOutcome, th.created, th.updated); err != nil {
			t.Fatalf("insert thread: %v", err)
		}
	}
	for _, m := range messages {
		requestID := sql.NullString{String: m.requestID, Valid: m.requestID != ""}
		if _, err := db.Exec(
			`INSERT INTO messages (thread_id, request_id, role, metrics_json, ts) VALUES (?,?,?,?,?)`,
			m.threadID, requestID, m.role, m.metricsJSON, m.ts); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	return root, dbPath
}

func (e fbEnv) adapter() *freebuffDesktopAdapter { return &freebuffDesktopAdapter{home: ""} }

type fbEnv struct{}

// Only assistant rows with a recognized metrics ledger produce events; a user row and an
// empty ledger still contribute their thread, so Desktop activity is visible even before
// the app records accounting.
func TestFreebuffDesktopReadsAssistantMetrics(t *testing.T) {
	ts := int64(1_790_870_252_587)
	root, _ := freebuffDesktopFixture(t, "agent-dashboard-0bf6f741", []fbThreadRow{
		{id: "th_1", projectPath: "/Users/me/Projects/agent-dashboard", model: "m-096e75164d",
			lastOutcome: "completed", created: ts, updated: ts + 60_000},
	}, []fbMessageRow{
		{threadID: "th_1", requestID: "req_1", role: "assistant", ts: ts,
			metricsJSON: `{"context":{"usedTokens":184824,"windowTokens":1048576},"usage":{"inputTokens":3602665,"cachedInputTokens":3444480,"outputTokens":29507,"reasoningOutputTokens":18105,"totalTokens":3632172},"costUsd":0}`},
		{threadID: "th_1", role: "user", ts: ts + 1, metricsJSON: `{}`},
		{threadID: "th_1", role: "assistant", ts: ts + 2, metricsJSON: `{}`},
	})

	events, sessions, _, states := scanFixture(t, fbEnv{}.adapter(), root, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want only the accounted assistant row", events)
	}
	e := events[0]
	if e.EventKey != "freebuff-desktop|th_1|req_1" {
		t.Errorf("key = %q", e.EventKey)
	}
	// inputTokens carries the cached portion, so the stored input is the uncached
	// remainder; reasoning is a subset of output and excluded from the total.
	if e.Input != 3602665-3444480 || e.Output != 29507 || e.CacheRead != 3444480 {
		t.Errorf("buckets = %d/%d/%d, want 158185/29507/3444480", e.Input, e.Output, e.CacheRead)
	}
	if e.Reasoning != 18105 {
		t.Errorf("reasoning = %d", e.Reasoning)
	}
	if e.Total != 3632172 {
		t.Errorf("total = %d, want the app's own totalTokens", e.Total)
	}
	if e.Project != "/Users/me/Projects/agent-dashboard" || e.SessionID != "th_1" {
		t.Errorf("identity = %q/%q", e.Project, e.SessionID)
	}
	if e.Model != "m-096e75164d" {
		t.Errorf("model = %q, want the thread's model", e.Model)
	}
	if e.Outcome != "ok" {
		t.Errorf("outcome = %q, want the thread outcome normalized", e.Outcome)
	}
	if e.CostUSD != nil {
		t.Errorf("cost = %v, want none", e.CostUSD)
	}

	if len(sessions) != 1 || sessions[0].SessionID != "th_1" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if sessions[0].StartedAt != ts || sessions[0].UpdatedAt != ts+60_000 {
		t.Errorf("session timestamps = %d..%d", sessions[0].StartedAt, sessions[0].UpdatedAt)
	}

	var watermark int64
	for _, st := range states {
		if st.Watermark > watermark {
			watermark = st.Watermark
		}
	}
	if watermark != 3 {
		t.Errorf("watermark = %d, want the highest seq (3)", watermark)
	}
}

// A total-only ledger keeps its aggregate; a ledger carrying only occupancy still records
// something rather than dropping the row.
func TestFreebuffDesktopTotalOnlyAndOccupancyFallback(t *testing.T) {
	ts := int64(1_790_870_252_587)
	root, _ := freebuffDesktopFixture(t, "p-1", []fbThreadRow{
		{id: "th_1", projectPath: "/p", model: "m", created: ts, updated: ts},
	}, []fbMessageRow{
		{threadID: "th_1", requestID: "req_total", role: "assistant", ts: ts, metricsJSON: `{"usage":{"totalTokens":5000}}`},
		{threadID: "th_1", requestID: "req_ctx", role: "assistant", ts: ts + 1, metricsJSON: `{"context":{"usedTokens":33002}}`},
	})

	events, _, _, _ := scanFixture(t, fbEnv{}.adapter(), root, true, nil)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want both rows", events)
	}
	byKey := map[string]Event{}
	for _, e := range events {
		byKey[e.EventKey] = e
	}
	if got := byKey["freebuff-desktop|th_1|req_total"]; got.Input != 5000 || got.Total != 5000 {
		t.Errorf("total-only event = %+v", got)
	}
	if got := byKey["freebuff-desktop|th_1|req_ctx"]; got.Input != 33002 || got.Total != 33002 {
		t.Errorf("occupancy fallback event = %+v", got)
	}
}

// The rowid watermark makes a repeat pass a no-op and picks up only newly appended rows.
func TestFreebuffDesktopWatermarkResume(t *testing.T) {
	ts := int64(1_790_870_252_587)
	root, dbPath := freebuffDesktopFixture(t, "p-1", []fbThreadRow{
		{id: "th_1", projectPath: "/p", model: "m", created: ts, updated: ts},
	}, []fbMessageRow{
		{threadID: "th_1", requestID: "req_1", role: "assistant", ts: ts, metricsJSON: `{"usage":{"inputTokens":10}}`},
	})

	a := fbEnv{}.adapter()
	events, _, _, states := scanFixture(t, a, root, true, nil)
	if len(events) != 1 {
		t.Fatalf("first pass events = %+v", events)
	}
	events2, _, _, states2 := scanFixture(t, a, root, false, states)
	if len(events2) != 0 {
		t.Fatalf("second pass events = %+v, want none", events2)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO messages (thread_id, request_id, role, metrics_json, ts) VALUES (?,?,?,?,?)`,
		"th_1", "req_2", "assistant", `{"usage":{"inputTokens":20}}`, ts+5000); err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	events3, _, _, _ := scanFixture(t, a, root, false, states2)
	if len(events3) != 1 || events3[0].EventKey != "freebuff-desktop|th_1|req_2" {
		t.Fatalf("third pass events = %+v, want only the new row", events3)
	}
}

func TestFreebuffDesktopRootsAndAvailability(t *testing.T) {
	home := t.TempDir()
	a := newFreebuffDesktopAdapter(home)
	roots := a.Roots(nil, []string{"/extra"})
	if len(roots) != 2 || roots[0] != filepath.Join(home, ".config", "freebuff-desktop", "projects") || roots[1] != "/extra" {
		t.Errorf("roots = %v", roots)
	}
	if a.Available(roots) {
		t.Error("Available = true with no database present")
	}
	if got := a.resolveDBs(roots); len(got) != 0 {
		t.Errorf("resolveDBs = %v, want none", got)
	}
	if a.ID() != "freebuff-desktop" || a.DisplayName() != "Freebuff Desktop" || a.ParserVersion() != freebuffDesktopParserVersion {
		t.Errorf("identity = %q/%q/%d", a.ID(), a.DisplayName(), a.ParserVersion())
	}

	root, dbPath := freebuffDesktopFixture(t, "p-1", nil, nil)
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("fixture db missing: %v", err)
	}
	if got := a.resolveDBs([]string{root}); len(got) != 1 || got[0] != dbPath {
		t.Errorf("resolveDBs = %v, want %q", got, dbPath)
	}
	if !a.Available([]string{root}) {
		t.Error("Available = false for a tree that holds a database")
	}
	// A root pointing straight at a project directory is accepted too.
	if got := a.resolveDBs([]string{filepath.Dir(dbPath)}); len(got) != 1 || got[0] != dbPath {
		t.Errorf("direct root resolveDBs = %v, want %q", got, dbPath)
	}
}
