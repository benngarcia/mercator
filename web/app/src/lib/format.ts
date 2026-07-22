// Pure formatting helpers for the operator console. No React, no I/O.

import type { RunPhase } from "./api/types";

const IEC_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"] as const;

// bytes formats a byte count using IEC binary units (1024-based).
export function bytes(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) {
    return "—";
  }
  if (value < 1024) {
    return `${value} B`;
  }
  let size = value;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < IEC_UNITS.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  const unit = IEC_UNITS[unitIndex] ?? "B";
  const precision = size >= 100 || Number.isInteger(size) ? 0 : size >= 10 ? 1 : 2;
  return `${size.toFixed(precision)} ${unit}`;
}

// usd formats a US-dollar amount. Sub-cent values keep more precision so small
// per-second rates (e.g. cost estimates / score_usd) stay legible.
export function usd(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) {
    return "—";
  }
  const abs = Math.abs(value);
  const fractionDigits = abs !== 0 && abs < 0.01 ? 6 : abs < 1 ? 4 : 2;
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: fractionDigits,
  }).format(value);
}

// duration formats a span given in seconds into a compact human string
// (e.g. "1h 2m", "45s", "320ms"). Fractional sub-second values render as ms.
export function duration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) {
    return "—";
  }
  if (seconds === 0) {
    return "0s";
  }
  if (seconds < 1) {
    return `${Math.round(seconds * 1000)}ms`;
  }
  const totalSeconds = Math.floor(seconds);
  const days = Math.floor(totalSeconds / 86400);
  const hours = Math.floor((totalSeconds % 86400) / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const secs = totalSeconds % 60;

  const parts: string[] = [];
  if (days > 0) parts.push(`${days}d`);
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (secs > 0 && days === 0) parts.push(`${secs}s`);
  // Always show at least the two most-significant non-zero units.
  return parts.slice(0, 2).join(" ") || `${totalSeconds}s`;
}

const RELATIVE_DIVISIONS: Array<{ amount: number; unit: Intl.RelativeTimeFormatUnit }> = [
  { amount: 60, unit: "second" },
  { amount: 60, unit: "minute" },
  { amount: 24, unit: "hour" },
  { amount: 7, unit: "day" },
  { amount: 4.34524, unit: "week" },
  { amount: 12, unit: "month" },
  { amount: Number.POSITIVE_INFINITY, unit: "year" },
];

const relativeFormatter = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

// relativeTime renders an ISO timestamp relative to now ("3m ago", "in 2h").
export function relativeTime(iso: string | null | undefined): string {
  if (!iso) {
    return "—";
  }
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) {
    return "—";
  }
  let delta = (then - Date.now()) / 1000;
  for (const division of RELATIVE_DIVISIONS) {
    if (Math.abs(delta) < division.amount) {
      return relativeFormatter.format(Math.round(delta), division.unit);
    }
    delta /= division.amount;
  }
  return relativeFormatter.format(Math.round(delta), "year");
}

// shortDigest truncates a content digest for dense table display, preserving
// any algorithm prefix (e.g. "sha256:ab12cd34…").
export function shortDigest(
  digest: string | null | undefined,
  head: number = 12,
): string {
  if (!digest) {
    return "—";
  }
  const colon = digest.indexOf(":");
  if (colon !== -1) {
    const algo = digest.slice(0, colon);
    const hash = digest.slice(colon + 1);
    if (hash.length <= head) {
      return digest;
    }
    return `${algo}:${hash.slice(0, head)}…`;
  }
  if (digest.length <= head) {
    return digest;
  }
  return `${digest.slice(0, head)}…`;
}

// humanizeEventType turns a CloudEvent type like
// "compute.run.booking_decided.v1" into "Booking decided". The
// "compute.run." prefix and trailing version suffix are stripped; the
// remaining segment's underscores become spaces with sentence casing.
export function humanizeEventType(type: string | null | undefined): string {
  if (!type) {
    return "—";
  }
  // Special cases for friendlier labels
  if (type === "compute.run.reported.v1") {
    return "Workload report";
  }
  let working = type;
  // Drop a trailing version segment (".v1", ".v2", …).
  working = working.replace(/\.v\d+$/i, "");
  const segments = working.split(".");
  const last = segments[segments.length - 1] ?? working;
  const words = last.replace(/_/g, " ").trim();
  if (!words) {
    return type;
  }
  return words.charAt(0).toUpperCase() + words.slice(1);
}

const PHASE_LABELS: Record<string, string> = {
  requested: "Requested",
  launching: "Launching",
  running: "Running",
  cleaning_up: "Cleaning up",
  closed: "Closed",
};

// phaseLabel maps a run phase to a display label, falling back to a
// titecased version of any unrecognized phase string.
export function phaseLabel(phase: RunPhase | string | null | undefined): string {
  if (!phase) {
    return "—";
  }
  const known = PHASE_LABELS[phase];
  if (known) {
    return known;
  }
  return phase
    .split("_")
    .map((part) => (part ? part.charAt(0).toUpperCase() + part.slice(1) : part))
    .join(" ");
}
