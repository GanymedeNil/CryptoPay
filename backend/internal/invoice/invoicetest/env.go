// Package invoicetest provides a ready-made PostgreSQL-backed environment
// for tests of the invoice service and of packages built on it (scanner,
// API): a seeded store with an injected clock, static rates, realistic
// receiving wallets and helpers to drive invoices through their lifecycle.
// Tests are skipped when TEST_DATABASE_URL is unset (see storetest).
package invoicetest

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet/hdwallettest"
	"cryptopay/internal/invoice"
	"cryptopay/internal/rates"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
	"cryptopay/internal/view"
)

// Mnemonic is the well-known Hardhat / Anvil development mnemonic the test
// wallets are derived from (never use it for real funds).
const Mnemonic = "test test test test test test test test test test test junk"

// Start is the time the environment's clock starts at.
var Start = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// Env is a test environment.
//
// Wallets: an EVM xpub wallet (m/44'/60'/0') assigned to ethereum and
// polygon, a Bitcoin xpub wallet (m/84'/0'/0') assigned to bitcoin, a static
// Tron wallet assigned to tron and a static Solana wallet assigned to
// solana. All other chains have no wallet.
//
// Rates (see DefaultRates): USDT/USDC = 1 USD = 0.92 EUR, ETH = 2000 USD,
// BTC = 50000 USD, TRX = 0.25 USD, SOL = 150 USD. Settings are the defaults:
// TTL 30 min, grace 24 h, tolerance 50 bps, stablecoin peg on, no markup.
type Env struct {
	T        testing.TB
	Ctx      context.Context
	Store    *store.Store
	Clock    *storetest.Clock
	Rates    *rates.Static
	Invoices *invoice.Service
	Log      *slog.Logger
	Merchant domain.Merchant

	EVMWallet     domain.Wallet // xpub
	BitcoinWallet domain.Wallet // xpub
	TronWallet    domain.Wallet // static
	SolanaWallet  domain.Wallet // static
}

// DefaultRates returns the rates an Env starts with.
func DefaultRates() map[string]map[string]decimal.Decimal {
	d := decimal.RequireFromString
	return map[string]map[string]decimal.Decimal{
		"tether":   {"usd": d("1"), "eur": d("0.92")},
		"usd-coin": {"usd": d("1"), "eur": d("0.92")},
		"ethereum": {"usd": d("2000"), "eur": d("1840")},
		"bitcoin":  {"usd": d("50000"), "eur": d("46000")},
		"tron":     {"usd": d("0.25")},
		"solana":   {"usd": d("150")},
	}
}

// New builds an Env on a fresh database schema.
func New(t testing.TB) *Env {
	t.Helper()
	clock := storetest.FixedClock(Start)
	s := storetest.NewSeeded(t, store.WithClock(clock.Now))
	log := slog.New(slog.DiscardHandler)
	rp := rates.NewStatic(DefaultRates())
	e := &Env{T: t, Ctx: context.Background(), Store: s, Clock: clock, Rates: rp, Log: log,
		Invoices: invoice.New(s, rp, log, clock.Now)}
	e.Merchant = storetest.Merchant(t, s)

	e.EVMWallet = storetest.Wallet(t, s, domain.FamilyEVM, domain.WalletXpub, func(w *domain.Wallet) {
		w.Xpub = hdwallettest.AccountXpub(Mnemonic, "m/44'/60'/0'")
	})
	storetest.AssignWallet(t, s, "ethereum", e.EVMWallet.ID)
	storetest.AssignWallet(t, s, "polygon", e.EVMWallet.ID)
	e.BitcoinWallet = storetest.Wallet(t, s, domain.FamilyUTXO, domain.WalletXpub, func(w *domain.Wallet) {
		w.Xpub = hdwallettest.AccountXpub(Mnemonic, "m/84'/0'/0'")
	})
	storetest.AssignWallet(t, s, "bitcoin", e.BitcoinWallet.ID)
	e.TronWallet = storetest.Wallet(t, s, domain.FamilyTron, domain.WalletStatic, func(w *domain.Wallet) {
		w.Address = "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8"
	})
	storetest.AssignWallet(t, s, "tron", e.TronWallet.ID)
	e.SolanaWallet = storetest.Wallet(t, s, domain.FamilySolana, domain.WalletStatic)
	storetest.AssignWallet(t, s, "solana", e.SolanaWallet.ID)
	return e
}

// Step advances the clock by one second (keeps created_at values distinct
// and ordered between actions).
func (e *Env) Step() { e.Clock.Advance(time.Second) }

// Settings changes the runtime settings.
func (e *Env) Settings(mod func(*domain.Settings)) domain.Settings {
	e.T.Helper()
	st, err := e.Store.GetSettings(e.Ctx)
	if err != nil {
		e.T.Fatalf("invoicetest: get settings: %v", err)
	}
	mod(&st)
	out, err := e.Store.PutSettings(e.Ctx, st)
	if err != nil {
		e.T.Fatalf("invoicetest: put settings: %v", err)
	}
	return out
}

// Create creates an invoice for the environment's merchant.
func (e *Env) Create(amount, currency string, mods ...func(*invoice.CreateParams)) store.InvoiceView {
	e.T.Helper()
	p := invoice.CreateParams{Amount: amount, Currency: currency}
	for _, m := range mods {
		m(&p)
	}
	v, _, err := e.Invoices.Create(e.Ctx, e.Merchant, p)
	if err != nil {
		e.T.Fatalf("invoicetest: create invoice: %v", err)
	}
	e.Step()
	return v
}

// Select selects an asset for an invoice and returns the quote.
func (e *Env) Select(invoiceID uuid.UUID, assetID string) domain.Quote {
	e.T.Helper()
	q, err := e.Invoices.SelectAsset(e.Ctx, invoiceID, assetID)
	if err != nil {
		e.T.Fatalf("invoicetest: select asset %s: %v", assetID, err)
	}
	e.Step()
	return q
}

// Quoted creates an invoice and selects assetID for it.
func (e *Env) Quoted(amount, currency, assetID string) (store.InvoiceView, domain.Quote) {
	e.T.Helper()
	v := e.Create(amount, currency)
	q := e.Select(v.ID, assetID)
	return v, q
}

// Invoice loads an invoice.
func (e *Env) Invoice(id uuid.UUID) domain.Invoice {
	e.T.Helper()
	inv, err := e.Store.GetInvoice(e.Ctx, id)
	if err != nil {
		e.T.Fatalf("invoicetest: get invoice: %v", err)
	}
	return inv
}

// Quote loads a quote.
func (e *Env) Quote(id uuid.UUID) domain.Quote {
	e.T.Helper()
	q, err := e.Store.GetQuote(e.Ctx, id)
	if err != nil {
		e.T.Fatalf("invoicetest: get quote: %v", err)
	}
	return q
}

// Reevaluate runs the crediting core for an invoice in its own transaction.
func (e *Env) Reevaluate(invoiceID uuid.UUID) invoice.Evaluation {
	e.T.Helper()
	var ev invoice.Evaluation
	err := e.Store.WithTx(e.Ctx, func(tx *store.Store) error {
		var err error
		ev, err = e.Invoices.Reevaluate(e.Ctx, tx, invoiceID)
		return err
	})
	if err != nil {
		e.T.Fatalf("invoicetest: reevaluate: %v", err)
	}
	e.Step()
	return ev
}

// Credit records a transfer of amount base units to quote q with the given
// status (as the scanner would) and re-evaluates the invoice.
func (e *Env) Credit(q domain.Quote, amount int64, status domain.TransferStatus) domain.Transfer {
	e.T.Helper()
	t := storetest.TransferFor(e.T, e.Store, q, amount, func(x *domain.Transfer) { x.Status = status })
	if status == domain.TransferConfirmed {
		var err error
		if t, err = e.Store.SetTransferStatus(e.Ctx, t.ID, status, 1); err != nil {
			e.T.Fatalf("invoicetest: confirm transfer: %v", err)
		}
	}
	e.Reevaluate(q.InvoiceID)
	return t
}

// SetTransfer changes a transfer's status (confirmed / orphaned / pending)
// and re-evaluates its invoice, if any.
func (e *Env) SetTransfer(t domain.Transfer, status domain.TransferStatus) domain.Transfer {
	e.T.Helper()
	out, err := e.Store.SetTransferStatus(e.Ctx, t.ID, status, t.Confirmations)
	if err != nil {
		e.T.Fatalf("invoicetest: set transfer status: %v", err)
	}
	if out.InvoiceID != nil {
		e.Reevaluate(*out.InvoiceID)
	}
	return out
}

// Events returns the webhook events of an invoice, oldest first.
func (e *Env) Events(invoiceID uuid.UUID) []domain.WebhookEvent {
	e.T.Helper()
	evs, err := e.Store.ListWebhookEventsByInvoice(e.Ctx, invoiceID)
	if err != nil {
		e.T.Fatalf("invoicetest: list webhook events: %v", err)
	}
	slices.Reverse(evs)
	return evs
}

// EventTypes returns the event types of an invoice's webhook events, oldest
// first (never nil).
func (e *Env) EventTypes(invoiceID uuid.UUID) []string {
	e.T.Helper()
	out := []string{}
	for _, ev := range e.Events(invoiceID) {
		out = append(out, ev.EventType)
	}
	return out
}

// Envelope decodes the payload of a webhook event.
func (e *Env) Envelope(ev domain.WebhookEvent) view.WebhookEnvelope {
	e.T.Helper()
	var env view.WebhookEnvelope
	if err := json.Unmarshal(ev.Payload, &env); err != nil {
		e.T.Fatalf("invoicetest: decode webhook payload: %v", err)
	}
	return env
}

// Base parses a whole-unit decimal string into base units.
func Base(amount string, decimals int) *big.Int {
	d := decimal.RequireFromString(amount).Shift(int32(decimals))
	if !d.IsInteger() {
		panic("invoicetest: " + amount + " has too many decimals")
	}
	return d.BigInt()
}
