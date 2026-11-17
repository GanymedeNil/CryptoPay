import { Guilloche } from "@/components/brand/guilloche";
import { cn } from "@/lib/utils";

/**
 * The payment slip: a stub (who / what / how much) printed over a guilloché
 * security band, a perforated tear line, then the body for the current step.
 */
export function Slip({
  stub,
  children,
  className,
}: {
  stub?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <article
      className={cn(
        "relative w-full rounded-2xl border bg-card text-card-foreground",
        "shadow-[0_1px_0_0_var(--border),0_28px_56px_-36px_rgb(19_32_29/0.45)] dark:shadow-[0_28px_56px_-36px_rgb(0_0_0/0.9)]",
        className,
      )}
    >
      {stub && (
        <>
          <div className="relative overflow-hidden rounded-t-2xl bg-slip-band">
            <Guilloche className="absolute inset-0 h-full w-full opacity-[0.22] dark:opacity-[0.16]" />
            <div className="relative px-5 pt-5 pb-6 max-[359px]:px-4 sm:px-7 sm:pt-6">{stub}</div>
          </div>
          <div className="slip-perforation" aria-hidden />
        </>
      )}
      <div className="px-5 pt-5 pb-6 max-[359px]:px-4 sm:px-7 sm:pt-6 sm:pb-7">{children}</div>
    </article>
  );
}

/**
 * Rubber "PAID" stamp, pressed onto the band when the invoice is paid. It
 * sits in the stub's right-hand slot (where the countdown was), so a long
 * merchant name wraps beside it instead of running underneath.
 */
export function PaidStamp({ label }: { label: string }) {
  return (
    <div className="pointer-events-none shrink-0" aria-hidden>
      <div className="animate-stamp rounded-md border-[2.5px] border-success px-2 py-0.5 font-heading text-base font-bold tracking-[0.14em] whitespace-nowrap text-success uppercase opacity-90 mix-blend-multiply sm:px-2.5 sm:py-1 sm:text-lg sm:tracking-[0.18em] dark:mix-blend-screen">
        {label}
      </div>
    </div>
  );
}
