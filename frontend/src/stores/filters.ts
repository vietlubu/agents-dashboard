import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type { Granularity, GranularityChoice, RangeQuery } from "@/api/dashboard";
import { addDays, localMidnight, parseDay, PRESETS, resolvePreset, type PresetKey } from "@/lib/range";
import { useSettingsStore } from "@/stores/settings";

/**
 * The filter state every data view shares, so switching pages keeps the same window and
 * the same harness/model selection instead of silently resetting.
 */
export const useFiltersStore = defineStore("filters", () => {
  const settings = useSettingsStore();

  const preset = ref<PresetKey>("7d");
  // Which bucket size the trend is drawn at. "auto" follows the range: a window under two
  // days has at most two day buckets, which shows nothing useful, so it is drawn by hour.
  const granularity = ref<GranularityChoice>("auto");
  const customFrom = ref("");
  const customTo = ref("");
  const harness = ref<string[]>([]);
  const models = ref<string[]>([]);
  const projects = ref<string[]>([]);
  const agentTypes = ref<string[]>([]);
  const outcomes = ref<string[]>([]);
  const costSources = ref<string[]>([]);

  const resolvedRange = computed(() => {
    const tz = settings.timezone;
    if (preset.value === "custom") {
      const from = parseDay(customFrom.value);
      const to = parseDay(customTo.value);
      if (from && to) {
        // The end date is inclusive, so the exclusive upper bound is the next day's start.
        return { fromMs: localMidnight(tz, from), toMs: localMidnight(tz, addDays(to, 1)) };
      }
    }
    return resolvePreset(preset.value, tz);
  });

  /**
   * The bucket size actually requested. The automatic choice is hourly under 48 hours and
   * daily otherwise; an explicit choice always wins.
   */
  const resolvedGranularity = computed<Granularity>(() => {
    if (granularity.value !== "auto") return granularity.value;
    const span = resolvedRange.value.toMs - resolvedRange.value.fromMs;
    return span < 48 * 60 * 60 * 1000 ? "hour" : "day";
  });

  /** The query sent to the backend: range plus every active filter. */
  const query = computed<RangeQuery>(() => ({
    fromMs: resolvedRange.value.fromMs,
    toMs: resolvedRange.value.toMs,
    harness: harness.value,
    models: models.value,
    projects: projects.value,
    agentTypes: agentTypes.value,
    outcomes: outcomes.value,
    costSources: costSources.value,
    sessionIds: [],
  }));

  function clearFilters() {
    harness.value = [];
    models.value = [];
    projects.value = [];
    agentTypes.value = [];
    outcomes.value = [];
    costSources.value = [];
  }

  const activeFilterCount = computed(
    () =>
      harness.value.length +
      models.value.length +
      projects.value.length +
      agentTypes.value.length +
      outcomes.value.length +
      costSources.value.length
  );

  return {
    preset,
    granularity,
    resolvedGranularity,
    customFrom,
    customTo,
    harness,
    models,
    projects,
    agentTypes,
    outcomes,
    costSources,
    presets: PRESETS,
    resolvedRange,
    query,
    activeFilterCount,
    clearFilters,
  };
});