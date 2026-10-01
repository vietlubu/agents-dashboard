<script setup lang="ts">
import { computed } from "vue";
import { formatTokens } from "@/lib/format";

/**
 * A proportional bar list. Used for every "share by X" card, so the colour assigned to a key
 * stays stable while the list is on screen.
 */
const props = withDefaults(
  defineProps<{
    rows: { key: string; total: number; costUsd?: number }[];
    limit?: number;
    showCost?: boolean;
    /** Fixed colour order so two cards showing the same keys agree. */
    paletteOffset?: number;
  }>(),
  { limit: 8, showCost: false, paletteOffset: 0 }
);

const COLORS = [
  "var(--series-1)",
  "var(--series-2)",
  "var(--series-3)",
  "var(--series-4)",
  "var(--series-5)",
  "var(--series-6)",
  "var(--series-7)",
  "var(--series-8)",
];

const shown = computed(() => {
  const sorted = [...props.rows].sort((a, b) => b.total - a.total);
  const top = sorted.slice(0, props.limit);
  const rest = sorted.slice(props.limit);
  const out = top.map((row, index) => ({
    ...row,
    color: COLORS[(index + props.paletteOffset) % COLORS.length],
  }));
  if (rest.length > 0) {
    out.push({
      key: "other",
      total: rest.reduce((sum, r) => sum + r.total, 0),
      color: "var(--text-faint)",
    });
  }
  return out;
});

const max = computed(() => shown.value.reduce((m, row) => Math.max(m, row.total), 0));
const sum = computed(() => props.rows.reduce((s, row) => s + row.total, 0));
</script>

<template>
  <div class="share-list">
    <div v-if="shown.length === 0" class="empty">{{ $t('common.noData') }}</div>
    <div v-for="row in shown" :key="row.key" class="share-row">
      <div class="share-label">
        <span class="share-dot" :style="{ background: row.color }" />
        <span class="share-name mono" :title="row.key">{{ row.key }}</span>
      </div>
      <div class="num nowrap">
        {{ formatTokens(row.total) }}
        <span class="faint">· {{ ((row.total / (sum || 1)) * 100).toFixed(0) }}%</span>
      </div>
      <div class="share-bar bar">
        <span
          :style="{
            width: (max ? (row.total / max) * 100 : 0) + '%',
            background: row.color,
          }"
        />
      </div>
    </div>
  </div>
</template>