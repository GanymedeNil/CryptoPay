/**
 * Wallet errors come in every shape (EIP-1193 `{code, message}`, viem error
 * classes with `cause` chains, TronLink plain strings, Phantom objects…).
 * `normalizeWalletError` maps them to a few codes the UI can explain.
 */

export type WalletErrorCode =
  | "rejected"
  | "wrong_network"
  | "insufficient_funds"
  | "not_installed"
  | "unsupported"
  | "pending"
  | "unknown";

export class WalletError extends Error {
  override readonly name = "WalletError";
  readonly code: WalletErrorCode;

  constructor(code: WalletErrorCode, message?: string, options?: { cause?: unknown }) {
    super(message ?? code, options);
    this.code = code;
  }
}

interface ErrorLike {
  code?: unknown;
  name?: unknown;
  message?: unknown;
  shortMessage?: unknown;
  details?: unknown;
  reason?: unknown;
  error?: unknown;
  data?: unknown;
  cause?: unknown;
}

/** Flattens an error and its causes into (codes, names, messages). */
function collect(e: unknown, depth = 0, acc = { codes: [] as unknown[], names: [] as string[], texts: [] as string[] }) {
  if (e === null || e === undefined || depth > 6) return acc;
  if (typeof e === "string") {
    acc.texts.push(e);
    return acc;
  }
  if (typeof e !== "object") return acc;
  const x = e as ErrorLike;
  if (x.code !== undefined) acc.codes.push(x.code);
  if (typeof x.name === "string") acc.names.push(x.name);
  for (const v of [x.shortMessage, x.message, x.details, x.reason]) {
    if (typeof v === "string" && v) acc.texts.push(v);
  }
  collect(x.cause, depth + 1, acc);
  collect(x.error, depth + 1, acc);
  if (typeof x.data === "object") collect(x.data, depth + 1, acc);
  return acc;
}

const REJECTED_RE =
  /user (rejected|denied|cancel+ed|aborted)|rejected the request|request rejected|declined|denied transaction|cancel+ed by user|user canceled|user cancelled/i;
const PENDING_RE = /already pending|request of type .* already|in queue|being processed/i;
const NETWORK_RE =
  /chain ?mismatch|wrong network|does not match the target chain|unrecognized chain|unsupported chain|blockhash not found|network mismatch/i;
const FUNDS_RE =
  /insufficient (funds|balance|lamports)|exceeds balance|not enough (balance|funds|energy|bandwidth)|balance is not sufficient|bandwith_error|bandwidth_error|account resource insufficient|custom program error: 0x1\b|insufficient fee/i;
const MISSING_RE = /provider not found|connector not found|not installed|no provider/i;

export function normalizeWalletError(e: unknown): WalletError {
  if (e instanceof WalletError) return e;
  const { codes, names, texts } = collect(e);
  const text = texts.join(" | ");
  const has = (c: unknown) => codes.includes(c);
  const named = (n: string) => names.includes(n);

  if (has(4001) || has("ACTION_REJECTED") || named("UserRejectedRequestError") || REJECTED_RE.test(text)) {
    return new WalletError("rejected", text || "rejected", { cause: e });
  }
  if (has(-32002) || PENDING_RE.test(text)) {
    return new WalletError("pending", text, { cause: e });
  }
  if (named("InsufficientFundsError") || FUNDS_RE.test(text)) {
    return new WalletError("insufficient_funds", text, { cause: e });
  }
  if (
    has(4902) ||
    named("ChainMismatchError") ||
    named("ChainNotConfiguredError") ||
    named("SwitchChainError") ||
    named("SwitchChainNotSupportedError") ||
    NETWORK_RE.test(text)
  ) {
    return new WalletError("wrong_network", text, { cause: e });
  }
  if (named("ConnectorNotFoundError") || named("ProviderNotFoundError") || MISSING_RE.test(text)) {
    return new WalletError("not_installed", text, { cause: e });
  }
  const first = texts.find(Boolean) ?? (e instanceof Error ? e.message : "Unknown error");
  return new WalletError("unknown", first.length > 180 ? `${first.slice(0, 177)}…` : first, { cause: e });
}
