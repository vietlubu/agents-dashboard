// Package harness reads the on-disk session/telemetry files that coding agents write
// and normalizes them into usage events. Each harness is one Adapter; adding a sixth
// harness means adding one file and one line in All().
//
// Privacy contract: adapters decode only usage, model, identifier and timestamp fields
// into fixed structs. Prompt and response text is never read, never buffered, and never
// stored — there is no generic map decode anywhere in this package.
package harness

import "context"

// Event is one normalized usage record. It mirrors store.Event without importing the
// store, so adapters stay testable in isolation.
type Event struct {
	// Harness is stamped by the sink from the adapter that produced the event, so a
	// batch is self-describing and the caller never has to thread the id through.
	Harness         string
	EventKey        string
	SourceFile      string
	SessionID       string
	ParentSessionID string
	Project         string
	Model           string
	Provider        string
	AgentType       string // main | subagent
	AgentName       string
	Outcome         string // ok | tool_use | aborted | error | unknown
	TS              int64  // epoch milliseconds

	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
	Total      int64

	// CostUSD is a source-reported amount when non-nil. CostSource is never set by an
	// adapter; the sync engine decides reported vs estimated vs unavailable.
	CostUSD   *float64
	LatencyMs *int64
	TTFTMs    *int64
}

// SessionInfo is the static identity of a session, emitted once per scanned file.
type SessionInfo struct {
	// Harness is stamped by the sink, like Event.Harness.
	Harness         string
	SessionID       string
	ParentSessionID string
	Project         string
	Model           string
	AgentType       string
	AgentName       string
	SourceFile      string
	StartedAt       int64
	UpdatedAt       int64
}

// ScanStateUpdate is a cursor to persist after the batch it describes is committed.
type ScanStateUpdate struct {
	Key       string
	Kind      string // file | sqlite
	Mtime     int64
	Size      int64
	Inode     int64
	Offset    int64
	Watermark int64
	Err       string
}

// SourceRef names one parsed file, used when its events must be dropped (the file was
// truncated or replaced, so the stored offsets no longer describe its content).
type SourceRef struct {
	Harness    string
	SourceFile string
}

// Batch is one unit of work handed to the sink. Deletes are applied before the events
// in the same batch are inserted.
type Batch struct {
	Events   []Event
	Sessions []SessionInfo
	States   []ScanStateUpdate
	Deletes  []SourceRef
	Walked   int
	Changed  int
}

// Progress is a throttled status update for the UI.
type Progress struct {
	Phase     string // walk | parse | done
	Walked    int
	Changed   int
	Events    int
	ElapsedMs int64
}

// ScanInput carries the prior cursors and resolved roots for one adapter run.
type ScanInput struct {
	States       map[string]ScanState
	Roots        []string
	Full         bool // ignore cursors (first run, or after a parser version bump)
	Emit         func(Batch) error
	EmitProgress func(Progress)
}

// ScanState is the cursor shape adapters read (a copy of store.ScanState's fields that
// matter for scanning, so this package does not depend on the store).
type ScanState struct {
	Mtime     int64
	Size      int64
	Inode     int64
	Offset    int64
	Watermark int64
}

// Unchanged reports whether a file signature still matches a stored cursor.
func (s ScanState) Unchanged(inode, mtime, size int64) bool {
	return s.Inode == inode && s.Mtime == mtime && s.Size == size
}

// Adapter reads one harness.
type Adapter interface {
	// ID is the stable harness identifier stored on every event.
	ID() string
	// DisplayName is the human label shown in the UI.
	DisplayName() string
	// ParserVersion is bumped when parse semantics change; the sync engine then wipes
	// this harness's events and cursors, because stored offsets no longer describe
	// what the parser reads.
	ParserVersion() int64
	// Roots resolves the directories/files to scan, including user-added extras.
	Roots(ctx context.Context, extra []string) []string
	// Available reports whether any root exists; a missing harness is hidden, not failed.
	Available(roots []string) bool
	// Scan walks the roots and emits batches.
	Scan(ctx context.Context, in ScanInput) error
}

// RootsExist splits the resolved roots into existing and missing paths, so the settings
// page can show the expected location of an uninstalled harness.
func RootsExist(roots []string) (found, missing []string) {
	for _, r := range roots {
		if dirExists(r) {
			found = append(found, r)
		} else {
			missing = append(missing, r)
		}
	}
	return found, missing
}
