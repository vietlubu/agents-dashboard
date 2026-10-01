<script setup lang="ts">
import { computed } from "vue";

const props = defineProps<{
  offset: number;
  limit: number;
  total: number;
}>();

const emit = defineEmits<{ "update:offset": [number] }>();

const pages = computed(() => Math.max(1, Math.ceil(props.total / Math.max(1, props.limit))));
const page = computed(() => Math.floor(props.offset / Math.max(1, props.limit)) + 1);

function go(delta: number) {
  const next = page.value + delta;
  if (next < 1 || next > pages.value) return;
  emit("update:offset", (next - 1) * props.limit);
}
</script>

<template>
  <div class="row" style="justify-content: flex-end">
    <span class="faint">
      {{ page }} / {{ pages }} · {{ total }} {{ $t('common.rows') }}
    </span>
    <button class="btn" :disabled="page <= 1" @click="go(-1)">{{ $t('common.prev') }}</button>
    <button class="btn" :disabled="page >= pages" @click="go(1)">{{ $t('common.next') }}</button>
  </div>
</template>