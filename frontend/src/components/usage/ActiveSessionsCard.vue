<script setup lang="ts">
import Card from "@/components/ui/Card.vue";
import type { SessionRow } from "@/api/meta";
import { formatDateTime, formatTokens } from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/** Sessions seen in the last few minutes, with what they have spent so far. */
const props = defineProps<{ sessions: SessionRow[] }>();

const settings = useSettingsStore();
</script>

<template>
  <Card
    :title="$t('realtime.activeSessions')"
    :subtitle="$t('realtime.activeSessionsHint')"
  >
    <div v-if="props.sessions.length === 0" class="empty">{{ $t('common.noData') }}</div>
    <table v-else class="data">
      <thead>
        <tr>
          <th>{{ $t('events.harness') }}</th>
          <th>{{ $t('events.session') }}</th>
          <th>{{ $t('events.project') }}</th>
          <th>{{ $t('sessions.updated') }}</th>
          <th class="right">{{ $t('metrics.tokens') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="session in props.sessions" :key="session.harness + session.sessionId">
          <td>{{ session.harness }}</td>
          <td class="mono">{{ session.sessionId.slice(0, 12) }}</td>
          <td class="mono" :title="session.project">{{ session.project || '—' }}</td>
          <td class="num">{{ formatDateTime(session.updatedAt, settings.timezone) }}</td>
          <td class="right num">{{ formatTokens(session.total) }}</td>
        </tr>
      </tbody>
    </table>
  </Card>
</template>