// Number, currency, duration and date formatting. Every date is rendered in the configured
// timezone, which is the same one the scan used to assign events to days.
import { safeZone } from "./range";

export function formatInt(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  return new Intl.NumberFormat().format(value);
}

/** Compact token counts: 17.3B, 682.4M, 4.9K. */
export function formatTokens(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  const abs = Math.abs(value);
  if (abs >= 1e12) return trim(value / 1e12) + "T";
  if (abs >= 1e9) return trim(value / 1e9) + "B";
  if (abs >= 1e6) return trim(value / 1e6) + "M";
  if (abs >= 1e3) return trim(value / 1e3) + "K";
  return String(value);
}

/**
 * Rounds to a readable number of digits and drops a trailing fraction.
 *
 * The zeros are only stripped after a decimal point: a plain "870" must stay 870, not become
 * 87 because the string happened to end in a zero.
 */
function trim(value: number): string {
  const abs = Math.abs(value);
  const digits = abs >= 100 ? 0 : abs >= 10 ? 1 : 2;
  const fixed = value.toFixed(digits);
  if (!fixed.includes(".")) return fixed;
  return fixed.replace(/0+$/, "").replace(/\.$/, "");
}

/** A cost with enough precision to stay meaningful for sub-cent amounts. */
export function formatUSD(value: number | null | undefined, opts: { compact?: boolean } = {}): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  const abs = Math.abs(value);
  if (opts.compact && abs >= 10000) return "$" + formatTokens(value);
  const digits = abs === 0 ? 2 : abs < 0.01 ? 4 : abs < 1 ? 3 : 2;
  return "$" + value.toFixed(digits);
}

export function formatMs(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  if (value < 1000) return Math.round(value) + " ms";
  const seconds = value / 1000;
  if (seconds < 60) return trim(seconds) + " s";
  const minutes = seconds / 60;
  if (minutes < 60) return trim(minutes) + " min";
  return trim(minutes / 60) + " h";
}

export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return "—";
  if (ms < 1000) return Math.round(ms) + "ms";
  if (ms < 60_000) return (ms / 1000).toFixed(ms < 10_000 ? 1 : 0) + "s";
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return `${minutes}m ${seconds}s`;
}

/** Date/time parts in a specific IANA timezone. */
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

/** YYYY-MM-DD for an instant, in the given timezone. */
export function formatDay(ms: number, timeZone: string): string {
  const p = tzParts(ms, timeZone);
  return `${p.year}-${p.month}-${p.day}`;
}

/** HH:MM for an instant, in the given timezone. */
export function formatTime(ms: number, timeZone: string): string {
  const p = tzParts(ms, timeZone);
  return `${p.hour === "24" ? "00" : p.hour}:${p.minute}`;
}

/** YYYY-MM-DD HH:MM for an instant, in the given timezone. */
export function formatDateTime(ms: number, timeZone: string): string {
  if (!ms) return "—";
  return `${formatDay(ms, timeZone)} ${formatTime(ms, timeZone)}`;
}

/**
 * A bucket label for a time axis.
 *
 * Day, week and month buckets already arrive as readable keys; an hour bucket is an epoch
 * millisecond at the start of the bucket, which is rendered in the configured timezone so
 * the axis reads in local time.
 */
export function formatBucket(key: string, granularity: string, timeZone: string): string {
  if (granularity !== "hour") return key;
  const ms = Number(key);
  return Number.isFinite(ms) ? formatTime(ms, timeZone) : key;
}

/** A short relative description ("3 min ago") for sync timestamps. */
export function formatRelative(ms: number | null | undefined, now = Date.now()): string {
  if (!ms) return "never";
  const diff = now - ms;
  if (diff < 0) return "in the future";
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.round(diff / 60_000)} min ago`;
  if (diff < 86_400_000) return `${Math.round(diff / 3_600_000)} h ago`;
  return `${Math.round(diff / 86_400_000)} d ago`;
}

export function formatPercent(part: number, whole: number, digits = 1): string {
  if (!whole) return "—";
  return ((part / whole) * 100).toFixed(digits) + "%";
}

export function formatNumber(value: number | null | undefined, digits: number): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  return value.toFixed(digits);
}

/** Bytes for the database size readout. */
export function formatBytes(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let v = value;
  let unit = 0;
  while (v >= 1024 && unit < units.length - 1) {
    v /= 1024;
    unit++;
  }
  return `${v.toFixed(v >= 100 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}