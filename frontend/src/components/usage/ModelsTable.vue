<script setup lang="ts">
import { computed, ref } from "vue";
import Sparkline from "@/components/charts/Sparkline.vue";
import ModelDetailPanel from "@/components/usage/ModelDetailPanel.vue";
import type { Granularity, ModelSeriesPoint, ModelStats } from "@/api/dashboard";
import { seriesColor } from "@/lib/chart";
import { formatInt, formatMs, formatNumber, formatPercent, formatTokens, formatUSD } from "@/lib/format";

/**
 * Every model in the range, one row per harness + provider + model.
 *
 * The whole aggregate is already in memory, so sorting happens here rather than in the
 * backend — the only reason SessionsTable emits its sort upward is that its rows are one
 * page out of many. Row expansion is a sibling <tr> so it inherits the table's columns
 * without a separate panel, and only one row is open at a time.
 */
const props = defineProps<{
  rows: ModelStats[];
  series: ModelSeriesPoint[];
  buckets: string[];
  granularity: Granularity;
}>();

const sortKey = ref("requests");
const sortDir = ref<"asc" | "desc">("desc");
const expanded = ref<string | null>(null);

const key = (row: ModelStats) => `${row.harness}|${row.provider}|${row.model}`;

const COLUMNS = [
  { key: "model", label: "events.model", right: false },
  { key: "requests", label: "models.requests", right: true },
  { key: "costUsd", label: "metrics.cost", right: true },
  { key: "total", label: "metrics.tokens", right: true },
  { key: "cacheRate", label: "metrics.cacheRate", right: true },
  { key: "errors", label: "analysis.outcomes", right: true },
  { key: "tps", label: "models.tokensPerSecond", right: true },
  { key: "ttft", label: "metrics.ttft", right: true },
  { key: "trend", label: "models.trend", right: false },
];

function setSort(column: string) {
  if (sortKey.value === column) {
    sortDir.value = sortDir.value === "desc" ? "asc" : "desc";
    return;
  }
  sortKey.value = column;
  sortDir.value = "desc";
}

/** Cache writes are excluded, matching the header note: this is reads over uncached input. */
const cacheRate = (row: ModelStats) =>
  formatPercent(row.cacheRead, row.input + row.cacheRead);

/** Unweighted mean of per-request output tokens per second; "—" when nothing was timed. */
const tokensPerSecond = (row: ModelStats) =>
  row.tpsCount > 0 ? formatNumber(row.tpsSum / row.tpsCount, 1) : "—";

/** Mean over the events that reported a TTFT, so a silent harness is a dash, not a zero. */
const ttft = (row: ModelStats) =>
  row.ttftCount > 0 ? formatMs(row.ttftSumMs / row.ttftCount) : "—";

const sorted = computed(() => {
  const value = (row: ModelStats): number => {
    switch (sortKey.value) {
      case "costUsd":
        return row.costUsd;
      case "total":
        return row.total;
      case "cacheRate":
        return row.input + row.cacheRead > 0 ? row.cacheRead / (row.input + row.cacheRead) : -1;
      case "errors":
        return row.errors;
      case "tps":
        return row.tpsCount > 0 ? row.tpsSum / row.tpsCount : -1;
      case "ttft":
        return row.ttftCount > 0 ? row.ttftSumMs / row.ttftCount : -1;
      default:
        return row.requests;
    }
  };
  const sign = sortDir.value === "asc" ? 1 : -1;
  return [...props.rows].sort((a, b) => {
    const diff = (value(a) - value(b)) * sign;
    // Ties break on the name so a refresh cannot reshuffle equal rows under the cursor.
    return diff !== 0 ? diff : a.model.localeCompare(b.model);
  });
});

/** Request counts per bucket for one model, dense so the sparkline has no gaps. */
const trendValues = computed(() => {
  const byKey = new Map<string, Map<string, number>>();
  for (const cell of props.series) {
    const id = `${cell.harness}|${cell.provider}|${cell.model}`;
    let row = byKey.get(id);
    if (!row) {
      row = new Map();
      byKey.set(id, row);
    }
    row.set(cell.bucket, cell.requests);
  }
  return (row: ModelStats) => {
    const cells = byKey.get(key(row));
    if (!cells) return null;
    return props.buckets.map((bucket) => cells.get(bucket) ?? 0);
  };
});

/** That model's series points, ascending, for the expanded chart. */
const pointsFor = computed(() => {
  const byKey = new Map<string, ModelSeriesPoint[]>();
  for (const cell of props.series) {
    const id = `${cell.harness}|${cell.provider}|${cell.model}`;
    const list = byKey.get(id);
    if (list) list.push(cell);
    else byKey.set(id, [cell]);
  }
  for (const list of byKey.values()) list.sort((a, b) => a.bucket.localeCompare(b.bucket));
  return (row: ModelStats) => byKey.get(key(row)) ?? [];
});

const maxRequests = computed(() =>
  props.rows.reduce((max, row) => Math.max(max, row.requests), 0),
);

/** A row with no recorded errors must not look like a verified-healthy green badge. */
function errorTone(row: ModelStats): string {
  if (row.errors === 0) return "faint";
  const rate = row.errors / (row.requests || 1);
  return rate >= 0.05 ? "tag-err" : rate >= 0.01 ? "tag-warn" : "tag-ok";
}
</script>

<template>
  <div>
    <div v-if="rows.length === 0" class="empty">{{ $t("common.noData") }}</div>
    <div v-else class="table-wrap">
      <table class="data">
        <thead>
          <tr>
            <th
              v-for="column in COLUMNS"
              :key="column.key"
              class="sortable"
              :class="{ right: column.right }"
              :title="
                column.key === 'cacheRate'
                  ? $t('models.cacheRateHint')
                  : column.key === 'total'
                    ? $t('models.tokensHint')
                    : column.key === 'tps'
                      ? $t('models.tpsHint')
                      : column.key === 'ttft'
                        ? $t('models.ttftHint')
                        : column.key === 'trend'
                          ? $t('models.trendHint')
                          : undefined
              "
              @click="setSort(column.key)"
            >
              {{ $t(column.label) }}
              <span v-if="sortKey === column.key" class="faint">
                {{ sortDir === "desc" ? "↓" : "↑" }}
              </span>
            </th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(row, index) in sorted" :key="key(row)">
            <tr style="cursor: pointer" @click="expanded = expanded === key(row) ? null : key(row)">
              <td>
                <div class="row" style="gap: 6px">
                  <span class="share-dot" :style="{ background: seriesColor(index) }" />
                  <div>
                    <div class="mono nowrap" :title="row.model">{{ row.model || $t("common.unknown") }}</div>
                    <div class="faint nowrap">
                      {{ row.provider ? `${row.harness} · ${row.provider}` : row.harness }}
                    </div>
                  </div>
                </div>
              </td>
              <td class="right num">
                <div class="row" style="gap: 6px; justify-content: flex-end">
                  <span>{{ formatInt(row.requests) }}</span>
                  <span class="bar" style="width: 48px">
                    <span
                      :style="{
                        width: (maxRequests ? (row.requests / maxRequests) * 100 : 0) + '%',
                        background: seriesColor(index),
                      }"
                    />
                  </span>
                </div>
              </td>
              <td class="right num">{{ formatUSD(row.costUsd) }}</td>
              <td class="right num">{{ formatTokens(row.total) }}</td>
              <td class="right num">{{ cacheRate(row) }}</td>
              <td class="right num" :title="row.errors === 0 ? $t('models.errorsCoverageHint') : undefined">
                <span :class="errorTone(row)">{{ formatPercent(row.errors, row.requests) }}</span>
              </td>
              <td class="right num">{{ tokensPerSecond(row) }}</td>
              <td class="right num">{{ ttft(row) }}</td>
              <td>
                <Sparkline
                  v-if="trendValues(row)"
                  :values="trendValues(row)!"
                  :color="seriesColor(index)"
                  :width="96"
                  :height="22"
                />
                <span v-else class="faint">{{ $t("models.trendEmpty") }}</span>
              </td>
            </tr>
            <tr v-if="expanded === key(row)">
              <td colspan="9" style="background: var(--bg-sunken)">
                <ModelDetailPanel
                  :row="row"
                  :points="pointsFor(row)"
                  :granularity="granularity"
                  :color="seriesColor(index)"
                />
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
  </div>
</template>