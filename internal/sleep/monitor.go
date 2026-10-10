package sleep

import (
	"context"
	"strings"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/store"
)

// agentProcessNames are the coding agents that count as a running session. A match is on a
// lowercased path segment of a process argument, so a native binary (`codex`), an npm
// package directory (`claude-code`) and a Windows image name (`opencode.exe`) all count.
var agentProcessNames = map[string]struct{}{
	"claude":   {},
	"codex":    {},
	"opencode": {},
	"pi":       {},
	"omp":      {},
	"freebuff": {},
	"dsh":      {},
}

// matchedAgents finds the agent names inside a process list. Each entry may be a bare image
// name (Windows) or a full command line (Unix); every whitespace-separated, non-flag token
// is split into path segments, because a Node-hosted agent appears as
// `node .../node_modules/@anthropic-ai/claude-code/cli.js` where the executable is `node`.
// A segment matches when it equals an agent name (`codex`) or starts with one plus a dash
// (`claude-code`). The result is de-duplicated.
func matchedAgents(lines []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, line := range lines {
		for _, field := range strings.Fields(line) {
			name := agentNameInToken(field)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

// agentNameInToken returns the agent an argument names, or "" when it names none. Flags are
// ignored so `--claude-notes.md` is not mistaken for a running agent.
func agentNameInToken(field string) string {
	if strings.HasPrefix(field, "-") {
		return ""
	}
	field = strings.TrimSuffix(field, ".exe")
	for _, segment := range strings.FieldsFunc(field, func(r rune) bool { return r == '/' || r == '\\' }) {
		segment = strings.ToLower(segment)
		for name := range agentProcessNames {
			if segment == name || strings.HasPrefix(segment, name+"-") {
				return name
			}
		}
	}
	return ""
}

// Monitor is the default ActivityProvider. It combines the store's last-write timestamp
// (file/database activity, including the sync engine's own marker) with a process listing.
type Monitor struct {
	db          *store.DB
	lastChanged func() int64
	procs       ProcessLister
}

// NewMonitor builds the default activity provider. lastChanged may be nil.
func NewMonitor(db *store.DB, lastChanged func() int64, procs ProcessLister) *Monitor {
	return &Monitor{db: db, lastChanged: lastChanged, procs: procs}
}

// Observe reads both signals and decides whether a session is active: an agent process
// must be running AND it must have written within the window.
func (m *Monitor) Observe(ctx context.Context, window time.Duration) (Activity, error) {
	var out Activity

	last, err := m.db.LatestActivityMs(ctx)
	if err != nil {
		return out, err
	}
	out.LastWriteMs = last
	if m.lastChanged != nil {
		if changed := m.lastChanged(); changed > out.LastWriteMs {
			out.LastWriteMs = changed
		}
	}

	if m.procs != nil {
		running, err := m.procs.Running()
		if err != nil {
			// Process detection is advisory: a listing failure must not be fatal to the
			// scan-activity signal.
			running = nil
		}
		out.Processes = matchedAgents(running)
	}
	out.AgentRunning = len(out.Processes) > 0

	now := time.Now().UnixMilli()
	recent := out.LastWriteMs > 0 && now-out.LastWriteMs < window.Milliseconds()
	out.Active = out.AgentRunning && recent

	if n, err := m.db.ActiveSessionCount(ctx, now-window.Milliseconds()); err == nil {
		out.ActiveSessions = n
	}
	return out, nil
}
