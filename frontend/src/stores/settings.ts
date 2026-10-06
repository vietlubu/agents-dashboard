import { defineStore } from "pinia";
import { computed, ref } from "vue";
import { Events } from "@wailsio/runtime";
import * as api from "@/api/settings";
import type { SettingsModel } from "@/api/settings";
import { applyTheme, type ThemeChoice } from "@/lib/theme";
import { EVENTS } from "@/api/sync";

/**
 * Application settings, backed by the database.
 *
 * The timezone lives here rather than in each view because it is the single knob that ties
 * the UI's day boundaries to the scan's day buckets; the backend rebuilds the rollups when
 * it changes, which is why `update` can be slow and reports progress through `saving`.
 */
export const useSettingsStore = defineStore("settings", () => {
  const settings = ref<SettingsModel | null>(null);
  const loading = ref(false);
  const saving = ref(false);
  const error = ref<string | null>(null);
  const warnings = ref<string[]>([]);

  let unsubscribe: (() => void) | undefined;
  let eventVersion = 0;

  const timezone = computed(() => settings.value?.tz ?? Intl.DateTimeFormat().resolvedOptions().timeZone);
  const theme = computed<ThemeChoice>(() => (settings.value?.theme as ThemeChoice) ?? "system");
  const locale = computed(() => settings.value?.locale ?? "en");

  async function load() {
    loading.value = true;
    error.value = null;
    const version = eventVersion;
    try {
      const value = await api.get();
      if (version === eventVersion) {
        settings.value = value;
        applyTheme(theme.value);
      }
    } catch (err) {
      error.value = String(err);
    } finally {
      loading.value = false;
    }
  }

  async function update(patch: api.SettingsPatch) {
    saving.value = true;
    error.value = null;
    const version = eventVersion;
    try {
      const value = await api.update(patch);
      if (version === eventVersion) {
        settings.value = value;
        applyTheme(theme.value);
      }
      return value;
    } catch (err) {
      error.value = String(err);
      throw err;
    } finally {
      saving.value = false;
    }
  }

  function start() {
    if (unsubscribe) return;
    unsubscribe = Events.On(EVENTS.settingsSaved, (event: { data: SettingsModel }) => {
      if (!event.data) return;
      eventVersion++;
      settings.value = event.data;
      applyTheme(theme.value);
    });
  }

  function stop() {
    unsubscribe?.();
    unsubscribe = undefined;
  }

  return { settings, loading, saving, error, warnings, timezone, theme, locale, load, update, start, stop };
});