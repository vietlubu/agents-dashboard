<script setup lang="ts">
import { computed, ref } from "vue";
import Card from "@/components/ui/Card.vue";
import StatTile from "@/components/ui/StatTile.vue";
import RealtimeVelocityCard from "@/components/usage/RealtimeVelocityCard.vue";
import ActiveSessionsCard from "@/components/usage/ActiveSessionsCard.vue";
import LatencyScatterCard from "@/components/usage/LatencyScatterCard.vue";
import ShareListCard from "@/components/usage/ShareListCard.vue";
import { useLiveQuery } from "@/composables/useLiveQuery";
import * as dashboard from "@/api/dashboard";
import { formatMs, formatTokens } from "@/lib/format";

const minutes = ref(60);
const WINDOWS = [15, 30, 60];

const snapshot = useLiveQuery(
  () => dashboard.realtime(minutes.value),
  null as dashboard.RealtimeSnapshot | null,
  [() => minutes.value]
);

// The window is local to this view; the shared filters and the scan's data signal still drive
// the reload through useLiveQuery.
const avgLatency = computed(() => {
  const totals = snapshot.data.value?.totals;
  if (!totals || totals.latencyCount === 0) return "—";
  return formatMs(totals.latencySumMs / totals.latencyCount);
});
</script>

<template>
  <div class="grid" style="gap: 14px">
    <div class="row-wrap">
      <div class="seg">
        <button
          v-for="option in WINDOWS"
          :key="option"
          :class="{ active: minutes === option }"
          @click="minutes = option"
        >
          {{ $t('realtime.minutes', { n: option }) }}
        </button>
      </div>
      <span class="faint">{{ $t('realtime.window') }}</span>
    </div>

    <div class="grid grid-4">
      <Card>
        <StatTile
          :label="$t('metrics.tokens')"
          :value="formatTokens(snapshot.data.value?.totals.total)"
        />
      </Card>
      <Card>
        <StatTile
          :label="$t('metrics.events')"
          :value="String(snapshot.data.value?.totals.events ?? 0)"
        />
      </Card>
      <Card>
        <StatTile :label="$t('metrics.active')" :value="String(snapshot.data.value?.activeSessions.length ?? 0)" />
      </Card>
      <Card>
        <StatTile
          :label="$t('metrics.latency')"
          :value="avgLatency"
          :hint="`${$t('metrics.coverage')}: ${snapshot.data.value?.totals.latencyCount ?? 0}`"
        />
      </Card>
    </div>

    <RealtimeVelocityCard
      v-if="snapshot.data.value"
      :buckets="snapshot.data.value.buckets"
      :window-minutes="minutes"
    />

    <div class="grid grid-2">
      <ActiveSessionsCard v-if="snapshot.data.value" :sessions="snapshot.data.value.activeSessions" />
      <Card :title="$t('metrics.byModel')">
        <ShareListCard :rows="snapshot.data.value?.byModel ?? []" :limit="5" />
      </Card>
    </div>

    <LatencyScatterCard v-if="snapshot.data.value" :points="snapshot.data.value.latency" />
  </div>
</template>