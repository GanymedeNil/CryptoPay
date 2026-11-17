"use client";

import { useRef } from "react";
import { Input } from "@/components/ui/input";
import { normalizeOtp, OTP_LENGTH } from "@/lib/admin/two-factor";
import { cn } from "@/lib/utils";

/**
 * One field for a 6-digit one-time code: numeric keypad on phones,
 * `autocomplete="one-time-code"` (SMS / password-manager autofill), pasting
 * "123 456" or "123-456" works, and `onComplete` fires once the sixth digit is
 * in (auto-submit) — again only after the value changed.
 */
export function OtpInput({
  id,
  value,
  onChange,
  onComplete,
  disabled,
  invalid,
  describedBy,
  autoFocus,
  className,
  label,
}: {
  id: string;
  value: string;
  onChange: (digits: string) => void;
  onComplete?: (code: string) => void;
  disabled?: boolean;
  invalid?: boolean;
  describedBy?: string;
  autoFocus?: boolean;
  className?: string;
  /** Accessible name when there is no visible <label for>. */
  label?: string;
}) {
  const lastSubmitted = useRef<string | null>(null);
  const update = (raw: string) => {
    const digits = normalizeOtp(raw);
    onChange(digits);
    if (digits.length < OTP_LENGTH) {
      lastSubmitted.current = null;
      return;
    }
    if (digits !== lastSubmitted.current) {
      lastSubmitted.current = digits;
      onComplete?.(digits);
    }
  };
  return (
    <Input
      id={id}
      value={value}
      onChange={(e) => update(e.target.value)}
      inputMode="numeric"
      autoComplete="one-time-code"
      pattern="[0-9]*"
      // No maxLength: a pasted "Code: 123-456" would be cut before it is normalised to 6 digits.
      spellCheck={false}
      autoFocus={autoFocus}
      disabled={disabled}
      aria-invalid={invalid || undefined}
      aria-describedby={describedBy}
      aria-label={label}
      placeholder="000000"
      className={cn("h-11 text-center font-mono text-xl tracking-[0.5em] tabular placeholder:tracking-[0.5em] placeholder:text-muted-foreground/40", className)}
    />
  );
}
