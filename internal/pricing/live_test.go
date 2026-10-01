package pricing

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/vietlubu/agent-dashboard/internal/store"
)

// TestSyncFromLiveCatalog exercises the real endpoints.
//
// It is opt-in because it needs network access: run it with
//
//	AGENT_DASHBOARD_LIVE_PRICING=1 go test ./internal/pricing/ -run LiveCatalog -v
//
// The catalog's shape is not something a fixture can be trusted to keep in step with, and the
// first version of this code silently imported nothing because the document turned out to be
// keyed by provider id with no wrapper object. This test is the guard against that class of
// mistake.
func TestSyncFromLiveCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	if os.Getenv("AGENT_DASHBOARD_LIVE_PRICING") == "" {
		t.Skip("set AGENT_DASHBOARD_LIVE_PRICING=1 to run against the live price catalogs")
	}

	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	cat := NewCatalog(slog.Default())

	for _, source := range []string{SourceModelsDev, SourceLiteLLM} {
		res, err := cat.SyncFrom(ctx, db, source)
		if err != nil {
			t.Fatalf("SyncFrom(%s): %v", source, err)
		}
		if res.Models < 100 {
			t.Errorf("%s imported %d models; a real catalog holds far more", source, res.Models)
		}
		t.Logf("%s: %d models, %d skipped", source, res.Models, res.Skipped)
	}

	// A handful of models that exist in this data set must resolve to a plausible rate.
	for _, model := range []string{"claude-opus-4-8", "gpt-5.6-sol", "kimi-k2.6"} {
		cost, source := cat.Resolve(model, 1_000_000, 0, 0, 0)
		if source != store.CostSourceEstimated || cost == nil {
			t.Errorf("%s did not resolve (source %q)", model, source)
			continue
		}
		if *cost <= 0 || *cost > 1000 {
			t.Errorf("%s resolved to an implausible per-million input rate: %v", model, *cost)
		}
		t.Logf("%s: $%v per 1M input tokens", model, *cost)
	}
}
