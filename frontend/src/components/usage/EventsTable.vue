<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useVirtualizer } from "@tanstack/vue-virtual";
import Card from "@/components/ui/Card.vue";
import type { EventRow } from "@/api/events";
import { formatDateTime, formatMs, formatTokens, formatUSD } from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/**
 * The event table is the only one that can hold hundreds of thousands of rows, so it renders
 * a window instead of the full set: a range over a year is a few hundred thousand rows and
 * mounting that many table rows would stall the window.
 */
const props = withDefaults(
  defineProps<{
    rows: EventRow[];
    visibleColumns: string[];
    loading?: boolean;
  }>(),
  { loading: false }
);

const settings = useSettingsStore();
const scrollRef = ref<HTMLElement | null>(null);

const ROW_HEIGHT = 30;

// The options are passed as a computed so the virtualizer sees a new row count when the page
// changes; the adapter accepts a Ref of options.
const virtualizer = useVirtualizer(
  computed(() => ({
    count: props.rows.length,
    getScrollElement: () => scrollRef.value,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  }))
);

const virtualRows = computed(() => virtualizer.value.getVirtualItems());
const totalHeight = computed(() => virtualizer.value.getTotalSize());

// A new page of rows starts at the top.
watch(
  () => props.rows,
  () => scrollRef.value?.scrollTo({ top: 0 })
);

const ALL_COLUMNS: { key: string; label: string; right?: boolean; render: (r: EventRow) => string }[] = [
  { key: "harness", label: "events.harness", render: (r) => r.harness },
  { key: "time", label: "events.time", render: (r) => formatDateTime(r.ts, settings.timezone) },
  { key: "project", label: "events.project", render: (r) => r.project || "—" },
  { key: "sessionId", label: "events.session", render: (r) => r.sessionId?.slice(0, 12) ?? "—" },
  { key: "model", label: "events.model", render: (r) => r.model },
  { key: "provider", label: "events.provider", render: (r) => r.provider || "—" },
  { key: "agentType", label: "events.agentType", render: (r) => r.agentType },
  { key: "agentName", label: "events.agentName", render: (r) => r.agentName || "—" },
  { key: "outcome", label: "events.outcome", render: (r) => r.outcome },
  { key: "input", label: "metrics.input", right: true, render: (r) => formatTokens(r.input) },
  { key: "output", label: "metrics.output", right: true, render: (r) => formatTokens(r.output) },
  { key: "cacheRead", label: "metrics.cacheRead", right: true, render: (r) => formatTokens(r.cacheRead) },
  { key: "cacheWrite", label: "metrics.cacheWrite", right: true, render: (r) => formatTokens(r.cacheWrite) },
  { key: "total", label: "metrics.tokens", right: true, render: (r) => formatTokens(r.total) },
  {
    key: "costUsd",
    label: "metrics.cost",
    right: true,
    render: (r) => (r.costUsd === null ? "—" : formatUSD(r.costUsd)),
  },
  { key: "costSource", label: "events.costSource", render: (r) => r.costSource },
  {
    key: "latencyMs",
    label: "metrics.latency",
    right: true,
    render: (r) => (r.latencyMs === null ? "—" : formatMs(r.latencyMs)),
  },
  { key: "ttftMs", label: "metrics.ttft", right: true, render: (r) => (r.ttftMs === null ? "—" : formatMs(r.ttftMs)) },
];

const columns = computed(() =>
  ALL_COLUMNS.filter((column) => props.visibleColumns.includes(column.key))
);

const gridTemplate = computed(
  () => `repeat(${columns.value.length}, minmax(0, max-content))`
);

function cellClass(column: { right?: boolean }): string {
  return column.right ? "right num" : "";
}
</script>

<template>
  <Card :padded="false">
    <div v-if="props.rows.length === 0" class="empty" style="padding: 40px">
      {{ props.loading ? $t('common.loading') : $t('common.noData') }}
    </div>
    <div v-else ref="scrollRef" class="events-scroll">
      <div class="events-head" :style="{ gridTemplateColumns: gridTemplate }">
        <div
          v-for="column in columns"
          :key="column.key"
          :class="column.right ? 'right' : ''"
        >
          {{ $t(column.label) }}
        </div>
      </div>
      <div class="events-body" :style="{ height: totalHeight + 'px' }">
        <div
          v-for="item in virtualRows"
          :key="String(item.key)"
          class="events-row"
          :style="{
            gridTemplateColumns: gridTemplate,
            transform: `translateY(${item.start}px)`,
            height: ROW_HEIGHT + 'px',
          }"
        >
          <div v-for="column in columns" :key="column.key" :class="cellClass(column)">
            {{ column.render(props.rows[item.index]) }}
          </div>
        </div>
      </div>
    </div>
  </Card>
</template>

<style scoped>
.events-scroll {
  max-height: 62vh;
  overflow: auto;
}

.events-head,
.events-row {
  display: grid;
  gap: 10px;
  align-items: center;
  padding: 0 12px;
  font-size: 13px;
}

.events-head {
  position: sticky;
  top: 0;
  z-index: 2;
  height: 30px;
  background: var(--bg-sunken);
  border-bottom: 1px solid var(--border);
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  white-space: nowrap;
}

.events-body {
  position: relative;
}

.events-row {
  position: absolute;
  left: 0;
  right: 0;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}

.events-row:hover {
  background: var(--bg-sunken);
}
</style>