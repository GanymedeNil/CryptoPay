"use client";

import { useQueryClient } from "@tanstack/react-query";
import { EyeIcon, KeyRoundIcon, LogOutIcon } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { createContext, Fragment, useContext, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { BrandMark } from "@/components/brand/brand-mark";
import { LocaleSwitcher } from "@/components/locale-switcher";
import { ThemeToggle } from "@/components/theme-toggle";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Separator } from "@/components/ui/separator";
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import { Skeleton } from "@/components/ui/skeleton";
import { adminApi } from "@/lib/api/admin";
import { useT } from "@/lib/i18n";
import { AuthGate, loginUrl, useAuth } from "../auth";
import { RoleBadge } from "../status-badge";
import { ChangePasswordDialog } from "./change-password-dialog";
import { isActive, NAV, navItemFor } from "./nav";

// ---- breadcrumb label for detail pages --------------------------------------

const CrumbContext = createContext<{ setDetail: (label: string | null) => void } | null>(null);

/** Detail pages name the last breadcrumb (merchant name, short invoice id). */
export function useBreadcrumbDetail(label: string | null | undefined) {
  const ctx = useContext(CrumbContext);
  useEffect(() => {
    if (!ctx) return;
    ctx.setDetail(label ?? null);
    return () => ctx.setDetail(null);
  }, [ctx, label]);
}

// ---- shell ------------------------------------------------------------------

/** Authenticated console frame: sidebar, header (breadcrumb, language, theme, account). */
export function AdminShell({ children }: { children: React.ReactNode }) {
  return (
    <AuthGate fallback={<ShellSkeleton />}>
      <ShellFrame>{children}</ShellFrame>
    </AuthGate>
  );
}

function ShellFrame({ children }: { children: React.ReactNode }) {
  const t = useT();
  const { canWrite } = useAuth();
  const [detail, setDetail] = useState<string | null>(null);
  const crumbCtx = useMemo(() => ({ setDetail }), []);
  return (
    <CrumbContext.Provider value={crumbCtx}>
      <a
        href="#admin-main"
        className="sr-only z-50 rounded-md bg-primary px-3 py-2 text-primary-foreground focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        {t("admin.nav.skipToContent")}
      </a>
      <SidebarProvider>
        <AppSidebar />
        <SidebarInset className="min-w-0">
          <header className="sticky top-0 z-20 flex h-14 shrink-0 items-center gap-2 border-b bg-background/85 px-3 backdrop-blur supports-[backdrop-filter]:bg-background/70 sm:px-4">
            <SidebarTrigger className="-ml-1" aria-label={t("admin.nav.toggleSidebar")} />
            <Separator orientation="vertical" className="mr-1 hidden h-5! sm:block" />
            <Crumbs detail={detail} />
            <div className="ml-auto flex shrink-0 items-center gap-0.5">
              <LocaleSwitcher />
              <ThemeToggle />
              <UserMenu />
            </div>
          </header>
          {!canWrite && (
            <div className="flex items-center gap-2 border-b border-warning-border bg-warning-surface px-4 py-2 text-xs text-warning sm:text-sm" role="note">
              <EyeIcon className="size-4 shrink-0" aria-hidden />
              {t("admin.shell.readOnlyBanner")}
            </div>
          )}
          <div id="admin-main" tabIndex={-1} className="mx-auto w-full max-w-[90rem] flex-1 px-4 pt-5 pb-16 outline-none sm:px-6 sm:pt-6 lg:px-8">
            {children}
          </div>
        </SidebarInset>
      </SidebarProvider>
    </CrumbContext.Provider>
  );
}

function AppSidebar() {
  const t = useT();
  const pathname = usePathname();
  const { isOwner, canWrite } = useAuth();
  const { isMobile, setOpenMobile } = useSidebar();
  return (
    <Sidebar collapsible="icon" mobileTitle={t("admin.nav.mobileTitle")} mobileDescription={t("admin.nav.mobileDescription")}>
      <SidebarHeader className="h-14 justify-center border-b border-sidebar-border px-3 group-data-[collapsible=icon]:px-2">
        <Link href="/admin" className="flex min-w-0 items-center gap-2 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring" onClick={() => isMobile && setOpenMobile(false)}>
          <BrandMark label="" className="shrink-0" />
          <span className="flex min-w-0 flex-col leading-none group-data-[collapsible=icon]:hidden">
            <span className="font-heading text-[15px] font-semibold tracking-tight">CryptoPay</span>
            <span className="mt-0.5 text-[11px] text-muted-foreground">{t("admin.nav.console")}</span>
          </span>
        </Link>
      </SidebarHeader>
      <SidebarContent className="gap-0 py-1">
        {NAV.map((group, gi) => {
          const items = group.items.filter((i) => (!i.ownerOnly || isOwner) && (!i.writersOnly || canWrite));
          if (items.length === 0) return null;
          return (
            <SidebarGroup key={gi} className="py-1.5">
              {group.label && <SidebarGroupLabel>{t(group.label)}</SidebarGroupLabel>}
              <SidebarGroupContent>
                <SidebarMenu>
                  {items.map((item) => {
                    const active = isActive(pathname, item.href);
                    const label = t(item.label);
                    return (
                      <SidebarMenuItem key={item.href}>
                        <SidebarMenuButton asChild isActive={active} tooltip={label}>
                          <Link
                            href={item.href}
                            aria-current={active ? "page" : undefined}
                            onClick={() => isMobile && setOpenMobile(false)}
                          >
                            <item.icon aria-hidden />
                            <span>{label}</span>
                          </Link>
                        </SidebarMenuButton>
                      </SidebarMenuItem>
                    );
                  })}
                </SidebarMenu>
              </SidebarGroupContent>
            </SidebarGroup>
          );
        })}
      </SidebarContent>
    </Sidebar>
  );
}

function Crumbs({ detail }: { detail: string | null }) {
  const t = useT();
  const pathname = usePathname();
  const item = navItemFor(pathname);
  const isDetail = item && pathname !== item.href && item.href !== "/admin";
  const trail: { label: string; href?: string }[] = [];
  if (item && item.href !== "/admin") trail.push({ label: t(item.label), href: isDetail ? item.href : undefined });
  if (isDetail) trail.push({ label: detail ?? "…" });
  return (
    <Breadcrumb aria-label={t("admin.shell.breadcrumb")} className="min-w-0">
      <BreadcrumbList className="flex-nowrap">
        <BreadcrumbItem className={trail.length > 0 ? "hidden sm:inline-flex" : undefined}>
          {trail.length > 0 ? (
            <BreadcrumbLink asChild>
              <Link href="/admin">{t("admin.nav.overview")}</Link>
            </BreadcrumbLink>
          ) : (
            <BreadcrumbPage>{t("admin.nav.overview")}</BreadcrumbPage>
          )}
        </BreadcrumbItem>
        {trail.map((c, i) => (
          <Fragment key={i}>
            <BreadcrumbSeparator className={i === 0 ? "hidden sm:inline-flex" : undefined} />
            <BreadcrumbItem className="min-w-0">
              {c.href ? (
                <BreadcrumbLink asChild>
                  <Link href={c.href} className="truncate">
                    {c.label}
                  </Link>
                </BreadcrumbLink>
              ) : (
                <BreadcrumbPage className="max-w-[40vw] truncate sm:max-w-xs">{c.label}</BreadcrumbPage>
              )}
            </BreadcrumbItem>
          </Fragment>
        ))}
      </BreadcrumbList>
    </Breadcrumb>
  );
}

function UserMenu() {
  const t = useT();
  const { user } = useAuth();
  const router = useRouter();
  const qc = useQueryClient();
  const [pwOpen, setPwOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  async function signOut() {
    setBusy(true);
    try {
      await adminApi.logout();
    } catch (e) {
      // Already signed out server-side is fine; anything else: tell the operator.
      const status = (e as { status?: number }).status;
      if (status !== 401) {
        toast.error(t("admin.shell.signOutFailed"));
        setBusy(false);
        return;
      }
    }
    qc.clear();
    router.replace(loginUrl("/admin", "signedOut"));
  }

  const initial = user.email.slice(0, 1).toUpperCase();
  return (
    <>
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" className="h-8 gap-2 px-1.5 sm:px-2" aria-label={`${t("admin.shell.userMenu")}: ${user.email}`}>
            <span className="flex size-6 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground" aria-hidden>
              {initial}
            </span>
            <span className="hidden max-w-40 truncate text-sm lg:inline">{user.email}</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-64">
          <DropdownMenuLabel className="space-y-1.5 py-2">
            <span className="block text-xs font-normal text-muted-foreground">{t("admin.shell.signedInAs")}</span>
            <span className="block truncate text-sm font-medium">{user.email}</span>
            <RoleBadge role={user.role} />
          </DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => setPwOpen(true)}>
            <KeyRoundIcon aria-hidden />
            {t("admin.shell.changePassword")}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={busy}
            onSelect={(e) => {
              e.preventDefault();
              void signOut();
            }}
          >
            <LogOutIcon aria-hidden />
            {busy ? t("admin.shell.signingOut") : t("admin.shell.signOut")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ChangePasswordDialog open={pwOpen} onOpenChange={setPwOpen} />
    </>
  );
}

export function ShellSkeleton() {
  const t = useT();
  return (
    <div className="flex min-h-dvh w-full" aria-busy="true">
      <div className="hidden w-64 shrink-0 border-r bg-sidebar p-3 md:block">
        <Skeleton className="mb-6 h-8 w-32" />
        {Array.from({ length: 9 }, (_, i) => (
          <Skeleton key={i} className="mb-2 h-7 w-full" />
        ))}
      </div>
      <div className="flex-1">
        <div className="flex h-14 items-center border-b px-4">
          <Skeleton className="h-5 w-40" />
        </div>
        <div className="space-y-4 p-6">
          <p className="sr-only" role="status">
            {t("admin.shell.checkingSession")}
          </p>
          <Skeleton className="h-7 w-56" />
          <Skeleton className="h-28 w-full" />
          <Skeleton className="h-64 w-full" />
        </div>
      </div>
    </div>
  );
}
