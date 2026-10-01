package sync

import (
	"context"

	"github.com/vietlubu/agent-dashboard/internal/harness"
	"github.com/vietlubu/agent-dashboard/internal/store"
)

// Report builds the per-harness report: what each harness is, where it was looked for,
// whether it was found, and what its events sum to. It is the single source behind the
// settings page and the scan-report tool, so the two can never disagree.
func (e *Engine) Report(ctx context.Context) ([]store.HarnessReport, error) {
	adapters := e.adapters
	out := make([]store.HarnessReport, 0, len(adapters))

	for _, a := range adapters {
		resolved, err := harness.ResolveRoots(ctx, a, e.db)
		if err != nil {
			return nil, err
		}

		report := store.HarnessReport{
			ID:        a.ID(),
			Name:      a.DisplayName(),
			Available: a.Available(resolved.Found),
			Roots:     resolved.All,
		}

		totals, err := e.db.HarnessTotals(ctx, a.ID())
		if err != nil {
			return nil, err
		}
		report.Events = totals.Events
		report.Input = totals.Input
		report.Output = totals.Output
		report.CacheRead = totals.CacheRead
		report.CacheWrite = totals.CacheWrite
		report.Reasoning = totals.Reasoning
		report.Total = totals.Total
		report.CostUSD = totals.CostUSD
		report.ReportedCostUSD = totals.ReportedCostUSD
		report.LatencyRows = totals.LatencyCount
		report.TTFTRows = totals.TTFTCount

		if report.Sessions, err = e.db.SessionCount(ctx, a.ID()); err != nil {
			return nil, err
		}
		if report.FirstTS, report.LastTS, err = e.db.HarnessBounds(ctx, a.ID()); err != nil {
			return nil, err
		}
		out = append(out, report)
	}
	return out, nil
}

// HarnessInfo returns the identity and availability of every harness for filter menus.
func (e *Engine) HarnessInfo(ctx context.Context) ([]store.HarnessInfo, error) {
	adapters := e.adapters
	out := make([]store.HarnessInfo, 0, len(adapters))
	for _, a := range adapters {
		resolved, err := harness.ResolveRoots(ctx, a, e.db)
		if err != nil {
			return nil, err
		}
		info := store.HarnessInfo{
			ID:        a.ID(),
			Name:      a.DisplayName(),
			Available: a.Available(resolved.Found),
			Roots:     resolved.All,
			Found:     resolved.Found,
		}
		if info.Events, err = e.db.HarnessEventCount(ctx, a.ID()); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// Stats proxies the database summary used by the settings page.
func (e *Engine) Stats(ctx context.Context) (store.Stats, error) {
	return e.db.Stats(ctx)
}
