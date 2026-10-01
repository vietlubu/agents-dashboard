package pricing

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vietlubu/agent-dashboard/internal/store"
)

func mustLocation(t *testing.T) *time.Location { return time.UTC }

func newTestStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestModelKeyAndCandidates(t *testing.T) {
	cases := []struct {
		in       string
		key      string
		mustHave []string
	}{
		{"gpt-5.6-sol", "gpt-5.6-sol", []string{"gpt-5.6-sol"}},
		{"openai/gpt-5.6-sol", "gpt-5.6-sol", []string{"openai/gpt-5.6-sol", "gpt-5.6-sol"}},
		{"models/gpt-5.5", "gpt-5.5", []string{"gpt-5.5"}},
		{"claude-opus-4-8[1m]", "claude-opus-4-8", []string{"claude-opus-4-8"}},
		{"anthropic/claude-sonnet-4-5[1m]", "claude-sonnet-4-5", []string{"claude-sonnet-4-5"}},
		{"gpt-5.2-2025-12-11", "gpt-5.2", []string{"gpt-5.2-2025-12-11", "gpt-5.2"}},
		{"gpt-5.2-20251211", "gpt-5.2", []string{"gpt-5.2"}},
		{"  Alibaba-Token-Plan/DeepSeek-V4.1-Flash ", "deepseek-v4.1-flash", []string{"deepseek-v4.1-flash"}},
	}
	for _, tc := range cases {
		if got := ModelKey(tc.in); got != tc.key {
			t.Errorf("ModelKey(%q) = %q, want %q", tc.in, got, tc.key)
		}
		got := Candidates(tc.in)
		for _, want := range tc.mustHave {
			found := false
			for _, c := range got {
				if c == want {
					found = true
				}
			}
			if !found {
				t.Errorf("Candidates(%q) = %v, missing %q", tc.in, got, want)
			}
		}
	}
	if ModelKey("") != "" || Candidates("") != nil {
		t.Error("empty model must yield no candidates")
	}
}

// A model that is not dated must not have digits carved off it.
func TestStripTrailingDateOnlyMatchesDates(t *testing.T) {
	for _, in := range []string{"gpt-5", "claude-opus-4-8", "gemini-2.5-pro", "o1-mini"} {
		if got := stripTrailingDate(in); got != in {
			t.Errorf("stripTrailingDate(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestResolveUsesTableAndRules(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()
	if err := db.UpsertModelPrices(ctx, []store.ModelPrice{{
		ModelKey: "gpt-5.6-sol", InputPerM: 10, OutputPerM: 40, CacheReadPerM: 1, Source: "manual",
	}}); err != nil {
		t.Fatalf("prices: %v", err)
	}
	cat := NewCatalog(slog.Default())
	if err := cat.Reload(ctx, db); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// 1M input + 1M output + 1M cache read = 10 + 40 + 1.
	cost, source := cat.Resolve("openai/gpt-5.6-sol", 1_000_000, 1_000_000, 1_000_000, 0)
	if source != store.CostSourceEstimated {
		t.Fatalf("source = %q, want estimated", source)
	}
	if cost == nil || *cost != 51 {
		t.Fatalf("cost = %v, want 51", cost)
	}

	if _, source := cat.Resolve("totally-unknown-model", 10, 10, 0, 0); source != store.CostSourceUnavailable {
		t.Errorf("unknown model source = %q, want unavailable", source)
	}
	if got := cat.UnpricedSeen(); len(got) != 1 || got[0] != "totally-unknown-model" {
		t.Errorf("UnpricedSeen = %v", got)
	}

	// A disabled rule suppresses the cost without removing the model.
	if err := db.UpsertPriceRule(ctx, store.PriceRule{
		ModelKey: "gpt-5.6-sol", InputMult: 1, OutputMult: 1, CacheReadMult: 1, CacheWriteMult: 1, Disabled: true,
	}); err != nil {
		t.Fatalf("rule: %v", err)
	}
	if err := cat.Reload(ctx, db); err != nil {
		t.Fatalf("Reload 2: %v", err)
	}
	cost, source = cat.Resolve("gpt-5.6-sol", 1_000_000, 0, 0, 0)
	if source != store.CostSourceEstimated || cost == nil || *cost != 0 {
		t.Errorf("disabled rule: cost=%v source=%q, want 0/estimated", cost, source)
	}

	// A multiplier scales its field only.
	if err := db.UpsertPriceRule(ctx, store.PriceRule{
		ModelKey: "gpt-5.6-sol", InputMult: 0.5, OutputMult: 1, CacheReadMult: 1, CacheWriteMult: 1,
	}); err != nil {
		t.Fatalf("rule 2: %v", err)
	}
	if err := cat.Reload(ctx, db); err != nil {
		t.Fatalf("Reload 3: %v", err)
	}
	cost, _ = cat.Resolve("gpt-5.6-sol", 1_000_000, 1_000_000, 0, 0)
	if cost == nil || *cost != 45 {
		t.Errorf("multiplier cost = %v, want 5 + 40 = 45", cost)
	}
}

// Recalculate covers every non-reported row and never touches a reported cost.
//
// The rows it visits are those whose cost came from the table, which includes rows currently
// marked unavailable (their model has no price yet) — so a report of "N updated" counts them,
// and the observable outcome is what matters: priced rows get a number, unpriced rows stay
// unavailable, reported rows keep their exact amount.
func TestRecalculateLeavesReportedCostsAlone(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()
	loc := mustLocation(t)

	events := []store.Event{
		{EventKey: "e1", Harness: "pi", SessionID: "s", Model: "m1", TS: 1_700_000_000_000, Input: 1_000_000, Total: 1_000_000, CostSource: store.CostSourceUnavailable},
		{EventKey: "e2", Harness: "omp", SessionID: "s", Model: "m2", TS: 1_700_000_000_000, Input: 1_000_000, Total: 1_000_000, CostSource: store.CostSourceUnavailable},
		{EventKey: "e3", Harness: "opencode", SessionID: "s", Model: "m1", TS: 1_700_000_000_000, Input: 1_000_000, Total: 1_000_000, CostUSD: new(7.5), CostSource: store.CostSourceReported},
	}
	if _, _, err := db.InsertEvents(ctx, events, loc); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.UpsertModelPrices(ctx, []store.ModelPrice{{ModelKey: "m1", InputPerM: 2, Source: "manual"}}); err != nil {
		t.Fatalf("prices: %v", err)
	}
	cat := NewCatalog(slog.Default())
	if err := cat.Reload(ctx, db); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// First give both an estimated cost so Recalculate has something to work on.
	page, err := db.Events(ctx, store.RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	updates := map[int64]*float64{}
	for _, row := range page.Rows {
		if row.Model == "m1" && row.CostSource != store.CostSourceReported {
			updates[row.ID] = new(1.0)
		}
	}
	if err := db.UpdateEventCosts(ctx, updates); err != nil {
		t.Fatalf("UpdateEventCosts: %v", err)
	}

	updated, err := cat.Recalculate(ctx, db, nil)
	if err != nil {
		t.Fatalf("Recalculate: %v", err)
	}
	// m1's estimated row is re-priced, and m2's unpriced row is re-examined and stays
	// unavailable; the reported row is not visited at all.
	if updated != 2 {
		t.Errorf("updated = %d, want 2 (the estimated row plus the unpriced row)", updated)
	}

	page, err = db.Events(ctx, store.RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events 2: %v", err)
	}
	byKey := map[string]store.EventRow{}
	for _, r := range page.Rows {
		byKey[r.Model+"|"+r.CostSource] = r
	}
	// m1 estimated row re-priced from the table: 1M input at 2/M = 2.0.
	if r, ok := byKey["m1|estimated"]; !ok || r.CostUSD == nil || *r.CostUSD != 2 {
		t.Errorf("re-priced row = %+v", r)
	}
	// The reported row keeps its exact amount.
	if r, ok := byKey["m1|reported"]; !ok || r.CostUSD == nil || *r.CostUSD != 7.5 {
		t.Errorf("reported row = %+v", r)
	}
	// A model with no price keeps a nil cost rather than a zero.
	if r, ok := byKey["m2|unavailable"]; !ok || r.CostUSD != nil {
		t.Errorf("unpriced row = %+v", r)
	}
}

// A catalog sync must run the real download, decode and store path, so the test points
// the catalog at a local server rather than re-implementing the parsers.
// Adding a price for a model that had none must price its existing events. The first version
// of Recalculate only touched rows already marked "estimated", so the model a user actually
// came to fix — an unpriced one — was never re-priced.
func TestRecalculatePricesPreviouslyUnavailableRows(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()
	loc := mustLocation(t)

	events := []store.Event{
		{EventKey: "u1", Harness: "pi", SessionID: "s", Model: "brand-new", TS: 1_700_000_000_000, Input: 1_000_000, Total: 1_000_000, CostSource: store.CostSourceUnavailable},
		{EventKey: "u2", Harness: "codex", SessionID: "s", Model: "brand-new", TS: 1_700_000_000_000, Input: 500_000, Total: 500_000, CostSource: store.CostSourceUnavailable},
		{EventKey: "r1", Harness: "omp", SessionID: "s", Model: "brand-new", TS: 1_700_000_000_000, Input: 1_000_000, Total: 1_000_000, CostUSD: new(9.0), CostSource: store.CostSourceReported},
	}
	if _, _, err := db.InsertEvents(ctx, events, loc); err != nil {
		t.Fatalf("insert: %v", err)
	}

	cat := NewCatalog(slog.Default())
	if err := db.UpsertModelPrices(ctx, []store.ModelPrice{{ModelKey: "brand-new", InputPerM: 2, Source: "manual"}}); err != nil {
		t.Fatalf("price: %v", err)
	}
	if err := cat.Reload(ctx, db); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	updated, err := cat.Recalculate(ctx, db, nil)
	if err != nil {
		t.Fatalf("Recalculate: %v", err)
	}
	if updated != 2 {
		t.Errorf("updated = %d, want the two non-reported rows", updated)
	}

	page, err := db.Events(ctx, store.RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	estimated := 0
	for _, row := range page.Rows {
		switch row.CostSource {
		case store.CostSourceEstimated:
			estimated++
			if row.CostUSD == nil {
				t.Errorf("estimated row has no cost: %+v", row)
			}
		case store.CostSourceReported:
			if row.CostUSD == nil || *row.CostUSD != 9 {
				t.Errorf("reported row changed: %+v", row)
			}
		}
	}
	if estimated != 2 {
		t.Errorf("estimated rows = %d, want 2", estimated)
	}

	// Deleting the price must put them back to unavailable rather than keep a stale number.
	if err := db.DeleteModelPrice(ctx, "brand-new"); err != nil {
		t.Fatalf("DeleteModelPrice: %v", err)
	}
	if err := cat.Reload(ctx, db); err != nil {
		t.Fatalf("Reload 2: %v", err)
	}
	if _, err := cat.Recalculate(ctx, db, nil); err != nil {
		t.Fatalf("Recalculate 2: %v", err)
	}
	page, err = db.Events(ctx, store.RangeQuery{}, 0, 10)
	if err != nil {
		t.Fatalf("Events 2: %v", err)
	}
	for _, row := range page.Rows {
		if row.CostSource == store.CostSourceEstimated {
			t.Errorf("row kept an estimated cost after its price was deleted: %+v", row)
		}
	}
}

func TestSyncFromModelsDevAndLiteLLM(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	// Shape verified against the live document: the top level is keyed by provider id (there
	// is no wrapping "providers" object), and the same model appears under several providers.
	modelsDev := map[string]any{
		"anthropic": map[string]any{
			"models": map[string]any{
				"claude-opus-4-8": map[string]any{
					"cost": map[string]any{"input": 5.0, "output": 25.0, "cache_read": 0.5, "cache_write": 6.25},
				},
				"unpriced-model": map[string]any{},
			},
		},
		"a-reseller": map[string]any{
			"models": map[string]any{
				// A cheaper listing of a first-party model must not win.
				"claude-opus-4-8": map[string]any{
					"cost": map[string]any{"input": 0.425, "output": 2.125},
				},
				// A model only this provider lists: the cheapest listing wins.
				"resold-model": map[string]any{
					"cost": map[string]any{"input": 1.0, "output": 2.0},
				},
			},
		},
		"z-reseller": map[string]any{
			"models": map[string]any{
				"resold-model": map[string]any{
					"cost": map[string]any{"input": 2.0, "output": 4.0},
				},
			},
		},
	}
	litellm := map[string]any{
		"sample_spec": map[string]any{"mode": "chat"},
		"openai/gpt-5.6-sol": map[string]any{
			"mode":                            "chat",
			"input_cost_per_token":            1.5e-06,
			"output_cost_per_token":           1.2e-05,
			"cache_read_input_token_cost":     1.5e-07,
			"cache_creation_input_token_cost": 1.875e-06,
		},
		"text-embedding-3-large": map[string]any{"mode": "embedding", "input_cost_per_token": 1e-07},
		"no-prices":              map[string]any{"mode": "chat"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models.json":
			_ = json.NewEncoder(w).Encode(modelsDev)
		case "/litellm.json":
			_ = json.NewEncoder(w).Encode(litellm)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cat := NewCatalog(slog.Default())
	cat.modelsDevURL = server.URL + "/models.json"
	cat.liteLLMURL = server.URL + "/litellm.json"

	res, err := cat.SyncFrom(ctx, db, SourceModelsDev)
	if err != nil {
		t.Fatalf("SyncFrom models-dev: %v", err)
	}
	if res.Models != 2 || res.Skipped != 1 {
		t.Errorf("models-dev result = %+v, want 2 models and 1 skipped", res)
	}
	prices, err := db.ModelPrices(ctx)
	if err != nil {
		t.Fatalf("ModelPrices: %v", err)
	}
	byModel := map[string]store.ModelPrice{}
	for _, p := range prices {
		byModel[p.ModelKey] = p
	}
	firstParty, ok := byModel["claude-opus-4-8"]
	if !ok {
		t.Fatalf("claude-opus-4-8 not stored: %+v", prices)
	}
	// The first-party listing wins over the reseller's cheaper one.
	if firstParty.InputPerM != 5 || firstParty.OutputPerM != 25 {
		t.Errorf("claude rate = %+v, want the anthropic listing", firstParty)
	}
	if firstParty.CacheWritePerM != 6.25 || firstParty.Source != SourceModelsDev {
		t.Errorf("stored price = %+v", firstParty)
	}
	// A model with only reseller listings takes the cheapest one.
	if resold, ok := byModel["resold-model"]; !ok || resold.InputPerM != 1 {
		t.Errorf("resold-model = %+v, want the cheapest listing (1/M)", resold)
	}

	res, err = cat.SyncFrom(ctx, db, SourceLiteLLM)
	if err != nil {
		t.Fatalf("SyncFrom litellm: %v", err)
	}
	// sample_spec is skipped silently; the embedding and the entry without rates count.
	if res.Models != 1 || res.Skipped != 2 {
		t.Errorf("litellm result = %+v, want 1 model and 2 skipped", res)
	}
	prices, err = db.ModelPrices(ctx)
	if err != nil {
		t.Fatalf("ModelPrices 2: %v", err)
	}
	for _, p := range prices {
		byModel[p.ModelKey] = p
	}
	got, ok := byModel["gpt-5.6-sol"]
	if !ok {
		t.Fatalf("gpt-5.6-sol not stored: %+v", prices)
	}
	// LiteLLM publishes per-token rates; they must be scaled to per-million.
	if got.InputPerM != 1.5 || got.OutputPerM != 12 || got.CacheReadPerM != 0.15 || got.CacheWritePerM != 1.875 {
		t.Errorf("per-token rates not scaled: %+v", got)
	}
	if got.Source != SourceLiteLLM {
		t.Errorf("source = %q", got.Source)
	}

	cost, source := cat.Resolve("openai/gpt-5.6-sol", 1_000_000, 1_000_000, 0, 0)
	if source != store.CostSourceEstimated || cost == nil || *cost != 13.5 {
		t.Errorf("resolved = %v/%q, want 13.5/estimated", cost, source)
	}
}

// A failing endpoint must surface an error and leave the price table untouched.
func TestSyncFromNetworkFailureIsNonFatal(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()
	if err := db.UpsertModelPrices(ctx, []store.ModelPrice{{ModelKey: "keep", InputPerM: 1, Source: "manual"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	cat := NewCatalog(slog.Default())
	cat.modelsDevURL = server.URL + "/models.json"
	if _, err := cat.SyncFrom(ctx, db, SourceModelsDev); err == nil {
		t.Fatal("expected an error for a failing endpoint")
	}
	prices, err := db.ModelPrices(ctx)
	if err != nil {
		t.Fatalf("ModelPrices: %v", err)
	}
	if len(prices) != 1 || prices[0].ModelKey != "keep" {
		t.Errorf("prices = %+v, want the seeded row untouched", prices)
	}
}

func TestSyncFromRejectsUnknownSource(t *testing.T) {
	db := newTestStore(t)
	cat := NewCatalog(slog.Default())
	if _, err := cat.SyncFrom(context.Background(), db, "nope"); err == nil {
		t.Fatal("expected an error for an unknown source")
	}
}
