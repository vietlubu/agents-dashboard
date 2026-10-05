<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import ChartBox from "@/components/charts/ChartBox.vue";
import type { Granularity, ModelSeriesPoint, ModelStats } from "@/api/dashboard";
import { baseChartOptions, mutedColor, seriesColor } from "@/lib/chart";
import { formatBucket, formatInt, formatPercent } from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/**
 * Each model's slice of the requests, stacked per bucket.
 *
 * Only the six busiest models get their own colour; the rest fold into one "Other" series
 * so the legend stays readable and the palette does not wrap. The share denominator is the
 * bucket total across *every* model, not just the plotted six — otherwise a bucket served
 * entirely by the folded tail would sum to nothing and read as no activity.
 */
const props = defineProps<{
  rows: ModelStats[];
  /** Dense, contiguous bucket keys; empty buckets are included so the axis has no gaps. */
  buckets: string[];
  series: ModelSeriesPoint[];
  granularity: Granularity;
}>();

const SHARE_LIMIT = 6;
/** The folded-tail key. Never a real row: no harness, provider or model is this string. */
const OTHER = "__other__";

const { t } = useI18n();
const settings = useSettingsStore();
// Absolute counts are the default view: they answer "how much did each model do", where
// the share mode answers "what mix was it", and the mix needs the count to be legible first.
const mode = ref<"share" | "requests">("requests");

const key = (harness: string, provider: string, model: string) =>
  `${harness}|${provider}|${model}`;

/** Requests per bucket across every model, including the ones folded into "Other". */
const bucketTotals = computed(() => {
  const totals = new Map<string, number>();
  for (const bucket of props.buckets) totals.set(bucket, 0);
  for (const cell of props.series) {
    totals.set(cell.bucket, (totals.get(cell.bucket) ?? 0) + cell.requests);
  }
  return totals;
});

const totalRequests = computed(() =>
  props.rows.reduce((sum, row) => sum + row.requests, 0),
);

/** One entry per plotted series: its identity, colour and requests per bucket. */
const entries = computed(() => {
  const requests = new Map<string, number>();
  const byKey = new Map<string, Map<string, number>>();
  for (const cell of props.series) {
    const id = key(cell.harness, cell.provider, cell.model);
    requests.set(id, (requests.get(id) ?? 0) + cell.requests);
    let row = byKey.get(id);
    if (!row) {
      row = new Map();
      byKey.set(id, row);
    }
    row.set(cell.bucket, cell.requests);
  }
  const byRow = new Map(props.rows.map((row) => [key(row.harness, row.provider, row.model), row]));

  // Rank once and reuse: the chart order and the legend colours must agree, and both come
  // from this one list rather than from two independent rankings that can drift apart.
  const ranked = [...requests.entries()]
    .filter(([, count]) => count > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));

  // The same model reached through two providers is two series, so a bare model name would
  // show twice in the legend with nothing to tell them apart. Qualify only the names that
  // actually repeat, so the common case stays short.
  const nameCounts = new Map<string, number>();
  for (const [id] of ranked) {
    const name = byRow.get(id)?.model || id;
    nameCounts.set(name, (nameCounts.get(name) ?? 0) + 1);
  }

  const head = ranked.slice(0, SHARE_LIMIT);
  const tail = ranked.slice(SHARE_LIMIT);
  const out = head.map(([id], index) => {
    const row = byRow.get(id);
    const name = row?.model || id;
    // Only a provider separates two rows that share a model name; with an empty provider
    // the name is unique across the plotted set, so there is nothing to qualify.
    const qualifier = row?.provider ?? "";
    return {
      id,
      label: (nameCounts.get(name) ?? 0) > 1 && qualifier ? `${name} · ${qualifier}` : name,
      color: seriesColor(index),
      requests: requests.get(id) ?? 0,
      values: props.buckets.map((bucket) => byKey.get(id)?.get(bucket) ?? 0),
    };
  });
  if (tail.length > 0) {
    const tailIds = new Set(tail.map(([id]) => id));
    out.push({
      id: OTHER,
      label: t("models.other", { n: tail.length }),
      color: mutedColor(),
      requests: tail.reduce((sum, [, count]) => sum + count, 0),
      values: props.buckets.map((bucket) => {
        let sum = 0;
        for (const id of tailIds) sum += byKey.get(id)?.get(bucket) ?? 0;
        return sum;
      }),
    });
  }
  return out;
});

const isShare = computed(() => mode.value === "share");

/** A null value leaves a gap rather than a zero-height slice, so tooltips list only the models active in that bucket. */
function datasetValues(values: number[]): (number | null)[] {
  return values.map((value, i) => {
    if (value <= 0) return null;
    if (!isShare.value) return value;
    const total = bucketTotals.value.get(props.buckets[i]) ?? 0;
    return total > 0 ? value / total : null;
  });
}

const config = computed(() => ({
  type: "bar" as const,
  data: {
    labels: props.buckets.map((bucket) => formatBucket(bucket, props.granularity, settings.timezone)),
    datasets: entries.value.map((entry) => ({
      label: entry.label,
      data: datasetValues(entry.values),
      backgroundColor: entry.color,
      borderRadius: 2,
    })),
  },
  options: baseChartOptions({
    scales: {
      x: {
        stacked: true,
        grid: { display: false },
        ticks: { color: mutedColor(), maxTicksLimit: 12 },
      },
      y: {
        stacked: true,
        beginAtZero: true,
        max: isShare.value ? 1 : undefined,
        grid: { color: "var(--border)" },
        ticks: {
          color: mutedColor(),
          callback: (value: string | number) =>
            isShare.value ? formatPercent(Number(value), 1, 0) : formatInt(Number(value)),
        },
      },
    },
    plugins: {
      legend: { display: false },
      tooltip: {
        callbacks: {
          label: (ctx: { dataset: { label?: string }; raw: unknown }) => {
            const raw = Number(ctx.raw);
            if (!Number.isFinite(raw)) return "";
            const value = isShare.value ? formatPercent(raw, 1) : formatInt(raw);
            return ctx.dataset.label ? `${ctx.dataset.label}: ${value}` : value;
          },
        },
      },
    },
  }),
}));

// The card owns the mode, so it also owns the caption: a parent-supplied subtitle would
// still claim "share" while the chart is showing counts.
const caption = computed(() => {
  const hour = props.granularity === "hour";
  if (isShare.value) return t(hour ? "models.sharePerHour" : "models.sharePerDay");
  return t(hour ? "models.requestsPerHour" : "models.requestsPerDay");
});
</script>

<template>
  <div>
    <div class="row-wrap" style="margin-bottom: 8px">
      <span class="faint">{{ caption }}</span>
      <div class="spacer" />
      <div class="seg">
        <button :class="{ active: mode === 'share' }" @click="mode = 'share'">
          {{ $t("models.shareMode") }}
        </button>
        <button :class="{ active: mode === 'requests' }" @click="mode = 'requests'">
          {{ $t("models.requestsMode") }}
        </button>
      </div>
    </div>

    <div v-if="entries.length === 0" class="empty">{{ $t("common.noData") }}</div>
    <ChartBox v-else :config="config" :height="240" />

    <!-- Shares are of every model's requests, so the listed percentages add up across all entries. -->
    <div v-if="entries.length" class="legend" style="margin-top: 10px">
      <span v-for="entry in entries" :key="entry.id" class="legend-item">
        <span class="share-dot" :style="{ background: entry.color }" />
        <span class="mono">{{ entry.label }}</span>
        <span class="faint num">{{ formatPercent(entry.requests, totalRequests) }}</span>
      </span>
    </div>
  </div>
</template>