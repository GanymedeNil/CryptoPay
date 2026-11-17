import {
  ArrowLeftRightIcon,
  CoinsIcon,
  LayoutDashboardIcon,
  MailIcon,
  NetworkIcon,
  ReceiptTextIcon,
  ScrollTextIcon,
  SettingsIcon,
  ShieldCheckIcon,
  StoreIcon,
  UsersIcon,
  WalletIcon,
  WebhookIcon,
  type LucideIcon,
} from "lucide-react";
import type { MessageKey } from "@/lib/i18n";

export interface NavItem {
  href: string;
  label: MessageKey;
  icon: LucideIcon;
  ownerOnly?: boolean;
  /** Hidden for viewers (owner / admin only). */
  writersOnly?: boolean;
}

export interface NavGroup {
  label: MessageKey | null;
  items: NavItem[];
}

export const NAV: NavGroup[] = [
  {
    label: null,
    items: [{ href: "/admin", label: "admin.nav.overview", icon: LayoutDashboardIcon }],
  },
  {
    label: "admin.nav.groupPayments",
    items: [
      { href: "/admin/invoices", label: "admin.nav.invoices", icon: ReceiptTextIcon },
      { href: "/admin/transfers", label: "admin.nav.transfers", icon: ArrowLeftRightIcon },
      { href: "/admin/webhooks", label: "admin.nav.webhooks", icon: WebhookIcon },
    ],
  },
  {
    label: "admin.nav.groupConfig",
    items: [
      { href: "/admin/merchants", label: "admin.nav.merchants", icon: StoreIcon },
      { href: "/admin/chains", label: "admin.nav.chains", icon: NetworkIcon },
      { href: "/admin/assets", label: "admin.nav.assets", icon: CoinsIcon },
      { href: "/admin/wallets", label: "admin.nav.wallets", icon: WalletIcon },
      { href: "/admin/settings", label: "admin.nav.settings", icon: SettingsIcon },
    ],
  },
  {
    label: "admin.nav.groupAdmin",
    items: [
      { href: "/admin/security", label: "admin.nav.security", icon: ShieldCheckIcon },
      { href: "/admin/users", label: "admin.nav.users", icon: UsersIcon, ownerOnly: true },
      { href: "/admin/emails", label: "admin.nav.emails", icon: MailIcon, writersOnly: true },
      { href: "/admin/audit-logs", label: "admin.nav.auditLogs", icon: ScrollTextIcon },
    ],
  },
];

/** Active when the path is the item or below it ("/admin" only matches exactly). */
export function isActive(pathname: string, href: string): boolean {
  if (href === "/admin") return pathname === "/admin" || pathname === "/admin/";
  return pathname === href || pathname.startsWith(`${href}/`);
}

/** The nav item a path belongs to (for breadcrumbs). */
export function navItemFor(pathname: string): NavItem | undefined {
  for (const g of NAV) for (const i of g.items) if (i.href !== "/admin" && isActive(pathname, i.href)) return i;
  return isActive(pathname, "/admin") ? NAV[0].items[0] : undefined;
}
