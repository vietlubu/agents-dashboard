<script setup lang="ts">
import { computed } from "vue";
import Card from "@/components/ui/Card.vue";
import type { Comparison } from "@/api/dashboard";
import { formatTokens, formatUSD } from "@/lib/format";

/**
 * Current vs previous period. Costs and tokens are shown side by side because the two move
 * independently: a cheaper model can raise the token count while lowering the bill.
 */
const props = defineProps<{ comparison: Comparison }>();

function change(current: number, previous: number): { text: string; cls: string } {
  if (previous === 0 && current === 0) return { text: "—", cls: "faint" };
  if (previous === 0) return { text: "new", cls: "delta-up" };
  const pct = ((current - previous) / previous) * 100;
  return {
    text: (pct >= 0 ? "+" : "") + pct.toFixed(1) + "%",
    cls: Math.abs(pct) < 0.05 ? "faint" : pct >= 0 ? "delta-up" : "delta-down",
  };
}

const rows = computed(() => [
  {
    label: "metrics.tokens",
    current: formatTokens(props.comparison.current.total),
    previous: formatTokens(props.comparison.previous.total),
    delta: change(props.comparison.current.total, props.comparison.previous.total),
  },
  {
    label: "metrics.cost",
    current: formatUSD(props.comparison.current.costUsd),
    previous: formatUSD(props.comparison.previous.costUsd),
    delta: change(props.comparison.current.costUsd, props.comparison.previous.costUsd),
  },
  {
    label: "metrics.events",
    current: String(props.comparison.current.events),
    previous: String(props.comparison.previous.events),
    delta: change(props.comparison.current.events, props.comparison.previous.events),
  },
]);
</script>

<template>
  <Card :title="$t('overview.vsPrevious')">
    <table class="data">
      <thead>
        <tr>
          <th />
          <th class="right">{{ $t('common.total') }}</th>
          <th class="right">{{ $t('overview.vsPrevious') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in rows" :key="row.label">
          <td class="muted">{{ $t(row.label) }}</td>
          <td class="right num">{{ row.current }}</td>
          <td class="right num" :class="row.delta.cls">
            {{ row.delta.text }}
            <span class="faint">({{ row.previous }})</span>
          </td>
        </tr>
      </tbody>
    </table>
  </Card>
</template>