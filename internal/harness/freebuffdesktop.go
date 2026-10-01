package harness

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
)

// freebuffDesktopParserVersion is bumped when the Freebuff Desktop parse rules change.
const freebuffDesktopParserVersion = 1

// freebuffDesktopMetrics is the whitelist of per-message accounting the Freebuff Desktop
// app stores in messages.metrics_json. The observed shape nests the call's usage under
// "usage" and the context window under "context"; a row whose shape this adapter does not
// recognize contributes a session but no event, never an error.
//
// The column also carries costUsd, which is deliberately not read: it is the same
// free/Freebucks ledger as the CLI and always zero, so taking it as a reported cost would
// render as "priced at $0" rather than "not metered in USD". The sibling parts_json and
// attachments_json columns hold the full prompt and response text and are never selected.
type freebuffDesktopMetrics struct {
	Usage *struct {
		// InputTokens includes the cached portion, and the app's own rows satisfy
		// totalTokens == inputTokens + outputTokens, so the billable input is the
		// uncached remainder.
		InputTokens           int64 `json:"inputTokens"`
		CachedInputTokens     int64 `json:"cachedInputTokens"`
		OutputTokens          int64 `json:"outputTokens"`
		ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
		TotalTokens           int64 `json:"totalTokens"`
	} `json:"usage"`
	// Context reports the context window occupancy, not billed usage; it is only used when
	// the row carries no usage ledger at all.
	Context *struct {
		UsedTokens int64 `json:"usedTokens"`
	} `json:"context"`
}

// freebuffDesktopThread is the session identity a Desktop thread contributes.
type freebuffDesktopThread struct {
	Project   string
	Model     string
	StartedAt int64
	UpdatedAt int64
}

// freebuffDesktopAdapter reads the Freebuff Desktop application's SQLite store, one
// desktop-v2.db per project directory. It shares the CLI harness's accounting stance: no
// USD cost is ever claimed, because the app meters in Freebucks rather than money.
type freebuffDesktopAdapter struct {
	home string
}

func newFreebuffDesktopAdapter(home string) *freebuffDesktopAdapter {
	return &freebuffDesktopAdapter{home: home}
}

func (a *freebuffDesktopAdapter) ID() string           { return "freebuff-desktop" }
func (a *freebuffDesktopAdapter) DisplayName() string  { return "Freebuff Desktop" }
func (a *freebuffDesktopAdapter) ParserVersion() int64 { return freebuffDesktopParserVersion }

func (a *freebuffDesktopAdapter) Roots(_ context.Context, extra []string) []string {
	roots := []string{filepath.Join(a.home, ".config", "freebuff-desktop", "projects")}
	return append(roots, extra...)
}

func (a *freebuffDesktopAdapter) Available(roots []string) bool {
	return len(a.resolveDBs(roots)) > 0
}

// resolveDBs finds every desktop-v2.db under the roots: either directly inside a root or
// one directory down, which is the app's per-project <slug>/desktop-v2.db layout.
func (a *freebuffDesktopAdapter) resolveDBs(roots []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, root := range roots {
		candidates := []string{filepath.Join(root, "desktop-v2.db")}
		if matches, err := filepath.Glob(filepath.Join(root, "*", "desktop-v2.db")); err == nil {
			candidates = append(candidates, matches...)
		}
		for _, candidate := range candidates {
			if !fileExists(candidate) {
				continue
			}
			if _, dup := seen[candidate]; dup {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out
}

func (a *freebuffDesktopAdapter) Scan(ctx context.Context, in ScanInput) error {
	for _, dbPath := range a.resolveDBs(in.Roots) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.scanDatabase(ctx, in, dbPath); err != nil {
			return err
		}
	}
	return nil
}

func (a *freebuffDesktopAdapter) scanDatabase(ctx context.Context, in ScanInput, dbPath string) error {
	db, err := OpenReadOnly(XDBSpec{Path: dbPath})
	if err != nil {
		return nil // locked or unreadable: retry on the next sync
	}
	defer db.Close()
	if !TableExists(db, "messages") {
		return nil
	}

	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
	// Counted as one walked unit so the progress numbers stay comparable with the
	// file-based harnesses.
	snk.noteWalked()

	key := sqlKey(a.ID(), dbPath)
	st := in.States[key]
	watermark := st.Watermark
	if in.Full {
		watermark = 0
	}

	inode, mtime, size, sigErr := FileSignature(dbPath)
	replaced := sigErr == nil && st.Mtime != 0 && inode != 0 && st.Inode != 0 && inode != st.Inode
	if replaced {
		snk.addDelete(dbPath)
		watermark = 0
	}

	rows, err := db.QueryContext(ctx, `
		SELECT m.seq, m.thread_id, COALESCE(m.request_id,''), COALESCE(m.role,''),
		       m.ts, COALESCE(m.metrics_json,'{}'),
		       COALESCE(t.project_path,''), COALESCE(t.model,''),
		       COALESCE(t.last_turn_outcome,''),
		       COALESCE(t.created_at,0), COALESCE(t.updated_at,0)
		FROM messages m LEFT JOIN threads t ON t.id = m.thread_id
		WHERE m.seq > ? ORDER BY m.seq`, watermark)
	if err != nil {
		if IsBusy(err) {
			return nil // keep the previous watermark and retry next sync
		}
		return err
	}
	defer rows.Close()

	var maxSeq int64
	scanned := 0
	threads := map[string]freebuffDesktopThread{}

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var seq, ts, created, updated int64
		var threadID, requestID, role, metricsJSON string
		var project, model, lastOutcome string
		if err := rows.Scan(&seq, &threadID, &requestID, &role, &ts, &metricsJSON,
			&project, &model, &lastOutcome, &created, &updated); err != nil {
			return err
		}
		scanned++
		if seq > maxSeq {
			maxSeq = seq
		}

		if info, ok := threads[threadID]; !ok {
			threads[threadID] = freebuffDesktopThread{
				Project:   project,
				Model:     model,
				StartedAt: ParseEpochMs(created),
				UpdatedAt: ParseEpochMs(updated),
			}
		} else {
			if info.UpdatedAt < ParseEpochMs(updated) {
				info.UpdatedAt = ParseEpochMs(updated)
			}
			if info.StartedAt == 0 {
				info.StartedAt = ParseEpochMs(created)
			}
			threads[threadID] = info
		}

		if role != "assistant" {
			continue
		}
		var m freebuffDesktopMetrics
		if err := json.Unmarshal([]byte(metricsJSON), &m); err != nil {
			continue
		}
		var input, output, cacheRead, cacheWrite, reasoning, total int64
		if u := m.Usage; u != nil {
			input = ClampTokens(u.InputTokens - u.CachedInputTokens)
			if input < 0 {
				input = 0
			}
			output = ClampTokens(u.OutputTokens)
			cacheRead = ClampTokens(u.CachedInputTokens)
			reasoning = ClampTokens(u.ReasoningOutputTokens)
			total = Total(input, output, cacheRead, cacheWrite)
			if total == 0 && u.TotalTokens > 0 {
				// A total-only ledger cannot be split into buckets; keep the aggregate so
				// the usage is not silently dropped.
				input = ClampTokens(u.TotalTokens)
				output, cacheRead, reasoning = 0, 0, 0
				total = input
			}
		}
		if total == 0 && m.Context != nil && m.Context.UsedTokens > 0 {
			// Last resort when the row records occupancy alone. This is context
			// occupancy, not billed usage, so it stays a single aggregate and is never
			// multiplied into the other buckets.
			input = ClampTokens(m.Context.UsedTokens)
			total = input
		}
		if total == 0 {
			continue
		}

		eventKey := NonEmpty(requestID, strconv.FormatInt(seq, 10))
		snk.addEvent(Event{
			EventKey:   "freebuff-desktop|" + threadID + "|" + eventKey,
			SourceFile: dbPath,
			SessionID:  threadID,
			Project:    project,
			Model:      NonEmpty(model, "freebuff"),
			AgentType:  "main",
			Outcome:    Outcome(lastOutcome),
			TS:         ParseEpochMs(ts),
			Input:      input,
			Output:     output,
			CacheRead:  cacheRead,
			CacheWrite: cacheWrite,
			Reasoning:  reasoning,
			Total:      total,
		})
		if snk.shouldFlush() {
			if err := snk.flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		if IsBusy(err) {
			return nil
		}
		return err
	}

	for threadID, info := range threads {
		snk.addSession(SessionInfo{
			SessionID:  threadID,
			Project:    info.Project,
			Model:      NonEmpty(info.Model, "freebuff"),
			AgentType:  "main",
			SourceFile: dbPath,
			StartedAt:  info.StartedAt,
			UpdatedAt:  info.UpdatedAt,
		})
	}

	// A pass that found no new rows must not regress the watermark to 0, or the next pass
	// would re-read the whole table.
	if maxSeq < watermark {
		maxSeq = watermark
	}
	if scanned > 0 {
		snk.noteChanged()
	}
	snk.addState(ScanStateUpdate{
		Key:       key,
		Kind:      "sqlite",
		Mtime:     mtime,
		Size:      size,
		Inode:     inode,
		Watermark: maxSeq,
	})
	return snk.finish()
}
