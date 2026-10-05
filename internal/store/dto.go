package store

// Cost sources recorded on an event. These strings are load-bearing: they appear in the
// rollup aggregate SQL (reported_cost_usd, cost_unavailable) and in the UI, so they live
// in one place.
const (
	// CostSourceReported is an amount the harness itself recorded, including an explicit
	// zero. A reported amount always wins over a table estimate.
	CostSourceReported = "reported"
	// CostSourceEstimated is computed from the price table.
	CostSourceEstimated = "estimated"
	// CostSourceUnavailable means no price is known; cost_usd stays NULL so the UI shows a
	// dash instead of a misleading zero.
	CostSourceUnavailable = "unavailable"
)

// RangeQuery is the single filter struct shared by every read path.
// FromMs is inclusive, ToMs is exclusive. Zero means unbounded.
type RangeQuery struct {
	FromMs int64 `json:"fromMs"`
	ToMs   int64 `json:"toMs"`

	Harness     []string `json:"harness"`
	Models      []string `json:"models"`
	Projects    []string `json:"projects"`
	AgentTypes  []string `json:"agentTypes"`
	Outcomes    []string `json:"outcomes"`
	CostSources []string `json:"costSources"`
	// SessionIDs restricts the query to specific sessions. It forces the event source,
	// because neither daily rollup carries a session dimension.
	SessionIDs []string `json:"sessionIds"`
}

// Totals is an aggregate over a range.
type Totals struct {
	Events     int64   `json:"events"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	Reasoning  int64   `json:"reasoning"`
	Total      int64   `json:"total"`
	CostUSD    float64 `json:"costUsd"`
	// ReportedCostUSD is the part of CostUSD the source itself reported (as opposed
	// to a price-table estimate), so the UI can show coverage.
	ReportedCostUSD float64 `json:"reportedCostUsd"`
	CostUnavailable int64   `json:"costUnavailable"`

	LatencySumMs int64 `json:"latencySumMs"`
	LatencyCount int64 `json:"latencyCount"`
	TTFTSumMs    int64 `json:"ttftSumMs"`
	TTFTCount    int64 `json:"ttftCount"`
}

// Comparison is a current window against the adjacent previous window of equal length.
type Comparison struct {
	Current  Totals `json:"current"`
	Previous Totals `json:"previous"`
}

// SeriesPoint is one bucket of a time series (a day, or a group key).
// Key is a local day (YYYY-MM-DD) for day series, an ISO week (YYYY-Www) or month
// (YYYY-MM) for the coarser buckets, and an epoch-millisecond string for hour and minute
// buckets; the frontend formats the last two in the configured timezone.
type SeriesPoint struct {
	Key        string  `json:"key"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	Reasoning  int64   `json:"reasoning"`
	Total      int64   `json:"total"`
	CostUSD    float64 `json:"costUsd"`
	Events     int64   `json:"events"`
}

// SeriesResult is a time series together with the bucket size it was actually built at.
// The store may answer more coarsely than asked when the range would produce a chart no
// one can read, so the caller renders the granularity it was given, not the one it asked
// for.
type SeriesResult struct {
	Granularity string        `json:"granularity"`
	Points      []SeriesPoint `json:"points"`
}

// StackedPoint is one (day, dimension value) cell of a stacked series.
type StackedPoint struct {
	Day     string  `json:"day"`
	Key     string  `json:"key"`
	Total   int64   `json:"total"`
	CostUSD float64 `json:"costUsd"`
	Events  int64   `json:"events"`
}

// BreakdownRow is one row of a grouped breakdown.
type BreakdownRow struct {
	Key        string  `json:"key"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	Reasoning  int64   `json:"reasoning"`
	Total      int64   `json:"total"`
	CostUSD    float64 `json:"costUsd"`
	Events     int64   `json:"events"`
	LatencyAvg float64 `json:"latencyAvgMs"`
	LatencyCnt int64   `json:"latencyCount"`
}

// HeatCell is one day of the activity heatmap.
type HeatCell struct {
	Day     string  `json:"day"`
	Total   int64   `json:"total"`
	CostUSD float64 `json:"costUsd"`
	Events  int64   `json:"events"`
}

// LatencyPoint is one event with timing, for the scatter/latency views.
type LatencyPoint struct {
	TS        int64  `json:"ts"`
	LatencyMs int64  `json:"latencyMs"`
	TTFTMs    int64  `json:"ttftMs"`
	Model     string `json:"model"`
	Harness   string `json:"harness"`
	Total     int64  `json:"total"`
	Input     int64  `json:"input"`
	Output    int64  `json:"output"`
	Project   string `json:"project"`
	SessionID string `json:"sessionId"`
}

// EventRow is one row of the events table.
type EventRow struct {
	ID         int64    `json:"id"`
	Harness    string   `json:"harness"`
	TS         int64    `json:"ts"`
	LocalDay   string   `json:"localDay"`
	Project    string   `json:"project"`
	SessionID  string   `json:"sessionId"`
	Model      string   `json:"model"`
	Provider   string   `json:"provider"`
	AgentType  string   `json:"agentType"`
	AgentName  string   `json:"agentName"`
	Outcome    string   `json:"outcome"`
	Input      int64    `json:"input"`
	Output     int64    `json:"output"`
	CacheRead  int64    `json:"cacheRead"`
	CacheWrite int64    `json:"cacheWrite"`
	Reasoning  int64    `json:"reasoning"`
	Total      int64    `json:"total"`
	CostUSD    *float64 `json:"costUsd"`
	CostSource string   `json:"costSource"`
	LatencyMs  *int64   `json:"latencyMs"`
	TTFTMs     *int64   `json:"ttftMs"`
}

// EventPage is a paged slice of events and the total matching count.
type EventPage struct {
	Rows  []EventRow `json:"rows"`
	Total int64      `json:"total"`
}

// SessionRow is one row of the sessions page. Token/cost columns are lifetime sums
// for the session; the range filter selects sessions active in the range.
type SessionRow struct {
	Harness    string  `json:"harness"`
	SessionID  string  `json:"sessionId"`
	Project    string  `json:"project"`
	Model      string  `json:"model"`
	AgentType  string  `json:"agentType"`
	AgentName  string  `json:"agentName"`
	StartedAt  int64   `json:"startedAt"`
	UpdatedAt  int64   `json:"updatedAt"`
	Events     int64   `json:"events"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	Reasoning  int64   `json:"reasoning"`
	Total      int64   `json:"total"`
	CostUSD    float64 `json:"costUsd"`
	LatencyAvg float64 `json:"latencyAvgMs"`
	LatencyCnt int64   `json:"latencyCount"`
}

// SessionPage is a paged slice of sessions and the total matching count.
type SessionPage struct {
	Rows  []SessionRow `json:"rows"`
	Total int64        `json:"total"`
}

// FacetValue is one selectable filter value with its event count.
type FacetValue struct {
	Value  string `json:"value"`
	Events int64  `json:"events"`
	Total  int64  `json:"total"`
}

// RealtimeSnapshot is the realtime view payload.
type RealtimeSnapshot struct {
	WindowMinutes int64          `json:"windowMinutes"`
	Buckets       []SeriesPoint  `json:"buckets"`        // one point per minute
	ActiveSession []SessionRow   `json:"activeSessions"` // seen in the last 5 minutes
	ByModel       []BreakdownRow `json:"byModel"`
	Latency       []LatencyPoint `json:"latency"`
	Totals        Totals         `json:"totals"`
}

// ExportResult carries a rendered export back to the frontend, which turns it into a
// download. Returning content (instead of writing a file) keeps export working in
// headless server mode, where native save dialogs do not exist.
type ExportResult struct {
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Content  string `json:"content"`
	Rows     int64  `json:"rows"`
	// Truncated is true when the export hit the row cap.
	Truncated bool `json:"truncated"`
}

// Stats describes the local database.
type Stats struct {
	DBPath   string `json:"dbPath"`
	DBBytes  int64  `json:"dbBytes"`
	Events   int64  `json:"events"`
	Sessions int64  `json:"sessions"`
	FirstTS  int64  `json:"firstTs"`
	LastTS   int64  `json:"lastTs"`
	Unpriced int64  `json:"unpricedModels"`
	SyncRuns int64  `json:"syncRuns"`
}

// SyncRun is one recorded sync execution.
type SyncRun struct {
	ID             int64  `json:"id"`
	StartedAt      int64  `json:"startedAt"`
	FinishedAt     int64  `json:"finishedAt"`
	Trigger        string `json:"trigger"`
	FilesWalked    int64  `json:"filesWalked"`
	FilesChanged   int64  `json:"filesChanged"`
	EventsInserted int64  `json:"eventsInserted"`
	EventsUpdated  int64  `json:"eventsUpdated"`
	DurationMs     int64  `json:"durationMs"`
	Error          string `json:"error"`
}

// HarnessReport is one row of the per-harness report (settings page + scan-report tool).
type HarnessReport struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Available       bool     `json:"available"`
	Roots           []string `json:"roots"`
	Events          int64    `json:"events"`
	Sessions        int64    `json:"sessions"`
	Input           int64    `json:"input"`
	Output          int64    `json:"output"`
	CacheRead       int64    `json:"cacheRead"`
	CacheWrite      int64    `json:"cacheWrite"`
	Reasoning       int64    `json:"reasoning"`
	Total           int64    `json:"total"`
	CostUSD         float64  `json:"costUsd"`
	ReportedCostUSD float64  `json:"reportedCostUsd"`
	LatencyRows     int64    `json:"latencyRows"`
	TTFTRows        int64    `json:"ttftRows"`
	FirstTS         int64    `json:"firstTs"`
	LastTS          int64    `json:"lastTs"`
}

// HarnessInfo describes a harness for the UI (id, label, availability, roots).
type HarnessInfo struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Roots     []string `json:"roots"`
	Found     []string `json:"found"`
	Events    int64    `json:"events"`
}

// ScanRoot is a user-configured extra root.
type ScanRoot struct {
	Harness string `json:"harness"`
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
}

// ModelPrice is one row of the price table.
type ModelPrice struct {
	ModelKey       string  `json:"modelKey"`
	InputPerM      float64 `json:"inputPerM"`
	OutputPerM     float64 `json:"outputPerM"`
	CacheReadPerM  float64 `json:"cacheReadPerM"`
	CacheWritePerM float64 `json:"cacheWritePerM"`
	Source         string  `json:"source"`
	UpdatedAt      int64   `json:"updatedAt"`
}

// PriceRule is a per-model multiplier rule.
type PriceRule struct {
	ModelKey       string  `json:"modelKey"`
	InputMult      float64 `json:"inputMult"`
	OutputMult     float64 `json:"outputMult"`
	CacheReadMult  float64 `json:"cacheReadMult"`
	CacheWriteMult float64 `json:"cacheWriteMult"`
	Disabled       bool    `json:"disabled"`
}

// Settings is the persisted, user-editable configuration.
type Settings struct {
	TZ             string `json:"tz"`
	IdleIntervalS  int64  `json:"idleIntervalSeconds"`
	BurstIntervalS int64  `json:"burstIntervalSeconds"`
	BurstWindowS   int64  `json:"burstWindowSeconds"`
	Concurrency    int64  `json:"concurrency"`
	AutoSyncPrices bool   `json:"autoSyncPrices"`
	LastPriceSync  int64  `json:"lastPriceSync"`
	ServerHost     string `json:"serverHost"`
	ServerPort     int64  `json:"serverPort"`
	Theme          string `json:"theme"`
	Locale         string `json:"locale"`

	// Sleep control.
	SleepEnabled          bool  `json:"sleepEnabled"`
	SleepAfterS           int64 `json:"sleepAfterSeconds"`
	SleepActiveWindowS    int64 `json:"sleepActiveWindowSeconds"`
	PreventSystemSleep    bool  `json:"preventSystemSleep"`
	PreventDisplaySleep   bool  `json:"preventDisplaySleep"`
	PreventLidClosedSleep bool  `json:"preventLidClosedSleep"`
}

// SettingsPatch is a partial settings update; empty/zero fields are ignored.
type SettingsPatch struct {
	TZ             string `json:"tz"`
	IdleIntervalS  int64  `json:"idleIntervalSeconds"`
	BurstIntervalS int64  `json:"burstIntervalSeconds"`
	Concurrency    int64  `json:"concurrency"`
	AutoSyncPrices *bool  `json:"autoSyncPrices"`
	ServerHost     string `json:"serverHost"`
	ServerPort     int64  `json:"serverPort"`
	Theme          string `json:"theme"`
	Locale         string `json:"locale"`

	// Sleep control. The booleans are pointers so "off" is distinguishable from "unchanged".
	SleepEnabled          *bool `json:"sleepEnabled"`
	PreventSystemSleep    *bool `json:"preventSystemSleep"`
	PreventDisplaySleep   *bool `json:"preventDisplaySleep"`
	PreventLidClosedSleep *bool `json:"preventLidClosedSleep"`
	SleepAfterS           int64 `json:"sleepAfterSeconds"`
	SleepActiveWindowS    int64 `json:"sleepActiveWindowSeconds"`
}
