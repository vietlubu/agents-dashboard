<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import Card from "@/components/ui/Card.vue";
import Toggle from "@/components/ui/Toggle.vue";
import Select from "@/components/ui/Select.vue";
import NumberInput from "@/components/ui/NumberInput.vue";
import { useSettingsStore } from "@/stores/settings";
import { useSleepStore } from "@/stores/sleep";
import { settingsPatch } from "@/api/settings";
import * as sleepApi from "@/api/sleep";

/**
 * Sleep control.
 *
 * The backend decides when to hold the machine awake; this card only edits the settings and
 * surfaces the resulting status. The lid-closed toggle is the one with a side effect: on
 * macOS it raises one administrator prompt and disables system sleep system-wide until it is
 * restored, so the card always shows a way back.
 */
const settings = useSettingsStore();
const sleep = useSleepStore();

const { t } = useI18n();
const mode = ref("off");
const modeOptions = computed(() => [
  { value: "off", label: t("settings.sleepModeOff") },
  { value: "agent", label: t("settings.sleepModeAgent"), disabled: sleep.status?.supported === false },
  { value: "always", label: t("settings.sleepModeAlways"), disabled: sleep.status?.supported === false },
]);
const system = ref(true);
const display = ref(true);
const lid = ref(false);
const after = ref(300);
const activeWindow = ref(120);

const saved = ref("");
const error = ref("");
const clamshellError = ref("");
const clamshellSupported = ref(false);

watch(
  () => settings.settings,
  (value) => {
    if (!value) return;
    mode.value = value.sleepMode;
    system.value = value.preventSystemSleep;
    display.value = value.preventDisplaySleep;
    lid.value = value.preventLidClosedSleep;
    after.value = Number(value.sleepAfterSeconds);
    activeWindow.value = Number(value.sleepActiveWindowSeconds);
  },
  { immediate: true }
);

void sleepApi.clamshellSupported().then((v) => (clamshellSupported.value = v));

const statusKey = computed(() => {
  if (sleep.status?.error) return "settings.statusError";
  const detail = sleep.status?.detail ?? "";
  switch (detail) {
    case "always":
      return sleep.keepingAwake ? "settings.statusAlways" : "settings.sleepNoTargets";
    case "active":
      return sleep.keepingAwake ? "settings.statusActive" : "settings.sleepNoTargets";
    case "grace":
      return "settings.statusGrace";
    case "waiting-user":
      return "settings.statusWaitingUser";
    case "sleeping":
      return "settings.statusSleeping";
    case "unsupported":
      return "settings.statusUnsupported";
    case "disabled":
      return "settings.statusDisabled";
    default:
      return "settings.statusIdle";
  }
});

const clamshell = computed(() => sleep.status?.clamshell ?? false);

async function save() {
  saved.value = "";
  error.value = "";
  clamshellError.value = "";
  const patch = settingsPatch({
    sleepMode: mode.value,
    preventSystemSleep: system.value,
    preventDisplaySleep: display.value,
    preventLidClosedSleep: lid.value,
    ...(mode.value === "agent" ? {
      sleepAfterSeconds: after.value,
      sleepActiveWindowSeconds: activeWindow.value,
    } : {}),
  });
  try {
    await settings.update(patch);
    // Only an explicit Save may request the machine-wide flag; cancellation keeps the choice.
    const effective = settings.settings;
    if (effective && effective.sleepMode !== "off" && effective.preventLidClosedSleep && clamshellSupported.value && !clamshell.value) {
      try {
        await sleepApi.requestClamshell();
      } catch (err) {
        clamshellError.value = String(err);
      }
    }
    saved.value = "settings.saved";
    await sleep.refresh();
  } catch (err) {
    error.value = String(err);
  }
}

async function restore() {
  clamshellError.value = "";
  try {
    await sleepApi.restoreClamshell();
    await sleep.refresh();
  } catch (err) {
    clamshellError.value = String(err);
  }
}
</script>

<template>
  <Card :title="$t('settings.sleep')">
    <p class="faint" style="margin-top: 0">
      {{ $t(mode === 'off' ? 'settings.sleepOffHint' : mode === 'always' ? 'settings.sleepAlwaysHint' : 'settings.sleepHint') }}
    </p>

    <div
      v-if="sleep.status && !sleep.status.supported"
      class="note"
      style="margin-bottom: 12px"
    >
      {{ $t('settings.sleepUnsupported') }}
    </div>

    <div class="field">
      <label for="sleep-mode">{{ $t('settings.sleepMode') }}</label>
      <Select id="sleep-mode" v-model="mode" :options="modeOptions" />
    </div>

    <div v-if="mode === 'agent'" class="grid grid-2" style="margin-top: 12px">
      <div class="field">
        <label for="sleep-after">{{ $t('settings.sleepAfter') }}</label>
        <NumberInput id="sleep-after" v-model="after" :min="30" />
      </div>
      <div class="field">
        <label for="sleep-window">{{ $t('settings.sleepActiveWindow') }}</label>
        <NumberInput id="sleep-window" v-model="activeWindow" :min="15" />
      </div>
    </div>

    <fieldset :disabled="mode === 'off' || sleep.status?.supported === false" class="grid sleep-scopes" style="gap: 8px; margin-top: 12px">
      <Toggle v-model="system" :label="$t('settings.preventSystemSleep')" />
      <Toggle v-model="display" :label="$t('settings.preventDisplaySleep')" />
      <Toggle v-model="lid" :label="$t('settings.preventLidClosedSleep')" />
    </fieldset>

    <p v-if="mode !== 'off' && !system && !display && !lid" class="note">{{ $t('settings.sleepNoTargets') }}</p>

    <p v-if="clamshellSupported && lid" class="faint">{{ $t('settings.lidHint') }}</p>

    <div v-if="clamshell" class="note row-wrap" style="margin-top: 12px">
      <span>{{ $t('settings.clamshellActive') }}</span>
      <button class="btn" @click="restore">{{ $t('settings.restoreClamshell') }}</button>
    </div>

    <div v-if="sleep.status" class="faint" style="margin-top: 12px">
      {{ $t('settings.sleepStatus') }}: {{ $t(statusKey) }}
      <span v-if="sleep.status.error">: {{ sleep.status.error }}</span>
    </div>

    <div class="row" style="margin-top: 14px">
      <button class="btn btn-primary" :disabled="settings.saving" @click="save">
        <span v-if="settings.saving" class="spinner" />
        {{ $t('common.save') }}
      </button>
      <span v-if="saved" class="faint">{{ $t(saved) }}</span>
      <span v-if="error" class="tag tag-err">{{ error }}</span>
    </div>
    <div v-if="clamshellError" class="note" style="margin-top: 8px">{{ clamshellError }}</div>
  </Card>
</template>

<style scoped>
.sleep-scopes {
  border: 0;
  padding: 0;
  margin-inline: 0;
  min-width: 0;
}
</style>
