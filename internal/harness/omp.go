package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/vietlubu/agents-dashboard/internal/config"
)

const ompParserVersion = 1

var ompMarker = [][]byte{[]byte(`"usage"`)}

// ompAdapter reads omp usage from stats.db, which records one row per API call with
// timing and cost, and from the session JSONL, which is the only source still being
// written (the database has not been updated since 2026-09-28 on this machine). The two
// share a key space, so the JSONL supplements the database rather than duplicating it.
type ompAdapter struct {
	home string
}

func newOmpAdapter(home string) *ompAdapter {
	return &ompAdapter{home: home}
}

func (a *ompAdapter) ID() string           { return "omp" }
func (a *ompAdapter) DisplayName() string  { return "omp" }
func (a *ompAdapter) ParserVersion() int64 { return ompParserVersion }

// Roots returns the omp home (which holds stats.db and agent/sessions) plus any
// OMP_CODING_AGENT_DIR override. When the override points at the agent home, its parent
// also holds stats.db, so both are considered.
func (a *ompAdapter) Roots(_ context.Context, extra []string) []string {
	roots := []string{filepath.Join(a.home, ".omp")}
	for _, dir := range config.CommaPaths(os.Getenv("OMP_CODING_AGENT_DIR")) {
		roots = append(roots, dir)
	}
	return append(roots, extra...)
}

func (a *ompAdapter) Available(roots []string) bool {
	if a.resolveStatsDB(roots) != "" {
		return true
	}
	for _, root := range roots {
		if dirExists(filepath.Join(root, "agent", "sessions")) {
			return true
		}
	}
	return false
}

// resolveStatsDB probes each root for stats.db, including one level up so that an
// OMP_CODING_AGENT_DIR pointing at the agent home still finds it.
func (a *ompAdapter) resolveStatsDB(roots []string) string {
	for _, root := range roots {
		for _, candidate := range []string{
			filepath.Join(root, "stats.db"),
			filepath.Join(filepath.Dir(root), "stats.db"),
		} {
			if fileExists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func (a *ompAdapter) Scan(ctx context.Context, in ScanInput) error {
	// stats.db and the session JSONL share one key space (omp|<session file>|<entry id>,
	// verified against real rows), so both sources are read: the database contributes the
	// per-call latency and ttft that the JSONL does not record, and the JSONL keeps the
	// data current when the database goes stale, which it did here — its last row is from
	// 2026-09-27 while sessions kept being written. stats.db runs first, because its
	// timings are attached by an upsert that only rewrites a row whose totals grew.
	if dbPath := a.resolveStatsDB(in.Roots); dbPath != "" {
		if err := a.scanStatsDB(ctx, in, dbPath); err != nil {
			return err
		}
	}
	return a.scanSessions(ctx, in)
}

func (a *ompAdapter) scanStatsDB(ctx context.Context, in ScanInput, dbPath string) error {
	db, err := OpenReadOnly(XDBSpec{Path: dbPath})
	if err != nil {
		return nil // locked or unreadable: the session JSONL still runs
	}
	defer db.Close()
	if !TableExists(db, "messages") {
		return nil // Scan reads the session JSONL as well, so there is nothing to fall back to
	}

	key := sqlKey(a.ID(), dbPath)
	st := in.States[key]
	watermark := st.Watermark
	if in.Full {
		watermark = 0
	}
	inode, mtime, size, sigErr := FileSignature(dbPath)
	replaced := sigErr == nil && st.Mtime != 0 && inode != 0 && st.Inode != 0 && inode != st.Inode

	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
	// Counted as one walked unit so the progress numbers stay comparable with the
	// file-based harnesses.
	snk.noteWalked()
	if replaced {
		snk.addDelete(dbPath)
		watermark = 0
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, session_file, entry_id, folder, model, provider, timestamp, duration, ttft,
		       stop_reason, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		       total_tokens, cost_total, agent_type
		FROM messages WHERE id > ? ORDER BY id`, watermark)
	if err != nil {
		if IsBusy(err) {
			return nil
		}
		return err
	}
	defer rows.Close()

	var maxRowID int64
	var scanned int
	projects := map[string]string{}
	sessions := map[string]*SessionInfo{}

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rowID int64
		var sessionFile, entryID, folder, model, provider, stopReason, agentType string
		var ts, input, output, cacheRead, cacheWrite, total int64
		var duration, ttft, costTotal sql.NullFloat64
		if err := rows.Scan(&rowID, &sessionFile, &entryID, &folder, &model, &provider, &ts,
			&duration, &ttft, &stopReason, &input, &output, &cacheRead, &cacheWrite,
			&total, &costTotal, &agentType); err != nil {
			return err
		}
		scanned++
		if rowID > maxRowID {
			maxRowID = rowID
		}
		if total == 0 {
			// 474 local rows carry no usage (interrupted or empty turns).
			continue
		}

		project := folder
		if resolved, ok := projects[sessionFile]; ok {
			project = resolved
		} else if cwd := a.sessionCwd(sessionFile); cwd != "" {
			projects[sessionFile] = cwd
			project = cwd
		} else {
			projects[sessionFile] = folder
		}

		var latency, ttftMs *int64
		if duration.Valid && duration.Float64 > 0 {
			ms := int64(duration.Float64)
			latency = &ms
		}
		if ttft.Valid && ttft.Float64 > 0 {
			ms := int64(ttft.Float64)
			ttftMs = &ms
		}
		var cost *float64
		if costTotal.Valid {
			v := costTotal.Float64
			cost = &v
		}

		name := "main"
		if agentType != "main" && agentType != "" {
			name = "subagent"
		}
		snk.addEvent(Event{
			EventKey:   "omp|" + sessionFile + "|" + entryID,
			SourceFile: dbPath,
			SessionID:  ompSessionID(sessionFile),
			Project:    project,
			Model:      NonEmpty(model, "omp"),
			Provider:   provider,
			AgentType:  name,
			AgentName:  ompAgentName(name, sessionFile),
			Outcome:    Outcome(stopReason),
			TS:         ParseEpochMs(ts),
			Input:      ClampTokens(input),
			Output:     ClampTokens(output),
			CacheRead:  ClampTokens(cacheRead),
			CacheWrite: ClampTokens(cacheWrite),
			Reasoning:  0, // omp has no reasoning column
			Total:      ClampTokens(total),
			CostUSD:    cost,
			LatencyMs:  latency,
			TTFTMs:     ttftMs,
		})
		sid := ompSessionID(sessionFile)
		if si, ok := sessions[sid]; ok {
			if ts > si.UpdatedAt {
				si.UpdatedAt = ParseEpochMs(ts)
			}
		} else {
			parsed := ParseEpochMs(ts)
			sessions[sid] = &SessionInfo{
				SessionID: sid, Project: project, Model: model, AgentType: name,
				AgentName: ompAgentName(name, sessionFile), SourceFile: sessionFile,
				StartedAt: parsed, UpdatedAt: parsed,
			}
		}

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

	for _, si := range sessions {
		snk.addSession(*si)
	}
	if maxRowID < watermark {
		maxRowID = watermark
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
		Watermark: maxRowID,
	})
	return snk.finish()
}

// sessionCwd reads the cwd from the first line of a session file so a session shows its
// real project instead of omp's home-relative folder slug. The slug encodes the path with
// every separator replaced by '-', which is ambiguous for hyphenated directory names.
func (a *ompAdapter) sessionCwd(sessionFile string) string {
	if sessionFile == "" {
		return ""
	}
	raw, err := firstLine(sessionFile, 8<<10)
	if err != nil {
		return ""
	}
	var header piHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return ""
	}
	return header.Cwd
}

// ompAgentName labels a session's agent. The database's agent_type column only says
// "subagent", so both sources use the subagent's own name instead, which is what the
// session id already holds.
func ompAgentName(agentType, sessionFile string) string {
	if agentType == "subagent" {
		return ompSessionID(sessionFile)
	}
	return "main"
}

// ompSessionID is the session id from <ISO>_<uuid>.jsonl.
func ompSessionID(sessionFile string) string {
	if sessionFile == "" {
		return ""
	}
	return piSessionFromFilename(sessionFile)
}

// scanSessions reads the session JSONL, which is the live half of omp's usage. Every
// field it can produce is derived the same way stats.db derives it, so the records the
// two sources share carry the same event key and the same session identity.
func (a *ompAdapter) scanSessions(ctx context.Context, in ScanInput) error {
	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
	dedup := NewEventDedup()
	var scanner FileScanner

	for _, root := range in.Roots {
		sessionsDir := filepath.Join(root, "agent", "sessions")
		if !dirExists(sessionsDir) {
			sessionsDir = filepath.Join(root, "sessions")
		}
		if !dirExists(sessionsDir) {
			continue
		}
		if err := a.scanSessionDir(ctx, in, snk, dedup, &scanner, sessionsDir); err != nil {
			return err
		}
	}
	return snk.finish()
}

func (a *ompAdapter) scanSessionDir(ctx context.Context, in ScanInput, snk *sink,
	dedup *EventDedup, scanner *FileScanner, sessionsDir string) error {
	for _, f := range walkJSONL([]string{sessionsDir}) {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Files under a bridge directory are conversion copies of other harnesses'
		// sessions; counting them would double-count the same tokens.
		if isOmpBridgeFile(f.Path) {
			continue
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

		// A session's own transcript sits directly in the project folder as
		// <ISO>_<uuid>.jsonl; a subagent's sits one level down, inside that session's
		// folder and named after the agent. Nested subagents repeat that pattern, so any
		// deeper path is a subagent too.
		sessionID := ompSessionID(f.Path)
		agentType, agentName := "main", "main"
		if sessionDepth(sessionsDir, f.Path) > 2 {
			agentType, agentName = "subagent", sessionID
		}
		// The session id comes from the file name, exactly as stats.db records it, so the
		// two sources cannot disagree about who a record belongs to. The project uses the
		// session header when it carries one and the folder slug otherwise, again matching
		// stats.db's resolution order.
		project := ""
		if d.Offset == 0 {
			if raw, err := firstLine(f.Path, 8<<10); err == nil {
				var header piHeader
				if json.Unmarshal(raw, &header) == nil {
					project = header.Cwd
				}
			}
		}
		if project == "" {
			project = sessionFolderSlug(sessionsDir, f.Path)
		}

		var minTS, maxTS int64
		res, err := scanner.Scan(ctx, f.Path, d.Offset, ompMarker, func(line []byte) error {
			var l piLine
			if err := json.Unmarshal(line, &l); err != nil {
				return nil
			}
			usage, model, stopReason, ts := ompRecordFields(&l)
			if usage == nil || ts == 0 {
				return nil
			}
			input := ClampTokens(usage.Input)
			output := ClampTokens(usage.Output)
			cacheRead := ClampTokens(usage.CacheRead)
			cacheWrite := ClampTokens(usage.CacheWrite)
			total := Total(input, output, cacheRead, cacheWrite)
			if total == 0 {
				if usage.TotalTokens <= 0 {
					return nil
				}
				input = ClampTokens(usage.TotalTokens)
				output, cacheRead, cacheWrite = 0, 0, 0
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
				EventKey:   "omp|" + f.Path + "|" + l.ID,
				SourceFile: f.Path,
				SessionID:  sessionID,
				Project:    project,
				Model:      NonEmpty(model, "omp"),
				AgentType:  agentType,
				AgentName:  agentName,
				Outcome:    Outcome(stopReason),
				TS:         ts,
				Input:      input,
				Output:     output,
				CacheRead:  cacheRead,
				CacheWrite: cacheWrite,
				Reasoning:  0,
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
			SessionID: sessionID, Project: project, AgentType: agentType, AgentName: agentName,
			SourceFile: f.Path, StartedAt: minTS, UpdatedAt: maxTS,
		})
		snk.addState(ScanStateUpdate{
			Key: fileKey(f.Path), Kind: "file",
			Mtime: res.Mtime, Size: res.Size, Inode: res.Inode, Offset: res.NewOffset,
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

// sessionDepth counts the path components of a session file below its sessions
// directory: 2 for a session transcript, more for a subagent's.
func sessionDepth(sessionsDir, path string) int {
	rel, err := filepath.Rel(sessionsDir, path)
	if err != nil {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

// sessionFolderSlug is the project folder a session file lives under: the
// home-relative path with every separator replaced by '-', the same value stats.db keeps
// in its folder column.
func sessionFolderSlug(sessionsDir, path string) string {
	rel, err := filepath.Rel(sessionsDir, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// isOmpBridgeFile reports whether a path sits under a bridge directory.
func isOmpBridgeFile(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "bridge" {
			return true
		}
	}
	return false
}

// ompRecordFields mirrors the Pi field extraction for omp's camelCase records, which
// carry no reasoning key.
func ompRecordFields(l *piLine) (usage *piUsage, model, stopReason string, ts int64) {
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
