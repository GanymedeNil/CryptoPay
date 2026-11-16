import { FlaskConicalIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { AssetInfo } from "@/lib/api/types";
import { tokenStandard } from "@/lib/checkout/payment";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

type NetworkInfo = Pick<AssetInfo, "chain_id" | "chain_name" | "family" | "contract_address" | "is_testnet">;

/** "Tron · TRC-20" — the network, plus the token standard for tokens. */
export function networkLabel(asset: NetworkInfo): string {
  const std = tokenStandard(asset);
  return std ? `${asset.chain_name} · ${std}` : asset.chain_name;
}

/** Network name with the token standard de-emphasised. */
export function NetworkName({ asset, className }: { asset: NetworkInfo; className?: string }) {
  const std = tokenStandard(asset);
  return (
    <span className={className}>
      {asset.chain_name}
      {std && (
        <>
          {" "}
          <span className="font-normal text-muted-foreground">· {std}</span>
        </>
      )}
    </span>
  );
}

export function NetworkBadge({ asset, className }: { asset: NetworkInfo; className?: string }) {
  return (
    <Badge variant="outline" className={cn("h-6 rounded-md bg-background/60 px-2 font-medium", className)}>
      {networkLabel(asset)}
    </Badge>
  );
}

export function TestnetBadge({ className }: { className?: string }) {
  const t = useT();
  return (
    <Badge
      variant="outline"
      className={cn("h-5 rounded-md border-warning-border bg-warning-surface px-1.5 text-warning", className)}
    >
      <FlaskConicalIcon aria-hidden />
      {t("common.testnet")}
    </Badge>
  );
}
