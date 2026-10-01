<script setup lang="ts">
import { computed } from "vue";
import ChartBox from "@/components/charts/ChartBox.vue";
import Card from "@/components/ui/Card.vue";
import type { SeriesPoint } from "@/api/dashboard";
import { baseChartOptions, resolveColor, seriesColor } from "@/lib/chart";
import { formatTokens, formatTime } from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/** Per-minute usage for the realtime window. Buckets are minute-aligned epoch times. */
const props = defineProps<{ buckets: SeriesPoint[]; windowMinutes: number }>();

const settings = useSettingsStore();

const labels = computed(() =>
  props.buckets.map((b) => formatTime(Number(b.key), settings.timezone))
);

const config = computed(() => {
  const color = resolveColor(seriesColor(5));
  return {
    type: "bar" as const,
    data: {
      labels: labels.value,
      datasets: [
        {
          data: props.buckets.map((b) => b.total),
          backgroundColor: color + "cc",
          borderRadius: 2,
        },
      ],
    },
    options: baseChartOptions({
      plugins: {
        legend: { display: false },
        tooltip: {
          callbacks: {
            label: (ctx: { raw: unknown }) => formatTokens(Number(ctx.raw) || 0) + " tokens",
          },
        },
      },
    }),
  };
});

const tokensPerMinute = computed(() => {
  const total = props.buckets.reduce((sum, b) => sum + b.total, 0);
  return props.windowMinutes > 0 ? total / props.windowMinutes : 0;
});
</script>

<template>
  <Card :title="$t('realtime.velocity')" :subtitle="`${formatTokens(tokensPerMinute)} ${$t('metrics.tokensPerMinute')}`">
    <ChartBox :config="config" :height="200" />
  </Card>
</template>