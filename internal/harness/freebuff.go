package harness

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
)

// freebuffParserVersion is bumped when the Freebuff parse rules change. The sync engine
// then clears this harness's stored offsets and events, because old offsets no longer
// describe what the parser reads.
const freebuffParserVersion = 1

// freebuffMarker prefilters log.jsonl. Freebuff writes one "Start agent … step N" record
// and one "End agent … step N" record per agent step, and both carry data.iteration.
// Everything else in the file is runtime noise (ads, auth, health checks, queue
// bookkeeping) that carries no usage.
var freebuffMarker = [][]byte{[]byte(`"iteration"`)}

// freebuffStep is the whitelist of fields this adapter reads from one step record.
//
// The same log line also carries data.prompt, data.fullResponse and data.toolResults,
// which hold prompt text, model output and whole file contents. None of them are declared
// here, so encoding/json skips their values without buffering: this adapter never reads
// or stores prompt or response text, matching the package privacy contract. The record's
// msg string is deliberately not decoded either, because Freebuff embeds a truncated
// prompt in it ("… (run - Prompt: …)"); Start and End are told apart by which fields are
// present.
type freebuffStep struct {
	Iteration *int64 `json:"iteration"`
	RunID     string `json:"runId"`
	// ContextTokenCount is context occupancy, not billed usage, and only the Start
	// record reports it. The adapter stores the per-step increase, never the occupancy
	// itself, so summing events does not multiply the session's context by its step count.
	ContextTokenCount *int64 `json:"contextTokenCount"`
	SystemTokens      *int64 `json:"systemTokens"`
	// DurationMs is the step's wall time; only the End record reports a meaningful value
	// (the Start record writes ~1ms).
	DurationMs *int64 `json:"duration"`
	// StepCreditsUsed and ShouldEndTurn only appear on the End record.
	StepCreditsUsed *float64 `json:"stepCreditsUsed"`
	ShouldEndTurn   *bool    `json:"shouldEndTurn"`
}

type freebuffLine struct {
	Timestamp string        `json:"timestamp"`
	Data      *freebuffStep `json:"data"`
}

// freebuffAdapter reads the Freebuff CLI's project tree. Freebuff meters in Freebucks and
// context occupancy rather than billed USD or input/output token buckets, so its events
// carry the per-step context increase as their token total and never a cost: the sync
// engine then records the cost as unavailable rather than inventing one.
type freebuffAdapter struct {
	home string
}

func newFreebuffAdapter(home string) *freebuffAdapter {
	return &freebuffAdapter{home: home}
}

func (a *freebuffAdapter) ID() string           { return "freebuff" }
func (a *freebuffAdapter) DisplayName() string  { return "Freebuff" }
func (a *freebuffAdapter) ParserVersion() int64 { return freebuffParserVersion }

// Roots returns Freebuff's project tree. The launcher keeps its state under
// <home>/.config/manicode regardless of platform — not ~/.freebuff — with one directory
// per project holding that project's chats.
func (a *freebuffAdapter) Roots(_ context.Context, extra []string) []string {
	roots := []string{filepath.Join(a.home, ".config", "manicode", "projects")}
	return append(roots, extra...)
}

func (a *freebuffAdapter) Available(roots []string) bool { return anyDirExists(roots) }

func (a *freebuffAdapter) Scan(ctx context.Context, in ScanInput) error {
	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
	var scanner FileScanner

	// Roots are walked one at a time because each file's project and chat come from its
	// path relative to the root it was found under.
	for _, root := range in.Roots {
		if err := a.scanRoot(ctx, in, snk, &scanner, root); err != nil {
			return err
		}
	}
	return snk.finish()
}

func (a *freebuffAdapter) scanRoot(ctx context.Context, in ScanInput, snk *sink, scanner *FileScanner, root string) error {
	for _, f := range walkJSONL([]string{root}) {
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

		project, chat := freebuffIdentity(root, f.Path)

		// prev is the context occupancy of the last step seen in this file. Seeding it
		// from the stored watermark is what keeps a resumed scan from re-counting the
		// whole context as new tokens on its first step.
		prev := st.Watermark
		if d.Replaced || in.Full {
			prev = 0
		}
		prevRun := ""
		var sessionID string
		var minTS, maxTS int64

		steps := map[string]*Event{}
		var order []string

		res, err := scanner.Scan(ctx, f.Path, d.Offset, freebuffMarker, func(line []byte) error {
			var l freebuffLine
			if err := json.Unmarshal(line, &l); err != nil {
				return nil // a non-JSON or foreign line is not an error
			}
			if l.Data == nil || l.Data.Iteration == nil {
				return nil
			}
			ts, ok := ParseISO(l.Timestamp)
			if !ok {
				return nil
			}
			runID := NonEmpty(l.Data.RunID, chat)
			key := "freebuff|" + runID + "|" + strconv.FormatInt(*l.Data.Iteration, 10)

			if l.Data.ContextTokenCount != nil {
				// Start record: the step's context occupancy. A new run inside the same
				// chat directory restarts the context, so the baseline resets with it.
				if prevRun != "" && runID != prevRun {
					prev = 0
				}
				prevRun = runID
				ctxTokens := ClampTokens(*l.Data.ContextTokenCount)
				increase := ctxTokens - prev
				if increase < 0 {
					increase = 0
				}
				prev = ctxTokens

				if sessionID == "" {
					sessionID = runID
				}
				if minTS == 0 || ts < minTS {
					minTS = ts
				}
				if ts > maxTS {
					maxTS = ts
				}
				if _, dup := steps[key]; !dup {
					order = append(order, key)
				}
				steps[key] = &Event{
					EventKey:   key,
					SourceFile: f.Path,
					SessionID:  runID,
					Project:    project,
					// The record's model field is an opaque per-request "fbm1.…" id, so
					// every step collapses to one label; a price catalog can never match
					// it, which is exactly the unavailable-cost outcome this harness wants.
					Model:     "freebuff",
					AgentType: "main",
					Outcome:   "unknown",
					TS:        ts,
					Input:     increase,
					Total:     increase,
				}
				return nil
			}

			if l.Data.StepCreditsUsed != nil {
				// End record: it adds the step's wall time and whether the turn ended to
				// the event its Start record created. When the Start was consumed by an
				// earlier scan the tokens are already stored, so the record is dropped
				// rather than re-emitted.
				ev, ok := steps[key]
				if !ok {
					return nil
				}
				if l.Data.DurationMs != nil && *l.Data.DurationMs > 0 {
					ms := *l.Data.DurationMs
					ev.LatencyMs = &ms
				}
				if l.Data.ShouldEndTurn != nil {
					if *l.Data.ShouldEndTurn {
						ev.Outcome = "ok"
					} else {
						ev.Outcome = "tool_use"
					}
				}
				return nil
			}
			return nil
		})
		if err != nil {
			return err
		}

		for _, key := range order {
			snk.addEvent(*steps[key])
		}
		snk.addSession(SessionInfo{
			SessionID:  NonEmpty(sessionID, chat),
			Project:    project,
			Model:      "freebuff",
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
			// The file cursor's watermark carries the last context occupancy seen, which
			// is the baseline the next incremental pass needs to keep its first step's
			// increase correct. The file adapters otherwise leave it unused.
			Watermark: prev,
		})
		if res.NewOffset != d.Offset {
			snk.noteChanged()
		}
		if err := snk.flush(); err != nil {
			return err
		}
	}
	return nil
}

// freebuffIdentity maps a log.jsonl path to its project and chat directory. The default
// layout is <root>/<project>/chats/<chat timestamp>/log.jsonl; a user-added root pointing
// deeper degrades to the first path component rather than failing the scan.
func freebuffIdentity(root, path string) (project, chat string) {
	chat = BaseName(DirName(path))
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", chat
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 3 && parts[1] == "chats" {
		return parts[0], chat
	}
	if len(parts) >= 2 {
		return parts[0], chat
	}
	return "", chat
}
