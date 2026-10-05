<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(
  defineProps<{
    label: string;
    value: string;
    /** Raw numbers for the delta, so the percentage is computed, not passed in. */
    current?: number;
    previous?: number;
    hint?: string;
    /** For metrics where a rise is bad (cost), the delta colour is inverted. */
    invertDelta?: boolean;
  }>(),
  { current: undefined, previous: undefined, hint: "", invertDelta: false }
);

const delta = computed(() => {
  if (props.current === undefined || props.previous === undefined) return null;
  if (props.previous === 0 && props.current === 0) return null;
  if (props.previous === 0) return { text: "new", up: true, flat: false };
  const change = ((props.current - props.previous) / props.previous) * 100;
  return {
    text: (change >= 0 ? "+" : "") + change.toFixed(1) + "%",
    up: change >= 0,
    flat: Math.abs(change) < 0.05,
  };
});

const deltaClass = computed(() => {
  if (!delta.value || delta.value.flat) return "faint";
  const good = props.invertDelta ? !delta.value.up : delta.value.up;
  return good ? "delta-up" : "delta-down";
});
</script>

<template>
  <div class="stat">
    <div class="stat-label">{{ props.label }}</div>
    <div class="stat-value">{{ props.value }}</div>
    <div v-if="delta" class="stat-delta" :class="deltaClass">
      {{ delta.text }}
      <span class="faint">vs previous</span>
    </div>
    <div v-else-if="props.hint" class="stat-delta faint">{{ props.hint }}</div>
    <slot name="extra" />
  </div>
</template>