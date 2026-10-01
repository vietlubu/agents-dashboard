package harness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func collect(path string, offset int64, markers [][]byte) ([]string, ReadResult, error) {
	var lines []string
	res, err := ScanFile(context.Background(), path, offset, markers, func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	return lines, res, err
}

// A partially written trailing line must not be consumed, and must be picked up exactly
// once on the next scan. This is what makes incremental scans safe while an agent is
// still writing to the file.
func TestScanFileResumesAfterPartialLine(t *testing.T) {
	path := writeFile(t, "s.jsonl", "{\"a\":1}\n{\"a\":2}\n{\"a\":3")

	marker := [][]byte{[]byte("\"a\"")}
	first, res, err := collect(path, 0, marker)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first scan lines = %v, want 2", first)
	}
	if want := int64(len("{\"a\":1}\n{\"a\":2}\n")); res.NewOffset != want {
		t.Fatalf("NewOffset = %d, want %d (must stop past the last complete line)", res.NewOffset, want)
	}
	if res.Truncated {
		t.Error("Truncated = true on a growing file")
	}

	// The writer completes the record.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString("}\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	second, res2, err := collect(path, res.NewOffset, marker)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(second) != 1 || second[0] != "{\"a\":3}" {
		t.Fatalf("second scan lines = %v, want exactly the completed record", second)
	}
	if res2.NewOffset <= res.NewOffset {
		t.Errorf("NewOffset did not advance: %d -> %d", res.NewOffset, res2.NewOffset)
	}

	// A third scan with no new bytes must emit nothing.
	third, res3, err := collect(path, res2.NewOffset, marker)
	if err != nil {
		t.Fatalf("third scan: %v", err)
	}
	if len(third) != 0 {
		t.Errorf("third scan lines = %v, want none", third)
	}
	if res3.NewOffset != res2.NewOffset {
		t.Errorf("NewOffset drifted on an unchanged file: %d -> %d", res2.NewOffset, res3.NewOffset)
	}
}

// A truncated or replaced file must report Truncated so the caller can drop the events
// it previously parsed from that path.
func TestScanFileDetectsTruncation(t *testing.T) {
	path := writeFile(t, "s.jsonl", "{\"a\":1}\n{\"a\":2}\n")
	_, res, err := collect(path, 0, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.NewOffset != res.Size {
		t.Fatalf("NewOffset = %d, size = %d", res.NewOffset, res.Size)
	}

	if err := os.WriteFile(path, []byte("{\"a\":9}\n"), 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	lines, res2, err := collect(path, res.NewOffset, nil)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if !res2.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(lines) != 1 || lines[0] != "{\"a\":9}" {
		t.Errorf("lines = %v, want the new content", lines)
	}
}

func TestScanFileMarkerPrefilter(t *testing.T) {
	content := "{\"kind\":\"noise\"}\n{\"kind\":\"usage\",\"v\":1}\n{\"kind\":\"noise2\"}\n"
	path := writeFile(t, "s.jsonl", content)

	lines, _, err := collect(path, 0, [][]byte{[]byte("\"usage\"")})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "\"usage\"") {
		t.Fatalf("lines = %v, want only the usage record", lines)
	}

	// No markers means every line is offered to the callback.
	all, _, err := collect(path, 0, nil)
	if err != nil {
		t.Fatalf("scan all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("lines = %v, want 3", all)
	}
}

// An oversized line is skipped without allocating without limit, and the stream must
// stay aligned so the records after it are still read.
func TestScanFileSkipsOversizedLine(t *testing.T) {
	huge := strings.Repeat("x", 3000)
	content := "{\"a\":1}\n" + huge + "{\"a\":\"tail\"}\n" + "{\"a\":2}\n"
	path := writeFile(t, "s.jsonl", content)

	var lines []string
	res, err := ScanFileLimit(context.Background(), path, 0, [][]byte{[]byte("\"a\"")}, 1024,
		func(line []byte) error {
			lines = append(lines, string(line))
			return nil
		})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.OversizedLines != 1 {
		t.Errorf("OversizedLines = %d, want 1", res.OversizedLines)
	}
	if len(lines) != 2 || lines[0] != "{\"a\":1}" || lines[1] != "{\"a\":2}" {
		t.Fatalf("lines = %v, want the two records around the oversized line", lines)
	}
	if res.NewOffset != res.Size {
		t.Errorf("NewOffset = %d, want the whole file (%d)", res.NewOffset, res.Size)
	}
}

// A line that spans multiple read chunks must be reassembled.
func TestScanFileReassemblesLineAcrossChunks(t *testing.T) {
	// chunkSize is 64 KiB; make one record comfortably larger.
	payload := strings.Repeat("y", chunkSize+1234)
	content := "{\"a\":\"" + payload + "\"}\n{\"a\":1}\n"
	path := writeFile(t, "s.jsonl", content)

	lines, res, err := collect(path, 0, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if len(lines[0]) != len(payload)+len("{\"a\":\"\"}") {
		t.Errorf("first line length = %d, want %d", len(lines[0]), len(payload)+len("{\"a\":\"\"}"))
	}
	if !bytes.HasSuffix([]byte(lines[0]), []byte(payload+"\"}")) {
		t.Error("first line was not reassembled correctly")
	}
	if res.OversizedLines != 0 {
		t.Errorf("OversizedLines = %d, want 0 (under the 4 MiB default)", res.OversizedLines)
	}
}

func TestScanFileReportsSignature(t *testing.T) {
	path := writeFile(t, "s.jsonl", "{\"a\":1}\n")
	_, res, err := collect(path, 0, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if res.Size != fi.Size() || res.Mtime != fi.ModTime().UnixMilli() {
		t.Errorf("signature = (%d,%d), want (%d,%d)", res.Size, res.Mtime, fi.Size(), fi.ModTime().UnixMilli())
	}
	if res.Inode == 0 {
		t.Error("Inode = 0; the inode is what detects a replaced file")
	}
}

func TestScanFileContextCancellation(t *testing.T) {
	path := writeFile(t, "s.jsonl", "{\"a\":1}\n{\"a\":2}\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanFile(ctx, path, 0, nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("expected a cancellation error")
	}
}

// A callback failure (for example, a batch that could not be written) must abort the
// scan and report the offset of the last consumed line, never silently continue.
func TestScanFilePropagatesCallbackError(t *testing.T) {
	path := writeFile(t, "s.jsonl", "{\"a\":1}\n{\"a\":2}\n{\"a\":3}\n")
	want := errors.New("sink is full")
	res, err := ScanFile(context.Background(), path, 0, nil, func([]byte) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want the callback error", err)
	}
	if res.NewOffset == 0 {
		t.Error("NewOffset = 0; the consumed line's offset must be reported even on failure")
	}
}

func TestFirstLineReadsHeaderOnly(t *testing.T) {
	payload := strings.Repeat("z", 20_000)
	path := writeFile(t, "s.jsonl", "{\"type\":\"session\",\"cwd\":\"/tmp\"}\n"+payload+"\n")
	line, err := firstLine(path, 8<<10)
	if err != nil {
		t.Fatalf("firstLine: %v", err)
	}
	if string(line) != "{\"type\":\"session\",\"cwd\":\"/tmp\"}" {
		t.Errorf("firstLine = %q", line)
	}
}

func TestNormalizeHelpers(t *testing.T) {
	if got := ClampTokens(-5); got != 0 {
		t.Errorf("ClampTokens(-5) = %d", got)
	}
	if got := ClampTokens(1 << 62); got != maxParsedTokenValue {
		t.Errorf("ClampTokens overflow = %d", got)
	}
	if got := Total(1, 2, 3, 4); got != 10 {
		t.Errorf("Total = %d", got)
	}

	cases := map[string]string{
		"stop": "ok", "end_turn": "ok", "toolUse": "tool_use", "tool_use": "tool_use",
		"aborted": "aborted", "error": "error", "": "unknown", "weird": "unknown",
	}
	for in, want := range cases {
		if got := Outcome(in); got != want {
			t.Errorf("Outcome(%q) = %q, want %q", in, got, want)
		}
	}

	if ms, ok := ParseISO("2026-09-30T15:39:37.048Z"); !ok || ms == 0 {
		t.Errorf("ParseISO RFC3339Nano failed: %d ok=%v", ms, ok)
	}
	if _, ok := ParseISO("not-a-date"); ok {
		t.Error("ParseISO accepted garbage")
	}

	if got := ParseEpochMs(1785336172); got != 1785336172000 {
		t.Errorf("seconds -> %d", got)
	}
	if got := ParseEpochMs(1785336172462); got != 1785336172462 {
		t.Errorf("milliseconds -> %d", got)
	}
	if got := ParseEpochMs(1785336172462000); got != 1785336172462 {
		t.Errorf("microseconds -> %d", got)
	}
	if got := ParseEpochMs(0); got != 0 {
		t.Errorf("zero -> %d", got)
	}

	if got := FileName("/a/b/2026-01-01T00-00-00_x.jsonl"); got != "2026-01-01T00-00-00_x" {
		t.Errorf("FileName = %q", got)
	}
	if got := NonEmpty("", "  b ", "c"); got != "b" {
		t.Errorf("NonEmpty = %q", got)
	}
	a := Sha1Hex([]any{1, "x", int64(2)})
	b := Sha1Hex([]any{1, "x", int64(2)})
	if a == "" || a != b {
		t.Errorf("Sha1Hex not stable: %q vs %q", a, b)
	}
	if Sha1Hex([]any{2, "x", int64(2)}) == a {
		t.Error("Sha1Hex collided on different input")
	}
}

func TestRootsExistSplitsPaths(t *testing.T) {
	dir := t.TempDir()
	found, missing := RootsExist([]string{dir, filepath.Join(dir, "nope")})
	if len(found) != 1 || found[0] != dir {
		t.Errorf("found = %v", found)
	}
	if len(missing) != 1 {
		t.Errorf("missing = %v", missing)
	}
}
