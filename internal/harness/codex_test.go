package harness

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// codexEnv is a fixture ~/.codex tree: a sessions directory plus an optional thread
// index, with an adapter already pointed at it.
type codexEnv struct {
	home    string
	session string // the sessions root to scan
}

func newCodexEnv(t *testing.T, files map[string]string, threads []threadRow) codexEnv {
	t.Helper()
	home := t.TempDir()
	sessions := filepath.Join(home, ".codex", "sessions", "2026", "07", "29")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(sessions, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if threads != nil {
		writeThreadsDB(t, filepath.Join(home, ".codex", "state_5.sqlite"), sessions, threads)
	}
	return codexEnv{home: home, session: sessions}
}

type threadRow struct {
	id           string
	file         string
	cwd          string
	model        string
	threadSource string
}

// writeThreadsDB creates the subset of the real Codex schema the adapter reads.
func writeThreadsDB(t *testing.T, path, sessionsDir string, rows []threadRow) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open threads db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, cwd TEXT NOT NULL,
		model TEXT, thread_source TEXT)`); err != nil {
		t.Fatalf("create threads: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO threads (id, rollout_path, cwd, model, thread_source) VALUES (?,?,?,?,?)`,
			r.id, filepath.Join(sessionsDir, r.file), r.cwd, r.model, r.threadSource); err != nil {
			t.Fatalf("insert thread: %v", err)
		}
	}
}

func usageJSON(in, cached, cw, out, reasoning, total int) string {
	return fmt.Sprintf(
		`{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}`,
		in, cached, cw, out, reasoning, total)
}

func tokenCountLine(ordinal int, ts, last, cum string) string {
	return fmt.Sprintf(
		`{"timestamp":%q,"ordinal":%d,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s,"last_token_usage":%s}}}`,
		ts, ordinal, cum, last)
}

func (e codexEnv) adapter() *codexAdapter { return &codexAdapter{home: e.home} }

// The key is a content fingerprint: it must collapse a verbatim replay (a fork copies
// its parent's records) while keeping two genuinely distinct turns that share an ordinal.
func TestCodexDistinctTurnsWithSameOrdinalStaySeparate(t *testing.T) {
	last := usageJSON(1000, 100, 0, 50, 20, 1050)
	cum := usageJSON(1000, 100, 0, 50, 20, 1050)
	body := tokenCountLine(3, "2026-07-28T23:29:41.861Z", last, cum) + "\n" +
		tokenCountLine(4, "2026-07-28T23:30:00.000Z", usageJSON(0, 0, 0, 0, 0, 0), cum) + "\n" +
		tokenCountLine(3, "2026-07-28T23:31:00.000Z", usageJSON(2000, 200, 0, 60, 30, 2060), usageJSON(3000, 300, 0, 110, 50, 3110)) + "\n"

	const file = "rollout-2026-07-28T23-29-04-019fab0f-752f-7c03-a19c-97a8ccd03bb5.jsonl"
	env := newCodexEnv(t, map[string]string{file: body}, []threadRow{{
		id: "019fab0f-752f-7c03-a19c-97a8ccd03bb5", file: file,
		cwd: "/proj/codex", model: "gpt-5.6-sol", threadSource: "user",
	}})

	events, sessions, _, _ := scanFixture(t, env.adapter(), env.session, true, nil)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (the zero-usage record is dropped): %+v", len(events), events)
	}

	var first, second Event
	for _, e := range events {
		if e.Input == 900 {
			first = e
		} else {
			second = e
		}
	}
	if first.EventKey == "" || second.EventKey == "" {
		t.Fatalf("expected one 900-input and one 1800-input turn: %+v", events)
	}
	// input = input_tokens - cached_input_tokens; cache_read = cached.
	if first.Input != 900 || first.Output != 50 || first.CacheRead != 100 || first.Total != 1050 {
		t.Errorf("first turn = in %d out %d cr %d total %d", first.Input, first.Output, first.CacheRead, first.Total)
	}
	if first.Reasoning != 20 {
		t.Errorf("reasoning = %d, want it stored", first.Reasoning)
	}
	if second.Input != 1800 || second.Output != 60 || second.CacheRead != 200 {
		t.Errorf("second turn = in %d out %d cr %d", second.Input, second.Output, second.CacheRead)
	}
	if first.Project != "/proj/codex" || first.Model != "gpt-5.6-sol" {
		t.Errorf("metadata = %q/%q, want the thread index", first.Project, first.Model)
	}
	if first.SessionID != "019fab0f-752f-7c03-a19c-97a8ccd03bb5" || first.AgentType != "main" {
		t.Errorf("identity = %q/%q", first.SessionID, first.AgentType)
	}
	if first.Outcome != "unknown" {
		t.Errorf("outcome = %q, want unknown (token_count carries none)", first.Outcome)
	}
	if len(sessions) != 1 || sessions[0].StartedAt == 0 || sessions[0].UpdatedAt < sessions[0].StartedAt {
		t.Errorf("sessions = %+v", sessions)
	}
}

// The same record copied into a second file (a fork) must produce the same key, so the
// store's upsert counts it once.
func TestCodexForkReplayProducesIdenticalKey(t *testing.T) {
	shared := tokenCountLine(7, "2026-07-18T22:19:20.000Z", usageJSON(500, 0, 0, 25, 0, 525), usageJSON(500, 0, 0, 25, 0, 525)) + "\n"
	parent := shared + tokenCountLine(8, "2026-07-18T22:20:00.000Z", usageJSON(700, 0, 0, 30, 0, 730), usageJSON(1200, 0, 0, 55, 0, 1255)) + "\n"
	child := shared + tokenCountLine(9, "2026-07-18T22:25:00.000Z", usageJSON(100, 0, 0, 10, 0, 110), usageJSON(100, 0, 0, 10, 0, 110)) + "\n"

	env := newCodexEnv(t, map[string]string{
		"rollout-2026-07-18T22-19-14-019f75cf-6599-7962-95e5-6ba46c7b818e.jsonl": parent,
		"rollout-2026-07-18T22-19-24-019f75cf-8d91-7b02-89a6-9edce2bcacea.jsonl": child,
	}, nil)

	events, _, _, _ := scanFixture(t, env.adapter(), env.session, true, nil)
	// 3 distinct records: the replayed one appears once because the key matches.
	seen := map[string]int{}
	for _, e := range events {
		seen[e.EventKey]++
	}
	if len(seen) != 3 {
		t.Fatalf("distinct keys = %d, want 3: %+v", len(seen), events)
	}
	// Both files contribute the shared record; the store's upsert is what collapses them,
	// so the adapter must emit the identical key from both.
	counts := 0
	for _, n := range seen {
		if n == 2 {
			counts++
		}
	}
	if counts != 1 {
		t.Errorf("expected exactly one key emitted from both files, got %d", counts)
	}
}

// A zero-component record whose total equals the cumulative total accounts for the whole
// session and must be counted; the fork-shaped variant must not be.
func TestCodexTotalOnlyTrustRules(t *testing.T) {
	trusted := tokenCountLine(1, "2026-07-16T06:51:44.330Z",
		usageJSON(0, 0, 0, 0, 0, 4221), usageJSON(0, 0, 0, 0, 0, 4221))
	untrusted := tokenCountLine(2, "2026-07-16T06:52:44.330Z",
		usageJSON(0, 0, 0, 0, 0, 500), usageJSON(9000, 0, 0, 900, 0, 9900))

	env := newCodexEnv(t, map[string]string{
		"rollout-2026-07-16T06-51-44-019f69b0-952c-7f71-b566-fc511d5f2fcc.jsonl": trusted + "\n" + untrusted + "\n",
	}, nil)

	events, _, _, _ := scanFixture(t, env.adapter(), env.session, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want only the trusted total-only record", events)
	}
	if events[0].Input != 4221 || events[0].Total != 4221 || events[0].Output != 0 {
		t.Errorf("total-only mapping = in %d out %d total %d", events[0].Input, events[0].Output, events[0].Total)
	}
}

// Without the thread index the scan still works: session id from the filename, and an
// empty project rather than an error.
func TestCodexFallsBackWithoutStateDB(t *testing.T) {
	body := tokenCountLine(1, "2026-07-16T06:51:44.330Z", usageJSON(10, 0, 0, 5, 0, 15), usageJSON(10, 0, 0, 5, 0, 15)) + "\n"
	env := newCodexEnv(t, map[string]string{
		"rollout-2026-07-16T06-51-44-019f69b0-952c-7f71-b566-fc511d5f2fcc.jsonl": body,
	}, nil)

	events, sessions, _, _ := scanFixture(t, env.adapter(), env.session, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].SessionID != "019f69b0-952c-7f71-b566-fc511d5f2fcc" {
		t.Errorf("session = %q, want the filename uuid", events[0].SessionID)
	}
	if events[0].Model != "codex" || events[0].Project != "" {
		t.Errorf("fallback metadata = %q/%q", events[0].Model, events[0].Project)
	}
	if len(sessions) != 1 || sessions[0].AgentType != "main" {
		t.Errorf("sessions = %+v", sessions)
	}
}

func TestCodexSubagentThreadAndIncrementalResume(t *testing.T) {
	const file = "rollout-2026-07-18T22-19-14-019f75cf-6599-7962-95e5-6ba46c7b818e.jsonl"
	first := tokenCountLine(1, "2026-07-18T22:19:20.000Z", usageJSON(10, 0, 0, 1, 0, 11), usageJSON(10, 0, 0, 1, 0, 11)) + "\n"
	env := newCodexEnv(t, map[string]string{file: first}, []threadRow{{
		id: "019f75cf-6599-7962-95e5-6ba46c7b818e", file: file,
		cwd: "/p", model: "gpt-5.5", threadSource: "subagent",
	}})

	a := env.adapter()
	events, _, _, states := scanFixture(t, a, env.session, true, nil)
	if len(events) != 1 || events[0].AgentType != "subagent" {
		t.Fatalf("events = %+v, want one subagent event", events)
	}

	// Unchanged file: skipped.
	events2, _, _, states2 := scanFixture(t, a, env.session, false, states)
	if len(events2) != 0 {
		t.Fatalf("second scan events = %d, want 0", len(events2))
	}

	// Appended record: only the new one.
	second := tokenCountLine(2, "2026-07-18T22:20:00.000Z", usageJSON(20, 0, 0, 2, 0, 22), usageJSON(30, 0, 0, 3, 0, 33)) + "\n"
	f, err := os.OpenFile(filepath.Join(env.session, file), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(second); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	events3, _, _, _ := scanFixture(t, a, env.session, false, states2)
	if len(events3) != 1 || events3[0].Input != 20 {
		t.Fatalf("third scan events = %+v, want only the appended record", events3)
	}
}

// ~/.codex must never be a scan root: it holds a .tmp directory with tens of thousands
// of entries and multi-hundred-megabyte log databases.
func TestCodexRootsExcludeCodexHomeSurface(t *testing.T) {
	a := &codexAdapter{home: "/Users/example"}
	roots := a.Roots(context.Background(), []string{"/extra"})
	if roots[0] != "/Users/example/.codex/sessions" || roots[1] != "/Users/example/.codex/archived_sessions" {
		t.Errorf("roots = %v", roots)
	}
	for _, r := range roots {
		if r == "/Users/example/.codex" {
			t.Fatalf("~/.codex itself must not be walked: %v", roots)
		}
	}
	if a.Available([]string{"/definitely/not/here"}) {
		t.Error("Available = true for a missing root")
	}
	if a.ParserVersion() != codexParserVersion || a.ID() != "codex" || a.DisplayName() != "Codex" {
		t.Errorf("identity = %q/%q/%d", a.ID(), a.DisplayName(), a.ParserVersion())
	}
}
