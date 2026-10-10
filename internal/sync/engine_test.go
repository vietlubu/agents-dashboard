package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/harness"
	"github.com/vietlubu/agents-dashboard/internal/pricing"
	"github.com/vietlubu/agents-dashboard/internal/store"
)

func testEngine(t *testing.T) (*Engine, *store.DB, *pricing.Catalog, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGENTS_DASHBOARD_HOME", home)
	t.Setenv("AGENTS_DASHBOARD_TZ", "UTC")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := store.Open(filepath.Join(home, "dashboard.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cat := pricing.NewCatalog(logger)
	if err := cat.Reload(context.Background(), db); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	eng := New(db, cfg, cat, logger, nil)
	// Point the scan at the fixture home instead of the developer's real sessions.
	eng.UseAdapters(harness.AllWithHome(home))
	return eng, db, cat, home
}

// writeClaudeSession plants a Claude transcript so the engine has something real to scan.
func writeClaudeSession(t *testing.T, home, project, session string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, session+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func claudeRecord(id, requestID, ts, model string, in, out, cacheWrite, cacheRead int) string {
	return `{"type":"assistant","requestId":"` + requestID + `","timestamp":"` + ts +
		`","sessionId":"s","cwd":"/proj","message":{"id":"` + id + `","model":"` + model +
		`","stop_reason":"stop","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) +
		`,"cache_creation_input_tokens":` + itoa(cacheWrite) + `,"cache_read_input_tokens":` + itoa(cacheRead) + `}}}`
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// A scan must insert events, record sessions, and build rollups that agree with the events.
func TestEngineScanPopulatesEventsSessionsAndRollups(t *testing.T) {
	eng, db, _, home := testEngine(t)
	ctx := context.Background()

	writeClaudeSession(t, home, "-proj", "s1",
		claudeRecord("m1", "r1", "2026-06-01T10:00:00.000Z", "claude-opus-4-8", 100, 50, 10, 20),
		claudeRecord("m2", "r2", "2026-06-01T10:05:00.000Z", "claude-opus-4-8", 200, 60, 0, 30),
	)

	summary, err := eng.Run(ctx, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.EventsInserted != 2 {
		t.Errorf("inserted = %d, want 2", summary.EventsInserted)
	}
	if !summary.Cold {
		t.Error("the first run must be reported as cold")
	}

	totals, err := db.Totals(ctx, store.RangeQuery{}, time.UTC)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals.Events != 2 || totals.Total != 100+50+10+20+200+60+0+30 {
		t.Errorf("totals = %+v", totals)
	}
	// With no price table every event must be reported as unavailable rather than priced
	// at zero, or the UI would show free usage instead of "unknown".
	if totals.CostUnavailable != 2 {
		t.Errorf("cost unavailable = %d, want 2", totals.CostUnavailable)
	}

	sessions, err := db.Sessions(ctx, store.RangeQuery{}, 0, 10, "updated")
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if sessions.Total != 1 || sessions.Rows[0].SessionID != "s1" {
		t.Errorf("sessions = %+v", sessions)
	}
	if sessions.Rows[0].Project != "/proj" {
		t.Errorf("session project = %q, want the record cwd", sessions.Rows[0].Project)
	}

	// The rollups must equal the event table exactly.
	var eventTotal, rollupTotal int64
	if err := db.Reader().QueryRowContext(ctx, `SELECT SUM(total_tokens) FROM usage_events`).Scan(&eventTotal); err != nil {
		t.Fatalf("event sum: %v", err)
	}
	if err := db.Reader().QueryRowContext(ctx, `SELECT SUM(total_tokens) FROM rollup_daily_hm`).Scan(&rollupTotal); err != nil {
		t.Fatalf("rollup sum: %v", err)
	}
	if rollupTotal != eventTotal {
		t.Errorf("rollup total = %d, event total = %d", rollupTotal, eventTotal)
	}
}

// A second scan with nothing changed must insert nothing, which is what makes the idle
// loop cheap.
func TestEngineSecondScanIsNoOp(t *testing.T) {
	eng, db, _, home := testEngine(t)
	ctx := context.Background()

	path := writeClaudeSession(t, home, "-proj", "s1",
		claudeRecord("m1", "r1", "2026-06-01T10:00:00.000Z", "m", 10, 1, 0, 0))

	if _, err := eng.Run(ctx, "startup"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := eng.Run(ctx, "scheduled")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.EventsInserted != 0 || second.EventsUpdated != 0 {
		t.Errorf("second run touched %d/%d events, want 0", second.EventsInserted, second.EventsUpdated)
	}
	if second.Cold {
		t.Error("an unchanged run must not be reported as cold")
	}

	// Appending one record must yield exactly one new event.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(claudeRecord("m2", "r2", "2026-06-01T11:00:00.000Z", "m", 20, 2, 0, 0) + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	third, err := eng.Run(ctx, "scheduled")
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if third.EventsInserted != 1 {
		t.Errorf("third run inserted %d, want 1", third.EventsInserted)
	}
	page, err := db.Events(ctx, store.RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if page.Total != 2 {
		t.Errorf("stored events = %d, want 2 (no duplicates)", page.Total)
	}
}

// A reported cost must survive the scan untouched, and a priced model must be estimated.
func TestEngineCostSourceSelection(t *testing.T) {
	eng, db, cat, home := testEngine(t)
	ctx := context.Background()

	writeClaudeSession(t, home, "-proj", "s1",
		claudeRecord("m1", "r1", "2026-06-01T10:00:00.000Z", "priced-model", 1_000_000, 0, 0, 0))

	if err := db.UpsertModelPrices(ctx, []store.ModelPrice{{ModelKey: "priced-model", InputPerM: 3, Source: "manual"}}); err != nil {
		t.Fatalf("prices: %v", err)
	}
	if err := cat.Reload(ctx, db); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if _, err := eng.Run(ctx, "startup"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	page, err := db.Events(ctx, store.RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	row := page.Rows[0]
	if row.CostSource != store.CostSourceEstimated {
		t.Errorf("cost source = %q, want estimated", row.CostSource)
	}
	if row.CostUSD == nil || *row.CostUSD != 3 {
		t.Errorf("cost = %v, want 3 (1M input at 3/M)", row.CostUSD)
	}
}

// Bumping an adapter's parser version must wipe that harness and re-read it, because the
// stored byte offsets no longer describe what the parser reads.
func TestEngineParserVersionReset(t *testing.T) {
	eng, db, _, home := testEngine(t)
	ctx := context.Background()

	writeClaudeSession(t, home, "-proj", "s1",
		claudeRecord("m1", "r1", "2026-06-01T10:00:00.000Z", "m", 10, 1, 0, 0))
	if _, err := eng.Run(ctx, "startup"); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Force a stored cursor to look stale by rewriting its parser version.
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE scan_state SET parser_version = 999 WHERE harness = 'claude'`); err != nil {
		t.Fatalf("update: %v", err)
	}

	summary, err := eng.Run(ctx, "scheduled")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if summary.EventsInserted != 1 {
		t.Errorf("inserted = %d, want the event re-ingested after a parser reset", summary.EventsInserted)
	}
	page, err := db.Events(ctx, store.RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("stored events = %d, want 1 (reset, not duplicated)", page.Total)
	}
}

// The guard is asserted directly: with a fixture home a scan finishes in milliseconds, so
// racing two real scans would be flaky rather than meaningful.
func TestEngineRejectsConcurrentRun(t *testing.T) {
	eng, _, _, home := testEngine(t)
	ctx := context.Background()
	writeClaudeSession(t, home, "-proj", "s1",
		claudeRecord("m1", "r1", "2026-06-01T10:00:00.000Z", "m", 10, 1, 0, 0))

	eng.mu.Lock()
	eng.running = true
	eng.mu.Unlock()

	if _, err := eng.Run(ctx, "manual"); err != ErrAlreadyRunning {
		t.Errorf("concurrent Run error = %v, want ErrAlreadyRunning", err)
	}

	// Once the flag clears, a normal run proceeds.
	eng.mu.Lock()
	eng.running = false
	eng.mu.Unlock()

	if _, err := eng.Run(ctx, "manual"); err != nil {
		t.Fatalf("Run after clearing the flag: %v", err)
	}
	if eng.Running() {
		t.Error("Running = true after Run returned")
	}
}

func TestEngineReportListsEveryHarness(t *testing.T) {
	eng, _, _, home := testEngine(t)
	ctx := context.Background()
	writeClaudeSession(t, home, "-proj", "s1",
		claudeRecord("m1", "r1", "2026-06-01T10:00:00.000Z", "m", 10, 1, 0, 0))
	if _, err := eng.Run(ctx, "startup"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	report, err := eng.Report(ctx)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(report) != 8 {
		t.Fatalf("report has %d harnesses, want 8", len(report))
	}
	byID := map[string]store.HarnessReport{}
	for _, r := range report {
		byID[r.ID] = r
	}
	for _, id := range []string{"claude", "codex", "opencode", "pi", "omp", "freebuff", "freebuff-desktop", "dsh"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("harness %q missing from the report", id)
		}
	}
	if byID["claude"].Events != 1 {
		t.Errorf("claude events = %d, want 1", byID["claude"].Events)
	}
	if len(byID["claude"].Roots) == 0 {
		t.Error("claude roots are empty")
	}
	if byID["claude"].Sessions != 1 {
		t.Errorf("claude sessions = %d, want 1", byID["claude"].Sessions)
	}
}

func TestEngineCancelStopsScan(t *testing.T) {
	eng, _, _, home := testEngine(t)
	for i := range 40 {
		writeClaudeSession(t, home, "-proj", "s"+itoa(i),
			claudeRecord("m"+itoa(i), "r", "2026-06-01T10:00:00.000Z", "m", 10, 1, 0, 0))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary, err := eng.Run(ctx, "manual")
	// A cancelled scan reports the cancellation rather than failing silently.
	if summary.Err == "" && err == nil {
		t.Errorf("cancelled run reported neither an error nor a summary error: %+v", summary)
	}
}

// A run killed between inserting events and refreshing its rollups would otherwise leave
// the UI permanently behind, because a later refresh only rebuilds the days it touches.
// The next run that writes anything must notice and repair the tables.
func TestEngineRepairsDriftedRollups(t *testing.T) {
	eng, db, _, home := testEngine(t)
	ctx := context.Background()

	writeClaudeSession(t, home, "-proj", "s1",
		claudeRecord("m1", "r1", "2026-06-01T10:00:00.000Z", "claude-opus-4-8", 100, 50, 0, 0),
	)
	if _, err := eng.Run(ctx, "startup"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if intact, err := db.RollupsIntact(ctx); err != nil || !intact {
		t.Fatalf("healthy scan left rollups inconsistent: intact=%v err=%v", intact, err)
	}

	// Simulate the interrupted run: the events are stored but the rollups were never
	// refreshed.
	if _, err := db.Writer().ExecContext(ctx, `
		DELETE FROM rollup_session;
		DELETE FROM rollup_daily_hm;
		DELETE FROM rollup_daily_hp;`); err != nil {
		t.Fatalf("drift the rollups: %v", err)
	}
	if intact, err := db.RollupsIntact(ctx); err != nil || intact {
		t.Fatalf("intact=%v err=%v, want the invariant reported as violated", intact, err)
	}

	writeClaudeSession(t, home, "-proj", "s2",
		claudeRecord("m2", "r2", "2026-06-01T11:00:00.000Z", "claude-opus-4-8", 10, 5, 0, 0),
	)
	if _, err := eng.Run(ctx, "scheduled"); err != nil {
		t.Fatalf("repairing run: %v", err)
	}
	if intact, err := db.RollupsIntact(ctx); err != nil || !intact {
		t.Fatalf("rollups were not repaired: intact=%v err=%v", intact, err)
	}
	totals, err := db.Totals(ctx, store.RangeQuery{}, time.UTC)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals.Events != 2 {
		t.Errorf("events after repair = %d, want both records", totals.Events)
	}
}
