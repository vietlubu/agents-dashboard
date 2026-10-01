// Package pricing resolves the cost of a usage event.
//
// A cost is either reported by the harness itself (an exact amount the tool recorded) or
// estimated from a price table. The dashboard never invents a rate: with no price table
// loaded, every event stays "unavailable" and the UI shows a dash rather than a zero.
package pricing

import (
	"strings"
)

// Rate is USD per one million tokens, which is how both price sources publish rates.
type Rate struct {
	InputPerM      float64
	OutputPerM     float64
	CacheReadPerM  float64
	CacheWritePerM float64
}

// Rule scales or suppresses a model's rate. A disabled rule means "do not price this
// model", which is how a user silences a model that is billed outside this dashboard.
type Rule struct {
	InputMult      float64
	OutputMult     float64
	CacheReadMult  float64
	CacheWriteMult float64
	Disabled       bool
}

// ModelKey is the canonical key stored in the price table: lowercased, with a
// context-window suffix, a vendor prefix and a trailing build date removed. It is what the
// settings page shows and edits.
//
// It is computed directly rather than by taking the last of Candidates: candidate order is
// "most specific first" for lookups, which is the opposite of the canonical form.
func ModelKey(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '['); i > 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	return stripTrailingDate(s)
}

// Candidates lists the keys to try when looking a model up, most specific first.
//
// Harnesses disagree about model naming: Claude reports claude-opus-4-8 but its own cost
// ledger uses claude-opus-4-8[1m]; LiteLLM keys carry a vendor prefix and dated builds
// (anthropic/claude-sonnet-4-5-20250929); models.dev keys carry neither. Trying the
// variants in order lets one price table serve all of them.
func Candidates(model string) []string {
	base := strings.ToLower(strings.TrimSpace(model))
	if base == "" {
		return nil
	}

	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, existing := range out {
			if existing == s {
				return
			}
		}
		out = append(out, s)
	}

	add(base)

	// Strip a context-window suffix: claude-opus-4-8[1m] -> claude-opus-4-8.
	if i := strings.IndexByte(base, '['); i > 0 {
		suffixless := base[:i]
		add(suffixless)
		if j := strings.LastIndexByte(suffixless, '/'); j >= 0 && j+1 < len(suffixless) {
			add(suffixless[j+1:])
		}
	}

	// Strip a vendor prefix, repeatedly for nested paths like "models/openai/gpt-5.5".
	for pass := 0; pass < 3; pass++ {
		before := len(out)
		for _, s := range out {
			if i := strings.LastIndexByte(s, '/'); i >= 0 && i+1 < len(s) {
				add(s[i+1:])
			}
		}
		if len(out) == before {
			break
		}
	}

	// Strip a trailing build date so a dated snapshot matches its family rate.
	for _, s := range append([]string(nil), out...) {
		add(stripTrailingDate(s))
	}

	return out
}

// stripTrailingDate removes -YYYYMMDD or -YYYY-MM-DD from the end of a model id.
func stripTrailingDate(s string) string {
	if len(s) >= 8 {
		tail := s[len(s)-8:]
		if allDigits(tail) && s[len(s)-9] == '-' {
			return s[:len(s)-9]
		}
	}
	if len(s) >= 11 {
		tail := s[len(s)-10:]
		if tail[4] == '-' && tail[7] == '-' &&
			allDigits(tail[:4]) && allDigits(tail[5:7]) && allDigits(tail[8:]) &&
			s[len(s)-11] == '-' {
			return s[:len(s)-11]
		}
	}
	return s
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
