"use client";

import { ArrowLeftIcon } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * Page title, one-line explanation and the page's primary actions. (The
 * browser tab title comes from each route's `generateMetadata`.)
 */
export function PageHeader({
  title,
  description,
  actions,
  back,
  meta,
  className,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  /** Link back to the parent list. */
  back?: { href: string; label: string };
  /** Badges / ids under the title. */
  meta?: React.ReactNode;
  className?: string;
}) {
  return (
    <header className={cn("mb-5 flex flex-col gap-3 sm:mb-6", className)}>
      {back && (
        <Button asChild variant="ghost" size="sm" className="-ml-2 w-fit text-muted-foreground">
          <Link href={back.href}>
            <ArrowLeftIcon data-icon="inline-start" aria-hidden />
            {back.label}
          </Link>
        </Button>
      )}
      <div className="flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
        <div className="min-w-0 space-y-1.5">
          <h1 className="font-heading text-2xl leading-tight font-semibold tracking-tight text-balance break-words">
            {title}
          </h1>
          {meta && <div className="flex flex-wrap items-center gap-2 text-sm">{meta}</div>}
          {description && <p className="max-w-3xl text-sm text-pretty text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </header>
  );
}

/** A titled card section used across detail and settings pages. */
export function Section({
  title,
  description,
  actions,
  children,
  className,
  bodyClassName,
  id,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
  bodyClassName?: string;
  id?: string;
}) {
  const headingId = id ? `${id}-title` : undefined;
  return (
    <section aria-labelledby={headingId} className={cn("min-w-0 rounded-xl border bg-card text-card-foreground", className)}>
      <div className="flex flex-wrap items-start justify-between gap-3 border-b px-4 py-3">
        <div className="min-w-0 space-y-0.5">
          <h2 id={headingId} className="font-heading text-[15px] font-semibold">
            {title}
          </h2>
          {description && <p className="text-xs text-pretty text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
      <div className={cn("p-4", bodyClassName)}>{children}</div>
    </section>
  );
}

/** Label / value pairs for detail pages. */
export function DetailList({
  items,
  className,
}: {
  items: { label: React.ReactNode; value: React.ReactNode; wide?: boolean; hidden?: boolean }[];
  className?: string;
}) {
  return (
    <dl className={cn("grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2", className)}>
      {items
        .filter((i) => !i.hidden)
        .map((item, i) => (
          <div key={i} className={cn("min-w-0 space-y-0.5", item.wide && "sm:col-span-2")}>
            <dt className="text-xs text-muted-foreground">{item.label}</dt>
            <dd className="min-w-0 text-sm">{item.value}</dd>
          </div>
        ))}
    </dl>
  );
}
