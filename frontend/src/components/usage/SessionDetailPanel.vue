<script setup lang="ts">
import { computed } from "vue";
import Modal from "@/components/ui/Modal.vue";
import ShareListCard from "@/components/usage/ShareListCard.vue";
import type { Totals } from "@/api/dashboard";
import type { BreakdownRow } from "@/api/dashboard";
import type { SessionRow } from "@/api/meta";
import { formatDateTime, formatMs, formatTokens, formatUSD } from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/**
 * The drill-down for one session: its identity, its totals and its model mix.
 *
 * Only accounting metadata is shown. The session's transcript is never displayed or read
 * beyond the fields the scan parsed.
 */
const props = defineProps<{
  row: SessionRow;
  totals: Totals | null;
  byModel: BreakdownRow[];
}>();

const emit = defineEmits<{ close: []; events: [row: SessionRow] }>();

const settings = useSettingsStore();

const rows = computed(() => [
  { key: "events.harness", value: props.row.harness },
  { key: "events.session", value: props.row.sessionId },
  { key: "events.project", value: props.row.project || "—" },
  { key: "events.model", value: props.row.model || "—" },
  { key: "events.agentType", value: props.row.agentType },
  { key: "events.agentName", value: props.row.agentName || "—" },
  { key: "sessions.started", value: formatDateTime(props.row.startedAt, settings.timezone) },
  { key: "sessions.updated", value: formatDateTime(props.row.updatedAt, settings.timezone) },
]);
</script>

<template>
  <Modal :title="$t('sessions.detail')" wide @close="emit('close')">
    <div class="grid grid-2">
      <div>
        <dl class="kv">
          <template v-for="item in rows" :key="item.key">
            <dt>{{ $t(item.key) }}</dt>
            <dd class="mono">{{ item.value }}</dd>
          </template>
        </dl>

        <div v-if="props.totals" class="grid grid-3" style="margin-top: 16px">
          <div class="stat">
            <div class="stat-label">{{ $t('metrics.tokens') }}</div>
            <div class="stat-value">{{ formatTokens(props.totals.total) }}</div>
          </div>
          <div class="stat">
            <div class="stat-label">{{ $t('metrics.cost') }}</div>
            <div class="stat-value">{{ formatUSD(props.totals.costUsd) }}</div>
          </div>
          <div class="stat">
            <div class="stat-label">{{ $t('metrics.latency') }}</div>
            <div class="stat-value">
              {{
                props.totals.latencyCount > 0
                  ? formatMs(props.totals.latencySumMs / props.totals.latencyCount)
                  : '—'
              }}
            </div>
          </div>
        </div>
      </div>

      <div>
        <div class="card-title" style="margin-bottom: 10px">{{ $t('metrics.byModel') }}</div>
        <ShareListCard :rows="props.byModel" :limit="8" />
      </div>
    </div>

    <div class="row" style="margin-top: 16px">
      <button class="btn btn-primary" @click="emit('events', props.row)">
        {{ $t('sessions.viewEvents') }}
      </button>
      <div class="spacer" />
      <button class="btn" @click="emit('close')">{{ $t('common.close') }}</button>
    </div>
  </Modal>
</template>