package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vietlubu/agent-dashboard/internal/config"
)

const opencodeParserVersion = 1

// ocMessage is the whitelist of fields read from OpenCode message JSON. Message content
// is intentionally absent.
type ocMessage struct {
	Role       string   `json:"role"`
	ModelID    string   `json:"modelID"`
	ProviderID string   `json:"providerID"`
	Cost       *float64 `json:"cost"`
	Agent      string   `json:"agent"`
	Tokens     struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

type opencodeAdapter struct {
	home string
}

func newOpencodeAdapter(home string) *opencodeAdapter {
	return &opencodeAdapter{home: home}
}

func (a *opencodeAdapter) ID() string           { return "opencode" }
func (a *opencodeAdapter) DisplayName() string  { return "OpenCode" }
func (a *opencodeAdapter) ParserVersion() int64 { return opencodeParserVersion }

func (a *opencodeAdapter) Roots(_ context.Context, extra []string) []string {
	roots := config.CommaPaths(os.Getenv("OPENCODE_DATA_DIR"))
	if len(roots) == 0 {
		roots = []string{filepath.Join(a.home, ".local", "share", "opencode")}
	}
	return append(roots, extra...)
}

func (a *opencodeAdapter) Available(roots []string) bool {
	return a.resolveDB(roots) != "" || anyDirExists(roots)
}

// resolveDB picks the first usable database. Newer OpenCode builds name the file
// opencode.db; channel builds append the channel, so both are probed.
func (a *opencodeAdapter) resolveDB(roots []string) string {
	for _, root := range roots {
		direct := filepath.Join(root, "opencode.db")
		if fileExists(direct) {
			return direct
		}
		matches, err := filepath.Glob(filepath.Join(root, "opencode-*.db"))
		if err != nil || len(matches) == 0 {
			continue
		}
		sort.Strings(matches)
		return matches[0]
	}
	return ""
}

func (a *opencodeAdapter) Scan(ctx context.Context, in ScanInput) error {
	if db := a.resolveDB(in.Roots); db != "" {
		return a.scanDatabase(ctx, in, db)
	}
	return a.scanLegacyStorage(ctx, in)
}

// scanDatabase reads the assistant messages one row at a time and resumes from a rowid
// watermark. The rowid is seekable (the primary key is a text id, so rowid is a separate
// ascending key), which means an incremental pass reads only the new rows instead of
// rescanning the whole table.
func (a *opencodeAdapter) scanDatabase(ctx context.Context, in ScanInput, dbPath string) error {
	db, err := OpenReadOnly(XDBSpec{Path: dbPath})
	if err != nil {
		return nil // a locked or unreadable database is retried on the next sync
	}
	defer db.Close()

	// Counted as one walked unit so the progress numbers stay comparable with the
	// file-based harnesses.
	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
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

	// The message payload holds the whole assistant response, which is hundreds of
	// kilobytes on some rows. Extracting only the accounting fields in SQL avoids reading
	// that text into the process at all; without JSON1 available, fall back to reading the
	// payload and decoding it.
	useJSON := supportsJSON(db)
	var query string
	if useJSON {
		query = `
		SELECT m.rowid, m.id, m.session_id, m.time_created,
		       json_extract(m.data,'$.role'), json_extract(m.data,'$.modelID'),
		       json_extract(m.data,'$.providerID'), json_extract(m.data,'$.cost'),
		       json_extract(m.data,'$.agent'),
		       json_extract(m.data,'$.tokens.input'), json_extract(m.data,'$.tokens.output'),
		       json_extract(m.data,'$.tokens.reasoning'),
		       json_extract(m.data,'$.tokens.cache.read'), json_extract(m.data,'$.tokens.cache.write'),
		       json_extract(m.data,'$.time.created'), json_extract(m.data,'$.time.completed'),
		       COALESCE(s.directory,''), COALESCE(s.parent_id,''), COALESCE(s.agent,'')
		FROM message m LEFT JOIN session s ON s.id = m.session_id
		WHERE m.rowid > ? ORDER BY m.rowid`
	} else {
		query = `
		SELECT m.rowid, m.id, m.session_id, m.time_created, m.data,
		       COALESCE(s.directory,''), COALESCE(s.parent_id,''), COALESCE(s.agent,'')
		FROM message m LEFT JOIN session s ON s.id = m.session_id
		WHERE m.rowid > ? ORDER BY m.rowid`
	}

	rows, err := db.QueryContext(ctx, query, watermark)
	if err != nil {
		if IsBusy(err) {
			return nil // keep the previous watermark and retry next sync
		}
		return err
	}
	defer rows.Close()

	var maxRowID int64
	sessionIDs := map[string]struct{}{}
	scanned := 0

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rowID, timeCreated int64
		var msgID, sessionID, directory, parentID, agent string
		var m ocMessage
		if useJSON {
			var role, modelID, providerID, agentField sql.NullString
			var cost sql.NullFloat64
			var in, out, reasoning, cacheRead, cacheWrite, created, completed sql.NullInt64
			if err := rows.Scan(&rowID, &msgID, &sessionID, &timeCreated,
				&role, &modelID, &providerID, &cost, &agentField,
				&in, &out, &reasoning, &cacheRead, &cacheWrite, &created, &completed,
				&directory, &parentID, &agent); err != nil {
				return err
			}
			m.Role = role.String
			m.ModelID = modelID.String
			m.ProviderID = providerID.String
			m.Agent = agentField.String
			m.Tokens.Input = in.Int64
			m.Tokens.Output = out.Int64
			m.Tokens.Reasoning = reasoning.Int64
			m.Tokens.Cache.Read = cacheRead.Int64
			m.Tokens.Cache.Write = cacheWrite.Int64
			m.Time.Created = created.Int64
			m.Time.Completed = completed.Int64
			if cost.Valid {
				v := cost.Float64
				m.Cost = &v
			}
		} else {
			var data string
			if err := rows.Scan(&rowID, &msgID, &sessionID, &timeCreated, &data,
				&directory, &parentID, &agent); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(data), &m); err != nil {
				continue
			}
		}
		scanned++
		if rowID > maxRowID {
			maxRowID = rowID
		}

		if m.Role != "assistant" {
			continue
		}
		input := ClampTokens(m.Tokens.Input)
		output := ClampTokens(m.Tokens.Output)
		cacheRead := ClampTokens(m.Tokens.Cache.Read)
		cacheWrite := ClampTokens(m.Tokens.Cache.Write)
		reasoning := ClampTokens(m.Tokens.Reasoning)
		total := Total(input, output, cacheRead, cacheWrite)
		if total == 0 {
			continue
		}

		ts := m.Time.Created
		if ts == 0 {
			ts = timeCreated
		}
		ts = ParseEpochMs(ts)

		var latency *int64
		if m.Time.Completed > m.Time.Created && m.Time.Created > 0 {
			ms := m.Time.Completed - m.Time.Created
			latency = &ms
		}

		agentType := "main"
		if parentID != "" {
			agentType = "subagent"
		}
		agentName := NonEmpty(agent, m.Agent)

		snk.addEvent(Event{
			EventKey:   "opencode|" + msgID,
			SourceFile: dbPath,
			SessionID:  sessionID,
			Project:    directory,
			Model:      NonEmpty(m.ModelID, "unknown"),
			Provider:   m.ProviderID,
			AgentType:  agentType,
			AgentName:  agentName,
			Outcome:    "unknown",
			TS:         ts,
			Input:      input,
			Output:     output,
			CacheRead:  cacheRead,
			CacheWrite: cacheWrite,
			Reasoning:  reasoning,
			Total:      total,
			CostUSD:    m.Cost,
			LatencyMs:  latency,
		})
		sessionIDs[sessionID] = struct{}{}

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

	// Session metadata is cosmetic: a failure there must not cost us the events and the
	// watermark below, which are the durable output of this pass.
	_ = a.emitSessions(ctx, db, snk, dbPath, sessionIDs)

	// A pass that found no new rows must not regress the watermark to 0, or the next
	// pass would re-read the whole table.
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

// emitSessions records the metadata of the sessions touched in this pass.
func (a *opencodeAdapter) emitSessions(ctx context.Context, db *sql.DB, snk *sink, dbPath string, ids map[string]struct{}) error {
	if len(ids) == 0 {
		return nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)

	const chunk = 200
	for start := 0; start < len(list); start += chunk {
		end := start + chunk
		if end > len(list) {
			end = len(list)
		}
		part := list[start:end]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		q := `SELECT id, COALESCE(directory,''), COALESCE(parent_id,''), COALESCE(agent,''),
		             COALESCE(model,''), time_created, time_updated
		      FROM session WHERE id IN (` + placeholders(len(part)) + `)`
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, directory, parentID, agent, model string
			var created, updated int64
			if err := rows.Scan(&id, &directory, &parentID, &agent, &model, &created, &updated); err != nil {
				rows.Close()
				return err
			}
			agentType := "main"
			if parentID != "" {
				agentType = "subagent"
			}
			snk.addSession(SessionInfo{
				SessionID:       id,
				ParentSessionID: parentID,
				Project:         directory,
				Model:           openCodeModelID(model),
				AgentType:       agentType,
				AgentName:       agent,
				SourceFile:      dbPath,
				StartedAt:       ParseEpochMs(created),
				UpdatedAt:       ParseEpochMs(updated),
			})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// supportsJSON reports whether SQLite has the JSON1 extension, which decides whether the
// message payload can be projected in SQL instead of read into the process.
func supportsJSON(db *sql.DB) bool {
	var v string
	if err := db.QueryRow(`SELECT json_extract('{"probe":1}','$.probe')`).Scan(&v); err != nil {
		return false
	}
	return v == "1"
}

// openCodeModelID pulls the model id out of the session's model JSON
// ({"id":"kimi-k2.6","providerID":"..."}), falling back to the raw value.
func openCodeModelID(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return raw
	}
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	return NonEmpty(v.ID, raw)
}

// scanLegacyStorage handles the pre-database layout (<root>/storage/message/<session>/<msg>.json).
// It is only used when no database exists, because the database supersedes it.
func (a *opencodeAdapter) scanLegacyStorage(ctx context.Context, in ScanInput) error {
	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
	var files []fileCandidate
	for _, root := range in.Roots {
		base := filepath.Join(root, "storage", "message")
		if !dirExists(base) {
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, sessionEntry := range entries {
			if !sessionEntry.IsDir() {
				continue
			}
			sessionDir := filepath.Join(base, sessionEntry.Name())
			msgs, err := os.ReadDir(sessionDir)
			if err != nil {
				continue
			}
			for _, m := range msgs {
				if m.IsDir() || !strings.HasSuffix(m.Name(), ".json") {
					continue
				}
				path := filepath.Join(sessionDir, m.Name())
				info, err := m.Info()
				if err != nil {
					continue
				}
				addCandidate(&files, path, info)
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Mtime > files[j].Mtime })

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		snk.noteWalked()

		st, seen := in.States[fileKey(f.Path)]
		if !in.Full && seen && st.Unchanged(f.Inode, f.Mtime, f.Size) {
			continue
		}
		raw, err := os.ReadFile(f.Path)
		if err != nil {
			continue
		}
		var m ocMessage
		var legacy struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		}
		if err := json.Unmarshal(raw, &legacy); err != nil {
			continue
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		snk.addState(ScanStateUpdate{
			Key: fileKey(f.Path), Kind: "file",
			Mtime: f.Mtime, Size: f.Size, Inode: f.Inode, Offset: f.Size,
		})
		snk.noteChanged()

		if m.Role != "assistant" {
			continue
		}
		input := ClampTokens(m.Tokens.Input)
		output := ClampTokens(m.Tokens.Output)
		cacheRead := ClampTokens(m.Tokens.Cache.Read)
		cacheWrite := ClampTokens(m.Tokens.Cache.Write)
		reasoning := ClampTokens(m.Tokens.Reasoning)
		total := Total(input, output, cacheRead, cacheWrite)
		if total == 0 {
			continue
		}
		sessionID := NonEmpty(legacy.SessionID, BaseName(DirName(f.Path)))
		snk.addEvent(Event{
			EventKey:   "opencode|" + legacy.ID,
			SourceFile: f.Path,
			SessionID:  sessionID,
			Model:      NonEmpty(m.ModelID, "unknown"),
			Provider:   m.ProviderID,
			AgentType:  "main",
			AgentName:  NonEmpty(m.Agent, ""),
			Outcome:    "unknown",
			TS:         ParseEpochMs(m.Time.Created),
			Input:      input,
			Output:     output,
			CacheRead:  cacheRead,
			CacheWrite: cacheWrite,
			Reasoning:  reasoning,
			Total:      total,
			CostUSD:    m.Cost,
		})
		if snk.shouldFlush() {
			if err := snk.flush(); err != nil {
				return err
			}
		}
	}
	return snk.finish()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
