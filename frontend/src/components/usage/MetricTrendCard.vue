<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import ChartBox from "@/components/charts/ChartBox.vue";
import Card from "@/components/ui/Card.vue";
import type { Granularity, GranularityChoice, SeriesPoint } from "@/api/dashboard";
import { baseChartOptions, resolveColor, seriesColor } from "@/lib/chart";
import { formatBucket, formatInt, formatPercent, formatTokens, formatUSD } from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/**
 * The main trend chart. The metric switch changes which measure is drawn rather than showing
 * three charts, so the card stays glanceable; the bucket switch changes the time grain,
 * because a range of a few hours drawn in days shows nothing at all.
 */
type Metric = "total" | "costUsd" | "events";

const props = defineProps<{
  points: SeriesPoint[];
  title: string;
  /** The bucket size the store answered with, which can be coarser than requested. */
  granularity: Granularity;
  /** The bucket size the user asked for, "auto" included. */
  granularityChoice: GranularityChoice;
  /** Whole days in the range, so the axis keeps its shape when data is missing. */
  days?: string[];
  /** Whole hours in the range, used instead of `days` for an hourly chart. */
  hours?: string[];
}>();

const emit = defineEmits<{ "update:granularityChoice": [GranularityChoice] }>();

const { t } = useI18n();
const settings = useSettingsStore();
const metric = ref<Metric>("total");

const METRICS: { key: Metric; label: string }[] = [
  { key: "total", label: "metrics.tokens" },
  { key: "costUsd", label: "metrics.cost" },
  { key: "events", label: "metrics.events" },
];

const CHOICES: GranularityChoice[] = ["auto", "hour", "day", "week", "month"];

// The switch shows the choice; this shows what it resolved to, which differs under "auto"
// and when the store answers more coarsely than asked.
const showsBucket = computed(
  () =>
    props.granularityChoice === "auto" || (props.granularityChoice as string) !== props.granularity
);

/** The axis: the buckets the range covers, so a gap is a gap rather than a straight line. */
const labels = computed(() => {
  if (props.granularity === "hour" && props.hours?.length) return props.hours;
  if (props.granularity === "day" && props.days?.length) return props.days;
  return props.points.map((p) => p.key);
});

function valueFor(key: string): number {
  const point = props.points.find((p) => p.key === key);
  if (!point) return 0;
  return metric.value === "total" ? point.total : metric.value === "costUsd" ? point.costUsd : point.events;
}

const config = computed(() => {
  const data = labels.value.map((key) => valueFor(key));
  const color = resolveColor(seriesColor(0));
  // A one-point line has neither a segment nor an area to fill, so a single day would draw
  // nothing at all: short series show their points instead.
  const radius = labels.value.length <= 2 ? 4 : 0;
  return {
    type: "line" as const,
    data: {
      labels: labels.value.map((key) => formatBucket(key, props.granularity, settings.timezone)),
      datasets: [
        {
          data,
          borderColor: color,
          backgroundColor: color + "22",
          fill: true,
          tension: 0.25,
          pointRadius: radius,
          pointBackgroundColor: color,
          pointHitRadius: 12,
          borderWidth: 2,
        },
      ],
    },
    options: baseChartOptions({
      plugins: {
        legend: { display: false },
        tooltip: {
          displayColors: false,
          callbacks: {
            label: (ctx: { dataIndex: number }) => {
              const point = props.points.find((p) => p.key === labels.value[ctx.dataIndex]);
              if (!point) return [];
              const selectedMetric = metric.value;
              const valueLabel =
                selectedMetric === "costUsd"
                  ? t("metrics.cost")
                  : selectedMetric === "events"
                    ? t("metrics.events")
                    : `${t("common.total")} ${t("metrics.tokens")}`;
              const value =
                selectedMetric === "costUsd"
                  ? formatUSD(point.costUsd)
                  : selectedMetric === "events"
                    ? formatInt(point.events)
                    : formatTokens(point.total);
              return [
                `${valueLabel}: ${value}`,
                `${t("metrics.input")}: ${formatTokens(point.input)}`,
                `${t("metrics.cacheRead")}: ${formatTokens(point.cacheRead)}`,
                `${t("metrics.cacheRate")}: ${formatPercent(point.cacheRead, point.input + point.cacheRead)}`,
                `${t("metrics.output")}: ${formatTokens(point.output)}`,
              ];
            },
          },
        },
      },
    }),
  };
});
</script>

<template>
  <Card :title="props.title">
    <template #actions>
      <div class="seg">
        <button
          v-for="option in METRICS"
          :key="option.key"
          :class="{ active: metric === option.key }"
          @click="metric = option.key"
        >
          {{ $t(option.label) }}
        </button>
      </div>
      <div class="seg">
        <button
          v-for="choice in CHOICES"
          :key="choice"
          :class="{ active: props.granularityChoice === choice }"
          @click="emit('update:granularityChoice', choice)"
        >
          {{ $t(`granularity.${choice}`) }}
        </button>
      </div>
      <span v-if="showsBucket" class="faint" style="font-size: 11px">
        {{ $t('granularity.asDrawn', { bucket: $t(`granularity.${props.granularity}`) }) }}
      </span>
    </template>
    <ChartBox :config="config" :height="240" />
  </Card>
</template>