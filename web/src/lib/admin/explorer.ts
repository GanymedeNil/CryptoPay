import type { Chain } from "@/lib/api/types";
import { explorerAddressUrl, explorerTxUrl } from "@/lib/explorer";

/**
 * Fills a `Chain.explorer_tx_url` / `explorer_addr_url` template. The contract
 * does not pin the placeholder syntax, so accept the common ones
 * (`{tx}` / `{hash}` / `{address}` / `{addr}` / `%s`) and fall back to
 * appending the value to a prefix URL.
 */
export function fillExplorerTemplate(template: string, value: string): string | null {
  if (!template || !value) return null;
  const v = encodeURIComponent(value);
  if (/\{(tx|hash|address|addr|txid)\}/.test(template)) {
    return template.replace(/\{(tx|hash|address|addr|txid)\}/g, v);
  }
  if (template.includes("%s")) return template.replace("%s", v);
  return template.endsWith("/") ? `${template}${v}` : `${template}/${v}`;
}

/** Explorer link for a tx: the chain's configured template, else the built-in table. */
export function txLink(chainId: string, txHash: string, chains?: readonly Chain[]): string | null {
  const chain = chains?.find((c) => c.id === chainId);
  return (chain && fillExplorerTemplate(chain.explorer_tx_url, txHash)) || explorerTxUrl(chainId, txHash);
}

/** Explorer link for an address. */
export function addressLink(chainId: string, address: string, chains?: readonly Chain[]): string | null {
  const chain = chains?.find((c) => c.id === chainId);
  return (chain && fillExplorerTemplate(chain.explorer_addr_url, address)) || explorerAddressUrl(chainId, address);
}

/** Only http(s) links are rendered as anchors. */
export function safeHttpUrl(url: string | null | undefined): string | null {
  if (!url) return null;
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u.toString() : null;
  } catch {
    return null;
  }
}
