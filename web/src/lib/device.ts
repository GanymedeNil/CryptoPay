/** Heuristics about the customer's device / browser. Client-side only. */

export function isMobileUserAgent(ua: string, maxTouchPoints = 0): boolean {
  if (/Android|iPhone|iPad|iPod|Mobile|HarmonyOS|Windows Phone/i.test(ua)) return true;
  // iPadOS 13+ reports a desktop Safari UA.
  return /Macintosh/.test(ua) && maxTouchPoints > 1;
}

export function isMobileDevice(): boolean {
  if (typeof navigator === "undefined") return false;
  return isMobileUserAgent(navigator.userAgent, navigator.maxTouchPoints ?? 0);
}
