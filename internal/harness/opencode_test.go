package harness

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// opencodeFixture builds a minimal opencode.db with the two tables the adapter reads.
func opencodeFixture(t *testing.T, sessions []ocSessionRow, messages []ocMessageRow) (env struct {
	home string
	root string
	db   string
}) {
	t.Helper()
	env.home = t.TempDir()
	env.root = filepath.Join(env.home, "share")
	if err := os.MkdirAll(env.root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	env.db = filepath.Join(env.root, "opencode.db")

	db, err := sql.Open("sqlite", "file:"+env.db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE session (
		id TEXT PRIMARY KEY, project_id TEXT NOT NULL DEFAULT '', parent_id TEXT,
		directory TEXT NOT NULL DEFAULT '', agent TEXT, model TEXT,
		time_created INTEGER NOT NULL DEFAULT 0, time_updated INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE message (
		id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
		time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`); err != nil {
		t.Fatalf("create message: %v", err)
	}
	for _, s := range sessions {
		if _, err := db.Exec(
			`INSERT INTO session (id, parent_id, directory, agent, model, time_created, time_updated)
			 VALUES (?,?,?,?,?,?,?)`,
			s.id, s.parentID, s.directory, s.agent, s.model, s.created, s.updated); err != nil {
			t.Fatalf("insert session: %v", err)
		}
	}
	for _, m := range messages {
		if _, err := db.Exec(
			`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`,
			m.id, m.sessionID, m.timeCreated, m.timeUpdated, m.data); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	return env
}

type ocSessionRow struct {
	id, parentID, directory, agent, model string
	created, updated                      int64
}

type ocMessageRow struct {
	id          string
	sessionID   string
	timeCreated int64
	timeUpdated int64
	data        string
}

func ocData(role, modelID, providerID string, in, out, reasoning, cacheRead, cacheWrite int, cost *float64, created, completed int64) string {
	costJSON := "null"
	if cost != nil {
		costJSON = fmt.Sprintf("%v", *cost)
	}
	return fmt.Sprintf(
		`{"role":%q,"modelID":%q,"providerID":%q,"cost":%s,"agent":"build","tokens":{"input":%d,"output":%d,"reasoning":%d,"cache":{"read":%d,"write":%d}},"time":{"created":%d,"completed":%d}}`,
		role, modelID, providerID, costJSON, in, out, reasoning, cacheRead, cacheWrite, created, completed)
}

type ocEnv struct {
	home string
	root string
	db   string
}

func (e ocEnv) adapter() *opencodeAdapter { return &opencodeAdapter{home: e.home} }

func newOCEnv(t *testing.T, sessions []ocSessionRow, messages []ocMessageRow) ocEnv {
	raw := opencodeFixture(t, sessions, messages)
	return ocEnv{home: raw.home, root: raw.root, db: raw.db}
}

// Only assistant rows count; non-assistant rows and empty-usage rows are dropped.
func TestOpenCodeSelectsAssistantRowsOnly(t *testing.T) {
	ts := int64(1_770_384_578_314)
	env := newOCEnv(t,
		[]ocSessionRow{{id: "ses_1", directory: "/proj/off", agent: "build", model: `{"id":"kimi-k2.6","providerID":"kizunax"}`, created: ts, updated: ts + 1000}},
		[]ocMessageRow{
			{id: "msg_keep", sessionID: "ses_1", timeCreated: ts, timeUpdated: ts, data: ocData("assistant", "gpt-5.3-codex", "openai", 1930, 777, 462, 120320, 0, new(0.0), ts, ts+18_596)},
			{id: "msg_user", sessionID: "ses_1", timeCreated: ts, timeUpdated: ts, data: ocData("user", "gpt-5.3-codex", "openai", 500, 0, 0, 0, 0, nil, ts, 0)},
			{id: "msg_zero", sessionID: "ses_1", timeCreated: ts, timeUpdated: ts, data: ocData("assistant", "m", "p", 0, 0, 0, 0, 0, nil, ts, 0)},
		})

	events, sessions, _, _ := scanFixture(t, env.adapter(), env.root, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want only the assistant row with usage", events)
	}
	e := events[0]
	if e.EventKey != "opencode|msg_keep" {
		t.Errorf("key = %q", e.EventKey)
	}
	if e.Input != 1930 || e.Output != 777 || e.CacheRead != 120320 || e.CacheWrite != 0 {
		t.Errorf("buckets = %d/%d/%d/%d", e.Input, e.Output, e.CacheRead, e.CacheWrite)
	}
	if e.Reasoning != 462 {
		t.Errorf("reasoning = %d, want it stored separately", e.Reasoning)
	}
	if e.Total != 1930+777+120320 {
		t.Errorf("total = %d, want reasoning excluded", e.Total)
	}
	if e.Model != "gpt-5.3-codex" || e.Provider != "openai" {
		t.Errorf("model/provider = %q/%q", e.Model, e.Provider)
	}
	if e.Project != "/proj/off" {
		t.Errorf("project = %q, want the session directory", e.Project)
	}
	if e.CostUSD == nil || *e.CostUSD != 0 {
		t.Errorf("cost = %v, want a reported zero preserved", e.CostUSD)
	}
	if e.LatencyMs == nil || *e.LatencyMs != 18_596 {
		t.Errorf("latency = %v, want created->completed", e.LatencyMs)
	}
	if e.TTFTMs != nil {
		t.Errorf("ttft = %v, want none (OpenCode records no first-token time)", e.TTFTMs)
	}
	if len(sessions) != 1 || sessions[0].Model != "kimi-k2.6" {
		t.Errorf("sessions = %+v, want the model id unwrapped from JSON", sessions)
	}
}

// The rowid watermark must make a repeat pass a no-op, and pick up only new rows after.
func TestOpenCodeWatermarkResume(t *testing.T) {
	ts := int64(1_770_384_578_314)
	env := newOCEnv(t, []ocSessionRow{{id: "ses_1", directory: "/p", created: ts, updated: ts}},
		[]ocMessageRow{{id: "msg_1", sessionID: "ses_1", timeCreated: ts, timeUpdated: ts, data: ocData("assistant", "m", "p", 10, 1, 0, 0, 0, nil, ts, 0)}})

	a := env.adapter()
	events, _, _, states := scanFixture(t, a, env.root, true, nil)
	if len(events) != 1 {
		t.Fatalf("first pass events = %+v", events)
	}
	var key string
	for k, st := range states {
		if st.Watermark > 0 {
			key = k
		}
	}
	if key == "" {
		t.Fatalf("no watermark stored: %+v", states)
	}

	events2, _, _, states2 := scanFixture(t, a, env.root, false, states)
	if len(events2) != 0 {
		t.Fatalf("second pass events = %+v, want none", events2)
	}

	db, err := sql.Open("sqlite", "file:"+env.db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`,
		"msg_2", "ses_1", ts+5000, ts+5000, ocData("assistant", "m", "p", 20, 2, 0, 0, 0, nil, ts+5000, ts+6000)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	events3, _, _, _ := scanFixture(t, a, env.root, false, states2)
	if len(events3) != 1 || events3[0].EventKey != "opencode|msg_2" {
		t.Fatalf("third pass events = %+v, want only the new row", events3)
	}
	if events3[0].LatencyMs == nil || *events3[0].LatencyMs != 1000 {
		t.Errorf("latency = %v, want 1000", events3[0].LatencyMs)
	}
}

// A session with a parent is a subagent session.
func TestOpenCodeSubagentSessionsLabelled(t *testing.T) {
	ts := int64(1_770_384_578_314)
	env := newOCEnv(t, []ocSessionRow{
		{id: "ses_main", directory: "/p", created: ts, updated: ts},
		{id: "ses_sub", parentID: "ses_main", directory: "/p", agent: "librarian", created: ts, updated: ts},
	}, []ocMessageRow{
		{id: "msg_main", sessionID: "ses_main", timeCreated: ts, timeUpdated: ts, data: ocData("assistant", "m", "p", 10, 1, 0, 0, 0, nil, ts, 0)},
		{id: "msg_sub", sessionID: "ses_sub", timeCreated: ts, timeUpdated: ts, data: ocData("assistant", "m", "p", 20, 2, 0, 0, 0, nil, ts, 0)},
	})

	events, _, _, _ := scanFixture(t, env.adapter(), env.root, true, nil)
	types := map[string]string{}
	for _, e := range events {
		types[e.EventKey] = e.AgentType + "/" + e.AgentName
	}
	// A main session records its agent name too ("build" here); only the type differs.
	if types["opencode|msg_main"] != "main/build" {
		t.Errorf("main session = %q", types["opencode|msg_main"])
	}
	if types["opencode|msg_sub"] != "subagent/librarian" {
		t.Errorf("subagent session = %q", types["opencode|msg_sub"])
	}
}

// Without a database the legacy JSON tree is read instead.
func TestOpenCodeLegacyStorageFallback(t *testing.T) {
	ts := int64(1_770_384_578_314)
	home := t.TempDir()
	root := filepath.Join(home, "share")
	msgDir := filepath.Join(root, "storage", "message", "ses_legacy")
	if err := os.MkdirAll(msgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := fmt.Sprintf(`{"id":"msg_l","sessionID":"ses_legacy",%s`, ocData("assistant", "gpt-5.3-codex", "openai", 100, 10, 5, 7, 0, nil, ts, 0)[1:])
	if err := os.WriteFile(filepath.Join(msgDir, "msg_l.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := &opencodeAdapter{home: home}
	events, _, _, states := scanFixture(t, a, root, true, nil)
	if len(events) != 1 {
		t.Fatalf("legacy events = %+v", events)
	}
	if events[0].EventKey != "opencode|msg_l" || events[0].SessionID != "ses_legacy" {
		t.Errorf("legacy event = %+v", events[0])
	}
	if events[0].Total != 117 {
		t.Errorf("total = %d, want 100+10+7", events[0].Total)
	}
	if len(states) != 1 {
		t.Errorf("states = %+v, want one file cursor", states)
	}
}

func TestOpenCodeResolveDBAndRoots(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "share")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := &opencodeAdapter{home: home}
	if got := a.resolveDB([]string{root}); got != "" {
		t.Errorf("resolveDB = %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(root, "opencode-beta.db"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := a.resolveDB([]string{root}); got != filepath.Join(root, "opencode-beta.db") {
		t.Errorf("resolveDB = %q, want the channel database", got)
	}
	if err := os.WriteFile(filepath.Join(root, "opencode.db"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := a.resolveDB([]string{root}); got != filepath.Join(root, "opencode.db") {
		t.Errorf("resolveDB = %q, want opencode.db to win", got)
	}

	t.Setenv("OPENCODE_DATA_DIR", "/tmp/oc-a, /tmp/oc-b")
	roots := a.Roots(context.Background(), []string{"/extra"})
	if len(roots) != 3 || roots[0] != "/tmp/oc-a" || roots[1] != "/tmp/oc-b" || roots[2] != "/extra" {
		t.Errorf("roots = %v", roots)
	}
	if a.ID() != "opencode" || a.DisplayName() != "OpenCode" || a.ParserVersion() != opencodeParserVersion {
		t.Errorf("identity = %q/%q/%d", a.ID(), a.DisplayName(), a.ParserVersion())
	}
}
