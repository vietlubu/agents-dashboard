<script setup lang="ts">
import { computed, ref } from "vue";

/**
 * A checkbox-list filter. It renders as a compact popover so a view with six filters still
 * fits on one line, and keeps its own open state because several instances are on screen.
 */
const props = withDefaults(
  defineProps<{
    modelValue: string[];
    options: string[];
    label: string;
    /** Caps the rendered list; filters with hundreds of values stay usable. */
    limit?: number;
  }>(),
  { limit: 400 }
);

const emit = defineEmits<{ "update:modelValue": [string[]] }>();

const open = ref(false);
const search = ref("");

const filtered = computed(() => {
  const term = search.value.trim().toLowerCase();
  const list = term ? props.options.filter((o) => o.toLowerCase().includes(term)) : props.options;
  return list.slice(0, props.limit);
});

function toggle(value: string) {
  const next = props.modelValue.includes(value)
    ? props.modelValue.filter((v) => v !== value)
    : [...props.modelValue, value];
  emit("update:modelValue", next);
}
</script>

<template>
  <div class="multi">
    <button class="pill" :class="{ busy: props.modelValue.length > 0 }" @click="open = !open">
      {{ props.label }}
      <span v-if="props.modelValue.length" class="faint">{{ props.modelValue.length }}</span>
    </button>

    <div v-if="open" class="multi-pop">
      <div class="row">
        <input v-model="search" class="input" :placeholder="$t('common.search')" style="flex: 1" />
        <!-- Clearing keeps the popover open: closing it here would swallow the next click. -->
        <button class="btn" @click="emit('update:modelValue', [])">
          {{ $t('common.reset') }}
        </button>
      </div>
      <div class="multi-list">
        <label v-for="option in filtered" :key="option" class="multi-item">
          <input
            type="checkbox"
            :checked="props.modelValue.includes(option)"
            @change="toggle(option)"
          />
          <span class="mono">{{ option || $t('common.none') }}</span>
        </label>
        <div v-if="filtered.length === 0" class="empty">{{ $t('common.noData') }}</div>
      </div>
      <div class="row">
        <div class="spacer" />
        <button class="btn" @click="open = false">{{ $t('common.close') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.multi {
  position: relative;
}

.multi-pop {
  position: absolute;
  top: calc(100% + 6px);
  left: 0;
  z-index: 20;
  width: 320px;
  background: var(--bg-elevated);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  box-shadow: 0 10px 30px rgba(0, 0, 0, 0.35);
}

.multi-list {
  max-height: 280px;
  overflow: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.multi-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  padding: 3px 4px;
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.multi-item:hover {
  background: var(--bg-sunken);
}
</style>