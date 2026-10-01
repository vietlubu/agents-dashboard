<script setup lang="ts">
import { computed } from "vue";
import Card from "@/components/ui/Card.vue";
import StatTile from "@/components/ui/StatTile.vue";
import MetricTrendCard from "@/components/usage/MetricTrendCard.vue";
import TokenCompositionCard from "@/components/usage/TokenCompositionCard.vue";
import ShareListCard from "@/components/usage/ShareListCard.vue";
import ComparisonCard from "@/components/usage/ComparisonCard.vue";
import HeatmapGrid from "@/components/charts/HeatmapGrid.vue";
import { useLiveQuery } from "@/composables/useLiveQuery";
import * as dashboard from "@/api/dashboard";
import { useFiltersStore } from "@/stores/filters";
import { useSettingsStore } from "@/stores/settings";
import { daysInRange, hourKeysInRange } from "@/lib/range";
import { formatPercent, formatTokens, formatUSD } from "@/lib/format";

const filters = useFiltersStore();
const settings = useSettingsStore();

const totals = useLiveQuery(() => dashboard.totals(filters.query), null as dashboard.Totals | null);
const comparison = useLiveQuery(
  () => dashboard.compare(filters.query),
  null as dashboard.Comparison | null
);
// The bucket size is view-local, so it has to be declared as a dependency or a switch
// would keep rendering the previous query's points.
const series = useLiveQuery(
  () => dashboard.series(filters.query, filters.resolvedGranularity),
  { granularity: "day", points: [] } as dashboard.SeriesResult,
  [computed(() => filters.resolvedGranularity)]
);
const byModel = useLiveQuery(() => dashboard.breakdown(filters.query, "model", 8), []);
const byHarness = useLiveQuery(() => dashboard.breakdown(filters.query, "harness", 8), []);
const heat = useLiveQuery(
  () => dashboard.heatmap(filters.query.fromMs, filters.query.toMs),
  [] as dashboard.HeatCell[]
);
const unpriced = useLiveQuery(() => dashboard.unpricedModels(), [] as string[]);

const days = computed(() => daysInRange(filters.resolvedRange, settings.timezone));
const hours = computed(() => hourKeysInRange(filters.resolvedRange));

const cacheShare = computed(() => {
  const t = totals.data.value;
  if (!t || t.total === 0) return "—";
  return formatPercent(t.cacheRead, t.total, 0);
});

const avgPerDay = computed(() => {
  const t = totals.data.value;
  const n = days.value.length || 1;
  if (!t) return "—";
  return formatTokens(t.total / n);
});

const costCoverage = computed(() => {
  const t = totals.data.value;
  if (!t || t.events === 0) return "";
  const covered = t.events - t.costUnavailable;
  return `${covered}/${t.events} priced`;
});
</script>

<template>
  <div class="grid" style="gap: 14px">
    <div class="grid grid-4">
      <Card>
        <StatTile
          :label="$t('metrics.tokens')"
          :value="formatTokens(totals.data.value?.total)"
          :current="comparison.data.value?.current.total"
          :previous="comparison.data.value?.previous.total"
        />
      </Card>
      <Card>
        <StatTile
          :label="$t('metrics.cost')"
          :value="formatUSD(totals.data.value?.costUsd)"
          :current="comparison.data.value?.current.costUsd"
          :previous="comparison.data.value?.previous.costUsd"
          :hint="costCoverage"
          invert-delta
        />
      </Card>
      <Card>
        <StatTile
          :label="$t('metrics.events')"
          :value="String(totals.data.value?.events ?? 0)"
          :current="comparison.data.value?.current.events"
          :previous="comparison.data.value?.previous.events"
        />
      </Card>
      <Card>
        <StatTile
          :label="$t('metrics.avgPerDay')"
          :value="avgPerDay"
          :hint="`${$t('metrics.cacheReadShare')} ${cacheShare}`"
        />
      </Card>
    </div>

    <MetricTrendCard
      :points="series.data.value.points"
      :granularity="series.data.value.granularity"
      :granularity-choice="filters.granularity"
      :days="days"
      :hours="hours"
      :title="$t('overview.trend')"
      @update:granularity-choice="filters.granularity = $event"
    />

    <div class="grid grid-3">
      <TokenCompositionCard v-if="totals.data.value" :totals="totals.data.value" />
      <Card :title="$t('metrics.byHarness')">
        <ShareListCard :rows="byHarness.data.value" :limit="6" />
      </Card>
      <Card :title="$t('metrics.byModel')">
        <ShareListCard :rows="byModel.data.value" :limit="6" :palette-offset="2" />
      </Card>
    </div>

    <div class="grid grid-2">
      <Card :title="$t('overview.activity')" :subtitle="$t('overview.recentDays')">
        <HeatmapGrid :cells="heat.data.value" :days="days" />
      </Card>
      <ComparisonCard v-if="comparison.data.value" :comparison="comparison.data.value" />
    </div>

    <div v-if="unpriced.data.value.length" class="note">
      {{ $t('overview.unpricedNotice', { count: unpriced.data.value.length }) }}
      <span class="mono">{{ unpriced.data.value.slice(0, 6).join(', ') }}</span>
    </div>
  </div>
</template>