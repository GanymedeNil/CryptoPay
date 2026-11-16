"use client";

import { useSyncExternalStore } from "react";
import { isMobileDevice } from "@/lib/device";

const noopSubscribe = () => () => {};

/** True on phones / tablets (user agent based; false during SSR). */
export function useIsMobile(): boolean {
  return useSyncExternalStore(noopSubscribe, isMobileDevice, () => false);
}
