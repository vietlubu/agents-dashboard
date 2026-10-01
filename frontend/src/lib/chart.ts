// Chart.js setup. Components are registered once here; views import the wrapper components,
// never Chart.js directly.
import { useSettingsStore } from "@/stores/settings";
import {
  ArcElement,
  BarController,
  BarElement,
  CategoryScale,
  Chart,
  DoughnutController,
  Filler,
  Legend,
  LinearScale,
  LineController,
  LineElement,
  PointElement,
  ScatterController,
  TimeScale,
  Tooltip,
} from "chart.js";

Chart.register(
  ArcElement,
  BarController,
  BarElement,
  CategoryScale,
  DoughnutController,
  Filler,
  Legend,
  LinearScale,
  LineController,
  LineElement,
  PointElement,
  ScatterController,
  TimeScale,
  Tooltip
);

/**
 * The palette, resolved to concrete colours.
 *
 * Callers draw these into a canvas, which cannot parse a CSS variable: an unresolved
 * `var(--series-1)` is silently ignored and the shape comes out black. Resolution happens
 * here so no caller can forget it; ChartBox redraws on a theme change, so the colours follow
 * the theme.
 */
export function seriesColor(index: number): string {
  const n = (index % 8) + 1;
  return resolveColor(`var(--series-${n})`);
}

/**
 * Read a theme token.
 *
 * Every colour helper below goes through this, which makes any computed that builds a chart
 * configuration depend on the theme: switching theme recomputes the colours it baked in, so
 * an already-drawn chart is rebuilt with the new palette instead of keeping the old one.
 */
function themeToken(name: string, fallback: string): string {
  // Reading the choice marks the dependency; the value itself is applied to <html> by
  // applyTheme, so the resolved colour below already reflects it.
  void useSettingsStore().theme;
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

/** Resolve a CSS variable to a concrete colour, which canvas drawing requires. */
export function resolveColor(value: string): string {
  if (!value.startsWith("var(")) return value;
  return themeToken(value.slice(4, -1), "#5b8cff");
}

export function chartFont(): string {
  return themeToken("--font", "sans-serif");
}

export function mutedColor(): string {
  return themeToken("--text-muted", "#98a0b3");
}

export function borderColor(): string {
  return themeToken("--border", "#262a35");
}

/** Shared axis/grid defaults so every chart looks like part of the same application. */
export function baseChartOptions(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: "index", intersect: false },
    plugins: {
      legend: {
        display: false,
      },
      tooltip: {
        backgroundColor: "rgba(20,22,28,0.95)",
        borderColor: borderColor(),
        borderWidth: 1,
        titleColor: "#fff",
        bodyColor: "#e6e8ee",
        padding: 8,
      },
    },
    scales: {
      x: {
        grid: { color: borderColor(), drawTicks: false },
        ticks: { color: mutedColor(), maxRotation: 0, autoSkipPadding: 16 },
        border: { display: false },
      },
      y: {
        grid: { color: borderColor(), drawTicks: false },
        ticks: { color: mutedColor() },
        border: { display: false },
        beginAtZero: true,
      },
    },
    ...overrides,
  };
}

export { Chart };