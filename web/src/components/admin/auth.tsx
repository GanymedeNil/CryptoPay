"use client";

import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { createContext, useContext, useEffect, useMemo, useRef } from "react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { adminKeys, useMe } from "@/lib/admin/queries";
import { UNAUTHORIZED_EVENT } from "@/lib/admin/query-client";
import { canWrite, isOwner } from "@/lib/admin/roles";
import { isApiError } from "@/lib/api/client";
import type { AdminUser } from "@/lib/api/types";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { ErrorState } from "./states";

export const LOGIN_PATH = "/admin/login";

/** Where to send someone after login: only console paths, never the login page itself. */
export function safeNextPath(next: string | null | undefined): string {
  if (!next || !next.startsWith("/admin") || next.startsWith("//")) return "/admin";
  if (next === LOGIN_PATH || next.startsWith(`${LOGIN_PATH}?`) || next.startsWith(`${LOGIN_PATH}/`)) return "/admin";
  return next;
}

export function loginUrl(next: string, reason?: "expired" | "signedOut"): string {
  const p = new URLSearchParams();
  if (next && next !== "/admin") p.set("next", next);
  if (reason) p.set("reason", reason);
  const s = p.toString();
  return s ? `${LOGIN_PATH}?${s}` : LOGIN_PATH;
}

function currentPath(): string {
  if (typeof window === "undefined") return "/admin";
  return `${window.location.pathname}${window.location.search}`;
}

interface AuthValue {
  user: AdminUser;
  canWrite: boolean;
  isOwner: boolean;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ user, children }: { user: AdminUser; children: React.ReactNode }) {
  const value = useMemo<AuthValue>(
    () => ({ user, canWrite: canWrite(user.role), isOwner: isOwner(user.role) }),
    [user],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside the authenticated admin layout");
  return ctx;
}

export function useCanWrite(): boolean {
  return useAuth().canWrite;
}

/**
 * Loads `GET /auth/me` and renders the console only for a signed-in user.
 * 401 (now or from any later request) → `/admin/login?next=<this page>`.
 */
export function AuthGate({ children, fallback }: { children: React.ReactNode; fallback: React.ReactNode }) {
  const me = useMe();
  const router = useRouter();
  const qc = useQueryClient();
  const redirected = useRef(false);

  useEffect(() => {
    const go = () => {
      if (redirected.current) return;
      redirected.current = true;
      // "Session expired" only if there was a session; a first visit just asks to sign in.
      router.replace(loginUrl(currentPath(), qc.getQueryData(adminKeys.me) ? "expired" : undefined));
      // Drop cached data of the expired session (keeps nothing readable after logout).
      qc.removeQueries({ queryKey: adminKeys.all, predicate: (q) => q.queryKey[1] !== "me" });
    };
    window.addEventListener(UNAUTHORIZED_EVENT, go);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, go);
  }, [router, qc]);

  const unauthorized = isApiError(me.error) && me.error.isUnauthorized;
  useEffect(() => {
    if (unauthorized && !redirected.current) {
      redirected.current = true;
      router.replace(loginUrl(currentPath(), me.data ? "expired" : undefined));
    }
  }, [unauthorized, router, me.data]);

  if (me.data && !unauthorized) return <AuthProvider user={me.data}>{children}</AuthProvider>;
  if (me.error && !unauthorized) {
    return (
      <div className="mx-auto flex min-h-dvh max-w-md items-center px-4">
        <ErrorState error={me.error} onRetry={() => void me.refetch()} className="w-full" />
      </div>
    );
  }
  return <>{fallback}</>;
}

/** Tooltip on a control disabled for role reasons (disabled buttons get no pointer events: wrap them). */
export function RoleTooltip({
  blocked,
  reason,
  children,
  className,
}: {
  blocked: boolean;
  reason: string;
  children: React.ReactNode;
  className?: string;
}) {
  if (!blocked) return <>{children}</>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          tabIndex={0}
          aria-label={reason}
          className={cn("inline-flex cursor-not-allowed rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-ring/50", className)}
        >
          {children}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-60">{reason}</TooltipContent>
    </Tooltip>
  );
}

/**
 * A button for a mutation. For viewers (or non-owners with `ownerOnly`) it is
 * disabled and explains why in a tooltip.
 */
export function GatedButton({
  ownerOnly = false,
  disabledReason,
  disabled,
  ...props
}: React.ComponentProps<typeof Button> & {
  ownerOnly?: boolean;
  /** Another reason the action is unavailable (shown as tooltip). */
  disabledReason?: string | null;
}) {
  const t = useT();
  const auth = useAuth();
  const roleBlocked = ownerOnly ? !auth.isOwner : !auth.canWrite;
  const reason = roleBlocked
    ? ownerOnly
      ? t("admin.access.ownerOnly")
      : t("admin.access.readOnly")
    : (disabledReason ?? "");
  const blocked = roleBlocked || Boolean(disabledReason);
  return (
    <RoleTooltip blocked={blocked} reason={reason}>
      <Button {...props} disabled={disabled || blocked} />
    </RoleTooltip>
  );
}
