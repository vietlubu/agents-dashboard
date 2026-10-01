<script setup lang="ts">
import { computed, ref } from "vue";
import Card from "@/components/ui/Card.vue";
import Pagination from "@/components/ui/Pagination.vue";
import type { SessionRow } from "@/api/meta";
import { formatDateTime, formatMs, formatTokens, formatUSD } from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/**
 * The sessions table. The range filter selects sessions active inside it, while the token and
 * cost columns describe the whole session — a session that started before the window still
 * reports what it spent in total, which is the honest reading of a row labelled "session".
 */
const props = defineProps<{
  rows: SessionRow[];
  total: number;
  loading?: boolean;
}>();

const emit = defineEmits<{
  page: [offset: number];
  sort: [key: string];
  select: [row: SessionRow];
}>();

const settings = useSettingsStore();
const offset = ref(0);
const PAGE = 50;

const sortKey = ref("updated");

function setSort(key: string) {
  sortKey.value = key;
  emit("sort", key);
}

function onPage(next: number) {
  offset.value = next;
  emit("page", next);
}

const columns = computed(() => [
  { key: "harness", label: "events.harness" },
  { key: "project", label: "events.project" },
  { key: "sessionId", label: "sessions.session" },
  { key: "model", label: "events.model" },
  { key: "agentName", label: "events.agentName" },
  { key: "started", label: "sessions.started", right: false },
  { key: "events", label: "metrics.events", right: true },
  { key: "total", label: "metrics.tokens", right: true },
  { key: "cost", label: "metrics.cost", right: true },
  { key: "latency", label: "metrics.latency", right: true },
]);
</script>

<template>
  <Card>
    <div v-if="props.rows.length === 0" class="empty">
      {{ props.loading ? $t('common.loading') : $t('common.noData') }}
    </div>
    <template v-else>
      <div class="table-wrap" style="max-height: 60vh">
        <table class="data">
          <thead>
            <tr>
              <th
                v-for="column in columns"
                :key="column.key"
                class="sortable"
                :class="{ right: column.right }"
                @click="setSort(column.key)"
              >
                {{ $t(column.label) }}
                <span v-if="sortKey === column.key" class="faint">↓</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in props.rows"
              :key="row.harness + row.sessionId"
              style="cursor: pointer"
              @click="emit('select', row)"
            >
              <td>{{ row.harness }}</td>
              <td class="mono" :title="row.project">{{ row.project || '—' }}</td>
              <td class="mono" :title="row.sessionId">{{ row.sessionId.slice(0, 12) }}</td>
              <td class="mono">{{ row.model || '—' }}</td>
              <td>{{ row.agentName || '—' }}</td>
              <td class="num">{{ formatDateTime(row.startedAt, settings.timezone) }}</td>
              <td class="right num">{{ row.events }}</td>
              <td class="right num">{{ formatTokens(row.total) }}</td>
              <td class="right num">{{ formatUSD(row.costUsd) }}</td>
              <td class="right num">
                {{ row.latencyCount > 0 ? formatMs(row.latencyAvgMs) : '—' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div style="margin-top: 10px">
        <Pagination :offset="offset" :limit="PAGE" :total="props.total" @update:offset="onPage" />
      </div>
    </template>
  </Card>
</template>