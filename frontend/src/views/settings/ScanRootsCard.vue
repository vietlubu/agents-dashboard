<script setup lang="ts">
import { onMounted, ref } from "vue";
import Card from "@/components/ui/Card.vue";
import Select from "@/components/ui/Select.vue";
import * as api from "@/api/settings";
import * as meta from "@/api/meta";

/**
 * Scan roots: the extra directories read in addition to each harness's default location.
 *
 * Adding a root triggers a scan immediately, so the card also shows the harness report — the
 * fastest way to confirm a path actually contributed something.
 */
const roots = ref<api.ScanRoot[]>([]);
const report = ref<meta.HarnessReportView[]>([]);
const harnesses = ref<meta.HarnessInfo[]>([]);
const newRoot = ref("");
const newHarness = ref("claude");
const error = ref("");
const busy = ref(false);

async function load() {
  roots.value = await api.scanRoots();
  harnesses.value = await meta.harnesses();
  report.value = await meta.harnessReport();
}

onMounted(load);

async function add() {
  error.value = "";
  if (!newRoot.value.trim()) return;
  busy.value = true;
  try {
    roots.value = await api.addScanRoot(newHarness.value, newRoot.value.trim());
    newRoot.value = "";
    report.value = await meta.harnessReport();
  } catch (err) {
    error.value = String(err);
  } finally {
    busy.value = false;
  }
}

async function remove(root: api.ScanRoot) {
  roots.value = await api.removeScanRoot(root.harness, root.path);
}
</script>

<template>
  <Card :title="$t('settings.scanRoots')" :subtitle="$t('settings.scanRootsHint')">
    <div class="row-wrap">
      <Select
        :model-value="newHarness"
        :options="harnesses.map((h) => ({ value: h.id, label: h.name }))"
        @update:model-value="newHarness = $event"
      />
      <input
        v-model="newRoot"
        class="input"
        style="flex: 1; min-width: 240px"
        :placeholder="$t('settings.rootPath')"
        @keyup.enter="add"
      />
      <button class="btn btn-primary" :disabled="busy" @click="add">{{ $t('settings.addRoot') }}</button>
    </div>
    <div v-if="error" class="tag tag-err" style="margin-top: 8px">{{ error }}</div>

    <table v-if="roots.length" class="data" style="margin-top: 12px">
      <thead>
        <tr>
          <th>{{ $t('filters.harness') }}</th>
          <th>{{ $t('settings.rootPath') }}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <tr v-for="root in roots" :key="root.harness + root.path">
          <td>{{ root.harness }}</td>
          <td class="mono">{{ root.path }}</td>
          <td>
            <span class="tag" :class="root.exists ? 'tag-ok' : 'tag-warn'">
              {{ root.exists ? $t('settings.rootExists') : $t('settings.rootMissing') }}
            </span>
          </td>
          <td class="right">
            <button class="btn btn-danger" @click="remove(root)">{{ $t('common.delete') }}</button>
          </td>
        </tr>
      </tbody>
    </table>

    <div class="card-title" style="margin: 18px 0 8px">{{ $t('settings.runReport') }}</div>
    <div class="faint" style="margin-bottom: 8px">{{ $t('settings.runReportHint') }}</div>
    <table class="data">
      <thead>
        <tr>
          <th>{{ $t('filters.harness') }}</th>
          <th>{{ $t('common.yes') }}/{{ $t('common.no') }}</th>
          <th class="right">{{ $t('metrics.sessions') }}</th>
          <th class="right">{{ $t('metrics.events') }}</th>
          <th class="right">{{ $t('metrics.tokens') }}</th>
          <th class="right">{{ $t('metrics.cost') }}</th>
          <th class="right">{{ $t('metrics.latency') }}</th>
          <th>{{ $t('settings.scanRoots') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in report" :key="row.id">
          <td>{{ row.name }}</td>
          <td>
            <span class="tag" :class="row.available ? 'tag-ok' : 'tag-warn'">
              {{ row.available ? $t('common.yes') : $t('common.no') }}
            </span>
          </td>
          <td class="right num">{{ row.sessions }}</td>
          <td class="right num">{{ row.events }}</td>
          <td class="right num">{{ row.total }}</td>
          <td class="right num">{{ row.costUsd.toFixed(4) }}</td>
          <td class="right num">{{ row.latencyRows }}</td>
          <td class="mono faint" :title="row.roots.join('\n')">
            {{ row.roots.length ? row.roots[0] : '—' }}
          </td>
        </tr>
      </tbody>
    </table>
  </Card>
</template>