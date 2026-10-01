<script setup lang="ts">
import { computed } from "vue";
import ChartBox from "@/components/charts/ChartBox.vue";
import Card from "@/components/ui/Card.vue";
import type { LatencyPoint } from "@/api/dashboard";
import { baseChartOptions, resolveColor, seriesColor } from "@/lib/chart";
import { formatMs, formatTokens } from "@/lib/format";

/**
 * Latency against token size, coloured by harness.
 *
 * Only OpenCode and omp record timings, so this chart is described as partial rather than
 * being filled with zeros for the harnesses that report nothing.
 */
const props = defineProps<{ points: LatencyPoint[] }>();

const harnesses = computed(() => {
  const seen: string[] = [];
  for (const point of props.points) {
    if (!seen.includes(point.harness)) seen.push(point.harness);
  }
  return seen;
});

const config = computed(() => ({
  type: "scatter" as const,
  data: {
    datasets: harnesses.value.map((harness, index) => ({
      label: harness,
      data: props.points
        .filter((p) => p.harness === harness)
        .map((p) => ({ x: p.total, y: p.latencyMs })),
      backgroundColor: resolveColor(seriesColor(index)) + "cc",
      pointRadius: 3,
      pointHoverRadius: 5,
    })),
  },
  options: baseChartOptions({
    plugins: {
      legend: { display: true, labels: { color: "#98a0b3", boxWidth: 10 } },
      tooltip: {
        callbacks: {
          label: (ctx: { raw: { x: number; y: number } }) =>
            `${formatTokens(ctx.raw.x)} tokens · ${formatMs(ctx.raw.y)}`,
        },
      },
    },
    scales: {
      x: {
        title: { display: true, text: "tokens", color: "#98a0b3" },
        grid: { color: "#262a35" },
        ticks: { color: "#98a0b3", callback: (v: number) => formatTokens(v) },
      },
      y: {
        title: { display: true, text: "latency (ms)", color: "#98a0b3" },
        grid: { color: "#262a35" },
        ticks: { color: "#98a0b3" },
      },
    },
  }),
}));
</script>

<template>
  <Card :title="$t('realtime.latencyScatter')" :subtitle="$t('realtime.latencyCoverage')">
    <div v-if="props.points.length === 0" class="empty">{{ $t('common.noData') }}</div>
    <ChartBox v-else :config="config" :height="260" />
  </Card>
</template>