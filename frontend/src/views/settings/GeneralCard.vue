<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Card from "@/components/ui/Card.vue";
import Select from "@/components/ui/Select.vue";
import NumberInput from "@/components/ui/NumberInput.vue";
import Toggle from "@/components/ui/Toggle.vue";
import { useSettingsStore } from "@/stores/settings";
import { settingsPatch } from "@/api/settings";

/**
 * General settings.
 *
 * The timezone is the sensitive one: day buckets and range presets both use it, so changing it
 * makes the backend rewrite every event's local day and rebuild the rollups before the call
 * returns. The card shows that as a busy state instead of pretending it is instant.
 */
const settings = useSettingsStore();

const zones = computed(() => {
  const list = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  return list.length > 0 ? list : [settings.timezone];
});

const zone = ref(settings.timezone);
const idle = ref(0);
const burst = ref(0);
const concurrency = ref(0);
const autoSync = ref(false);
const serverHost = ref("");
const serverPort = ref(0);
const saved = ref("");
const error = ref("");

watch(
  () => settings.settings,
  (value) => {
    if (!value) return;
    zone.value = value.tz;
    idle.value = Number(value.idleIntervalSeconds);
    burst.value = Number(value.burstIntervalSeconds);
    concurrency.value = Number(value.concurrency);
    autoSync.value = value.autoSyncPrices;
    serverHost.value = value.serverHost;
    serverPort.value = Number(value.serverPort);
  },
  { immediate: true }
);

const nonLoopback = computed(
  () => serverHost.value !== "" && serverHost.value !== "localhost" && serverHost.value !== "127.0.0.1"
);

async function save() {
  saved.value = "";
  error.value = "";
  const patch = settingsPatch({
    tz: zone.value,
    idleIntervalSeconds: idle.value,
    burstIntervalSeconds: burst.value,
    concurrency: concurrency.value,
    autoSyncPrices: autoSync.value,
    serverHost: serverHost.value,
    serverPort: serverPort.value,
    theme: settings.settings?.theme ?? "system",
    locale: settings.settings?.locale ?? "en",
  });
  try {
    await settings.update(patch);
    saved.value = settings.timezone === zone.value ? "settings.saved" : "settings.rebuilt";
  } catch (err) {
    error.value = String(err);
  }
}
</script>

<template>
  <Card :title="$t('settings.general')">
    <div class="grid grid-2">
      <div class="field">
        <label for="tz">{{ $t('settings.timezone') }}</label>
        <Select
          id="tz"
          :model-value="zone"
          :options="zones.map((z) => ({ value: z, label: z }))"
          @update:model-value="zone = $event"
        />
        <span class="faint">{{ $t('settings.timezoneHint') }}</span>
      </div>
      <div class="field">
        <label for="idle">{{ $t('settings.idleInterval') }}</label>
        <NumberInput id="idle" v-model="idle" :min="1" />
      </div>
      <div class="field">
        <label for="burst">{{ $t('settings.burstInterval') }}</label>
        <NumberInput id="burst" v-model="burst" :min="1" />
      </div>
      <div class="field">
        <label for="conc">{{ $t('settings.concurrency') }}</label>
        <NumberInput id="conc" v-model="concurrency" :min="1" :max="16" />
      </div>
      <div class="field">
        <label for="host">{{ $t('settings.serverHost') }}</label>
        <input id="host" v-model="serverHost" class="input" />
      </div>
      <div class="field">
        <label for="port">{{ $t('settings.serverPort') }}</label>
        <NumberInput id="port" v-model="serverPort" :min="1" :max="65535" />
      </div>
    </div>

    <div style="margin-top: 12px">
      <Toggle v-model="autoSync" :label="$t('settings.autoSyncPrices')" />
    </div>

    <div v-if="nonLoopback" class="note" style="margin-top: 12px">
      {{ $t('settings.serverWarning') }}
    </div>

    <div class="row" style="margin-top: 14px">
      <button class="btn btn-primary" :disabled="settings.saving" @click="save">
        <span v-if="settings.saving" class="spinner" />
        {{ $t('common.save') }}
      </button>
      <span v-if="saved" class="faint">{{ $t(saved) }}</span>
      <span v-if="error" class="tag tag-err">{{ error }}</span>
    </div>
  </Card>
</template>