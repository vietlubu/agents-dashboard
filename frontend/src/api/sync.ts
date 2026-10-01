// Sync API and the event stream.
import * as Sync from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/syncservice";
import type * as ServiceModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/models";
import type * as StoreModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/store/models";
import type * as SyncModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/sync/models";

export type SyncStatus = ServiceModels.SyncStatus;
export type RunSummary = SyncModels.RunSummary;
export type ProgressEvent = SyncModels.ProgressEvent;
export type DoneEvent = SyncModels.DoneEvent;
export type ErrorEvent = SyncModels.ErrorEvent;
export type StateEvent = SyncModels.StateEvent;
export type SyncRun = StoreModels.SyncRun;

export const status = Sync.Status as () => Promise<SyncStatus>;
export const triggerNow = Sync.TriggerNow as () => Promise<boolean>;
export const cancel = Sync.Cancel as () => Promise<boolean>;
export const lastRun = Sync.LastRun as () => Promise<RunSummary>;

export async function history(limit = 20): Promise<SyncRun[]> {
  return (await Sync.History(limit)) ?? [];
}

/** Event names, mirroring the Go constants. */
export const EVENTS = {
  state: "sync:state",
  progress: "sync:progress",
  done: "sync:done",
  error: "sync:error",
  dataChanged: "data:changed",
  pricingSynced: "pricing:synced",
  settingsSaved: "settings:saved",
  appUpdate: "app:update",
} as const;