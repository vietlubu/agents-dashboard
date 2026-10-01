package harness

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// maxParsedTokenValue clamps absurd token counts. Usage logs are written outside this
// process (hand edits, transport corruption, upstream bugs) and stay on disk, so a
// single bad value would otherwise be re-parsed on every scan. Clamping is a safe
// degradation where arithmetic overflow would be a crash.
const maxParsedTokenValue = 1_000_000_000_000_000

// ClampTokens maps a raw token count to a safe non-negative value.
func ClampTokens(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > maxParsedTokenValue {
		return maxParsedTokenValue
	}
	return v
}

// Total is the billable token count: reasoning is deliberately excluded because every
// harness reports it as a subset of the output already (or not at all).
func Total(input, output, cacheRead, cacheWrite int64) int64 {
	return ClampTokens(input) + ClampTokens(output) + ClampTokens(cacheRead) + ClampTokens(cacheWrite)
}

// Outcome normalizes a source-specific stop reason into a small, comparable set.
func Outcome(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "stop", "end_turn", "endturn", "completed", "complete", "length", "max_tokens":
		return "ok"
	case "tooluse", "tool_use", "tool_calls", "toolcalls", "function_call":
		return "tool_use"
	case "aborted", "cancelled", "canceled", "interrupted":
		return "aborted"
	case "error", "failed", "failure":
		return "error"
	case "":
		return "unknown"
	default:
		return "unknown"
	}
}

// ParseISO converts an RFC3339 timestamp string to epoch milliseconds. Both
// "2026-09-30T15:39:37.048Z" and a fractional-second-free variant are accepted.
func ParseISO(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999Z0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

// ParseEpochMs converts a numeric timestamp to milliseconds by inferring its unit from
// magnitude. Harnesses differ (Pi writes milliseconds, some envelopes write seconds), and
// a wrong guess moves a day's usage by decades, so the bands are explicit:
// >= 1e16 nanoseconds, >= 1e14 microseconds, >= 1e11 milliseconds, else seconds.
func ParseEpochMs(v int64) int64 {
	switch {
	case v <= 0:
		return 0
	case v >= 1e16:
		return v / 1e6
	case v >= 1e14:
		return v / 1e3
	case v >= 1e11:
		return v
	default:
		return v * 1000
	}
}

// ParseTimeMS resolves a timestamp that may be either a number (epoch, unit inferred)
// or a string (RFC3339).
func ParseTimeMS(number int64, text string) (int64, bool) {
	if number > 0 {
		return ParseEpochMs(number), true
	}
	return ParseISO(text)
}

// Sha1Hex is the fingerprint helper for dedupe keys built from a record's content.
func Sha1Hex(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

// NonEmpty returns the first non-empty, trimmed string.
func NonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// FileName returns the last path element without its extension.
func FileName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	if i := strings.LastIndexByte(path, '.'); i > 0 {
		return path[:i]
	}
	return path
}

// BaseName returns the last path element.
func BaseName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// DirName returns the parent path.
func DirName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return "/"
}
