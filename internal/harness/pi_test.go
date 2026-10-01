package harness

import (
	"os"
	"path/filepath"
	"testing"
)

// piEnv mirrors the omp fixture: a fake home holding the sessions tree the adapter walks.
type piEnv struct {
	home string
	root string
}

func (e piEnv) adapter() *piAdapter { return &piAdapter{home: e.home} }

func newPiEnv(t *testing.T) piEnv {
	t.Helper()
	home := t.TempDir()
	return piEnv{home: home, root: filepath.Join(home, ".pi", "agent", "sessions")}
}

// writePiSession writes a transcript under the sessions root and returns its path.
func writePiSession(t *testing.T, env piEnv, rel string, lines ...string) string {
	t.Helper()
	full := filepath.Join(env.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return full
}

// A transcript that records no countable usage — every turn aborted, or none taken —
// must not leave a session row behind: its timestamps would both be zero, so no date
// range could ever reach it.
func TestPiSessionWithoutUsageIsNotRecorded(t *testing.T) {
	env := newPiEnv(t)
	header := `{"type":"session","id":"01a04681-6a4c-739a-9a9c-4b6c9f20e45c","timestamp":"2026-08-28T03:54:41.612Z","cwd":"/Users/example/vietmemory"}`
	// An assistant turn whose usage block is all zero, which Pi writes for a turn that
	// produced nothing, and which the adapter deliberately skips.
	empty := `{"type":"message","id":"aaaaaaaa","timestamp":"2026-08-28T03:54:50.000Z","message":{"role":"assistant","model":"gpt-5.6-sol","stopReason":"aborted","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0},"timestamp":1787889290000}}`
	writePiSession(t, env, "--Documents-vietmemory--/2026-08-28T03-54-41-612Z_01a04681-6a4c-739a-9a9c-4b6c9f20e45c.jsonl", header, empty)

	events, sessions, _, _ := scanFixture(t, env.adapter(), env.root, true, nil)
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %+v, want none (a session exists only once it has usage)", sessions)
	}
}

// A transcript with usage is attributed to its session, and its record ids — eight hex
// digits, unique only within a session — must not collide across two sessions that
// happen to reuse one.
func TestPiSessionAttributionAndScopedKeys(t *testing.T) {
	env := newPiEnv(t)
	turn := `{"type":"message","id":"bbbbbbbb","timestamp":"2026-08-28T03:55:00.000Z","message":{"role":"assistant","model":"gpt-5.6-sol","stopReason":"stop","usage":{"input":100,"output":20,"cacheRead":5,"cacheWrite":1,"reasoning":7,"totalTokens":133},"timestamp":1787889300000}}`
	first := newPiSessionHeader(t, "01a04681-6a4c-739a-9a9c-4b6c9f20e45c", "/Users/example/vietmemory")
	firstPath := writePiSession(t, env, "--Documents-vietmemory--/2026-08-28T03-54-41-612Z_01a04681-6a4c-739a-9a9c-4b6c9f20e45c.jsonl", first, turn)

	second := newPiSessionHeader(t, "01a04682-0000-0000-0000-000000000000", "/Users/example/vietmemory")
	secondPath := writePiSession(t, env, "--Documents-vietmemory--/2026-08-29T03-54-41-612Z_01a04682-0000-0000-0000-000000000000.jsonl", second, turn)

	events, sessions, _, _ := scanFixture(t, env.adapter(), env.root, true, nil)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want one per session", events)
	}
	bySession := map[string]Event{}
	for _, e := range events {
		bySession[e.SessionID] = e
	}
	got, ok := bySession["01a04681-6a4c-739a-9a9c-4b6c9f20e45c"]
	if !ok {
		t.Fatalf("session not attributed: %+v", bySession)
	}
	if got.EventKey != "pi|01a04681-6a4c-739a-9a9c-4b6c9f20e45c|bbbbbbbb" {
		t.Errorf("key = %q, want the session-scoped key", got.EventKey)
	}
	if got.Project != "/Users/example/vietmemory" {
		t.Errorf("project = %q, want the header cwd", got.Project)
	}
	if got.Total != 126 || got.Reasoning != 7 {
		t.Errorf("buckets = total %d reasoning %d (reasoning must stay out of the total)", got.Total, got.Reasoning)
	}
	if got.Outcome != "ok" {
		t.Errorf("outcome = %q", got.Outcome)
	}
	if got.CostUSD != nil {
		t.Errorf("cost = %v, want none when the record has no cost block", got.CostUSD)
	}
	if _, ok := bySession["01a04682-0000-0000-0000-000000000000"]; !ok {
		t.Errorf("the second session's identical record id collapsed into the first")
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v", sessions)
	}
	for _, si := range sessions {
		if si.StartedAt != 1787889300000 || si.UpdatedAt != 1787889300000 {
			t.Errorf("session %q timestamps = %d..%d, want the turn's time",
				si.SessionID, si.StartedAt, si.UpdatedAt)
		}
	}
	_ = firstPath
	_ = secondPath
}

func newPiSessionHeader(t *testing.T, id, cwd string) string {
	t.Helper()
	return `{"type":"session","id":"` + id + `","timestamp":"2026-08-28T03:54:41.612Z","cwd":"` + cwd + `"}`
}
