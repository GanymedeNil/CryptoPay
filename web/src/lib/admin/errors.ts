import type { FieldPath, FieldValues, UseFormSetError } from "react-hook-form";
import { ApiError, isApiError } from "@/lib/api/client";
import type { TranslateFn } from "@/lib/i18n/types";

export type ErrorKind =
  | "network"
  | "unauthorized"
  | "forbidden"
  | "notFound"
  | "conflict"
  | "validation"
  | "rateLimited"
  | "server"
  | "unknown";

export function errorKind(error: unknown): ErrorKind {
  if (!isApiError(error)) return "unknown";
  if (error.isNetworkError) return "network";
  if (error.isUnauthorized) return "unauthorized";
  if (error.isForbidden) return "forbidden";
  if (error.isNotFound) return "notFound";
  if (error.isConflict) return "conflict";
  if (error.isRateLimited) return "rateLimited";
  if (error.status === 400 || error.status === 422) return "validation";
  if (error.status >= 500) return "server";
  return "unknown";
}

/**
 * One sentence for a toast or an error panel. Conflict / validation messages
 * come from the backend (they name the actual rule that was hit); everything
 * else gets a localised explanation of what happened and what to do.
 */
export function describeError(t: TranslateFn, error: unknown): string {
  const kind = errorKind(error);
  const serverMessage = isApiError(error) ? error.message : "";
  switch (kind) {
    case "network":
      return t("admin.errors.network");
    case "unauthorized":
      return t("admin.errors.sessionExpired");
    case "forbidden":
      return t("admin.errors.forbidden");
    case "notFound":
      return t("admin.errors.notFound");
    case "rateLimited":
      return t("admin.errors.rateLimited");
    case "server":
      return t("admin.errors.server");
    case "conflict":
      return serverMessage ? t("admin.errors.conflictWith", { message: serverMessage }) : t("admin.errors.conflict");
    case "validation":
      if (isApiError(error) && error.fields && Object.keys(error.fields).length > 0) {
        return t("admin.errors.fixFields");
      }
      return serverMessage ? t("admin.errors.invalidWith", { message: serverMessage }) : t("admin.errors.invalid");
    default:
      return error instanceof Error && error.message ? error.message : t("admin.errors.generic");
  }
}

/**
 * Maps `ApiError.fields` onto react-hook-form fields. `map` renames API
 * fields to form fields (e.g. `{ expires_in: "expiresMinutes" }`). Returns the
 * API fields that had no matching form field (show them in a form-level
 * error), or `null` when the error carried no field errors.
 */
export function applyFieldErrors<T extends FieldValues>(
  error: unknown,
  setError: UseFormSetError<T>,
  known: readonly string[],
  map: Record<string, string> = {},
): Record<string, string> | null {
  if (!(error instanceof ApiError) || !error.fields) return null;
  const unmatched: Record<string, string> = {};
  let first = true;
  for (const [apiField, message] of Object.entries(error.fields)) {
    const field = map[apiField] ?? apiField;
    if (known.includes(field)) {
      setError(field as FieldPath<T>, { type: "server", message }, { shouldFocus: first });
      first = false;
    } else {
      unmatched[apiField] = message;
    }
  }
  return unmatched;
}
