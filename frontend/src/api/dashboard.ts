// The API seam: components never import the generated bindings directly.
//
// Two things happen here.
//
// 1. Re-exports. A generated method is already the right shape, so it is re-exported under a
//    lower-case name instead of being wrapped in a function that adds nothing.
// 2. Null normalisation. Go's nil slice serialises as null, so every list-returning call is
//    normalised to an empty array. That contract is why those few wrappers exist: without it
//    each caller would have to repeat the `?? []`.
import * as Dashboard from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/dashboardservice";
import type * as StoreModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/store/models";

export type Totals = StoreModels.Totals;
export type Comparison = StoreModels.Comparison;
export type SeriesPoint = StoreModels.SeriesPoint;
export type StackedPoint = StoreModels.StackedPoint;
export type BreakdownRow = StoreModels.BreakdownRow;
export type HeatCell = StoreModels.HeatCell;
export type LatencyPoint = StoreModels.LatencyPoint;
export type RangeQuery = StoreModels.RangeQuery;

/** The realtime payload with its lists guaranteed present. */
export interface RealtimeSnapshot {
  windowMinutes: number;
  buckets: SeriesPoint[];
  activeSessions: StoreModels.SessionRow[];
  byModel: BreakdownRow[];
  latency: LatencyPoint[];
  totals: Totals;
}

export const totals = Dashboard.Totals as (q: RangeQuery) => Promise<Totals>;
export const compare = Dashboard.Compare as (q: RangeQuery) => Promise<Comparison>;
// The snapshot's lists are nullable in the generated model; an empty window must render as
// empty lists rather than as null.
export async function realtime(minutes: number): Promise<RealtimeSnapshot> {
  const snapshot = await Dashboard.Realtime(minutes);
  return {
    windowMinutes: snapshot?.windowMinutes ?? minutes,
    buckets: snapshot?.buckets ?? [],
    activeSessions: snapshot?.activeSessions ?? [],
    byModel: snapshot?.byModel ?? [],
    latency: snapshot?.latency ?? [],
    totals: snapshot?.totals ?? emptyTotals(),
  };
}
export const heatmap = Dashboard.Heatmap as (fromMs: number, toMs: number) => Promise<HeatCell[]>;
export const unpricedModels = Dashboard.UnpricedModels as () => Promise<string[]>;
export const dims = Dashboard.Dims as () => Promise<string[]>;

// List responses: null (no rows) is normalised to an empty array.

/** Bucket sizes a series can be drawn at. */
export type Granularity = "hour" | "day" | "week" | "month";
/** The bucket size the user asks for; "auto" follows the range. */
export type GranularityChoice = Granularity | "auto";

/**
 * A time series with the bucket size the store answered at. The store may answer more
 * coarsely than asked when an hourly chart would be unreadable, and its own model types the
 * grain as a plain string in case an older backend sends something unknown.
 */
export interface SeriesResult {
  granularity: Granularity;
  points: SeriesPoint[];
}

const GRANULARITIES = ["hour", "day", "week", "month"];

function asGranularity(value: string): Granularity {
  return GRANULARITIES.includes(value) ? (value as Granularity) : "day";
}

export async function series(q: RangeQuery, granularity: GranularityChoice): Promise<SeriesResult> {
  const result = await Dashboard.Series(q, granularity === "auto" ? "" : granularity);
  return { granularity: asGranularity(result.granularity), points: result.points ?? [] };
}

export async function stacked(q: RangeQuery, dim: string, top: number): Promise<StackedPoint[]> {
  return (await Dashboard.Stacked(q, dim, top)) ?? [];
}

export async function breakdown(q: RangeQuery, dim: string, limit = 50): Promise<BreakdownRow[]> {
  return (await Dashboard.Breakdown(q, dim, limit)) ?? [];
}

export async function latency(q: RangeQuery, limit = 2000): Promise<LatencyPoint[]> {
  return (await Dashboard.Latency(q, limit)) ?? [];
}

/** A zeroed aggregate, used when a window has no data at all. */
function emptyTotals(): Totals {
  return {
    events: 0,
    input: 0,
    output: 0,
    cacheRead: 0,
    cacheWrite: 0,
    reasoning: 0,
    total: 0,
    costUsd: 0,
    reportedCostUsd: 0,
    costUnavailable: 0,
    latencySumMs: 0,
    latencyCount: 0,
    ttftSumMs: 0,
    ttftCount: 0,
  };
}
