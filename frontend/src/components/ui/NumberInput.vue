<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";

const props = withDefaults(
  defineProps<{
    modelValue?: number | string | null;
    min?: number;
    max?: number;
    step?: number;
    placeholder?: string;
    disabled?: boolean;
    id?: string;
    stepper?: boolean;
  }>(),
  {
    modelValue: undefined,
    min: undefined,
    max: undefined,
    step: 1,
    placeholder: undefined,
    disabled: false,
    id: undefined,
    stepper: true,
  }
);

const emit = defineEmits<{
  "update:modelValue": [number];
  change: [number];
}>();

const inputRef = ref<HTMLInputElement | null>(null);

const decimals = computed(() => {
  const s = String(props.step ?? 1);
  return (s.split(".")[1] || "").length;
});

function clamp(val: number): number {
  let v = Number(val.toFixed(decimals.value));
  if (props.min !== undefined && v < props.min) v = props.min;
  if (props.max !== undefined && v > props.max) v = props.max;
  return v;
}

function onInput(event: Event) {
  const target = event.target as HTMLInputElement;
  const raw = target.value;
  if (raw === "") return;
  const num = Number(raw);
  if (!isNaN(num)) {
    emit("update:modelValue", num);
  }
}

function onChange(event: Event) {
  const target = event.target as HTMLInputElement;
  const raw = target.value;
  if (raw === "" || isNaN(Number(raw))) {
    const fallback = props.min ?? 0;
    target.value = String(fallback);
    emit("update:modelValue", fallback);
    emit("change", fallback);
    return;
  }
  const clamped = clamp(Number(raw));
  target.value = String(clamped);
  emit("update:modelValue", clamped);
  emit("change", clamped);
}

function stepBy(delta: number) {
  if (props.disabled) return;
  const current = Number(props.modelValue ?? props.min ?? 0);
  const next = clamp(current + delta * (props.step ?? 1));
  if (inputRef.value) {
    inputRef.value.value = String(next);
  }
  emit("update:modelValue", next);
  emit("change", next);
}

let timer: ReturnType<typeof setTimeout> | null = null;
let interval: ReturnType<typeof setInterval> | null = null;

function startStepping(delta: number) {
  if (props.disabled) return;
  stepBy(delta);
  clearStepping();
  timer = setTimeout(() => {
    interval = setInterval(() => {
      stepBy(delta);
    }, 60);
  }, 350);
}

function clearStepping() {
  if (timer) {
    clearTimeout(timer);
    timer = null;
  }
  if (interval) {
    clearInterval(interval);
    interval = null;
  }
}

onUnmounted(clearStepping);
</script>

<template>
  <div class="number-input-wrap" :class="{ disabled: props.disabled }">
    <input
      :id="props.id"
      ref="inputRef"
      type="number"
      class="input number-input"
      :class="{ 'has-stepper': props.stepper }"
      :value="props.modelValue"
      :min="props.min"
      :max="props.max"
      :step="props.step"
      :placeholder="props.placeholder"
      :disabled="props.disabled"
      @input="onInput"
      @change="onChange"
    />
    <div v-if="props.stepper" class="steppers">
      <button
        type="button"
        tabindex="-1"
        class="step-btn step-up"
        :disabled="props.disabled || (props.max !== undefined && Number(props.modelValue) >= props.max)"
        aria-label="Increment"
        @mousedown.prevent="startStepping(1)"
        @mouseup="clearStepping"
        @mouseleave="clearStepping"
      >
        <svg width="8" height="5" viewBox="0 0 8 5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
          <path d="M1 4L4 1L7 4" />
        </svg>
      </button>
      <button
        type="button"
        tabindex="-1"
        class="step-btn step-down"
        :disabled="props.disabled || (props.min !== undefined && Number(props.modelValue) <= props.min)"
        aria-label="Decrement"
        @mousedown.prevent="startStepping(-1)"
        @mouseup="clearStepping"
        @mouseleave="clearStepping"
      >
        <svg width="8" height="5" viewBox="0 0 8 5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
          <path d="M1 1L4 4L7 1" />
        </svg>
      </button>
    </div>
  </div>
</template>

<style scoped>
.number-input-wrap {
  position: relative;
  display: inline-flex;
  align-items: stretch;
  width: 100%;
  box-sizing: border-box;
}

.number-input {
  width: 100%;
  flex: 1;
}

.number-input.has-stepper {
  padding-right: 24px;
}

.steppers {
  position: absolute;
  top: 1px;
  right: 1px;
  bottom: 1px;
  width: 22px;
  display: flex;
  flex-direction: column;
  pointer-events: none;
  border-left: 1px solid var(--border);
}

.step-btn {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  border: none;
  padding: 0;
  margin: 0;
  color: var(--text-muted);
  cursor: pointer;
  pointer-events: auto;
  transition: color 0.12s ease, background-color 0.12s ease;
}

.step-btn.step-up {
  border-top-right-radius: calc(var(--radius-sm) - 1px);
}

.step-btn.step-down {
  border-bottom-right-radius: calc(var(--radius-sm) - 1px);
  border-top: 1px solid var(--border);
}

.step-btn:hover:not(:disabled) {
  color: var(--text);
  background: var(--bg-elevated);
}

.step-btn:active:not(:disabled) {
  background: var(--border);
}

.step-btn:disabled {
  opacity: 0.25;
  cursor: not-allowed;
}

.disabled {
  opacity: 0.5;
}
</style>
