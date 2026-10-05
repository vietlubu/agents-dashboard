package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func sampleEvent(key string, ts int64, total int64) Event {
	return Event{
		EventKey: key, Harness: "claude", SourceFile: "/tmp/a.jsonl",
		SessionID: "sess-1", Project: "/proj", Model: "claude-opus-4-8",
		AgentType: "main", Outcome: "ok", TS: ts,
		Input: total, Total: total,
	}
}

// A later, smaller record for the same key must not shrink the row or move its time:
// streaming duplicates collapse onto the completed record.
func TestInsertEventsKeepsLargestAndEarliest(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC
	first := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).UnixMilli()
	later := first + 5000

	// Streaming writes a partial record first, then the completed record for the same id.
	partial := sampleEvent("claude|m1|r1", first, 100)
	complete := sampleEvent("claude|m1|r1", later, 900)

	if _, _, err := db.InsertEvents(ctx, []Event{partial}, loc); err != nil {
		t.Fatalf("insert partial: %v", err)
	}
	if _, _, err := db.InsertEvents(ctx, []Event{complete}, loc); err != nil {
		t.Fatalf("insert complete: %v", err)
	}
	// Re-inserting an older, smaller record must not move the timestamp back or shrink it.
	if _, _, err := db.InsertEvents(ctx, []Event{partial}, loc); err != nil {
		t.Fatalf("re-insert partial: %v", err)
	}

	page, err := db.Events(ctx, RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(page.Rows))
	}
	got := page.Rows[0]
	if got.Total != 900 {
		t.Errorf("total = %d, want 900 (largest wins)", got.Total)
	}
	if got.TS != first {
		t.Errorf("ts = %d, want %d (earliest wins)", got.TS, first)
	}
	if page.Total != 1 {
		t.Errorf("count = %d, want 1", page.Total)
	}
}

func TestInsertEventsMergesLatencyWithoutOverwriting(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := int64(1_700_000_000_000)

	e := sampleEvent("k1", ts, 10)
	e.LatencyMs = new(int64(250))
	if _, _, err := db.InsertEvents(ctx, []Event{e}, time.UTC); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	e2 := sampleEvent("k1", ts, 20) // no latency recorded this time
	if _, _, err := db.InsertEvents(ctx, []Event{e2}, time.UTC); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	page, err := db.Events(ctx, RangeQuery{}, 0, 1)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	row := page.Rows[0]
	if row.Total != 20 {
		t.Errorf("total = %d, want 20", row.Total)
	}
	if row.LatencyMs == nil || *row.LatencyMs != 250 {
		t.Errorf("latency = %v, want 250 preserved", row.LatencyMs)
	}
}

func TestInsertEventsReportsDirtyDaysAndSessions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.FixedZone("UTC+7", 7*3600)

	// 2026-03-01T20:00:00Z is 2026-03-02T03:00 local.
	ts := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC).UnixMilli()
	dirty, inserted, err := db.InsertEvents(ctx, []Event{sampleEvent("k1", ts, 5)}, loc)
	if err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if inserted != 1 {
		t.Errorf("inserted = %d, want 1", inserted)
	}
	if _, ok := dirty.Days["2026-03-02"]; !ok {
		t.Errorf("dirty days = %v, want the local day 2026-03-02", dirty.Days)
	}
	if _, ok := dirty.Sessions[[2]string{"claude", "sess-1"}]; !ok {
		t.Errorf("dirty sessions = %v, want claude/sess-1", dirty.Sessions)
	}
}

func TestDeleteEventsBySourceRemovesOnlyThatFile(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	a := sampleEvent("a1", 1_700_000_000_000, 10)
	a.SourceFile = "/tmp/a.jsonl"
	b := sampleEvent("b1", 1_700_000_000_000, 20)
	b.SourceFile = "/tmp/b.jsonl"
	if _, _, err := db.InsertEvents(ctx, []Event{a, b}, time.UTC); err != nil {
		t.Fatalf("insert: %v", err)
	}

	dirty, err := db.DeleteEventsBySource(ctx, "claude", "/tmp/a.jsonl")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(dirty.Days) != 1 {
		t.Errorf("dirty days = %v", dirty.Days)
	}
	page, err := db.Events(ctx, RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Total != 20 {
		t.Fatalf("remaining rows = %+v, want only the b event", page.Rows)
	}
}

// Rollups must be exactly the event table, grouped: this is the invariant every UI
// number depends on.
func TestRollupsMatchEventTotals(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC

	events := []Event{
		{EventKey: "1", Harness: "claude", SessionID: "s1", Project: "/p1", Model: "m1", TS: 1_700_000_000_000, Input: 10, Output: 5, CacheRead: 1, CacheWrite: 2, Total: 18, CostUSD: new(0.5), CostSource: "estimated", LatencyMs: new(int64(100))},
		{EventKey: "2", Harness: "claude", SessionID: "s1", Project: "/p1", Model: "m1", TS: 1_700_000_060_000, Input: 20, Output: 10, Total: 30, CostUSD: new(1.0), CostSource: "reported"},
		{EventKey: "3", Harness: "omp", SessionID: "s2", Project: "/p2", Model: "m2", TS: 1_700_100_000_000, Input: 7, Total: 7, CostSource: "unavailable"},
	}
	dirty, _, err := db.InsertEvents(ctx, events, loc)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.RefreshRollups(ctx, dirty); err != nil {
		t.Fatalf("RefreshRollups: %v", err)
	}

	var eventTotal, rollupTotal, sessionTotal int64
	if err := db.Reader().QueryRowContext(ctx, `SELECT SUM(total_tokens) FROM usage_events`).Scan(&eventTotal); err != nil {
		t.Fatalf("event total: %v", err)
	}
	if err := db.Reader().QueryRowContext(ctx, `SELECT SUM(total_tokens) FROM rollup_daily_hm`).Scan(&rollupTotal); err != nil {
		t.Fatalf("rollup total: %v", err)
	}
	if err := db.Reader().QueryRowContext(ctx, `SELECT SUM(total_tokens) FROM rollup_session`).Scan(&sessionTotal); err != nil {
		t.Fatalf("session rollup total: %v", err)
	}
	if eventTotal != 55 {
		t.Fatalf("event total = %d, want 55", eventTotal)
	}
	if rollupTotal != eventTotal {
		t.Errorf("rollup_daily_hm total = %d, want %d", rollupTotal, eventTotal)
	}
	if sessionTotal != eventTotal {
		t.Errorf("rollup_session total = %d, want %d", sessionTotal, eventTotal)
	}

	// The project rollup must also agree, and the reported/estimated split must be kept.
	var projectTotal, cost, reported int64
	var costF, reportedF float64
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT SUM(total_tokens) FROM rollup_daily_hp`).Scan(&projectTotal); err != nil {
		t.Fatalf("project total: %v", err)
	}
	if projectTotal != eventTotal {
		t.Errorf("rollup_daily_hp total = %d, want %d", projectTotal, eventTotal)
	}
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT SUM(cost_usd), SUM(reported_cost_usd) FROM rollup_daily_hm`).Scan(&costF, &reportedF); err != nil {
		t.Fatalf("cost split: %v", err)
	}
	cost, reported = int64(costF*100), int64(reportedF*100)
	if cost != 150 {
		t.Errorf("cost = %v, want 1.50 summed", costF)
	}
	if reported != 100 {
		t.Errorf("reported cost = %v, want 1.00 (only the reported event)", reportedF)
	}

	// Totals() must equal the same numbers when read through the rollup path.
	totals, err := db.Totals(ctx, RangeQuery{}, loc)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals.Total != eventTotal {
		t.Errorf("Totals().Total = %d, want %d", totals.Total, eventTotal)
	}
	if totals.LatencyCount != 1 {
		t.Errorf("LatencyCount = %d, want 1", totals.LatencyCount)
	}
	if totals.CostUnavailable != 1 {
		t.Errorf("CostUnavailable = %d, want 1", totals.CostUnavailable)
	}
}

// A partial-day range cannot be answered by a day-keyed rollup, so the read path must
// fall back to the event rows.
func TestTotalsFallsBackForPartialRange(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC

	// 09:00 and 11:00 on the same day.
	base := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	e1 := sampleEvent("k1", base.UnixMilli(), 100)
	e2 := sampleEvent("k2", base.Add(2*time.Hour).UnixMilli(), 200)
	dirty, _, err := db.InsertEvents(ctx, []Event{e1, e2}, loc)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.RefreshRollups(ctx, dirty); err != nil {
		t.Fatalf("rollups: %v", err)
	}

	whole, err := db.Totals(ctx, RangeQuery{}, loc)
	if err != nil {
		t.Fatalf("whole: %v", err)
	}
	if whole.Total != 300 {
		t.Fatalf("whole-day total = %d, want 300", whole.Total)
	}

	// 09:30 .. 12:00 must see only the 11:00 event.
	partial, err := db.Totals(ctx, RangeQuery{
		FromMs: base.Add(30 * time.Minute).UnixMilli(),
		ToMs:   base.Add(3 * time.Hour).UnixMilli(),
	}, loc)
	if err != nil {
		t.Fatalf("partial: %v", err)
	}
	if partial.Total != 200 {
		t.Errorf("partial total = %d, want 200 (day rollups must not be used)", partial.Total)
	}
	if partial.Events != 1 {
		t.Errorf("partial events = %d, want 1", partial.Events)
	}
}

func TestBreakdownAndStackedSeriesByModel(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC

	day1 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	day2 := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC).UnixMilli()
	events := []Event{
		{EventKey: "a", Harness: "claude", SessionID: "s1", Project: "/p", Model: "big", TS: day1, Total: 100, Input: 100},
		{EventKey: "b", Harness: "claude", SessionID: "s1", Project: "/p", Model: "small", TS: day1, Total: 10, Input: 10},
		{EventKey: "c", Harness: "codex", SessionID: "s2", Project: "/p", Model: "big", TS: day2, Total: 50, Input: 50},
	}
	dirty, _, err := db.InsertEvents(ctx, events, loc)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.RefreshRollups(ctx, dirty); err != nil {
		t.Fatalf("rollups: %v", err)
	}

	rows, err := db.Breakdown(ctx, RangeQuery{}, loc, DimModel, 10)
	if err != nil {
		t.Fatalf("Breakdown: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d breakdown rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Key != "big" || rows[0].Total != 150 {
		t.Errorf("top row = %+v, want big/150", rows[0])
	}

	series, err := db.DailySeries(ctx, RangeQuery{}, loc)
	if err != nil {
		t.Fatalf("DailySeries: %v", err)
	}
	if len(series) != 2 || series[0].Key != "2026-06-01" || series[0].Total != 110 || series[1].Total != 50 {
		t.Errorf("series = %+v", series)
	}

	stacked, err := db.StackedSeries(ctx, RangeQuery{}, loc, DimModel, 1)
	if err != nil {
		t.Fatalf("StackedSeries: %v", err)
	}
	for _, c := range stacked {
		if c.Key != "big" {
			t.Errorf("stacked kept a non-top key: %+v", c)
		}
	}
	if len(stacked) != 2 {
		t.Errorf("stacked cells = %d, want 2 (one per day for the top key)", len(stacked))
	}
}

func TestTotalsWithModelAndProjectFilterUsesEventRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC

	events := []Event{
		{EventKey: "a", Harness: "claude", SessionID: "s1", Project: "/keep", Model: "m", TS: 1_700_000_000_000, Total: 100},
		{EventKey: "b", Harness: "claude", SessionID: "s1", Project: "/drop", Model: "m", TS: 1_700_000_000_000, Total: 900},
	}
	dirty, _, err := db.InsertEvents(ctx, events, loc)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.RefreshRollups(ctx, dirty); err != nil {
		t.Fatalf("rollups: %v", err)
	}

	// Neither rollup table holds both model and project, so this must read events.
	totals, err := db.Totals(ctx, RangeQuery{Models: []string{"m"}, Projects: []string{"/keep"}}, loc)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals.Total != 100 {
		t.Errorf("total = %d, want 100", totals.Total)
	}
}

func TestRecomputeLocalDaysAfterTimezoneChange(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ts := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC).UnixMilli()
	if _, _, err := db.InsertEvents(ctx, []Event{sampleEvent("k", ts, 5)}, time.UTC); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.RecomputeLocalDays(ctx, time.FixedZone("UTC+7", 7*3600), nil); err != nil {
		t.Fatalf("RecomputeLocalDays: %v", err)
	}
	var day string
	if err := db.Reader().QueryRowContext(ctx, `SELECT local_day FROM usage_events`).Scan(&day); err != nil {
		t.Fatalf("read day: %v", err)
	}
	if day != "2026-03-02" {
		t.Errorf("local_day = %q, want 2026-03-02", day)
	}
}

// Outcome and cost source are not stored in the rollups, so those breakdowns must read the
// event rows; asking a rollup for them used to fail at query time.
func TestBreakdownByOutcomeUsesEventRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC

	events := []Event{
		{EventKey: "a", Harness: "claude", SessionID: "s1", Project: "/p", Model: "m", TS: 1_700_000_000_000, Input: 10, Total: 10, Outcome: "ok"},
		{EventKey: "b", Harness: "claude", SessionID: "s1", Project: "/p", Model: "m", TS: 1_700_000_000_000, Input: 5, Total: 5, Outcome: "tool_use"},
		{EventKey: "c", Harness: "claude", SessionID: "s1", Project: "/p", Model: "m", TS: 1_700_000_000_000, Input: 3, Total: 3, Outcome: "tool_use"},
	}
	dirty, _, err := db.InsertEvents(ctx, events, loc)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.RefreshRollups(ctx, dirty); err != nil {
		t.Fatalf("rollups: %v", err)
	}

	rows, err := db.Breakdown(ctx, RangeQuery{}, loc, DimOutcome, 10)
	if err != nil {
		t.Fatalf("Breakdown(outcome): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want one per outcome", rows)
	}
	// Rows are ordered by total, so `ok` (10 tokens in one event) leads `tool_use` (8 across two).
	if rows[0].Key != "ok" || rows[0].Events != 1 {
		t.Errorf("top row = %+v, want ok with 1 event", rows[0])
	}
	if rows[1].Key != "tool_use" || rows[1].Events != 2 || rows[1].Total != 8 {
		t.Errorf("second row = %+v, want tool_use with 2 events and 8 tokens", rows[1])
	}

	// The same guard applies to a breakdown by cost source.
	if _, err := db.Breakdown(ctx, RangeQuery{}, loc, DimCostSource, 10); err != nil {
		t.Fatalf("Breakdown(costSource): %v", err)
	}

	// A model breakdown must still use the rollup path (no error, correct totals).
	byModel, err := db.Breakdown(ctx, RangeQuery{}, loc, DimModel, 10)
	if err != nil {
		t.Fatalf("Breakdown(model): %v", err)
	}
	if len(byModel) != 1 || byModel[0].Total != 18 {
		t.Errorf("byModel = %+v", byModel)
	}
}

// Every facet dimension must resolve to a source that actually carries it. Two failures here
// left the filter menus empty: a rollup queried for a column it does not have, and an
// inverted rollup flag.
func TestFacetValuesForEveryDimension(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC

	events := []Event{
		{EventKey: "a", Harness: "claude", SessionID: "s1", Project: "/p1", Model: "m1", TS: 1_700_000_000_000, Input: 10, Total: 10, Outcome: "ok", CostSource: CostSourceReported},
		{EventKey: "b", Harness: "omp", SessionID: "s2", Project: "/p2", Model: "m2", TS: 1_700_000_000_000, Input: 20, Total: 20, Outcome: "tool_use", CostSource: CostSourceUnavailable},
	}
	dirty, _, err := db.InsertEvents(ctx, events, loc)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.RefreshRollups(ctx, dirty); err != nil {
		t.Fatalf("rollups: %v", err)
	}

	cases := []struct {
		dim  string
		want int
	}{
		{DimHarness, 2},
		{DimModel, 2},
		{DimProject, 2},
		{DimAgentType, 1},
		{DimOutcome, 2},
		{DimCostSource, 2},
	}
	for _, tc := range cases {
		values, err := db.FacetValues(ctx, tc.dim)
		if err != nil {
			t.Errorf("FacetValues(%s): %v", tc.dim, err)
			continue
		}
		if len(values) != tc.want {
			t.Errorf("FacetValues(%s) = %+v, want %d values", tc.dim, values, tc.want)
		}
	}
	if _, err := db.FacetValues(ctx, "nope"); err == nil {
		t.Error("FacetValues accepted an unknown dimension")
	}
}

func TestEventsPagingAndCount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	var events []Event
	for i := range 25 {
		e := sampleEvent(string(rune('a'+i%26))+string(rune('0'+i/26)), 1_700_000_000_000+int64(i)*1000, int64(i+1))
		e.Model = "m"
		events = append(events, e)
	}
	if _, _, err := db.InsertEvents(ctx, events, time.UTC); err != nil {
		t.Fatalf("insert: %v", err)
	}

	page, err := db.Events(ctx, RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if page.Total != 25 {
		t.Errorf("total = %d, want 25", page.Total)
	}
	if len(page.Rows) != 10 {
		t.Fatalf("rows = %d, want 10", len(page.Rows))
	}
	// Newest first.
	if page.Rows[0].TS <= page.Rows[9].TS {
		t.Errorf("rows are not newest-first: %d then %d", page.Rows[0].TS, page.Rows[9].TS)
	}

	page2, err := db.Events(ctx, RangeQuery{}, 20, 10)
	if err != nil {
		t.Fatalf("Events page 2: %v", err)
	}
	if len(page2.Rows) != 5 {
		t.Errorf("second page rows = %d, want 5", len(page2.Rows))
	}
}

func TestExportEventsTruncation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var events []Event
	for i := range 5 {
		e := sampleEvent(string(rune('a'+i)), 1_700_000_000_000+int64(i), int64(i+1))
		events = append(events, e)
	}
	if _, _, err := db.InsertEvents(ctx, events, time.UTC); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, truncated, err := db.ExportEvents(ctx, RangeQuery{}, 3)
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3", len(rows))
	}
	if !truncated {
		t.Error("truncated = false, want true when the cap is hit")
	}
}

func TestSessionsSortAndRangeFilter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.UTC

	old := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	recent := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	events := []Event{
		{EventKey: "o1", Harness: "pi", SessionID: "old", Project: "/p", Model: "m", TS: old, Total: 10_000},
		{EventKey: "r1", Harness: "pi", SessionID: "new", Project: "/p", Model: "m", TS: recent, Total: 5},
	}
	dirty, _, err := db.InsertEvents(ctx, events, loc)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.UpdateEventCosts(ctx, map[int64]*float64{}); err != nil {
		t.Fatalf("noop: %v", err)
	}
	if err := db.RefreshRollups(ctx, dirty); err != nil {
		t.Fatalf("rollups: %v", err)
	}

	all, err := db.Sessions(ctx, RangeQuery{}, 0, 10, "total")
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if all.Total != 2 || len(all.Rows) != 2 {
		t.Fatalf("sessions = %+v", all)
	}
	if all.Rows[0].SessionID != "old" {
		t.Errorf("sort by total put %q first", all.Rows[0].SessionID)
	}

	// A range covering only June must select just the recent session.
	recentOnly, err := db.Sessions(ctx, RangeQuery{
		FromMs: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
		ToMs:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
	}, 0, 10, "updated")
	if err != nil {
		t.Fatalf("Sessions range: %v", err)
	}
	if len(recentOnly.Rows) != 1 || recentOnly.Rows[0].SessionID != "new" {
		t.Errorf("range filter rows = %+v", recentOnly.Rows)
	}
}

func TestScanStateRoundTripAndPrune(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	states := []ScanState{
		{Key: "file:/tmp/exists.jsonl", Harness: "pi", Kind: "file", Mtime: 5, Size: 100, Inode: 7, Offset: 90, ParserVersion: 1},
		{Key: "sql:omp:/tmp/x.db", Harness: "omp", Kind: "sqlite", Watermark: 42, ParserVersion: 1},
	}
	if err := db.SaveScanStates(ctx, states); err != nil {
		t.Fatalf("SaveScanStates: %v", err)
	}
	loaded, err := db.LoadScanStates(ctx, "pi")
	if err != nil {
		t.Fatalf("LoadScanStates: %v", err)
	}
	got, ok := loaded["file:/tmp/exists.jsonl"]
	if !ok {
		t.Fatalf("state missing: %+v", loaded)
	}
	if got.Offset != 90 || got.Inode != 7 || !got.Unchanged(7, 5, 100, 1) {
		t.Errorf("state round-trip mismatch: %+v", got)
	}

	// A missing file's cursor is dropped; the sqlite cursor is left alone.
	if _, err := db.PruneScanStates(ctx, "pi"); err != nil {
		t.Fatalf("PruneScanStates: %v", err)
	}
	remaining, err := db.LoadScanStates(ctx, "pi")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := remaining["file:/tmp/exists.jsonl"]; ok {
		t.Error("missing file cursor was not pruned")
	}
	ompStates, err := db.LoadScanStates(ctx, "omp")
	if err != nil {
		t.Fatalf("omp states: %v", err)
	}
	if _, ok := ompStates["sql:omp:/tmp/x.db"]; !ok {
		t.Error("sqlite cursor must survive pruning")
	}
}

func TestClearHarnessRemovesEverythingForOneHarness(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	events := []Event{
		sampleEvent("c1", 1_700_000_000_000, 10),
		{EventKey: "p1", Harness: "pi", SessionID: "s2", Project: "/p", Model: "m", TS: 1_700_000_000_000, Total: 20},
	}
	dirty, _, err := db.InsertEvents(ctx, events, time.UTC)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.UpsertSessions(ctx, []Session{
		{Harness: "claude", SessionID: "sess-1", Project: "/proj"},
		{Harness: "pi", SessionID: "s2", Project: "/p"},
	}); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if err := db.RefreshRollups(ctx, dirty); err != nil {
		t.Fatalf("rollups: %v", err)
	}

	if err := db.ClearHarness(ctx, "claude"); err != nil {
		t.Fatalf("ClearHarness: %v", err)
	}
	claude, err := db.HarnessTotals(ctx, "claude")
	if err != nil {
		t.Fatalf("HarnessTotals: %v", err)
	}
	if claude.Events != 0 {
		t.Errorf("claude events = %d, want 0", claude.Events)
	}
	pi, err := db.HarnessTotals(ctx, "pi")
	if err != nil {
		t.Fatalf("HarnessTotals pi: %v", err)
	}
	if pi.Events != 1 {
		t.Errorf("pi events = %d, want 1 (other harness untouched)", pi.Events)
	}
	ids, err := db.KnownSessionIDs(ctx, "claude")
	if err != nil {
		t.Fatalf("KnownSessionIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("claude sessions = %v, want none", ids)
	}
}

func TestUpsertSessionsKeepsEarliestStartAndLatestUpdate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.UpsertSessions(ctx, []Session{{
		Harness: "codex", SessionID: "s1", StartedAt: 2000, UpdatedAt: 3000, Project: "/p",
	}}); err != nil {
		t.Fatalf("first: %v", err)
	}
	// A later observation carries an older claimed start; the earliest must win.
	if err := db.UpsertSessions(ctx, []Session{{
		Harness: "codex", SessionID: "s1", StartedAt: 1000, UpdatedAt: 5000,
	}}); err != nil {
		t.Fatalf("second: %v", err)
	}
	s, ok, err := db.SessionDetail(ctx, "codex", "s1")
	if err != nil || !ok {
		t.Fatalf("SessionDetail: %v ok=%v", err, ok)
	}
	if s.StartedAt != 1000 {
		t.Errorf("started_at = %d, want 1000", s.StartedAt)
	}
	if s.UpdatedAt != 5000 {
		t.Errorf("updated_at = %d, want 5000", s.UpdatedAt)
	}
	if s.Project != "/p" {
		t.Errorf("project = %q, want /p (kept when the update omits it)", s.Project)
	}
}

func TestUpdateEventCostsDowngradesToUnavailable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	e := sampleEvent("k", 1_700_000_000_000, 100)
	e.CostUSD = new(1.25)
	e.CostSource = "estimated"
	if _, _, err := db.InsertEvents(ctx, []Event{e}, time.UTC); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var id int64
	if err := db.Reader().QueryRowContext(ctx, `SELECT id FROM usage_events`).Scan(&id); err != nil {
		t.Fatalf("id: %v", err)
	}

	batch, next, err := db.RepriceableEvents(ctx, 0, 10)
	if err != nil {
		t.Fatalf("RepriceableEvents: %v", err)
	}
	if len(batch) != 1 || next != id {
		t.Fatalf("batch = %+v next=%d", batch, next)
	}

	if err := db.UpdateEventCosts(ctx, map[int64]*float64{id: nil}); err != nil {
		t.Fatalf("UpdateEventCosts: %v", err)
	}
	page, err := db.Events(ctx, RangeQuery{}, 0, 1)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if page.Rows[0].CostUSD != nil {
		t.Errorf("cost = %v, want nil after downgrade", page.Rows[0].CostUSD)
	}
	if page.Rows[0].CostSource != "unavailable" {
		t.Errorf("cost source = %q, want unavailable", page.Rows[0].CostSource)
	}
}

func TestStatsAndSyncRunLog(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	id, err := db.StartSyncRun(ctx, "startup")
	if err != nil {
		t.Fatalf("StartSyncRun: %v", err)
	}
	if err := db.FinishSyncRun(ctx, id, 1_700_000_000_000, 3, 2, 1, 0, ""); err != nil {
		t.Fatalf("FinishSyncRun: %v", err)
	}
	runs, err := db.SyncHistory(ctx, 5)
	if err != nil {
		t.Fatalf("SyncHistory: %v", err)
	}
	if len(runs) != 1 || runs[0].FilesWalked != 3 || runs[0].FinishedAt == 0 {
		t.Fatalf("runs = %+v", runs)
	}
	if _, err := db.StartSyncRun(ctx, "scheduled"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if err := db.TrimSyncHistory(ctx, 1); err != nil {
		t.Fatalf("TrimSyncHistory: %v", err)
	}
	runs, err = db.SyncHistory(ctx, 5)
	if err != nil {
		t.Fatalf("SyncHistory 2: %v", err)
	}
	if len(runs) != 1 || runs[0].Trigger != "scheduled" {
		t.Errorf("trim kept %+v", runs)
	}

	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Events != 0 || stats.DBPath == "" || stats.DBPath == ":memory:" {
		t.Errorf("stats = %+v", stats)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, ok, err := db.GetSetting(ctx, SettingTZ); err != nil || ok {
		t.Fatalf("absent setting: ok=%v err=%v", ok, err)
	}
	if err := db.SetSetting(ctx, SettingTZ, "Asia/Ho_Chi_Minh"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	v, ok, err := db.GetSetting(ctx, SettingTZ)
	if err != nil || !ok || v != "Asia/Ho_Chi_Minh" {
		t.Fatalf("got %q ok=%v err=%v", v, ok, err)
	}
	if err := db.SetSetting(ctx, SettingIdleIntervalS, "90"); err != nil {
		t.Fatalf("set int: %v", err)
	}
	n, err := db.SettingInt(ctx, SettingIdleIntervalS, 60)
	if err != nil || n != 90 {
		t.Errorf("SettingInt = %d err=%v", n, err)
	}
	if err := db.SetSetting(ctx, SettingConcurrency, "not-a-number"); err != nil {
		t.Fatalf("set bad int: %v", err)
	}
	n, err = db.SettingInt(ctx, SettingConcurrency, 4)
	if err != nil || n != 4 {
		t.Errorf("fallback SettingInt = %d err=%v", n, err)
	}
	if err := db.SetSetting(ctx, SettingAutoSyncPrices, "true"); err != nil {
		t.Fatalf("set bool: %v", err)
	}
	b, err := db.SettingBool(ctx, SettingAutoSyncPrices, false)
	if err != nil || !b {
		t.Errorf("SettingBool = %v err=%v", b, err)
	}
	all, err := db.AllSettings(ctx)
	if err != nil || len(all) != 4 {
		t.Errorf("AllSettings = %v err=%v", all, err)
	}
}

func TestPriceTablesAndManualProtection(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.UpsertModelPrices(ctx, []ModelPrice{
		{ModelKey: "gpt-5.6-sol", InputPerM: 1.5, OutputPerM: 12, Source: "models-dev"},
	}); err != nil {
		t.Fatalf("sync insert: %v", err)
	}
	// A manual price wins over a later sync.
	if err := db.UpsertModelPrice(ctx, ModelPrice{
		ModelKey: "gpt-5.6-sol", InputPerM: 2.5, OutputPerM: 20, Source: "manual",
	}, true); err != nil {
		t.Fatalf("manual insert: %v", err)
	}
	if err := db.UpsertModelPrices(ctx, []ModelPrice{
		{ModelKey: "gpt-5.6-sol", InputPerM: 9, OutputPerM: 9, Source: "litellm"},
	}); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	prices, err := db.ModelPrices(ctx)
	if err != nil {
		t.Fatalf("ModelPrices: %v", err)
	}
	if len(prices) != 1 {
		t.Fatalf("prices = %+v", prices)
	}
	if prices[0].InputPerM != 2.5 || prices[0].Source != "manual" {
		t.Errorf("manual row was overwritten: %+v", prices[0])
	}

	if err := db.UpsertPriceRule(ctx, PriceRule{ModelKey: "gpt-5.6-sol", InputMult: 0.5, OutputMult: 1, CacheReadMult: 1, CacheWriteMult: 1}); err != nil {
		t.Fatalf("rule: %v", err)
	}
	if err := db.UpsertPriceRule(ctx, PriceRule{ModelKey: "gpt-5.6-sol", InputMult: 0.5, OutputMult: 1, CacheReadMult: 1, CacheWriteMult: 1, Disabled: true}); err != nil {
		t.Fatalf("rule update: %v", err)
	}
	rules, err := db.PriceRules(ctx)
	if err != nil {
		t.Fatalf("PriceRules: %v", err)
	}
	if len(rules) != 1 || !rules[0].Disabled {
		t.Errorf("rules = %+v", rules)
	}

	if err := db.DeleteModelPrice(ctx, "gpt-5.6-sol"); err != nil {
		t.Fatalf("DeleteModelPrice: %v", err)
	}
	prices, err = db.ModelPrices(ctx)
	if err != nil || len(prices) != 0 {
		t.Errorf("prices after delete = %+v err=%v", prices, err)
	}
}

func TestTruncateAllAndScanRoots(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.AddScanRoot(ctx, "claude", "/tmp/extra"); err != nil {
		t.Fatalf("AddScanRoot: %v", err)
	}
	if err := db.AddScanRoot(ctx, "claude", "/tmp/extra"); err != nil {
		t.Fatalf("AddScanRoot twice must be a no-op: %v", err)
	}
	roots, err := db.AllScanRoots(ctx)
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots = %+v err=%v", roots, err)
	}
	if roots[0].Exists {
		t.Errorf("/tmp/extra should not exist: %+v", roots[0])
	}
	if err := db.RemoveScanRoot(ctx, "claude", "/tmp/extra"); err != nil {
		t.Fatalf("RemoveScanRoot: %v", err)
	}
	roots, err = db.AllScanRoots(ctx)
	if err != nil || len(roots) != 0 {
		t.Errorf("roots after remove = %+v err=%v", roots, err)
	}

	if _, _, err := db.InsertEvents(ctx, []Event{sampleEvent("k", 1_700_000_000_000, 5)}, time.UTC); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.TruncateAll(ctx); err != nil {
		t.Fatalf("TruncateAll: %v", err)
	}
	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Events != 0 {
		t.Errorf("events = %d, want 0 after truncate", stats.Events)
	}
}

// A session row with no events behind it matches no date range, so the sync engine drops
// it; a session that has even one event must survive.
func TestPruneEmptySessionsKeepsSessionsWithEvents(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.UpsertSessions(ctx, []Session{
		{Harness: "claude", SessionID: "sess-1", Project: "/proj", StartedAt: 1, UpdatedAt: 2},
		{Harness: "claude", SessionID: "empty", SourceFile: "/tmp/empty.jsonl"},
		{Harness: "pi", SessionID: "sess-1", StartedAt: 3, UpdatedAt: 4},
	}); err != nil {
		t.Fatalf("UpsertSessions: %v", err)
	}
	if _, _, err := db.InsertEvents(ctx, []Event{sampleEvent("k1", 1_700_000_000_000, 5)}, time.UTC); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	pruned, err := db.PruneEmptySessions(ctx)
	if err != nil {
		t.Fatalf("PruneEmptySessions: %v", err)
	}
	if pruned != 2 {
		t.Errorf("pruned = %d, want the event-less claude and pi rows", pruned)
	}
	if _, ok, err := db.SessionDetail(ctx, "claude", "sess-1"); err != nil || !ok {
		t.Errorf("the session with an event was pruned: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := db.SessionDetail(ctx, "claude", "empty"); ok {
		t.Errorf("the event-less session survived")
	}
	// A session id that exists under another harness must not keep a row alive: the
	// match is scoped by harness.
	if _, ok, _ := db.SessionDetail(ctx, "pi", "sess-1"); ok {
		t.Errorf("pi/sess-1 has no events and should be gone")
	}
}

// The trend must be able to answer at several bucket sizes: hours for a window of a few
// hours (a day bucket per day would show nothing), weeks and months by folding the day
// rollup, and the coarser size automatically when an hourly chart would be unreadable.
func TestSeriesGranularities(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	loc := time.FixedZone("UTC+7", 7*3600)

	// Two events on one day (one hour apart), and one three weeks earlier.
	at := func(y int, m time.Month, d, h int) int64 {
		return time.Date(y, m, d, h, 30, 0, 0, loc).UnixMilli()
	}
	events := []Event{
		{EventKey: "1", Harness: "omp", SessionID: "s1", Project: "/p", Model: "m", TS: at(2026, 9, 30, 9), Total: 100, Input: 100, CostUSD: new(1.0), CostSource: "reported"},
		{EventKey: "2", Harness: "omp", SessionID: "s1", Project: "/p", Model: "m", TS: at(2026, 9, 30, 10), Total: 200, Input: 200, CostUSD: new(2.0), CostSource: "reported"},
		{EventKey: "3", Harness: "omp", SessionID: "s1", Project: "/p", Model: "m", TS: at(2026, 9, 9, 10), Total: 400, Input: 400, CostUSD: new(4.0), CostSource: "reported"},
	}
	if _, _, err := db.InsertEvents(ctx, events, loc); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := db.RebuildAllRollups(ctx); err != nil {
		t.Fatalf("RebuildAllRollups: %v", err)
	}

	whole := RangeQuery{FromMs: at(2026, 9, 1, 0), ToMs: at(2026, 10, 1, 0)}

	// Day buckets: three days only if the events were spread across days; here two.
	day, err := db.Series(ctx, whole, loc, GranDay)
	if err != nil {
		t.Fatalf("Series day: %v", err)
	}
	if day.Granularity != GranDay || len(day.Points) != 2 {
		t.Fatalf("day series = %+v", day)
	}
	if day.Points[0].Key != "2026-09-09" || day.Points[1].Key != "2026-09-30" {
		t.Errorf("day keys = %q, %q", day.Points[0].Key, day.Points[1].Key)
	}
	if day.Points[1].Total != 300 || day.Points[1].Events != 2 {
		t.Errorf("day bucket = %+v, want both of that day's events", day.Points[1])
	}

	// Hour buckets: one per clock hour, from the event rows.
	hours := RangeQuery{FromMs: at(2026, 9, 30, 0), ToMs: at(2026, 10, 1, 0)}
	hour, err := db.Series(ctx, hours, loc, GranHour)
	if err != nil {
		t.Fatalf("Series hour: %v", err)
	}
	if hour.Granularity != GranHour || len(hour.Points) != 2 {
		t.Fatalf("hour series = %+v", hour)
	}
	// The key is the instant at the start of the bucket, so a whole-hour timezone reads it
	// back as the local hour.
	first, _ := strconv.ParseInt(hour.Points[0].Key, 10, 64)
	// The key is the start of the bucket, so the 09:30 event lands in the 09:00 bucket.
	bucketStart := time.Date(2026, 9, 30, 9, 0, 0, 0, loc).UnixMilli()
	if first != bucketStart {
		t.Errorf("first hour key = %v, want the 09:00 bucket start %v", first, bucketStart)
	}
	if hour.Points[0].Total != 100 || hour.Points[1].Total != 200 {
		t.Errorf("hour buckets = %+v", hour.Points)
	}

	// Week: the day rollup folded, so both September days land in their ISO weeks.
	week, err := db.Series(ctx, whole, loc, GranWeek)
	if err != nil {
		t.Fatalf("Series week: %v", err)
	}
	if week.Granularity != GranWeek || len(week.Points) != 2 {
		t.Fatalf("week series = %+v", week)
	}
	if week.Points[0].Key != "2026-W37" || week.Points[1].Key != "2026-W40" {
		t.Errorf("week keys = %q, %q", week.Points[0].Key, week.Points[1].Key)
	}
	if week.Points[1].Total != 300 {
		t.Errorf("week bucket = %+v", week.Points[1])
	}

	// Month: one bucket for the whole of September.
	month, err := db.Series(ctx, whole, loc, GranMonth)
	if err != nil {
		t.Fatalf("Series month: %v", err)
	}
	if month.Granularity != GranMonth || len(month.Points) != 1 || month.Points[0].Key != "2026-09" {
		t.Fatalf("month series = %+v", month)
	}
	if month.Points[0].Total != 700 || month.Points[0].Events != 3 {
		t.Errorf("month bucket = %+v", month.Points[0])
	}

	// An empty granularity is the day series, and an hourly request over a range too long to
	// read is answered with days and says so.
	fallback, err := db.Series(ctx, whole, loc, "")
	if err != nil || fallback.Granularity != GranDay {
		t.Fatalf("empty granularity = %+v (err %v)", fallback, err)
	}
	long := RangeQuery{FromMs: at(2025, 1, 1, 0), ToMs: at(2026, 10, 1, 0)}
	if picked, err := db.Series(ctx, long, loc, GranHour); err != nil || picked.Granularity != GranDay {
		t.Fatalf("hour over a long range = %+v (err %v), want the day fallback", picked, err)
	}
}

// modelEvent builds one event for the Models screen tests, defaulting every field the
// grouping does not depend on.
func modelEvent(key, harness, provider, model string, ts int64) Event {
	return Event{
		EventKey: key, Harness: harness, SourceFile: "/tmp/a.jsonl", SessionID: "sess-1",
		Project: "/proj", Model: model, Provider: provider, AgentType: "main",
		Outcome: "ok", TS: ts, Input: 10, Output: 5, CacheRead: 0, CacheWrite: 0,
		Reasoning: 0, Total: 15,
	}
}

// A model reached through two providers, or on two harnesses, is two rows: the Models
// screen exists to separate them, so collapsing on model alone would lose the answer.
func TestModelStatsGroupsByHarnessProviderModel(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := func(h, m int) int64 {
		return time.Date(2026, 3, 1, h, m, 0, 0, time.UTC).UnixMilli()
	}

	claudeViaAPI := modelEvent("e1", "claude", "anthropic", "opus", at(10, 0))
	claudeViaBedrock := modelEvent("e2", "claude", "amazon-bedrock", "opus", at(10, 5))
	// One failed request, and one timed request whose tokens/s is 100 * 1000 / 2000 = 50.
	failed := modelEvent("e3", "codex", "openai", "gpt", at(10, 10))
	failed.Outcome = "error"
	timed := modelEvent("e4", "omp", "cpa", "sol", at(10, 15))
	lat, ttft := int64(2000), int64(800)
	timed.LatencyMs, timed.TTFTMs = &lat, &ttft
	timed.Output, timed.Total = 100, 110

	if _, _, err := db.InsertEvents(ctx, []Event{
		claudeViaAPI, claudeViaBedrock, failed, timed,
	}, time.UTC); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := db.ModelStats(ctx, RangeQuery{FromMs: at(0, 0), ToMs: at(23, 0)})
	if err != nil {
		t.Fatalf("ModelStats: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4 (provider/harness must split): %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Requests != 1 {
			t.Errorf("%v requests = %d, want 1", row.ModelKey, row.Requests)
		}
		switch row.ModelKey {
		case ModelKey{Harness: "codex", Provider: "openai", Model: "gpt"}:
			if row.Errors != 1 {
				t.Errorf("failed row errors = %d, want 1", row.Errors)
			}
			if row.TpsCount != 0 || row.TpsSum != 0 {
				t.Errorf("untimed row tps = %v/%d, want 0/0", row.TpsSum, row.TpsCount)
			}
			if row.TTFTCount != 0 {
				t.Errorf("untimed row ttftCount = %d, want 0", row.TTFTCount)
			}
		case ModelKey{Harness: "omp", Provider: "cpa", Model: "sol"}:
			if row.TpsCount != 1 || row.TpsSum != 50 {
				t.Errorf("timed row tps = %v/%d, want 50/1", row.TpsSum, row.TpsCount)
			}
			if row.LatencySumMs != 2000 || row.LatencyCount != 1 {
				t.Errorf("timed row latency = %d/%d, want 2000/1", row.LatencySumMs, row.LatencyCount)
			}
			if row.TTFTSumMs != 800 || row.TTFTCount != 1 {
				t.Errorf("timed row ttft = %d/%d, want 800/1", row.TTFTSumMs, row.TTFTCount)
			}
		}
	}
}

// Requests fold into the model row; latency and TTFT fold as sum+count so the caller can
// divide once, weighted by the number of rows that actually reported a value.
func TestModelSeriesBucketsAndFoldsSums(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := func(h, m int) int64 {
		return time.Date(2026, 3, 1, h, m, 0, 0, time.UTC).UnixMilli()
	}
	bucket := func(h int) string {
		ts := at(h, 0)
		return strconv.FormatInt(ts/3_600_000*3_600_000, 10)
	}

	ttft1, ttft2 := int64(400), int64(600)
	a := modelEvent("s1", "omp", "cpa", "sol", at(10, 0))
	a.TTFTMs = &ttft1
	b := modelEvent("s2", "omp", "cpa", "sol", at(10, 30))
	b.TTFTMs = &ttft2
	c := modelEvent("s3", "omp", "cpa", "sol", at(11, 15))

	if _, _, err := db.InsertEvents(ctx, []Event{a, b, c}, time.UTC); err != nil {
		t.Fatalf("insert: %v", err)
	}

	points, err := db.ModelSeries(ctx, RangeQuery{FromMs: at(0, 0), ToMs: at(23, 0)}, GranHour)
	if err != nil {
		t.Fatalf("ModelSeries: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("got %d cells, want 2 hourly buckets: %+v", len(points), points)
	}
	if points[0].Bucket != bucket(10) || points[0].Requests != 2 {
		t.Errorf("first cell = %+v, want bucket %s with 2 requests", points[0], bucket(10))
	}
	if points[1].Bucket != bucket(11) || points[1].Requests != 1 {
		t.Errorf("second cell = %+v, want bucket %s with 1 request", points[1], bucket(11))
	}
	if points[0].TTFTSumMs != 1000 || points[0].TTFTCount != 2 {
		t.Errorf("ttft fold = %d/%d, want 1000/2 (mean 500, not 400)", points[0].TTFTSumMs, points[0].TTFTCount)
	}
	if points[1].TTFTCount != 0 {
		t.Errorf("cell without ttft should carry a zero count, got %d", points[1].TTFTCount)
	}

	// Day granularity keys on the stored local_day instead of the clock hour.
	days, err := db.ModelSeries(ctx, RangeQuery{FromMs: at(0, 0), ToMs: at(23, 0)}, GranDay)
	if err != nil {
		t.Fatalf("ModelSeries day: %v", err)
	}
	if len(days) != 1 || days[0].Bucket != "2026-03-01" || days[0].Requests != 3 {
		t.Errorf("day cells = %+v, want one 2026-03-01 cell with 3 requests", days)
	}
}

// An empty range must yield an empty slice, never nil: it crosses to JS as null and every
// caller loops over it.
func TestModelStatsEmptyRangeReturnsEmptySlice(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	rows, err := db.ModelStats(ctx, RangeQuery{FromMs: 1, ToMs: 2})
	if err != nil {
		t.Fatalf("ModelStats: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("ModelStats on empty range = %+v, want empty non-nil slice", rows)
	}
	points, err := db.ModelSeries(ctx, RangeQuery{FromMs: 1, ToMs: 2}, GranHour)
	if err != nil {
		t.Fatalf("ModelSeries: %v", err)
	}
	if points == nil || len(points) != 0 {
		t.Fatalf("ModelSeries on empty range = %+v, want empty non-nil slice", points)
	}
}
