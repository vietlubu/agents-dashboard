package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/store"
)

// Price catalog sources. Both are the sources the reference implementation uses, and both
// publish USD rates that normalize to per-million tokens.
const (
	SourceModelsDev = "models-dev"
	SourceLiteLLM   = "litellm"

	ModelsDevURL = "https://models.dev/api.json"
	LiteLLMURL   = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

	// maxCatalogBytes caps the fetched body. models.dev's catalog is several megabytes; the
	// cap stops a bad response from exhausting memory.
	maxCatalogBytes = 64 << 20
	fetchTimeout    = 60 * time.Second
)

// SyncResult reports what a catalog sync did.
type SyncResult struct {
	Source  string `json:"source"`
	Models  int    `json:"models"`
	Skipped int    `json:"skipped"`
}

// SyncFrom downloads a price catalog and stores it.
//
// A failure here is never fatal: without prices every event stays "unavailable" and the UI
// shows a dash. Manual prices are never overwritten by a sync.
func (c *Catalog) SyncFrom(ctx context.Context, db *store.DB, source string) (SyncResult, error) {
	res := SyncResult{Source: source}

	var (
		prices []store.ModelPrice
		err    error
	)
	switch source {
	case SourceModelsDev:
		prices, res.Skipped, err = c.fetchModelsDev(ctx)
	case SourceLiteLLM:
		prices, res.Skipped, err = c.fetchLiteLLM(ctx)
	default:
		return res, fmt.Errorf("unknown price source %q", source)
	}
	if err != nil {
		return res, err
	}

	if err := db.UpsertModelPrices(ctx, prices); err != nil {
		return res, err
	}
	res.Models = len(prices)
	if err := c.Reload(ctx, db); err != nil {
		return res, err
	}
	return res, nil
}

// download retrieves a catalog body with a hard size cap and a timeout, so a slow or oversized
// response cannot stall a sync or exhaust memory.
func download(ctx context.Context, url string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// modelsDevCost is one model's published rates, already per million tokens.
type modelsDevCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

// modelsDevProvider is one entry of models.dev's catalog. The document is keyed by provider id
// at the top level — there is no wrapping "providers" object — and each provider holds its own
// model map.
type modelsDevProvider struct {
	Models map[string]struct {
		Cost *modelsDevCost `json:"cost"`
	} `json:"models"`
}

func (c *Catalog) fetchModelsDev(ctx context.Context) ([]store.ModelPrice, int, error) {
	body, err := download(ctx, c.modelsDevURL)
	if err != nil {
		return nil, 0, err
	}
	var doc map[string]modelsDevProvider
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("decode models.dev catalog: %w", err)
	}

	// The same model id appears under many providers at different prices, so one candidate is
	// chosen per model: the provider that publishes the model itself when it is present,
	// otherwise the cheapest listing. Both rules are deterministic, which matters because the
	// alternative — first entry wins — would depend on map iteration order and change between
	// runs.
	type candidate struct {
		provider string
		price    store.ModelPrice
	}
	best := map[string]candidate{}
	skipped := 0

	providers := make([]string, 0, len(doc))
	for id := range doc {
		providers = append(providers, id)
	}
	sort.Strings(providers)

	for _, providerID := range providers {
		for modelID, model := range doc[providerID].Models {
			if model.Cost == nil {
				skipped++
				continue
			}
			input := valueOr(model.Cost.Input)
			output := valueOr(model.Cost.Output)
			if input == 0 && output == 0 {
				// Some listings publish zeroes (a subscription plan, or a placeholder). A zero
				// rate is not a price: it would claim the usage is free.
				skipped++
				continue
			}
			key := ModelKey(modelID)
			if key == "" {
				skipped++
				continue
			}
			price := store.ModelPrice{
				ModelKey:       key,
				InputPerM:      input,
				OutputPerM:     output,
				CacheReadPerM:  valueOr(model.Cost.CacheRead),
				CacheWritePerM: valueOr(model.Cost.CacheWrite),
				Source:         SourceModelsDev,
			}
			current, seen := best[key]
			if !seen || betterListing(providerID, price, current.provider, current.price) {
				best[key] = candidate{provider: providerID, price: price}
			}
		}
	}

	out := make([]store.ModelPrice, 0, len(best))
	for _, entry := range best {
		out = append(out, entry.price)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelKey < out[j].ModelKey })
	return out, skipped, nil
}

// betterListing decides between two providers listing the same model: the first-party
// publisher wins, and otherwise the cheaper rate does.
func betterListing(candidateProvider string, candidate store.ModelPrice, currentProvider string, current store.ModelPrice) bool {
	candidateFirstParty := isFirstParty(candidateProvider, candidate.ModelKey)
	if candidateFirstParty != isFirstParty(currentProvider, current.ModelKey) {
		return candidateFirstParty
	}
	if candidate.InputPerM != current.InputPerM {
		return candidate.InputPerM < current.InputPerM
	}
	if candidate.OutputPerM != current.OutputPerM {
		return candidate.OutputPerM < current.OutputPerM
	}
	return candidateProvider < currentProvider
}

// firstPartyProviders maps a model-id prefix to the provider names that publish that model
// itself, so a reseller's discounted listing does not become the estimate for a first-party
// model.
//
// Several names are accepted per family because the two catalogs name providers differently
// (models.dev says "google", LiteLLM says "gemini" or "vertex_ai" for the same models).
var firstPartyProviders = []struct {
	prefix    string
	providers []string
}{
	{"claude", []string{"anthropic"}},
	{"gpt", []string{"openai"}},
	{"o1", []string{"openai"}},
	{"o3", []string{"openai"}},
	{"o4", []string{"openai"}},
	{"gemini", []string{"google", "gemini", "vertex_ai"}},
	{"gemma", []string{"google", "gemini"}},
	{"grok", []string{"xai"}},
	{"kimi", []string{"moonshotai", "moonshot", "moonshotai-cn"}},
	{"deepseek", []string{"deepseek"}},
	{"qwen", []string{"alibaba", "alibaba-cn", "dashscope", "qwen"}},
	{"glm", []string{"zhipuai", "zhipu", "z-ai"}},
	{"mistral", []string{"mistral"}},
	{"llama", []string{"meta", "meta-llama"}},
}

func isFirstParty(provider, modelKey string) bool {
	for _, entry := range firstPartyProviders {
		if !strings.HasPrefix(modelKey, entry.prefix) {
			continue
		}
		for _, name := range entry.providers {
			if provider == name {
				return true
			}
		}
	}
	return false
}

// providerOfKey extracts the leading provider segment of a LiteLLM catalog key
// ("openai/gpt-5.6-sol" -> "openai"); a key without a segment returns "".
func providerOfKey(key string) string {
	if i := strings.IndexByte(key, '/'); i > 0 {
		return key[:i]
	}
	return ""
}

// litellmEntry is one entry of LiteLLM's flat catalog. Rates are per token there, so they are
// scaled by 1e6 on the way in.
type litellmEntry struct {
	Mode                        string   `json:"mode"`
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
}

func (c *Catalog) fetchLiteLLM(ctx context.Context) ([]store.ModelPrice, int, error) {
	body, err := download(ctx, c.liteLLMURL)
	if err != nil {
		return nil, 0, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("decode LiteLLM catalog: %w", err)
	}

	// The same model appears under several providers at different rates, and the map has no
	// order, so one listing is chosen deterministically per model: the first-party publisher
	// when present, otherwise the cheapest.
	type candidate struct {
		provider string
		price    store.ModelPrice
	}
	best := map[string]candidate{}
	skipped := 0

	keys := make([]string, 0, len(doc))
	for id := range doc {
		keys = append(keys, id)
	}
	sort.Strings(keys)

	for _, id := range keys {
		if id == "sample_spec" {
			continue
		}
		var entry litellmEntry
		if err := json.Unmarshal(doc[id], &entry); err != nil {
			skipped++
			continue
		}
		// Non-chat deployments (embeddings, audio, image) have no token accounting that matches
		// a session's usage.
		switch strings.ToLower(entry.Mode) {
		case "chat", "completion", "responses":
		default:
			skipped++
			continue
		}
		if entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil {
			skipped++
			continue
		}
		key := ModelKey(id)
		if key == "" {
			skipped++
			continue
		}
		price := store.ModelPrice{
			ModelKey:       key,
			InputPerM:      scale(valueOr(entry.InputCostPerToken)),
			OutputPerM:     scale(valueOr(entry.OutputCostPerToken)),
			CacheReadPerM:  scale(valueOr(entry.CacheReadInputTokenCost)),
			CacheWritePerM: scale(valueOr(entry.CacheCreationInputTokenCost)),
			Source:         SourceLiteLLM,
		}
		provider := providerOfKey(id)
		current, seen := best[key]
		if !seen || betterListing(provider, price, current.provider, current.price) {
			best[key] = candidate{provider: provider, price: price}
		}
	}

	out := make([]store.ModelPrice, 0, len(best))
	for _, entry := range best {
		out = append(out, entry.price)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelKey < out[j].ModelKey })
	return out, skipped, nil
}

func valueOr(v *float64) float64 {
	if v == nil || *v < 0 {
		return 0
	}
	return *v
}

func scale(perToken float64) float64 { return perToken * 1e6 }
