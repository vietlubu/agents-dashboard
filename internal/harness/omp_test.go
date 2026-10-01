package harness

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type ompEnv struct {
	home string
	root string
}

func (e ompEnv) adapter() *ompAdapter { return &ompAdapter{home: e.home} }

// newOmpHome creates an empty ~/.omp and returns the home and root, so tests can build
// session paths that match the files they write.
func newOmpHome(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	root = filepath.Join(home, ".omp")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return home, root
}

// writeOmpSession writes a session file under the home and returns its absolute path.
func writeOmpSession(t *testing.T, home, rel, cwd string) string {
	t.Helper()
	full := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := os.WriteFile(full, []byte(sessionHeader(cwd)+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return full
}

// ompStatsFixture builds ~/.omp/stats.db with the subset of the real schema the adapter
// reads.
func ompStatsFixture(t *testing.T, home, root string, rows []ompRow) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "stats.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT, session_file TEXT NOT NULL, entry_id TEXT NOT NULL,
		folder TEXT NOT NULL, model TEXT NOT NULL, provider TEXT NOT NULL, api TEXT NOT NULL,
		timestamp INTEGER NOT NULL, duration INTEGER, ttft INTEGER, stop_reason TEXT NOT NULL,
		error_message TEXT, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
		cache_read_tokens INTEGER NOT NULL, cache_write_tokens INTEGER NOT NULL,
		total_tokens INTEGER NOT NULL, cost_total REAL NOT NULL DEFAULT 0,
		agent_type TEXT NOT NULL DEFAULT 'main', UNIQUE(session_file, entry_id))`); err != nil {
		t.Fatalf("create messages: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO messages
			(session_file, entry_id, folder, model, provider, api, timestamp, duration, ttft,
			 stop_reason, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			 total_tokens, cost_total, agent_type)
			VALUES (?,?,?,?,?,'openai-responses',?,?,?,?,?,?,?,?,?,?,?)`,
			r.sessionFile, r.entryID, r.folder, r.model, r.provider, r.timestamp,
			r.duration, r.ttft, r.stopReason, r.input, r.output, r.cacheRead, r.cacheWrite,
			r.total, r.cost, r.agentType); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
}

type ompRow struct {
	sessionFile, entryID, folder, model, provider string
	timestamp                                     int64
	duration, ttft                                float64
	stopReason                                    string
	input, output, cacheRead, cacheWrite, total   int64
	cost                                          float64
	agentType                                     string
}

func sessionHeader(cwd string) string {
	return fmt.Sprintf(`{"type":"session","version":3,"id":"019fae53-e19c-72dd-b081-7e9c8fc26ce1","timestamp":"2026-07-29T14:42:40.668Z","cwd":%q}`, cwd)
}

// stats.db rows are the primary source: timings map to latency and TTFT, cost is
// reported, zero-usage rows are skipped, and the project comes from the session header
// rather than omp's ambiguous folder slug.
func TestOmpStatsDBMapsTimingCostAndProject(t *testing.T) {
	home, root := newOmpHome(t)
	rel := filepath.Join(".omp", "agent", "sessions", "-Projects-x", "2026-07-29T14-42-40-668Z_019fae53-e19c-72dd-b081-7e9c8fc26ce1.jsonl")
	session := writeOmpSession(t, home, rel, "/Users/example/Projects/x")
	subagent := filepath.Join(filepath.Dir(session), "2026-07-29T14-42-40-668Z_019fae53-e19c-72dd-b081-7e9c8fc26ce1", "librarian.jsonl")
	if err := os.MkdirAll(filepath.Dir(subagent), 0o755); err != nil {
		t.Fatalf("mkdir subagent: %v", err)
	}
	if err := os.WriteFile(subagent, []byte(sessionHeader("")+"\n"), 0o644); err != nil {
		t.Fatalf("write subagent: %v", err)
	}

	ompStatsFixture(t, home, root, []ompRow{
		{
			sessionFile: session, entryID: "0db9fc63", folder: "-Projects-x", model: "gpt-5.6-sol",
			provider: "cpa", timestamp: 1788750929859, duration: 25217.77, ttft: 11963.74,
			stopReason: "toolUse", input: 10586, output: 430, cacheRead: 37248, cacheWrite: 0,
			total: 48264, cost: 0.0658432, agentType: "main",
		},
		{
			// Zero-usage interrupted row: skipped.
			sessionFile: session, entryID: "aaaa0001", folder: "-Projects-x", model: "gpt-5.6-sol",
			provider: "cpa", timestamp: 1788750930000, stopReason: "aborted", total: 0, agentType: "main",
		},
		{
			sessionFile: subagent, entryID: "sub00001", folder: "-Projects-x", model: "gpt-5.6-sol",
			provider: "cpa", timestamp: 1788750940000, duration: 1000, ttft: 500,
			stopReason: "stop", input: 10, output: 2, total: 12, cost: 0.001, agentType: "subagent",
		},
	})

	env := ompEnv{home: home, root: root}
	events, sessions, _, states := scanFixture(t, env.adapter(), env.root, true, nil)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want 2 (zero-usage row dropped)", events)
	}
	main := events[0]
	if main.EventKey != "omp|"+session+"|0db9fc63" {
		t.Errorf("key = %q", main.EventKey)
	}
	if main.Project != "/Users/example/Projects/x" {
		t.Errorf("project = %q, want the session header cwd", main.Project)
	}
	if main.LatencyMs == nil || *main.LatencyMs != 25217 {
		t.Errorf("latency = %v, want the rounded duration", main.LatencyMs)
	}
	if main.TTFTMs == nil || *main.TTFTMs != 11963 {
		t.Errorf("ttft = %v", main.TTFTMs)
	}
	if main.CostUSD == nil || *main.CostUSD != 0.0658432 {
		t.Errorf("cost = %v, want the reported amount", main.CostUSD)
	}
	if main.Outcome != "tool_use" {
		t.Errorf("outcome = %q, want tool_use", main.Outcome)
	}
	if main.Total != 48264 || main.Reasoning != 0 {
		t.Errorf("total = %d reasoning = %d", main.Total, main.Reasoning)
	}
	if main.SessionID != "019fae53-e19c-72dd-b081-7e9c8fc26ce1" {
		t.Errorf("session = %q, want the session file uuid", main.SessionID)
	}

	sub := events[1]
	if sub.AgentType != "subagent" || sub.AgentName != "librarian" {
		t.Errorf("subagent = %q/%q", sub.AgentType, sub.AgentName)
	}
	if sub.SessionID != "librarian" {
		t.Errorf("subagent session = %q, want the transcript name", sub.SessionID)
	}

	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want the session and its subagent", sessions)
	}
	for _, si := range sessions {
		if si.StartedAt == 0 || si.UpdatedAt == 0 {
			t.Errorf("session %q carries no timestamps: %+v", si.SessionID, si)
		}
	}
	var watermark int64
	for _, st := range states {
		if st.Watermark > 0 {
			watermark = st.Watermark
		}
	}
	if watermark != 3 {
		t.Errorf("watermark = %d, want the highest rowid (3)", watermark)
	}
}

// The rowid watermark must survive a pass that finds nothing, and then pick up only new
// rows.
func TestOmpStatsDBWatermarkResume(t *testing.T) {
	home, root := newOmpHome(t)
	session := "/tmp/sess.jsonl"
	ompStatsFixture(t, home, root, []ompRow{{
		sessionFile: session, entryID: "e1", folder: "-f", model: "m", provider: "p",
		timestamp: 1788750929859, stopReason: "stop", input: 10, total: 10, agentType: "main",
	}})
	env := ompEnv{home: home, root: root}

	a := env.adapter()
	events, _, _, states := scanFixture(t, a, env.root, true, nil)
	if len(events) != 1 {
		t.Fatalf("first pass = %+v", events)
	}
	events2, _, _, states2 := scanFixture(t, a, env.root, false, states)
	if len(events2) != 0 {
		t.Fatalf("second pass = %+v, want none", events2)
	}
	var wm int64
	for _, st := range states2 {
		if st.Watermark > wm {
			wm = st.Watermark
		}
	}
	if wm != 1 {
		t.Fatalf("watermark regressed to %d, want 1", wm)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(env.root, "stats.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO messages
		(session_file, entry_id, folder, model, provider, api, timestamp, stop_reason,
		 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens,
		 cost_total, agent_type) VALUES (?,'e2','-f','m','p','a',?,?,20,2,0,0,22,0,'main')`,
		session, 1788751000000, "stop"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	events3, _, _, _ := scanFixture(t, a, env.root, false, states2)
	if len(events3) != 1 || events3[0].EventKey != "omp|"+session+"|e2" {
		t.Fatalf("third pass = %+v, want only the new row", events3)
	}
}

// Without stats.db the JSONL is read, and bridge copies are excluded.
func TestOmpSessionsFallbackAndBridgeExclusion(t *testing.T) {
	home := t.TempDir()
	sessionDir := filepath.Join(home, ".omp", "agent", "sessions", "-Projects-x")
	bridgeDir := filepath.Join(home, ".omp", "agent", "sessions", "bridge")
	for _, dir := range []string{sessionDir, bridgeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	record := `{"type":"message","id":"0e20d97e","timestamp":"2026-07-29T14:42:55.799Z","message":{"role":"assistant","model":"gpt-5.6-sol","stopReason":"stop","usage":{"input":27248,"output":5,"cacheRead":0,"cacheWrite":0,"totalTokens":27253,"cost":{"total":0.5}},"timestamp":1785336172462}}`
	if err := os.WriteFile(filepath.Join(sessionDir, "2026-07-29T14-42-40-668Z_019fae53-e19c-72dd-b081-7e9c8fc26ce1.jsonl"),
		[]byte(sessionHeader("/Users/example/x")+"\n"+record+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bridgeDir, "2026-08-19T03-19-25.604_00000000.jsonl"),
		[]byte(sessionHeader("/other")+"\n"+record+"\n"), 0o644); err != nil {
		t.Fatalf("write bridge: %v", err)
	}

	root := filepath.Join(home, ".omp")
	a := &ompAdapter{home: home}
	events, _, _, _ := scanFixture(t, a, root, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want only the non-bridge session", events)
	}
	if events[0].Project != "/Users/example/x" {
		t.Errorf("project = %q", events[0].Project)
	}
	if events[0].Total != 27253 || events[0].Input != 27248 {
		t.Errorf("buckets = in %d total %d", events[0].Input, events[0].Total)
	}
	if events[0].CostUSD == nil || *events[0].CostUSD != 0.5 {
		t.Errorf("cost = %v", events[0].CostUSD)
	}
	if events[0].Reasoning != 0 {
		t.Errorf("reasoning = %d, want 0 (omp records none)", events[0].Reasoning)
	}
}

func TestOmpRootsAndAvailability(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".omp", "agent", "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := &ompAdapter{home: home}
	if !a.Available(a.Roots(context.Background(), nil)) {
		t.Error("Available = false even though the sessions directory exists")
	}
	if a.resolveStatsDB([]string{filepath.Join(home, ".omp")}) != "" {
		t.Error("resolveStatsDB found a database that does not exist")
	}
	if _, err := os.Create(filepath.Join(home, ".omp", "stats.db")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := a.resolveStatsDB([]string{filepath.Join(home, ".omp")}); got != filepath.Join(home, ".omp", "stats.db") {
		t.Errorf("resolveStatsDB = %q", got)
	}
	// An OMP_CODING_AGENT_DIR pointing at the agent home must still find the sibling db.
	if got := a.resolveStatsDB([]string{filepath.Join(home, ".omp", "agent")}); got != filepath.Join(home, ".omp", "stats.db") {
		t.Errorf("sibling resolution = %q", got)
	}
	if a.ID() != "omp" || a.DisplayName() != "omp" || a.ParserVersion() != ompParserVersion {
		t.Errorf("identity = %q/%q/%d", a.ID(), a.DisplayName(), a.ParserVersion())
	}
}

func TestOmpPiSessionFieldsSkipAborted(t *testing.T) {
	l := &piLine{Type: "message", ID: "x", Message: &struct {
		Role       string   `json:"role"`
		Model      string   `json:"model"`
		StopReason string   `json:"stopReason"`
		Timestamp  int64    `json:"timestamp"`
		Usage      *piUsage `json:"usage"`
	}{Role: "assistant", StopReason: "aborted", Usage: &piUsage{Input: 5}}}
	if u, _, _, _ := piRecordFields(l); u != nil {
		t.Error("an aborted Pi message must be skipped")
	}
	if u, _, _, _ := ompRecordFields(l); u != nil {
		t.Error("an aborted omp message must be skipped")
	}

	compaction := &piLine{Type: "compaction", Timestamp: "2026-07-29T14:42:40.668Z", Usage: &piUsage{Input: 100}}
	u, _, _, ts := piRecordFields(compaction)
	if u == nil || ts == 0 {
		t.Errorf("compaction usage must count: usage=%v ts=%d", u, ts)
	}
}

// stats.db and the session JSONL describe the same API calls, so reading both must
// produce one event per call: the JSONL adds the calls the stale database is missing and
// the database keeps the latencies the JSONL does not record.
func TestOmpStatsDBAndSessionsMergeWithoutDoubleCounting(t *testing.T) {
	home, root := newOmpHome(t)
	rel := filepath.Join(".omp", "agent", "sessions", "-Projects-x", "2026-07-29T14-42-40-668Z_019fae53-e19c-72dd-b081-7e9c8fc26ce1.jsonl")
	session := writeOmpSession(t, home, rel, "")

	// The record stats.db has, and one it never saw: the database stopped being written
	// before the second call happened.
	shared := `{"type":"message","id":"0db9fc63","timestamp":"2026-07-29T14:42:55.799Z","message":{"role":"assistant","model":"gpt-5.6-sol","stopReason":"toolUse","usage":{"input":10586,"output":430,"cacheRead":37248,"cacheWrite":0,"totalTokens":48264,"cost":{"total":0.0658432}},"timestamp":1788750929859}}`
	newer := `{"type":"message","id":"7f000001","timestamp":"2026-07-30T09:00:00.000Z","message":{"role":"assistant","model":"gpt-5.6-sol","stopReason":"stop","usage":{"input":100,"output":10,"cacheRead":0,"cacheWrite":0,"totalTokens":110,"cost":{"total":0.002}},"timestamp":1788792330000}}`
	file, err := os.OpenFile(session, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := file.WriteString(shared + "\n" + newer + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	file.Close()

	ompStatsFixture(t, home, root, []ompRow{{
		sessionFile: session, entryID: "0db9fc63", folder: "-Projects-x", model: "gpt-5.6-sol",
		provider: "cpa", timestamp: 1788750929859, duration: 25217.77, ttft: 11963.74,
		stopReason: "toolUse", input: 10586, output: 430, cacheRead: 37248, cacheWrite: 0,
		total: 48264, cost: 0.0658432, agentType: "main",
	}})

	env := ompEnv{home: home, root: root}
	events, _, _, _ := scanFixture(t, env.adapter(), env.root, true, nil)

	// The two sources may both describe a call: that is safe only while they agree on the
	// key, which is what makes the store collapse them into one row.
	byKey := map[string][]Event{}
	for _, e := range events {
		byKey[e.EventKey] = append(byKey[e.EventKey], e)
	}
	if len(byKey) != 2 {
		t.Fatalf("keys = %v, want one per API call", byKey)
	}
	sharedEvents := byKey["omp|"+session+"|0db9fc63"]
	if len(sharedEvents) != 2 {
		t.Fatalf("shared call seen %d times, want once from each source", len(sharedEvents))
	}
	if sharedEvents[0].Total != sharedEvents[1].Total {
		t.Errorf("the two sources disagree about the shared call: %d vs %d",
			sharedEvents[0].Total, sharedEvents[1].Total)
	}
	live, ok := byKey["omp|"+session+"|7f000001"]
	if !ok {
		t.Fatalf("the call the database never saw is missing: %v", byKey)
	}
	if live[0].Total != 110 {
		t.Errorf("live call total = %d, want 110", live[0].Total)
	}
}
