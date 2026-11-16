/**
 * Countdown helpers. The client clock may be wrong, so we keep an offset
 * between the server clock (`server_time` of each checkout response) and the
 * local clock and compute remaining time on the corrected "server now".
 */

/** serverTime − clientNow in ms; 0 when `serverTime` is not parseable. */
export function computeClockOffset(serverTime: string, clientNowMs: number): number {
  const t = Date.parse(serverTime);
  return Number.isFinite(t) ? t - clientNowMs : 0;
}

/** Milliseconds until `deadline` (never negative). */
export function remainingMs(deadline: string, clientNowMs: number, offsetMs = 0): number {
  const end = Date.parse(deadline);
  if (!Number.isFinite(end)) return 0;
  return Math.max(0, end - (clientNowMs + offsetMs));
}

export interface CountdownParts {
  hours: number;
  minutes: number;
  seconds: number;
  totalSeconds: number;
}

export function countdownParts(ms: number): CountdownParts {
  const totalSeconds = Math.max(0, Math.ceil(ms / 1000));
  return {
    hours: Math.floor(totalSeconds / 3600),
    minutes: Math.floor((totalSeconds % 3600) / 60),
    seconds: totalSeconds % 60,
    totalSeconds,
  };
}

const pad = (n: number) => String(n).padStart(2, "0");

/** 75_000 → "01:15"; 3_726_000 → "1:02:06". */
export function formatCountdown(ms: number): string {
  const { hours, minutes, seconds } = countdownParts(ms);
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${pad(minutes)}:${pad(seconds)}`;
}

/** Fraction of the window [start, end] still left, clamped to 0..1. */
export function remainingFraction(start: string, end: string, nowMs: number, offsetMs = 0): number {
  const s = Date.parse(start);
  const e = Date.parse(end);
  if (!Number.isFinite(s) || !Number.isFinite(e) || e <= s) return 0;
  const f = (e - (nowMs + offsetMs)) / (e - s);
  return Math.min(1, Math.max(0, f));
}
