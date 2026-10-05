package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Dimension names accepted by Breakdown, FacetValues and StackedSeries.
const (
	DimHarness    = "harness"
	DimModel      = "model"
	DimProject    = "project"
	DimAgentType  = "agentType"
	DimOutcome    = "outcome"
	DimCostSource = "costSource"
	DimDay        = "day"
)

// Aggregate column lists. Both sources expose the same 14 columns in the same order so
// one scanner handles either; only the expressions differ.
// Every aggregate is COALESCE'd to 0 because an empty filter result scans into int64
// fields and SQLite returns NULL for SUM over no rows.
const eventsAggCols = `COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
  COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
  COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(total_tokens),0) AS agg_total,
  COALESCE(SUM(cost_usd),0),
  COALESCE(SUM(CASE WHEN cost_source='reported' THEN COALESCE(cost_usd,0) ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN cost_source='unavailable' THEN 1 ELSE 0 END),0) AS agg_unpriced,
  COUNT(*),
  COALESCE(SUM(latency_ms),0), COALESCE(SUM(CASE WHEN latency_ms IS NOT NULL THEN 1 ELSE 0 END),0),
  COALESCE(SUM(ttft_ms),0), COALESCE(SUM(CASE WHEN ttft_ms IS NOT NULL THEN 1 ELSE 0 END),0)`

const rollupAggCols = `COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
  COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
  COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(total_tokens),0) AS agg_total,
  COALESCE(SUM(cost_usd),0), COALESCE(SUM(reported_cost_usd),0), COALESCE(SUM(cost_unavailable),0),
  COALESCE(SUM(event_count),0),
  COALESCE(SUM(latency_sum_ms),0), COALESCE(SUM(latency_count),0),
  COALESCE(SUM(ttft_sum_ms),0), COALESCE(SUM(ttft_count),0)`

type aggRow struct {
	input, output, cacheRead, cacheWrite, reasoning, total int64
	costUSD, reportedCostUSD                               float64
	costUnavailable                                        int64
	events                                                 int64
	latencySum, latencyCount, ttftSum, ttftCount           int64
}

func scanAgg(s interface{ Scan(...any) error }) (aggRow, error) {
	var a aggRow
	err := s.Scan(&a.input, &a.output, &a.cacheRead, &a.cacheWrite, &a.reasoning, &a.total,
		&a.costUSD, &a.reportedCostUSD, &a.costUnavailable, &a.events,
		&a.latencySum, &a.latencyCount, &a.ttftSum, &a.ttftCount)
	return a, err
}

func (a aggRow) totals() Totals {
	return Totals{
		Events: a.events, Input: a.input, Output: a.output, CacheRead: a.cacheRead,
		CacheWrite: a.cacheWrite, Reasoning: a.reasoning, Total: a.total,
		CostUSD: a.costUSD, ReportedCostUSD: a.reportedCostUSD, CostUnavailable: a.costUnavailable,
		LatencySumMs: a.latencySum, LatencyCount: a.latencyCount,
		TTFTSumMs: a.ttftSum, TTFTCount: a.ttftCount,
	}
}

// dayRange is a [fromDay, toDay] inclusive local-day window plus whether it aligns to
// whole days. Rollup tables are day-keyed, so a partial range must read events.
type dayRange struct {
	fromDay string
	toDay   string
	whole   bool
}

func resolveDayRange(q RangeQuery, loc *time.Location) dayRange {
	dr := dayRange{fromDay: "0000-01-01", toDay: "9999-12-31", whole: true}

	if q.FromMs > 0 {
		dr.fromDay = LocalDay(q.FromMs, loc)
		if midnightMs(q.FromMs, loc) != q.FromMs {
			dr.whole = false
		}
	}
	if q.ToMs > 0 {
		dr.toDay = LocalDay(q.ToMs-1, loc)
		if midnightMs(q.ToMs, loc) != q.ToMs {
			dr.whole = false
		}
	}
	return dr
}

func midnightMs(tsMs int64, loc *time.Location) int64 {
	t := time.UnixMilli(tsMs).In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).UnixMilli()
}

// pred accumulates SQL conditions and their arguments.
type pred struct {
	clauses []string
	args    []any
}

func (p *pred) add(clause string, args ...any) {
	p.clauses = append(p.clauses, clause)
	p.args = append(p.args, args...)
}

func (p *pred) in(column string, values []string) {
	if len(values) == 0 {
		return
	}
	ph := make([]string, len(values))
	for i := range values {
		ph[i] = "?"
	}
	p.add(column+" IN ("+strings.Join(ph, ",")+")", toArgs(values)...)
}

func (p *pred) where() string {
	if len(p.clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(p.clauses, " AND ")
}

// eventsWhere builds a predicate over usage_events.
func eventsWhere(q RangeQuery) pred {
	var p pred
	if q.FromMs > 0 {
		p.add("ts >= ?", q.FromMs)
	}
	if q.ToMs > 0 {
		p.add("ts < ?", q.ToMs)
	}
	p.in("harness", q.Harness)
	p.in("model", q.Models)
	p.in("project", q.Projects)
	p.in("agent_type", q.AgentTypes)
	p.in("outcome", q.Outcomes)
	p.in("cost_source", q.CostSources)
	p.in("session_id", q.SessionIDs)
	return p
}

// rollupWhere builds a predicate over a rollup table.
func rollupWhere(q RangeQuery, dr dayRange, hasProject bool) pred {
	var p pred
	p.add("day >= ?", dr.fromDay)
	p.add("day <= ?", dr.toDay)
	p.in("harness", q.Harness)
	if hasProject {
		p.in("project", q.Projects)
	}
	p.in("model", q.Models)
	p.in("agent_type", q.AgentTypes)
	return p
}

// rollupTable picks the rollup table that can serve a query, or reports that neither can.
//
// It refuses in two cases: when both a model and a project dimension are needed (each table
// carries only one of them), and when the dimension itself is one the rollups do not store
// (outcome, cost source) — otherwise the aggregate would be built against a missing column.
func rollupTable(q RangeQuery, dim string) (string, bool) {
	if dim == DimOutcome || dim == DimCostSource {
		return "", false
	}
	needProject := len(q.Projects) > 0 || dim == DimProject
	needModel := len(q.Models) > 0 || dim == DimModel
	if needProject && needModel {
		return "", false
	}
	if needProject {
		return "rollup_daily_hp", true
	}
	return "rollup_daily_hm", true
}

// source describes where a read is served from.
type source struct {
	table  string // usage_events | rollup_daily_hm | rollup_daily_hp
	from   string // "FROM ..."
	where  string
	args   []any
	isRoll bool
	cols   string
}

func (d *DB) pickSource(q RangeQuery, loc *time.Location, dim string) source {
	dr := resolveDayRange(q, loc)
	// Outcomes, cost sources and session ids only exist on the event rows, and a partial
	// day range cannot be answered by a day-keyed rollup.
	if dr.whole && len(q.Outcomes) == 0 && len(q.CostSources) == 0 && len(q.SessionIDs) == 0 {
		if table, ok := rollupTable(q, dim); ok {
			hasProject := table == "rollup_daily_hp"
			p := rollupWhere(q, dr, hasProject)
			return source{
				table: table, from: "FROM " + table, where: p.where(), args: p.args,
				isRoll: true, cols: rollupAggCols,
			}
		}
	}
	p := eventsWhere(q)
	return source{
		table: "usage_events", from: "FROM usage_events", where: p.where(), args: p.args,
		cols: eventsAggCols,
	}
}

// Totals aggregates the whole range. Served from a rollup when the range aligns to
// whole days, otherwise from the indexed event rows.
func (d *DB) Totals(ctx context.Context, q RangeQuery, loc *time.Location) (Totals, error) {
	src := d.pickSource(q, loc, DimHarness)
	row := d.r.QueryRowContext(ctx, "SELECT "+src.cols+" "+src.from+src.where, src.args...)
	a, err := scanAgg(row)
	if err != nil {
		return Totals{}, err
	}
	return a.totals(), nil
}

// Compare returns the range against the adjacent previous window of equal length. An
// unbounded range (no FromMs) has no meaningful predecessor, so Previous is zero.
func (d *DB) Compare(ctx context.Context, q RangeQuery, loc *time.Location) (Comparison, error) {
	current, err := d.Totals(ctx, q, loc)
	if err != nil {
		return Comparison{}, err
	}
	out := Comparison{Current: current}
	if q.FromMs <= 0 || q.ToMs <= q.FromMs {
		return out, nil
	}
	span := q.ToMs - q.FromMs
	prev := q
	prev.FromMs = q.FromMs - span
	prev.ToMs = q.FromMs
	out.Previous, err = d.Totals(ctx, prev, loc)
	return out, err
}

// Granularities a series can be bucketed at.
const (
	GranHour  = "hour"
	GranDay   = "day"
	GranWeek  = "week"
	GranMonth = "month"
)

// maxHourBuckets caps an hourly series. Past this the chart is unreadable and the query
// scans the whole range, so the store answers with days instead and says so in the result.
const maxHourBuckets = 400

// Series returns a time series at the requested bucket size. Hour buckets come from the
// event rows (the rollups are day-keyed); day buckets come from the day rollup; week and
// month buckets fold the day rollup in Go, which keeps the SQL one shape and gets the ISO
// week right instead of relying on strftime's week numbering.
func (d *DB) Series(ctx context.Context, q RangeQuery, loc *time.Location, granularity string) (SeriesResult, error) {
	switch granularity {
	case GranWeek, GranMonth:
		days, err := d.DailySeries(ctx, q, loc)
		if err != nil {
			return SeriesResult{}, err
		}
		return SeriesResult{Granularity: granularity, Points: foldSeries(days, granularity, loc)}, nil
	case GranHour:
		if hours := rangeHours(q); hours > maxHourBuckets {
			break // folded below into the day series
		}
		points, err := d.hourSeries(ctx, q)
		if err != nil {
			return SeriesResult{}, err
		}
		return SeriesResult{Granularity: GranHour, Points: points}, nil
	case "", GranDay:
		break
	}
	points, err := d.DailySeries(ctx, q, loc)
	if err != nil {
		return SeriesResult{}, err
	}
	return SeriesResult{Granularity: GranDay, Points: points}, nil
}

// rangeHours is how many hour buckets a range covers; an open-ended range is unbounded.
func rangeHours(q RangeQuery) int {
	if q.FromMs <= 0 || q.ToMs <= 0 || q.ToMs <= q.FromMs {
		return 0
	}
	return int((q.ToMs - q.FromMs) / (60 * 60 * 1000))
}

// hourSeries buckets the event rows by clock hour. The key is the epoch millisecond at the
// start of the bucket, which the frontend renders in the configured timezone, so a zone
// with a fractional offset still labels each bucket correctly.
func (d *DB) hourSeries(ctx context.Context, q RangeQuery) ([]SeriesPoint, error) {
	p := eventsWhere(q)
	query := "SELECT CAST((ts / 3600000) * 3600000 AS TEXT), " + eventsAggCols +
		" FROM usage_events" + p.where() + " GROUP BY 1 ORDER BY 1"
	rows, err := d.r.QueryContext(ctx, query, p.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SeriesPoint{}
	for rows.Next() {
		var key string
		var a aggRow
		if err := rows.Scan(&key, &a.input, &a.output, &a.cacheRead, &a.cacheWrite, &a.reasoning,
			&a.total, &a.costUSD, &a.reportedCostUSD, &a.costUnavailable, &a.events,
			&a.latencySum, &a.latencyCount, &a.ttftSum, &a.ttftCount); err != nil {
			return nil, err
		}
		out = append(out, SeriesPoint{
			Key: key, Input: a.input, Output: a.output, CacheRead: a.cacheRead,
			CacheWrite: a.cacheWrite, Reasoning: a.reasoning, Total: a.total,
			CostUSD: a.costUSD, Events: a.events,
		})
	}
	return out, rows.Err()
}

// foldSeries rolls day points up into weeks or months. Input is in date order, so the
// output is too.
func foldSeries(days []SeriesPoint, granularity string, loc *time.Location) []SeriesPoint {
	out := make([]SeriesPoint, 0, len(days))
	index := map[string]int{}
	for _, day := range days {
		key := bucketKey(day.Key, granularity, loc)
		if key == "" {
			continue
		}
		at, ok := index[key]
		if !ok {
			out = append(out, SeriesPoint{Key: key})
			at = len(out) - 1
			index[key] = at
		}
		into := &out[at]
		into.Input += day.Input
		into.Output += day.Output
		into.CacheRead += day.CacheRead
		into.CacheWrite += day.CacheWrite
		into.Reasoning += day.Reasoning
		into.Total += day.Total
		into.CostUSD += day.CostUSD
		into.Events += day.Events
	}
	return out
}

// bucketKey turns a local day into the week or month key it belongs to.
func bucketKey(day, granularity string, loc *time.Location) string {
	t, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return ""
	}
	if granularity == GranMonth {
		return t.Format("2006-01")
	}
	year, week := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// DailySeries returns one point per local day in the range, in date order. Days with no
// usage are omitted; the frontend fills the axis from the range.
func (d *DB) DailySeries(ctx context.Context, q RangeQuery, loc *time.Location) ([]SeriesPoint, error) {
	src := d.pickSource(q, loc, DimDay)
	query := "SELECT " + dayExpr(src.isRoll) + ", " + src.cols + " " + src.from + src.where + " GROUP BY 1 ORDER BY 1"
	rows, err := d.r.QueryContext(ctx, query, src.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SeriesPoint{}
	for rows.Next() {
		var key string
		var a aggRow
		if err := rows.Scan(&key, &a.input, &a.output, &a.cacheRead, &a.cacheWrite, &a.reasoning,
			&a.total, &a.costUSD, &a.reportedCostUSD, &a.costUnavailable, &a.events,
			&a.latencySum, &a.latencyCount, &a.ttftSum, &a.ttftCount); err != nil {
			return nil, err
		}
		out = append(out, SeriesPoint{
			Key: key, Input: a.input, Output: a.output, CacheRead: a.cacheRead,
			CacheWrite: a.cacheWrite, Reasoning: a.reasoning, Total: a.total,
			CostUSD: a.costUSD, Events: a.events,
		})
	}
	return out, rows.Err()
}

// HourlySeries returns one point per minute over the last `minutes` minutes. Key is an
// epoch-millisecond string; the frontend formats it as local time.
func (d *DB) HourlySeries(ctx context.Context, q RangeQuery, minutes int) ([]SeriesPoint, error) {
	if minutes <= 0 {
		minutes = 60
	}
	fromMs := time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
	if q.FromMs > fromMs {
		fromMs = q.FromMs
	}
	p := eventsWhere(RangeQuery{
		FromMs: fromMs, Harness: q.Harness, Models: q.Models, Projects: q.Projects,
		AgentTypes: q.AgentTypes, Outcomes: q.Outcomes, CostSources: q.CostSources,
	})
	query := "SELECT CAST((ts / 60000) * 60000 AS TEXT), " + eventsAggCols + " FROM usage_events" +
		p.where() + " GROUP BY 1 ORDER BY 1"
	rows, err := d.r.QueryContext(ctx, query, p.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SeriesPoint{}
	for rows.Next() {
		var key string
		var a aggRow
		if err := rows.Scan(&key, &a.input, &a.output, &a.cacheRead, &a.cacheWrite, &a.reasoning,
			&a.total, &a.costUSD, &a.reportedCostUSD, &a.costUnavailable, &a.events,
			&a.latencySum, &a.latencyCount, &a.ttftSum, &a.ttftCount); err != nil {
			return nil, err
		}
		out = append(out, SeriesPoint{
			Key: key, Input: a.input, Output: a.output, CacheRead: a.cacheRead,
			CacheWrite: a.cacheWrite, Reasoning: a.reasoning, Total: a.total,
			CostUSD: a.costUSD, Events: a.events,
		})
	}
	return out, rows.Err()
}

func dayExpr(isRollup bool) string {
	if isRollup {
		return "day"
	}
	return "local_day"
}

// dimExpr maps a public dimension name to the SQL column for a source.
func dimExpr(dim string, isRollup bool) (string, error) {
	switch dim {
	case DimHarness:
		return "harness", nil
	case DimModel:
		return "model", nil
	case DimProject:
		return "project", nil
	case DimAgentType:
		return "agent_type", nil
	case DimOutcome:
		if isRollup {
			return "", fmt.Errorf("outcome is not available from rollups")
		}
		return "outcome", nil
	case DimCostSource:
		if isRollup {
			return "", fmt.Errorf("cost source is not available from rollups")
		}
		return "cost_source", nil
	default:
		return "", fmt.Errorf("unknown dimension %q", dim)
	}
}

// Breakdown groups the range by one dimension, largest total first.
func (d *DB) Breakdown(ctx context.Context, q RangeQuery, loc *time.Location, dim string, limit int) ([]BreakdownRow, error) {
	if limit <= 0 {
		limit = 50
	}
	src := d.pickSource(q, loc, dim)
	col, err := dimExpr(dim, src.isRoll)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("SELECT %s, %s %s%s GROUP BY 1 ORDER BY agg_total DESC LIMIT %d",
		col, src.cols, src.from, src.where, limit)
	rows, err := d.r.QueryContext(ctx, query, src.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BreakdownRow{}
	for rows.Next() {
		var key string
		var a aggRow
		if err := rows.Scan(&key, &a.input, &a.output, &a.cacheRead, &a.cacheWrite, &a.reasoning,
			&a.total, &a.costUSD, &a.reportedCostUSD, &a.costUnavailable, &a.events,
			&a.latencySum, &a.latencyCount, &a.ttftSum, &a.ttftCount); err != nil {
			return nil, err
		}
		row := BreakdownRow{
			Key: key, Input: a.input, Output: a.output, CacheRead: a.cacheRead,
			CacheWrite: a.cacheWrite, Reasoning: a.reasoning, Total: a.total,
			CostUSD: a.costUSD, Events: a.events, LatencyCnt: a.latencyCount,
		}
		if a.latencyCount > 0 {
			row.LatencyAvg = float64(a.latencySum) / float64(a.latencyCount)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// StackedSeries returns (day, dimension value) cells for a stacked chart. Only days in
// the range are produced, and only the top `top` values are kept per day.
func (d *DB) StackedSeries(ctx context.Context, q RangeQuery, loc *time.Location, dim string, top int) ([]StackedPoint, error) {
	if top <= 0 {
		top = 6
	}
	src := d.pickSource(q, loc, dim)
	col, err := dimExpr(dim, src.isRoll)
	if err != nil {
		return nil, err
	}
	dayCol := dayExpr(src.isRoll)
	query := fmt.Sprintf(
		"SELECT %s, %s, SUM(total_tokens), SUM(COALESCE(cost_usd,0)), SUM(%s) %s%s GROUP BY 1,2",
		dayCol, col, eventCountExpr(src.isRoll), src.from, src.where)
	rows, err := d.r.QueryContext(ctx, query, src.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Keep the top-N keys by total across the whole range, then drop the rest so the
	// stacked chart has a stable legend instead of a different key set each day.
	cells := []StackedPoint{}
	perKey := map[string]int64{}
	for rows.Next() {
		var day, key string
		var total int64
		var cost float64
		var events int64
		if err := rows.Scan(&day, &key, &total, &cost, &events); err != nil {
			return nil, err
		}
		cells = append(cells, StackedPoint{Day: day, Key: key, Total: total, CostUSD: cost, Events: events})
		perKey[key] += total
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	allowed := topKeys(perKey, top)
	out := make([]StackedPoint, 0, len(cells))
	for _, c := range cells {
		if _, ok := allowed[c.Key]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func eventCountExpr(isRollup bool) string {
	if isRollup {
		return "event_count"
	}
	return "1"
}

func topKeys(counts map[string]int64, top int) map[string]struct{} {
	type kv struct {
		key string
		val int64
	}
	list := make([]kv, 0, len(counts))
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	// Simple selection of the largest `top` values; the caller caps `top` at small
	// numbers, so an O(n*top) scan is cheaper than a full sort.
	for i := 0; i < top && i < len(list); i++ {
		best := i
		for j := i + 1; j < len(list); j++ {
			if list[j].val > list[best].val {
				best = j
			}
		}
		list[i], list[best] = list[best], list[i]
	}
	out := map[string]struct{}{}
	for i := 0; i < top && i < len(list); i++ {
		out[list[i].key] = struct{}{}
	}
	return out
}

// Heatmap returns per-day totals across a window, read from the daily rollup.
func (d *DB) Heatmap(ctx context.Context, fromMs, toMs int64, loc *time.Location) ([]HeatCell, error) {
	dr := resolveDayRange(RangeQuery{FromMs: fromMs, ToMs: toMs}, loc)
	query := `SELECT day, SUM(total_tokens), SUM(cost_usd), SUM(event_count)
	          FROM rollup_daily_hm WHERE day >= ? AND day <= ?
	          GROUP BY day ORDER BY day`
	rows, err := d.r.QueryContext(ctx, query, dr.fromDay, dr.toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HeatCell{}
	for rows.Next() {
		var c HeatCell
		if err := rows.Scan(&c.Day, &c.Total, &c.CostUSD, &c.Events); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatencySamples returns events that carry timing, newest first. Only OpenCode and omp
// record timings, so callers must show coverage rather than treating missing rows as 0.
func (d *DB) LatencySamples(ctx context.Context, q RangeQuery, limit int) ([]LatencyPoint, error) {
	if limit <= 0 {
		limit = 2000
	}
	p := eventsWhere(q)
	p.add("latency_ms IS NOT NULL")
	query := fmt.Sprintf(`SELECT ts, COALESCE(latency_ms,0), COALESCE(ttft_ms,0), model, harness,
	       total_tokens, input_tokens, output_tokens, project, session_id
	       FROM usage_events%s ORDER BY ts DESC LIMIT %d`, p.where(), limit)
	rows, err := d.r.QueryContext(ctx, query, p.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LatencyPoint{}
	for rows.Next() {
		var pt LatencyPoint
		if err := rows.Scan(&pt.TS, &pt.LatencyMs, &pt.TTFTMs, &pt.Model, &pt.Harness,
			&pt.Total, &pt.Input, &pt.Output, &pt.Project, &pt.SessionID); err != nil {
			return nil, err
		}
		out = append(out, pt)
	}
	return out, rows.Err()
}

const eventColumns = `id,harness,ts,local_day,project,session_id,model,provider,agent_type,agent_name,
  outcome,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,
  total_tokens,cost_usd,cost_source,latency_ms,ttft_ms`

func scanEvent(rows *sql.Rows) (EventRow, error) {
	var e EventRow
	err := rows.Scan(&e.ID, &e.Harness, &e.TS, &e.LocalDay, &e.Project, &e.SessionID, &e.Model,
		&e.Provider, &e.AgentType, &e.AgentName, &e.Outcome, &e.Input, &e.Output, &e.CacheRead,
		&e.CacheWrite, &e.Reasoning, &e.Total, &e.CostUSD, &e.CostSource, &e.LatencyMs, &e.TTFTMs)
	return e, err
}

// Events returns a page of raw events, newest first, with the total match count.
func (d *DB) Events(ctx context.Context, q RangeQuery, offset, limit int) (EventPage, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	p := eventsWhere(q)

	var total int64
	if err := d.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_events"+p.where(), p.args...).Scan(&total); err != nil {
		return EventPage{}, err
	}

	query := fmt.Sprintf("SELECT %s FROM usage_events%s ORDER BY ts DESC, id DESC LIMIT %d OFFSET %d",
		eventColumns, p.where(), limit, offset)
	rows, err := d.r.QueryContext(ctx, query, p.args...)
	if err != nil {
		return EventPage{}, err
	}
	defer rows.Close()

	out := EventPage{Rows: []EventRow{}, Total: total}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return EventPage{}, err
		}
		out.Rows = append(out.Rows, e)
	}
	return out, rows.Err()
}

// ExportEvents returns up to `max` events for a client-rendered export.
func (d *DB) ExportEvents(ctx context.Context, q RangeQuery, max int) ([]EventRow, bool, error) {
	if max <= 0 {
		max = 200000
	}
	p := eventsWhere(q)
	query := fmt.Sprintf("SELECT %s FROM usage_events%s ORDER BY ts DESC, id DESC LIMIT %d",
		eventColumns, p.where(), max+1)
	rows, err := d.r.QueryContext(ctx, query, p.args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []EventRow{}
	truncated := false
	for rows.Next() {
		if len(out) == max {
			truncated = true
			break
		}
		e, err := scanEvent(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}

// Sessions returns a page of session rollups. The range filter selects sessions active
// inside it (started before the end, updated after the start); the token and cost
// columns are lifetime sums for the session.
func (d *DB) Sessions(ctx context.Context, q RangeQuery, offset, limit int, sortBy string) (SessionPage, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var p pred
	if q.ToMs > 0 {
		p.add("started_at < ?", q.ToMs)
	}
	if q.FromMs > 0 {
		p.add("updated_at >= ?", q.FromMs)
	}
	p.in("harness", q.Harness)
	p.in("model", q.Models)
	p.in("project", q.Projects)
	p.in("agent_type", q.AgentTypes)

	orderBy := "updated_at DESC"
	switch sortBy {
	case "total":
		orderBy = "total_tokens DESC"
	case "cost":
		orderBy = "cost_usd DESC"
	case "events":
		orderBy = "event_count DESC"
	case "started":
		orderBy = "started_at DESC"
	}

	var total int64
	if err := d.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM rollup_session"+p.where(), p.args...).Scan(&total); err != nil {
		return SessionPage{}, err
	}

	query := fmt.Sprintf(`SELECT harness, session_id, project, model, agent_type, agent_name,
	    started_at, updated_at, event_count, input_tokens, output_tokens, cache_read_tokens,
	    cache_write_tokens, reasoning_tokens, total_tokens, cost_usd, latency_sum_ms, latency_count
	    FROM rollup_session%s ORDER BY %s LIMIT %d OFFSET %d`, p.where(), orderBy, limit, offset)
	rows, err := d.r.QueryContext(ctx, query, p.args...)
	if err != nil {
		return SessionPage{}, err
	}
	defer rows.Close()

	out := SessionPage{Rows: []SessionRow{}, Total: total}
	for rows.Next() {
		var s SessionRow
		var latencySum int64
		if err := rows.Scan(&s.Harness, &s.SessionID, &s.Project, &s.Model, &s.AgentType, &s.AgentName,
			&s.StartedAt, &s.UpdatedAt, &s.Events, &s.Input, &s.Output, &s.CacheRead, &s.CacheWrite,
			&s.Reasoning, &s.Total, &s.CostUSD, &latencySum, &s.LatencyCnt); err != nil {
			return SessionPage{}, err
		}
		if s.LatencyCnt > 0 {
			s.LatencyAvg = float64(latencySum) / float64(s.LatencyCnt)
		}
		out.Rows = append(out.Rows, s)
	}
	return out, rows.Err()
}

// FacetValues lists the distinct values of a dimension with their totals, for filter
// dropdowns. Served from the rollup that carries the dimension; outcome and cost source are
// not stored in any rollup, so those read the event rows.
func (d *DB) FacetValues(ctx context.Context, dim string) ([]FacetValue, error) {
	// Project lives in the harness×project rollup; everything else the rollups carry lives in
	// the harness×model one.
	table := "rollup_daily_hm"
	switch dim {
	case DimOutcome, DimCostSource:
		table = ""
	case DimProject:
		table = "rollup_daily_hp"
	}

	col, err := dimExpr(dim, table != "")
	if err != nil {
		return nil, err
	}

	var query string
	if table == "" {
		query = fmt.Sprintf(`SELECT %s, COUNT(*), SUM(total_tokens) FROM usage_events
		                     GROUP BY 1 ORDER BY 2 DESC`, col)
	} else {
		query = fmt.Sprintf(`SELECT %s, SUM(event_count), SUM(total_tokens) FROM %s
		                     GROUP BY 1 ORDER BY 2 DESC`, col, table)
	}
	rows, err := d.r.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FacetValue{}
	for rows.Next() {
		var v FacetValue
		if err := rows.Scan(&v.Value, &v.Events, &v.Total); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Realtime builds the realtime snapshot for the last `minutes` minutes.
func (d *DB) Realtime(ctx context.Context, minutes int, loc *time.Location) (RealtimeSnapshot, error) {
	if minutes <= 0 {
		minutes = 60
	}
	now := time.Now().UnixMilli()
	from := now - int64(minutes)*60_000
	out := RealtimeSnapshot{WindowMinutes: int64(minutes)}

	var err error
	if out.Buckets, err = d.HourlySeries(ctx, RangeQuery{FromMs: from}, minutes); err != nil {
		return out, err
	}
	if out.Totals, err = d.Totals(ctx, RangeQuery{FromMs: from}, loc); err != nil {
		return out, err
	}
	if out.ByModel, err = d.Breakdown(ctx, RangeQuery{FromMs: from}, loc, DimModel, 5); err != nil {
		return out, err
	}
	if out.Latency, err = d.LatencySamples(ctx, RangeQuery{FromMs: from}, 500); err != nil {
		return out, err
	}
	active, err := d.Sessions(ctx, RangeQuery{FromMs: now - 5*60_000}, 0, 20, "updated")
	if err != nil {
		return out, err
	}
	out.ActiveSession = active.Rows
	return out, nil
}

// LatestActivityMs is the newest moment any harness recorded activity: the later of the
// last session update and the last usage event. It is the sleep controller's "file/DB just
// changed" signal, deliberately independent of whether the write produced a new event.
func (d *DB) LatestActivityMs(ctx context.Context) (int64, error) {
	var sessions, events int64
	if err := d.r.QueryRowContext(ctx, `SELECT COALESCE(MAX(updated_at), 0) FROM sessions`).Scan(&sessions); err != nil {
		return 0, err
	}
	if err := d.r.QueryRowContext(ctx, `SELECT COALESCE(MAX(ts), 0) FROM usage_events`).Scan(&events); err != nil {
		return 0, err
	}
	if events > sessions {
		return events, nil
	}
	return sessions, nil
}

// ActiveSessionCount counts sessions whose last update falls inside the window, which is
// the sleep controller's view of "how many agents are still working".
func (d *DB) ActiveSessionCount(ctx context.Context, sinceMs int64) (int64, error) {
	var n int64
	err := d.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE updated_at >= ?`, sinceMs).Scan(&n)
	return n, err
}

// UnpricedModels lists models that have usage but no price, so the UI can offer to add
// a rate instead of silently showing nothing.
func (d *DB) UnpricedModels(ctx context.Context) ([]string, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT model FROM usage_events WHERE cost_source = 'unavailable'
		 GROUP BY model ORDER BY SUM(total_tokens) DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Stats summarises the local database for the settings page.
func (d *DB) Stats(ctx context.Context) (Stats, error) {
	s := Stats{DBPath: d.dbPath, DBBytes: d.FileSize()}
	if err := d.r.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MIN(ts),0), COALESCE(MAX(ts),0) FROM usage_events`).
		Scan(&s.Events, &s.FirstTS, &s.LastTS); err != nil {
		return s, err
	}
	if err := d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&s.Sessions); err != nil {
		return s, err
	}
	if err := d.r.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT model) FROM usage_events WHERE cost_source='unavailable'`).
		Scan(&s.Unpriced); err != nil {
		return s, err
	}
	if err := d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_runs`).Scan(&s.SyncRuns); err != nil {
		return s, err
	}
	return s, nil
}

// HarnessTotals aggregates one harness for the report view.
func (d *DB) HarnessTotals(ctx context.Context, harness string) (Totals, error) {
	row := d.r.QueryRowContext(ctx, "SELECT "+eventsAggCols+" FROM usage_events WHERE harness = ?", harness)
	a, err := scanAgg(row)
	if err != nil {
		return Totals{}, err
	}
	return a.totals(), nil
}

// HarnessBounds returns the first and last event timestamps for a harness.
func (d *DB) HarnessBounds(ctx context.Context, harness string) (int64, int64, error) {
	var first, last sql.NullInt64
	err := d.r.QueryRowContext(ctx,
		`SELECT MIN(ts), MAX(ts) FROM usage_events WHERE harness = ?`, harness).Scan(&first, &last)
	if err != nil {
		return 0, 0, err
	}
	return first.Int64, last.Int64, nil
}

// HarnessEventCount counts the events recorded for a harness.
func (d *DB) HarnessEventCount(ctx context.Context, harness string) (int64, error) {
	var n int64
	err := d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events WHERE harness = ?`, harness).Scan(&n)
	return n, err
}
