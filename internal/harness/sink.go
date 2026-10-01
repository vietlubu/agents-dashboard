package harness

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// batchEvents is the flush threshold for one emitted batch. Large enough to amortise
// transaction overhead, small enough that a cancelled scan never loses more than this
// many records and progress events stay responsive.
const batchEvents = 2000

// progressInterval throttles progress notifications so a fast scan cannot flood the UI.
const progressInterval = 250 * time.Millisecond

// fileCandidate is a walked file together with the signature used for skip decisions.
type fileCandidate struct {
	Path  string
	Mtime int64
	Size  int64
	Inode int64
}

// sink accumulates events and emits them in batches, keeping one adapter's Scan body
// free of bookkeeping.
type sink struct {
	harness  string
	emit     func(Batch) error
	progress func(Progress)

	events   []Event
	sessions []SessionInfo
	states   []ScanStateUpdate
	deletes  []SourceRef

	walked      int
	changed     int
	totalEvents int
	started     time.Time
	lastAt      time.Time
}

func newSink(harness string, emit func(Batch) error, progress func(Progress)) *sink {
	now := time.Now()
	return &sink{harness: harness, emit: emit, progress: progress, started: now, lastAt: now}
}

func (s *sink) noteWalked()  { s.walked++ }
func (s *sink) noteChanged() { s.changed++ }

func (s *sink) addEvent(e Event) {
	e.Harness = s.harness
	s.events = append(s.events, e)
}

func (s *sink) addSession(si SessionInfo) {
	// A session whose timestamps are both zero came from a file that yielded no usage at
	// all (an empty, user-only or aborted transcript). Storing it would create a row that
	// no date range can ever match, so it is dropped: a session exists only once it has
	// usage behind it.
	if si.StartedAt == 0 && si.UpdatedAt == 0 {
		return
	}
	si.Harness = s.harness
	s.sessions = append(s.sessions, si)
}
func (s *sink) addState(u ScanStateUpdate) { s.states = append(s.states, u) }

func (s *sink) addDelete(sourceFile string) {
	s.deletes = append(s.deletes, SourceRef{Harness: s.harness, SourceFile: sourceFile})
}

// shouldFlush reports whether the batch is full enough to emit.
func (s *sink) shouldFlush() bool { return len(s.events) >= batchEvents }

// flush emits everything accumulated so far. Per-file states and deletes are emitted
// with the events they describe, so a crash between batches can only lose data that was
// never acknowledged.
func (s *sink) flush() error {
	if len(s.events) == 0 && len(s.states) == 0 && len(s.deletes) == 0 && len(s.sessions) == 0 {
		return nil
	}
	b := Batch{
		Events:   s.events,
		Sessions: s.sessions,
		States:   s.states,
		Deletes:  s.deletes,
		Walked:   s.walked,
		Changed:  s.changed,
	}
	s.totalEvents += len(s.events)
	s.events = nil
	s.sessions = nil
	s.states = nil
	s.deletes = nil
	if err := s.emit(b); err != nil {
		return err
	}
	s.maybeProgress("parse")
	return nil
}

func (s *sink) maybeProgress(phase string) {
	if s.progress == nil {
		return
	}
	now := time.Now()
	if now.Sub(s.lastAt) < progressInterval {
		return
	}
	s.lastAt = now
	s.progress(Progress{
		Phase:     phase,
		Walked:    s.walked,
		Changed:   s.changed,
		Events:    s.totalEvents,
		ElapsedMs: now.Sub(s.started).Milliseconds(),
	})
}

func (s *sink) finish() error {
	if err := s.flush(); err != nil {
		return err
	}
	if s.progress != nil {
		s.progress(Progress{
			Phase:     "done",
			Walked:    s.walked,
			Changed:   s.changed,
			Events:    s.totalEvents,
			ElapsedMs: time.Since(s.started).Milliseconds(),
		})
	}
	return nil
}

// walkJSONL returns every *.jsonl under the roots, newest first. Newest-first matters
// for responsiveness: a cold scan of a year of history should surface today's sessions
// to the UI long before it finishes, and every JSONL adapter dedupes by content, so
// ordering cannot change the totals.
func walkJSONL(roots []string) []fileCandidate {
	var out []fileCandidate
	for _, root := range roots {
		fi, err := os.Stat(root)
		if err != nil {
			continue
		}
		if fi.Mode().IsRegular() {
			addCandidate(&out, root, fi)
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // an unreadable subtree must not abort the whole scan
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			addCandidate(&out, path, info)
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mtime != out[j].Mtime {
			return out[i].Mtime > out[j].Mtime
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func addCandidate(out *[]fileCandidate, path string, fi os.FileInfo) {
	*out = append(*out, fileCandidate{
		Path:  path,
		Mtime: fi.ModTime().UnixMilli(),
		Size:  fi.Size(),
		Inode: FileInode(fi),
	})
}

// fileKey is the scan_state primary key for a JSONL file.
func fileKey(path string) string { return "file:" + path }

// sqlKey is the scan_state primary key for a foreign database.
func sqlKey(harness, path string) string { return "sql:" + harness + ":" + path }

// sharedState bundles what every adapter needs to decide whether a file changed and to
// persist the new cursor.
type fileDecision struct {
	Offset   int64
	Skip     bool
	Replaced bool
}

// decideFile applies the incremental rules for one JSONL file: skip it when nothing
// changed, restart from zero when it was truncated or replaced, otherwise resume.
func decideFile(f fileCandidate, st ScanState, seen, full bool) fileDecision {
	if full || !seen {
		return fileDecision{Offset: 0}
	}
	if st.Unchanged(f.Inode, f.Mtime, f.Size) {
		return fileDecision{Skip: true}
	}
	// A replaced file (new inode) or a shrunk file holds different content: previously
	// parsed events for this path must be dropped before re-reading from the start.
	replaced := (st.Inode != 0 && f.Inode != 0 && st.Inode != f.Inode) || f.Size < st.Offset
	if replaced {
		return fileDecision{Offset: 0, Replaced: true}
	}
	return fileDecision{Offset: st.Offset}
}
