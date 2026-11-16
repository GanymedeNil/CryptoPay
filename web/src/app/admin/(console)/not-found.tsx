"use client";

import { SearchXIcon } from "lucide-react";
import Link from "next/link";
import { EmptyState } from "@/components/admin/states";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n";

export default function AdminNotFound() {
  const t = useT();
  return (
    <EmptyState
      icon={SearchXIcon}
      title={t("admin.errors.notFoundTitle")}
      className="rounded-xl border bg-card"
      action={
        <Button asChild variant="outline">
          <Link href="/admin">{t("admin.nav.overview")}</Link>
        </Button>
      }
    />
  );
}
