<script setup lang="ts">
import { computed } from "vue";
import type { HeatCell } from "@/api/dashboard";
import { formatTokens, formatUSD } from "@/lib/format";

/**
 * GitHub-style activity grid: one column per week, one cell per day, intensity by tokens.
 *
 * The axis is built from the range rather than from the data, so a day with no usage is an
 * empty cell in its correct place instead of shifting every later day left.
 */
const props = defineProps<{
  cells: HeatCell[];
  /** Day keys (YYYY-MM-DD) in order, covering the window. */
  days: string[];
}>();

const byDay = computed(() => {
  const map = new Map<string, HeatCell>();
  for (const cell of props.cells) map.set(cell.day, cell);
  return map;
});

const max = computed(() => {
  let m = 0;
  for (const cell of props.cells) m = Math.max(m, cell.total);
  return m;
});

/** Leading blanks so the first column starts on the correct weekday. */
const lead = computed(() => {
  if (props.days.length === 0) return 0;
  const first = props.days[0];
  const [y, m, d] = first.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d)).getUTCDay();
});

const grid = computed(() => {
  const out: (string | null)[] = [];
  for (let i = 0; i < lead.value; i++) out.push(null);
  for (const day of props.days) out.push(day);
  return out;
});

function level(day: string | null): number {
  if (!day) return -1;
  const cell = byDay.value.get(day);
  if (!cell || cell.total === 0) return 0;
  if (max.value <= 0) return 1;
  const ratio = cell.total / max.value;
  if (ratio > 0.75) return 4;
  if (ratio > 0.5) return 3;
  if (ratio > 0.25) return 2;
  return 1;
}

function title(day: string | null): string {
  if (!day) return "";
  const cell = byDay.value.get(day);
  if (!cell) return `${day} · no usage`;
  return `${day} · ${formatTokens(cell.total)} tokens · ${formatUSD(cell.costUsd)} · ${cell.events} events`;
}
</script>

<template>
  <div>
    <div class="heatmap">
      <div
        v-for="(day, index) in grid"
        :key="day ?? 'blank-' + index"
        class="heat-cell"
        :style="level(day) <= 0 ? undefined : { background: 'var(--heat-' + level(day) + ')' }"
        :title="title(day)"
      />
    </div>
    <div class="legend" style="margin-top: 8px">
      <span class="faint">{{ $t('metrics.tokens') }}</span>
      <span class="heat-cell" style="background: var(--heat-0)" />
      <span class="heat-cell" style="background: var(--heat-1)" />
      <span class="heat-cell" style="background: var(--heat-2)" />
      <span class="heat-cell" style="background: var(--heat-3)" />
      <span class="heat-cell" style="background: var(--heat-4)" />
      <span class="spacer" />
      <span class="faint">max {{ formatTokens(max) }}</span>
    </div>
  </div>
</template>