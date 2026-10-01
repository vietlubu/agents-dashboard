import { defineStore } from "pinia";
import { ref } from "vue";
import * as meta from "@/api/meta";

/**
 * Filter menus need the distinct harnesses, models and projects. These change only when a
 * scan writes new data, so they are cached and refreshed on demand rather than on every
 * view mount.
 */
export const useMetaStore = defineStore("meta", () => {
  const harnesses = ref<meta.HarnessInfo[]>([]);
  const models = ref<string[]>([]);
  const projects = ref<string[]>([]);
  const agentTypes = ref<string[]>([]);
  const outcomes = ref<string[]>([]);
  const costSources = ref<string[]>([]);
  const loaded = ref(false);

  const error = ref<string | null>(null);

  async function load(force = false) {
    if (loaded.value && !force) return;
    try {
      await fetchAll();
      loaded.value = true;
      error.value = null;
    } catch (err) {
      // A failed facet load would otherwise leave every filter menu silently empty.
      error.value = String(err);
      console.error("facet load failed", err);
    }
  }

  async function fetchAll() {
    const [h, m, p, a, o, c] = await Promise.all([
      meta.harnesses(),
      meta.facets("model"),
      meta.facets("project"),
      meta.facets("agentType"),
      meta.facets("outcome"),
      meta.facets("costSource"),
    ]);
    harnesses.value = h;
    models.value = m.map((v) => v.value);
    projects.value = p.map((v) => v.value);
    agentTypes.value = a.map((v) => v.value);
    outcomes.value = o.map((v) => v.value);
    costSources.value = c.map((v) => v.value);
  }

  return { harnesses, models, projects, agentTypes, outcomes, costSources, loaded, error, load };
});