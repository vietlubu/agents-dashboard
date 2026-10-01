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
const currentValue = ref(Number(props.modelValue ?? props.min ?? 0));
const atMin = computed(() => props.min !== undefined && currentValue.value <= props.min);
const atMax = computed(() => props.max !== undefined && currentValue.value >= props.max);

watch(
  () => props.modelValue,
  (value) => {
    currentValue.value = Number(value ?? props.min ?? 0);
  }
);

function clamp(value: number): number {
  if (props.min !== undefined && value < props.min) return props.min;
  if (props.max !== undefined && value > props.max) return props.max;
  return value;
}

function onInput(event: Event) {
  const target = event.target as HTMLInputElement;
  const raw = target.value;
  if (raw === "") {
    currentValue.value = Number.NaN;
    return;
  }
  const value = Number(raw);
  if (Number.isFinite(value)) {
    currentValue.value = value;
    emit("update:modelValue", value);
  }
}

function onChange(event: Event) {
  const target = event.target as HTMLInputElement;
  const raw = target.value;
  const value = raw === "" || !Number.isFinite(Number(raw))
    ? props.min ?? 0
    : clamp(Number(raw));
  target.value = String(value);
  currentValue.value = value;
  emit("update:modelValue", value);
  emit("change", value);
}

function stepBy(delta: number): boolean {
  if (props.disabled || !inputRef.value) return false;
  const input = inputRef.value;
  const before = input.value;
  try {
    if (delta > 0) input.stepUp(delta);
    else input.stepDown(-delta);
  } catch {
    return false;
  }
  const stepped = Number(input.value);
  if (input.value === before || !Number.isFinite(stepped)) return false;

  const value = clamp(stepped);
  input.value = String(value);
  currentValue.value = value;
  emit("update:modelValue", value);
  return true;
}

function atBound(delta: number): boolean {
  return delta > 0 ? atMax.value : atMin.value;
}

let timer: ReturnType<typeof setTimeout> | null = null;
let interval: ReturnType<typeof setInterval> | null = null;
let stepping = false;

function clearStepping(commit = true) {
  if (timer) {
    clearTimeout(timer);
    timer = null;
  }
  if (interval) {
    clearInterval(interval);
    interval = null;
  }
  window.removeEventListener("pointerup", onPointerEnd);
  window.removeEventListener("pointercancel", onPointerEnd);
  if (stepping && commit && inputRef.value) {
    emit("change", Number(inputRef.value.value));
  }
  stepping = false;
}

function onPointerEnd() {
  clearStepping();
}

function startStepping(delta: number) {
  clearStepping(false);
  if (props.disabled || !inputRef.value) return;
  inputRef.value.focus({ preventScroll: true });
  if (!stepBy(delta)) return;

  stepping = true;
  window.addEventListener("pointerup", onPointerEnd, { once: true });
  window.addEventListener("pointercancel", onPointerEnd, { once: true });
  if (atBound(delta)) {
    clearStepping();
    return;
  }
  timer = setTimeout(() => {
    interval = setInterval(() => {
      if (!stepBy(delta) || atBound(delta)) clearStepping();
    }, 60);
  }, 350);
}

function activateStep(event: MouseEvent, delta: number) {
  if (event.detail !== 0 || props.disabled) return;
  inputRef.value?.focus({ preventScroll: true });
  if (stepBy(delta) && inputRef.value) emit("change", Number(inputRef.value.value));
}

onUnmounted(() => clearStepping(false));
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
        class="step-btn step-up"
        :disabled="props.disabled || atMax"
        aria-label="Increment"
        @pointerdown.prevent="startStepping(1)"
        @click="activateStep($event, 1)"
      >
        <svg width="8" height="5" viewBox="0 0 8 5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
          <path d="M1 4L4 1L7 4" />
        </svg>
      </button>
      <button
        type="button"
        class="step-btn step-down"
        :disabled="props.disabled || atMin"
        aria-label="Decrement"
        @pointerdown.prevent="startStepping(-1)"
        @click="activateStep($event, -1)"
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
