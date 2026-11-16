"use client";

import { useSyncExternalStore } from "react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useT } from "@/lib/i18n";
import { CodeBlock } from "../code-block";
import { Section } from "../page-header";

const noop = () => () => {};

export function createInvoiceCurl(origin: string): string {
  return `curl -X POST ${origin}/api/v1/invoices \\
  -H "Authorization: Bearer sk_live_…" \\
  -H "Content-Type: application/json" \\
  -d '{
    "amount": "99.00",
    "currency": "USD",
    "order_id": "10482",
    "title": "Order #10482",
    "success_url": "https://shop.example.com/orders/10482",
    "cancel_url": "https://shop.example.com/cart"
  }'
# → 201 Invoice: redirect the customer to "checkout_url"`;
}

export const VERIFY_NODE = `import crypto from "node:crypto";

// rawBody: the request body exactly as received (string/Buffer), before JSON.parse.
export function verifyCryptoPay(rawBody, headers, secret) {
  const timestamp = headers["x-cryptopay-timestamp"];
  const signature = headers["x-cryptopay-signature"] ?? "";
  // Reject stale deliveries (replay protection): 5 minutes.
  if (Math.abs(Date.now() / 1000 - Number(timestamp)) > 300) return false;
  const expected = crypto
    .createHmac("sha256", secret)
    .update(\`\${timestamp}.\${rawBody}\`)
    .digest("hex");
  return (
    signature.length === expected.length &&
    crypto.timingSafeEqual(Buffer.from(signature), Buffer.from(expected))
  );
}

// Headers: X-CryptoPay-Event, X-CryptoPay-Delivery (event id, dedupe on it),
// X-CryptoPay-Timestamp, X-CryptoPay-Signature. Respond 2xx when processed.`;

export const VERIFY_PYTHON = `import hashlib, hmac, time

def verify_cryptopay(raw_body: bytes, headers, secret: str) -> bool:
    timestamp = headers.get("X-CryptoPay-Timestamp", "")
    signature = headers.get("X-CryptoPay-Signature", "")
    if abs(time.time() - int(timestamp or 0)) > 300:  # replay window
        return False
    expected = hmac.new(
        secret.encode(), timestamp.encode() + b"." + raw_body, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(signature, expected)`;

/** How a merchant backend creates an invoice and checks webhook signatures. */
export function IntegrationSnippet() {
  const t = useT();
  // The browser talks to this origin, which proxies /api/* to the gateway.
  const origin = useSyncExternalStore(noop, () => window.location.origin, () => "https://pay.example.com");
  return (
    <Section title={t("admin.merchant.integration.title")} description={t("admin.merchant.integration.description")}>
      <Tabs defaultValue="create">
        <TabsList className="h-auto! max-w-full flex-wrap justify-start">
          <TabsTrigger value="create">{t("admin.merchant.integration.createInvoice")}</TabsTrigger>
          <TabsTrigger value="node">{t("admin.merchant.integration.verify")} · Node.js</TabsTrigger>
          <TabsTrigger value="python">{t("admin.merchant.integration.verify")} · Python</TabsTrigger>
        </TabsList>
        <TabsContent value="create" className="mt-2">
          <CodeBlock code={createInvoiceCurl(origin)} label={t("admin.merchant.integration.createInvoice")} />
        </TabsContent>
        <TabsContent value="node" className="mt-2 space-y-2">
          <CodeBlock code={VERIFY_NODE} label={t("admin.merchant.integration.verify")} />
          <p className="text-xs text-muted-foreground">{t("admin.merchant.integration.verifyNote")}</p>
        </TabsContent>
        <TabsContent value="python" className="mt-2 space-y-2">
          <CodeBlock code={VERIFY_PYTHON} label={t("admin.merchant.integration.verify")} />
          <p className="text-xs text-muted-foreground">{t("admin.merchant.integration.verifyNote")}</p>
        </TabsContent>
      </Tabs>
    </Section>
  );
}
