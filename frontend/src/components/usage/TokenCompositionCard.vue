<script setup lang="ts">
import { computed } from "vue";
import ChartBox from "@/components/charts/ChartBox.vue";
import Card from "@/components/ui/Card.vue";
import type { Totals } from "@/api/dashboard";
import { baseChartOptions, seriesColor } from "@/lib/chart";
import { formatTokens, formatUSD } from "@/lib/format";

/** Token composition: input, output, cache read, cache write. */
const props = defineProps<{ totals: Totals }>();

const config = computed(() => {
  const buckets = [
    { label: "metrics.input", value: props.totals.input, color: seriesColor(0) },
    { label: "metrics.output", value: props.totals.output, color: seriesColor(1) },
    { label: "metrics.cacheRead", value: props.totals.cacheRead, color: seriesColor(2) },
    { label: "metrics.cacheWrite", value: props.totals.cacheWrite, color: seriesColor(3) },
  ];
  return {
    type: "doughnut" as const,
    data: {
      labels: buckets.map((b) => b.label),
      datasets: [
        {
          data: buckets.map((b) => b.value),
          backgroundColor: buckets.map((b) => b.color),
          borderWidth: 0,
        },
      ],
    },
    options: baseChartOptions({
      cutout: "62%",
      plugins: {
        legend: { display: false },
        tooltip: {
          callbacks: {
            label: (ctx: { label: string; raw: unknown }) => {
              const value = Number(ctx.raw) || 0;
              const sum = props.totals.total || 1;
              return `${ctx.label}: ${formatTokens(value)} (${((value / sum) * 100).toFixed(1)}%)`;
            },
          },
        },
      },
      scales: {},
    }),
  };
});
</script>

<template>
  <Card :title="$t('overview.composition')">
    <ChartBox :config="config" :height="200" />
    <div class="legend" style="margin-top: 10px; justify-content: center">
      <span class="legend-item">
        <span class="share-dot" :style="{ background: 'var(--series-1)' }" />
        {{ $t('metrics.input') }} <span class="num">{{ formatTokens(props.totals.input) }}</span>
      </span>
      <span class="legend-item">
        <span class="share-dot" :style="{ background: 'var(--series-2)' }" />
        {{ $t('metrics.output') }} <span class="num">{{ formatTokens(props.totals.output) }}</span>
      </span>
      <span class="legend-item">
        <span class="share-dot" :style="{ background: 'var(--series-3)' }" />
        {{ $t('metrics.cacheRead') }} <span class="num">{{ formatTokens(props.totals.cacheRead) }}</span>
      </span>
      <span class="legend-item">
        <span class="share-dot" :style="{ background: 'var(--series-4)' }" />
        {{ $t('metrics.cacheWrite') }} <span class="num">{{ formatTokens(props.totals.cacheWrite) }}</span>
      </span>
      <span class="spacer" />
      <span class="faint">{{ $t('metrics.cost') }} {{ formatUSD(props.totals.costUsd) }}</span>
    </div>
  </Card>
</template>