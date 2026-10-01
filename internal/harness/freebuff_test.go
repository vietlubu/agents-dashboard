package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freebuffStart writes the per-step Start record: it is the one that carries context
// occupancy, and it embeds a truncated prompt in its msg string.
func freebuffStart(ts string, iteration int, runID string, ctx int) string {
	return fmt.Sprintf(
		`{"level":"DEBUG","timestamp":%q,"data":{"iteration":%d,"runId":%q,"model":"fbm1.abc","duration":1,"contextTokenCount":%d,"systemTokens":6761,"toolNames":["read_files"]},"msg":"Start agent base3-free-catalog step %d (oh2541zx7u - Prompt: TOP_SECRET_PROMPT)"}`,
		ts, iteration, runID, ctx, iteration)
}

// freebuffEnd writes the per-step End record: it carries the wall time and whether the
// turn ended, plus the whole response and tool payloads that must never be read.
func freebuffEnd(ts string, iteration int, runID string, duration int, endTurn bool) string {
	return fmt.Sprintf(
		`{"level":"DEBUG","timestamp":%q,"data":{"iteration":%d,"runId":%q,"model":"fbm1.abc","duration":%d,"stepCreditsUsed":0,"shouldEndTurn":%t,"fullResponse":"TOP_SECRET_RESPONSE","toolResults":[{"content":"TOP_SECRET_FILE"}]},"msg":"End agent base3-free-catalog step %d (oh2541zx7u)"}`,
		ts, iteration, runID, duration, endTurn, iteration)
}

// The adapter records the per-step increase in context occupancy, pairs each Start with
// its End for timing and outcome, and never reads the prompt or response text that shares
// the same log lines.
func TestFreebuffRecordsPerStepIncreaseAndIgnoresPromptText(t *testing.T) {
	runID := "a6e8b072-4cc1-4b69-b109-52435f050c49"
	log := strings.Join([]string{
		freebuffStart("2026-10-01T15:51:43.208Z", 1, runID, 24862),
		freebuffEnd("2026-10-01T15:51:45.953Z", 1, runID, 2746, false),
		freebuffStart("2026-10-01T15:51:55.000Z", 2, runID, 33937),
		freebuffEnd("2026-10-01T15:52:06.487Z", 2, runID, 20531, true),
		// Filler that carries no step data at all.
		`{"level":"INFO","timestamp":"2026-10-01T15:49:28.187Z","msg":"[chat-runtime] Freebuff session over"}`,
	}, "\n") + "\n"

	root := writeTree(t, map[string]string{
		"hat-nhua-ui/chats/2026-10-01T15-51-23.555Z/log.jsonl": log,
	})

	events, sessions, _, states := scanFixture(t, newFreebuffAdapter(""), root, true, nil)
	if len(events) != 2 {
		t.Fatalf("events = %d, want one per step: %+v", len(events), events)
	}
	first := events[0]
	if first.EventKey != "freebuff|"+runID+"|1" {
		t.Errorf("key = %q", first.EventKey)
	}
	// Step one's increase is its whole context (nothing preceded it in this file).
	if first.Input != 24862 || first.Total != 24862 || first.Output != 0 {
		t.Errorf("step 1 buckets = in %d total %d out %d, want 24862/24862/0", first.Input, first.Total, first.Output)
	}
	if first.LatencyMs == nil || *first.LatencyMs != 2746 {
		t.Errorf("step 1 latency = %v, want the End record's duration", first.LatencyMs)
	}
	if first.Outcome != "tool_use" {
		t.Errorf("step 1 outcome = %q, want tool_use (shouldEndTurn false)", first.Outcome)
	}
	if first.Project != "hat-nhua-ui" || first.SessionID != runID {
		t.Errorf("identity = %q/%q", first.Project, first.SessionID)
	}
	if first.CostUSD != nil {
		t.Errorf("cost = %v, want none (Freebuff meters in Freebucks, not USD)", first.CostUSD)
	}

	second := events[1]
	// The increase is the difference, never the cumulative occupancy: summing occupancy
	// would multiply a session's context by its step count.
	if second.Input != 33937-24862 {
		t.Errorf("step 2 input = %d, want the increase 9075", second.Input)
	}
	if second.Outcome != "ok" {
		t.Errorf("step 2 outcome = %q, want ok", second.Outcome)
	}
	if second.LatencyMs == nil || *second.LatencyMs != 20531 {
		t.Errorf("step 2 latency = %v", second.LatencyMs)
	}

	if len(sessions) != 1 || sessions[0].SessionID != runID {
		t.Fatalf("sessions = %+v", sessions)
	}
	if sessions[0].StartedAt == 0 || sessions[0].UpdatedAt < sessions[0].StartedAt {
		t.Errorf("session timestamps = %d..%d", sessions[0].StartedAt, sessions[0].UpdatedAt)
	}

	// The privacy contract: prompt, response and tool payloads are on the same lines but
	// must never reach an event or a session.
	for _, e := range events {
		if strings.Contains(e.Model, "TOP_SECRET") || strings.Contains(e.Project, "TOP_SECRET") {
			t.Errorf("secret text leaked into event %+v", e)
		}
	}
	st := states[fileKey(filepath.Join(root, "hat-nhua-ui", "chats", "2026-10-01T15-51-23.555Z", "log.jsonl"))]
	if st.Watermark != 33937 {
		t.Errorf("watermark = %d, want the last context occupancy (33937)", st.Watermark)
	}
}

// A resumed scan must continue the occupancy baseline from the stored watermark instead of
// re-counting the whole context as new tokens.
func TestFreebuffResumeKeepsContextBaseline(t *testing.T) {
	runID := "run-1"
	first := freebuffStart("2026-10-01T15:51:43.208Z", 1, runID, 24862) + "\n" +
		freebuffEnd("2026-10-01T15:51:45.953Z", 1, runID, 100, false) + "\n" +
		freebuffStart("2026-10-01T15:51:55.000Z", 2, runID, 33937) + "\n"

	root := writeTree(t, map[string]string{"p/chats/2026-10-01T15-51-23.555Z/log.jsonl": first})
	path := filepath.Join(root, "p", "chats", "2026-10-01T15-51-23.555Z", "log.jsonl")

	a := newFreebuffAdapter("")
	_, _, _, states := scanFixture(t, a, root, true, nil)

	// Nothing changed: the file is skipped entirely.
	again, _, _, _ := scanFixture(t, a, root, false, states)
	if len(again) != 0 {
		t.Fatalf("unchanged rescan events = %+v, want none", again)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(freebuffStart("2026-10-01T15:52:00.000Z", 3, runID, 40000) + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	events, _, _, _ := scanFixture(t, a, root, false, states)
	if len(events) != 1 || events[0].EventKey != "freebuff|"+runID+"|3" {
		t.Fatalf("resume events = %+v, want only the appended step", events)
	}
	if events[0].Input != 40000-33937 {
		t.Errorf("resumed step input = %d, want 6063 (baseline carried in the watermark)", events[0].Input)
	}
}

// A brand-new run inside the same chat directory restarts the context, so its first step
// must not be measured against the previous run's much larger context.
func TestFreebuffNewRunResetsBaseline(t *testing.T) {
	log := freebuffStart("2026-10-01T15:51:43.208Z", 1, "run-a", 60000) + "\n" +
		freebuffStart("2026-10-01T16:00:00.000Z", 1, "run-b", 5000) + "\n"
	root := writeTree(t, map[string]string{"p/chats/2026-10-01T15-51-23.555Z/log.jsonl": log})

	events, _, _, _ := scanFixture(t, newFreebuffAdapter(""), root, true, nil)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want one per run", events)
	}
	bySession := map[string]int64{}
	for _, e := range events {
		bySession[e.SessionID] = e.Input
	}
	if bySession["run-a"] != 60000 {
		t.Errorf("run-a input = %d, want 60000", bySession["run-a"])
	}
	if bySession["run-b"] != 5000 {
		t.Errorf("run-b input = %d, want its own 5000, not a remeasured delta", bySession["run-b"])
	}
}

func TestFreebuffRootsAvailabilityAndIdentity(t *testing.T) {
	home := t.TempDir()
	a := newFreebuffAdapter(home)
	roots := a.Roots(nil, []string{"/extra"})
	if len(roots) != 2 || roots[0] != filepath.Join(home, ".config", "manicode", "projects") || roots[1] != "/extra" {
		t.Errorf("roots = %v", roots)
	}
	if a.Available([]string{"/definitely/not/here"}) {
		t.Error("Available = true for a missing root")
	}
	if a.ID() != "freebuff" || a.DisplayName() != "Freebuff" || a.ParserVersion() != freebuffParserVersion {
		t.Errorf("identity = %q/%q/%d", a.ID(), a.DisplayName(), a.ParserVersion())
	}

	project, chat := freebuffIdentity("/roots", "/roots/myproj/chats/2026-10-01T15-51-23.555Z/log.jsonl")
	if project != "myproj" || chat != "2026-10-01T15-51-23.555Z" {
		t.Errorf("identity = %q/%q", project, chat)
	}
}
