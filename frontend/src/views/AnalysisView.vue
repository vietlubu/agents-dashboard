<script setup lang="ts">
import { computed, ref } from "vue";
import Card from "@/components/ui/Card.vue";
import ChartBox from "@/components/charts/ChartBox.vue";
import ShareListCard from "@/components/usage/ShareListCard.vue";
import ModelEfficiencyTable from "@/components/usage/ModelEfficiencyTable.vue";
import ProjectTable from "@/components/usage/ProjectTable.vue";
import OutcomeBreakdownCard from "@/components/usage/OutcomeBreakdownCard.vue";
import LatencyScatterCard from "@/components/usage/LatencyScatterCard.vue";
import { useLiveQuery } from "@/composables/useLiveQuery";
import * as dashboard from "@/api/dashboard";
import { baseChartOptions, seriesColor } from "@/lib/chart";
import { formatTokens } from "@/lib/format";
import { useFiltersStore } from "@/stores/filters";

/**
 * The analysis view switches one dimension at a time. `day` is not offered because the
 * Overview trend chart already shows a day breakdown.
 */
const DIMS = [
  { key: "model", label: "metrics.byModel" },
  { key: "harness", label: "metrics.byHarness" },
  { key: "project", label: "metrics.byProject" },
  { key: "agentType", label: "metrics.byAgentType" },
  { key: "outcome", label: "metrics.byOutcome" },
  { key: "costSource", label: "metrics.byCostSource" },
];

const filters = useFiltersStore();
const dim = ref("model");

const breakdown = useLiveQuery(() => dashboard.breakdown(filters.query, dim.value, 100), [], [() => dim.value]);
const byModel = useLiveQuery(() => dashboard.breakdown(filters.query, "model", 100), []);
const stacked = useLiveQuery(() => dashboard.stacked(filters.query, "harness", 5), []);
const projects = useLiveQuery(() => dashboard.breakdown(filters.query, "project", 100), []);
const outcomes = useLiveQuery(() => dashboard.breakdown(filters.query, "outcome", 20), []);
const latency = useLiveQuery(() => dashboard.latency(filters.query, 2000), []);
const unpriced = useLiveQuery(() => dashboard.unpricedModels(), [] as string[]);

const dimLabel = computed(() => DIMS.find((d) => d.key === dim.value)?.label ?? "metrics.byModel");

/** Days in the stacked series, in date order. */
const stackedDays = computed(() => {
  const days: string[] = [];
  for (const cell of stacked.data.value) {
    if (!days.includes(cell.day)) days.push(cell.day);
  }
  return days.sort();
});

/** Harness keys, ordered by their total so the legend is stable. */
const stackedKeys = computed(() => {
  const totals = new Map<string, number>();
  for (const cell of stacked.data.value) {
    totals.set(cell.key, (totals.get(cell.key) ?? 0) + cell.total);
  }
  return [...totals.entries()].sort((a, b) => b[1] - a[1]).map(([key]) => key);
});

const stackedConfig = computed(() => ({
  type: "bar" as const,
  data: {
    labels: stackedDays.value,
    datasets: stackedKeys.value.map((key, index) => ({
      label: key,
      data: stackedDays.value.map((day) => {
        const cell = stacked.data.value.find((c) => c.day === day && c.key === key);
        return cell?.total ?? 0;
      }),
      backgroundColor: seriesColor(index),
      borderRadius: 2,
    })),
  },
  options: baseChartOptions({
    plugins: {
      legend: { display: true, labels: { color: "#98a0b3", boxWidth: 10 } },
      tooltip: {
        callbacks: {
          label: (ctx: { dataset: { label: string }; raw: unknown }) =>
            `${ctx.dataset.label}: ${formatTokens(Number(ctx.raw) || 0)}`,
        },
      },
    },
    scales: {
      x: { stacked: true, grid: { color: "#262a35" }, ticks: { color: "#98a0b3" } },
      y: { stacked: true, grid: { color: "#262a35" }, ticks: { color: "#98a0b3" } },
    },
  }),
}));
</script>

<template>
  <div class="grid" style="gap: 14px">
    <div class="row-wrap">
      <span class="faint">{{ $t('analysis.dimension') }}</span>
      <div class="seg">
        <button
          v-for="option in DIMS"
          :key="option.key"
          :class="{ active: dim === option.key }"
          @click="dim = option.key"
        >
          {{ $t(option.label) }}
        </button>
      </div>
    </div>

    <div class="grid grid-2">
      <Card :title="$t('analysis.breakdown')" :subtitle="$t(dimLabel)">
        <ShareListCard :rows="breakdown.data.value" :limit="12" />
      </Card>
      <Card :title="$t('analysis.stacked', { dim: $t('dims.harness') })">
        <div v-if="stackedDays.length === 0" class="empty">{{ $t('common.noData') }}</div>
        <ChartBox v-else :config="stackedConfig" :height="240" />
      </Card>
    </div>

    <ModelEfficiencyTable :rows="byModel.data.value" />

    <div class="grid grid-2">
      <ProjectTable :rows="projects.data.value" />
      <OutcomeBreakdownCard :rows="outcomes.data.value" />
    </div>

    <LatencyScatterCard :points="latency.data.value" />

    <div v-if="unpriced.data.value.length" class="note">
      <div>{{ $t('analysis.unpriced') }}</div>
      <div class="mono" style="margin-top: 6px">{{ unpriced.data.value.join(', ') }}</div>
      <div class="faint" style="margin-top: 6px">{{ $t('analysis.unpricedHint') }}</div>
    </div>
  </div>
</template>