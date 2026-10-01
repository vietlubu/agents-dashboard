import { defineStore } from "pinia";
import { computed, ref } from "vue";
import { Events } from "@wailsio/runtime";
import * as api from "@/api/sync";
import type { ErrorEvent, ProgressEvent } from "@/api/sync";

/**
 * Scan state shared by every view.
 *
 * Two jobs: show whether a scan is running, and act as the refetch signal. Rather than each
 * view polling, the backend emits `data:changed` once per scan that actually wrote something,
 * and views watch `dataVersion` to reload.
 */
export const useSyncStore = defineStore("sync", () => {
  const running = ref(false);
  const trigger = ref("");
  const nextRunAt = ref(0);
  const intervalMs = ref(0);
  const burst = ref(false);
  const progress = ref<ProgressEvent | null>(null);
  const lastError = ref<string | null>(null);
  const lastFinishedAt = ref(0);
  const inserted = ref(0);
  const updated = ref(0);
  const cold = ref(false);

  // Bumped whenever the backend wrote data; views watch it instead of polling.
  const dataVersion = ref(0);

  let started = false;

  async function refreshStatus() {
    const status = await api.status();
    running.value = status.running;
    nextRunAt.value = status.nextRunAt;
    intervalMs.value = status.intervalMs;
    burst.value = status.burst;
  }

  /** Subscribes to the backend event stream exactly once. */
  function start() {
    if (started) return;
    started = true;

    Events.On(api.EVENTS.state, (event: { data: { running: boolean; trigger: string } }) => {
      running.value = event.data?.running ?? false;
      trigger.value = event.data?.trigger ?? "";
      // A finished scan invalidates the schedule, so pull it rather than guessing.
      if (!running.value) void refreshStatus();
    });

    Events.On(api.EVENTS.progress, (event: { data: ProgressEvent }) => {
      progress.value = event.data ?? null;
    });

    Events.On(api.EVENTS.done, (event) => {
      inserted.value = event.data?.eventsInserted ?? 0;
      updated.value = event.data?.eventsUpdated ?? 0;
      cold.value = event.data?.cold ?? false;
      lastFinishedAt.value = Date.now();
      progress.value = null;
    });

    Events.On(api.EVENTS.error, (event: { data: ErrorEvent }) => {
      lastError.value = event.data?.message ?? null;
    });

    Events.On(api.EVENTS.dataChanged, () => {
      dataVersion.value += 1;
    });

    void refreshStatus();
  }

  async function triggerNow() {
    lastError.value = null;
    return api.triggerNow();
  }

  async function cancel() {
    return api.cancel();
  }

  const statusLabel = computed(() => {
    if (running.value) return "Scanning…";
    if (progress.value) return `${progress.value.harness} ${progress.value.phase}`;
    return "Idle";
  });

  return {
    running,
    trigger,
    nextRunAt,
    intervalMs,
    burst,
    progress,
    lastError,
    lastFinishedAt,
    inserted,
    updated,
    cold,
    dataVersion,
    statusLabel,
    start,
    refreshStatus,
    triggerNow,
    cancel,
  };
});