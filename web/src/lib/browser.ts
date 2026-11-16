/** Thin wrappers around browser navigation (mockable in tests). */
export function redirectTo(url: string): void {
  window.location.assign(url);
}
