<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import EventsTable from "@/components/usage/EventsTable.vue";
import EventsColumnSettingsModal from "@/components/usage/EventsColumnSettingsModal.vue";
import Pagination from "@/components/ui/Pagination.vue";
import { useLiveQuery } from "@/composables/useLiveQuery";
import * as events from "@/api/events";
import { useFiltersStore } from "@/stores/filters";

/**
 * The event log: filters come from the shared header, columns are user-selectable, and the
 * export is rendered by the backend and downloaded by the browser (which is the only path
 * that also works in headless server mode).
 */
const filters = useFiltersStore();
const { t } = useI18n();

const PAGE = 200;
const offset = ref(0);

const COLUMNS = [
  { key: "harness", label: "events.harness" },
  { key: "time", label: "events.time" },
  { key: "project", label: "events.project" },
  { key: "sessionId", label: "events.session" },
  { key: "model", label: "events.model" },
  { key: "provider", label: "events.provider" },
  { key: "agentType", label: "events.agentType" },
  { key: "agentName", label: "events.agentName" },
  { key: "outcome", label: "events.outcome" },
  { key: "input", label: "metrics.input" },
  { key: "output", label: "metrics.output" },
  { key: "cacheRead", label: "metrics.cacheRead" },
  { key: "cacheWrite", label: "metrics.cacheWrite" },
  { key: "total", label: "metrics.tokens" },
  { key: "costUsd", label: "metrics.cost" },
  { key: "costSource", label: "events.costSource" },
  { key: "latencyMs", label: "metrics.latency" },
  { key: "ttftMs", label: "metrics.ttft" },
];

const DEFAULT_COLUMNS = [
  "harness",
  "time",
  "project",
  "model",
  "input",
  "output",
  "cacheRead",
  "cacheWrite",
  "total",
  "costUsd",
  "costSource",
  "latencyMs",
  "outcome",
];

const visibleColumns = ref<string[]>(DEFAULT_COLUMNS);
const showColumns = ref(false);
const message = ref("");

const page = useLiveQuery(
  () => events.list(filters.query, offset.value, PAGE),
  { rows: [], total: 0 } as events.EventPage,
  [() => offset.value]
);

// Any filter change resets to the first page: page 4 of a different filter set is meaningless.
watch(
  () => filters.query,
  () => {
    offset.value = 0;
  },
  { deep: true }
);

const busy = computed(() => page.loading.value);

async function exportAs(format: "csv" | "json") {
  message.value = "";
  const result = await events.exportEvents(filters.query, format);
  events.download(result);
  message.value = result.truncated
    ? t("events.truncated", { max: result.rows })
    : t("events.exported", { rows: result.rows });
}
</script>

<template>
  <div class="grid" style="gap: 12px">
    <div class="row-wrap">
      <button class="btn" @click="showColumns = true">{{ $t('events.columns') }}</button>
      <button class="btn" :disabled="busy" @click="exportAs('csv')">
        {{ $t('events.exportCsv') }}
      </button>
      <button class="btn" :disabled="busy" @click="exportAs('json')">
        {{ $t('events.exportJson') }}
      </button>
      <span v-if="message" class="faint">{{ message }}</span>
      <div class="spacer" />
      <span class="faint">{{ page.data.value.total }} {{ $t('common.rows') }}</span>
    </div>

    <EventsTable
      :rows="page.data.value.rows"
      :visible-columns="visibleColumns"
      :loading="busy"
    />

    <Pagination
      :offset="offset"
      :limit="PAGE"
      :total="page.data.value.total"
      @update:offset="offset = $event"
    />

    <EventsColumnSettingsModal
      v-if="showColumns"
      :columns="COLUMNS"
      :selected="visibleColumns"
      @update:selected="visibleColumns = $event"
      @close="showColumns = false"
    />
  </div>
</template>