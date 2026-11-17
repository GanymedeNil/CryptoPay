"use client";

import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryKey,
} from "@tanstack/react-query";
import { toast } from "sonner";
import { adminApi } from "@/lib/api/admin";
import type {
  AdminInvoiceQuery,
  AuditLogQuery,
  EmailOutboxQuery,
  MerchantQuery,
  TransferQuery,
  WebhookEventQuery,
} from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { describeError } from "./errors";

/**
 * Query keys. Everything lives under "admin" so logout can drop the whole
 * cache; lists and details of one resource share a prefix so a mutation can
 * invalidate both with one key (e.g. `adminKeys.invoices.all`).
 */
export const adminKeys = {
  all: ["admin"] as const,
  me: ["admin", "me"] as const,
  sessions: ["admin", "sessions"] as const,
  overview: {
    all: ["admin", "overview"] as const,
    range: (days: number) => ["admin", "overview", days] as const,
  },
  invoices: {
    all: ["admin", "invoices"] as const,
    list: (q: AdminInvoiceQuery) => ["admin", "invoices", "list", q] as const,
    detail: (id: string) => ["admin", "invoices", "detail", id] as const,
  },
  transfers: {
    all: ["admin", "transfers"] as const,
    list: (q: TransferQuery) => ["admin", "transfers", "list", q] as const,
  },
  merchants: {
    all: ["admin", "merchants"] as const,
    list: (q: MerchantQuery) => ["admin", "merchants", "list", q] as const,
    detail: (id: string) => ["admin", "merchants", "detail", id] as const,
  },
  chains: ["admin", "chains"] as const,
  assets: ["admin", "assets"] as const,
  wallets: ["admin", "wallets"] as const,
  webhooks: {
    all: ["admin", "webhook-events"] as const,
    list: (q: WebhookEventQuery) => ["admin", "webhook-events", "list", q] as const,
  },
  settings: ["admin", "settings"] as const,
  users: ["admin", "users"] as const,
  auditLogs: {
    all: ["admin", "audit-logs"] as const,
    list: (q: AuditLogQuery) => ["admin", "audit-logs", "list", q] as const,
  },
  email: {
    all: ["admin", "email"] as const,
    status: ["admin", "email", "status"] as const,
    outbox: (q: EmailOutboxQuery) => ["admin", "email", "outbox", q] as const,
    message: (id: string) => ["admin", "email", "message", id] as const,
  },
};

// ---- reads -----------------------------------------------------------------

export function useMe() {
  return useQuery({
    queryKey: adminKeys.me,
    queryFn: ({ signal }) => adminApi.me({ signal }),
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: true,
  });
}

export function useOverview(days: number) {
  return useQuery({
    queryKey: adminKeys.overview.range(days),
    queryFn: ({ signal }) => adminApi.getOverview({ days }, { signal }),
    placeholderData: keepPreviousData,
    refetchInterval: 60_000,
  });
}

export function useInvoices(q: AdminInvoiceQuery) {
  return useQuery({
    queryKey: adminKeys.invoices.list(q),
    queryFn: ({ signal }) => adminApi.listInvoices(q, { signal }),
    placeholderData: keepPreviousData,
  });
}

export function useInvoice(id: string) {
  return useQuery({
    queryKey: adminKeys.invoices.detail(id),
    queryFn: ({ signal }) => adminApi.getInvoice(id, { signal }),
    // Open invoices change on their own (scanner): keep the page live.
    refetchInterval: (query) => {
      const s = query.state.data?.status;
      return s === "pending" || s === "partial" || s === "confirming" ? 10_000 : false;
    },
  });
}

export function useTransfers(q: TransferQuery) {
  return useQuery({
    queryKey: adminKeys.transfers.list(q),
    queryFn: ({ signal }) => adminApi.listTransfers(q, { signal }),
    placeholderData: keepPreviousData,
  });
}

export function useMerchants(q: MerchantQuery) {
  return useQuery({
    queryKey: adminKeys.merchants.list(q),
    queryFn: ({ signal }) => adminApi.listMerchants(q, { signal }),
    placeholderData: keepPreviousData,
  });
}

/** Every merchant (for filters / selects): first 100, sorted by the API. */
export function useAllMerchants() {
  return useMerchants({ page: 1, page_size: 100 });
}

export function useMerchant(id: string) {
  return useQuery({
    queryKey: adminKeys.merchants.detail(id),
    queryFn: ({ signal }) => adminApi.getMerchant(id, { signal }),
  });
}

export function useChains(options: { refetchInterval?: number | false } = {}) {
  return useQuery({
    queryKey: adminKeys.chains,
    queryFn: ({ signal }) => adminApi.listChains({ signal }),
    select: (r) => r.items,
    refetchInterval: options.refetchInterval ?? 30_000,
  });
}

export function useAssets() {
  return useQuery({
    queryKey: adminKeys.assets,
    queryFn: ({ signal }) => adminApi.listAssets({ signal }),
    select: (r) => r.items,
  });
}

export function useWallets() {
  return useQuery({
    queryKey: adminKeys.wallets,
    queryFn: ({ signal }) => adminApi.listWallets({ signal }),
    select: (r) => r.items,
  });
}

export function useWebhookEvents(q: WebhookEventQuery) {
  return useQuery({
    queryKey: adminKeys.webhooks.list(q),
    queryFn: ({ signal }) => adminApi.listWebhookEvents(q, { signal }),
    placeholderData: keepPreviousData,
  });
}

export function useSettings() {
  return useQuery({
    queryKey: adminKeys.settings,
    queryFn: ({ signal }) => adminApi.getSettings({ signal }),
    // A form is being edited: never swap the values underneath the operator.
    refetchOnWindowFocus: false,
  });
}

export function useUsers(enabled = true) {
  return useQuery({
    queryKey: adminKeys.users,
    queryFn: ({ signal }) => adminApi.listUsers({ signal }),
    select: (r) => r.items,
    enabled,
  });
}

export function useAuditLogs(q: AuditLogQuery) {
  return useQuery({
    queryKey: adminKeys.auditLogs.list(q),
    queryFn: ({ signal }) => adminApi.listAuditLogs(q, { signal }),
    placeholderData: keepPreviousData,
  });
}

/** The caller's own sessions (`GET /auth/sessions`). */
export function useSessions() {
  return useQuery({
    queryKey: adminKeys.sessions,
    queryFn: ({ signal }) => adminApi.listSessions({ signal }),
    select: (r) => r.items,
  });
}

export function useEmailStatus() {
  return useQuery({
    queryKey: adminKeys.email.status,
    queryFn: ({ signal }) => adminApi.getEmailStatus({ signal }),
    staleTime: 5 * 60_000,
  });
}

export function useEmailOutbox(q: EmailOutboxQuery, enabled = true) {
  return useQuery({
    queryKey: adminKeys.email.outbox(q),
    queryFn: ({ signal }) => adminApi.listEmailOutbox(q, { signal }),
    placeholderData: keepPreviousData,
    // Pending messages go out within seconds: keep the list live while any are queued.
    refetchInterval: (query) => (query.state.data?.items.some((m) => m.status === "pending") ? 10_000 : false),
    enabled,
  });
}

/** One outbox message with its text body (expanded row). */
export function useEmailMessage(id: string) {
  return useQuery({
    queryKey: adminKeys.email.message(id),
    queryFn: ({ signal }) => adminApi.getEmailMessage(id, { signal }),
  });
}

// ---- writes ----------------------------------------------------------------

/**
 * Optimistic update of one item in an `{ items }` list (switches: chain /
 * asset enabled). The change shows at once, rolls back on error, and the list
 * is refetched afterwards either way.
 */
export function useOptimisticItemMutation<TItem extends { id: string }, TVars>(opts: {
  queryKey: QueryKey;
  id: (vars: TVars) => string;
  apply: (item: TItem, vars: TVars) => TItem;
  mutationFn: (vars: TVars) => Promise<TItem>;
  successMessage?: (item: TItem, vars: TVars) => string;
  invalidate?: readonly QueryKey[];
}) {
  const qc = useQueryClient();
  const t = useT();
  type List = { items: TItem[] };
  return useMutation<TItem, unknown, TVars, { prev: List | undefined }>({
    mutationFn: opts.mutationFn,
    onMutate: async (vars) => {
      await qc.cancelQueries({ queryKey: opts.queryKey });
      const prev = qc.getQueryData<List>(opts.queryKey);
      const id = opts.id(vars);
      qc.setQueryData<List>(opts.queryKey, (old) =>
        old ? { ...old, items: old.items.map((x) => (x.id === id ? opts.apply(x, vars) : x)) } : old,
      );
      return { prev };
    },
    onError: (error, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(opts.queryKey, ctx.prev);
      toast.error(describeError(t, error));
    },
    onSuccess: (item, vars) => {
      qc.setQueryData<List>(opts.queryKey, (old) =>
        old ? { ...old, items: old.items.map((x) => (x.id === item.id ? item : x)) } : old,
      );
      const msg = opts.successMessage?.(item, vars);
      if (msg) toast.success(msg);
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: opts.queryKey });
      void qc.invalidateQueries({ queryKey: adminKeys.auditLogs.all });
      for (const k of opts.invalidate ?? []) void qc.invalidateQueries({ queryKey: k });
    },
  });
}

export interface AdminMutationOptions<TData, TVars> {
  mutationFn: (vars: TVars) => Promise<TData>;
  /** Query keys (prefixes) to invalidate after success. */
  invalidate?: readonly QueryKey[] | ((data: TData, vars: TVars) => readonly QueryKey[]);
  /** Toast on success (same verb as the control that triggered it). */
  successMessage?: string | ((data: TData, vars: TVars) => string);
  /**
   * Toast the error (default true). Forms that map `ApiError.fields` onto
   * inputs still get a short toast unless they pass false.
   */
  toastError?: boolean | ((error: unknown) => boolean);
  onSuccess?: (data: TData, vars: TVars) => void | Promise<void>;
  onError?: (error: unknown, vars: TVars) => void;
  /** A 401 from this call is not a lost session (see query-client). */
  skipAuthRedirect?: boolean;
}

/**
 * `useMutation` + the admin conventions: success / error toasts, cache
 * invalidation of the affected resources and the audit log (every mutation
 * writes an audit entry), 401 → login (handled globally by the query client).
 */
export function useAdminMutation<TData = unknown, TVars = void>(opts: AdminMutationOptions<TData, TVars>) {
  const qc = useQueryClient();
  const t = useT();
  return useMutation<TData, unknown, TVars>({
    mutationFn: opts.mutationFn,
    meta: opts.skipAuthRedirect ? { skipAuthRedirect: true } : undefined,
    onSuccess: async (data, vars) => {
      const keys = typeof opts.invalidate === "function" ? opts.invalidate(data, vars) : (opts.invalidate ?? []);
      await Promise.all([
        ...keys.map((queryKey) => qc.invalidateQueries({ queryKey })),
        qc.invalidateQueries({ queryKey: adminKeys.auditLogs.all }),
      ]);
      const msg = typeof opts.successMessage === "function" ? opts.successMessage(data, vars) : opts.successMessage;
      if (msg) toast.success(msg);
      await opts.onSuccess?.(data, vars);
    },
    onError: (error, vars) => {
      const show = typeof opts.toastError === "function" ? opts.toastError(error) : (opts.toastError ?? true);
      if (show) toast.error(describeError(t, error));
      opts.onError?.(error, vars);
    },
  });
}
