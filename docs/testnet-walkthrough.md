# Testnet walkthrough

Take one invoice from creation to a delivered webhook on real test networks (Sepolia, Tron Nile, Bitcoin testnet3).
No real funds are involved at any point.

## 0. Prerequisites

- The stack is up: `make up`, and `http://localhost:3000/healthz` returns 200.
- `make seed-dev` has been run. It enables three test chains and gives them **test-only** default wallets:

  | Chain | Default wallet | Confirmations needed | Roughly how long |
  |---|---|---|---|
  | `sepolia` | TEST ONLY - Hardhat (EVM) | 3 | about 1 minute |
  | `tron-nile` | TEST ONLY - Hardhat (Tron) | 19 | about 1 minute |
  | `bitcoin-testnet` | TEST ONLY - Hardhat (Bitcoin testnet) | 1 | 10 minutes to several hours (testnet3 block times are erratic) |

  These wallets are derived from the **public** Hardhat mnemonic, so anyone can move the coins out of them
  (the chains often have bots that sweep them automatically). That does not affect payment detection, but do not send
  more than the test needs.
- The gateway only starts scanning from the moment a chain is enabled and does not look back at history:
  a transfer sent before the chain was enabled will not be found.

## 1. Prepare a paying wallet and test coins

Pay from an **ordinary wallet account**. Native coins sent by a contract on EVM and Tron (for example a multisig wallet,
or a faucet contract paying straight to the receiving address) are not detected.

| Network | Wallet | Where to get test coins |
|---|---|---|
| Ethereum Sepolia | MetaMask (Settings → Advanced → Show test networks) | ETH: any Sepolia ETH faucet (Google Cloud Web3 faucet, QuickNode faucet, …); USDC: `https://faucet.circle.com`, choose the **Ethereum Sepolia** network; PYUSD: Google Cloud Web3 faucet, choose PYUSD · Sepolia |
| Tron Nile | TronLink (switch to the Nile testnet) | `https://nileex.io/join/getJoinPage` for TRX and USDT |
| Bitcoin testnet3 | UniSat or OKX Wallet, network **Testnet3** | any testnet3 faucet |

Notes:

- **The USDC contract must be** `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238` (Circle's official Sepolia USDC),
  **the Sepolia PYUSD contract must be** `0xCaC524BcA292aaade2DF8A05cC58F0a65B1B3bB9` (Paxos's official test token), and
  **the Nile USDT contract must be** `TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf`. Look-alike test tokens from other contracts are not recognized.
  If your faucet gave you a different contract, you can add it in the console under Assets → Add token.
- Sending USDC on Sepolia needs a little ETH for gas; sending USDT on Nile needs TRX for energy (the checkout sets a feeLimit of 100 TRX).
- The default Bitcoin RPC is **testnet3**. Coins on testnet4 are not detected.

## 2. Check the console

1. Open `http://localhost:3000/admin` and sign in with `ADMIN_EMAIL` / `ADMIN_PASSWORD` from `.env`.
2. Chains & RPC: confirm that `sepolia`, `tron-nile` and `bitcoin-testnet` are enabled, that the default wallet is a `TEST ONLY - …` wallet,
   that "Behind by" is close to 0, and that there is no recent error. The free public RPCs rate-limit now and then;
   a short error that clears by itself is fine.

## 3. Start the local webhook receiver

1. In the console open Merchants → `Demo Shop (dev)` and click "Rotate secret" in the Webhook section. Copy the new signing secret (it is shown only once).
2. Set the Webhook URL to `http://host.docker.internal:9009/hook` and save.
3. In the project root run:

   ```bash
   WEBHOOK_SECRET=whsec_the_secret_you_just_copied python3 scripts/webhook-receiver.py
   ```

   For every notification it receives it prints the event type, the invoice status, the confirmed amount, and whether the signature verified.

## 4. Create an invoice

Either way works:

- **Console**: Invoices → Create invoice, merchant `Demo Shop (dev)`, amount `1`, currency `USD`.
- **Merchant API**: first create a key on the merchant page (API keys → Create key) to get an `sk_live_…`, then:

  ```bash
  curl -s localhost:3000/api/v1/invoices \
    -H "Authorization: Bearer sk_live_your_key" \
    -H 'Content-Type: application/json' \
    -d '{"amount":"1","currency":"USD","order_id":"walkthrough-1","title":"Testnet walkthrough"}'
  ```

  Sending the same `order_id` again returns the same invoice (HTTP 200); changing the amount returns 409.

Open the returned `checkout_url` (shaped like `http://localhost:3000/pay/<id>`).

## 5. Payment scenarios

Use a fresh invoice for each scenario. After paying, **do not refresh the checkout**: the page updates itself every 3 seconds.

### A. Sepolia USDC, browser wallet

1. On the checkout choose **USDC · Ethereum Sepolia**. The amount due is `1` (stablecoins are priced 1:1).
2. "Pay with wallet" tab → choose MetaMask → connect. The page asks the wallet to switch to Sepolia; confirm the transaction.
3. Expected:
   - within about ten seconds the page shows "Payment detected" with `n of 3 confirmations`;
   - after about a minute it turns to "Payment complete"; if a success_url is set, the browser redirects to it a few seconds later;
   - the receiver prints `invoice.confirming` and then `invoice.paid`, with signature `OK`.

### A2. Sepolia PYUSD

Same as A, but pick **PYUSD · Ethereum Sepolia** on the checkout. PYUSD is a standard ERC-20, so the flow and the expected result are the same as for USDC.

### B. Sepolia ETH, phone QR code

1. Choose **ETH · Ethereum Sepolia**. The amount is converted at the live CoinGecko rate.
2. In MetaMask mobile (also switched to Sepolia) scan the "Payment request" QR code; the amount and network are filled in for you.
   If the wallet does not understand the code, switch to "Address only" and type the amount shown on the page by hand.
3. Expected: the same as A.

### C. Tron Nile USDT, TronLink

1. Choose **USDT · Tron Nile**.
2. "Pay with wallet" → TronLink. Make sure TronLink is on the Nile network, otherwise the page reports a network mismatch.
3. Expected: 19 confirmations, then "Payment complete" after about a minute.

### D. Partial payment, then top up

1. On a new invoice choose Sepolia USDC and send only `0.5` first.
2. Expected: the checkout shows "Partial payment received" with the amount received and the remainder, and the receiver prints `invoice.payment_detected`.
3. Send the remaining `0.5`: the invoice goes to confirming and then paid (`invoice.confirming` → `invoice.paid`).

### E. Overpayment

Send `1.5` USDC: the invoice is paid, the console flags it "Overpaid", the checkout tells the payer they sent more than the amount due,
and the flag is visible on the invoice detail page.

### F. Expiry and grace period

1. Create a short-lived invoice through the merchant API by adding `"expires_in": 120` to the request body.
2. Pick a coin on the checkout, do not pay, and wait 2 minutes: the checkout shows "This payment has expired", the console status is "Expired",
   and the receiver prints `invoice.expired`.
3. Pay the full amount within the 24-hour grace period after expiry: the invoice still becomes Paid, and the console flags it "Paid late" (`paid_late`).

### G. A merchant's own wallet

`Shop B` has its own wallet on Sepolia (`Shop B EVM`, also a public test key).
Create a 1 USD invoice for `Shop B` and pick Sepolia USDC: the receiving address differs from the one on a Demo Shop invoice, and each is credited independently.
Its webhook URL has to be set separately.

### H. Bitcoin testnet3 (optional)

Choose **BTC · Bitcoin Testnet** and pay with "Pay with wallet" in UniSat or OKX, or scan the QR code.
One confirmation completes the payment, but testnet3 block times are very uneven, so it can take a long while.

## 6. Check it in the console

- **Invoice detail**: quotes (address, amount due / received / confirmed, rate), on-chain transfers (confirmations, block explorer links),
  and webhook deliveries (payload, status code, with a manual retry).
- **Transfers**: every transfer that was detected; "Unmatched" transfers can be assigned to an invoice by hand.
- **Webhook deliveries**: failed deliveries are retried with exponential backoff, and can also be retried by hand.
- **Overview**: amount received, invoice counts, conversion, breakdown by coin.
- **Audit log**: every console action you just took is recorded.

## 7. Troubleshooting

| Symptom | What to check |
|---|---|
| Nothing happens a few minutes after paying | Chains & RPC: the chain's last error and how far it is behind; that the paying wallet is on the right network; that the token contract is the one listed in section 1; `docker compose logs -f backend` |
| Stuck on "Payment detected" | Normal: wait for enough confirmations. It is slower when a free RPC is rate-limiting |
| The amount received does not match | Look at the transfer amount on the invoice detail page. Less than the amount due is treated as a partial payment; a shortfall within 0.5% counts as paid (adjustable under Settings → Underpayment tolerance) |
| The receiver got no notification | Webhook deliveries: the "Last error" column; the receiver must run on port 9009 of this machine; a signature `INVALID` means the secret is wrong |
| A coin is missing from the checkout | The chain is not enabled, has no wallet, or the coin is disabled |

When you are done, you can disable the test chains you no longer need under Chains & RPC.
