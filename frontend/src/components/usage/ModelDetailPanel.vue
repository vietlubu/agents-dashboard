<script setup lang="ts">
import { computed } from "vue";
import ChartBox from "@/components/charts/ChartBox.vue";
import type { Granularity, ModelSeriesPoint, ModelStats } from "@/api/dashboard";
import { baseChartOptions, mutedColor, seriesColor } from "@/lib/chart";
import {
  formatBucket,
  formatDateTime,
  formatMs,
  formatNumber,
  formatInt,
  formatPercent,
  formatTokens,
} from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/**
 * The expanded row: this model's totals beside how it behaved over time.
 *
 * Both series are averages over the buckets where the model actually ran, so a bucket it
 * skipped is absent rather than plotted as a zero — a zero would read as "answered slowly"
 * when it means "not asked".
 */
const props = defineProps<{
  row: ModelStats;
  /** Already filtered to this row's key and sorted ascending by bucket. */
  points: ModelSeriesPoint[];
  granularity: Granularity;
  color: string;
}>();

const settings = useSettingsStore();

const avgDuration = computed(() =>
  props.row.latencyCount > 0 ? props.row.latencySumMs / props.row.latencyCount : null,
);

const avgTTFT = computed(() =>
  props.row.ttftCount > 0 ? props.row.ttftSumMs / props.row.ttftCount : null,
);

const avgTPS = computed(() =>
  props.row.tpsCount > 0 ? props.row.tpsSum / props.row.tpsCount : null,
);

/** Per-bucket means; null where the bucket recorded no sample, which Chart.js skips. */
const tps = computed(() =>
  props.points.map((point) => (point.tpsCount > 0 ? point.tpsSum / point.tpsCount : null)),
);

const ttft = computed(() =>
  props.points.map((point) => (point.ttftCount > 0 ? point.ttftSumMs / point.ttftCount : null)),
);

const config = computed(() => ({
  type: "line" as const,
  data: {
    labels: props.points.map((point) =>
      formatBucket(point.bucket, props.granularity, settings.timezone),
    ),
    datasets: [
      {
        label: "tok/s",
        data: tps.value,
        borderColor: props.color,
        backgroundColor: props.color,
        yAxisID: "y",
        tension: 0.25,
        spanGaps: true,
      },
      {
        label: "ttft",
        data: ttft.value,
        borderColor: mutedColor(),
        backgroundColor: mutedColor(),
        borderDash: [4, 4],
        yAxisID: "y1",
        tension: 0.25,
        spanGaps: true,
      },
    ],
  },
  options: baseChartOptions({
    scales: {
      x: { grid: { display: false }, ticks: { color: mutedColor(), maxTicksLimit: 8 } },
      y: {
        beginAtZero: true,
        position: "left",
        grid: { color: "var(--border)" },
        ticks: { color: mutedColor(), callback: (v: string | number) => formatNumber(Number(v), 1) },
      },
      y1: {
        beginAtZero: true,
        position: "right",
        grid: { display: false },
        ticks: { color: mutedColor(), callback: (v: string | number) => `${formatNumber(Number(v), 2)}s` },
      },
    },
    plugins: { legend: { display: false } },
  }),
}));
</script>

<template>
  <div class="grid grid-2" style="gap: 18px">
    <div>
      <dl class="kv">
        <dt>{{ $t("models.errorRate") }}</dt>
        <dd class="mono">{{ formatPercent(row.errors, row.requests) }}</dd>
        <dt>{{ $t("metrics.cacheRate") }}</dt>
        <dd class="mono">{{ formatPercent(row.cacheRead, row.input + row.cacheRead) }}</dd>
        <dt>{{ $t("metrics.costUnavailable") }}</dt>
        <dd class="mono">{{ formatInt(row.unpriced) }}</dd>
      </dl>

      <dl class="kv" style="margin-top: 14px">
        <dt>{{ $t("models.avgDuration") }}</dt>
        <dd class="mono">{{ formatMs(avgDuration) }}</dd>
        <dt>{{ $t("models.avgTtft") }}</dt>
        <dd class="mono">{{ formatMs(avgTTFT) }}</dd>
        <dt>{{ $t("models.tokensPerSecond") }}</dt>
        <dd class="mono">{{ avgTPS === null ? "—" : formatNumber(avgTPS, 1) }}</dd>
      </dl>

      <dl class="kv" style="margin-top: 14px">
        <dt>{{ $t("models.uncachedInput") }}</dt>
        <dd class="mono">{{ formatTokens(row.input) }}</dd>
        <dt>{{ $t("metrics.cacheRead") }}</dt>
        <dd class="mono">{{ formatTokens(row.cacheRead) }}</dd>
        <dt>{{ $t("metrics.cacheWrite") }}</dt>
        <dd class="mono">{{ formatTokens(row.cacheWrite) }}</dd>
        <dt>{{ $t("metrics.output") }}</dt>
        <dd class="mono">{{ formatTokens(row.output) }}</dd>
      </dl>

      <div class="faint" style="margin-top: 14px">
        {{ $t("models.firstSeen") }} {{ formatDateTime(row.firstTs, settings.timezone) }}
        · {{ $t("models.lastSeen") }} {{ formatDateTime(row.lastTs, settings.timezone) }}
      </div>
    </div>

    <div>
      <div v-if="points.length === 0" class="empty">
        {{ $t("models.noPerformance") }}
        <div class="faint" style="margin-top: 4px">{{ $t("models.noPerformanceHint") }}</div>
      </div>
      <ChartBox v-else :config="config" :height="240" />
    </div>
  </div>
</template>