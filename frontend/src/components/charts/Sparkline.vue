<script setup lang="ts">
import { computed } from "vue";

/**
 * An axis-free sparkline: the shape of a series, not its values.
 *
 * Plain SVG rather than Chart.js — it is 22 pixels tall with no axes, ticks or tooltip, so
 * a chart engine would be pure overhead for a component that renders inside a table cell.
 * This follows HeatmapGrid, which also draws by hand.
 *
 * The colour is a CSS var string (`var(--series-1)`), which the SVG attribute resolves
 * natively, so it recolours with the theme without a redraw like ChartBox must do.
 */
const props = withDefaults(
  defineProps<{
    /** One value per bucket, in axis order. */
    values: number[];
    color?: string;
    width?: number;
    height?: number;
  }>(),
  { color: "var(--series-1)", width: 96, height: 22 }
);

const PAD = 1;

/** A single point has no line to draw, and a flat series would sit on the baseline. */
const drawn = computed(() => props.values.length > 1 && Math.max(...props.values) > 0);

const max = computed(() => Math.max(1, ...props.values));

const line = computed(() =>
  props.values
    .map((value, i) => {
      const x = (i * props.width) / (props.values.length - 1);
      const y = props.height - PAD - (value / max.value) * (props.height - PAD * 2);
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" "),
);

const area = computed(() =>
  drawn.value ? `0,${props.height} ${line.value} ${props.width},${props.height}` : "",
);
</script>

<template>
  <svg
    class="sparkline"
    :width="props.width"
    :height="props.height"
    :viewBox="`0 0 ${props.width} ${props.height}`"
    aria-hidden="true"
  >
    <polygon v-if="drawn" :points="area" :fill="props.color" />
    <polyline
      v-if="drawn"
      :points="line"
      fill="none"
      :stroke="props.color"
      stroke-width="1.5"
      stroke-linejoin="round"
      stroke-linecap="round"
    />
  </svg>
</template>

<style scoped>
.sparkline {
  display: block;
}
</style>