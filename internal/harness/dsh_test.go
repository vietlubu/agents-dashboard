package harness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// dshHeaderLine is the first line of every session log.
func dshHeaderLine(sessionID, cwd string, createdAt int64, depth int) string {
	return fmt.Sprintf(
		`{"type":"session","version":4,"id":%q,"createdAt":%d,"cwd":%q,"isSeeded":false,"delegationDepth":%d,"agentPreset":"standard"}`,
		sessionID, createdAt, cwd, depth)
}

func dshContextLine(seq, at int64, provider, model string) string {
	return fmt.Sprintf(
		`{"type":"request/context","seq":%d,"time":%d,"data":{"provider":%q,"model":%q,"contextWindow":1000000,"systemPromptUpdate":"in-history"}}`,
		seq, at, provider, model)
}

// dshUsageJSON builds the token payload. reasoningTokens is emitted only when set, matching
// the OpenAI convention where outputTokens already includes it.
func dshUsageJSON(in, out, cacheRead, cacheWrite, reasoning int64) string {
	total := in + out + cacheRead + cacheWrite
	if reasoning > 0 {
		return fmt.Sprintf(
			`{"inputTokens":%d,"outputTokens":%d,"cacheReadTokens":%d,"cacheWriteTokens":%d,"reasoningTokens":%d,"totalTokens":%d}`,
			in, out, cacheRead, cacheWrite, reasoning, total)
	}
	return fmt.Sprintf(
		`{"inputTokens":%d,"outputTokens":%d,"cacheReadTokens":%d,"cacheWriteTokens":%d,"totalTokens":%d}`,
		in, out, cacheRead, cacheWrite, total)
}

// dshMessageLine is an assembled assistant/message. It embeds a content array holding
// response text and a stream that repeats the usage, so a test can assert neither the text
// nor a doubled count ever reaches an event.
func dshMessageLine(seq, at, turn, step int64, provider, model string, usage string, interrupted bool) string {
	interrupt := ""
	if interrupted {
		interrupt = `,"interrupted":true`
	}
	return fmt.Sprintf(
		`{"type":"assistant/message","seq":%d,"time":%d,"data":{"turn":%d,"step":%d,"message":{"role":"assistant","content":[{"type":"text","text":"TOP_SECRET_RESPONSE"}],"source":{"kind":"model","provider":%q,"model":%q,"replayState":{"raw":"TOP_SECRET_STREAM"}}},"usage":%s%s,"stream":[{"type":"chunk","chunk":{"type":"usage","usage":%s}}],"surfaceOp":"append"}}`,
		seq, at, turn, step, provider, model, usage, interrupt, usage)
}

func dshChunkLine(seq, at, turn, step int64, usage string) string {
	return fmt.Sprintf(
		`{"type":"assistant/chunk","seq":%d,"time":%d,"data":{"turn":%d,"step":%d,"chunk":{"type":"usage","usage":%s}}}`,
		seq, at, turn, step, usage)
}

// dshWriteLog writes a session log, compressing each batch into its own Zstandard frame and
// concatenating them exactly as DSH's persistence backend does. An uncompressed log is the
// raw JSONL a root configured with compression: 'none' produces.
func dshWriteLog(t *testing.T, root, rel string, batches [][]string, compressed bool) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var buf bytes.Buffer
	for _, batch := range batches {
		plain := strings.Join(batch, "\n") + "\n"
		if !compressed {
			buf.WriteString(plain)
			continue
		}
		enc, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatalf("zstd writer: %v", err)
		}
		buf.Write(enc.EncodeAll([]byte(plain), nil))
		enc.Close()
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}

const (
	dshSessionA = "session-ce8ccd2b-133d-45ba-b3be-251ba3920c58"
	dshCwdA     = "/Users/vietlubu/Projects/vietlubu/kitesRO/idleRO/roBrowserLegacy"
)

func TestDshIdentityRootsAndFileNames(t *testing.T) {
	home := t.TempDir()
	a := newDshAdapter(home)
	roots := a.Roots(nil, []string{"/extra"})
	want := filepath.Join(home, ".dsh", "sessions")
	if len(roots) != 2 || roots[0] != want || roots[1] != "/extra" {
		t.Errorf("roots = %v, want [%s /extra]", roots, want)
	}
	if a.Available([]string{"/definitely/not/here"}) {
		t.Error("Available = true for a missing root")
	}
	if a.ID() != "dsh" || a.DisplayName() != "DeepSeek Harness" || a.ParserVersion() != dshParserVersion {
		t.Errorf("identity = %q/%q/%d", a.ID(), a.DisplayName(), a.ParserVersion())
	}

	t.Setenv("DSH_HOME", "/custom/dsh")
	if got := a.Roots(nil, nil); len(got) != 1 || got[0] != filepath.Join("/custom/dsh", "sessions") {
		t.Errorf("DSH_HOME roots = %v", got)
	}

	cases := []struct {
		name       string
		generation int
		compressed bool
		ok         bool
	}{
		{"session.jsonl", 0, false, true},
		{"session.jsonl.zstd", 0, true, true},
		{"session.v4.jsonl.zstd", 4, true, true},
		{"session.v3.jsonl", 3, false, true},
		{"session.v10.jsonl.zstd", 10, true, true},
		{"session.lock", 0, false, false},
		{"session.v.jsonl", 0, false, false},
		{"session.vx.jsonl", 0, false, false},
		{"other.jsonl", 0, false, false},
	}
	for _, c := range cases {
		gen, comp, ok := dshParseFileName(c.name)
		if ok != c.ok || (ok && (gen != c.generation || comp != c.compressed)) {
			t.Errorf("dshParseFileName(%q) = (%d,%v,%v), want (%d,%v,%v)",
				c.name, gen, comp, ok, c.generation, c.compressed, c.ok)
		}
	}
}

// A compressed log is a concatenation of independent frames, and the adapter must decode
// every one of them: a single-frame decoder would stop after the header.
func TestDshDecodesEveryFrame(t *testing.T) {
	root := t.TempDir()
	steps := []string{
		dshMessageLine(27, 1791592721707, 1, 1, "deepseek-account", "deepseek-flash", dshUsageJSON(15583, 163, 0, 0, 0), false),
		dshMessageLine(35, 1791592722960, 1, 2, "deepseek-account", "deepseek-flash", dshUsageJSON(1288, 105, 15744, 0, 0), false),
		dshMessageLine(43, 1791592724359, 1, 3, "deepseek-account", "deepseek-flash", dshUsageJSON(2820, 180, 17024, 0, 0), false),
	}
	dshWriteLog(t, root, "--slug--/"+dshSessionA+"/session.v4.jsonl.zstd", [][]string{
		{dshHeaderLine(dshSessionA, dshCwdA, 1791592694411, 0)},
		{dshContextLine(21, 1791592720295, "deepseek-account", "deepseek-flash"), steps[0]},
		{steps[1]},
		{steps[2]},
	}, true)

	events, sessions, _, _ := scanFixture(t, newDshAdapter(""), root, true, nil)
	if len(events) != 3 {
		t.Fatalf("events = %d, want one per step: %+v", len(events), events)
	}

	first := events[0]
	if first.EventKey != "dsh|"+dshSessionA+"|1|1" {
		t.Errorf("key = %q", first.EventKey)
	}
	if first.Input != 15583 || first.Output != 163 || first.CacheRead != 0 || first.CacheWrite != 0 || first.Total != 15746 {
		t.Errorf("step 1 buckets = %d/%d/%d/%d total %d", first.Input, first.Output, first.CacheRead, first.CacheWrite, first.Total)
	}
	if first.Model != "deepseek-flash" || first.Provider != "deepseek-account" {
		t.Errorf("route = %q/%q", first.Model, first.Provider)
	}
	if first.SessionID != dshSessionA || first.Project != dshCwdA {
		t.Errorf("identity = %q / %q", first.SessionID, first.Project)
	}
	if first.AgentType != "main" || first.AgentName != "main" {
		t.Errorf("agent = %q/%q", first.AgentType, first.AgentName)
	}
	if first.CostUSD != nil {
		t.Errorf("cost = %v, want none (DSH reports tokens, not USD)", first.CostUSD)
	}
	// inputTokens is disjoint from the cache buckets; step 2 proves the split is preserved.
	if events[1].Input != 1288 || events[1].CacheRead != 15744 || events[1].Total != 17137 {
		t.Errorf("step 2 buckets = in %d cacheRead %d total %d", events[1].Input, events[1].CacheRead, events[1].Total)
	}

	if len(sessions) != 1 || sessions[0].SessionID != dshSessionA || sessions[0].Project != dshCwdA {
		t.Fatalf("sessions = %+v", sessions)
	}
	if sessions[0].StartedAt == 0 || sessions[0].UpdatedAt < sessions[0].StartedAt {
		t.Errorf("session timestamps = %d..%d", sessions[0].StartedAt, sessions[0].UpdatedAt)
	}

	// Privacy contract: the response text and the raw provider stream share these lines
	// but must never reach an event.
	for _, e := range events {
		if strings.Contains(e.Model, "TOP_SECRET") || strings.Contains(e.AgentName, "TOP_SECRET") {
			t.Errorf("secret text leaked into %+v", e)
		}
	}
}

// DSH retains a predecessor generation after a format migration; the numerically highest
// one is live and counting both would double the session.
func TestDshSelectsHighestGeneration(t *testing.T) {
	root := t.TempDir()
	dir := "--slug--/" + dshSessionA + "/"
	dshWriteLog(t, root, dir+"session.v3.jsonl.zstd", [][]string{
		{dshHeaderLine(dshSessionA, dshCwdA, 1000, 0)},
		{dshMessageLine(27, 2000, 1, 1, "p", "old-model", dshUsageJSON(999999, 0, 0, 0, 0), false)},
	}, true)
	v4 := dshWriteLog(t, root, dir+"session.v4.jsonl.zstd", [][]string{
		{dshHeaderLine(dshSessionA, dshCwdA, 1000, 0)},
		{dshMessageLine(27, 2000, 1, 1, "p", "new-model", dshUsageJSON(100, 0, 0, 0, 0), false)},
	}, true)
	// A session.lock sits beside the log and must never be read as one.
	if err := os.WriteFile(filepath.Join(filepath.Dir(v4), "session.lock"), nil, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	events, _, _, states := scanFixture(t, newDshAdapter(""), root, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %d, want only the live generation: %+v", len(events), events)
	}
	if events[0].Model != "new-model" || events[0].Input != 100 {
		t.Errorf("scanned the wrong generation: %+v", events[0])
	}
	if len(states) != 1 {
		t.Fatalf("states = %d, want one (the v4 log)", len(states))
	}
	for k := range states {
		if !strings.Contains(k, "session.v4.jsonl.zstd") {
			t.Errorf("state key = %q, want the v4 log", k)
		}
	}
}

// A step's usage is reported twice — an early chunk sample and the assembled message — and
// the later record must supersede the earlier one rather than being summed with it.
func TestDshCollapsesChunkAndMessagePerStep(t *testing.T) {
	root := t.TempDir()
	dshWriteLog(t, root, "--slug--/"+dshSessionA+"/session.v4.jsonl.zstd", [][]string{
		{dshHeaderLine(dshSessionA, dshCwdA, 1000, 0)},
		{
			dshContextLine(1, 1000, "deepseek-account", "deepseek-flash"),
			dshChunkLine(2, 1001, 1, 1, dshUsageJSON(100, 10, 0, 0, 0)),
			dshMessageLine(3, 1002, 1, 1, "deepseek-account", "deepseek-flash", dshUsageJSON(15583, 163, 0, 0, 0), false),
		},
	}, true)

	events, _, _, _ := scanFixture(t, newDshAdapter(""), root, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one per step (chunk and message are the same step)", events)
	}
	if events[0].Input != 15583 || events[0].Output != 163 {
		t.Errorf("buckets = %d/%d, want the assembled message's values", events[0].Input, events[0].Output)
	}
}

// An aborted turn still bills its tokens, so its usage is kept and its outcome marked.
func TestDshCountsInterruptedTurnAndReasoning(t *testing.T) {
	root := t.TempDir()
	dshWriteLog(t, root, "--slug--/"+dshSessionA+"/session.v4.jsonl.zstd", [][]string{
		{dshHeaderLine(dshSessionA, dshCwdA, 1000, 0)},
		{dshMessageLine(3, 1002, 1, 1, "deepseek-account", "deepseek-flash", dshUsageJSON(500, 200, 0, 0, 50), true)},
	}, true)

	events, _, _, _ := scanFixture(t, newDshAdapter(""), root, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want the interrupted turn's usage", events)
	}
	if events[0].Outcome != "aborted" {
		t.Errorf("outcome = %q, want aborted", events[0].Outcome)
	}
	// outputTokens already includes reasoningTokens, so reasoning is informational and
	// excluded from the billable total.
	if events[0].Reasoning != 50 || events[0].Total != 700 {
		t.Errorf("reasoning/total = %d/%d, want 50/700", events[0].Reasoning, events[0].Total)
	}
}

// A root configured with compression: 'none' writes raw JSONL, which the adapter must read
// without a Zstandard frame.
func TestDshReadsUncompressedLog(t *testing.T) {
	root := t.TempDir()
	dshWriteLog(t, root, "--slug--/"+dshSessionA+"/session.jsonl", [][]string{
		{dshHeaderLine(dshSessionA, dshCwdA, 1000, 0)},
		{dshMessageLine(3, 1002, 1, 1, "deepseek-account", "deepseek-flash", dshUsageJSON(1000, 20, 30, 40, 0), false)},
	}, false)

	events, _, _, _ := scanFixture(t, newDshAdapter(""), root, true, nil)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Input != 1000 || events[0].Output != 20 || events[0].CacheRead != 30 || events[0].CacheWrite != 40 || events[0].Total != 1090 {
		t.Errorf("buckets = %+v", events[0])
	}
}

// A subagent session records its delegation depth in the header.
func TestDshMarksSubagentSessions(t *testing.T) {
	root := t.TempDir()
	dshWriteLog(t, root, "--slug--/session-child/session.v4.jsonl.zstd", [][]string{
		{dshHeaderLine("session-child", dshCwdA, 1000, 1)},
		{dshMessageLine(3, 1002, 1, 1, "p", "m", dshUsageJSON(10, 1, 0, 0, 0), false)},
	}, true)

	events, sessions, _, _ := scanFixture(t, newDshAdapter(""), root, true, nil)
	if len(events) != 1 || events[0].AgentType != "subagent" || events[0].AgentName != "session-child" {
		t.Fatalf("events = %+v, want a subagent step", events)
	}
	if len(sessions) != 1 || sessions[0].AgentType != "subagent" {
		t.Errorf("sessions = %+v", sessions)
	}
}

// An appended frame is decoded incrementally: the resumed scan emits only the new step,
// and an unchanged file is skipped entirely.
func TestDshResumesAtFrameBoundary(t *testing.T) {
	root := t.TempDir()
	rel := "--slug--/" + dshSessionA + "/session.v4.jsonl.zstd"
	dshWriteLog(t, root, rel, [][]string{
		{dshHeaderLine(dshSessionA, dshCwdA, 1000, 0)},
		{dshMessageLine(3, 1002, 1, 1, "p", "m", dshUsageJSON(100, 1, 0, 0, 0), false)},
	}, true)
	path := filepath.Join(root, filepath.FromSlash(rel))

	a := newDshAdapter("")
	_, _, _, states := scanFixture(t, a, root, true, nil)

	// Nothing changed: the file is skipped.
	again, _, _, _ := scanFixture(t, a, root, false, states)
	if len(again) != 0 {
		t.Fatalf("unchanged rescan = %+v, want none", again)
	}

	// Append a second, independent frame — exactly how a live session grows.
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	frame := enc.EncodeAll([]byte(dshMessageLine(4, 2000, 1, 2, "p", "m", dshUsageJSON(200, 2, 0, 0, 0), false)+"\n"), nil)
	enc.Close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.Write(frame); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	events, _, _, _ := scanFixture(t, a, root, false, states)
	if len(events) != 1 {
		t.Fatalf("resume events = %+v, want only the appended step", events)
	}
	if events[0].EventKey != "dsh|"+dshSessionA+"|1|2" || events[0].Input != 200 {
		t.Errorf("resumed step = %+v", events[0])
	}
}

// A torn trailing frame is a normal crash state, not corruption. The adapter must keep the
// records decoded before it and fall back to a full re-read rather than lose usage.
func TestDshRecoversFromTornTrailingFrame(t *testing.T) {
	root := t.TempDir()
	rel := "--slug--/" + dshSessionA + "/session.v4.jsonl.zstd"
	dshWriteLog(t, root, rel, [][]string{
		{dshHeaderLine(dshSessionA, dshCwdA, 1000, 0)},
		{dshMessageLine(3, 1002, 1, 1, "p", "m", dshUsageJSON(100, 1, 0, 0, 0), false)},
	}, true)
	path := filepath.Join(root, filepath.FromSlash(rel))

	a := newDshAdapter("")
	_, _, _, states := scanFixture(t, a, root, true, nil)

	// Append a frame whose tail is missing, as an interrupted write leaves it.
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	frame := enc.EncodeAll([]byte(dshMessageLine(4, 2000, 1, 2, "p", "m", dshUsageJSON(200, 2, 0, 0, 0), false)+"\n"), nil)
	enc.Close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.Write(frame[:len(frame)-5]); err != nil {
		t.Fatalf("append torn frame: %v", err)
	}
	f.Close()

	events, _, _, next := scanFixture(t, a, root, false, states)
	if len(events) == 0 {
		t.Fatal("torn frame lost all usage; want the completed records retained")
	}
	// The offset must not advance past the damaged tail, so the completed frames are
	// re-read once the writer repairs the file.
	key := fileKey(path)
	if next[key].Offset != 0 {
		t.Errorf("offset = %d after a torn frame, want 0 (re-read from the start)", next[key].Offset)
	}
}
