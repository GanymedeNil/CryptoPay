import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, type RenderOptions } from "@testing-library/react";
import { AuthProvider } from "@/components/admin/auth";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { AdminMe, AdminRole, AdminSession, AdminUser, Chain, EmailMessage, Merchant, Settings, Wallet } from "@/lib/api/types";
import type { Locale } from "@/lib/i18n/config";
import { I18nProvider } from "@/lib/i18n/provider";

export function adminUser(role: AdminRole = "owner", overrides: Partial<AdminUser> = {}): AdminUser {
  return {
    id: `u-${role}`,
    email: `${role}@cryptopay.test`,
    role,
    active: true,
    last_login_at: "2026-11-16T07:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    two_factor_enabled: false,
    ...overrides,
  };
}

/** `GET /auth/me` answer for a user. */
export function adminMe(role: AdminRole = "owner", overrides: Partial<AdminMe> = {}): AdminMe {
  return { ...adminUser(role), two_factor_setup_required: false, ...overrides };
}

export function sessionFixture(overrides: Partial<AdminSession> = {}): AdminSession {
  return {
    id: "s-1",
    ip: "203.0.113.24",
    user_agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
    created_at: "2026-11-16T07:00:00Z",
    expires_at: "2026-11-23T07:00:00Z",
    current: true,
    ...overrides,
  };
}

export function emailFixture(overrides: Partial<EmailMessage> = {}): EmailMessage {
  return {
    id: "e-1",
    kind: "alert.webhook_failed",
    to: "ops@example.com",
    subject: "[CryptoPay] Webhook delivery failed for Acme Coffee Roasters",
    status: "failed",
    attempts: 8,
    last_error: "dial tcp 10.0.0.5:587: connection refused",
    created_at: "2026-11-16T06:00:00Z",
    sent_at: null,
    ...overrides,
  };
}

export function testQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity, staleTime: Infinity }, mutations: { retry: false } },
  });
}

/**
 * Renders inside the admin providers: query client, i18n (en), tooltips and a
 * signed-in user (owner by default; `user: null` renders without AuthProvider).
 */
export function renderAdmin(
  ui: React.ReactElement,
  {
    role = "owner",
    user,
    locale = "en",
    client = testQueryClient(),
    ...options
  }: RenderOptions & { role?: AdminRole; user?: AdminUser | null; locale?: Locale; client?: QueryClient } = {},
) {
  const u = user === undefined ? adminUser(role) : user;
  // A wrapper (not a wrapping element) so `rerender` keeps the providers.
  function Wrapper({ children }: { children: React.ReactNode }) {
    return (
      <QueryClientProvider client={client}>
        <I18nProvider initialLocale={locale}>
          <TooltipProvider>{u ? <AuthProvider user={u}>{children}</AuthProvider> : children}</TooltipProvider>
        </I18nProvider>
      </QueryClientProvider>
    );
  }
  const result = render(ui, { wrapper: Wrapper, ...options });
  return { ...result, client };
}

export function chainFixture(overrides: Partial<Chain> = {}): Chain {
  return {
    id: "ethereum",
    family: "evm",
    name: "Ethereum",
    network_id: "1",
    native_symbol: "ETH",
    is_testnet: false,
    enabled: true,
    rpc_urls: ["https://eth-mainnet.g.alchemy.com/…"],
    rpc_source: "database",
    confirmations: 12,
    poll_interval_ms: 3000,
    scan_batch: 500,
    explorer_tx_url: "https://etherscan.io/tx/{tx}",
    explorer_addr_url: "https://etherscan.io/address/{address}",
    wallet_id: "w-1",
    scan_cursor: 100,
    last_head: 102,
    lag: 2,
    last_scan_at: "2026-11-16T07:59:00Z",
    last_error: "",
    payable: true,
    merchant_wallets: 0,
    ...overrides,
  };
}

export function walletFixture(overrides: Partial<Wallet> = {}): Wallet {
  return {
    id: "w-1",
    name: "Ledger · EVM main account",
    family: "evm",
    kind: "xpub",
    xpub: "xpub6CUGRUonZSQ4TWtTMmzXdrXDtypWKiKrhko4egpiMZbpiaQL2jkwSB1icqYh2cfDfVxdx4df189oLKnC5fSwqPfgyP3hooxujYzAu3fDVmz",
    address: "",
    next_index: 3,
    chain_ids: ["ethereum"],
    merchant_bindings: [],
    created_at: "2026-09-01T00:00:00Z",
    ...overrides,
  };
}

export function merchantFixture(overrides: Partial<Merchant> = {}): Merchant {
  return {
    id: "m-1",
    name: "Acme Coffee Roasters",
    status: "active",
    webhook_url: "https://shop.example/hook",
    has_webhook_secret: true,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides,
  };
}

export const SETTINGS: Settings = {
  invoice_ttl_seconds: 900,
  grace_period_seconds: 1800,
  underpay_tolerance_bps: 50,
  stablecoin_peg: true,
  rate_markup_bps: 100,
  webhook_max_attempts: 10,
  webhook_timeout_seconds: 10,
  checkout_base_url: "https://pay.example.com",
  walletconnect_project_id: "",
  require_2fa: false,
  security_emails: true,
  alert_emails: false,
  alert_recipients: [],
  email_locale: "zh",
};
