package pricing

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/vietlubu/agents-dashboard/internal/store"
)

// Snapshot is an immutable view of the price table and its rules. Reads take the pointer
// and never see a half-applied update.
type Snapshot struct {
	rates map[string]Rate
	rules map[string]Rule
}

// Catalog resolves costs. The snapshot is swapped atomically after every load or edit, so
// a running scan always prices against a consistent table.
type Catalog struct {
	snap atomic.Pointer[Snapshot]

	log *slog.Logger

	unpricedMu sync.Mutex
	unpriced   map[string]struct{}

	// Catalog endpoints are fields so a test can point them at a local server and still
	// exercise the real download, decode and store path.
	modelsDevURL string
	liteLLMURL   string
}

// NewCatalog returns an empty catalog: every model resolves to "unavailable" until prices
// are loaded or synced.
func NewCatalog(log *slog.Logger) *Catalog {
	c := &Catalog{
		log:          log,
		unpriced:     map[string]struct{}{},
		modelsDevURL: ModelsDevURL,
		liteLLMURL:   LiteLLMURL,
	}
	c.snap.Store(&Snapshot{rates: map[string]Rate{}, rules: map[string]Rule{}})
	return c
}

// Reload replaces the snapshot from the database.
func (c *Catalog) Reload(ctx context.Context, db *store.DB) error {
	prices, err := db.ModelPrices(ctx)
	if err != nil {
		return err
	}
	rules, err := db.PriceRules(ctx)
	if err != nil {
		return err
	}

	snap := &Snapshot{
		rates: make(map[string]Rate, len(prices)),
		rules: make(map[string]Rule, len(rules)),
	}
	for _, p := range prices {
		snap.rates[p.ModelKey] = Rate{
			InputPerM:      p.InputPerM,
			OutputPerM:     p.OutputPerM,
			CacheReadPerM:  p.CacheReadPerM,
			CacheWritePerM: p.CacheWritePerM,
		}
	}
	for _, r := range rules {
		snap.rules[r.ModelKey] = Rule{
			InputMult:      r.InputMult,
			OutputMult:     r.OutputMult,
			CacheReadMult:  r.CacheReadMult,
			CacheWriteMult: r.CacheWriteMult,
			Disabled:       r.Disabled,
		}
	}
	c.snap.Store(snap)
	return nil
}

// Resolve returns the estimated cost in USD and a cost source of "estimated", or
// (nil, "unavailable") when no candidate key has a price.
//
// Buckets must be one request's disjoint token counts. Passing an aggregate would apply
// request-size pricing rules to a whole day.
func (c *Catalog) Resolve(model string, input, output, cacheRead, cacheWrite int64) (*float64, string) {
	snap := c.snap.Load()
	if snap == nil {
		return nil, store.CostSourceUnavailable
	}

	for _, key := range Candidates(model) {
		rate, ok := snap.rates[key]
		if !ok {
			continue
		}
		rule, hasRule := snap.rules[key]
		if !hasRule {
			// A rule may also be stored under the canonical key.
			if canonical := ModelKey(model); canonical != "" {
				rule, hasRule = snap.rules[canonical]
			}
		}
		if hasRule && rule.Disabled {
			zero := 0.0
			return &zero, store.CostSourceEstimated
		}
		mult := Rule{InputMult: 1, OutputMult: 1, CacheReadMult: 1, CacheWriteMult: 1}
		if hasRule {
			mult = rule
		}
		cost := float64(input)/1e6*rate.InputPerM*mult.InputMult +
			float64(output)/1e6*rate.OutputPerM*mult.OutputMult +
			float64(cacheRead)/1e6*rate.CacheReadPerM*mult.CacheReadMult +
			float64(cacheWrite)/1e6*rate.CacheWritePerM*mult.CacheWriteMult
		return &cost, store.CostSourceEstimated
	}

	c.noteUnpriced(model)
	return nil, store.CostSourceUnavailable
}

// Priced reports whether a model has a usable rate.
func (c *Catalog) Priced(model string) bool {
	snap := c.snap.Load()
	if snap == nil {
		return false
	}
	for _, key := range Candidates(model) {
		if _, ok := snap.rates[key]; ok {
			return true
		}
	}
	return false
}

// noteUnpriced logs each unknown model once per process. Without it a new model id shows
// up only as a missing number on a chart, which is easy to miss; with it the first usage
// of a new model is visible in the log.
func (c *Catalog) noteUnpriced(model string) {
	if c.log == nil {
		return
	}
	key := ModelKey(model)
	if key == "" {
		key = model
	}
	c.unpricedMu.Lock()
	if _, seen := c.unpriced[key]; seen {
		c.unpricedMu.Unlock()
		return
	}
	c.unpriced[key] = struct{}{}
	c.unpricedMu.Unlock()
	c.log.Warn("no price for model; cost will be unavailable", "model", model)
}

// UnpricedSeen lists the models logged as unpriced in this process.
func (c *Catalog) UnpricedSeen() []string {
	c.unpricedMu.Lock()
	defer c.unpricedMu.Unlock()
	out := make([]string, 0, len(c.unpriced))
	for k := range c.unpriced {
		out = append(out, k)
	}
	return out
}

// Recalculate re-prices every event whose cost came from the table. Reported costs are left
// alone: the harness recorded an exact amount and a price table must not overwrite it.
//
// The set covers rows currently marked "unavailable" as well as "estimated": adding a price
// for a model that had none is exactly the case this exists for, and a row that lost its
// price must fall back to unavailable rather than keep a stale number.
//
// Callers must run this after any price or rule change, then rebuild rollups, because the day
// set affected by a global price change is unknown.
func (c *Catalog) Recalculate(ctx context.Context, db *store.DB, progress func(done, total int64)) (int64, error) {
	total, err := db.CountRepriceable(ctx)
	if err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, nil
	}

	const batch = 5000
	var cursor int64
	var updated int64
	for {
		if err := ctx.Err(); err != nil {
			return updated, err
		}
		events, next, err := db.RepriceableEvents(ctx, cursor, batch)
		if err != nil {
			return updated, err
		}
		if len(events) == 0 {
			break
		}
		updates := make(map[int64]*float64, len(events))
		for _, e := range events {
			cost, source := c.Resolve(e.Model, e.Input, e.Output, e.CacheRead, e.CacheWrite)
			if source == store.CostSourceEstimated {
				updates[e.ID] = cost
			} else {
				// The model lost its price: the event must fall back to unavailable rather
				// than keep a stale number.
				updates[e.ID] = nil
			}
		}
		if err := db.UpdateEventCosts(ctx, updates); err != nil {
			return updated, err
		}
		updated += int64(len(updates))
		cursor = next
		if progress != nil {
			progress(updated, total)
		}
	}
	return updated, nil
}
