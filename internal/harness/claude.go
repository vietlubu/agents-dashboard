package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

// claudeParserVersion is bumped when the Claude parse rules change. The sync engine then
// clears this harness's stored offsets and events, because old offsets no longer
// describe what the parser reads.
const claudeParserVersion = 1

// claudeMarker prefilters lines: an assistant usage record is the only line shape this
// adapter consumes, and there are ~10x more tool/attachment/metadata lines than that.
var claudeMarker = [][]byte{[]byte(`"usage"`), []byte(`"assistant"`)}

// claudeLine is the whitelist of fields this adapter reads. Prompt and response content
// is intentionally absent: it is never decoded and never stored.
type claudeLine struct {
	Type        string `json:"type"`
	RequestID   string `json:"requestId"`
	Timestamp   string `json:"timestamp"`
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Usage      *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type claudeAdapter struct {
	home string
}

func newClaudeAdapter(home string) *claudeAdapter {
	return &claudeAdapter{home: home}
}

func (a *claudeAdapter) ID() string           { return "claude" }
func (a *claudeAdapter) DisplayName() string  { return "Claude Code" }
func (a *claudeAdapter) ParserVersion() int64 { return claudeParserVersion }

// Roots returns CLAUDE_CONFIG_DIR-derived projects directories (when the CLI was moved
// with that variable) plus the default ~/.claude/projects, plus user-added roots.
func (a *claudeAdapter) Roots(_ context.Context, extra []string) []string {
	var roots []string
	for _, dir := range config.CommaPaths(os.Getenv("CLAUDE_CONFIG_DIR")) {
		roots = append(roots, filepath.Join(dir, "projects"))
	}
	roots = append(roots, filepath.Join(a.home, ".claude", "projects"))
	return append(roots, extra...)
}

func (a *claudeAdapter) Available(roots []string) bool { return anyDirExists(roots) }

func (a *claudeAdapter) Scan(ctx context.Context, in ScanInput) error {
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

		sessionID, agentType, agentName, projectDir := claudeSessionIdentity(f.Path)
		var minTS, maxTS int64
		var cwd, sessionModel string

		res, err := scanner.Scan(ctx, f.Path, d.Offset, claudeMarker, func(line []byte) error {
			var l claudeLine
			if err := json.Unmarshal(line, &l); err != nil {
				return nil // a non-JSON or foreign line is not an error
			}
			if l.Type != "assistant" || l.Message.Usage == nil {
				return nil
			}
			u := l.Message.Usage
			input := ClampTokens(u.InputTokens)
			output := ClampTokens(u.OutputTokens)
			cacheWrite := ClampTokens(u.CacheCreationInputTokens)
			cacheRead := ClampTokens(u.CacheReadInputTokens)
			total := Total(input, output, cacheRead, cacheWrite)
			if total == 0 {
				// Claude writes <synthetic> assistant records with all-zero usage (session
				// bootstrap, local wrappers). They are metadata, not usage.
				return nil
			}
			ts, ok := ParseISO(l.Timestamp)
			if !ok {
				return nil
			}
			if cwd == "" && l.Cwd != "" {
				cwd = l.Cwd
			}
			if sessionModel == "" {
				sessionModel = l.Message.Model
			}
			if minTS == 0 || ts < minTS {
				minTS = ts
			}
			if ts > maxTS {
				maxTS = ts
			}
			dedup.Add(Event{
				EventKey:   "claude|" + l.Message.ID + "|" + l.RequestID,
				SourceFile: f.Path,
				SessionID:  sessionID,
				Project:    cwd,
				Model:      NonEmpty(l.Message.Model, "unknown"),
				AgentType:  agentType,
				AgentName:  agentName,
				Outcome:    Outcome(l.Message.StopReason),
				TS:         ts,
				Input:      input,
				Output:     output,
				CacheRead:  cacheRead,
				CacheWrite: cacheWrite,
				Reasoning:  0, // Claude reports no separate reasoning bucket
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

		project := cwd
		if project == "" {
			project = projectDir
		}
		snk.addSession(SessionInfo{
			SessionID:  sessionID,
			Project:    project,
			Model:      sessionModel,
			AgentType:  agentType,
			AgentName:  agentName,
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

// claudeSessionIdentity maps a transcript path to its session.
//
// A subagent transcript lives at <project>/<session>/subagents/agent-<hash>.jsonl and is
// attributed to the parent session, so its tokens count once under the session that ran
// the work rather than under a synthetic session of its own.
func claudeSessionIdentity(path string) (sessionID, agentType, agentName, projectDir string) {
	dir := DirName(path)
	stem := FileName(path)
	if BaseName(dir) == "subagents" {
		sessionDir := DirName(dir)
		return BaseName(sessionDir), "subagent", stem, BaseName(DirName(sessionDir))
	}
	return stem, "main", "", BaseName(dir)
}
