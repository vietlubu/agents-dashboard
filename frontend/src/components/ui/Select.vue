<script setup lang="ts">
const props = defineProps<{
  modelValue: string;
  options: { value: string; label: string }[];
  placeholder?: string;
  disabled?: boolean;
}>();

const emit = defineEmits<{ "update:modelValue": [string] }>();
</script>

<template>
  <select
    class="select"
    :value="props.modelValue"
    :disabled="props.disabled"
    @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
  >
    <option v-if="props.placeholder" value="">{{ props.placeholder }}</option>
    <option v-for="option in props.options" :key="option.value" :value="option.value">
      {{ option.label }}
    </option>
  </select>
</template>