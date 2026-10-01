import { defineStore } from "pinia";
import { computed, ref } from "vue";
import { Events } from "@wailsio/runtime";
import * as api from "@/api/app";
import { EVENTS } from "@/api/sync";

export const useUpdateStore = defineStore("update", () => {
  const status = ref<api.UpdateStatus | null>(null);
  const pending = ref(false);
  const localError = ref("");
  const error = computed(() => status.value?.error || localError.value);
  const busy = computed(() => pending.value || ["checking", "installing", "restarting"].includes(status.value?.state ?? ""));

  let unsubscribe: (() => void) | undefined;
  let generation = 0;

  async function refreshStatus(fallbackError = "") {
    const request = ++generation;
    try {
      const snapshot = await api.updateStatus();
      if (request === generation) {
        status.value = snapshot;
        localError.value = fallbackError;
      }
    } catch (err) {
      if (request === generation) localError.value = fallbackError || String(err);
    }
  }

  function start() {
    if (unsubscribe) return;
    unsubscribe = Events.On(EVENTS.appUpdate, (event: { data: api.UpdateStatus }) => {
      // An event is newer than any in-flight snapshot or check response.
      generation += 1;
      status.value = event.data;
      localError.value = "";
    });
    void refreshStatus();
  }

  function stop() {
    unsubscribe?.();
    unsubscribe = undefined;
    generation += 1;
  }

  async function checkForUpdates() {
    if (busy.value) return;
    const subscription = unsubscribe;
    pending.value = true;
    localError.value = "";
    const request = ++generation;
    try {
      const snapshot = await api.checkForUpdates();
      if (request === generation) status.value = snapshot;
    } catch (err) {
      // A rejected binding call has no snapshot; recover the backend's current state.
      if (subscription === unsubscribe) {
        await refreshStatus(request === generation ? String(err) : "");
      }
    } finally {
      pending.value = false;
    }
  }

  async function installUpdate(expectedVersion: string) {
    if (busy.value) return;
    const subscription = unsubscribe;
    pending.value = true;
    localError.value = "";
    const request = ++generation;
    try {
      await api.installUpdate(expectedVersion);
    } catch (err) {
      if (subscription === unsubscribe) {
        await refreshStatus(request === generation ? String(err) : "");
      }
    } finally {
      pending.value = false;
    }
  }

  return { status, busy, error, start, stop, checkForUpdates, installUpdate };
});
