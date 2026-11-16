/** Display helpers for addresses and transaction hashes. */

/** "0x1234567890abcdef…" → "0x1234…cdef". Short strings are returned as is. */
export function shortenAddress(value: string, head = 6, tail = 4): string {
  if (!value) return "";
  if (value.length <= head + tail + 1) return value;
  return `${value.slice(0, head)}…${value.slice(-tail)}`;
}

/** Same as `shortenAddress` with defaults better suited to tx hashes. */
export function shortenHash(value: string, head = 8, tail = 6): string {
  return shortenAddress(value, head, tail);
}

/**
 * Splits an address into head / middle / tail so the UI can emphasise the
 * characters people actually compare (address-poisoning defence). The `0x`
 * prefix counts as part of the head.
 */
export function splitAddressForVerification(
  address: string,
  visible = 6,
): { head: string; middle: string; tail: string } {
  const prefix = address.startsWith("0x") || address.startsWith("0X") ? 2 : 0;
  const h = prefix + visible;
  if (address.length <= h + visible) return { head: address, middle: "", tail: "" };
  return {
    head: address.slice(0, h),
    middle: address.slice(h, address.length - visible),
    tail: address.slice(address.length - visible),
  };
}

/** Groups characters for easier reading: "abcdefgh" → ["abcd", "efgh"]. */
export function chunk(value: string, size = 4): string[] {
  const out: string[] = [];
  for (let i = 0; i < value.length; i += size) out.push(value.slice(i, i + size));
  return out;
}
