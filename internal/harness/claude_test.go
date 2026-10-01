package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// scanFixture runs one adapter over a directory tree and returns every emitted event.
func scanFixture(t *testing.T, a Adapter, root string, full bool, states map[string]ScanState) ([]Event, []SessionInfo, []SourceRef, map[string]ScanState) {
	t.Helper()
	var events []Event
	var sessions []SessionInfo
	var deletes []SourceRef
	next := map[string]ScanState{}
	for k, v := range states {
		next[k] = v
	}
	err := a.Scan(context.Background(), ScanInput{
		States: states,
		Roots:  []string{root},
		Full:   full,
		Emit: func(b Batch) error {
			events = append(events, b.Events...)
			sessions = append(sessions, b.Sessions...)
			deletes = append(deletes, b.Deletes...)
			for _, st := range b.States {
				next[st.Key] = ScanState{
					Mtime: st.Mtime, Size: st.Size, Inode: st.Inode,
					Offset: st.Offset, Watermark: st.Watermark,
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return events, sessions, deletes, next
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// A Claude transcript repeats the same usage object on every content-block record of one
// API message, so counting lines instead of ids would over-count several times over.
func TestClaudeDedupesRepeatedUsagePerMessage(t *testing.T) {
	line := `{"type":"assistant","requestId":"req_1","timestamp":"2026-06-22T01:47:20.091Z","sessionId":"sess-a","cwd":"/proj/a","isSidechain":false,"message":{"id":"msg_1","model":"claude-opus-4-8","stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":20}}}`
	// Same id, smaller usage: a partial record written while the response streamed.
	partial := `{"type":"assistant","requestId":"req_1","timestamp":"2026-06-22T01:47:19.000Z","sessionId":"sess-a","cwd":"/proj/a","message":{"id":"msg_1","model":"claude-opus-4-8","stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	noise := `{"type":"tool_result","toolUseID":"t1","content":[{"type":"text","text":"ignore me"}]}`
	zero := `{"type":"assistant","requestId":"req_synth","timestamp":"2026-06-22T01:48:00.000Z","sessionId":"sess-a","message":{"id":"msg_synth","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	root := writeTree(t, map[string]string{
		"-proj-a/sess-a.jsonl": partial + "\n" + line + "\n" + noise + "\n" + zero + "\n",
	})

	events, sessions, _, _ := scanFixture(t, newClaudeAdapter(""), root, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (one message id, noise and zero-usage dropped): %+v", len(events), events)
	}
	e := events[0]
	if e.EventKey != "claude|msg_1|req_1" {
		t.Errorf("EventKey = %q", e.EventKey)
	}
	if e.Input != 100 || e.Output != 50 || e.CacheWrite != 10 || e.CacheRead != 20 {
		t.Errorf("buckets = %d/%d/%d/%d, want 100/50/10/20", e.Input, e.Output, e.CacheWrite, e.CacheRead)
	}
	if e.Total != 180 {
		t.Errorf("total = %d, want 180", e.Total)
	}
	if e.Project != "/proj/a" {
		t.Errorf("project = %q, want the record cwd", e.Project)
	}
	if e.SessionID != "sess-a" || e.AgentType != "main" {
		t.Errorf("session = %q/%q", e.SessionID, e.AgentType)
	}
	if e.Outcome != "tool_use" {
		t.Errorf("outcome = %q", e.Outcome)
	}
	// The earliest timestamp in the file for that message must win.
	if got := e.TS; got != mustParse(t, "2026-06-22T01:47:19.000Z") {
		t.Errorf("ts = %d, want the partial record's earlier timestamp", got)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "sess-a" || sessions[0].Model != "claude-opus-4-8" {
		t.Errorf("sessions = %+v", sessions)
	}
}

// Subagent transcripts belong to their parent session so the work is counted once, in
// the session that produced it.
func TestClaudeSubagentAttributedToParentSession(t *testing.T) {
	main := `{"type":"assistant","requestId":"req_m","timestamp":"2026-06-22T02:00:00.000Z","sessionId":"sess-parent","cwd":"/proj/x","message":{"id":"msg_m","model":"claude-opus-4-8","stop_reason":"stop","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	sub := `{"type":"assistant","requestId":"req_s","timestamp":"2026-06-22T02:01:00.000Z","sessionId":"sess-parent","cwd":"/proj/x","isSidechain":true,"message":{"id":"msg_s","model":"claude-opus-4-8","stop_reason":"stop","usage":{"input_tokens":7,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	root := writeTree(t, map[string]string{
		"-proj-x/sess-parent.jsonl":                        main + "\n",
		"-proj-x/sess-parent/subagents/agent-abc123.jsonl": sub + "\n",
	})

	events, _, _, _ := scanFixture(t, newClaudeAdapter(""), root, true, nil)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	byKey := map[string]Event{}
	for _, e := range events {
		byKey[e.EventKey] = e
	}
	parent, ok := byKey["claude|msg_m|req_m"]
	if !ok || parent.AgentType != "main" {
		t.Errorf("parent event = %+v (ok=%v)", parent, ok)
	}
	child, ok := byKey["claude|msg_s|req_s"]
	if !ok {
		t.Fatalf("subagent event missing: %+v", byKey)
	}
	if child.SessionID != "sess-parent" {
		t.Errorf("subagent session = %q, want the parent session", child.SessionID)
	}
	if child.AgentType != "subagent" || child.AgentName != "agent-abc123" {
		t.Errorf("subagent identity = %q/%q", child.AgentType, child.AgentName)
	}
}

// An unchanged file must be skipped entirely; a grown file must resume at its offset.
func TestClaudeIncrementalResumeAndSkip(t *testing.T) {
	line := `{"type":"assistant","requestId":"req_1","timestamp":"2026-06-22T01:47:20.091Z","sessionId":"s1","cwd":"/p","message":{"id":"msg_1","model":"m","stop_reason":"stop","usage":{"input_tokens":10,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	root := writeTree(t, map[string]string{"-p/s1.jsonl": line + "\n"})
	path := filepath.Join(root, "-p", "s1.jsonl")

	a := newClaudeAdapter("")
	events, _, _, states := scanFixture(t, a, root, true, nil)
	if len(events) != 1 {
		t.Fatalf("first scan events = %d, want 1", len(events))
	}

	// Second scan with the stored cursor: nothing changed, nothing emitted.
	events2, _, _, states2 := scanFixture(t, a, root, false, states)
	if len(events2) != 0 {
		t.Fatalf("second scan events = %d, want 0 (unchanged file must be skipped)", len(events2))
	}
	st, ok := states2[fileKey(path)]
	if !ok || st.Offset == 0 {
		t.Fatalf("cursor not persisted: %+v", states2)
	}

	// Append a new record: only that record is parsed.
	second := `{"type":"assistant","requestId":"req_2","timestamp":"2026-06-22T01:50:00.000Z","sessionId":"s1","cwd":"/p","message":{"id":"msg_2","model":"m","stop_reason":"stop","usage":{"input_tokens":20,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(second + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	events3, _, deletes, _ := scanFixture(t, a, root, false, states2)
	if len(events3) != 1 || events3[0].EventKey != "claude|msg_2|req_2" {
		t.Fatalf("third scan events = %+v, want only the appended record", events3)
	}
	if len(deletes) != 0 {
		t.Errorf("deletes = %+v, want none for a growing file", deletes)
	}
}

// A truncated transcript must be re-read from the start with its old events dropped,
// otherwise the stale rows would survive forever.
func TestClaudeDetectsTruncationAndRequestsDelete(t *testing.T) {
	line := `{"type":"assistant","requestId":"req_1","timestamp":"2026-06-22T01:47:20.091Z","sessionId":"s1","cwd":"/p","message":{"id":"msg_1","model":"m","stop_reason":"stop","usage":{"input_tokens":10,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	root := writeTree(t, map[string]string{"-p/s1.jsonl": line + "\n"})
	path := filepath.Join(root, "-p", "s1.jsonl")

	a := newClaudeAdapter("")
	_, _, _, states := scanFixture(t, a, root, true, nil)

	// Replace the file with different, shorter content.
	// Deliberately shorter than the original so the truncation path is exercised.
	replacement := `{"type":"assistant","requestId":"r","timestamp":"2026-06-22T03:00:00.000Z","message":{"id":"m","usage":{"input_tokens":9}}}`
	if err := os.WriteFile(path, []byte(replacement+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	events, _, deletes, _ := scanFixture(t, a, root, false, states)
	if len(deletes) != 1 || deletes[0].SourceFile != path {
		t.Fatalf("deletes = %+v, want one for %s", deletes, path)
	}
	if len(events) != 1 || events[0].EventKey != "claude|m|r" {
		t.Fatalf("events = %+v, want the new content", events)
	}
}

func TestClaudeAvailableAndRoots(t *testing.T) {
	a := newClaudeAdapter("")
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/moved-claude")
	roots := a.Roots(context.Background(), []string{"/tmp/extra"})
	want := map[string]bool{"/tmp/moved-claude/projects": true, "/tmp/extra": true}
	seen := map[string]bool{}
	for _, r := range roots {
		seen[r] = true
	}
	for w := range want {
		if !seen[w] {
			t.Errorf("root %q missing from %v", w, roots)
		}
	}
	if a.Available([]string{"/definitely/not/here"}) {
		t.Error("Available = true for a missing root")
	}
	if a.ParserVersion() != claudeParserVersion {
		t.Errorf("ParserVersion = %d", a.ParserVersion())
	}
	if a.ID() != "claude" || a.DisplayName() != "Claude Code" {
		t.Errorf("identity = %q/%q", a.ID(), a.DisplayName())
	}
}

func mustParse(t *testing.T, s string) int64 {
	t.Helper()
	ms, ok := ParseISO(s)
	if !ok {
		t.Fatalf("ParseISO(%q) failed", s)
	}
	return ms
}
