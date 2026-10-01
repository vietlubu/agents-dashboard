<script setup lang="ts">
import { computed } from "vue";
import Card from "@/components/ui/Card.vue";
import type { BreakdownRow } from "@/api/dashboard";
import { formatTokens } from "@/lib/format";

/** Outcome split: completed turns, tool-driven turns, and aborted or failed requests. */
const props = defineProps<{ rows: BreakdownRow[] }>();

const TAGS: Record<string, string> = {
  ok: "tag-ok",
  tool_use: "",
  aborted: "tag-warn",
  error: "tag-err",
  unknown: "",
};

const total = computed(() => props.rows.reduce((sum, row) => sum + row.events, 0));
</script>

<template>
  <Card :title="$t('analysis.outcomes')">
    <div v-if="props.rows.length === 0" class="empty">{{ $t('common.noData') }}</div>
    <table v-else class="data">
      <thead>
        <tr>
          <th>{{ $t('events.outcome') }}</th>
          <th class="right">{{ $t('metrics.events') }}</th>
          <th class="right">{{ $t('metrics.tokens') }}</th>
          <th class="right">%</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in props.rows" :key="row.key">
          <td><span class="tag" :class="TAGS[row.key] ?? ''">{{ row.key }}</span></td>
          <td class="right num">{{ row.events }}</td>
          <td class="right num">{{ formatTokens(row.total) }}</td>
          <td class="right num faint">
            {{ total ? ((row.events / total) * 100).toFixed(1) : '—' }}%
          </td>
        </tr>
      </tbody>
    </table>
  </Card>
</template>