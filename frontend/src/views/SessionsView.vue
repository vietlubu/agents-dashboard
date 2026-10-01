<script setup lang="ts">
import { ref, watch } from "vue";
import { useRouter } from "vue-router";
import SessionsTable from "@/components/usage/SessionsTable.vue";
import SessionDetailPanel from "@/components/usage/SessionDetailPanel.vue";
import { useLiveQuery } from "@/composables/useLiveQuery";
import * as meta from "@/api/meta";
import { useFiltersStore } from "@/stores/filters";

/**
 * The session list. Selecting a row opens the detail panel, which shows accounting metadata
 * only — the transcript itself is never displayed.
 */
const filters = useFiltersStore();
const router = useRouter();

const offset = ref(0);
const sortBy = ref("updated");
const detail = ref<meta.SessionRow | null>(null);

const PAGE = 50;

const page = useLiveQuery(
  () => meta.sessions(filters.query, offset.value, PAGE, sortBy.value),
  { rows: [], total: 0 } as meta.SessionPage,
  [() => offset.value, () => sortBy.value]
);

const sessionTotals = ref<Awaited<ReturnType<typeof meta.sessionDetail>> | null>(null);

watch(detail, async (row) => {
  if (!row) {
    sessionTotals.value = null;
    return;
  }
  sessionTotals.value = await meta.sessionDetail(row.harness, row.sessionId);
});

function showEvents(row: meta.SessionRow) {
  // The events page has no session filter of its own; select it there by harness and let the
  // user narrow from the shared filters.
  filters.harness = [row.harness];
  detail.value = null;
  void router.push("/events");
}
</script>

<template>
  <div class="grid" style="gap: 12px">
    <div class="faint">{{ $t('sessions.subtitle') }}</div>

    <SessionsTable
      :rows="page.data.value.rows"
      :total="page.data.value.total"
      :loading="page.loading.value"
      @page="offset = $event"
      @sort="sortBy = $event"
      @select="detail = $event"
    />

    <SessionDetailPanel
      v-if="detail"
      :row="detail"
      :totals="sessionTotals?.totals ?? null"
      :by-model="sessionTotals?.byModel ?? []"
      @close="detail = null"
      @events="showEvents"
    />
  </div>
</template>