<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import Card from "@/components/ui/Card.vue";
import StatTile from "@/components/ui/StatTile.vue";
import Sparkline from "@/components/charts/Sparkline.vue";
import ModelShareCard from "@/components/usage/ModelShareCard.vue";
import ModelsTable from "@/components/usage/ModelsTable.vue";
import { useLiveQuery } from "@/composables/useLiveQuery";
import * as dashboard from "@/api/dashboard";
import type { ModelSeriesPoint, ModelStats } from "@/api/dashboard";
import { seriesColor } from "@/lib/chart";
import { daysInRange, hourKeysInRange } from "@/lib/range";
import { formatInt, formatPercent, formatUSD } from "@/lib/format";
import { useFiltersStore } from "@/stores/filters";
import { useSettingsStore } from "@/stores/settings";

/**
 * Which models did the work in the selected range, and how fast they answered.
 *
 * The range comes from the shared header control, not from here, so switching views keeps
 * the window and the filters. Rows are keyed by harness + provider + model: the same model
 * reached through two providers is two rows, which is the distinction this screen exists to
 * show and which the daily rollups cannot answer.
 */
const { t } = useI18n();
const filters = useFiltersStore();
const settings = useSettingsStore();

// The store already resolves the bucket size (hour under 48h, otherwise day), so the view
// does not get a second granularity of its own. Pinia unwraps the store's refs, so this is
// read through `filters.resolvedGranularity` at each use rather than destructured once:
// a captured copy would keep the range's first bucket size for the life of the screen.
const stats = useLiveQuery(() => dashboard.modelStats(filters.query), [] as ModelStats[]);
const series = useLiveQuery(
  () => dashboard.modelSeries(filters.query, filters.resolvedGranularity),
  [] as ModelSeriesPoint[],
  [() => filters.resolvedGranularity],
);

const rows = computed(() => stats.data.value);
const points = computed(() => series.data.value);

/** Dense axis so an idle hour renders as a gap in time, not as a missing bucket. */
const buckets = computed(() =>
  filters.resolvedGranularity === "hour"
    ? hourKeysInRange(filters.resolvedRange)
    : daysInRange(filters.resolvedRange, settings.timezone),
);

const totalRequests = computed(() => rows.value.reduce((sum, row) => sum + row.requests, 0));
const totalErrors = computed(() => rows.value.reduce((sum, row) => sum + row.errors, 0));
const totalCost = computed(() => rows.value.reduce((sum, row) => sum + row.costUsd, 0));
const totalUnpriced = computed(() => rows.value.reduce((sum, row) => sum + row.unpriced, 0));

const modelCount = computed(() => new Set(rows.value.map((row) => row.model)).size);
const providerCount = computed(() => new Set(rows.value.map((row) => row.provider)).size);

/** Rows arrive requests-desc, so a strict > keeps the first of any tie. */
const mostUsed = computed(() => {
  let top: ModelStats | null = null;
  for (const row of rows.value) {
    if (!top || row.requests > top.requests) top = row;
  }
  return top;
});

/** Requests per bucket across every model, for the tile's sparkline. */
const requestTrend = computed(() => {
  const totals = new Map<string, number>();
  for (const bucket of buckets.value) totals.set(bucket, 0);
  for (const cell of points.value) {
    totals.set(cell.bucket, (totals.get(cell.bucket) ?? 0) + cell.requests);
  }
  return buckets.value.map((bucket) => totals.get(bucket) ?? 0);
});

const costHint = computed(() =>
  totalUnpriced.value > 0
    ? `${t("models.costHint")} · ${t("models.unpricedHint", { n: formatInt(totalUnpriced.value) })}`
    : t("models.costHint"),
);

</script>

<template>
  <div class="grid" style="gap: 14px">
    <div class="grid grid-4">
      <Card>
        <StatTile
          :label="$t('models.modelsUsed')"
          :value="rows.length === 0 ? '—' : formatInt(modelCount)"
          :hint="$t('models.providers', { n: providerCount })"
        />
      </Card>
      <Card>
        <StatTile
          :label="$t('models.mostUsed')"
          :value="mostUsed ? mostUsed.model || $t('common.unknown') : '—'"
          :hint="
            mostUsed
              ? `${formatPercent(mostUsed.requests, totalRequests)} · ${mostUsed.provider || mostUsed.harness}`
              : ''
          "
        />
      </Card>
      <Card>
        <StatTile
          :label="$t('models.requests')"
          :value="formatInt(totalRequests)"
          :hint="$t('models.failed', { n: formatInt(totalErrors) })"
        >
          <template #extra>
            <Sparkline
              v-if="requestTrend.length > 1"
              :values="requestTrend"
              :color="seriesColor(0)"
              :width="140"
              :height="26"
              style="margin-top: 6px"
            />
          </template>
        </StatTile>
      </Card>
      <Card>
        <StatTile
          :label="$t('metrics.cost')"
          :value="formatUSD(totalCost, { compact: true })"
          :hint="costHint"
        />
      </Card>
    </div>

    <Card :title="$t('models.share')">
      <ModelShareCard
        :rows="rows"
        :buckets="buckets"
        :series="points"
        :granularity="filters.resolvedGranularity"
      />
    </Card>

    <Card :title="$t('models.allModels')" :subtitle="$t('models.allModelsHint')">
      <ModelsTable
        :rows="rows"
        :series="points"
        :buckets="buckets"
        :granularity="filters.resolvedGranularity"
      />
    </Card>
  </div>
</template>