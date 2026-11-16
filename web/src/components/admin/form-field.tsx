"use client";

import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * Label + control + help text + error, wired for screen readers: give the
 * control `id={htmlFor}`, `aria-invalid` and `aria-describedby={describedBy(htmlFor, …)}`.
 */
export function FormField({
  label,
  htmlFor,
  description,
  error,
  optional,
  children,
  className,
  orientation,
}: {
  label: React.ReactNode;
  htmlFor: string;
  description?: React.ReactNode;
  error?: string;
  optional?: boolean;
  children: React.ReactNode;
  className?: string;
  orientation?: "vertical" | "horizontal";
}) {
  const t = useT();
  return (
    <Field data-invalid={!!error} className={cn("gap-1.5", className)} orientation={orientation}>
      <FieldLabel htmlFor={htmlFor} className="gap-1.5">
        {label}
        {optional && <span className="text-xs font-normal text-muted-foreground">({t("admin.common.optional")})</span>}
      </FieldLabel>
      {children}
      {description && <FieldDescription id={`${htmlFor}-desc`} className="text-xs">{description}</FieldDescription>}
      {error && <FieldError id={`${htmlFor}-err`}>{error}</FieldError>}
    </Field>
  );
}

/** `aria-describedby` for a control rendered inside `FormField`. */
export function describedBy(id: string, opts: { description?: boolean; error?: unknown }): string | undefined {
  const ids = [opts.description ? `${id}-desc` : null, opts.error ? `${id}-err` : null].filter(Boolean);
  return ids.length ? ids.join(" ") : undefined;
}

/** Form-level error (API errors that don't belong to a field). */
export function FormError({ message }: { message?: string | null }) {
  if (!message) return null;
  return (
    <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
      {message}
    </p>
  );
}
