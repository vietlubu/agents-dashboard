<script setup lang="ts">
import { onMounted, ref } from "vue";
import Card from "@/components/ui/Card.vue";
import * as app from "@/api/app";

/** Build version, where the data lives, and any startup warning the backend recorded. */
const version = ref("");
const warnings = ref<string[]>([]);
const dbPath = ref("");

onMounted(async () => {
  version.value = await app.version();
  warnings.value = await app.warnings();
  const settings = await import("@/api/settings");
  dbPath.value = (await settings.stats()).dbPath;
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

    <div v-if="warnings.length" class="note" style="margin-top: 12px">
      <div>{{ $t('settings.warnings') }}</div>
      <ul class="list-plain">
        <li v-for="warning in warnings" :key="warning">{{ warning }}</li>
      </ul>
    </div>
  </Card>
</template>