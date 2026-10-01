<script setup lang="ts">
import { useFiltersStore } from "@/stores/filters";
import { PRESETS } from "@/lib/range";

/**
 * The shared time window control. Presets resolve in the configured timezone; the custom
 * option takes two dates and treats the end date as inclusive.
 */
const filters = useFiltersStore();

function selectPreset(key: (typeof PRESETS)[number]["key"] | "custom") {
  filters.preset = key;
}
</script>

<template>
  <div class="row-wrap">
    <div class="seg">
      <button
        v-for="preset in PRESETS"
        :key="preset.key"
        :class="{ active: filters.preset === preset.key }"
        @click="selectPreset(preset.key)"
      >
        {{ $t('range.' + preset.key) }}
      </button>
      <button :class="{ active: filters.preset === 'custom' }" @click="selectPreset('custom')">
        {{ $t('range.custom') }}
      </button>
    </div>

    <template v-if="filters.preset === 'custom'">
      <input v-model="filters.customFrom" class="input" type="date" :title="$t('range.from')" />
      <span class="faint">→</span>
      <input v-model="filters.customTo" class="input" type="date" :title="$t('range.to')" />
    </template>
  </div>
</template>