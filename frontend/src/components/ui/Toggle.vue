<script setup lang="ts">
import { computed } from "vue";

const props = defineProps<{
  modelValue: boolean;
  label: string;
  hint?: string;
}>();

const emit = defineEmits<{ "update:modelValue": [boolean] }>();

const id = computed(() => "toggle-" + props.label.replace(/\W+/g, "-").toLowerCase());
</script>

<template>
  <label class="toggle" :for="id">
    <input
      :id="id"
      type="checkbox"
      :checked="props.modelValue"
      @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
    />
    <span>
      {{ props.label }}
      <span v-if="props.hint" class="faint"> — {{ props.hint }}</span>
    </span>
  </label>
</template>

<style scoped>
.toggle {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 13px;
  cursor: pointer;
}

.toggle input {
  margin-top: 2px;
}
</style>