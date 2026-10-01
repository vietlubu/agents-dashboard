package service

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/store"
)

// exportRowCap bounds one export. An unbounded export of a multi-year range would build a
// multi-hundred-megabyte string in memory only to hand it to the browser.
const exportRowCap = 200000

// eventColumns is the column set the events table and the export share.
var eventColumns = []string{
	"harness", "time", "day", "project", "sessionId", "model", "provider",
	"agentType", "agentName", "outcome", "input", "output", "cacheRead", "cacheWrite",
	"reasoning", "total", "costUsd", "costSource", "latencyMs", "ttftMs",
}

// EventsService serves the raw event table and its export.
type EventsService struct {
	deps *Deps
}

// NewEventsService builds the events service.
func NewEventsService(deps *Deps) *EventsService { return &EventsService{deps: deps} }

// List returns a page of events, newest first, with the total match count.
func (s *EventsService) List(q store.RangeQuery, offset, limit int) (store.EventPage, error) {
	return s.deps.DB.Events(context.Background(), q, offset, limit)
}

// Columns returns the column names available for the export and the column settings modal.
func (s *EventsService) Columns() ([]string, error) {
	out := make([]string, len(eventColumns))
	copy(out, eventColumns)
	return out, nil
}

// Export renders the filtered events as CSV or JSON. The content is returned rather than
// written to a file so export also works in headless server mode, where native save
// dialogs do not exist; the frontend turns it into a download.
func (s *EventsService) Export(q store.RangeQuery, format string) (store.ExportResult, error) {
	ctx := context.Background()
	rows, truncated, err := s.deps.DB.ExportEvents(ctx, q, exportRowCap)
	if err != nil {
		return store.ExportResult{}, err
	}

	loc := s.deps.loc()
	stamp := time.Now().In(loc).Format("20060102-150405")
	result := store.ExportResult{Rows: int64(len(rows)), Truncated: truncated}

	switch strings.ToLower(format) {
	case "json":
		content, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return store.ExportResult{}, err
		}
		result.Filename = fmt.Sprintf("agents-dashboard-events-%s.json", stamp)
		result.MimeType = "application/json"
		result.Content = string(content)
	case "csv", "":
		var sb strings.Builder
		w := csv.NewWriter(&sb)
		if err := w.Write(eventColumns); err != nil {
			return store.ExportResult{}, err
		}
		for _, r := range rows {
			if err := w.Write(eventCSVRow(r, loc)); err != nil {
				return store.ExportResult{}, err
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return store.ExportResult{}, err
		}
		result.Filename = fmt.Sprintf("agents-dashboard-events-%s.csv", stamp)
		result.MimeType = "text/csv"
		result.Content = sb.String()
	default:
		return store.ExportResult{}, fmt.Errorf("unsupported export format %q", format)
	}
	return result, nil
}

// eventCSVRow renders one event in the same column order as eventColumns.
func eventCSVRow(r store.EventRow, loc *time.Location) []string {
	cost := ""
	if r.CostUSD != nil {
		cost = strconv.FormatFloat(*r.CostUSD, 'f', 6, 64)
	}
	latency := ""
	if r.LatencyMs != nil {
		latency = strconv.FormatInt(*r.LatencyMs, 10)
	}
	ttft := ""
	if r.TTFTMs != nil {
		ttft = strconv.FormatInt(*r.TTFTMs, 10)
	}
	return []string{
		r.Harness,
		time.UnixMilli(r.TS).In(loc).Format(time.RFC3339),
		r.LocalDay,
		r.Project,
		r.SessionID,
		r.Model,
		r.Provider,
		r.AgentType,
		r.AgentName,
		r.Outcome,
		strconv.FormatInt(r.Input, 10),
		strconv.FormatInt(r.Output, 10),
		strconv.FormatInt(r.CacheRead, 10),
		strconv.FormatInt(r.CacheWrite, 10),
		strconv.FormatInt(r.Reasoning, 10),
		strconv.FormatInt(r.Total, 10),
		cost,
		r.CostSource,
		latency,
		ttft,
	}
}
