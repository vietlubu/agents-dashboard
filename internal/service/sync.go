package service

import (
	"context"

	"github.com/vietlubu/agents-dashboard/internal/store"
	syncengine "github.com/vietlubu/agents-dashboard/internal/sync"
)

// SyncService exposes the scan loop to the UI: its status, a manual trigger, a cancel, and
// the run history.
type SyncService struct {
	deps *Deps
}

// NewSyncService builds the sync service.
func NewSyncService(deps *Deps) *SyncService { return &SyncService{deps: deps} }

// SyncStatus describes the scan loop's schedule. It is separate from the state event
// because the schedule is pulled on demand rather than pushed on every change.
type SyncStatus struct {
	Running    bool  `json:"running"`
	NextRunAt  int64 `json:"nextRunAt"`
	IntervalMs int64 `json:"intervalMs"`
	Burst      bool  `json:"burst"`
}

// Status reports whether a scan is running and when the next one starts.
func (s *SyncService) Status() SyncStatus {
	st := s.deps.Scheduler.Status()
	return SyncStatus{
		Running:    st.Running,
		NextRunAt:  st.NextRunAt,
		IntervalMs: st.IntervalMs,
		Burst:      st.Burst,
	}
}

// TriggerNow starts an immediate scan. It returns false when one is already running.
func (s *SyncService) TriggerNow() (bool, error) {
	return s.deps.Scheduler.TriggerNow("manual"), nil
}

// Cancel stops an in-flight scan. The next scan resumes from the stored cursors, so only
// the work since the last committed batch is repeated.
func (s *SyncService) Cancel() bool {
	return s.deps.Engine.Cancel()
}

// History returns the most recent scans, newest first.
func (s *SyncService) History(limit int) ([]store.SyncRun, error) {
	return s.deps.DB.SyncHistory(context.Background(), limit)
}

// LastRun returns the previous scan's summary, or a zero value before the first scan.
func (s *SyncService) LastRun() syncengine.RunSummary {
	if last := s.deps.Engine.LastRun(); last != nil {
		return *last
	}
	return syncengine.RunSummary{}
}
