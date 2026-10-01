<script setup lang="ts">
import { useSettingsStore } from "@/stores/settings";
import type { ThemeChoice } from "@/lib/theme";

/** Theme and language, the two header controls every view shares. */
const settings = useSettingsStore();

async function setTheme(theme: ThemeChoice) {
  await settings.update({ theme } as never);
}
</script>

<template>
  <div class="row-wrap">
    <div class="spacer" />
    <div class="seg">
      <button :class="{ active: settings.theme === 'system' }" @click="setTheme('system')">
        {{ $t('settings.themeSystem') }}
      </button>
      <button :class="{ active: settings.theme === 'light' }" @click="setTheme('light')">
        {{ $t('settings.themeLight') }}
      </button>
      <button :class="{ active: settings.theme === 'dark' }" @click="setTheme('dark')">
        {{ $t('settings.themeDark') }}
      </button>
    </div>
    <div class="seg">
      <button
        :class="{ active: settings.locale === 'en' }"
        @click="settings.update({ locale: 'en' } as never)"
      >
        EN
      </button>
      <button
        :class="{ active: settings.locale === 'vi' }"
        @click="settings.update({ locale: 'vi' } as never)"
      >
        VI
      </button>
    </div>
  </div>
</template>