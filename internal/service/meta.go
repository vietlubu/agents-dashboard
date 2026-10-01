package service

import (
	"context"

	"github.com/vietlubu/agents-dashboard/internal/store"
)

// MetaService serves the dimensions the UI filters by: harnesses, facet values, sessions.
type MetaService struct {
	deps *Deps
}

// NewMetaService builds the metadata service.
func NewMetaService(deps *Deps) *MetaService { return &MetaService{deps: deps} }

// Harnesses lists every known harness with its availability and resolved roots.
func (s *MetaService) Harnesses() ([]store.HarnessInfo, error) {
	return s.deps.Engine.HarnessInfo(context.Background())
}

// Facets lists the distinct values of a dimension with their totals, for filter menus.
// Accepted dimensions: harness, model, project, agentType, outcome, costSource.
func (s *MetaService) Facets(dim string) ([]store.FacetValue, error) {
	return s.deps.DB.FacetValues(context.Background(), dim)
}

// Sessions returns a page of sessions. The range filter selects sessions active inside it;
// the token and cost columns are lifetime sums for each session.
func (s *MetaService) Sessions(q store.RangeQuery, offset, limit int, sortBy string) (store.SessionPage, error) {
	return s.deps.DB.Sessions(context.Background(), q, offset, limit, sortBy)
}

// SessionDetail is one session plus what it spent, for the drill-down panel.
type SessionDetail struct {
	Session store.Session        `json:"session"`
	Totals  store.Totals         `json:"totals"`
	ByModel []store.BreakdownRow `json:"byModel"`
}

// SessionDetail returns a session's identity, its lifetime totals and its model mix.
func (s *MetaService) SessionDetail(harness, sessionID string) (SessionDetail, error) {
	ctx := context.Background()
	session, found, err := s.deps.DB.SessionDetail(ctx, harness, sessionID)
	if err != nil {
		return SessionDetail{}, err
	}
	if !found {
		return SessionDetail{}, nil
	}

	q := store.RangeQuery{Harness: []string{harness}, SessionIDs: []string{sessionID}}
	totals, err := s.deps.DB.Totals(ctx, q, s.deps.loc())
	if err != nil {
		return SessionDetail{}, err
	}
	byModel, err := s.deps.DB.Breakdown(ctx, q, s.deps.loc(), store.DimModel, 20)
	if err != nil {
		return SessionDetail{}, err
	}
	return SessionDetail{Session: session, Totals: totals, ByModel: byModel}, nil
}

// RunReport returns the per-harness report (roots, availability, and what each harness
// contributed). It is the same data the scan-report tool prints.
func (s *MetaService) RunReport() ([]store.HarnessReport, error) {
	return s.deps.Engine.Report(context.Background())
}
