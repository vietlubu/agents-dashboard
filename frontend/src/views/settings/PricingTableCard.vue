<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import Card from "@/components/ui/Card.vue";
import * as api from "@/api/settings";

/**
 * The price table.
 *
 * Nothing is priced by default: a cost is never invented. Rates come from a synced catalog or
 * from a manual entry, and a cost the harness reported itself always wins over either. The
 * unpriced-model list is the actionable part — those models are why a total can look low.
 */
const prices = ref<api.ModelPrice[]>([]);
const unpriced = ref<string[]>([]);
const message = ref("");
const error = ref("");
const busy = ref(false);
const search = ref("");
const draft = ref<api.ModelPrice>(emptyRow());

function emptyRow(): api.ModelPrice {
  return {
    modelKey: "",
    inputPerM: 0,
    outputPerM: 0,
    cacheReadPerM: 0,
    cacheWritePerM: 0,
    source: "manual",
    updatedAt: 0,
  };
}

const filtered = computed(() => {
  const term = search.value.trim().toLowerCase();
  const list = term ? prices.value.filter((p) => p.modelKey.includes(term)) : prices.value;
  return [...list].sort((a, b) => a.modelKey.localeCompare(b.modelKey)).slice(0, 200);
});

async function load() {
  prices.value = await api.prices();
  const dashboard = await import("@/api/dashboard");
  unpriced.value = await dashboard.unpricedModels();
}

onMounted(load);

async function sync(source: string) {
  busy.value = true;
  message.value = "";
  error.value = "";
  try {
    const result = await api.syncPrices(source);
    message.value = `${source}: ${result.models} models, ${result.skipped} skipped`;
    await load();
  } catch (err) {
    error.value = String(err);
  } finally {
    busy.value = false;
  }
}

async function recalculate() {
  busy.value = true;
  error.value = "";
  try {
    const updated = await api.recalculateCosts();
    message.value = `recalculated ${updated} events`;
    await load();
  } catch (err) {
    error.value = String(err);
  } finally {
    busy.value = false;
  }
}

async function saveDraft() {
  if (!draft.value.modelKey.trim()) return;
  prices.value = await api.savePrice({ ...draft.value, modelKey: draft.value.modelKey.trim().toLowerCase() });
  draft.value = emptyRow();
  await load();
}

async function remove(row: api.ModelPrice) {
  prices.value = await api.deletePrice(row.modelKey);
  await load();
}

function edit(row: api.ModelPrice) {
  draft.value = { ...row };
}
</script>

<template>
  <Card :title="$t('settings.pricing')" :subtitle="$t('settings.pricingHint')">
    <div class="row-wrap">
      <button class="btn" :disabled="busy" @click="sync('models-dev')">
        {{ $t('settings.syncModelsDev') }}
      </button>
      <button class="btn" :disabled="busy" @click="sync('litellm')">
        {{ $t('settings.syncLiteLLM') }}
      </button>
      <button class="btn" :disabled="busy" @click="recalculate">
        {{ $t('settings.recalculate') }}
      </button>
      <span v-if="busy" class="spinner" />
      <span v-if="message" class="faint">{{ message }}</span>
      <span v-if="error" class="tag tag-err">{{ error }}</span>
    </div>

    <div v-if="unpriced.length" class="note" style="margin-top: 12px">
      <div>{{ $t('settings.unpricedModels') }}</div>
      <div class="mono" style="margin-top: 6px">{{ unpriced.slice(0, 12).join(', ') }}</div>
    </div>

    <div class="grid grid-5" style="margin-top: 14px; display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 10px">
      <div class="field">
        <label>{{ $t('settings.priceKey') }}</label>
        <input v-model="draft.modelKey" class="input" placeholder="gpt-5.6-sol" />
      </div>
      <div class="field">
        <label>{{ $t('settings.priceInput') }}</label>
        <input v-model.number="draft.inputPerM" class="input" type="number" step="0.01" min="0" />
      </div>
      <div class="field">
        <label>{{ $t('settings.priceOutput') }}</label>
        <input v-model.number="draft.outputPerM" class="input" type="number" step="0.01" min="0" />
      </div>
      <div class="field">
        <label>{{ $t('settings.priceCacheRead') }}</label>
        <input v-model.number="draft.cacheReadPerM" class="input" type="number" step="0.01" min="0" />
      </div>
      <div class="field">
        <label>{{ $t('settings.priceCacheWrite') }}</label>
        <input v-model.number="draft.cacheWritePerM" class="input" type="number" step="0.01" min="0" />
      </div>
    </div>
    <div class="row" style="margin-top: 10px">
      <button class="btn btn-primary" @click="saveDraft">
        {{ draft.updatedAt ? $t('common.save') : $t('common.add') }}
      </button>
      <input v-model="search" class="input" :placeholder="$t('common.search')" />
      <span class="faint">{{ prices.length }} {{ $t('metrics.models') }}</span>
    </div>

    <div class="table-wrap" style="margin-top: 12px; max-height: 45vh">
      <table class="data">
        <thead>
          <tr>
            <th>{{ $t('settings.priceKey') }}</th>
            <th class="right">{{ $t('settings.priceInput') }}</th>
            <th class="right">{{ $t('settings.priceOutput') }}</th>
            <th class="right">{{ $t('settings.priceCacheRead') }}</th>
            <th class="right">{{ $t('settings.priceCacheWrite') }}</th>
            <th>{{ $t('settings.priceSource') }}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in filtered" :key="row.modelKey">
            <td class="mono">{{ row.modelKey }}</td>
            <td class="right num">{{ row.inputPerM }}</td>
            <td class="right num">{{ row.outputPerM }}</td>
            <td class="right num">{{ row.cacheReadPerM }}</td>
            <td class="right num">{{ row.cacheWritePerM }}</td>
            <td>
              <span class="tag" :class="row.source === 'manual' ? '' : 'tag-ok'">{{ row.source }}</span>
            </td>
            <td class="right">
              <button class="btn" @click="edit(row)">{{ $t('common.edit') }}</button>
              <button class="btn btn-danger" @click="remove(row)">{{ $t('common.delete') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </Card>
</template>