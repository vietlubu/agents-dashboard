// Events API: listing, column set, and the client-side download of an export.
import * as Events from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/eventsservice";
import type * as StoreModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/store/models";

export type EventRow = StoreModels.EventRow;
export type ExportResult = StoreModels.ExportResult;
export type RangeQuery = StoreModels.RangeQuery;

/** A page whose list and count are always present; nullability is handled here. */
export interface EventPage {
  rows: EventRow[];
  total: number;
}

export const columns = Events.Columns as () => Promise<string[]>;

// The export result is normalised because a null payload would otherwise surface as a
// download of "null" in the browser.
export async function exportEvents(q: RangeQuery, format: string): Promise<ExportResult> {
  const result = await Events.Export(q, format);
  return {
    filename: result?.filename ?? "export",
    mimeType: result?.mimeType ?? "text/plain",
    content: result?.content ?? "",
    rows: result?.rows ?? 0,
    truncated: result?.truncated ?? false,
  };
}

// List normalises both list and count: a page with no rows still needs a Total to display.
export async function list(q: RangeQuery, offset: number, limit: number): Promise<EventPage> {
  const page = await Events.List(q, offset, limit);
  return { rows: page?.rows ?? [], total: page?.total ?? 0 };
}

/**
 * Downloads an export produced by the backend.
 *
 * The backend returns the rendered content instead of writing a file, because in headless
 * server mode there is no native save dialog; the browser download is the one path that
 * works in both modes.
 */
export function download(result: ExportResult): void {
  const blob = new Blob([result.content ?? ""], { type: result.mimeType || "text/plain" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = result.filename || "export";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  // Revoking immediately can cancel the download in some engines; one tick is enough.
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}