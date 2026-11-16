"use client";

import { useEffect, useState } from "react";

/**
 * Current time (ms), re-rendering every `intervalMs` while `active`.
 * Pass `active = false` to stop ticking (e.g. in terminal states).
 */
export function useNow(intervalMs = 1000, active = true): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs, active]);
  return now;
}
