/**
 * EIP-6963 multi injected provider discovery, dependency free so it can run
 * in the main bundle and list wallets (with their icons) before any wallet
 * library is loaded. https://eips.ethereum.org/EIPS/eip-6963
 */

export interface Eip1193Provider {
  request(args: { method: string; params?: unknown }): Promise<unknown>;
  on?(event: string, listener: (...args: unknown[]) => void): void;
  removeListener?(event: string, listener: (...args: unknown[]) => void): void;
}

export interface Eip6963ProviderInfo {
  uuid: string;
  name: string;
  /** data: URI (RFC-2397). */
  icon: string;
  /** Reverse DNS id, e.g. "io.metamask". */
  rdns: string;
}

export interface Eip6963ProviderDetail {
  info: Eip6963ProviderInfo;
  provider: Eip1193Provider;
}

type Listener = () => void;

let started = false;
let providers: Eip6963ProviderDetail[] = [];
const listeners = new Set<Listener>();

function isDetail(v: unknown): v is Eip6963ProviderDetail {
  if (typeof v !== "object" || v === null) return false;
  const d = v as { info?: Partial<Eip6963ProviderInfo>; provider?: { request?: unknown } };
  return (
    typeof d.info?.rdns === "string" &&
    typeof d.info?.name === "string" &&
    typeof d.provider?.request === "function"
  );
}

function onAnnounce(event: Event) {
  const detail = (event as CustomEvent<unknown>).detail;
  if (!isDetail(detail)) return;
  // One entry per wallet (rdns); some wallets announce more than once.
  if (providers.some((p) => p.info.rdns === detail.info.rdns)) return;
  providers = [...providers, detail];
  listeners.forEach((l) => l());
}

/** Starts listening (idempotent) and asks wallets to announce themselves. */
export function startEip6963Discovery(): void {
  if (typeof window === "undefined") return;
  if (!started) {
    started = true;
    window.addEventListener("eip6963:announceProvider", onAnnounce);
  }
  window.dispatchEvent(new Event("eip6963:requestProvider"));
}

export function getEip6963Providers(): Eip6963ProviderDetail[] {
  return providers;
}

export function findEip6963Provider(rdns: string): Eip6963ProviderDetail | undefined {
  return providers.find((p) => p.info.rdns === rdns);
}

export function subscribeEip6963(listener: Listener): () => void {
  listeners.add(listener);
  startEip6963Discovery();
  return () => listeners.delete(listener);
}

/** Test helper: forget every announced provider. */
export function resetEip6963ForTests(): void {
  providers = [];
  if (started && typeof window !== "undefined") {
    window.removeEventListener("eip6963:announceProvider", onAnnounce);
  }
  started = false;
  listeners.clear();
}
