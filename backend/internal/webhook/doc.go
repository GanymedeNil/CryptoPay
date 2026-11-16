// Package webhook delivers the webhook outbox to merchant endpoints.
//
// A [Dispatcher] claims due rows of the outbox (store.ClaimDueWebhooks, safe
// with several gateway instances), POSTs each stored payload byte-for-byte to
// the URL stored on the event and records the outcome. 2xx means delivered;
// anything else (non-2xx, timeout, transport error, redirect, blocked target)
// is a failed attempt rescheduled with exponential backoff
// min(2^attempt x 15s, 6h) plus up to 10% jitter, until the
// webhook_max_attempts setting is exhausted. Delivery is at-least-once:
// merchants must de-duplicate on the delivery id.
//
// # Request
//
//	POST <merchant url>
//	Content-Type: application/json
//	User-Agent: CryptoPay-Webhook/1
//	X-CryptoPay-Event: invoice.paid
//	X-CryptoPay-Delivery: <event id, stable across retries>
//	X-CryptoPay-Timestamp: <unix seconds at send time>
//	X-CryptoPay-Signature: hex(HMAC-SHA256(secret, timestamp + "." + body))
//
// The body is {"id","type","created_at","data"} (see [BuildEnvelope]).
//
// # Verifying a delivery (merchant side)
//
// Compute the HMAC over the timestamp header, a dot and the raw request
// body (not a re-serialised one), compare in constant time and reject old
// timestamps to prevent replays:
//
//	func handler(w http.ResponseWriter, r *http.Request) {
//		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
//		ts, _ := strconv.ParseInt(r.Header.Get("X-CryptoPay-Timestamp"), 10, 64)
//
//		mac := hmac.New(sha256.New, []byte(webhookSecret))
//		fmt.Fprintf(mac, "%d.", ts)
//		mac.Write(body)
//		want := hex.EncodeToString(mac.Sum(nil))
//
//		got := r.Header.Get("X-CryptoPay-Signature")
//		if !hmac.Equal([]byte(want), []byte(got)) ||
//			time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
//			http.Error(w, "bad signature", http.StatusUnauthorized)
//			return
//		}
//		// Process once per X-CryptoPay-Delivery, then:
//		w.WriteHeader(http.StatusNoContent) // any 2xx acknowledges
//	}
//
// Go services of this repository can call auth.Verify instead.
//
// # Behaviour decisions
//
//   - Disabled or deleted merchant, or one with an empty webhook secret: the
//     event is failed permanently on its first attempt (status "failed" with
//     an explanatory last_error) instead of being retried forever. An admin
//     can re-queue it with store.RequeueWebhook once fixed.
//   - The merchant's current secret is read at send time, so a rotation
//     applies to pending retries; the URL is the one stored on the event.
//   - Redirects are never followed; 3xx is a failure. Only http/https URLs
//     are accepted. Unless [AllowPrivateTargets] is set, loopback, private,
//     link-local, CGNAT, multicast and unspecified addresses are refused at
//     dial time (DNS-rebinding safe). HTTP proxies from the environment are
//     ignored.
//   - Response bodies are read up to 4 KiB and stored as a diagnostic snippet.
//   - On context cancellation in-flight requests are aborted and not
//     recorded; their lease expires and the event is retried.
package webhook
