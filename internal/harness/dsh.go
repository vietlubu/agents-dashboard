package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// dshParserVersion is bumped when the DSH parse rules change. The sync engine then clears
// this harness's stored offsets and events, because old offsets no longer describe what
// the parser reads.
const dshParserVersion = 1

// dshMarkers prefilters decoded lines. A DSH session log is dominated by tool results,
// inbox splices and boundary markers that carry no usage; only three record shapes are
// consumed. The embedded stream inside an assistant/message repeats the word "chunk" but
// never the quoted type marker, so the prefilter cannot miss a record.
var dshMarkers = [][]byte{
	[]byte(`"assistant/message"`),
	[]byte(`"assistant/chunk"`),
	[]byte(`"request/context"`),
}

// dshZstdMagic is the little-endian frame header magic (0xFD2FB528) that opens every
// Zstandard frame. DSH writes a session log as a concatenation of independent frames —
// one tiny header frame, then one per durable append batch — so a file starts with it
// while a raw log (compression: 'none') does not.
var dshZstdMagic = [4]byte{0x28, 0xB5, 0x2F, 0xFD}

// dshUsage is the whitelist of token buckets DSH reports. inputTokens is disjoint from the
// cache buckets; outputTokens follows the OpenAI convention and already includes
// reasoningTokens, which is why Total() excludes reasoning.
type dshUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
	TotalTokens      int64 `json:"totalTokens"`
}

// dshSource is the model route a message was produced by. The sibling replayState carries
// the raw provider stream (model output, signatures); it is never decoded.
type dshSource struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// dshMessage declares only the routing fields. The message's content array holds prompt,
// reasoning and tool text and is deliberately absent, so encoding/json skips it without
// buffering — the package privacy contract.
type dshMessage struct {
	Source *dshSource `json:"source"`
}

// dshChunk is the streamed variant of a usage report: an early sample written before the
// assembled assistant/message exists, which the same (turn, step) key collapses onto.
type dshChunk struct {
	Type  string    `json:"type"`
	Usage *dshUsage `json:"usage"`
}

// dshData is the payload of one consumed record. The assistant/message's embedded stream
// and the user/message content are not declared and are never read.
type dshData struct {
	Turn        int64       `json:"turn"`
	Step        int64       `json:"step"`
	Interrupted bool        `json:"interrupted"`
	Usage       *dshUsage   `json:"usage"`
	Provider    string      `json:"provider"`
	Model       string      `json:"model"`
	Message     *dshMessage `json:"message"`
	Chunk       *dshChunk   `json:"chunk"`
}

// dshLine is the envelope every record shares: a type, a monotonic seq and epoch-ms time.
type dshLine struct {
	Type string   `json:"type"`
	Seq  int64    `json:"seq"`
	Time int64    `json:"time"`
	Data *dshData `json:"data"`
}

// dshHeader is the session's first line. It carries the identity the incremental scan
// needs but that the appended frames do not repeat: the id, the working directory and the
// delegation depth that separates a subagent's session from its parent's.
type dshHeader struct {
	Type            string `json:"type"`
	Version         int64  `json:"version"`
	ID              string `json:"id"`
	CreatedAt       int64  `json:"createdAt"`
	Cwd             string `json:"cwd"`
	DelegationDepth int64  `json:"delegationDepth"`
}

// dshFile is the one canonical log chosen for a session directory, plus its signature.
// DSH retains predecessor generations after a format migration, so a session directory can
// hold both session.v3.jsonl.zstd and session.v4.jsonl.zstd; counting both would double
// the session's usage, and only the numerically highest generation is live.
type dshFile struct {
	Path       string
	Generation int
	Compressed bool
	Mtime      int64
	Size       int64
	Inode      int64
}

// dshStep identifies one model step inside a session. DSH reports a step's usage twice
// (an assistant/chunk sample, then the assembled assistant/message), so the step — not the
// record — is the unit of usage.
type dshStep struct {
	Turn int64
	Step int64
}

type dshAdapter struct {
	home string
}

func newDshAdapter(home string) *dshAdapter {
	return &dshAdapter{home: home}
}

func (a *dshAdapter) ID() string           { return "dsh" }
func (a *dshAdapter) DisplayName() string  { return "DeepSeek Harness" }
func (a *dshAdapter) ParserVersion() int64 { return dshParserVersion }

// Roots returns DSH's sessions directory. DSH_HOME relocates the whole home (and with it
// the sessions tree), so it is honoured before the default ~/.dsh.
func (a *dshAdapter) Roots(_ context.Context, extra []string) []string {
	home := NonEmpty(os.Getenv("DSH_HOME"))
	if home == "" {
		home = filepath.Join(a.home, ".dsh")
	}
	return append([]string{filepath.Join(home, "sessions")}, extra...)
}

func (a *dshAdapter) Available(roots []string) bool { return anyDirExists(roots) }

func (a *dshAdapter) Scan(ctx context.Context, in ScanInput) error {
	snk := newSink(a.ID(), in.Emit, in.EmitProgress)
	var scanner FileScanner
	for _, sf := range a.sessionFiles(in.Roots) {
		if err := ctx.Err(); err != nil {
			return err
		}
		snk.noteWalked()
		if err := a.scanFile(ctx, in, snk, &scanner, sf); err != nil {
			return err
		}
	}
	return snk.finish()
}

// sessionFiles returns one log per session directory: the highest canonical generation,
// newest file first. Roots are walked independently so a user-added root pointing at a
// single log file still works.
func (a *dshAdapter) sessionFiles(roots []string) []dshFile {
	byDir := map[string]dshFile{}
	for _, root := range roots {
		fi, err := os.Stat(root)
		if err != nil {
			continue
		}
		if fi.Mode().IsRegular() {
			if gen, comp, ok := dshParseFileName(filepath.Base(root)); ok {
				dshKeepGeneration(byDir, root, gen, comp, fi)
			}
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // an unreadable subtree must not abort the whole scan
			}
			if d.IsDir() {
				return nil
			}
			gen, comp, ok := dshParseFileName(d.Name())
			if !ok {
				return nil
			}
			di, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			dshKeepGeneration(byDir, path, gen, comp, di)
			return nil
		})
	}
	out := make([]dshFile, 0, len(byDir))
	for _, f := range byDir {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mtime != out[j].Mtime {
			return out[i].Mtime > out[j].Mtime
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// dshKeepGeneration records a candidate log, replacing the entry for its directory when it
// is a newer format generation. The generation ordering is what prevents a retained
// predecessor from being counted alongside its successor.
func dshKeepGeneration(byDir map[string]dshFile, path string, gen int, compressed bool, fi os.FileInfo) {
	dir := filepath.Dir(path)
	cur, seen := byDir[dir]
	if seen && (gen < cur.Generation || (gen == cur.Generation && !(compressed && !cur.Compressed))) {
		return
	}
	byDir[dir] = dshFile{
		Path:       path,
		Generation: gen,
		Compressed: compressed,
		Mtime:      fi.ModTime().UnixMilli(),
		Size:       fi.Size(),
		Inode:      FileInode(fi),
	}
}

// dshParseFileName reports whether a basename is a canonical DSH session log and returns
// its format generation (0 for the unversioned form) and whether it is compressed. The
// match is exact so sibling artifacts — session.lock in particular — are never mistaken
// for a log.
func dshParseFileName(name string) (generation int, compressed bool, ok bool) {
	base := name
	if strings.HasSuffix(base, ".zstd") {
		base = strings.TrimSuffix(base, ".zstd")
		compressed = true
	}
	if !strings.HasSuffix(base, ".jsonl") {
		return 0, false, false
	}
	base = strings.TrimSuffix(base, ".jsonl")
	if base == "session" {
		return 0, compressed, true
	}
	if !strings.HasPrefix(base, "session.v") {
		return 0, false, false
	}
	n, err := strconv.Atoi(base[len("session.v"):])
	if err != nil || n < 0 {
		return 0, false, false
	}
	return n, compressed, true
}

func (a *dshAdapter) scanFile(ctx context.Context, in ScanInput, snk *sink, scanner *FileScanner, sf dshFile) error {
	st, seen := in.States[fileKey(sf.Path)]
	d := decideFile(fileCandidate{
		Path: sf.Path, Mtime: sf.Mtime, Size: sf.Size, Inode: sf.Inode,
	}, st, seen, in.Full)
	if d.Skip {
		return nil
	}
	if d.Replaced {
		snk.addDelete(sf.Path)
	}

	compressed := dshIsZstd(sf.Path)
	hdr := a.readHeader(sf.Path, compressed)
	sessionID := NonEmpty(hdr.ID, dshSessionIDFromPath(sf.Path))
	agentType := "main"
	if hdr.DelegationDepth > 0 {
		agentType = "subagent"
	}
	baseTS := int64(0)
	if hdr.CreatedAt > 0 {
		baseTS = ParseEpochMs(hdr.CreatedAt)
	}
	acc := &dshAccumulator{sessionID: sessionID, project: hdr.Cwd, agentType: agentType}
	acc.reset(baseTS)

	var res dshReadResult
	if compressed {
		res = dshScanZstd(sf.Path, d.Offset, acc.add)
		// An unclean decode means the stored offset was not usable (a torn trailing frame
		// from a crash, or a frame boundary that no longer exists). Re-read from the start
		// so no usage is lost: the upsert replaces the events by key, so a re-read never
		// double counts.
		if !res.Clean && d.Offset > 0 {
			acc.reset(baseTS)
			res = dshScanZstd(sf.Path, 0, acc.add)
		}
	} else {
		r, err := scanner.Scan(ctx, sf.Path, d.Offset, dshMarkers, func(line []byte) error {
			acc.add(line)
			return nil
		})
		if err != nil {
			return err
		}
		res = dshReadResult{NewOffset: r.NewOffset, Mtime: r.Mtime, Size: r.Size, Inode: r.Inode, Clean: true}
	}

	acc.flush(snk, sf.Path)
	snk.addState(ScanStateUpdate{
		Key:    fileKey(sf.Path),
		Kind:   "file",
		Mtime:  res.Mtime,
		Size:   res.Size,
		Inode:  res.Inode,
		Offset: res.NewOffset,
	})
	if res.NewOffset != d.Offset {
		snk.noteChanged()
	}
	return snk.flush()
}

// dshAccumulator folds one file's consumed records into events. It is reset between an
// incremental read and a full re-read so the fallback cannot emit the same step twice.
type dshAccumulator struct {
	sessionID string
	project   string
	agentType string

	routeModel    string
	routeProvider string
	model         string
	minTS         int64
	maxTS         int64

	events map[dshStep]*Event
	order  []dshStep
}

func (acc *dshAccumulator) reset(baseTS int64) {
	acc.routeModel, acc.routeProvider, acc.model = "", "", ""
	acc.minTS, acc.maxTS = baseTS, 0
	acc.events = map[dshStep]*Event{}
	acc.order = nil
}

func (acc *dshAccumulator) add(line []byte) {
	var l dshLine
	if err := json.Unmarshal(line, &l); err != nil {
		return // a non-JSON or foreign line is not an error
	}
	if l.Data == nil {
		return
	}
	// request/context carries the route the following step's usage belongs to. DSH logs
	// it only when the route changes, so it has to be carried forward.
	if l.Type == "request/context" {
		acc.routeModel = NonEmpty(l.Data.Model, acc.routeModel)
		acc.routeProvider = NonEmpty(l.Data.Provider, acc.routeProvider)
		return
	}
	if l.Type != "assistant/message" && l.Type != "assistant/chunk" {
		return
	}
	usage, model, provider := dshRecordUsage(&l, acc.routeModel, acc.routeProvider)
	if usage == nil {
		return
	}
	ts := ParseEpochMs(l.Time)
	if ts == 0 {
		return
	}

	input := ClampTokens(usage.InputTokens)
	output := ClampTokens(usage.OutputTokens)
	cacheRead := ClampTokens(usage.CacheReadTokens)
	cacheWrite := ClampTokens(usage.CacheWriteTokens)
	reasoning := ClampTokens(usage.ReasoningTokens)
	total := Total(input, output, cacheRead, cacheWrite)
	if total == 0 {
		if usage.TotalTokens <= 0 {
			return
		}
		// Total-only usage cannot be split into buckets; keep the aggregate rather than
		// dropping the usage.
		input = ClampTokens(usage.TotalTokens)
		output, cacheRead, cacheWrite, reasoning = 0, 0, 0, 0
		total = input
	}

	if acc.minTS == 0 || ts < acc.minTS {
		acc.minTS = ts
	}
	if ts > acc.maxTS {
		acc.maxTS = ts
	}
	if acc.model == "" && model != "" {
		acc.model = model
	}

	outcome := "ok"
	if l.Data.Interrupted {
		outcome = "aborted"
	}
	key := dshStep{Turn: l.Data.Turn, Step: l.Data.Step}
	if _, dup := acc.events[key]; !dup {
		acc.order = append(acc.order, key)
	}
	acc.events[key] = &Event{
		EventKey:   dshEventKey(acc.sessionID, key),
		SourceFile: "",
		SessionID:  acc.sessionID,
		Project:    acc.project,
		Model:      NonEmpty(model, "dsh"),
		Provider:   provider,
		AgentType:  acc.agentType,
		AgentName:  dshAgentName(acc.agentType, acc.sessionID),
		Outcome:    outcome,
		TS:         ts,
		Input:      input,
		Output:     output,
		CacheRead:  cacheRead,
		CacheWrite: cacheWrite,
		Reasoning:  reasoning,
		Total:      total,
	}
}

func (acc *dshAccumulator) flush(snk *sink, sourceFile string) {
	for _, key := range acc.order {
		e := acc.events[key]
		e.SourceFile = sourceFile
		snk.addEvent(*e)
	}
	snk.addSession(SessionInfo{
		SessionID:  acc.sessionID,
		Project:    acc.project,
		Model:      acc.model,
		AgentType:  acc.agentType,
		AgentName:  dshAgentName(acc.agentType, acc.sessionID),
		SourceFile: sourceFile,
		StartedAt:  acc.minTS,
		UpdatedAt:  acc.maxTS,
	})
}

// dshRecordUsage extracts the usage and route of one record. An assistant/message carries
// its own provider and model; an assistant/chunk takes them from the last request/context.
func dshRecordUsage(l *dshLine, routeModel, routeProvider string) (*dshUsage, string, string) {
	switch l.Type {
	case "assistant/chunk":
		if l.Data.Chunk == nil || l.Data.Chunk.Type != "usage" || l.Data.Chunk.Usage == nil {
			return nil, "", ""
		}
		return l.Data.Chunk.Usage, routeModel, routeProvider
	case "assistant/message":
		if l.Data.Usage == nil {
			return nil, "", ""
		}
		model, provider := "", ""
		if l.Data.Message != nil && l.Data.Message.Source != nil {
			model = l.Data.Message.Source.Model
			provider = l.Data.Message.Source.Provider
		}
		return l.Data.Usage, NonEmpty(model, routeModel), NonEmpty(provider, routeProvider)
	default:
		return nil, "", ""
	}
}

// dshEventKey keys one model step. DSH writes a step's usage twice (an early chunk sample
// then the assembled message), so keying per step lets the later record supersede the
// earlier one instead of being summed with it.
func dshEventKey(sessionID string, step dshStep) string {
	return "dsh|" + sessionID + "|" + strconv.FormatInt(step.Turn, 10) + "|" + strconv.FormatInt(step.Step, 10)
}

func dshAgentName(agentType, sessionID string) string {
	if agentType == "subagent" {
		return sessionID
	}
	return "main"
}

// dshSessionIDFromPath falls back to the session directory's name — DSH escapes the
// session id into one path segment — when the header cannot be read.
func dshSessionIDFromPath(path string) string {
	return BaseName(DirName(path))
}

// dshReadResult describes what a compressed read consumed. NewOffset is the file size when
// every frame decoded to EOF (the end of the last complete frame), and the requested
// offset otherwise, so a torn trailing frame is re-read rather than skipped.
type dshReadResult struct {
	NewOffset int64
	Mtime     int64
	Size      int64
	Inode     int64
	Clean     bool
}

// dshScanZstd streams a compressed log from offset, offering every marker-matching line to
// add. Frames are independent, so resuming at a frame boundary decodes only what was
// appended; a decode error (a torn final frame, or an offset that is not a boundary) is
// reported as an unclean result, never as a hard failure.
func dshScanZstd(path string, offset int64, add func([]byte)) dshReadResult {
	var res dshReadResult
	f, err := os.Open(path)
	if err != nil {
		return res
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return res
	}
	res.Mtime = fi.ModTime().UnixMilli()
	res.Size = fi.Size()
	res.Inode = FileInode(fi)

	if offset < 0 || offset > res.Size {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return res
		}
	}
	res.NewOffset = offset

	dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return res
	}
	defer dec.Close()

	if _, err := ReadLines(dec, dshMarkers, add); err != nil {
		return res // unclean: keep the previous offset
	}
	res.Clean = true
	res.NewOffset = res.Size
	return res
}

// readHeader reads the session header, which is always the first line of the first frame.
// It is read from the start of the file on every scan because an incremental read resumes
// past it.
func (a *dshAdapter) readHeader(path string, compressed bool) dshHeader {
	var raw []byte
	if compressed {
		raw = dshFirstDecodedLine(path)
	} else if b, err := firstLine(path, 8<<10); err == nil {
		raw = b
	}
	var h dshHeader
	if len(raw) == 0 || json.Unmarshal(raw, &h) != nil || h.Type != "session" {
		return dshHeader{}
	}
	return h
}

// dshFirstDecodedLine decompresses only far enough to return the header line.
func dshFirstDecodedLine(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil
	}
	defer dec.Close()
	line, err := bufio.NewReaderSize(dec, 8<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil
	}
	return bytes.TrimRight(line, "\r\n")
}

// dshIsZstd reports whether a log opens with the Zstandard frame magic, which is how a
// compressed root and a raw one are told apart regardless of the file name.
func dshIsZstd(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return magic == dshZstdMagic
}
