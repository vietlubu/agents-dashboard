// Range presets. Everything is computed in the configured timezone, which is the same one
// the scan uses to assign events to days, so a "today" range and a "today" bucket in the
// database always describe the same window.
//
// Presets that end mid-day (last 5h, last 24h) are sent as exact instants; the store then
// answers them from the event rows rather than from day-keyed rollups, so the totals stay
// exact instead of including the whole of the first and last day.

export type PresetKey =
  | "today"
  | "yesterday"
  | "5h"
  | "24h"
  | "7d"
  | "30d"
  | "90d"
  | "custom";

export interface Range {
  fromMs: number;
  toMs: number;
}

export interface DayParts {
  year: number;
  month: number;
  day: number;
}

/**
 * A zone name Intl will accept.
 *
 * The stored zone comes from the backend and could be stale or unusable (an old value, or a
 * name this engine does not know). Rather than throwing while computing a range — which would
 * leave every view empty — fall back to the browser's own zone.
 */
export function safeZone(timeZone: string): string {
  try {
    new Intl.DateTimeFormat("en-CA", { timeZone });
    return timeZone;
  } catch {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  }
}

function tzParts(ms: number, timeZone: string): Record<string, string> {
  const fmt = new Intl.DateTimeFormat("en-CA", {
    timeZone: safeZone(timeZone),
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
  const out: Record<string, string> = {};
  for (const part of fmt.formatToParts(ms)) {
    if (part.type !== "literal") out[part.type] = part.value;
  }
  return out;
}

export function todayIn(tz: string, now = Date.now()): DayParts {
  const p = tzParts(now, tz);
  return { year: Number(p.year), month: Number(p.month), day: Number(p.day) };
}

/**
 * The UTC instant at which the given calendar day starts in `tz`.
 *
 * The offset is measured at the guessed instant and applied once; for the day boundaries
 * this dashboard uses (midnight) a single correction is exact, because the guess and the
 * result are within one offset of each other.
 */
export function localMidnight(tz: string, day: DayParts): number {
  const guess = Date.UTC(day.year, day.month - 1, day.day, 0, 0, 0);
  const p = tzParts(guess, tz);
  const asUtc = Date.UTC(
    Number(p.year),
    Number(p.month) - 1,
    Number(p.day),
    Number(p.hour) % 24,
    Number(p.minute),
    Number(p.second)
  );
  const offset = asUtc - guess;
  return guess - offset;
}

/** Calendar day arithmetic that is safe across DST: anchored at noon UTC, then read back. */
export function addDays(day: DayParts, delta: number): DayParts {
  const d = new Date(Date.UTC(day.year, day.month - 1, day.day, 12, 0, 0));
  d.setUTCDate(d.getUTCDate() + delta);
  return { year: d.getUTCFullYear(), month: d.getUTCMonth() + 1, day: d.getUTCDate() };
}

/** Parses a yyyy-mm-dd input value; returns null for an incomplete or invalid value. */
export function parseDay(value: string): DayParts | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value.trim());
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  if (month < 1 || month > 12 || day < 1 || day > 31) return null;
  return { year, month, day };
}

export function dayKey(day: DayParts): string {
  const m = String(day.month).padStart(2, "0");
  const d = String(day.day).padStart(2, "0");
  return `${day.year}-${m}-${d}`;
}

/** The inclusive list of day keys a range covers, for chart axes and heatmaps. */
export function daysInRange(range: Range, tz: string): string[] {
  const start = todayIn(tz, Math.max(range.fromMs, 0));
  let cursor: DayParts = start;
  const lastDay = todayIn(tz, Math.max(range.toMs - 1, 0));
  const out: string[] = [];
  // Bounded so a bad range cannot spin: 5 years of days is already more than the UI shows.
  for (let i = 0; i < 2000; i++) {
    out.push(dayKey(cursor));
    if (cursor.year === lastDay.year && cursor.month === lastDay.month && cursor.day === lastDay.day) {
      break;
    }
    cursor = addDays(cursor, 1);
  }
  return out;
}

/**
 * The hour buckets a range covers, as epoch-millisecond keys.
 *
 * The keys are the same instants the store groups by (the start of each hour), so an hourly
 * chart keeps its shape when a bucket has no usage instead of drawing across the gap.
 */
export function hourKeysInRange(range: Range, limit = 400): string[] {
  const hour = 60 * 60 * 1000;
  const first = Math.floor(range.fromMs / hour) * hour;
  const last = Math.floor((range.toMs - 1) / hour) * hour;
  const out: string[] = [];
  for (let at = first; at <= last && out.length < limit; at += hour) out.push(String(at));
  return out;
}

export function resolvePreset(key: PresetKey, tz: string, now = Date.now()): Range {
  const today = todayIn(tz, now);
  const tomorrow = addDays(today, 1);
  switch (key) {
    case "today":
      return { fromMs: localMidnight(tz, today), toMs: localMidnight(tz, tomorrow) };
    case "yesterday": {
      const y = addDays(today, -1);
      return { fromMs: localMidnight(tz, y), toMs: localMidnight(tz, today) };
    }
    case "5h":
      return { fromMs: now - 5 * 3_600_000, toMs: now };
    case "24h":
      return { fromMs: now - 24 * 3_600_000, toMs: now };
    case "7d":
      return { fromMs: localMidnight(tz, addDays(today, -6)), toMs: localMidnight(tz, tomorrow) };
    case "30d":
      return { fromMs: localMidnight(tz, addDays(today, -29)), toMs: localMidnight(tz, tomorrow) };
    case "90d":
      return { fromMs: localMidnight(tz, addDays(today, -89)), toMs: localMidnight(tz, tomorrow) };
    default:
      return { fromMs: localMidnight(tz, addDays(today, -29)), toMs: localMidnight(tz, tomorrow) };
  }
}

export const PRESETS: { key: PresetKey; label: string }[] = [
  { key: "today", label: "Today" },
  { key: "yesterday", label: "Yesterday" },
  { key: "5h", label: "Last 5h" },
  { key: "24h", label: "Last 24h" },
  { key: "7d", label: "Last 7d" },
  { key: "30d", label: "Last 30d" },
  { key: "90d", label: "Last 90d" },
];