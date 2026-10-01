// Application-level API that does not belong to a page.
import * as App from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/appservice";
import type * as ServiceModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/models";

export type UpdateStatus = ServiceModels.UpdateStatus;

/** Startup warnings (for example an unusable timezone) that would otherwise only reach the log. */
export const warnings = App.Warnings as () => Promise<string[]>;

/** The build version reported by the binary. */
export const version = App.Version as () => Promise<string>;

/** Update state is shared across the app and pushed through app:update events. */
export const updateStatus = App.UpdateStatus as () => Promise<UpdateStatus>;
export const checkForUpdates = App.CheckForUpdates as () => Promise<UpdateStatus>;
export const installUpdate = App.InstallUpdate as (expectedVersion: string) => Promise<void>;