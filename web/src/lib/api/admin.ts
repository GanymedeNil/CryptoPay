/**
 * Admin API — `/api/admin`. One typed function per endpoint of docs/api.md §3.
 *
 * Auth is the HttpOnly `cp_session` cookie set by `login`; the shared client
 * sends `credentials: "same-origin"` and the `X-Requested-With: cryptopay`
 * CSRF header on every mutation. A 401 means "not signed in" → go to login.
 */
import { apiClient, seg, type ApiClient } from "./client";
import type {
  AdminCreateInvoice,
  AdminInvoice,
  AdminInvoiceQuery,
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
  InvoiceDetail,
  ItemsResponse,
  LoginRequest,
  MarkPaidRequest,
  Merchant,
  MerchantDetail,
  MerchantQuery,
  Overview,
  OverviewQuery,
  Page,
  MerchantWallet,
  RotateWebhookSecretResponse,
  SetMerchantWalletRequest,
  Settings,
  TestRpcRequest,
  TestRpcResult,
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
    /** `POST /auth/login` (401 bad credentials; 429 after repeated failures). */
    login: (body: LoginRequest) => client.post<AdminUser>(`${A}/auth/login`, body),
    /** `POST /auth/logout` → 204 */
    logout: () => client.post<void>(`${A}/auth/logout`),
    /** `GET /auth/me` */
    me: (o?: Opts) => client.get<AdminUser>(`${A}/auth/me`, undefined, o),
    /** `POST /auth/password` → 204 (min 8 chars). */
    changePassword: (body: ChangePasswordRequest) =>
      client.post<void>(`${A}/auth/password`, body),

    // ---- overview --------------------------------------------------------
    /** `GET /overview?days=` (1..365, default 30). */
    getOverview: (query: OverviewQuery = {}, o?: Opts) =>
      client.get<Overview>(`${A}/overview`, { ...query }, o),

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

    // ---- audit -----------------------------------------------------------
    /** `GET /audit-logs` */
    listAuditLogs: (query: AuditLogQuery = {}, o?: Opts) =>
      client.get<Page<AuditLog>>(`${A}/audit-logs`, { ...query }, o),
  };
}

export type AdminApi = ReturnType<typeof createAdminApi>;

/** Default instance for the browser (same-origin, cookie session). */
export const adminApi: AdminApi = createAdminApi();
