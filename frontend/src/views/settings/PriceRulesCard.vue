<script setup lang="ts">
import { onMounted, ref } from "vue";
import Card from "@/components/ui/Card.vue";
import Toggle from "@/components/ui/Toggle.vue";
import * as api from "@/api/settings";

/**
 * Per-model price rules: a multiplier per token class, and a switch that removes a model from
 * the cost totals entirely. The switch is what a subscription-billed model needs — its token
 * count is real, but putting an API price on it would be wrong.
 */
const rules = ref<api.PriceRule[]>([]);
const draftKey = ref("");

function empty(): api.PriceRule {
  return {
    modelKey: draftKey.value.trim().toLowerCase(),
    inputMult: 1,
    outputMult: 1,
    cacheReadMult: 1,
    cacheWriteMult: 1,
    disabled: false,
  };
}

onMounted(async () => {
  rules.value = await api.priceRules();
});

async function add() {
  if (!draftKey.value.trim()) return;
  rules.value = await api.savePriceRule(empty());
  draftKey.value = "";
}

async function update(rule: api.PriceRule, patch: Partial<api.PriceRule>) {
  rules.value = await api.savePriceRule({ ...rule, ...patch });
}
</script>

<template>
  <Card :title="$t('settings.rules')" :subtitle="$t('settings.rulesHint')">
    <div class="row-wrap">
      <input
        v-model="draftKey"
        class="input"
        :placeholder="$t('settings.priceKey')"
        @keyup.enter="add"
      />
      <button class="btn btn-primary" @click="add">{{ $t('common.add') }}</button>
    </div>

    <div v-if="rules.length === 0" class="empty">{{ $t('common.noData') }}</div>
    <table v-else class="data" style="margin-top: 12px">
      <thead>
        <tr>
          <th>{{ $t('settings.priceKey') }}</th>
          <th class="right">{{ $t('settings.priceInput') }}</th>
          <th class="right">{{ $t('settings.priceOutput') }}</th>
          <th class="right">{{ $t('settings.priceCacheRead') }}</th>
          <th class="right">{{ $t('settings.priceCacheWrite') }}</th>
          <th>{{ $t('settings.ruleDisabled') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="rule in rules" :key="rule.modelKey">
          <td class="mono">{{ rule.modelKey }}</td>
          <td class="right">
            <input
              class="input"
              style="width: 80px"
              type="number"
              step="0.1"
              min="0"
              :value="rule.inputMult"
              @change="update(rule, { inputMult: Number(($event.target as HTMLInputElement).value) })"
            />
          </td>
          <td class="right">
            <input
              class="input"
              style="width: 80px"
              type="number"
              step="0.1"
              min="0"
              :value="rule.outputMult"
              @change="update(rule, { outputMult: Number(($event.target as HTMLInputElement).value) })"
            />
          </td>
          <td class="right">
            <input
              class="input"
              style="width: 80px"
              type="number"
              step="0.1"
              min="0"
              :value="rule.cacheReadMult"
              @change="update(rule, { cacheReadMult: Number(($event.target as HTMLInputElement).value) })"
            />
          </td>
          <td class="right">
            <input
              class="input"
              style="width: 80px"
              type="number"
              step="0.1"
              min="0"
              :value="rule.cacheWriteMult"
              @change="update(rule, { cacheWriteMult: Number(($event.target as HTMLInputElement).value) })"
            />
          </td>
          <td>
            <Toggle
              :model-value="rule.disabled"
              :label="''"
              @update:model-value="update(rule, { disabled: $event })"
            />
          </td>
        </tr>
      </tbody>
    </table>
  </Card>
</template>