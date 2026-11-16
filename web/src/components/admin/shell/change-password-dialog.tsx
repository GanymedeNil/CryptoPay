"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useId } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { applyFieldErrors } from "@/lib/admin/errors";
import { useAdminMutation } from "@/lib/admin/queries";
import { adminApi } from "@/lib/api/admin";
import { isApiError } from "@/lib/api/client";
import { useT } from "@/lib/i18n";
import type { TranslateFn } from "@/lib/i18n/types";

function schema(t: TranslateFn) {
  return z
    .object({
      current_password: z.string().min(1, t("admin.password.required")),
      new_password: z.string().min(8, t("admin.password.tooShort")),
      confirm: z.string().min(1, t("admin.password.required")),
    })
    .refine((v) => v.new_password === v.confirm, { path: ["confirm"], message: t("admin.password.mismatch") })
    .refine((v) => v.new_password !== v.current_password, {
      path: ["new_password"],
      message: t("admin.password.sameAsCurrent"),
    });
}

type Values = z.infer<ReturnType<typeof schema>>;

/** `POST /auth/password` (own password; allowed for every role). */
export function ChangePasswordDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <ChangePasswordForm onDone={() => onOpenChange(false)} />}
    </Dialog>
  );
}

function ChangePasswordForm({ onDone }: { onDone: () => void }) {
  const t = useT();
  const ids = { cur: useId(), next: useId(), confirm: useId() };
  const form = useForm<Values>({
    resolver: zodResolver(schema(t)),
    defaultValues: { current_password: "", new_password: "", confirm: "" },
  });
  const mutation = useAdminMutation({
    mutationFn: (v: Values) => adminApi.changePassword({ current_password: v.current_password, new_password: v.new_password }),
    successMessage: t("admin.password.changed"),
    skipAuthRedirect: true,
    // A wrong current password is reported on the field, not as a toast.
    toastError: (e) => !(isApiError(e) && (e.isUnauthorized || e.fields)),
    onSuccess: () => onDone(),
    onError: (e) => {
      if (isApiError(e) && e.isUnauthorized && !e.fields) {
        form.setError("current_password", { type: "server", message: t("admin.password.wrongCurrent") }, { shouldFocus: true });
        return;
      }
      applyFieldErrors(e, form.setError, ["current_password", "new_password"]);
    },
  });
  const errors = form.formState.errors;
  return (
    <DialogContent className="sm:max-w-md">
      <form onSubmit={form.handleSubmit((v) => mutation.mutate(v))} noValidate className="grid gap-4">
        <DialogHeader>
          <DialogTitle>{t("admin.password.title")}</DialogTitle>
          <DialogDescription>{t("admin.password.description")}</DialogDescription>
        </DialogHeader>
        <FieldGroup className="gap-4">
          <Field data-invalid={!!errors.current_password}>
            <FieldLabel htmlFor={ids.cur}>{t("admin.password.current")}</FieldLabel>
            <Input
              id={ids.cur}
              type="password"
              autoComplete="current-password"
              aria-invalid={!!errors.current_password}
              {...form.register("current_password")}
            />
            <FieldError errors={[errors.current_password]} />
          </Field>
          <Field data-invalid={!!errors.new_password}>
            <FieldLabel htmlFor={ids.next}>{t("admin.password.next")}</FieldLabel>
            <Input
              id={ids.next}
              type="password"
              autoComplete="new-password"
              aria-invalid={!!errors.new_password}
              {...form.register("new_password")}
            />
            <FieldDescription>{t("admin.password.nextHint")}</FieldDescription>
            <FieldError errors={[errors.new_password]} />
          </Field>
          <Field data-invalid={!!errors.confirm}>
            <FieldLabel htmlFor={ids.confirm}>{t("admin.password.confirm")}</FieldLabel>
            <Input
              id={ids.confirm}
              type="password"
              autoComplete="new-password"
              aria-invalid={!!errors.confirm}
              {...form.register("confirm")}
            />
            <FieldError errors={[errors.confirm]} />
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onDone}>
            {t("admin.common.cancel")}
          </Button>
          <Button type="submit" disabled={mutation.isPending}>
            {mutation.isPending && <Spinner data-icon="inline-start" />}
            {mutation.isPending ? t("admin.password.submitting") : t("admin.password.submit")}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}
