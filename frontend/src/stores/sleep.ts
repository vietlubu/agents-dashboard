import { defineStore } from "pinia";
import { computed, ref } from "vue";
import { Events } from "@wailsio/runtime";
import * as api from "@/api/sleep";
import { EVENTS } from "@/api/sync";

/**
 * Sleep controller status, pushed by the backend as `sleep:status`.
 *
 * The backend owns the decision (which platform assertions are held, whether a countdown
 * to sleep is running), so the UI reads a snapshot rather than recomputing it.
 */
export const useSleepStore = defineStore("sleep", () => {
  const status = ref<api.SleepStatus | null>(null);
  const error = ref("");

  let unsubscribe: (() => void) | undefined;

  const supported = computed(() => status.value?.supported ?? false);
  const keepingAwake = computed(() => status.value?.keepingAwake ?? false);

  async function refresh() {
    try {
      status.value = await api.status();
      error.value = "";
    } catch (err) {
      error.value = String(err);
    }
  }

  function start() {
    if (unsubscribe) return;
    unsubscribe = Events.On(EVENTS.sleepStatus, (event: { data: api.SleepStatus }) => {
      status.value = event.data ?? status.value;
    });
    void refresh();
  }

  function stop() {
    unsubscribe?.();
    unsubscribe = undefined;
  }

  return { status, error, supported, keepingAwake, start, stop, refresh };
});
