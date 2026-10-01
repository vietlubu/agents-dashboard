package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vietlubu/agent-dashboard/internal/config"
)

const piParserVersion = 1

var piMarker = [][]byte{[]byte(`"usage"`)}

// piUsage is shared by Pi and omp: both are camelCase variants of the same shape.
type piUsage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	Reasoning   int64 `json:"reasoning"`
	TotalTokens int64 `json:"totalTokens"`
	Cost        *struct {
		Total *float64 `json:"total"`
	} `json:"cost"`
}

// piLine is the whitelist of fields read from a Pi session record. Message content is
// intentionally absent.
type piLine struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	// Forks write the model at the envelope level while vanilla Pi nests it in the
	// message, so both are read.
	Model   string   `json:"model"`
	Usage   *piUsage `json:"usage"`
	Message *struct {
		Role       string   `json:"role"`
		Model      string   `json:"model"`
		StopReason string   `json:"stopReason"`
		Timestamp  int64    `json:"timestamp"`
		Usage      *piUsage `json:"usage"`
	} `json:"message"`
}

// piHeader is the first record of a session file, which carries the session id and the
// working directory the session ran in.
type piHeader struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

type piAdapter struct {
	home string
}

func newPiAdapter(home string) *piAdapter {
	return &piAdapter{home: home}
}

func (a *piAdapter) ID() string           { return "pi" }
func (a *piAdapter) DisplayName() string  { return "Pi" }
func (a *piAdapter) ParserVersion() int64 { return piParserVersion }

func (a *piAdapter) Roots(_ context.Context, extra []string) []string {
	roots := []string{filepath.Join(a.home, ".pi", "agent", "sessions")}
	for _, dir := range config.CommaPaths(os.Getenv("PI_CODING_AGENT_DIR")) {
		roots = append(roots, filepath.Join(dir, "sessions"))
	}
	roots = append(roots, config.CommaPaths(os.Getenv("PI_CODING_AGENT_SESSION_DIR"))...)
	return append(roots, extra...)
}

func (a *piAdapter) Available(roots []string) bool { return anyDirExists(roots) }

func (a *piAdapter) Scan(ctx context.Context, in ScanInput) error {
	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
	dedup := NewEventDedup()
	var scanner FileScanner

	for _, f := range walkJSONL(in.Roots) {
		if err := ctx.Err(); err != nil {
			return err
		}
		snk.noteWalked()

		st, seen := in.States[fileKey(f.Path)]
		d := decideFile(f, st, seen, in.Full)
		if d.Skip {
			continue
		}
		if d.Replaced {
			snk.addDelete(f.Path)
		}

		// The session header is the file's first line: read it only when the session is
		// not already known, so a repeat scan of a large session costs nothing extra.
		var session piHeader
		if d.Offset == 0 {
			if raw, err := firstLine(f.Path, 8<<10); err == nil {
				_ = json.Unmarshal(raw, &session)
			}
		}
		sessionID := session.ID
		if sessionID == "" {
			sessionID = piSessionFromFilename(f.Path)
		}

		var minTS, maxTS int64
		res, err := scanner.Scan(ctx, f.Path, d.Offset, piMarker, func(line []byte) error {
			var l piLine
			if err := json.Unmarshal(line, &l); err != nil {
				return nil
			}
			usage, model, stopReason, ts := piRecordFields(&l)
			if usage == nil {
				return nil
			}
			if ts == 0 {
				return nil
			}
			input := ClampTokens(usage.Input)
			output := ClampTokens(usage.Output)
			cacheRead := ClampTokens(usage.CacheRead)
			cacheWrite := ClampTokens(usage.CacheWrite)
			reasoning := ClampTokens(usage.Reasoning)
			total := Total(input, output, cacheRead, cacheWrite)
			if total == 0 {
				if usage.TotalTokens <= 0 {
					return nil
				}
				// Total-only usage cannot be split into buckets; keep the aggregate so the
				// usage is not silently dropped.
				input = ClampTokens(usage.TotalTokens)
				output, cacheRead, cacheWrite, reasoning = 0, 0, 0, 0
				total = input
			}
			if minTS == 0 || ts < minTS {
				minTS = ts
			}
			if ts > maxTS {
				maxTS = ts
			}

			var cost *float64
			if usage.Cost != nil && usage.Cost.Total != nil {
				cost = usage.Cost.Total
			}

			dedup.Add(Event{
				EventKey:   "pi|" + sessionID + "|" + l.ID,
				SourceFile: f.Path,
				SessionID:  sessionID,
				Project:    session.Cwd,
				Model:      NonEmpty(model, "pi"),
				AgentType:  "main",
				Outcome:    Outcome(stopReason),
				TS:         ts,
				Input:      input,
				Output:     output,
				CacheRead:  cacheRead,
				CacheWrite: cacheWrite,
				Reasoning:  reasoning,
				Total:      total,
				CostUSD:    cost,
			})
			return nil
		})
		if err != nil {
			return err
		}
		for _, e := range dedup.Events() {
			snk.addEvent(e)
		}
		dedup.Reset()

		snk.addSession(SessionInfo{
			SessionID:  sessionID,
			Project:    session.Cwd,
			AgentType:  "main",
			SourceFile: f.Path,
			StartedAt:  minTS,
			UpdatedAt:  maxTS,
		})
		snk.addState(ScanStateUpdate{
			Key:    fileKey(f.Path),
			Kind:   "file",
			Mtime:  res.Mtime,
			Size:   res.Size,
			Inode:  res.Inode,
			Offset: res.NewOffset,
		})
		if res.NewOffset != d.Offset {
			snk.noteChanged()
		}
		if err := snk.flush(); err != nil {
			return err
		}
	}
	return snk.finish()
}

// piRecordFields extracts the usage and identity of one Pi record. Message records nest
// usage under message; compaction and branch_summary records carry it at the top level.
// Aborted and failed messages are skipped because their usage describes a request that
// did not complete.
func piRecordFields(l *piLine) (usage *piUsage, model, stopReason string, ts int64) {
	switch l.Type {
	case "message":
		if l.Message == nil || l.Message.Role != "assistant" || l.Message.Usage == nil {
			return nil, "", "", 0
		}
		switch l.Message.StopReason {
		case "aborted", "error":
			return nil, "", "", 0
		}
		model = NonEmpty(l.Model, l.Message.Model)
		stopReason = l.Message.StopReason
		ts, _ = ParseTimeMS(l.Message.Timestamp, l.Timestamp)
		return l.Message.Usage, model, stopReason, ts
	case "compaction", "branch_summary":
		if l.Usage == nil {
			return nil, "", "", 0
		}
		ts, _ = ParseTimeMS(0, l.Timestamp)
		return l.Usage, "", "", ts
	default:
		return nil, "", "", 0
	}
}

// piSessionFromFilename recovers the session id from <ISO>_<uuid>.jsonl.
func piSessionFromFilename(path string) string {
	name := FileName(path)
	if i := indexByteFromEnd(name, '_'); i >= 0 && i+1 < len(name) {
		return name[i+1:]
	}
	return name
}

func indexByteFromEnd(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}
