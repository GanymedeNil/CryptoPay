package invoice_test

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/rates"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestQuoteMath(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	quote := func(amount, currency, assetID string) domain.Quote {
		t.Helper()
		_, q := e.Quoted(amount, currency, assetID)
		return q
	}

	// Stablecoin pegged 1:1 to USD: no provider call at all.
	q := quote("10", "USD", "usdt-ethereum")
	assert.Equal(t, "1", q.Rate.String())
	assert.Equal(t, "10000000", q.AmountDue.String())
	assert.Equal(t, "0", q.AmountReceived.String())
	assert.Equal(t, 0, e.Rates.Calls())
	// Rounded UP to the quote precision (2 for USDT).
	q = quote("10.001", "USD", "usdt-ethereum")
	assert.Equal(t, "10010000", q.AmountDue.String())

	// Fiat → coin at the provider's rate: 10 / 2000 = 0.005 ETH.
	q = quote("10", "USD", "eth-ethereum")
	assert.Equal(t, "2000", q.Rate.String())
	assert.Equal(t, "5000000000000000", q.AmountDue.String())
	// Non-terminating division, rounded up to 6 places: 10 / 3000.33 = 0.0033329… → 0.003333.
	e.Rates.Set("ethereum", "usd", dec("3000.33"))
	q = quote("10", "USD", "eth-ethereum")
	assert.Equal(t, "3333000000000000", q.AmountDue.String())
	// Bitcoin, 8 places: 99.99 / 50000 = 0.0019998.
	q = quote("99.99", "USD", "btc-bitcoin")
	assert.Equal(t, "199980", q.AmountDue.String())

	// Another fiat currency uses that currency's rate: 10 EUR / 0.92 = 10.8695… → 10.87 USDT.
	q = quote("10", "EUR", "usdt-ethereum")
	assert.Equal(t, "0.92", q.Rate.String())
	assert.Equal(t, "10870000", q.AmountDue.String())
	q = quote("92", "EUR", "eth-ethereum")
	assert.Equal(t, "1840", q.Rate.String())
	assert.Equal(t, "50000000000000000", q.AmountDue.String())

	// Same symbol as the price currency: rate 1 regardless of peg and provider.
	e.Settings(func(s *domain.Settings) { s.StablecoinPeg = false })
	e.Rates.Set("tether", "usd", dec("0.999"))
	q = quote("12.34", "USDT", "usdt-polygon")
	assert.Equal(t, "1", q.Rate.String())
	assert.Equal(t, "12340000", q.AmountDue.String())
	q = quote("0.015", "ETH", "eth-ethereum")
	assert.Equal(t, "1", q.Rate.String())
	assert.Equal(t, "15000000000000000", q.AmountDue.String())

	// Peg off: the stablecoin's market rate applies: 10 / 0.999 = 10.01001 → 10.02.
	q = quote("10", "USD", "usdt-ethereum")
	assert.Equal(t, "0.999", q.Rate.String())
	assert.Equal(t, "10020000", q.AmountDue.String())
	e.Settings(func(s *domain.Settings) { s.StablecoinPeg = true })

	// Priced in one crypto, paid in another: ratio of the USD prices.
	// 0.01 ETH at 3000.33 USD paid in pegged USDT = 30.0033 → 30.01 USDT.
	q = quote("0.01", "ETH", "usdt-ethereum")
	assert.Equal(t, "30010000", q.AmountDue.String())
	assert.True(t, q.Rate.Equal(dec("1").DivRound(dec("3000.33"), 18)), q.Rate.String())
	// 100 USDT paid in BTC: 100 / 50000 = 0.002 BTC.
	q = quote("100", "USDT", "btc-bitcoin")
	assert.Equal(t, "50000", q.Rate.String())
	assert.Equal(t, "200000", q.AmountDue.String())

	// Markup (bps) is added to the crypto amount: 10 × 1.01 = 10.10; 10/2000 × 1.01.
	e.Rates.Set("ethereum", "usd", dec("2000"))
	e.Settings(func(s *domain.Settings) { s.RateMarkupBps = 100 })
	q = quote("10", "USD", "usdt-ethereum")
	assert.Equal(t, "10100000", q.AmountDue.String())
	q = quote("10", "USD", "eth-ethereum")
	assert.Equal(t, "5050000000000000", q.AmountDue.String())
	assert.Equal(t, "2000", q.Rate.String(), "the stored rate is the market rate")

	// A tiny price never quotes zero: at least one precision step.
	e.Settings(func(s *domain.Settings) { s.RateMarkupBps = 0 })
	q = quote("0.000001", "USD", "btc-bitcoin")
	assert.Equal(t, "1", q.AmountDue.String())
}

func TestQuoteNoRate(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v := e.Create("10", "USD")

	e.Rates.SetError(rates.ErrUnavailable)
	_, err := e.Invoices.SelectAsset(e.Ctx, v.ID, "eth-ethereum")
	assert.ErrorIs(t, err, invoice.ErrRateUnavailable)
	// The peg and same-symbol invoices do not depend on the provider.
	_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-ethereum")
	assert.NoError(t, err)

	e.Rates.SetError(nil)
	// No price for this pair (TRX has no EUR rate in the fixture).
	eur := e.Create("10", "EUR")
	_, err = e.Invoices.SelectAsset(e.Ctx, eur.ID, "trx-tron")
	assert.ErrorIs(t, err, invoice.ErrRateUnavailable)
	// A zero / negative rate is never used.
	e.Rates.Set("solana", "usd", dec("0"))
	_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, "sol-solana")
	assert.ErrorIs(t, err, invoice.ErrRateUnavailable)

	quotes, err := e.Store.ListQuotesByInvoice(e.Ctx, eur.ID)
	require.NoError(t, err)
	assert.Empty(t, quotes, "a failed selection leaves nothing behind")
	// The failed switch did not disturb the active quote.
	cur, err := e.Store.GetActiveQuote(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, "usdt-ethereum", cur.AssetID)
}

func TestXpubAddressAllocation(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	eth, err := e.Store.GetChain(e.Ctx, "ethereum")
	require.NoError(t, err)

	inv1, q1 := e.Quoted("10", "USD", "usdt-ethereum")
	inv2, q2 := e.Quoted("10", "USD", "usdt-ethereum")

	// The Hardhat mnemonic's first two accounts.
	assert.Equal(t, "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", q1.Address)
	assert.Equal(t, "0x70997970C51812dc3A010C7d01b50e0d17dc79C8", q2.Address)
	for i, q := range []domain.Quote{q1, q2} {
		require.NotNil(t, q.AddressIndex)
		assert.Equal(t, uint32(i), *q.AddressIndex)
		want, err := hdwallet.Derive(eth, e.EVMWallet.Xpub, uint32(i))
		require.NoError(t, err)
		assert.Equal(t, want, q.Address)
		assert.Equal(t, domain.WalletXpub, q.WalletKind)
		assert.Equal(t, e.EVMWallet.ID, q.WalletID)
	}
	// Same amount on xpub wallets is no problem: the address tells them apart.
	assert.Equal(t, q1.AmountDue.String(), q2.AmountDue.String())

	// Other assets / chains of the same wallet reuse the invoice's address.
	for _, assetID := range []string{"usdt-polygon", "eth-ethereum", "usdc-polygon"} {
		q := e.Select(inv1.ID, assetID)
		assert.Equal(t, q1.Address, q.Address, assetID)
		assert.Equal(t, uint32(0), *q.AddressIndex, assetID)
	}
	q := e.Select(inv2.ID, "usdc-ethereum")
	assert.Equal(t, q2.Address, q.Address)
	w, err := e.Store.GetWallet(e.Ctx, e.EVMWallet.ID)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), w.NextIndex, "one index per invoice")

	// A different wallet (Bitcoin, BIP-84) has its own index space and format.
	qb := e.Select(inv1.ID, "btc-bitcoin")
	assert.Equal(t, uint32(0), *qb.AddressIndex)
	btc, err := e.Store.GetChain(e.Ctx, "bitcoin")
	require.NoError(t, err)
	want, err := hdwallet.Derive(btc, e.BitcoinWallet.Xpub, 0)
	require.NoError(t, err)
	assert.Equal(t, want, qb.Address)
	assert.Contains(t, qb.Address, "bc1q")
}

func TestConcurrentInvoicesGetDistinctAddresses(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	const n = 24
	addrs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			v, _, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "10", Currency: "USD", AssetID: "usdt-ethereum"})
			if assert.NoError(t, err) && assert.NotNil(t, v.Quote) {
				addrs[i] = v.Quote.Address
			}
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, a := range addrs {
		require.NotEmpty(t, a)
		assert.False(t, seen[a], "address %s handed out twice", a)
		seen[a] = true
	}
	w, err := e.Store.GetWallet(e.Ctx, e.EVMWallet.ID)
	require.NoError(t, err)
	assert.Equal(t, uint32(n), w.NextIndex)
}

func TestStaticAmountStep(t *testing.T) {
	t.Parallel()
	step := func(decimals, precision int) string {
		return invoice.StaticAmountStep(domain.Asset{Decimals: decimals, QuotePrecision: precision}).String()
	}
	assert.Equal(t, "100", step(6, 2))              // two digits below the quote precision
	assert.Equal(t, "100000000000000", step(18, 2)) // 10^-4 of an 18-decimals token
	assert.Equal(t, "1", step(6, 4))                // exactly two spare digits
	assert.Equal(t, "1", step(6, 5))                // one spare digit: the last digit
	assert.Equal(t, "1", step(8, 8))                // none: the last digit
	assert.Equal(t, "1000", step(9, 4))             // SOL
	assert.Equal(t, "1", step(0, 0))
}

func TestStaticWalletUniqueAmounts(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	var quotes []domain.Quote
	var invs []store.InvoiceView
	for range 5 {
		v, q := e.Quoted("10", "USD", "usdt-tron")
		invs, quotes = append(invs, v), append(quotes, q)
	}
	for i, q := range quotes {
		assert.Equal(t, e.TronWallet.Address, q.Address)
		assert.Equal(t, domain.WalletStatic, q.WalletKind)
		assert.Nil(t, q.AddressIndex)
		// 10.0000, 10.0001, 10.0002 … : upward only, in steps of 10^-4.
		assert.Equal(t, big.NewInt(10_000_000+int64(i)*100).String(), q.AmountDue.String())
	}
	// Another asset on the same address has its own amount space.
	_, trx := e.Quoted("10", "USD", "trx-tron")
	assert.Equal(t, "40000000", trx.AmountDue.String())
	// A different price does not collide.
	_, other := e.Quoted("10.01", "USD", "usdt-tron")
	assert.Equal(t, "10010000", other.AmountDue.String())

	// Once a quote stops being live its amount is free again: the next
	// invoice takes the lowest free amount.
	_, err := e.Store.SetQuoteStatus(e.Ctx, quotes[1].ID, domain.QuoteClosed)
	require.NoError(t, err)
	_, again := e.Quoted("10", "USD", "usdt-tron")
	assert.Equal(t, "10000100", again.AmountDue.String())

	// Switching away and back re-activates the original amount.
	e.Select(invs[2].ID, "trx-tron")
	back := e.Select(invs[2].ID, "usdt-tron")
	assert.Equal(t, quotes[2].ID, back.ID)
	assert.Equal(t, quotes[2].AmountDue.String(), back.AmountDue.String())

	// Solana (static only), 9 decimals / precision 4: steps of 10^-6 SOL.
	_, s1 := e.Quoted("15", "USD", "sol-solana")
	_, s2 := e.Quoted("15", "USD", "sol-solana")
	assert.Equal(t, "100000000", s1.AmountDue.String())
	assert.Equal(t, "100001000", s2.AmountDue.String())
}

func TestStaticWalletUniqueAmountsUnderConcurrency(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	const n = 16
	invs := make([]store.InvoiceView, n)
	for i := range n {
		invs[i] = e.Create("10", "USD")
	}
	amounts := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			q, err := e.Invoices.SelectAsset(e.Ctx, invs[i].ID, "usdt-tron")
			if assert.NoError(t, err) {
				amounts[i] = q.AmountDue.String()
			}
		})
	}
	wg.Wait()
	// Every invoice got its own amount: exactly the n lowest steps from 10 USDT up.
	want := map[string]bool{}
	for i := range n {
		want[big.NewInt(10_000_000+int64(i)*100).String()] = true
	}
	got := map[string]bool{}
	for _, a := range amounts {
		assert.False(t, got[a], "amount %s handed out twice", a)
		got[a] = true
	}
	assert.Equal(t, want, got)
}

func TestStaticWalletAmountSpaceExhausted(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	// Occupy the 200 amounts a 10 USDT quote may be bumped to.
	holder := storetest.Invoice(t, e.Store, e.Merchant.ID)
	for i := range 200 {
		other := storetest.Invoice(t, e.Store, e.Merchant.ID)
		storetest.Quote(t, e.Store, other, "usdt-tron", e.TronWallet, func(q *domain.Quote) {
			q.AmountDue = big.NewInt(10_000_000 + int64(i)*100)
		})
	}
	_ = holder
	v, first := e.Quoted("10", "USD", "trx-tron")
	_, err := e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-tron")
	assert.ErrorIs(t, err, invoice.ErrNoUniqueAmount)
	// The failed attempt rolled back completely: the previous quote is still active.
	cur, err := e.Store.GetActiveQuote(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, cur.ID)
	quotes, err := e.Store.ListQuotesByInvoice(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Len(t, quotes, 1)
}

func TestAssetSwitching(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, a := e.Quoted("10", "USD", "eth-ethereum")
	require.Equal(t, domain.QuoteActive, a.Status)

	// Selecting the active asset again returns the quote unchanged, without a rate lookup.
	calls := e.Rates.Calls()
	e.Rates.Set("ethereum", "usd", dec("4000"))
	same := e.Select(v.ID, "eth-ethereum")
	assert.Equal(t, a.ID, same.ID)
	assert.Equal(t, a.AmountDue.String(), same.AmountDue.String())
	assert.Equal(t, a.UpdatedAt, same.UpdatedAt)
	assert.Equal(t, calls, e.Rates.Calls())

	// Switching supersedes the old quote (still watched) and creates a new one.
	b := e.Select(v.ID, "usdt-polygon")
	assert.NotEqual(t, a.ID, b.ID)
	assert.Equal(t, domain.QuoteActive, b.Status)
	assert.Equal(t, domain.QuoteSuperseded, e.Quote(a.ID).Status)
	watched, err := e.Store.ListWatched(e.Ctx, "ethereum", e.Clock.Now())
	require.NoError(t, err)
	require.Len(t, watched, 1)
	assert.Equal(t, a.ID, watched[0].ID)

	// Switching back re-activates the original quote: original amount and
	// address, although the rate has changed meanwhile.
	back := e.Select(v.ID, "eth-ethereum")
	assert.Equal(t, a.ID, back.ID)
	assert.Equal(t, domain.QuoteActive, back.Status)
	assert.Equal(t, "5000000000000000", back.AmountDue.String())
	assert.Equal(t, "2000", back.Rate.String())
	assert.Equal(t, a.Address, back.Address)
	assert.Equal(t, domain.QuoteSuperseded, e.Quote(b.ID).Status)

	c := e.Select(v.ID, "btc-bitcoin")
	quotes, err := e.Store.ListQuotesByInvoice(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Len(t, quotes, 3)
	active := 0
	for _, q := range quotes {
		if q.Status == domain.QuoteActive {
			active++
			assert.Equal(t, c.ID, q.ID)
		}
	}
	assert.Equal(t, 1, active)
	cur, err := e.Invoices.GetView(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, c.ID, cur.Quote.ID)
	assert.Empty(t, e.EventTypes(v.ID), "selecting assets is not a status change")
}

func TestSwitchingRefusedAfterFunds(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, a := e.Quoted("10", "USD", "usdt-ethereum")
	b := e.Select(v.ID, "usdt-polygon")

	// Funds arrive on the superseded quote (the customer paid the first one).
	e.Credit(a, 4_000_000, domain.TransferPending)
	require.Equal(t, domain.InvoicePartial, e.Invoice(v.ID).Status)

	for _, assetID := range []string{"usdt-ethereum", "eth-ethereum", "btc-bitcoin"} {
		_, err := e.Invoices.SelectAsset(e.Ctx, v.ID, assetID)
		assert.ErrorIs(t, err, invoice.ErrFundsDetected, assetID)
	}
	// The active asset may still be "selected": unchanged.
	same, err := e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-polygon")
	require.NoError(t, err)
	assert.Equal(t, b.ID, same.ID)
	assert.Equal(t, domain.QuoteActive, e.Quote(b.ID).Status)
	assert.Equal(t, domain.QuoteSuperseded, e.Quote(a.ID).Status)
}

func TestSelectAssetNotAllowed(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	_, err := e.Invoices.SelectAsset(e.Ctx, uuid.New(), "usdt-ethereum")
	assert.ErrorIs(t, err, invoice.ErrNotFound)

	v := e.Create("10", "USD")
	for _, assetID := range []string{"", "nope", "usdc-base", "usdc-sepolia"} {
		_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, assetID)
		assert.ErrorIs(t, err, invoice.ErrAssetUnavailable, assetID)
	}
	// Asset disabled by the admin.
	_, err = e.Store.UpdateAsset(e.Ctx, "usdt-polygon", store.AssetUpdate{Enabled: ptr(false)})
	require.NoError(t, err)
	_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-polygon")
	assert.ErrorIs(t, err, invoice.ErrAssetUnavailable)

	// One second before expiry it still works; at expires_at it does not,
	// even though the expiry job has not run yet (status still pending).
	e.Clock.Set(v.ExpiresAt.Add(-time.Second))
	_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-ethereum")
	require.NoError(t, err)
	e.Clock.Set(v.ExpiresAt)
	_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, "usdt-ethereum")
	assert.ErrorIs(t, err, invoice.ErrNotOpen)
	_, err = e.Invoices.SelectAsset(e.Ctx, v.ID, "eth-ethereum")
	assert.ErrorIs(t, err, invoice.ErrNotOpen)

	// Closed invoices.
	e.Clock.Set(invoicetest.Start)
	c := e.Create("10", "USD")
	_, err = e.Invoices.Cancel(e.Ctx, c.ID, "test")
	require.NoError(t, err)
	_, err = e.Invoices.SelectAsset(e.Ctx, c.ID, "usdt-ethereum")
	assert.ErrorIs(t, err, invoice.ErrNotOpen)
	p, q := e.Quoted("10", "USD", "usdt-ethereum")
	e.Credit(q, 10_000_000, domain.TransferConfirmed)
	require.Equal(t, domain.InvoicePaid, e.Invoice(p.ID).Status)
	_, err = e.Invoices.SelectAsset(e.Ctx, p.ID, "usdt-ethereum")
	assert.ErrorIs(t, err, invoice.ErrNotOpen)
}

func TestConcurrentSelectAsset(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	for _, tc := range []struct {
		name   string
		assets []string
	}{
		{"double submit of one asset", []string{"usdt-tron"}},
		{"two assets racing", []string{"usdt-ethereum", "usdt-tron"}},
		{"three assets racing", []string{"eth-ethereum", "usdt-polygon", "sol-solana"}},
	} {
		v := e.Create("10", "USD")
		const n = 12
		got := make([]domain.Quote, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				q, err := e.Invoices.SelectAsset(e.Ctx, v.ID, tc.assets[i%len(tc.assets)])
				if assert.NoError(t, err, tc.name) {
					got[i] = q
				}
			})
		}
		wg.Wait()
		quotes, err := e.Store.ListQuotesByInvoice(e.Ctx, v.ID)
		require.NoError(t, err)
		assert.Len(t, quotes, len(tc.assets), "%s: one quote per asset, no duplicates", tc.name)
		active, perAsset := 0, map[string]uuid.UUID{}
		for _, q := range quotes {
			_, dup := perAsset[q.AssetID]
			assert.False(t, dup, tc.name)
			perAsset[q.AssetID] = q.ID
			switch q.Status {
			case domain.QuoteActive:
				active++
			case domain.QuoteSuperseded:
			default:
				t.Errorf("%s: unexpected quote status %s", tc.name, q.Status)
			}
		}
		assert.Equal(t, 1, active, "%s: exactly one active quote", tc.name)
		for i, q := range got {
			assert.Equal(t, perAsset[tc.assets[i%len(tc.assets)]], q.ID, "%s: every caller got the invoice's quote for its asset", tc.name)
		}
	}
}

func TestEstimates(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v := e.Create("10", "USD")
	est, err := e.Invoices.Estimates(e.Ctx, v.Invoice)
	require.NoError(t, err)
	payable, err := e.Store.ListPayableAssets(e.Ctx)
	require.NoError(t, err)
	require.Len(t, est, len(payable))
	byID := map[string]*big.Int{}
	for i, x := range est {
		assert.Equal(t, payable[i].ID, x.Asset.ID, "catalog order")
		byID[x.Asset.ID] = x.Amount
	}
	assert.NotContains(t, byID, "usdc-base", "chain without wallet is not payable")
	assert.Equal(t, "10000000", byID["usdt-tron"].String())
	assert.Equal(t, "10000000", byID["usdc-polygon"].String())
	assert.Equal(t, "5000000000000000", byID["eth-ethereum"].String())
	assert.Equal(t, "20000", byID["btc-bitcoin"].String())
	assert.Equal(t, "40000000", byID["trx-tron"].String())
	assert.Equal(t, "66700000", byID["sol-solana"].String()) // 10/150 = 0.0666… → 0.0667 SOL

	// Estimates create nothing.
	quotes, err := e.Store.ListQuotesByInvoice(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Empty(t, quotes)
	w, err := e.Store.GetWallet(e.Ctx, e.EVMWallet.ID)
	require.NoError(t, err)
	assert.Zero(t, w.NextIndex)

	// Provider down: null for everything that needs a rate, pegged stablecoins stay.
	e.Rates.SetError(rates.ErrUnavailable)
	est, err = e.Invoices.Estimates(e.Ctx, v.Invoice)
	require.NoError(t, err)
	for _, x := range est {
		if x.Asset.IsStablecoin {
			require.NotNil(t, x.Amount, x.Asset.ID)
			assert.Equal(t, "10", invoicetestFormat(x.Amount, x.Asset.Decimals), x.Asset.ID)
		} else {
			assert.Nil(t, x.Amount, x.Asset.ID)
		}
	}
	// A crypto-priced invoice: the same symbol needs no rate either.
	e.Rates.SetError(nil)
	c := e.Create("0.5", "ETH")
	est, err = e.Invoices.Estimates(e.Ctx, c.Invoice)
	require.NoError(t, err)
	for _, x := range est {
		switch x.Asset.ID {
		case "eth-ethereum":
			assert.Equal(t, "500000000000000000", x.Amount.String())
		case "usdt-tron":
			assert.Equal(t, "1000000000", x.Amount.String()) // 0.5 × 2000
		}
	}
}

func invoicetestFormat(v *big.Int, decimals int) string {
	return decimal.NewFromBigInt(v, -int32(decimals)).String()
}
