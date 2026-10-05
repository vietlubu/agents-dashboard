<script setup lang="ts">
import { onMounted, ref } from "vue";
import GeneralCard from "@/views/settings/GeneralCard.vue";
import SleepCard from "@/views/settings/SleepCard.vue";
import ScanRootsCard from "@/views/settings/ScanRootsCard.vue";
import PricingTableCard from "@/views/settings/PricingTableCard.vue";
import PriceRulesCard from "@/views/settings/PriceRulesCard.vue";
import DataCard from "@/views/settings/DataCard.vue";
import AboutCard from "@/views/settings/AboutCard.vue";
import * as app from "@/api/app";

/**
 * Settings. The scan-root, pricing and data cards load their own data on mount; the startup
 * warnings are fetched here because they are not tied to any one card.
 */
const warnings = ref<string[]>([]);

onMounted(async () => {
  warnings.value = await app.warnings();
});
</script>

<template>
  <div class="grid" style="gap: 14px">
    <div v-if="warnings.length" class="note">
      <div>{{ $t('settings.warnings') }}</div>
      <ul class="list-plain">
        <li v-for="warning in warnings" :key="warning">{{ warning }}</li>
      </ul>
    </div>

    <GeneralCard />
    <SleepCard />
    <ScanRootsCard />
    <PricingTableCard />
    <PriceRulesCard />
    <DataCard />
    <AboutCard />
  </div>
</template>