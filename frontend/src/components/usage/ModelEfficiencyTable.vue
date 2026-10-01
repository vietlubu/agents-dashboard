<script setup lang="ts">
import { computed } from "vue";
import Card from "@/components/ui/Card.vue";
import type { BreakdownRow } from "@/api/dashboard";
import { formatMs, formatTokens, formatUSD } from "@/lib/format";

/**
 * Cost and latency per model.
 *
 * The efficiency column is cost per thousand tokens and is the metric that actually compares
 * models: a model with a large token count can still be the cheaper way to get work done. A
 * model with no price shows a dash, never a zero.
 */
const props = defineProps<{ rows: BreakdownRow[] }>();

const sorted = computed(() => [...props.rows].sort((a, b) => b.total - a.total));

function perThousand(row: BreakdownRow): string {
  if (row.total === 0) return "—";
  return formatUSD((row.costUsd / row.total) * 1000);
}
</script>

<template>
  <Card :title="$t('analysis.efficiency')">
    <div v-if="sorted.length === 0" class="empty">{{ $t('common.noData') }}</div>
    <table v-else class="data">
      <thead>
        <tr>
          <th>{{ $t('events.model') }}</th>
          <th class="right">{{ $t('metrics.events') }}</th>
          <th class="right">{{ $t('metrics.tokens') }}</th>
          <th class="right">{{ $t('metrics.cost') }}</th>
          <th class="right">{{ $t('metrics.costPerKToken') }}</th>
          <th class="right">{{ $t('metrics.latency') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in sorted" :key="row.key">
          <td class="mono" :title="row.key">{{ row.key }}</td>
          <td class="right num">{{ row.events }}</td>
          <td class="right num">{{ formatTokens(row.total) }}</td>
          <td class="right num">{{ formatUSD(row.costUsd) }}</td>
          <td class="right num">{{ perThousand(row) }}</td>
          <td class="right num">
            {{ row.latencyCount > 0 ? formatMs(row.latencyAvgMs) : '—' }}
          </td>
        </tr>
      </tbody>
    </table>
  </Card>
</template>