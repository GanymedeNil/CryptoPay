// Package view turns domain objects into the JSON shapes of docs/api.md
// (Invoice, Quote, Transfer, AssetInfo, CheckoutView with CheckoutAsset).
// The builders are pure: they take the
// domain values plus a catalog snapshot (chains and assets) and the runtime
// settings, and never touch the database or the network.
//
// The same shapes are used for API responses and for the `data` member of
// webhook payloads, so a merchant sees one Invoice representation everywhere.
//
// Conventions (docs/api.md): amounts are decimal strings in whole units
// formatted by package money, `*_base` fields are integer strings in base
// units, timestamps are RFC 3339 UTC and nullable values are JSON null.
package view

import (
	"encoding/json"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"

	"cryptopay/internal/domain"
	"cryptopay/internal/money"
	"cryptopay/internal/paymenturi"
	"cryptopay/internal/store"
)

// Invoice is the `Invoice` type of docs/api.md.
type Invoice struct {
	ID            string          `json:"id"`
	MerchantID    string          `json:"merchant_id"`
	OrderID       string          `json:"order_id"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	Amount        string          `json:"amount"`
	Currency      string          `json:"currency"`
	AmountUSD     *string         `json:"amount_usd"`
	Status        string          `json:"status"`
	Overpaid      bool            `json:"overpaid"`
	PaidLate      bool            `json:"paid_late"`
	SuccessURL    string          `json:"success_url"`
	CancelURL     string          `json:"cancel_url"`
	CustomerEmail string          `json:"customer_email"`
	Metadata      json.RawMessage `json:"metadata"`
	CheckoutURL   string          `json:"checkout_url"`
	ExpiresAt     time.Time       `json:"expires_at"`
	PaidAt        *time.Time      `json:"paid_at"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Quote         *Quote          `json:"quote"`
}

// Quote is the `Quote` type of docs/api.md.
type Quote struct {
	ID                    string    `json:"id"`
	AssetID               string    `json:"asset_id"`
	Symbol                string    `json:"symbol"`
	ChainID               string    `json:"chain_id"`
	ChainName             string    `json:"chain_name"`
	Family                string    `json:"family"`
	NetworkID             string    `json:"network_id"`
	IsTestnet             bool      `json:"is_testnet"`
	ContractAddress       string    `json:"contract_address"`
	Decimals              int       `json:"decimals"`
	WalletKind            string    `json:"wallet_kind"`
	Address               string    `json:"address"`
	Rate                  string    `json:"rate"`
	Amount                string    `json:"amount"`
	AmountBase            string    `json:"amount_base"`
	AmountReceived        string    `json:"amount_received"`
	AmountReceivedBase    string    `json:"amount_received_base"`
	AmountConfirmed       string    `json:"amount_confirmed"`
	AmountConfirmedBase   string    `json:"amount_confirmed_base"`
	AmountRemaining       string    `json:"amount_remaining"`
	AmountRemainingBase   string    `json:"amount_remaining_base"`
	PaymentURI            string    `json:"payment_uri"`
	RequiredConfirmations int       `json:"required_confirmations"`
	Status                string    `json:"status"`
	CreatedAt             time.Time `json:"created_at"`
}

// Transfer is the `Transfer` type of docs/api.md.
type Transfer struct {
	ID                    string     `json:"id"`
	ChainID               string     `json:"chain_id"`
	AssetID               string     `json:"asset_id"`
	Symbol                string     `json:"symbol"`
	TxHash                string     `json:"tx_hash"`
	Index                 int        `json:"index"`
	BlockNumber           uint64     `json:"block_number"`
	FromAddress           string     `json:"from_address"`
	ToAddress             string     `json:"to_address"`
	Amount                string     `json:"amount"`
	AmountBase            string     `json:"amount_base"`
	Status                string     `json:"status"`
	Confirmations         int        `json:"confirmations"`
	RequiredConfirmations int        `json:"required_confirmations"`
	ExplorerURL           string     `json:"explorer_url"`
	InvoiceID             *string    `json:"invoice_id"`
	QuoteID               *string    `json:"quote_id"`
	DetectedAt            time.Time  `json:"detected_at"`
	ConfirmedAt           *time.Time `json:"confirmed_at"`
}

// AssetInfo is the `AssetInfo` type of docs/api.md.
type AssetInfo struct {
	ID              string `json:"id"`
	Symbol          string `json:"symbol"`
	Name            string `json:"name"`
	ChainID         string `json:"chain_id"`
	ChainName       string `json:"chain_name"`
	Family          string `json:"family"`
	NetworkID       string `json:"network_id"`
	IsTestnet       bool   `json:"is_testnet"`
	ContractAddress string `json:"contract_address"`
	Decimals        int    `json:"decimals"`
	IsStablecoin    bool   `json:"is_stablecoin"`
}

// NewInvoice builds the API representation of inv. quote is the invoice's
// current quote (the paid one, else the active one; see
// store.GetCurrentQuote) or nil when no asset was selected yet.
func NewInvoice(inv domain.Invoice, quote *domain.Quote, cat *store.Catalog, st domain.Settings) Invoice {
	out := Invoice{
		ID:            inv.ID.String(),
		MerchantID:    inv.MerchantID.String(),
		OrderID:       inv.MerchantOrderID,
		Title:         inv.Title,
		Description:   inv.Description,
		Amount:        inv.PriceAmount.String(),
		Currency:      inv.PriceCurrency,
		Status:        string(inv.Status),
		Overpaid:      inv.Overpaid,
		PaidLate:      inv.PaidLate,
		SuccessURL:    inv.SuccessURL,
		CancelURL:     inv.CancelURL,
		CustomerEmail: inv.CustomerEmail,
		Metadata:      metadata(inv.Metadata),
		CheckoutURL:   CheckoutURL(st.CheckoutBaseURL, inv.ID),
		ExpiresAt:     inv.ExpiresAt.UTC(),
		PaidAt:        utcPtr(inv.PaidAt),
		CreatedAt:     inv.CreatedAt.UTC(),
		UpdatedAt:     inv.UpdatedAt.UTC(),
	}
	if inv.AmountUSD != nil {
		s := inv.AmountUSD.String()
		out.AmountUSD = &s
	}
	if quote != nil {
		q := NewQuote(*quote, cat)
		out.Quote = &q
	}
	return out
}

// NewQuote builds the API representation of q. Chain and asset attributes
// come from cat; if either is missing from the snapshot (which the foreign
// keys normally rule out) the corresponding fields stay empty and amounts
// are formatted with zero decimals.
//
// payment_uri asks for the amount due, except for an xpub quote that has
// received part of it: then it asks for the remaining amount, so a customer
// who scans the QR code again tops up instead of paying everything twice.
// (A static-wallet quote only ever matches its exact amount, so its URI
// always carries the full amount due.)
func NewQuote(q domain.Quote, cat *store.Catalog) Quote {
	a, c := lookup(cat, q.AssetID, q.ChainID)
	due, received, confirmed := nz(q.AmountDue), nz(q.AmountReceived), nz(q.AmountConfirmed)
	remaining := new(big.Int).Sub(due, received)
	if remaining.Sign() < 0 {
		remaining.SetInt64(0)
	}
	out := Quote{
		ID:                    q.ID.String(),
		AssetID:               q.AssetID,
		Symbol:                a.Symbol,
		ChainID:               q.ChainID,
		ChainName:             c.Name,
		Family:                string(c.Family),
		NetworkID:             c.NetworkID,
		IsTestnet:             c.IsTestnet,
		ContractAddress:       a.ContractAddress,
		Decimals:              a.Decimals,
		WalletKind:            string(q.WalletKind),
		Address:               q.Address,
		Rate:                  q.Rate.String(),
		Amount:                money.Format(due, a.Decimals),
		AmountBase:            due.String(),
		AmountReceived:        money.Format(received, a.Decimals),
		AmountReceivedBase:    received.String(),
		AmountConfirmed:       money.Format(confirmed, a.Decimals),
		AmountConfirmedBase:   confirmed.String(),
		AmountRemaining:       money.Format(remaining, a.Decimals),
		AmountRemainingBase:   remaining.String(),
		RequiredConfirmations: c.Confirmations,
		Status:                string(q.Status),
		CreatedAt:             q.CreatedAt.UTC(),
	}
	if a.ID != "" && c.ID != "" {
		uriAmount := due
		if q.WalletKind == domain.WalletXpub && received.Sign() > 0 && remaining.Sign() > 0 {
			uriAmount = remaining
		}
		out.PaymentURI = paymenturi.Build(c, a, q.Address, uriAmount)
	}
	return out
}

// NewQuotes maps NewQuote over qs (never nil).
func NewQuotes(qs []domain.Quote, cat *store.Catalog) []Quote {
	out := make([]Quote, 0, len(qs))
	for _, q := range qs {
		out = append(out, NewQuote(q, cat))
	}
	return out
}

// NewTransfer builds the API representation of t. A transfer of a token
// that is not a configured asset has asset_id "" and symbol ""; its amount
// is then shown in base units (the decimals are unknown).
func NewTransfer(t domain.Transfer, cat *store.Catalog) Transfer {
	a, c := lookup(cat, t.AssetID, t.ChainID)
	amount := nz(t.Amount)
	out := Transfer{
		ID:                    t.ID.String(),
		ChainID:               t.ChainID,
		AssetID:               t.AssetID,
		Symbol:                a.Symbol,
		TxHash:                t.TxHash,
		Index:                 t.Index,
		BlockNumber:           t.BlockNumber,
		FromAddress:           t.FromAddress,
		ToAddress:             t.ToAddress,
		Amount:                money.Format(amount, a.Decimals),
		AmountBase:            amount.String(),
		Status:                string(t.Status),
		Confirmations:         t.Confirmations,
		RequiredConfirmations: c.Confirmations,
		ExplorerURL:           ExplorerTxURL(c, t.TxHash),
		InvoiceID:             idPtr(t.InvoiceID),
		QuoteID:               idPtr(t.QuoteID),
		DetectedAt:            t.DetectedAt.UTC(),
		ConfirmedAt:           utcPtr(t.ConfirmedAt),
	}
	return out
}

// NewTransfers maps NewTransfer over ts (never nil).
func NewTransfers(ts []domain.Transfer, cat *store.Catalog) []Transfer {
	out := make([]Transfer, 0, len(ts))
	for _, t := range ts {
		out = append(out, NewTransfer(t, cat))
	}
	return out
}

// NewAssetInfo builds the API representation of asset a.
func NewAssetInfo(a domain.Asset, cat *store.Catalog) AssetInfo {
	_, c := lookup(cat, "", a.ChainID)
	return AssetInfo{
		ID:              a.ID,
		Symbol:          a.Symbol,
		Name:            a.Name,
		ChainID:         a.ChainID,
		ChainName:       c.Name,
		Family:          string(c.Family),
		NetworkID:       c.NetworkID,
		IsTestnet:       c.IsTestnet,
		ContractAddress: a.ContractAddress,
		Decimals:        a.Decimals,
		IsStablecoin:    a.IsStablecoin,
	}
}

// NewAssetInfos maps NewAssetInfo over as (never nil).
func NewAssetInfos(as []domain.Asset, cat *store.Catalog) []AssetInfo {
	out := make([]AssetInfo, 0, len(as))
	for _, a := range as {
		out = append(out, NewAssetInfo(a, cat))
	}
	return out
}

// CheckoutURL returns `${base}/pay/${id}` (a trailing slash of base is
// dropped).
func CheckoutURL(base string, id uuid.UUID) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + "/pay/" + id.String()
}

// ExplorerTxURL fills the chain's transaction explorer template ("{tx}")
// with txHash; "" when the chain has no template.
func ExplorerTxURL(c domain.Chain, txHash string) string {
	if c.ExplorerTxURL == "" || txHash == "" {
		return ""
	}
	return strings.ReplaceAll(c.ExplorerTxURL, "{tx}", txHash)
}

// ExplorerAddressURL fills the chain's address explorer template
// ("{address}"); "" when the chain has no template.
func ExplorerAddressURL(c domain.Chain, address string) string {
	if c.ExplorerAddr == "" || address == "" {
		return ""
	}
	return strings.ReplaceAll(c.ExplorerAddr, "{address}", address)
}

func lookup(cat *store.Catalog, assetID, chainID string) (domain.Asset, domain.Chain) {
	var a domain.Asset
	var c domain.Chain
	if cat == nil {
		return a, c
	}
	if assetID != "" {
		a, _ = cat.Asset(assetID)
	}
	if chainID != "" {
		c, _ = cat.Chain(chainID)
	}
	return a, c
}

func nz(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func idPtr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// metadata returns m as a JSON object, defaulting to {}.
func metadata(m json.RawMessage) json.RawMessage {
	if len(m) == 0 || !json.Valid(m) || string(m) == "null" {
		return json.RawMessage(`{}`)
	}
	return m
}

// WebhookEnvelope is the typed form of a webhook delivery body (docs/api.md,
// "Webhooks"): `{ id, type, created_at, data: Invoice }`. ID is the webhook
// event id (also sent as X-CryptoPay-Delivery). Payloads are produced with
// webhook.BuildEnvelope; this type is for decoding them (tests, Go
// merchants).
type WebhookEnvelope struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Data      Invoice   `json:"data"`
}

// ---- checkout (public) ----

// CheckoutInvoice is the `invoice` member of `CheckoutView` in docs/api.md:
// the subset of the invoice a customer may see (no merchant id, order id,
// customer e-mail or metadata).
type CheckoutInvoice struct {
	ID           string     `json:"id"`
	MerchantName string     `json:"merchant_name"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Amount       string     `json:"amount"`
	Currency     string     `json:"currency"`
	Status       string     `json:"status"`
	Overpaid     bool       `json:"overpaid"`
	PaidLate     bool       `json:"paid_late"`
	SuccessURL   string     `json:"success_url"`
	CancelURL    string     `json:"cancel_url"`
	ExpiresAt    time.Time  `json:"expires_at"`
	PaidAt       *time.Time `json:"paid_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

// CheckoutAsset is the `CheckoutAsset` type of docs/api.md: an AssetInfo
// plus the approximate amount due in that asset (null without a rate).
type CheckoutAsset struct {
	AssetInfo
	EstimatedAmount *string `json:"estimated_amount"`
}

// CheckoutView is the `CheckoutView` type of docs/api.md.
type CheckoutView struct {
	Invoice                CheckoutInvoice `json:"invoice"`
	Assets                 []CheckoutAsset `json:"assets"`
	Quote                  *Quote          `json:"quote"`
	Transfers              []Transfer      `json:"transfers"`
	WalletConnectProjectID string          `json:"walletconnect_project_id"`
	ServerTime             time.Time       `json:"server_time"`
}

// NewCheckoutAsset builds a checkout asset entry. estimate is the amount due
// in base units of a (invoice.Estimate.Amount); nil renders as null.
func NewCheckoutAsset(a domain.Asset, estimate *big.Int, cat *store.Catalog) CheckoutAsset {
	out := CheckoutAsset{AssetInfo: NewAssetInfo(a, cat)}
	if estimate != nil {
		s := money.Format(estimate, a.Decimals)
		out.EstimatedAmount = &s
	}
	return out
}

// NewCheckoutView builds the public checkout document. v is the invoice
// with merchant name and current quote (store.InvoiceView), assets the
// payable assets in display order (NewCheckoutAsset), transfers the
// transfers credited to the invoice, newest first. now is the server time
// reported for countdown drift correction.
func NewCheckoutView(v store.InvoiceView, assets []CheckoutAsset, transfers []domain.Transfer, cat *store.Catalog, st domain.Settings, now time.Time) CheckoutView {
	out := CheckoutView{
		Invoice: CheckoutInvoice{
			ID:           v.ID.String(),
			MerchantName: v.MerchantName,
			Title:        v.Title,
			Description:  v.Description,
			Amount:       v.PriceAmount.String(),
			Currency:     v.PriceCurrency,
			Status:       string(v.Status),
			Overpaid:     v.Overpaid,
			PaidLate:     v.PaidLate,
			SuccessURL:   v.SuccessURL,
			CancelURL:    v.CancelURL,
			ExpiresAt:    v.ExpiresAt.UTC(),
			PaidAt:       utcPtr(v.PaidAt),
			CreatedAt:    v.CreatedAt.UTC(),
		},
		Assets:                 assets,
		Transfers:              NewTransfers(transfers, cat),
		WalletConnectProjectID: st.WalletConnectID,
		ServerTime:             now.UTC(),
	}
	if out.Assets == nil {
		out.Assets = []CheckoutAsset{}
	}
	if v.Quote != nil {
		q := NewQuote(*v.Quote, cat)
		out.Quote = &q
	}
	return out
}
