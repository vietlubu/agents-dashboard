<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Chart, type ChartConfiguration } from "chart.js";
import { resolveColor } from "@/lib/chart";
import { useSettingsStore } from "@/stores/settings";

/**
 * A copy of a chart configuration with every CSS variable turned into a concrete colour.
 *
 * A canvas cannot parse `var(--series-1)`: the value is dropped and the shape is drawn
 * black. Resolving here, at draw time rather than while the caller builds its options, is
 * also what lets a theme switch recolour a chart whose data has not changed.
 */
function withResolvedColors<T>(value: T): T {
  if (typeof value === "string") return resolveColor(value) as T;
  if (Array.isArray(value)) return value.map(withResolvedColors) as T;
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, nested] of Object.entries(value)) out[key] = withResolvedColors(nested);
    return out as T;
  }
  return value;
}

/**
 * The single chart host. It drives Chart.js directly rather than wrapping one component per
 * chart type: the surrounding markup, theme re-render and resize behaviour are identical for
 * every chart, so only the configuration differs.
 */
const props = withDefaults(
  defineProps<{
    /** A Chart.js configuration without the canvas; the component owns the element. */
    config: ChartConfiguration;
    height?: number;
  }>(),
  { height: 220 }
);

const settings = useSettingsStore();
const canvas = ref<HTMLCanvasElement | null>(null);
let chart: Chart | null = null;

function render() {
  if (!canvas.value) return;
  chart?.destroy();
  chart = new Chart(canvas.value, withResolvedColors(props.config));
}

onMounted(render);
onBeforeUnmount(() => {
  chart?.destroy();
  chart = null;
});

// A configuration change means new data: rebuild rather than mutate, which keeps the
// callers free of Chart.js's update semantics.
watch(() => props.config, render, { deep: true });

// Charts draw resolved colours into a canvas, so switching theme has to redraw.
watch(
  () => settings.theme,
  () => requestAnimationFrame(render)
);
</script>

<template>
  <div class="chart-wrap" :style="{ height: props.height + 'px' }">
    <canvas ref="canvas" />
  </div>
</template>