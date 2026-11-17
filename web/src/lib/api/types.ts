/**
 * Types of the HTTP API, mirrored verbatim from `docs/api.md` (the single
 * source of truth shared with the Go backend). Keep names and fields in sync
 * with that file; do not add client-only fields here.
 *
 * Amounts are decimal strings in whole units ("12.5"); `*_base` fields are
 * integer strings in base units. Never convert them to `number`.
 */

// ---------------------------------------------------------------------------
// Conventions
// ---------------------------------------------------------------------------

export type ApiErrorCode =
  | "invalid_request" // 400
  | "unauthorized" // 401
  | "forbidden" // 403
  | "not_found" // 404
  | "conflict" // 409
  | "unprocessable" // 422
  | "rate_limited" // 429
  | "internal" // 500
  | "unavailable" // 503
  | "two_factor_setup_required"; // 403, admin API: the account must enable 2FA first

/** Body of every non-2xx response. */
export interface ApiErrorBody {
  error: {
    code: ApiErrorCode | (string & {});
    message: string;
    fields?: Record<string, string>;
  };
}

export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

/** Query parameters accepted by every paginated list. */
export interface PageQuery {
  /** 1-based, default 1. */
  page?: number;
  /** Default 20, max 100. */
  page_size?: number;
}

/** `{ items: T[] }` responses of non-paginated lists. */
export interface ItemsResponse<T> {
  items: T[];
}

export interface HealthStatus {
  status: "ok" | (string & {});
}

// ---------------------------------------------------------------------------
// Shared types
// ---------------------------------------------------------------------------

export type InvoiceStatus =
  | "pending"
  | "partial"
  | "confirming"
  | "paid"
  | "underpaid"
  | "expired"
  | "cancelled";
export type Family = "evm" | "tron" | "utxo" | "solana";
export type WalletKind = "xpub" | "static";

export type QuoteStatus = "active" | "superseded" | "paid" | "closed";
export type TransferStatus = "pending" | "confirmed" | "orphaned";

export interface Invoice {
  id: string;
  merchant_id: string;
  /** Merchant order id, "" if none. */
  order_id: string;
  title: string;
  description: string;
  /** Price amount. */
  amount: string;
  /** "USD", "EUR", "CNY", ... or a crypto symbol. */
  currency: string;
  amount_usd: string | null;
  status: InvoiceStatus;
  overpaid: boolean;
  paid_late: boolean;
  success_url: string;
  cancel_url: string;
  customer_email: string;
  metadata: Record<string, unknown>;
  /** `${checkout_base_url}/pay/${id}` */
  checkout_url: string;
  expires_at: string;
  paid_at: string | null;
  created_at: string;
  updated_at: string;
  /** The active (or paid) quote. */
  quote: Quote | null;
}

export interface Quote {
  id: string;
  asset_id: string;
  symbol: string;
  chain_id: string;
  chain_name: string;
  family: Family;
  /** EVM chain id (decimal string) or "". */
  network_id: string;
  is_testnet: boolean;
  /** "" for native. */
  contract_address: string;
  decimals: number;
  wallet_kind: WalletKind;
  address: string;
  /** Price currency per 1 unit of asset. */
  rate: string;
  /** Amount due, whole units. */
  amount: string;
  amount_base: string;
  /** Seen on chain (pending + confirmed). */
  amount_received: string;
  amount_received_base: string;
  amount_confirmed: string;
  amount_confirmed_base: string;
  /** max(due - received, 0) */
  amount_remaining: string;
  amount_remaining_base: string;
  /** EIP-681 / BIP-21 / Solana Pay / "" (Tron: plain address is used for QR). */
  payment_uri: string;
  required_confirmations: number;
  status: QuoteStatus;
  created_at: string;
}

export interface Transfer {
  id: string;
  chain_id: string;
  /** "" if unknown token. */
  asset_id: string;
  symbol: string;
  tx_hash: string;
  index: number;
  block_number: number;
  from_address: string;
  to_address: string;
  amount: string;
  amount_base: string;
  status: TransferStatus;
  confirmations: number;
  required_confirmations: number;
  explorer_url: string;
  /** null = unmatched */
  invoice_id: string | null;
  quote_id: string | null;
  detected_at: string;
  confirmed_at: string | null;
}

export interface AssetInfo {
  id: string;
  symbol: string;
  name: string;
  chain_id: string;
  chain_name: string;
  family: Family;
  network_id: string;
  is_testnet: boolean;
  contract_address: string;
  decimals: number;
  is_stablecoin: boolean;
}

// ---------------------------------------------------------------------------
// 1. Merchant API — /api/v1
// ---------------------------------------------------------------------------

export interface CreateInvoice {
  /** Required, > 0. */
  amount: string;
  /** Required. */
  currency: string;
  /** Idempotency key, max 128. */
  order_id?: string;
  /** Max 200. */
  title?: string;
  /** Max 2000. */
  description?: string;
  /** Pre-select the asset (creates the quote immediately). */
  asset_id?: string;
  /** http(s) URL. */
  success_url?: string;
  cancel_url?: string;
  customer_email?: string;
  /** Seconds, 60..86400; default = settings.invoice_ttl_seconds. */
  expires_in?: number;
  /** Max 4 KB. */
  metadata?: Record<string, unknown>;
}

export interface MerchantInvoiceQuery extends PageQuery {
  status?: InvoiceStatus;
  order_id?: string;
}

export type WebhookEventType =
  | "invoice.payment_detected"
  | "invoice.confirming"
  | "invoice.paid"
  | "invoice.underpaid"
  | "invoice.expired"
  | "invoice.cancelled";

/** Body of a webhook delivery (gateway → merchant). */
export interface WebhookPayload {
  id: string;
  type: WebhookEventType;
  created_at: string;
  data: Invoice;
}

// ---------------------------------------------------------------------------
// 2. Checkout API — /api/checkout
// ---------------------------------------------------------------------------

export interface CheckoutInvoice {
  id: string;
  merchant_name: string;
  title: string;
  description: string;
  amount: string;
  currency: string;
  status: InvoiceStatus;
  overpaid: boolean;
  paid_late: boolean;
  success_url: string;
  cancel_url: string;
  expires_at: string;
  paid_at: string | null;
  created_at: string;
}

export interface CheckoutView {
  invoice: CheckoutInvoice;
  /** Every asset payable right now, sorted. */
  assets: CheckoutAsset[];
  /** Active quote (or the paid one). */
  quote: Quote | null;
  /** Transfers credited to this invoice, newest first. */
  transfers: Transfer[];
  /** "" when not configured. */
  walletconnect_project_id: string;
  /** For countdown drift correction. */
  server_time: string;
}

export interface CheckoutAsset extends AssetInfo {
  /** Approx. amount due in this asset, null if no rate. */
  estimated_amount: string | null;
}

export interface CreateQuoteRequest {
  asset_id: string;
}

// ---------------------------------------------------------------------------
// 3. Admin API — /api/admin
// ---------------------------------------------------------------------------

export type AdminRole = "owner" | "admin" | "viewer";

export interface AdminUser {
  id: string;
  email: string;
  role: AdminRole;
  active: boolean;
  last_login_at: string | null;
  created_at: string;
  two_factor_enabled: boolean;
}

/** `GET /auth/me`: `AdminUser & { two_factor_setup_required: boolean }`. */
export type AdminMe = AdminUser & { two_factor_setup_required: boolean };

/** The first step of a sign-in for an account with two-factor authentication (`202`). */
export interface LoginChallenge {
  two_factor_required: true;
  challenge_token: string;
  expires_at: string;
}

export interface AdminSession {
  id: string;
  ip: string;
  user_agent: string;
  created_at: string;
  expires_at: string;
  current: boolean;
}

export type EmailMessageStatus = "pending" | "sent" | "failed";

/** E-mail kinds of docs/api.md ("E-mail"). */
export type EmailKind =
  | "security.new_login"
  | "security.login_blocked"
  | "security.password_changed"
  | "security.2fa_enabled"
  | "security.2fa_disabled"
  | "security.2fa_reset"
  | "security.recovery_codes"
  | "security.recovery_code_used"
  | "security.account_created"
  | "security.role_changed"
  | "security.account_deactivated"
  | "security.account_reactivated"
  | "alert.webhook_failed"
  | "alert.transfer_unmatched"
  | "alert.invoice_underpaid"
  | "alert.scanner_failing";

/** One row of the e-mail outbox (bodies only in `GET /email/outbox/{id}`). */
export interface EmailMessage {
  id: string;
  kind: EmailKind | (string & {});
  to: string;
  subject: string;
  status: EmailMessageStatus;
  attempts: number;
  last_error: string;
  created_at: string;
  sent_at: string | null;
}

/** `GET /email/outbox/{id}` */
export type EmailMessageDetail = EmailMessage & { body_text: string };

export type MerchantStatus = "active" | "disabled";

export interface Merchant {
  id: string;
  name: string;
  status: MerchantStatus;
  webhook_url: string;
  has_webhook_secret: boolean;
  created_at: string;
  updated_at: string;
}

export interface ApiKey {
  id: string;
  merchant_id: string;
  name: string;
  prefix: string;
  last_used_at: string | null;
  revoked_at: string | null;
  created_at: string;
}

export interface Wallet {
  id: string;
  name: string;
  family: Family;
  kind: WalletKind;
  xpub: string;
  address: string;
  next_index: number;
  /** Chains this wallet is the default wallet of. */
  chain_ids: string[];
  /** Merchants using it as their own wallet. */
  merchant_bindings: { merchant_id: string; merchant_name: string; chain_id: string }[];
  created_at: string;
}

/** A merchant's own receiving wallet on one chain (overrides the chain's default wallet for that merchant). */
export interface MerchantWallet {
  chain_id: string;
  wallet_id: string;
  wallet_name: string;
  kind: WalletKind;
}

export interface Chain {
  id: string;
  family: Family;
  name: string;
  network_id: string;
  native_symbol: string;
  is_testnet: boolean;
  enabled: boolean;
  /** Masked: scheme + host only, path/query replaced by "/…" when present. */
  rpc_urls: string[];
  rpc_source: "database" | "env" | "default";
  confirmations: number;
  poll_interval_ms: number;
  scan_batch: number;
  explorer_tx_url: string;
  explorer_addr_url: string;
  /** The chain's DEFAULT wallet (merchants without their own wallet). */
  wallet_id: string | null;
  scan_cursor: number;
  last_head: number;
  /** last_head - scan_cursor */
  lag: number;
  last_scan_at: string | null;
  last_error: string;
  /** enabled && default wallet assigned && rpc configured */
  payable: boolean;
  /** Merchants with their own wallet on this chain (they can be paid even when payable is false). */
  merchant_wallets: number;
}

export interface Asset extends AssetInfo {
  quote_precision: number;
  rate_key: string;
  enabled: boolean;
  sort_order: number;
  rate_usd: string | null;
}

export interface InvoiceDetail extends Invoice {
  merchant_name: string;
  quotes: Quote[];
  transfers: Transfer[];
  webhook_events: WebhookEvent[];
}

export interface AdminInvoice extends Invoice {
  merchant_name: string;
}

export type WebhookEventStatus = "pending" | "delivered" | "failed";

export interface WebhookEvent {
  id: string;
  merchant_id: string;
  invoice_id: string;
  event_type: string;
  url: string;
  status: WebhookEventStatus;
  attempts: number;
  next_attempt_at: string;
  last_status_code: number;
  last_error: string;
  last_response: string;
  delivered_at: string | null;
  created_at: string;
  payload: unknown;
}

export interface AuditLog {
  id: number;
  actor_email: string;
  action: string;
  target_type: string;
  target_id: string;
  detail: unknown;
  ip: string;
  created_at: string;
}

export interface Settings {
  invoice_ttl_seconds: number;
  grace_period_seconds: number;
  underpay_tolerance_bps: number;
  stablecoin_peg: boolean;
  rate_markup_bps: number;
  webhook_max_attempts: number;
  webhook_timeout_seconds: number;
  checkout_base_url: string;
  walletconnect_project_id: string;
  /** Every admin must use two-factor authentication. */
  require_2fa: boolean;
  /** E-mail admins about security events on their own account. */
  security_emails: boolean;
  /** E-mail operational alerts. */
  alert_emails: boolean;
  /** Max 10 addresses; [] = every active owner. */
  alert_recipients: string[];
  /** Language of every e-mail. */
  email_locale: EmailLocale;
}

export type EmailLocale = "zh" | "en";

export interface Overview {
  range_days: number;
  /** The `merchant_id` filter; null = every merchant. */
  merchant_id: string | null;
  totals: {
    invoices: number;
    paid_invoices: number;
    open_invoices: number;
    volume_usd: string;
    /** 0..1 */
    conversion_rate: number;
    unmatched_transfers: number;
    failed_webhooks: number;
  };
  /** One row per day, zero-filled. */
  daily: {
    /** YYYY-MM-DD */
    date: string;
    invoices: number;
    paid: number;
    volume_usd: string;
  }[];
  by_asset: {
    asset_id: string;
    symbol: string;
    chain_id: string;
    chain_name: string;
    paid: number;
    volume_usd: string;
  }[];
  by_status: { status: InvoiceStatus; count: number }[];
}

/** One merchant's figures over the range (`GET /overview/merchants`); every merchant has a row. */
export interface MerchantOverview {
  merchant_id: string;
  merchant_name: string;
  status: "active" | "disabled";
  /** Created in range. */
  invoices: number;
  /** Paid in range (by paid_at). */
  paid_invoices: number;
  /** Sum of amount_usd of invoices paid in range. */
  volume_usd: string;
  /** Created in range and now paid ÷ created in range (0 when none). */
  conversion_rate: number;
  /** Currently open (pending/partial/confirming), not range-limited. */
  open_invoices: number;
  /** Currently failed events, not range-limited. */
  failed_webhooks: number;
  /** Newest invoice ever created. */
  last_invoice_at: string | null;
  /** Newest paid_at ever. */
  last_paid_at: string | null;
}

// ---- Admin request / response bodies (inline in the api.md table) ----------

export interface LoginRequest {
  email: string;
  password: string;
}

/** `POST /auth/login` answers `AdminUser` (no 2FA) or `202 LoginChallenge`. */
export type LoginResponse = AdminUser | LoginChallenge;

/** `POST /auth/login/2fa` — exactly one of `code` / `recovery_code`. */
export interface LoginTwoFactorRequest {
  challenge_token: string;
  code?: string;
  recovery_code?: string;
}

export interface TwoFactorSetupRequest {
  current_password: string;
}

export interface TwoFactorSetupResponse {
  /** Base32 secret for manual entry. */
  secret: string;
  otpauth_uri: string;
}

export interface TwoFactorEnableRequest {
  code: string;
}

export interface TwoFactorDisableRequest {
  current_password: string;
  /** A current code or a recovery code. */
  code: string;
}

export interface RegenerateRecoveryCodesRequest {
  current_password: string;
  code: string;
}

/** 10 single-use codes, shown only in this response. */
export interface RecoveryCodesResponse {
  recovery_codes: string[];
}

export interface RevokeOtherSessionsResponse {
  revoked: number;
}

export interface EmailStatus {
  configured: boolean;
  from: string;
  /** Host only, never credentials; "" when not configured. */
  host: string;
}

export interface TestEmailRequest {
  /** Default: the caller's own address. */
  to?: string;
}

export interface TestEmailResult {
  ok: boolean;
  /** Sanitized SMTP error ("" when ok). */
  error: string;
}

export interface EmailOutboxQuery extends PageQuery {
  status?: EmailMessageStatus;
  kind?: string;
}

export interface ChangePasswordRequest {
  current_password: string;
  /** Min 8 chars. */
  new_password: string;
}

export interface OverviewQuery {
  /** 1..365, default 30. */
  days?: number;
  /** Restrict every figure to this merchant (unmatched_transfers is then 0). */
  merchant_id?: string;
}

export type MerchantOverviewSort =
  | "volume_usd"
  | "invoices"
  | "paid_invoices"
  | "conversion_rate"
  | "open_invoices"
  | "failed_webhooks"
  | "name";

export type SortOrder = "asc" | "desc";

export interface MerchantOverviewQuery extends PageQuery {
  /** 1..365, default 30 (as `GET /overview`). */
  days?: number;
  /** Default `volume_usd`. */
  sort?: MerchantOverviewSort;
  /** Default `desc` (`asc` for `name`). */
  order?: SortOrder;
}

/** `GET /overview/merchants` → `Page<MerchantOverview> & { range_days: number }`. */
export type MerchantOverviewPage = Page<MerchantOverview> & { range_days: number };

export interface AdminInvoiceQuery extends PageQuery {
  status?: InvoiceStatus;
  merchant_id?: string;
  /** Invoice id, order id, address or tx hash. */
  q?: string;
  /** RFC 3339. */
  from?: string;
  /** RFC 3339. */
  to?: string;
}

export type AdminCreateInvoice = CreateInvoice & { merchant_id: string };

export interface MarkPaidRequest {
  note: string;
}

export interface TransferQuery extends PageQuery {
  chain_id?: string;
  status?: TransferStatus;
  matched?: boolean;
  /** Tx hash or address. */
  q?: string;
}

export interface AssignTransferRequest {
  invoice_id: string;
}

export interface MerchantQuery extends PageQuery {
  q?: string;
}

export interface CreateMerchantRequest {
  name: string;
  webhook_url?: string;
}

export interface CreateMerchantResponse {
  merchant: Merchant;
  /** Shown once. */
  webhook_secret: string;
}

export interface MerchantDetail {
  merchant: Merchant;
  api_keys: ApiKey[];
  /** Explicit bindings only. */
  wallets: MerchantWallet[];
}

export interface UpdateMerchantRequest {
  name?: string;
  status?: MerchantStatus;
  webhook_url?: string;
}

export interface CreateApiKeyRequest {
  name: string;
}

export interface CreateApiKeyResponse {
  api_key: ApiKey;
  /** Plaintext, shown once. */
  key: string;
}

export interface RotateWebhookSecretResponse {
  webhook_secret: string;
}

export interface SetMerchantWalletRequest {
  wallet_id: string;
}

export interface UpdateChainRequest {
  enabled?: boolean;
  /** [] resets to env/default. */
  rpc_urls?: string[];
  confirmations?: number;
  poll_interval_ms?: number;
  scan_batch?: number;
  /** The chain's default wallet. */
  wallet_id?: string | null;
}

export interface TestRpcRequest {
  /** Omit to test the configured endpoints. */
  rpc_url?: string;
}

export interface TestRpcResult {
  ok: boolean;
  head: number;
  latency_ms: number;
  error: string;
}

export interface CreateAssetRequest {
  chain_id: string;
  symbol: string;
  name: string;
  contract_address: string;
  decimals: number;
  quote_precision: number;
  rate_key: string;
  is_stablecoin: boolean;
}

export interface UpdateAssetRequest {
  enabled?: boolean;
  quote_precision?: number;
  sort_order?: number;
  rate_key?: string;
  name?: string;
}

export interface WalletPreviewRequest {
  family: Family;
  kind: WalletKind;
  xpub?: string;
  address?: string;
  chain_id: string;
}

export interface WalletPreviewResponse {
  /** First 3 derived addresses, or the normalised static address. */
  addresses: string[];
}

export interface CreateWalletRequest {
  name: string;
  family: Family;
  kind: WalletKind;
  xpub?: string;
  address?: string;
}

export interface UpdateWalletRequest {
  name: string;
}

export interface WebhookEventQuery extends PageQuery {
  status?: WebhookEventStatus;
  merchant_id?: string;
  invoice_id?: string;
}

export interface CreateUserRequest {
  email: string;
  password: string;
  role: AdminRole;
}

export interface UpdateUserRequest {
  role?: AdminRole;
  active?: boolean;
  password?: string;
}

export interface AuditLogQuery extends PageQuery {
  action?: string;
  actor?: string;
}
