<script setup lang="ts">
import { useSlots } from "vue";

const props = withDefaults(
  defineProps<{
    title?: string;
    subtitle?: string;
    padded?: boolean;
  }>(),
  { title: "", subtitle: "", padded: true }
);

const slots = useSlots();
</script>

<template>
  <section class="card" :style="props.padded ? undefined : { padding: 0 }">
    <div v-if="props.title || slots.actions" class="card-head">
      <div>
        <div class="card-title">{{ props.title }}</div>
        <div v-if="props.subtitle" class="card-sub">{{ props.subtitle }}</div>
      </div>
      <div class="spacer" />
      <slot name="actions" />
    </div>
    <slot />
  </section>
</template>