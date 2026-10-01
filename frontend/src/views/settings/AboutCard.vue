<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import Card from "@/components/ui/Card.vue";
import * as app from "@/api/app";
import { useUpdateStore } from "@/stores/update";

/** Build version, where the data lives, and any startup warning the backend recorded. */
const version = ref("");
const warnings = ref<string[]>([]);
const dbPath = ref("");
const loadError = ref("");
const confirmedVersion = ref("");
const update = useUpdateStore();
const { t } = useI18n();

const canInstall = computed(() => update.status?.enabled && update.status.state === "available" && update.status.canInstall);
const statusMessage = computed(() => {
  const status = update.status;
  if (!status) return t("common.loading");
  if (!status.enabled) {
    switch (status.reason) {
      case "server": return t("updates.disabled.server");
      case "development": return t("updates.disabled.development");
      default: return t("updates.disabled.unsupportedPlatform");
    }
  }
  switch (status.state) {
    case "checking": return t("updates.checking");
    case "up-to-date": return t("updates.upToDate");
    case "available": return t("updates.available", { version: status.latestVersion });
    case "installing": return t("updates.installing");
    case "restarting": return t("updates.restarting");
    case "error": return t("updates.error");
    default: return t("updates.idle");
  }
});

const blockedMessage = computed(() => {
  if (update.status?.state !== "available" || update.status.canInstall) return "";
  switch (update.status.reason) {
    case "not-writable": return t("updates.blocked.notWritable");
    case "cross-filesystem": return t("updates.blocked.crossFilesystem");
    case "unsupported-platform": return t("updates.disabled.unsupportedPlatform");
    default: return "";
  }
});

watch(
  [() => update.status?.latestVersion, () => update.status?.state, canInstall],
  () => { confirmedVersion.value = ""; },
  { flush: "sync" }
);

function confirmUpdate() {
  if (canInstall.value && !update.busy) confirmedVersion.value = update.status!.latestVersion;
}

async function installConfirmed() {
  const expectedVersion = confirmedVersion.value;
  confirmedVersion.value = "";
  if (!expectedVersion || expectedVersion !== update.status?.latestVersion || !canInstall.value || update.busy) return;
  await update.installUpdate(expectedVersion);
}

async function checkForUpdates() {
  confirmedVersion.value = "";
  await update.checkForUpdates();
}

onMounted(async () => {
  try {
    version.value = await app.version();
    warnings.value = await app.warnings();
    const settings = await import("@/api/settings");
    dbPath.value = (await settings.stats()).dbPath;
  } catch (err) {
    loadError.value = String(err);
  }
});
</script>

<template>
  <Card :title="$t('settings.about')">
    <dl class="kv">
      <dt>{{ $t('settings.version') }}</dt>
      <dd class="mono">{{ version || '—' }}</dd>
      <dt>{{ $t('settings.database') }}</dt>
      <dd class="mono">{{ dbPath || '—' }}</dd>
      <dt>{{ $t('settings.scanRoots') }}</dt>
      <dd class="faint">
        Claude Code · Codex · OpenCode · Pi · omp
      </dd>
    </dl>

    <div style="margin-top: 16px">
      <div class="row" aria-live="polite">
        <span v-if="update.busy" class="spinner" aria-hidden="true" />
        <span>{{ statusMessage }}</span>
      </div>

      <div v-if="blockedMessage" class="note" style="margin-top: 10px">{{ blockedMessage }}</div>
      <div v-if="update.error || loadError" class="note" style="margin-top: 10px" role="alert">
        {{ update.error || loadError }}
      </div>

      <div v-if="update.status?.releaseUrl" style="margin-top: 10px">
        <a :href="update.status.releaseUrl" target="_blank" rel="noopener noreferrer">{{ $t('updates.viewRelease') }}</a>
      </div>

      <div v-if="update.status?.enabled" class="row-wrap" style="margin-top: 12px">
        <template v-if="canInstall">
          <button v-if="!confirmedVersion" class="btn btn-primary" :disabled="update.busy" @click="confirmUpdate">
            {{ $t('updates.install') }}
          </button>
          <template v-else>
            <span class="faint">{{ $t('updates.confirm', { version: confirmedVersion }) }}</span>
            <button class="btn btn-primary" :disabled="update.busy" @click="installConfirmed">{{ $t('common.yes') }}</button>
            <button class="btn" :disabled="update.busy" @click="confirmedVersion = ''">{{ $t('common.cancel') }}</button>
          </template>
        </template>
        <button class="btn" :disabled="update.busy" @click="checkForUpdates">{{ $t('updates.check') }}</button>
      </div>
    </div>

    <div v-if="warnings.length" class="note" style="margin-top: 12px">
      <div>{{ $t('settings.warnings') }}</div>
      <ul class="list-plain">
        <li v-for="warning in warnings" :key="warning">{{ warning }}</li>
      </ul>
    </div>
  </Card>
</template>