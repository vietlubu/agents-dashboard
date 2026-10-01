package harness

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

const codexParserVersion = 1

// codexMarkers prefilter Codex rollout lines. A rollout contains far more response_item
// lines (including encrypted reasoning blobs of hundreds of kilobytes) than it contains
// usage records, so nothing is JSON-decoded unless a marker matched first.
var codexMarkers = [][]byte{[]byte(`"token_count"`), []byte(`"thread_settings"`)}

type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

// codexLine reads only the fields needed for usage accounting. session_meta is never
// parsed: it embeds base_instructions (hundreds of kilobytes of prompt text) and would
// defeat the marker prefilter. Session identity comes from state_5.sqlite or the
// filename instead.
type codexLine struct {
	Ordinal   int64  `json:"ordinal"`
	Timestamp string `json:"timestamp"`
	Payload   *struct {
		Type           string `json:"type"`
		Model          string `json:"model"`
		ThreadSettings *struct {
			Model string `json:"model"`
		} `json:"thread_settings"`
		Info *struct {
			LastTokenUsage  *codexUsage `json:"last_token_usage"`
			TotalTokenUsage *codexUsage `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type codexMeta struct {
	SessionID string
	Project   string
	Model     string
	AgentType string
}

type codexAdapter struct {
	home string
}

func newCodexAdapter(home string) *codexAdapter {
	return &codexAdapter{home: home}
}

func (a *codexAdapter) ID() string           { return "codex" }
func (a *codexAdapter) DisplayName() string  { return "Codex" }
func (a *codexAdapter) ParserVersion() int64 { return codexParserVersion }

// Roots is deliberately limited to the two session directories. ~/.codex itself must
// never be walked: it holds a .tmp directory with tens of thousands of transitory
// entries and multi-hundred-megabyte log databases.
func (a *codexAdapter) Roots(_ context.Context, extra []string) []string {
	roots := []string{
		filepath.Join(a.home, ".codex", "sessions"),
		filepath.Join(a.home, ".codex", "archived_sessions"),
	}
	return append(roots, extra...)
}

func (a *codexAdapter) Available(roots []string) bool { return anyDirExists(roots) }

// stateDB is the Codex session index, which carries the cwd, model and thread kind that
// the rollout records cannot supply without parsing session_meta.
func (a *codexAdapter) stateDB() string {
	return filepath.Join(a.home, ".codex", "state_5.sqlite")
}

// loadMeta reads the thread index. A missing or unreadable index is not an error: the
// rollout files still carry every token_count record, so the scan degrades to
// filename-derived session ids and an unknown project.
func (a *codexAdapter) loadMeta() map[string]codexMeta {
	out := map[string]codexMeta{}
	path := a.stateDB()
	if !fileExists(path) {
		return out
	}
	db, err := OpenReadOnly(XDBSpec{Path: path})
	if err != nil {
		return out
	}
	defer db.Close()
	if !TableExists(db, "threads") {
		return out
	}

	rows, err := db.Query(
		`SELECT id, rollout_path, COALESCE(cwd,''), COALESCE(model,''), COALESCE(thread_source,'')
		 FROM threads`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, rolloutPath, cwd, model, threadSource string
		if err := rows.Scan(&id, &rolloutPath, &cwd, &model, &threadSource); err != nil {
			return out
		}
		if rolloutPath == "" {
			continue
		}
		agentType := "main"
		if threadSource == "subagent" {
			agentType = "subagent"
		}
		out[filepath.Clean(rolloutPath)] = codexMeta{
			SessionID: id,
			Project:   cwd,
			Model:     model,
			AgentType: agentType,
		}
	}
	return out
}

func (a *codexAdapter) Scan(ctx context.Context, in ScanInput) error {
	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
	dedup := NewEventDedup()
	var scanner FileScanner
	meta := a.loadMeta()

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

		m := meta[filepath.Clean(f.Path)]
		if m.SessionID == "" {
			m.SessionID = codexSessionFromFilename(f.Path)
		}
		if m.AgentType == "" {
			m.AgentType = "main"
		}
		var minTS, maxTS int64

		res, err := scanner.Scan(ctx, f.Path, d.Offset, codexMarkers, func(line []byte) error {
			var l codexLine
			if err := json.Unmarshal(line, &l); err != nil {
				return nil
			}
			if l.Payload == nil {
				return nil
			}
			if l.Payload.Type == "thread_settings_applied" {
				if m.Model == "" {
					if l.Payload.ThreadSettings != nil {
						m.Model = NonEmpty(l.Payload.ThreadSettings.Model, l.Payload.Model)
					} else {
						m.Model = l.Payload.Model
					}
				}
				return nil
			}
			if l.Payload.Type != "token_count" || l.Payload.Info == nil {
				return nil
			}
			info := l.Payload.Info
			last := info.LastTokenUsage
			if last == nil {
				return nil
			}

			input := ClampTokens(last.InputTokens - last.CachedInputTokens)
			if input < 0 {
				input = 0
			}
			output := ClampTokens(last.OutputTokens)
			cacheRead := ClampTokens(last.CachedInputTokens)
			cacheWrite := ClampTokens(last.CacheWriteInputTokens)
			reasoning := ClampTokens(last.ReasoningOutputTokens)
			total := Total(input, output, cacheRead, cacheWrite)

			if total == 0 {
				// Some turns report every bucket as zero while still carrying a total.
				// Trust that total only when the event itself says it is total-only, or
				// when the last-usage total equals the whole cumulative total.
				if last.TotalTokens > 0 && codexTrustTotalOnly(last.TotalTokens, info.TotalTokenUsage) {
					input = ClampTokens(last.TotalTokens)
					output, cacheRead, cacheWrite, reasoning = 0, 0, 0, 0
					total = input
				} else {
					// A genuinely empty record (for example immediately after an aborted
					// turn) carries no usage and must not create a zero row.
					return nil
				}
			}

			ts, ok := ParseISO(l.Timestamp)
			if !ok {
				return nil
			}
			if minTS == 0 || ts < minTS {
				minTS = ts
			}
			if ts > maxTS {
				maxTS = ts
			}

			// The key is a content fingerprint, not the ordinal: ordinals restart per file
			// (1,067 of 2,237 local values are reused across files), while a fork replays
			// the parent's records verbatim. Fingerprinting collapses replays and cannot
			// collide across distinct turns without also colliding on the timestamp.
			key := "codex|" + Sha1Hex([]any{
				l.Ordinal, l.Timestamp,
				cumField(info.TotalTokenUsage, func(u *codexUsage) int64 { return u.InputTokens }),
				cumField(info.TotalTokenUsage, func(u *codexUsage) int64 { return u.CachedInputTokens }),
				cumField(info.TotalTokenUsage, func(u *codexUsage) int64 { return u.CacheWriteInputTokens }),
				cumField(info.TotalTokenUsage, func(u *codexUsage) int64 { return u.OutputTokens }),
				cumField(info.TotalTokenUsage, func(u *codexUsage) int64 { return u.ReasoningOutputTokens }),
				cumField(info.TotalTokenUsage, func(u *codexUsage) int64 { return u.TotalTokens }),
				last.InputTokens, last.CachedInputTokens, last.CacheWriteInputTokens,
				last.OutputTokens, last.ReasoningOutputTokens, last.TotalTokens,
			})

			dedup.Add(Event{
				EventKey:   key,
				SourceFile: f.Path,
				SessionID:  m.SessionID,
				Project:    m.Project,
				Model:      NonEmpty(m.Model, "codex"),
				AgentType:  m.AgentType,
				Outcome:    "unknown", // token_count carries no outcome
				TS:         ts,
				Input:      input,
				Output:     output,
				CacheRead:  cacheRead,
				CacheWrite: cacheWrite,
				Reasoning:  reasoning,
				Total:      total,
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
			SessionID:  m.SessionID,
			Project:    m.Project,
			Model:      m.Model,
			AgentType:  m.AgentType,
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

// codexTrustTotalOnly decides whether a zero-component record should contribute its
// total. With no cumulative vector the total is the only signal; an equally
// component-empty cumulative vector means the session is total-only; an equal total
// means this turn accounts for the whole session. Anything else is the post-replay
// zero-context shape of a fork and must stay at zero.
func codexTrustTotalOnly(lastTotal int64, cumulative *codexUsage) bool {
	if cumulative == nil {
		return true
	}
	billable := cumulative.InputTokens + cumulative.CachedInputTokens +
		cumulative.CacheWriteInputTokens + cumulative.OutputTokens
	if billable == 0 && cumulative.TotalTokens > 0 {
		return true
	}
	return cumulative.TotalTokens == lastTotal
}

func cumField(u *codexUsage, pick func(*codexUsage) int64) int64 {
	if u == nil {
		return -1 // distinguishes "absent" from a real zero
	}
	return pick(u)
}

// codexSessionFromFilename recovers the session id from
// rollout-<ISO timestamp>-<uuid>.jsonl when the thread index has no row for the file.
func codexSessionFromFilename(path string) string {
	name := FileName(path)
	if len(name) >= 36 {
		id := name[len(name)-36:]
		if strings.Count(id, "-") == 4 {
			return id
		}
	}
	return name
}
