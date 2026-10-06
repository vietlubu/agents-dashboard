// Sleep control API: controller status, explicit legacy recovery and today's usage.
import * as Sleep from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/sleepservice";
import type * as SleepModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/sleep/models";
import type * as StoreModels from "@bindings/github.com/vietlubu/agents-dashboard/internal/store/models";

export type SleepStatus = SleepModels.Status;
export type Totals = StoreModels.Totals;

export const status = Sleep.Status as () => Promise<SleepStatus>;
export const today = Sleep.Today as () => Promise<Totals>;
export const restoreClamshell = Sleep.RestoreClamshell as () => Promise<void>;
