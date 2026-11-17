/**
 * Admin API — `/api/admin`. One typed function per endpoint of docs/api.md §3.
 *
 * Auth is the HttpOnly `cp_session` cookie set by `login`; the shared client
 * sends `credentials: "same-origin"` and the `X-Requested-With: cryptopay`
 * CSRF header on every mutation. A 401 means "not signed in" → go to login.
 * Accounts with two-factor authentication sign in in two steps: `login`
 * answers a `LoginChallenge` (202, no cookie) and `loginTwoFactor` completes it.
 */
import { apiClient, seg, type ApiClient } from "./client";
import type {
  AdminCreateInvoice,
  AdminInvoice,
  AdminInvoiceQuery,
  AdminMe,
  AdminSession,
  AdminUser,
  Asset,
  AssignTransferRequest,
  AuditLog,
  AuditLogQuery,
  Chain,
  ChangePasswordRequest,
  CreateApiKeyRequest,
  CreateApiKeyResponse,
  CreateAssetRequest,
  CreateMerchantRequest,
  CreateMerchantResponse,
  CreateUserRequest,
  CreateWalletRequest,
  EmailMessage,
  EmailMessageDetail,
  EmailOutboxQuery,
  EmailStatus,
  InvoiceDetail,
  ItemsResponse,
  LoginRequest,
  LoginResponse,
  LoginTwoFactorRequest,
  MarkPaidRequest,
  Merchant,
  MerchantDetail,
  MerchantOverviewPage,
  MerchantOverviewQuery,
  MerchantQuery,
  Overview,
  OverviewQuery,
  Page,
  MerchantWallet,
  RecoveryCodesResponse,
  RegenerateRecoveryCodesRequest,
  RevokeOtherSessionsResponse,
  RotateWebhookSecretResponse,
  SetMerchantWalletRequest,
  Settings,
  TestEmailRequest,
  TestEmailResult,
  TestRpcRequest,
  TestRpcResult,
  TwoFactorDisableRequest,
  TwoFactorEnableRequest,
  TwoFactorSetupRequest,
  TwoFactorSetupResponse,
  Transfer,
  TransferQuery,
  UpdateAssetRequest,
  UpdateChainRequest,
  UpdateMerchantRequest,
  UpdateUserRequest,
  UpdateWalletRequest,
  Wallet,
  WalletPreviewRequest,
  WalletPreviewResponse,
  WebhookEvent,
  WebhookEventQuery,
} from "./types";

const A = "/api/admin";

type Opts = { signal?: AbortSignal };

export function createAdminApi(client: ApiClient = apiClient) {
  return {
    // ---- auth ------------------------------------------------------------
    /**
     * `POST /auth/login`: `AdminUser` + cookie, or `202 LoginChallenge` (no
     * cookie) for an account with two-factor authentication — see
     * `isLoginChallenge` in `@/lib/admin/two-factor` (401 bad credentials; 429 after repeated failures).
     */
    login: (body: LoginRequest) => client.post<LoginResponse>(`${A}/auth/login`, body),
    /** `POST /auth/login/2fa` (generic 401 for any failure; 429 when throttled). */
    loginTwoFactor: (body: LoginTwoFactorRequest) => client.post<AdminUser>(`${A}/auth/login/2fa`, body),
    /** `POST /auth/logout` → 204 */
    logout: () => client.post<void>(`${A}/auth/logout`),
    /** `GET /auth/me` */
    me: (o?: Opts) => client.get<AdminMe>(`${A}/auth/me`, undefined, o),
    /** `POST /auth/password` → 204 (min 8 chars). */
    changePassword: (body: ChangePasswordRequest) =>
      client.post<void>(`${A}/auth/password`, body),

    // ---- own two-factor authentication ----------------------------------
    /** `POST /auth/2fa/setup` (pending secret; 409 when already enabled). */
    setupTwoFactor: (body: TwoFactorSetupRequest) =>
      client.post<TwoFactorSetupResponse>(`${A}/auth/2fa/setup`, body),
    /** `POST /auth/2fa/enable` → recovery codes, shown once. */
    enableTwoFactor: (body: TwoFactorEnableRequest) =>
      client.post<RecoveryCodesResponse>(`${A}/auth/2fa/enable`, body),
    /** `POST /auth/2fa/disable` → 204 (409 while `require_2fa` is on). */
    disableTwoFactor: (body: TwoFactorDisableRequest) => client.post<void>(`${A}/auth/2fa/disable`, body),
    /** `POST /auth/2fa/recovery-codes` → a new set of recovery codes. */
    regenerateRecoveryCodes: (body: RegenerateRecoveryCodesRequest) =>
      client.post<RecoveryCodesResponse>(`${A}/auth/2fa/recovery-codes`, body),

    // ---- own sessions ----------------------------------------------------
    /** `GET /auth/sessions` (own unexpired sessions, newest first). */
    listSessions: (o?: Opts) => client.get<ItemsResponse<AdminSession>>(`${A}/auth/sessions`, undefined, o),
    /** `DELETE /auth/sessions/{id}` → 204 (the current one = sign out). */
    revokeSession: (id: string) => client.delete<void>(`${A}/auth/sessions/${seg(id)}`),
    /** `POST /auth/sessions/revoke-others` */
    revokeOtherSessions: () => client.post<RevokeOtherSessionsResponse>(`${A}/auth/sessions/revoke-others`),

    // ---- overview --------------------------------------------------------
    /** `GET /overview?days=&merchant_id=` (days 1..365, default 30; 404 for an unknown merchant). */
    getOverview: (query: OverviewQuery = {}, o?: Opts) =>
      client.get<Overview>(`${A}/overview`, { ...query }, o),
    /** `GET /overview/merchants` (one row per merchant, sorted and paged by the server). */
    listMerchantOverviews: (query: MerchantOverviewQuery = {}, o?: Opts) =>
      client.get<MerchantOverviewPage>(`${A}/overview/merchants`, { ...query }, o),

    // ---- invoices --------------------------------------------------------
    /** `GET /invoices` */
    listInvoices: (query: AdminInvoiceQuery = {}, o?: Opts) =>
      client.get<Page<AdminInvoice>>(`${A}/invoices`, { ...query }, o),
    /** `POST /invoices` → 201 */
    createInvoice: (body: AdminCreateInvoice) =>
      client.post<AdminInvoice>(`${A}/invoices`, body),
    /** `GET /invoices/{id}` */
    getInvoice: (id: string, o?: Opts) =>
      client.get<InvoiceDetail>(`${A}/invoices/${seg(id)}`, undefined, o),
    /** `POST /invoices/{id}/cancel` */
    cancelInvoice: (id: string) => client.post<AdminInvoice>(`${A}/invoices/${seg(id)}/cancel`),
    /** `POST /invoices/{id}/mark-paid` (manual resolution). */
    markInvoicePaid: (id: string, body: MarkPaidRequest) =>
      client.post<AdminInvoice>(`${A}/invoices/${seg(id)}/mark-paid`, body),

    // ---- transfers -------------------------------------------------------
    /** `GET /transfers` */
    listTransfers: (query: TransferQuery = {}, o?: Opts) =>
      client.get<Page<Transfer>>(`${A}/transfers`, { ...query }, o),
    /** `POST /transfers/{id}/assign` (unmatched transfers only). */
    assignTransfer: (id: string, body: AssignTransferRequest) =>
      client.post<Transfer>(`${A}/transfers/${seg(id)}/assign`, body),

    // ---- merchants -------------------------------------------------------
    /** `GET /merchants` */
    listMerchants: (query: MerchantQuery = {}, o?: Opts) =>
      client.get<Page<Merchant>>(`${A}/merchants`, { ...query }, o),
    /** `POST /merchants` → 201, webhook secret shown once. */
    createMerchant: (body: CreateMerchantRequest) =>
      client.post<CreateMerchantResponse>(`${A}/merchants`, body),
    /** `GET /merchants/{id}` */
    getMerchant: (id: string, o?: Opts) =>
      client.get<MerchantDetail>(`${A}/merchants/${seg(id)}`, undefined, o),
    /** `PATCH /merchants/{id}` */
    updateMerchant: (id: string, body: UpdateMerchantRequest) =>
      client.patch<Merchant>(`${A}/merchants/${seg(id)}`, body),
    /** `POST /merchants/{id}/api-keys` → 201, plaintext key shown once. */
    createApiKey: (merchantId: string, body: CreateApiKeyRequest) =>
      client.post<CreateApiKeyResponse>(`${A}/merchants/${seg(merchantId)}/api-keys`, body),
    /** `DELETE /merchants/{id}/api-keys/{key_id}` → 204 (revokes). */
    revokeApiKey: (merchantId: string, keyId: string) =>
      client.delete<void>(`${A}/merchants/${seg(merchantId)}/api-keys/${seg(keyId)}`),
    /** `POST /merchants/{id}/webhook-secret` (rotates). */
    rotateWebhookSecret: (merchantId: string) =>
      client.post<RotateWebhookSecretResponse>(`${A}/merchants/${seg(merchantId)}/webhook-secret`),
    /** `PUT /merchants/{id}/wallets/{chain_id}` (insert or replace the merchant's own wallet on a chain). */
    setMerchantWallet: (merchantId: string, chainId: string, body: SetMerchantWalletRequest) =>
      client.put<MerchantWallet>(`${A}/merchants/${seg(merchantId)}/wallets/${seg(chainId)}`, body),
    /** `DELETE /merchants/{id}/wallets/{chain_id}` → 204 (back to the chain's default wallet; idempotent). */
    unsetMerchantWallet: (merchantId: string, chainId: string) =>
      client.delete<void>(`${A}/merchants/${seg(merchantId)}/wallets/${seg(chainId)}`),

    // ---- chains ----------------------------------------------------------
    /** `GET /chains` */
    listChains: (o?: Opts) => client.get<ItemsResponse<Chain>>(`${A}/chains`, undefined, o),
    /** `PATCH /chains/{id}` */
    updateChain: (id: string, body: UpdateChainRequest) =>
      client.patch<Chain>(`${A}/chains/${seg(id)}`, body),
    /** `POST /chains/{id}/test-rpc` */
    testChainRpc: (id: string, body: TestRpcRequest = {}) =>
      client.post<TestRpcResult>(`${A}/chains/${seg(id)}/test-rpc`, body),

    // ---- assets ----------------------------------------------------------
    /** `GET /assets` */
    listAssets: (o?: Opts) => client.get<ItemsResponse<Asset>>(`${A}/assets`, undefined, o),
    /** `POST /assets` → 201 (custom token). */
    createAsset: (body: CreateAssetRequest) => client.post<Asset>(`${A}/assets`, body),
    /** `PATCH /assets/{id}` */
    updateAsset: (id: string, body: UpdateAssetRequest) =>
      client.patch<Asset>(`${A}/assets/${seg(id)}`, body),

    // ---- wallets ---------------------------------------------------------
    /** `GET /wallets` */
    listWallets: (o?: Opts) => client.get<ItemsResponse<Wallet>>(`${A}/wallets`, undefined, o),
    /** `POST /wallets/preview` */
    previewWallet: (body: WalletPreviewRequest) =>
      client.post<WalletPreviewResponse>(`${A}/wallets/preview`, body),
    /** `POST /wallets` → 201 (409 with `fields.xpub` / `fields.address` when the key material is already registered). */
    createWallet: (body: CreateWalletRequest) => client.post<Wallet>(`${A}/wallets`, body),
    /** `PATCH /wallets/{id}` */
    updateWallet: (id: string, body: UpdateWalletRequest) =>
      client.patch<Wallet>(`${A}/wallets/${seg(id)}`, body),
    /** `DELETE /wallets/{id}` → 204 (409 if referenced by quotes or used as a merchant's own wallet). */
    deleteWallet: (id: string) => client.delete<void>(`${A}/wallets/${seg(id)}`),

    // ---- webhook events --------------------------------------------------
    /** `GET /webhook-events` */
    listWebhookEvents: (query: WebhookEventQuery = {}, o?: Opts) =>
      client.get<Page<WebhookEvent>>(`${A}/webhook-events`, { ...query }, o),
    /** `POST /webhook-events/{id}/retry` */
    retryWebhookEvent: (id: string) =>
      client.post<WebhookEvent>(`${A}/webhook-events/${seg(id)}/retry`),

    // ---- settings --------------------------------------------------------
    /** `GET /settings` */
    getSettings: (o?: Opts) => client.get<Settings>(`${A}/settings`, undefined, o),
    /** `PUT /settings` */
    updateSettings: (body: Settings) => client.put<Settings>(`${A}/settings`, body),

    // ---- users (owner only) ----------------------------------------------
    /** `GET /users` */
    listUsers: (o?: Opts) => client.get<ItemsResponse<AdminUser>>(`${A}/users`, undefined, o),
    /** `POST /users` → 201 */
    createUser: (body: CreateUserRequest) => client.post<AdminUser>(`${A}/users`, body),
    /** `PATCH /users/{id}` */
    updateUser: (id: string, body: UpdateUserRequest) =>
      client.patch<AdminUser>(`${A}/users/${seg(id)}`, body),
    /** `DELETE /users/{id}` → 204 */
    deleteUser: (id: string) => client.delete<void>(`${A}/users/${seg(id)}`),
    /** `DELETE /users/{id}/2fa` → 204 (owner reset of another user's two-factor authentication). */
    resetUserTwoFactor: (id: string) => client.delete<void>(`${A}/users/${seg(id)}/2fa`),

    // ---- audit -----------------------------------------------------------
    /** `GET /audit-logs` */
    listAuditLogs: (query: AuditLogQuery = {}, o?: Opts) =>
      client.get<Page<AuditLog>>(`${A}/audit-logs`, { ...query }, o),

    // ---- e-mail ----------------------------------------------------------
    /** `GET /email/status` */
    getEmailStatus: (o?: Opts) => client.get<EmailStatus>(`${A}/email/status`, undefined, o),
    /** `POST /email/test` (synchronous; 409 when e-mail is not configured). */
    sendTestEmail: (body: TestEmailRequest = {}) => client.post<TestEmailResult>(`${A}/email/test`, body),
    /** `GET /email/outbox` (owner/admin). */
    listEmailOutbox: (query: EmailOutboxQuery = {}, o?: Opts) =>
      client.get<Page<EmailMessage>>(`${A}/email/outbox`, { ...query }, o),
    /** `GET /email/outbox/{id}` (with the text body). */
    getEmailMessage: (id: string, o?: Opts) =>
      client.get<EmailMessageDetail>(`${A}/email/outbox/${seg(id)}`, undefined, o),
    /** `POST /email/outbox/{id}/retry` (409 for a message that was already sent). */
    retryEmailMessage: (id: string) => client.post<EmailMessage>(`${A}/email/outbox/${seg(id)}/retry`),
  };
}

export type AdminApi = ReturnType<typeof createAdminApi>;

/** Default instance for the browser (same-origin, cookie session). */
export const adminApi: AdminApi = createAdminApi();
