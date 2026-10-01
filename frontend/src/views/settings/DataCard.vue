<script setup lang="ts">
import { onMounted, ref } from "vue";
import Card from "@/components/ui/Card.vue";
import * as api from "@/api/settings";
import * as syncApi from "@/api/sync";
import { formatBytes, formatDateTime, formatDuration } from "@/lib/format";
import { useSettingsStore } from "@/stores/settings";

/**
 * Database summary, the scan history, and the destructive reset.
 *
 * The reset is safe by construction: session files are never modified, so deleting every
 * stored row only costs the next scan's time.
 */
const settings = useSettingsStore();
const stats = ref<api.Stats | null>(null);
const runs = ref<syncApi.SyncRun[]>([]);
const confirming = ref(false);

async function load() {
  stats.value = await api.stats();
  runs.value = await syncApi.history(10);
}

onMounted(load);

async function reset() {
  confirming.value = false;
  await api.deleteAllData();
  await load();
}
</script>

<template>
  <Card :title="$t('settings.data')">
    <dl class="kv">
      <dt>{{ $t('settings.dbPath') }}</dt>
      <dd class="mono">{{ stats?.dbPath ?? '—' }}</dd>
      <dt>{{ $t('settings.dbSize') }}</dt>
      <dd class="num">{{ formatBytes(stats?.dbBytes) }}</dd>
      <dt>{{ $t('settings.dbEvents') }}</dt>
      <dd class="num">{{ stats?.events ?? 0 }}</dd>
      <dt>{{ $t('settings.dbSessions') }}</dt>
      <dd class="num">{{ stats?.sessions ?? 0 }}</dd>
      <dt>{{ $t('settings.unpricedModels') }}</dt>
      <dd class="num">{{ stats?.unpricedModels ?? 0 }}</dd>
    </dl>

    <div class="card-title" style="margin: 16px 0 8px">{{ $t('settings.syncHistory') }}</div>
    <div v-if="runs.length === 0" class="empty">{{ $t('common.noData') }}</div>
    <table v-else class="data">
      <thead>
        <tr>
          <th>{{ $t('events.time') }}</th>
          <th>{{ $t('filters.title') }}</th>
          <th class="right">{{ $t('metrics.events') }}</th>
          <th class="right">{{ $t('metrics.latency') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="run in runs" :key="run.id">
          <td class="num">{{ formatDateTime(run.startedAt, settings.timezone) }}</td>
          <td>
            {{ run.trigger }}
            <span v-if="run.error" class="tag tag-err" :title="run.error">{{ run.error.slice(0, 40) }}</span>
          </td>
          <td class="right num">+{{ run.eventsInserted }} ~{{ run.eventsUpdated }}</td>
          <td class="right num">{{ formatDuration(run.durationMs) }}</td>
        </tr>
      </tbody>
    </table>

    <div class="note" style="margin-top: 16px">{{ $t('settings.deleteDataHint') }}</div>
    <div class="row" style="margin-top: 10px">
      <button v-if="!confirming" class="btn btn-danger" @click="confirming = true">
        {{ $t('settings.deleteData') }}
      </button>
      <template v-else>
        <span class="faint">{{ $t('settings.deleteConfirm') }}</span>
        <button class="btn btn-danger" @click="reset">{{ $t('common.yes') }}</button>
        <button class="btn" @click="confirming = false">{{ $t('common.cancel') }}</button>
      </template>
      <div class="spacer" />
      <button class="btn" @click="load">{{ $t('common.refresh') }}</button>
    </div>
  </Card>
</template>