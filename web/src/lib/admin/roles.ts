import type { AdminRole } from "@/lib/api/types";

/** owner > admin > viewer (docs/api.md §3). */
export const ROLE_RANK: Record<AdminRole, number> = { viewer: 0, admin: 1, owner: 2 };

export const ROLES: readonly AdminRole[] = ["owner", "admin", "viewer"];

/** Viewers get 403 on every non-GET except logout / own password. */
export function canWrite(role: AdminRole | undefined): boolean {
  return role === "admin" || role === "owner";
}

/** User management is owner-only (GET included). */
export function isOwner(role: AdminRole | undefined): boolean {
  return role === "owner";
}

export function atLeast(role: AdminRole | undefined, min: AdminRole): boolean {
  return role !== undefined && ROLE_RANK[role] >= ROLE_RANK[min];
}
