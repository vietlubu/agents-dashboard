<script setup lang="ts">
import { onMounted } from "vue";
import MultiSelect from "@/components/ui/MultiSelect.vue";
import { useFiltersStore } from "@/stores/filters";
import { useMetaStore } from "@/stores/meta";

/** The shared filter bar: harness, model, project, agent type, outcome, cost source. */
const filters = useFiltersStore();
const meta = useMetaStore();

onMounted(() => void meta.load());

function toggleHarness(value: string[]) {
  filters.harness = value;
}
</script>

<template>
  <div class="row-wrap">
    <MultiSelect
      :model-value="filters.harness"
      :options="meta.harnesses.map((h) => h.id)"
      :label="$t('filters.harness')"
      @update:model-value="toggleHarness"
    />
    <MultiSelect
      v-model="filters.models"
      :options="meta.models"
      :label="$t('filters.model')"
    />
    <MultiSelect
      v-model="filters.projects"
      :options="meta.projects"
      :label="$t('filters.project')"
    />
    <MultiSelect
      v-model="filters.agentTypes"
      :options="meta.agentTypes"
      :label="$t('filters.agentType')"
    />
    <MultiSelect
      v-model="filters.outcomes"
      :options="meta.outcomes"
      :label="$t('filters.outcome')"
    />
    <MultiSelect
      v-model="filters.costSources"
      :options="meta.costSources"
      :label="$t('filters.costSource')"
    />
    <button v-if="filters.activeFilterCount" class="btn" @click="filters.clearFilters()">
      {{ $t('filters.clear') }}
    </button>
  </div>
</template>