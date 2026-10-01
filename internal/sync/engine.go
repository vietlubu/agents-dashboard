// Package sync drives the scan loop: it walks every harness, persists what changed, and
// keeps the derived rollups in step with the events.
package sync

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/harness"
	"github.com/vietlubu/agents-dashboard/internal/pricing"
	"github.com/vietlubu/agents-dashboard/internal/store"
)

// ErrAlreadyRunning is returned when a scan is requested while one is in flight.
var ErrAlreadyRunning = errors.New("a sync is already running")

// Event names emitted to the frontend. They are registered with the Wails event system so
// the generated TypeScript bindings stay typed.
const (
	EventState    = "sync:state"
	EventProgress = "sync:progress"
	EventDone     = "sync:done"
	EventError    = "sync:error"
	EventData     = "data:changed"
)

// StateEvent is the payload of EventState. It is the only event the frontend needs to know
// whether a scan is in flight; intervals and the next run time come from the sync service,
// which owns the schedule.
type StateEvent struct {
	Running bool   `json:"running"`
	Trigger string `json:"trigger"`
}

// DataChangedEvent is the payload of EventData: an empty signal meaning "refetch what you
// are showing". It exists as a named type so the generated bindings stay typed.
type DataChangedEvent struct{}

// ProgressEvent is the payload of EventProgress.
type ProgressEvent struct {
	Harness   string `json:"harness"`
	Phase     string `json:"phase"`
	Walked    int    `json:"walked"`
	Changed   int    `json:"changed"`
	Events    int    `json:"events"`
	ElapsedMs int64  `json:"elapsedMs"`
}

// DoneEvent is the payload of EventDone.
type DoneEvent struct {
	Trigger        string `json:"trigger"`
	FilesWalked    int    `json:"filesWalked"`
	FilesChanged   int    `json:"filesChanged"`
	EventsInserted int    `json:"eventsInserted"`
	EventsUpdated  int    `json:"eventsUpdated"`
	DurationMs     int64  `json:"durationMs"`
	Cold           bool   `json:"cold"`
}

// ErrorEvent is the payload of EventError.
type ErrorEvent struct {
	Harness string `json:"harness"`
	Message string `json:"message"`
}

// RunSummary describes one completed scan.
type RunSummary struct {
	StartedAt      int64          `json:"startedAt"`
	FinishedAt     int64          `json:"finishedAt"`
	Trigger        string         `json:"trigger"`
	FilesWalked    int            `json:"filesWalked"`
	FilesChanged   int            `json:"filesChanged"`
	EventsInserted int            `json:"eventsInserted"`
	EventsUpdated  int            `json:"eventsUpdated"`
	DurationMs     int64          `json:"durationMs"`
	Cold           bool           `json:"cold"`
	Harnesses      []AdapterState `json:"harnesses"`
	Err            string         `json:"error"`
}

// AdapterState is the per-harness outcome of a scan.
type AdapterState struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Skipped   bool   `json:"skipped"`
	Full      bool   `json:"full"`
	Events    int    `json:"events"`
	Error     string `json:"error"`
}

// Engine runs scans. One scan at a time: a second concurrent scan would double-read the
// same files and fight over the writer connection.
type Engine struct {
	db      *store.DB
	cfg     *config.Config
	catalog *pricing.Catalog
	log     *slog.Logger
	emit    func(name string, payload any)

	// adapters defaults to the registered harnesses rooted at the current user's home.
	// It is settable so a scan can be pointed at another home (tests, and a future
	// "import another machine's sessions" path). Set it before the first Run.
	adapters []harness.Adapter

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	last    *RunSummary

	// checkedRollups records that the rollup invariant has been verified in this process.
	// The check costs a scan of the event table, so it runs on the first completed run and
	// then only after a run that wrote something — which is exactly when drift can appear.
	checkedRollups bool
}

// New builds an engine. emit may be nil (the scan-report tool has no UI).
func New(db *store.DB, cfg *config.Config, catalog *pricing.Catalog, log *slog.Logger, emit func(string, any)) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{db: db, cfg: cfg, catalog: catalog, log: log, emit: emit, adapters: harness.All()}
}

// UseAdapters replaces the harness set the engine scans. It exists so a scan can be aimed
// at a different home directory; it must be called before the first Run.
func (e *Engine) UseAdapters(adapters []harness.Adapter) { e.adapters = adapters }

// SetEmitter installs the event sink. The application object only exists after the
// services are registered, so the emitter is set once it does; call it before Run.
func (e *Engine) SetEmitter(emit func(name string, payload any)) { e.emit = emit }

// Running reports whether a scan is in flight.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// LastRun returns the previous scan's summary.
func (e *Engine) LastRun() *RunSummary {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.last == nil {
		return nil
	}
	out := *e.last
	return &out
}

// Cancel stops an in-flight scan. The next scan resumes from the stored cursors, so a
// cancelled run only loses the work since its last committed batch.
func (e *Engine) Cancel() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running || e.cancel == nil {
		return false
	}
	e.cancel()
	return true
}

func (e *Engine) notify(name string, payload any) {
	if e.emit != nil {
		e.emit(name, payload)
	}
}

// Run performs one scan of every harness.
func (e *Engine) Run(ctx context.Context, trigger string) (RunSummary, error) {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return RunSummary{}, ErrAlreadyRunning
	}
	runCtx, cancel := context.WithCancel(ctx)
	e.running = true
	e.cancel = cancel
	e.mu.Unlock()

	defer func() {
		cancel()
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		e.mu.Unlock()
	}()

	startedAt := time.Now()
	summary := RunSummary{StartedAt: startedAt.UnixMilli(), Trigger: trigger}
	e.notify(EventState, StateEvent{Running: true, Trigger: trigger})

	runID, err := e.db.StartSyncRun(runCtx, trigger)
	if err != nil {
		return e.finishWithError(summary, startedAt, err)
	}

	// A price change since the last scan must be visible before events are priced.
	if err := e.catalog.Reload(runCtx, e.db); err != nil {
		e.log.Warn("reload price catalog", "error", err)
	}

	dirty := store.NewDirtyKeys()
	for _, a := range e.adapters {
		if err := runCtx.Err(); err != nil {
			summary.Err = err.Error()
			break
		}
		prog := e.scanOne(runCtx, a, trigger, &dirty)
		summary.Harnesses = append(summary.Harnesses, prog.AdapterState)
		summary.FilesWalked += prog.Files
		summary.FilesChanged += prog.Changed
		summary.EventsInserted += prog.Inserted
		summary.EventsUpdated += prog.Updated

		// Refresh after each harness so a long first scan becomes visible incrementally
		// instead of only at the end.
		if err := e.db.RefreshRollups(runCtx, dirty); err != nil {
			e.log.Warn("refresh rollups", "harness", a.ID(), "error", err)
			prog.Error = err.Error()
			summary.Harnesses[len(summary.Harnesses)-1] = prog.AdapterState
			e.notify(EventError, ErrorEvent{Harness: a.ID(), Message: err.Error()})
		}
		dirty = store.NewDirtyKeys()
	}

	// A run that was killed before its rollups were refreshed leaves them permanently
	// behind, because a refresh only rebuilds the days it touched. Verify the invariant
	// whenever this run could have broken it and rebuild the tables when it has.
	if runCtx.Err() == nil && (!e.checkedRollups || summary.EventsInserted+summary.EventsUpdated > 0) {
		if intact, err := e.db.RollupsIntact(runCtx); err != nil {
			e.log.Warn("verify rollups", "error", err)
		} else if !intact {
			e.log.Warn("rollups no longer match the event table; rebuilding")
			if err := e.db.RebuildAllRollups(runCtx); err != nil {
				e.log.Warn("rebuild rollups", "error", err)
				e.notify(EventError, ErrorEvent{Harness: "store", Message: err.Error()})
			}
		}
		e.checkedRollups = true
	}

	summary.DurationMs = time.Since(startedAt).Milliseconds()
	summary.FinishedAt = time.Now().UnixMilli()
	summary.Cold = e.isCold(summary)

	if err := e.db.FinishSyncRun(runCtx, runID, summary.StartedAt, summary.FilesWalked,
		summary.FilesChanged, summary.EventsInserted, summary.EventsUpdated, summary.Err); err != nil {
		e.log.Warn("record sync run", "error", err)
	}
	if pruned, err := e.db.PruneEmptySessions(runCtx); err != nil {
		e.log.Warn("prune empty sessions", "error", err)
	} else if pruned > 0 {
		e.log.Info("pruned sessions with no usage", "count", pruned)
	}
	if err := e.db.TrimSyncHistory(runCtx, 200); err != nil {
		e.log.Warn("trim sync history", "error", err)
	}
	if err := e.db.Optimize(runCtx); err != nil {
		e.log.Warn("optimize database", "error", err)
	}

	e.mu.Lock()
	e.last = &summary
	e.mu.Unlock()

	e.notify(EventDone, DoneEvent{
		Trigger: trigger, FilesWalked: summary.FilesWalked, FilesChanged: summary.FilesChanged,
		EventsInserted: summary.EventsInserted, EventsUpdated: summary.EventsUpdated,
		DurationMs: summary.DurationMs, Cold: summary.Cold,
	})
	if summary.EventsInserted+summary.EventsUpdated > 0 {
		e.notify(EventData, DataChangedEvent{})
	}
	e.notify(EventState, StateEvent{Running: false, Trigger: trigger})

	return summary, nil
}

// adapterProgress is the mutable per-harness accounting used while scanning.
type adapterProgress struct {
	AdapterState
	Files    int
	Changed  int
	Inserted int
	Updated  int
}

func (e *Engine) scanOne(ctx context.Context, a harness.Adapter, trigger string, dirty *store.DirtyKeys) adapterProgress {
	prog := adapterProgress{AdapterState: AdapterState{ID: a.ID()}}

	roots, err := harness.ScanRoots(ctx, a, e.db)
	if err != nil {
		prog.Error = err.Error()
		e.notify(EventError, ErrorEvent{Harness: a.ID(), Message: err.Error()})
		return prog
	}
	if !a.Available(roots) {
		// A harness that is not installed is hidden, not an error.
		prog.Skipped = true
		return prog
	}
	prog.Available = true

	states, err := e.db.LoadScanStates(ctx, a.ID())
	if err != nil {
		prog.Error = err.Error()
		e.notify(EventError, ErrorEvent{Harness: a.ID(), Message: err.Error()})
		return prog
	}

	// A parser change means the stored byte offsets no longer describe what the parser
	// reads, so this harness's events and cursors are dropped and rebuilt.
	full := len(states) == 0
	for _, st := range states {
		if st.ParserVersion != a.ParserVersion() {
			full = true
			break
		}
	}
	if full && len(states) > 0 {
		if err := e.db.ClearHarness(ctx, a.ID()); err != nil {
			prog.Error = err.Error()
			e.notify(EventError, ErrorEvent{Harness: a.ID(), Message: err.Error()})
			return prog
		}
		states = map[string]store.ScanState{}
	}
	prog.Full = full

	loc := e.cfg.Snapshot().Location
	started := time.Now()
	var lastProgress time.Time

	in := harness.ScanInput{
		States: toHarnessStates(states),
		Roots:  roots,
		Full:   full,
		Emit: func(b harness.Batch) error {
			// Deletes run before inserts: they describe a file whose previous events are
			// no longer valid.
			for _, d := range b.Deletes {
				removed, derr := e.db.DeleteEventsBySource(ctx, d.Harness, d.SourceFile)
				if derr != nil {
					return derr
				}
				dirty.Merge(removed)
			}

			if len(b.Events) > 0 {
				events := make([]store.Event, 0, len(b.Events))
				for _, ev := range b.Events {
					events = append(events, e.toStoreEvent(ev))
				}
				result, inserted, insErr := e.db.InsertEvents(ctx, events, loc)
				if insErr != nil {
					return insErr
				}
				dirty.Merge(result)
				prog.Inserted += inserted
				prog.Updated += len(events) - inserted
				prog.Events += len(events)
			}
			if len(b.Sessions) > 0 {
				if err := e.db.UpsertSessions(ctx, e.toStoreSessions(b.Sessions)); err != nil {
					return err
				}
			}
			if len(b.States) > 0 {
				if err := e.db.SaveScanStates(ctx, e.toStoreStates(a, b.States)); err != nil {
					return err
				}
			}
			if b.Walked > 0 {
				prog.Files = b.Walked
			}
			if b.Changed > 0 {
				prog.Changed = b.Changed
			}
			if time.Since(lastProgress) > 200*time.Millisecond {
				lastProgress = time.Now()
				e.notify(EventProgress, ProgressEvent{
					Harness: a.ID(), Phase: "parse", Walked: prog.Files, Changed: prog.Changed,
					Events: prog.Events, ElapsedMs: time.Since(started).Milliseconds(),
				})
			}
			return nil
		},
		EmitProgress: func(p harness.Progress) {
			if p.Walked > prog.Files {
				prog.Files = p.Walked
			}
			if p.Changed > prog.Changed {
				prog.Changed = p.Changed
			}
			e.notify(EventProgress, ProgressEvent{
				Harness: a.ID(), Phase: p.Phase, Walked: p.Walked, Changed: p.Changed,
				Events: p.Events, ElapsedMs: p.ElapsedMs,
			})
		},
	}

	if err := a.Scan(ctx, in); err != nil {
		prog.Error = err.Error()
		if ctx.Err() != nil {
			prog.Error = "cancelled"
		} else {
			e.notify(EventError, ErrorEvent{Harness: a.ID(), Message: err.Error()})
		}
	}

	// A cursor whose file disappeared must not linger, or a deleted session would keep a
	// row forever.
	if removed, err := e.db.PruneScanStates(ctx, a.ID()); err == nil && removed > 0 {
		e.log.Debug("pruned stale cursors", "harness", a.ID(), "count", removed)
	}
	return prog
}

func (e *Engine) finishWithError(summary RunSummary, startedAt time.Time, err error) (RunSummary, error) {
	summary.Err = err.Error()
	summary.DurationMs = time.Since(startedAt).Milliseconds()
	summary.FinishedAt = time.Now().UnixMilli()
	e.notify(EventError, ErrorEvent{Message: err.Error()})
	e.notify(EventState, StateEvent{Running: false})
	return summary, err
}

// isCold reports whether this run was effectively a first scan, which the UI uses to
// explain why the numbers appear progressively.
func (e *Engine) isCold(summary RunSummary) bool {
	for _, h := range summary.Harnesses {
		if h.Full && !h.Skipped {
			return true
		}
	}
	return false
}

// toStoreEvent maps an adapter event, resolving a cost for anything the harness did not
// report itself.
func (e *Engine) toStoreEvent(ev harness.Event) store.Event {
	cost := ev.CostUSD
	source := store.CostSourceReported
	if cost == nil {
		cost, source = e.catalog.Resolve(ev.Model, ev.Input, ev.Output, ev.CacheRead, ev.CacheWrite)
		if source == "" {
			source = store.CostSourceUnavailable
		}
	}
	return store.Event{
		EventKey: ev.EventKey, Harness: ev.Harness, SourceFile: ev.SourceFile,
		SessionID: ev.SessionID, Project: ev.Project, Model: ev.Model, Provider: ev.Provider,
		AgentType: ev.AgentType, AgentName: ev.AgentName, Outcome: ev.Outcome, TS: ev.TS,
		Input: ev.Input, Output: ev.Output, CacheRead: ev.CacheRead, CacheWrite: ev.CacheWrite,
		Reasoning: ev.Reasoning, Total: ev.Total,
		CostUSD: cost, CostSource: source,
		LatencyMs: ev.LatencyMs, TTFTMs: ev.TTFTMs,
	}
}

func (e *Engine) toStoreSessions(in []harness.SessionInfo) []store.Session {
	out := make([]store.Session, 0, len(in))
	for _, s := range in {
		out = append(out, store.Session{
			Harness: s.Harness, SessionID: s.SessionID, ParentSessionID: s.ParentSessionID,
			Project: s.Project, Model: s.Model, AgentType: s.AgentType, AgentName: s.AgentName,
			SourceFile: s.SourceFile, StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt,
		})
	}
	return out
}

// toHarnessStates adapts the stored cursors to the shape adapters read.
func toHarnessStates(in map[string]store.ScanState) map[string]harness.ScanState {
	out := make(map[string]harness.ScanState, len(in))
	for k, s := range in {
		out[k] = harness.ScanState{
			Mtime: s.Mtime, Size: s.Size, Inode: s.Inode,
			Offset: s.Offset, Watermark: s.Watermark,
		}
	}
	return out
}

func (e *Engine) toStoreStates(a harness.Adapter, in []harness.ScanStateUpdate) []store.ScanState {
	out := make([]store.ScanState, 0, len(in))
	for _, s := range in {
		out = append(out, store.ScanState{
			Key: s.Key, Harness: a.ID(), Kind: s.Kind,
			Mtime: s.Mtime, Size: s.Size, Inode: s.Inode,
			Offset: s.Offset, Watermark: s.Watermark,
			ParserVersion: a.ParserVersion(), Err: s.Err,
		})
	}
	return out
}
