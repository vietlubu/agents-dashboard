package service

import (
	"context"

	"github.com/vietlubu/agent-dashboard/internal/store"
)

// DashboardService serves every aggregate the charts need. All of it is read from the
// precomputed rollups, except where a range cannot be answered by a day-keyed aggregate
// (see store.pickSource), so a page render never scans the event table.
type DashboardService struct {
	deps *Deps
}

// NewDashboardService builds the aggregate service.
func NewDashboardService(deps *Deps) *DashboardService { return &DashboardService{deps: deps} }

// Totals aggregates the range.
func (s *DashboardService) Totals(q store.RangeQuery) (store.Totals, error) {
	return s.deps.DB.Totals(context.Background(), q, s.deps.loc())
}

// Compare aggregates the range and the adjacent previous window of equal length, which is
// what the delta badges are computed from.
func (s *DashboardService) Compare(q store.RangeQuery) (store.Comparison, error) {
	return s.deps.DB.Compare(context.Background(), q, s.deps.loc())
}

// Series returns a time series at the requested granularity (hour, day, week or month).
// Buckets with no usage are omitted and the frontend fills the axis from the range it asked
// for; the result names the granularity it was actually built at, which can be coarser than
// the request when an hourly chart would be unreadable.
func (s *DashboardService) Series(q store.RangeQuery, granularity string) (store.SeriesResult, error) {
	return s.deps.DB.Series(context.Background(), q, s.deps.loc(), granularity)
}

// Stacked returns (day, dimension value) cells for a stacked chart, keeping only the
// largest `top` values so the legend is stable across days.
func (s *DashboardService) Stacked(q store.RangeQuery, dim string, top int) ([]store.StackedPoint, error) {
	return s.deps.DB.StackedSeries(context.Background(), q, s.deps.loc(), dim, top)
}

// Breakdown groups the range by one dimension. Accepted dimensions: harness, model,
// project, agentType, outcome, costSource.
func (s *DashboardService) Breakdown(q store.RangeQuery, dim string, limit int) ([]store.BreakdownRow, error) {
	return s.deps.DB.Breakdown(context.Background(), q, s.deps.loc(), dim, limit)
}

// Realtime returns per-minute buckets and active sessions for the last `minutes`.
func (s *DashboardService) Realtime(minutes int) (store.RealtimeSnapshot, error) {
	return s.deps.DB.Realtime(context.Background(), minutes, s.deps.loc())
}

// Heatmap returns per-day totals across a window.
func (s *DashboardService) Heatmap(fromMs, toMs int64) ([]store.HeatCell, error) {
	return s.deps.DB.Heatmap(context.Background(), fromMs, toMs, s.deps.loc())
}

// Latency returns events that carry timing, newest first. Only OpenCode and omp record
// timings, so the caller must present coverage rather than treating missing rows as zero.
func (s *DashboardService) Latency(q store.RangeQuery, limit int) ([]store.LatencyPoint, error) {
	return s.deps.DB.LatencySamples(context.Background(), q, limit)
}

// UnpricedModels lists models with usage but no price, so the UI can offer to add a rate
// instead of leaving a blank in the cost column.
func (s *DashboardService) UnpricedModels() ([]string, error) {
	return s.deps.DB.UnpricedModels(context.Background())
}

// Dims returns the dimension names the breakdown endpoints accept, so the UI does not have
// to hardcode them.
func (s *DashboardService) Dims() ([]string, error) {
	return []string{
		store.DimHarness, store.DimModel, store.DimProject,
		store.DimAgentType, store.DimOutcome, store.DimCostSource,
	}, nil
}
