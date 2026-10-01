<script setup lang="ts">
import { computed } from "vue";
import Modal from "@/components/ui/Modal.vue";

/**
 * Column chooser for the events table. The choice is persisted by the parent so the table
 * opens with the same columns next time.
 */
const props = defineProps<{
  columns: { key: string; label: string }[];
  selected: string[];
}>();

const emit = defineEmits<{
  "update:selected": [string[]];
  close: [];
}>();

const selectedSet = computed(() => new Set(props.selected));

function toggle(key: string) {
  const next = selectedSet.value.has(key)
    ? props.selected.filter((k) => k !== key)
    : [...props.selected, key];
  // At least one column must remain, or the table renders nothing.
  if (next.length === 0) return;
  emit("update:selected", next);
}

function reset() {
  emit("update:selected", props.columns.map((c) => c.key));
}
</script>

<template>
  <Modal :title="$t('events.columns')" @close="emit('close')">
    <div class="grid grid-3">
      <label v-for="column in props.columns" :key="column.key" class="row" style="cursor: pointer">
        <input
          type="checkbox"
          :checked="selectedSet.has(column.key)"
          @change="toggle(column.key)"
        />
        <span>{{ $t(column.label) }}</span>
      </label>
    </div>
    <div class="row" style="margin-top: 14px">
      <button class="btn" @click="reset">{{ $t('common.reset') }}</button>
      <div class="spacer" />
      <button class="btn btn-primary" @click="emit('close')">{{ $t('common.close') }}</button>
    </div>
  </Modal>
</template>