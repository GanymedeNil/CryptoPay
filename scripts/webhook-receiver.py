#!/usr/bin/env python3
"""Local webhook receiver for manual testing (stdlib only).

Prints every CryptoPay webhook it receives and checks its signature:

    WEBHOOK_SECRET=whsec_... python3 scripts/webhook-receiver.py [port]

Point the merchant's webhook URL at http://host.docker.internal:<port>/hook
when the gateway runs in docker compose (the default port is 9009).
Signature = hex(HMAC-SHA256(secret, timestamp + "." + raw body)).
"""

import hashlib
import hmac
import json
import os
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = os.environ.get("WEBHOOK_SECRET", "")
TOLERANCE = 300  # seconds
seen = set()


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        ts = self.headers.get("X-CryptoPay-Timestamp", "")
        sig = self.headers.get("X-CryptoPay-Signature", "")
        delivery = self.headers.get("X-CryptoPay-Delivery", "")

        if not SECRET:
            verdict = "NOT CHECKED (WEBHOOK_SECRET unset)"
        else:
            want = hmac.new(SECRET.encode(), ts.encode() + b"." + body, hashlib.sha256).hexdigest()
            fresh = ts.isdigit() and abs(time.time() - int(ts)) <= TOLERANCE
            verdict = "OK" if hmac.compare_digest(want, sig.lower()) and fresh else "INVALID"

        try:
            event = json.loads(body)
            inv = event.get("data", {})
            quote = inv.get("quote") or {}
            summary = (
                f"{event.get('type')}  invoice={inv.get('id')}  status={inv.get('status')}"
                f"  paid={quote.get('amount_confirmed')}/{quote.get('amount')} {quote.get('symbol', '')}"
            )
        except ValueError:
            summary = "unparseable body"

        dup = "  (duplicate delivery)" if delivery in seen else ""
        seen.add(delivery)
        print(f"[{time.strftime('%H:%M:%S')}] signature {verdict}  {summary}{dup}", flush=True)

        # Answer 2xx only for a valid signature so a bad secret shows up as retries.
        self.send_response(200 if verdict != "INVALID" else 401)
        self.end_headers()

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 9009
    print(f"listening on :{port}  (merchant webhook URL: http://host.docker.internal:{port}/hook)", flush=True)
    HTTPServer(("0.0.0.0", port), Handler).serve_forever()
