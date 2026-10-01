<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useSyncStore } from "@/stores/sync";
import { formatRelative } from "@/lib/format";

/**
 * The scan status control: whether a scan is running, and a manual trigger or cancel.
 *
 * It is mounted on the Settings screen only. Every data view refetches by itself when the
 * backend reports a write, so a status that flips on each background scan belongs where the
 * scanning itself is configured, not in the header of every page.
 */
const sync = useSyncStore();
const { t } = useI18n();

const label = computed(() => {
  if (sync.running) {
    const p = sync.progress;
    return p ? `${t("common.scanning")} ${p.harness} ${p.walked}` : t("common.scanning");
  }
  if (sync.lastFinishedAt) {
    return `${t("common.idle")} · ${formatRelative(sync.lastFinishedAt)}`;
  }
  return t("common.idle");
});

const intervalSeconds = computed(() => Math.round(sync.intervalMs / 1000));

async function toggle() {
  if (sync.running) {
    await sync.cancel();
    return;
  }
  await sync.triggerNow();
}
</script>

<template>
  <div class="row-wrap">
    <button class="pill" :class="{ busy: sync.running }" @click="toggle">
      <span v-if="sync.running" class="spinner" />
      {{ label }}
    </button>
    <span v-if="intervalSeconds > 0" class="faint" style="font-size: 12px">
      {{ $t('common.scanEvery', { s: intervalSeconds }) }}
    </span>
  </div>
</template>
