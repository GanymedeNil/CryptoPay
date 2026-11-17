# Merchant integration

How a merchant's backend creates invoices and consumes webhooks.

## 1. Create an invoice

```bash
curl -X POST https://pay.example.com/api/v1/invoices \
  -H "Authorization: Bearer sk_live_xxx" \
  -H "Content-Type: application/json" \
  -d '{
        "amount": "25.00",
        "currency": "USD",
        "order_id": "ORDER-1001",
        "title": "Pro plan",
        "success_url": "https://shop.example/thanks",
        "cancel_url": "https://shop.example/cart",
        "metadata": {"customer_id": "42"}
      }'
```

- Returns `201` and an `Invoice`. Submitting the same `order_id` again with the same amount and currency returns `200` and the original invoice (idempotent); a different amount or currency returns `409`.
- `currency` can be a fiat code (USD, EUR, CNY…) or the symbol of an enabled asset (such as `USDT`).
- The optional `asset_id` (for example `usdt-tron`) pins the payment asset; `GET /api/v1/assets` lists the assets that can currently be paid.
- Amounts are always **strings** (`"25.00"`). Never use floating-point numbers.

## 2. Redirect the customer to `checkout_url`

`checkout_url` looks like `https://pay.example.com/pay/{invoice_id}`. The checkout lets the customer choose a coin and a network, shows the address, the amount, a QR code and wallet buttons, and follows the confirmation progress live. When the payment completes it redirects to `success_url`.

## 3. Receive and verify the webhook

The body is `{ "id", "type", "created_at", "data": Invoice }`. Event types: `invoice.payment_detected`, `invoice.confirming`, `invoice.paid`, `invoice.underpaid`, `invoice.expired`, `invoice.cancelled`. The signature is `hex(HMAC-SHA256(secret, timestamp + "." + raw request body))`:

```go
func cryptopayWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	ts, _ := strconv.ParseInt(r.Header.Get("X-CryptoPay-Timestamp"), 10, 64)

	mac := hmac.New(sha256.New, []byte(webhookSecret)) // whsec_...
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body) // the raw bytes; do not re-serialize
	want := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-CryptoPay-Signature"))) ||
		time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var ev struct {
		ID   string          `json:"id"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &ev)
	// Deduplicate on ev.ID (= X-CryptoPay-Delivery) before processing, then return any 2xx.
	w.WriteHeader(http.StatusNoContent)
}
```

- Delivery is **at-least-once**: a non-2xx response or a timeout is retried with the backoff `min(2^n × 15s, 6h)` up to the configured maximum number of attempts. The same event can arrive more than once, so you **must deduplicate on the event `id`**.
- Several events can be delivered in parallel and **their arrival order is not guaranteed**: trust `data.status` (and `data.updated_at`), and only ship the goods on `invoice.paid`.
- The admin console shows the status code and response of every delivery and lets you retry one by hand.

## 4. Other endpoints

`GET /api/v1/invoices/{id}`, `GET /api/v1/invoices?status=&order_id=&page=&page_size=`, `POST /api/v1/invoices/{id}/cancel` (only `pending` invoices can be cancelled). The full contract is in [`api.md`](api.md).
