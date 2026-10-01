<script setup lang="ts">
import Card from "@/components/ui/Card.vue";
import type { BreakdownRow } from "@/api/dashboard";
import { formatTokens, formatUSD } from "@/lib/format";

/** Per-project usage. Project labels come from the session's working directory. */
const props = defineProps<{ rows: BreakdownRow[] }>();
</script>

<template>
  <Card :title="$t('metrics.byProject')">
    <div v-if="props.rows.length === 0" class="empty">{{ $t('common.noData') }}</div>
    <table v-else class="data">
      <thead>
        <tr>
          <th>{{ $t('events.project') }}</th>
          <th class="right">{{ $t('metrics.events') }}</th>
          <th class="right">{{ $t('metrics.tokens') }}</th>
          <th class="right">{{ $t('metrics.cost') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in props.rows" :key="row.key">
          <td class="mono" :title="row.key">{{ row.key || '—' }}</td>
          <td class="right num">{{ row.events }}</td>
          <td class="right num">{{ formatTokens(row.total) }}</td>
          <td class="right num">{{ formatUSD(row.costUsd) }}</td>
        </tr>
      </tbody>
    </table>
  </Card>
</template>