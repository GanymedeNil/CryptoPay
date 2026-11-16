package view_test

import (
	"encoding/json"
	"math/big"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/catalog"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/view"
)

func testCatalog() *store.Catalog { return store.NewCatalog(catalog.Chains(), catalog.Assets()) }

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sorted(s ...string) []string { sort.Strings(s); return s }

func TestJSONFieldsMatchAPIContract(t *testing.T) {
	assert.Equal(t, sorted("id", "merchant_id", "order_id", "title", "description", "amount", "currency", "amount_usd",
		"status", "overpaid", "paid_late", "success_url", "cancel_url", "customer_email", "metadata", "checkout_url",
		"expires_at", "paid_at", "created_at", "updated_at", "quote"), keysOf(t, view.Invoice{}))
	assert.Equal(t, sorted("id", "asset_id", "symbol", "chain_id", "chain_name", "family", "network_id", "is_testnet",
		"contract_address", "decimals", "wallet_kind", "address", "rate", "amount", "amount_base", "amount_received",
		"amount_received_base", "amount_confirmed", "amount_confirmed_base", "amount_remaining", "amount_remaining_base",
		"payment_uri", "required_confirmations", "status", "created_at"), keysOf(t, view.Quote{}))
	assert.Equal(t, sorted("id", "chain_id", "asset_id", "symbol", "tx_hash", "index", "block_number", "from_address",
		"to_address", "amount", "amount_base", "status", "confirmations", "required_confirmations", "explorer_url",
		"invoice_id", "quote_id", "detected_at", "confirmed_at"), keysOf(t, view.Transfer{}))
	assert.Equal(t, sorted("id", "symbol", "name", "chain_id", "chain_name", "family", "network_id", "is_testnet",
		"contract_address", "decimals", "is_stablecoin"), keysOf(t, view.AssetInfo{}))
	assert.Equal(t, sorted("id", "type", "created_at", "data"), keysOf(t, view.WebhookEnvelope{}))
}

func TestNewQuote(t *testing.T) {
	cat := testCatalog()
	q := domain.Quote{
		ID: uuid.New(), InvoiceID: uuid.New(), AssetID: "usdt-ethereum", ChainID: "ethereum",
		WalletKind: domain.WalletXpub, Address: "0x52908400098527886E0F7030069857D2E4169EE7",
		Rate: decimal.RequireFromString("1"), AmountDue: big.NewInt(12_500_000), AmountReceived: big.NewInt(2_500_000),
		AmountConfirmed: big.NewInt(1_000_000), Status: domain.QuoteActive, CreatedAt: t0,
	}
	v := view.NewQuote(q, cat)
	assert.Equal(t, q.ID.String(), v.ID)
	assert.Equal(t, "USDT", v.Symbol)
	assert.Equal(t, "Ethereum", v.ChainName)
	assert.Equal(t, "evm", v.Family)
	assert.Equal(t, "1", v.NetworkID)
	assert.False(t, v.IsTestnet)
	assert.Equal(t, "0xdAC17F958D2ee523a2206206994597C13D831ec7", v.ContractAddress)
	assert.Equal(t, 6, v.Decimals)
	assert.Equal(t, "xpub", v.WalletKind)
	assert.Equal(t, "1", v.Rate)
	assert.Equal(t, "12.5", v.Amount)
	assert.Equal(t, "12500000", v.AmountBase)
	assert.Equal(t, "2.5", v.AmountReceived)
	assert.Equal(t, "2500000", v.AmountReceivedBase)
	assert.Equal(t, "1", v.AmountConfirmed)
	assert.Equal(t, "1000000", v.AmountConfirmedBase)
	assert.Equal(t, "10", v.AmountRemaining)
	assert.Equal(t, "10000000", v.AmountRemainingBase)
	// Partially paid xpub quote: the URI asks for the remaining 10 USDT.
	assert.Equal(t, "ethereum:0xdAC17F958D2ee523a2206206994597C13D831ec7@1/transfer?address=0x52908400098527886E0F7030069857D2E4169EE7&uint256=10000000", v.PaymentURI)
	eth, _ := cat.Chain("ethereum")
	assert.Equal(t, eth.Confirmations, v.RequiredConfirmations)
	assert.Equal(t, "active", v.Status)
	assert.Equal(t, time.UTC, v.CreatedAt.Location())
	assert.True(t, v.CreatedAt.Equal(t0))
}

func TestNewQuoteOverpaidRemainingIsZeroAndNilAmounts(t *testing.T) {
	cat := testCatalog()
	v := view.NewQuote(domain.Quote{AssetID: "usdt-tron", ChainID: "tron", AmountDue: big.NewInt(1_000_000),
		AmountReceived: big.NewInt(3_000_000), Address: "TXYZ"}, cat)
	assert.Equal(t, "0", v.AmountRemaining)
	assert.Equal(t, "0", v.AmountRemainingBase)
	assert.Equal(t, "", v.PaymentURI, "tron has no payment URI")
	assert.Equal(t, "0", v.AmountConfirmed)

	// Unknown asset / nil catalog must not panic.
	v = view.NewQuote(domain.Quote{AssetID: "nope", ChainID: "nope", AmountDue: big.NewInt(5)}, nil)
	assert.Equal(t, "5", v.Amount)
	assert.Equal(t, "", v.Symbol)
}

func TestNewInvoice(t *testing.T) {
	cat := testCatalog()
	st := domain.DefaultSettings()
	st.CheckoutBaseURL = "https://pay.example.com/"
	usd := decimal.RequireFromString("10.5")
	paid := t0.Add(time.Minute)
	inv := domain.Invoice{
		ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "A-1", Title: "T", Description: "D",
		PriceAmount: decimal.RequireFromString("10.50"), PriceCurrency: "USD", AmountUSD: &usd,
		Status: domain.InvoicePaid, Overpaid: true, PaidLate: true, SuccessURL: "https://s", CancelURL: "https://c",
		CustomerEmail: "a@b.co", Metadata: json.RawMessage(`{"k":[1,2]}`), ExpiresAt: t0, PaidAt: &paid,
		CreatedAt: t0, UpdatedAt: t0,
	}
	q := domain.Quote{ID: uuid.New(), AssetID: "btc-bitcoin", ChainID: "bitcoin", AmountDue: big.NewInt(21_000),
		Address: "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", Status: domain.QuotePaid}
	v := view.NewInvoice(inv, &q, cat, st)
	assert.Equal(t, inv.ID.String(), v.ID)
	assert.Equal(t, inv.MerchantID.String(), v.MerchantID)
	assert.Equal(t, "A-1", v.OrderID)
	assert.Equal(t, "10.5", v.Amount)
	assert.Equal(t, "USD", v.Currency)
	require.NotNil(t, v.AmountUSD)
	assert.Equal(t, "10.5", *v.AmountUSD)
	assert.Equal(t, "paid", v.Status)
	assert.True(t, v.Overpaid)
	assert.True(t, v.PaidLate)
	assert.Equal(t, "https://pay.example.com/pay/"+inv.ID.String(), v.CheckoutURL)
	assert.JSONEq(t, `{"k":[1,2]}`, string(v.Metadata))
	require.NotNil(t, v.PaidAt)
	assert.Equal(t, time.UTC, v.PaidAt.Location())
	require.NotNil(t, v.Quote)
	assert.Equal(t, "0.00021", v.Quote.Amount)
	assert.Equal(t, "bitcoin:bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq?amount=0.00021", v.Quote.PaymentURI)

	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "2026-03-01T11:00:00Z", m["expires_at"])
	assert.Equal(t, "10.5", m["amount_usd"])

	// Nullable fields are JSON null, metadata defaults to {}.
	inv.AmountUSD, inv.PaidAt, inv.Metadata = nil, nil, nil
	raw, err = json.Marshal(view.NewInvoice(inv, nil, cat, st))
	require.NoError(t, err)
	m = map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Nil(t, m["amount_usd"])
	assert.Nil(t, m["paid_at"])
	assert.Nil(t, m["quote"])
	assert.Contains(t, m, "quote")
	assert.Equal(t, map[string]any{}, m["metadata"])

	// Round trip.
	var back view.Invoice
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, v.ID, back.ID)
}

func TestNewTransfer(t *testing.T) {
	cat := testCatalog()
	inv, qid := uuid.New(), uuid.New()
	conf := t0.Add(time.Hour)
	tr := domain.Transfer{
		ID: uuid.New(), ChainID: "tron", TxHash: "abc123", Index: 2, BlockNumber: 77, FromAddress: "TFrom",
		ToAddress: "TTo", TokenContract: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", AssetID: "usdt-tron",
		Amount: big.NewInt(1_230_000), QuoteID: &qid, InvoiceID: &inv, Status: domain.TransferConfirmed,
		Confirmations: 19, DetectedAt: t0, ConfirmedAt: &conf,
	}
	v := view.NewTransfer(tr, cat)
	assert.Equal(t, "USDT", v.Symbol)
	assert.Equal(t, "1.23", v.Amount)
	assert.Equal(t, "1230000", v.AmountBase)
	assert.Equal(t, 2, v.Index)
	assert.Equal(t, uint64(77), v.BlockNumber)
	assert.Equal(t, "confirmed", v.Status)
	assert.Equal(t, 19, v.Confirmations)
	assert.Equal(t, 19, v.RequiredConfirmations)
	assert.Equal(t, "https://tronscan.org/#/transaction/abc123", v.ExplorerURL)
	require.NotNil(t, v.InvoiceID)
	assert.Equal(t, inv.String(), *v.InvoiceID)
	require.NotNil(t, v.QuoteID)
	assert.Equal(t, qid.String(), *v.QuoteID)
	require.NotNil(t, v.ConfirmedAt)

	// Unmatched transfer of an unknown token.
	tr.AssetID, tr.QuoteID, tr.InvoiceID, tr.ConfirmedAt = "", nil, nil, nil
	v = view.NewTransfer(tr, cat)
	assert.Equal(t, "", v.AssetID)
	assert.Equal(t, "", v.Symbol)
	assert.Equal(t, "1230000", v.Amount, "unknown decimals: base units")
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Nil(t, m["invoice_id"])
	assert.Nil(t, m["quote_id"])
	assert.Nil(t, m["confirmed_at"])

	assert.Empty(t, view.NewTransfers(nil, cat))
	assert.NotNil(t, view.NewTransfers(nil, cat))
	assert.Len(t, view.NewTransfers([]domain.Transfer{tr, tr}, cat), 2)
}

func TestNewAssetInfo(t *testing.T) {
	cat := testCatalog()
	a, ok := cat.Asset("usdc-sepolia")
	require.True(t, ok)
	v := view.NewAssetInfo(a, cat)
	assert.Equal(t, view.AssetInfo{ID: "usdc-sepolia", Symbol: "USDC", Name: "USD Coin (Sepolia)", ChainID: "sepolia",
		ChainName: "Ethereum Sepolia", Family: "evm", NetworkID: "11155111", IsTestnet: true,
		ContractAddress: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", Decimals: 6, IsStablecoin: true}, v)
	assert.Len(t, view.NewAssetInfos(cat.Assets, cat), len(cat.Assets))
	assert.Len(t, view.NewQuotes(nil, cat), 0)
}

func TestPaymentURIAsksForRemainingOnPartialXpubQuote(t *testing.T) {
	cat := testCatalog()
	q := domain.Quote{ID: uuid.New(), AssetID: "btc-bitcoin", ChainID: "bitcoin", WalletKind: domain.WalletXpub,
		Address: "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", AmountDue: big.NewInt(100_000)}
	const base = "bitcoin:bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq?amount="
	assert.Equal(t, base+"0.001", view.NewQuote(q, cat).PaymentURI, "nothing received: full amount")
	q.AmountReceived = big.NewInt(40_000)
	v := view.NewQuote(q, cat)
	assert.Equal(t, base+"0.0006", v.PaymentURI, "partial: the remaining amount")
	assert.Equal(t, "0.001", v.Amount, "amount stays the amount due")
	q.AmountReceived = big.NewInt(100_000)
	assert.Equal(t, base+"0.001", view.NewQuote(q, cat).PaymentURI, "fully received: amount due")

	// Static wallets match exact amounts only: always the amount due.
	q.WalletKind, q.AmountReceived = domain.WalletStatic, big.NewInt(40_000)
	assert.Equal(t, base+"0.001", view.NewQuote(q, cat).PaymentURI)
}

func TestCheckoutView(t *testing.T) {
	cat := testCatalog()
	st := domain.DefaultSettings()
	st.WalletConnectID = "wc-123"
	paid := t0.Add(time.Minute)
	q := domain.Quote{ID: uuid.New(), AssetID: "usdt-tron", ChainID: "tron", WalletKind: domain.WalletStatic,
		Address: "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8", AmountDue: big.NewInt(10_000_100), Status: domain.QuotePaid}
	v := store.InvoiceView{
		Invoice: domain.Invoice{ID: uuid.New(), MerchantID: uuid.New(), MerchantOrderID: "secret-order",
			Title: "T", Description: "D", PriceAmount: decimal.RequireFromString("10.00"), PriceCurrency: "USD",
			Status: domain.InvoicePaid, Overpaid: true, SuccessURL: "https://s", CancelURL: "https://c",
			CustomerEmail: "private@example.com", Metadata: json.RawMessage(`{"internal":1}`),
			ExpiresAt: t0, PaidAt: &paid, CreatedAt: t0},
		MerchantName: "Shop",
		Quote:        &q,
	}
	usdt, _ := cat.Asset("usdt-tron")
	btc, _ := cat.Asset("btc-bitcoin")
	assets := []view.CheckoutAsset{
		view.NewCheckoutAsset(usdt, big.NewInt(10_000_000), cat),
		view.NewCheckoutAsset(btc, nil, cat),
	}
	tr := domain.Transfer{ID: uuid.New(), ChainID: "tron", AssetID: "usdt-tron", TxHash: "h", Amount: big.NewInt(10_000_100),
		Status: domain.TransferConfirmed, DetectedAt: t0, QuoteID: &q.ID, InvoiceID: &v.ID}
	now := t0.Add(2 * time.Minute)
	cv := view.NewCheckoutView(v, assets, []domain.Transfer{tr}, cat, st, now)

	raw, err := json.Marshal(cv)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, sorted("invoice", "assets", "quote", "transfers", "walletconnect_project_id", "server_time"), keysOf(t, cv))
	assert.Equal(t, sorted("id", "merchant_name", "title", "description", "amount", "currency", "status", "overpaid",
		"paid_late", "success_url", "cancel_url", "expires_at", "paid_at", "created_at"), keysOf(t, cv.Invoice))
	assert.NotContains(t, string(raw), "secret-order", "no merchant-private data on the public page")
	assert.NotContains(t, string(raw), "private@example.com")
	assert.NotContains(t, string(raw), "internal")

	assert.Equal(t, "Shop", cv.Invoice.MerchantName)
	assert.Equal(t, "10", cv.Invoice.Amount)
	assert.Equal(t, "paid", cv.Invoice.Status)
	assert.Equal(t, "wc-123", cv.WalletConnectProjectID)
	assert.Equal(t, "2026-03-01T11:02:00Z", m["server_time"])
	require.NotNil(t, cv.Quote)
	assert.Equal(t, "10.0001", cv.Quote.Amount)
	require.Len(t, cv.Transfers, 1)
	assert.Equal(t, "10.0001", cv.Transfers[0].Amount)

	// CheckoutAsset flattens AssetInfo and adds estimated_amount.
	items := m["assets"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	assert.Equal(t, "usdt-tron", first["id"])
	assert.Equal(t, "Tron", first["chain_name"])
	assert.Equal(t, "10", first["estimated_amount"])
	assert.Contains(t, items[1].(map[string]any), "estimated_amount")
	assert.Nil(t, items[1].(map[string]any)["estimated_amount"])

	// No quote, no assets, no transfers: null / [] / [].
	v.Quote = nil
	raw, err = json.Marshal(view.NewCheckoutView(v, nil, nil, cat, st, now))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Nil(t, m["quote"])
	assert.Equal(t, []any{}, m["assets"])
	assert.Equal(t, []any{}, m["transfers"])
}

func TestURLHelpers(t *testing.T) {
	id := uuid.MustParse("7b5a1e0e-98a5-4e0b-9d8e-1f2f3a4b5c6d")
	assert.Equal(t, "https://x.io/pay/"+id.String(), view.CheckoutURL("https://x.io", id))
	assert.Equal(t, "https://x.io/pay/"+id.String(), view.CheckoutURL(" https://x.io// ", id))
	assert.Equal(t, "", view.ExplorerTxURL(domain.Chain{}, "h"))
	assert.Equal(t, "", view.ExplorerTxURL(domain.Chain{ExplorerTxURL: "https://e/{tx}"}, ""))
	assert.Equal(t, "https://e/tx/h", view.ExplorerTxURL(domain.Chain{ExplorerTxURL: "https://e/tx/{tx}"}, "h"))
	assert.Equal(t, "https://e/a/x", view.ExplorerAddressURL(domain.Chain{ExplorerAddr: "https://e/a/{address}"}, "x"))
	assert.Equal(t, "", view.ExplorerAddressURL(domain.Chain{}, "x"))
}
