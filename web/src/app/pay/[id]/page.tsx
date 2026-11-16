import type { Metadata } from "next";
import { Suspense } from "react";
import { CheckoutPage, CheckoutShell } from "@/components/checkout/checkout-page";

export const metadata: Metadata = {
  title: "Checkout",
  robots: { index: false, follow: false },
  // The invoice id in the URL is a bearer secret.
  referrer: "no-referrer",
};

async function CheckoutRoute({ params }: { params: PageProps<"/pay/[id]">["params"] }) {
  const { id } = await params;
  return <CheckoutPage invoiceId={id} />;
}

/** Everything on this page is client data; the shell renders instantly. */
export default function PayPage({ params }: PageProps<"/pay/[id]">) {
  return (
    <Suspense fallback={<CheckoutShell />}>
      <CheckoutRoute params={params} />
    </Suspense>
  );
}
