// Metadata API: harnesses, filter facets, sessions.
import * as Meta from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/metaservice";
import type * as StoreModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/store/models";

export type HarnessInfo = StoreModels.HarnessInfo;
export type FacetValue = StoreModels.FacetValue;
export type SessionRow = StoreModels.SessionRow;
export type HarnessReport = StoreModels.HarnessReport;
export type RangeQuery = StoreModels.RangeQuery;

/** A page whose list and count are always present; nullability is handled here. */
export interface SessionPage {
  rows: SessionRow[];
  total: number;
}

/** A report row whose root list is guaranteed present. */
export type HarnessReportView = Omit<HarnessReport, "roots"> & { roots: string[] };

// Roots is nullable in the generated model; a report row without roots is normalised to [].
export async function harnessReport(): Promise<HarnessReportView[]> {
  const list = await Meta.RunReport();
  return (list ?? []).map((row) => ({ ...row, roots: row.roots ?? [] }));
}

// List responses: null (no rows) is normalised to an empty array.

export async function harnesses(): Promise<HarnessInfo[]> {
  return (await Meta.Harnesses()) ?? [];
}

export async function facets(dim: string): Promise<FacetValue[]> {
  return (await Meta.Facets(dim)) ?? [];
}

export async function sessions(
  q: RangeQuery,
  offset: number,
  limit: number,
  sortBy: string
): Promise<SessionPage> {
  const page = await Meta.Sessions(q, offset, limit, sortBy);
  return { rows: page?.rows ?? [], total: page?.total ?? 0 };
}

export async function sessionDetail(harness: string, sessionId: string) {
  const detail = await Meta.SessionDetail(harness, sessionId);
  return {
    session: detail?.session ?? null,
    totals: detail?.totals ?? null,
    byModel: detail?.byModel ?? [],
  };
}