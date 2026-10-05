<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Card from "@/components/ui/Card.vue";
import Toggle from "@/components/ui/Toggle.vue";
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

const enabled = ref(false);
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
    enabled.value = value.sleepEnabled;
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
  const detail = sleep.status?.detail ?? "";
  switch (detail) {
    case "active":
      return "settings.statusActive";
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
    sleepEnabled: enabled.value,
    preventSystemSleep: system.value,
    preventDisplaySleep: display.value,
    preventLidClosedSleep: lid.value,
    sleepAfterSeconds: after.value,
    sleepActiveWindowSeconds: activeWindow.value,
  });
  try {
    await settings.update(patch);
    // Enabling the lid toggle needs the system-level flag; ask for it once. If the prompt
    // is declined the controller falls back to caffeinate, which only works on AC power.
    if (lid.value && clamshellSupported.value && !clamshell.value) {
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
    <p class="faint" style="margin-top: 0">{{ $t('settings.sleepHint') }}</p>

    <div
      v-if="sleep.status && !sleep.status.supported"
      class="note"
      style="margin-bottom: 12px"
    >
      {{ $t('settings.sleepUnsupported') }}
    </div>

    <Toggle v-model="enabled" :label="$t('settings.sleepEnabled')" />

    <div class="grid grid-2" style="margin-top: 12px">
      <div class="field">
        <label for="sleep-after">{{ $t('settings.sleepAfter') }}</label>
        <NumberInput id="sleep-after" v-model="after" :min="30" />
      </div>
      <div class="field">
        <label for="sleep-window">{{ $t('settings.sleepActiveWindow') }}</label>
        <NumberInput id="sleep-window" v-model="activeWindow" :min="15" />
      </div>
    </div>

    <div class="grid" style="gap: 8px; margin-top: 12px">
      <Toggle v-model="system" :label="$t('settings.preventSystemSleep')" />
      <Toggle v-model="display" :label="$t('settings.preventDisplaySleep')" />
      <Toggle v-model="lid" :label="$t('settings.preventLidClosedSleep')" />
    </div>

    <p v-if="clamshellSupported && lid" class="faint">{{ $t('settings.lidHint') }}</p>

    <div v-if="clamshell" class="note row-wrap" style="margin-top: 12px">
      <span>{{ $t('settings.clamshellActive') }}</span>
      <button class="btn" @click="restore">{{ $t('settings.restoreClamshell') }}</button>
    </div>

    <div v-if="sleep.status" class="faint" style="margin-top: 12px">
      {{ $t('settings.sleepStatus') }}: {{ $t(statusKey) }}
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
