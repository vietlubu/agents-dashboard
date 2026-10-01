<script setup lang="ts">
import { computed, onMounted, onUnmounted } from "vue";
import { RouterLink, RouterView, useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import HeaderControls from "@/components/layout/HeaderControls.vue";
import SyncStatusPill from "@/components/layout/SyncStatusPill.vue";
import RangeControl from "@/components/layout/RangeControl.vue";
import FilterBar from "@/components/layout/FilterBar.vue";
import { useSettingsStore } from "@/stores/settings";
import { useSyncStore } from "@/stores/sync";
import { useFiltersStore } from "@/stores/filters";
import { useUpdateStore } from "@/stores/update";
import { applyTheme } from "@/lib/theme";

const settings = useSettingsStore();
const sync = useSyncStore();
const filters = useFiltersStore();
const update = useUpdateStore();
const { locale } = useI18n();
const route = useRoute();

// The range and filter bar are shared chrome on every data view; Settings does not use them.
const showChrome = computed(() => route.path !== "/settings");
// The scan status belongs to the screen that configures scanning; the data views reload
// themselves when the backend reports a write.
const showSyncStatus = computed(() => route.path === "/settings");

onMounted(async () => {
  update.start();
  await settings.load();
  applyTheme(settings.theme);
  locale.value = settings.locale;
  filters.preset = filters.preset || "7d";
  sync.start();

  // Follow a locale change made anywhere in the app.
  const stop = settings.$subscribe(() => {
    locale.value = settings.locale;
  });
  void stop;
});

onUnmounted(() => update.stop());
</script>

<template>
  <div class="app">
    <AppSidebar>
      <template #footer>
        <div class="faint" style="font-size: 11px">
          {{ settings.timezone }}
        </div>
      </template>
    </AppSidebar>

    <main class="main">
      <header class="head">
        <HeaderControls />
        <SyncStatusPill v-if="showSyncStatus" />
        <div v-if="showChrome" class="head-filters">
          <RangeControl />
          <FilterBar />
        </div>
      </header>

      <div v-if="sync.lastError" class="note" style="margin-bottom: 12px">
        {{ sync.lastError }}
      </div>

      <div v-if="update.status?.state === 'available'" class="note row-wrap" style="margin-bottom: 12px" aria-live="polite">
        <span>{{ $t('updates.available', { version: update.status.latestVersion }) }}</span>
        <RouterLink to="/settings">{{ $t('updates.openSettings') }}</RouterLink>
      </div>

      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.app {
  display: flex;
  height: 100%;
}

.main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  padding: 14px 16px 24px;
  overflow: auto;
}

.head {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-bottom: 14px;
}

.head-filters {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--border);
}

@media (max-width: 860px) {
  .app {
    flex-direction: column;
    height: auto;
  }
}
</style>