package invoice_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/rates"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func ptr[T any](v T) *T { return &v }

func TestCreateValidation(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	ok := invoice.CreateParams{Amount: "10", Currency: "USD"}
	cases := []struct {
		name  string
		mod   func(*invoice.CreateParams)
		field string
	}{
		{"amount missing", func(p *invoice.CreateParams) { p.Amount = "" }, "amount"},
		{"amount zero", func(p *invoice.CreateParams) { p.Amount = "0.00" }, "amount"},
		{"amount negative", func(p *invoice.CreateParams) { p.Amount = "-5" }, "amount"},
		{"amount plus sign", func(p *invoice.CreateParams) { p.Amount = "+5" }, "amount"},
		{"amount exponent", func(p *invoice.CreateParams) { p.Amount = "1e3" }, "amount"},
		{"amount text", func(p *invoice.CreateParams) { p.Amount = "ten" }, "amount"},
		{"amount spaces", func(p *invoice.CreateParams) { p.Amount = " 10" }, "amount"},
		{"amount trailing dot", func(p *invoice.CreateParams) { p.Amount = "10." }, "amount"},
		{"amount leading dot", func(p *invoice.CreateParams) { p.Amount = ".5" }, "amount"},
		{"amount comma", func(p *invoice.CreateParams) { p.Amount = "1,000" }, "amount"},
		{"amount fiat precision", func(p *invoice.CreateParams) { p.Amount = "1.1234567" }, "amount"},
		{"amount too large", func(p *invoice.CreateParams) { p.Amount = "1000000000000.01" }, "amount"},
		{"amount absurdly long", func(p *invoice.CreateParams) { p.Amount = strings.Repeat("9", 80) }, "amount"},
		{"crypto precision beyond decimals", func(p *invoice.CreateParams) { p.Currency, p.Amount = "USDT", "1.1234567" }, "amount"},
		{"currency missing", func(p *invoice.CreateParams) { p.Currency = " " }, "currency"},
		{"currency unknown", func(p *invoice.CreateParams) { p.Currency = "XYZ" }, "currency"},
		{"order id too long", func(p *invoice.CreateParams) { p.OrderID = strings.Repeat("a", 129) }, "order_id"},
		{"title too long", func(p *invoice.CreateParams) { p.Title = strings.Repeat("a", 201) }, "title"},
		{"description too long", func(p *invoice.CreateParams) { p.Description = strings.Repeat("a", 2001) }, "description"},
		{"success url scheme", func(p *invoice.CreateParams) { p.SuccessURL = "javascript:alert(1)" }, "success_url"},
		{"success url relative", func(p *invoice.CreateParams) { p.SuccessURL = "/thanks" }, "success_url"},
		{"cancel url scheme", func(p *invoice.CreateParams) { p.CancelURL = "ftp://x.example/a" }, "cancel_url"},
		{"cancel url no host", func(p *invoice.CreateParams) { p.CancelURL = "https://" }, "cancel_url"},
		{"email", func(p *invoice.CreateParams) { p.CustomerEmail = "not-an-email" }, "customer_email"},
		{"email with name", func(p *invoice.CreateParams) { p.CustomerEmail = "Bob <bob@example.com>" }, "customer_email"},
		{"expires_in low", func(p *invoice.CreateParams) { p.ExpiresIn = ptr(59) }, "expires_in"},
		{"expires_in high", func(p *invoice.CreateParams) { p.ExpiresIn = ptr(86401) }, "expires_in"},
		{"metadata array", func(p *invoice.CreateParams) { p.Metadata = json.RawMessage(`[1]`) }, "metadata"},
		{"metadata broken", func(p *invoice.CreateParams) { p.Metadata = json.RawMessage(`{"a":`) }, "metadata"},
		{"metadata too big", func(p *invoice.CreateParams) {
			p.Metadata = json.RawMessage(`{"a":"` + strings.Repeat("x", 4096) + `"}`)
		}, "metadata"},
	}
	for _, tc := range cases {
		p := ok
		tc.mod(&p)
		_, created, err := e.Invoices.Create(e.Ctx, e.Merchant, p)
		require.Error(t, err, tc.name)
		assert.ErrorIs(t, err, invoice.ErrInvalid, tc.name)
		assert.False(t, created, tc.name)
		fields := invoice.FieldErrors(err)
		assert.Contains(t, fields, tc.field, tc.name)
		assert.Len(t, fields, 1, tc.name)
	}

	// Several problems are reported together.
	_, _, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "x", Currency: "??", SuccessURL: "nope"})
	assert.Len(t, invoice.FieldErrors(err), 3)
	assert.Contains(t, err.Error(), "amount")

	page, err := e.Store.ListInvoices(e.Ctx, store.InvoiceFilter{})
	require.NoError(t, err)
	assert.Zero(t, page.Total, "nothing persisted on validation errors")
}

func TestCreateDefaultsAndFields(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, created, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{
		Amount: "10.50", Currency: "usd", OrderID: " A-1 ", Title: "Order", Description: "Two things",
		SuccessURL: "https://shop.example/ok?x=1", CancelURL: "http://shop.example/cancel",
		CustomerEmail: "bob@example.com", Metadata: json.RawMessage(`{"cart":7}`),
	})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, domain.InvoicePending, v.Status)
	assert.Equal(t, "USD", v.PriceCurrency)
	assert.True(t, v.PriceAmount.Equal(decimal.RequireFromString("10.5")))
	assert.Equal(t, "A-1", v.MerchantOrderID)
	assert.Equal(t, e.Merchant.ID, v.MerchantID)
	assert.Equal(t, e.Merchant.Name, v.MerchantName)
	assert.Equal(t, invoicetest.Start.Add(30*time.Minute), v.ExpiresAt)
	require.NotNil(t, v.AmountUSD)
	assert.True(t, v.AmountUSD.Equal(decimal.RequireFromString("10.5")))
	assert.JSONEq(t, `{"cart":7}`, string(v.Metadata))
	assert.Nil(t, v.Quote)
	assert.Equal(t, 0, e.Rates.Calls(), "a USD invoice needs no rate")

	// expires_in bounds are inclusive; metadata defaults to {}.
	for _, ttl := range []int{60, 86400} {
		v, _, err = e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "1", Currency: "USD", ExpiresIn: ptr(ttl)})
		require.NoError(t, err)
		assert.Equal(t, invoicetest.Start.Add(time.Duration(ttl)*time.Second), v.ExpiresAt)
		assert.JSONEq(t, `{}`, string(v.Metadata))
	}
	// The default TTL follows the settings.
	e.Settings(func(s *domain.Settings) { s.InvoiceTTLSeconds = 600 })
	v, _, err = e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "1", Currency: "USD", Metadata: json.RawMessage(`null`)})
	require.NoError(t, err)
	assert.Equal(t, invoicetest.Start.Add(10*time.Minute), v.ExpiresAt)
}

func TestCreateAmountUSD(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	usd := func(amount, currency string) *decimal.Decimal {
		v, _, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: amount, Currency: currency})
		require.NoError(t, err)
		return v.AmountUSD
	}
	// Another fiat currency: crossed through a stablecoin's two prices (1 / 0.92).
	got := usd("10", "EUR")
	require.NotNil(t, got)
	assert.Equal(t, "10.869565", got.String())
	// Crypto symbols (any case): pegged stablecoin, and a priced coin.
	got = usd("25.5", "usdt")
	require.NotNil(t, got)
	assert.Equal(t, "25.5", got.String())
	got = usd("0.5", "ETH")
	require.NotNil(t, got)
	assert.Equal(t, "1000", got.String())
	v, _, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "0.000000000000000001", Currency: "eth"})
	require.NoError(t, err, "crypto amounts may use the asset's full precision")
	assert.Equal(t, "ETH", v.PriceCurrency)

	// Provider down: creation still succeeds, amount_usd stays NULL.
	e.Rates.SetError(rates.ErrUnavailable)
	assert.Nil(t, usd("10", "EUR"))
	assert.Nil(t, usd("1", "BTC"))
	got = usd("10", "USD")
	require.NotNil(t, got)
	got = usd("3", "USDC")
	require.NotNil(t, got, "the peg needs no provider")
}

func TestCreateIdempotentOnOrderID(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	p := invoice.CreateParams{Amount: "10", Currency: "USD", OrderID: "ORD-1", Title: "first"}
	a, created, err := e.Invoices.Create(e.Ctx, e.Merchant, p)
	require.NoError(t, err)
	require.True(t, created)

	// Same amount + currency (also in another notation / case): the existing invoice.
	for _, q := range []invoice.CreateParams{
		p,
		{Amount: "10.00", Currency: "usd", OrderID: "ORD-1", Title: "ignored", AssetID: "usdt-ethereum"},
	} {
		b, created, err := e.Invoices.Create(e.Ctx, e.Merchant, q)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, a.ID, b.ID)
		assert.Equal(t, "first", b.Title)
		assert.Nil(t, b.Quote, "a replay changes nothing")
	}
	// Different amount or currency: conflict.
	_, _, err = e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "11", Currency: "USD", OrderID: "ORD-1"})
	assert.ErrorIs(t, err, invoice.ErrOrderConflict)
	_, _, err = e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "10", Currency: "EUR", OrderID: "ORD-1"})
	assert.ErrorIs(t, err, invoice.ErrOrderConflict)

	// Order ids are per merchant; invoices without one are never merged.
	other := storetest.Merchant(t, e.Store)
	c, created, err := e.Invoices.Create(e.Ctx, other, p)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, a.ID, c.ID)
	x, _, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "10", Currency: "USD"})
	require.NoError(t, err)
	y, _, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "10", Currency: "USD"})
	require.NoError(t, err)
	assert.NotEqual(t, x.ID, y.ID)
}

func TestCreateConcurrentSameOrderID(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	const n = 12
	ids := make([]uuid.UUID, n)
	createdCount := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			v, created, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{
				Amount: "10", Currency: "USD", OrderID: "RACE", AssetID: "usdt-ethereum"})
			if !assert.NoError(t, err) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[i] = v.ID
			if created {
				createdCount++
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, createdCount)
	for _, id := range ids {
		assert.Equal(t, ids[0], id)
	}
	page, err := e.Store.ListInvoices(e.Ctx, store.InvoiceFilter{})
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total)
	quotes, err := e.Store.ListQuotesByInvoice(e.Ctx, ids[0])
	require.NoError(t, err)
	assert.Len(t, quotes, 1)
	w, err := e.Store.GetWallet(e.Ctx, e.EVMWallet.ID)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), w.NextIndex, "losers of the race must not burn addresses")
}

func TestCreateWithAsset(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, created, err := e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "10", Currency: "USD", AssetID: "usdt-ethereum"})
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, v.Quote)
	assert.Equal(t, "usdt-ethereum", v.Quote.AssetID)
	assert.Equal(t, domain.QuoteActive, v.Quote.Status)
	assert.Equal(t, "10000000", v.Quote.AmountDue.String())
	assert.Equal(t, v.ExpiresAt.Add(24*time.Hour), v.Quote.WatchUntil)

	count := func() int {
		page, err := e.Store.ListInvoices(e.Ctx, store.InvoiceFilter{})
		require.NoError(t, err)
		return page.Total
	}
	require.Equal(t, 1, count())

	// A quote that cannot be created fails the whole creation.
	for _, assetID := range []string{"nope", "usdc-base" /* chain without wallet */, "usdc-sepolia" /* disabled chain */} {
		_, _, err = e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "10", Currency: "USD", AssetID: assetID, OrderID: "X-" + assetID})
		assert.ErrorIs(t, err, invoice.ErrAssetUnavailable, assetID)
	}
	e.Rates.SetError(rates.ErrUnavailable)
	_, _, err = e.Invoices.Create(e.Ctx, e.Merchant, invoice.CreateParams{Amount: "10", Currency: "USD", AssetID: "btc-bitcoin", OrderID: "X-btc"})
	assert.ErrorIs(t, err, invoice.ErrRateUnavailable)
	assert.Equal(t, 1, count(), "nothing persisted")
	w, err := e.Store.GetWallet(e.Ctx, e.BitcoinWallet.ID)
	require.NoError(t, err)
	assert.Zero(t, w.NextIndex)

	// Unknown merchant.
	_, _, err = e.Invoices.Create(e.Ctx, domain.Merchant{ID: uuid.New()}, invoice.CreateParams{Amount: "10", Currency: "USD"})
	assert.ErrorIs(t, err, invoice.ErrNotFound)
}

func TestReadHelpers(t *testing.T) {
	t.Parallel()
	e := invoicetest.New(t)
	v, q := e.Quoted("10", "USD", "usdt-ethereum")
	e.Select(v.ID, "usdt-polygon")
	e.Credit(e.Quote(q.ID), 1_000_000, domain.TransferPending)

	d, err := e.Invoices.Get(e.Ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, v.ID, d.ID)
	assert.Equal(t, e.Merchant.Name, d.MerchantName)
	assert.Len(t, d.Quotes, 2)
	assert.Len(t, d.Transfers, 1)
	require.NotNil(t, d.Quote)
	assert.Equal(t, "usdt-polygon", d.Quote.AssetID, "current quote = the active one")

	_, err = e.Invoices.Get(e.Ctx, uuid.New())
	assert.ErrorIs(t, err, invoice.ErrNotFound)
	_, err = e.Invoices.GetView(e.Ctx, uuid.New())
	assert.ErrorIs(t, err, invoice.ErrNotFound)

	got, err := e.Invoices.GetViewForMerchant(e.Ctx, e.Merchant.ID, v.ID)
	require.NoError(t, err)
	assert.Equal(t, v.ID, got.ID)
	_, err = e.Invoices.GetViewForMerchant(e.Ctx, uuid.New(), v.ID)
	assert.ErrorIs(t, err, invoice.ErrNotFound)
}
