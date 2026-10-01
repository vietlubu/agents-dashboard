import { onBeforeUnmount, ref, watch, type Ref, type WatchSource } from "vue";
import { useFiltersStore } from "@/stores/filters";
import { useSyncStore } from "@/stores/sync";

/**
 * Runs a read against the store and re-runs it whenever the filters, the data, or any extra
 * reactive source changes.
 *
 * `extraDeps` covers the parameters a view owns rather than the shared filters: the analysis
 * dimension, the realtime window, and a table's page offset. They have to be declared, because
 * a view-local value read inside the fetcher is invisible to this composable — a page that
 * forgot them would keep showing the previous query's rows.
 *
 * Two other details are easy to get wrong per view, so they live here: a scan emits several
 * `data:changed` signals in quick succession (reloads are debounced), and a slow response must
 * not overwrite a fresher one (only the newest request is applied).
 */
export function useLiveQuery<T>(fetcher: () => Promise<T>, initial: T, extraDeps: WatchSource[] = []) {
  const debounceMs = 400;
  const filters = useFiltersStore();
  const sync = useSyncStore();

  const data = ref(initial) as Ref<T>;
  const loading = ref(false);
  const error = ref<string | null>(null);

  let timer: ReturnType<typeof setTimeout> | undefined;
  let requestId = 0;

  async function reload() {
    const id = ++requestId;
    loading.value = true;
    try {
      const result = await fetcher();
      if (id === requestId) {
        data.value = result;
        error.value = null;
      }
    } catch (err) {
      if (id === requestId) error.value = String(err);
    } finally {
      if (id === requestId) loading.value = false;
    }
  }

  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(() => void reload(), debounceMs);
  }

  watch(() => filters.query, schedule, { deep: true, immediate: true });
  watch(() => sync.dataVersion, schedule);
  if (extraDeps.length > 0) {
    watch(extraDeps, schedule);
  }

  onBeforeUnmount(() => {
    clearTimeout(timer);
    timer = undefined;
  });

  return { data, loading, error, reload };
}