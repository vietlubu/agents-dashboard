<script setup lang="ts">
const props = withDefaults(defineProps<{ title?: string; wide?: boolean }>(), {
  title: "",
  wide: false,
});

const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal" :style="props.wide ? { maxWidth: '1200px' } : undefined">
      <div class="card-head">
        <div class="card-title">{{ props.title }}</div>
        <div class="spacer" />
        <button class="btn" @click="emit('close')">{{ $t('common.close') }}</button>
      </div>
      <slot />
    </div>
  </div>
</template>