package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"cryptopay/internal/api"
	"cryptopay/internal/auth"
	"cryptopay/internal/chain"
	"cryptopay/internal/chain/registry"
	"cryptopay/internal/config"
	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet/hdwallettest"
	"cryptopay/internal/invoice"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/rates"
	"cryptopay/internal/scanner"
	"cryptopay/internal/scanner/scannertest"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
	"cryptopay/internal/webhook"
)

const (
	ownerEmail    = "owner@shop.example"
	ownerPassword = "e2e-owner-password"

	usdtEthereum = "0xdAC17F958D2ee523a2206206994597C13D831ec7"
	usdcSolana   = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
)

// system is the whole gateway in one process.
type system struct {
	t          *testing.T
	ctx        context.Context
	clock      *storetest.Clock
	store      *store.Store
	scanner    *scanner.Manager
	dispatcher *webhook.Dispatcher
	server     *httptest.Server
	chains     map[string]*scannertest.FakeChain
	merchantRx *receiver
}

func newSystem(t *testing.T) *system {
	t.Helper()
	ctx := context.Background()
	clock := storetest.FixedClock(invoicetest.Start)
	st := storetest.NewSeeded(t, store.WithClock(clock.Now))
	log := slog.New(slog.DiscardHandler)
	rp := rates.NewStatic(invoicetest.DefaultRates())
	invoices := invoice.New(st, rp, log, clock.Now)

	chains := map[string]*scannertest.FakeChain{
		"ethereum": scannertest.NewFakeChain(1000),
		"solana":   scannertest.NewFakeChain(250_000_000),
	}
	fake := func(c domain.Chain) (chain.Client, error) {
		if f, ok := chains[c.ID]; ok {
			return f, nil
		}
		return nil, fmt.Errorf("no fake chain for %s", c.ID)
	}
	// The real registry (built from the stored chain configuration), with
	// fake adapters instead of RPC clients.
	reg := registry.New(st, registry.Factories{domain.FamilyEVM: fake, domain.FamilySolana: fake,
		domain.FamilyTron: fake, domain.FamilyUTXO: fake})
	mgr := scanner.New(st, reg, invoices, log, clock.Now, scanner.Config{})
	disp := webhook.New(st, log, clock.Now, webhook.AllowPrivateTargets(true), webhook.WithJitter(0))

	hash, err := auth.HashPasswordCost(ownerPassword, bcrypt.MinCost)
	require.NoError(t, err)
	created, err := st.EnsureOwner(ctx, ownerEmail, hash)
	require.NoError(t, err)
	require.True(t, created)

	handler := api.New(api.Deps{
		Store: st, Invoices: invoices, Registry: reg, Rates: rp,
		Config: config.Config{Env: config.EnvDevelopment}, Logger: log, Now: clock.Now,
		Hasher: auth.Hasher{Cost: bcrypt.MinCost},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &system{t: t, ctx: ctx, clock: clock, store: st, scanner: mgr, dispatcher: disp, server: srv,
		chains: chains, merchantRx: newReceiver(t)}
}

// tick runs one scanner iteration of a chain.
func (s *system) tick(chainID string) scanner.TickResult {
	s.t.Helper()
	res, err := s.scanner.TickOnce(s.ctx, chainID)
	require.NoError(s.t, err)
	require.Empty(s.t, res.Skipped, "chain %s is not scanned", chainID)
	s.clock.Advance(time.Second)
	return res
}

// deliver runs the webhook dispatcher once and returns how many deliveries succeeded.
func (s *system) deliver() int {
	s.t.Helper()
	ok, failed, err := s.dispatcher.DeliverDue(s.ctx)
	require.NoError(s.t, err)
	require.Zero(s.t, failed, "webhook deliveries failed")
	return ok
}

// client is an HTTP client of the gateway (cookie jar for the admin API).
type client struct {
	s       *system
	http    *http.Client
	headers map[string]string
}

func (s *system) client(headers map[string]string) *client {
	jar, err := cookiejar.New(nil)
	require.NoError(s.t, err)
	return &client{s: s, http: &http.Client{Jar: jar}, headers: headers}
}

// call performs a request and decodes the JSON answer into out (when
// non-nil), requiring the status code.
func (c *client) call(method, path string, body any, wantStatus int, out any) {
	c.s.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(c.s.t, err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.s.server.URL+path, rd)
	require.NoError(c.s.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set(api.CSRFHeader, api.CSRFValue)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	require.NoError(c.s.t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(c.s.t, err)
	require.Equal(c.s.t, wantStatus, res.StatusCode, "%s %s: %s", method, path, raw)
	if out != nil {
		require.NoError(c.s.t, json.Unmarshal(raw, out), "%s %s: %s", method, path, raw)
	}
}

type obj = map[string]any

// login signs the bootstrap owner in.
func (s *system) login() *client {
	s.t.Helper()
	admin := s.client(nil)
	var me obj
	admin.call(http.MethodPost, "/api/admin/auth/login", obj{"email": ownerEmail, "password": ownerPassword}, http.StatusOK, &me)
	require.Equal(s.t, "owner", me["role"])
	return admin
}

// onboardMerchant creates a merchant with a webhook pointing at the
// receiver and an API key, through the admin API.
func (s *system) onboardMerchant(admin *client, name string) (merchant *client, webhookSecret string) {
	s.t.Helper()
	var m struct {
		Merchant      obj    `json:"merchant"`
		WebhookSecret string `json:"webhook_secret"`
	}
	admin.call(http.MethodPost, "/api/admin/merchants", obj{"name": name, "webhook_url": s.merchantRx.URL()}, http.StatusCreated, &m)
	var k struct {
		Key string `json:"key"`
	}
	admin.call(http.MethodPost, "/api/admin/merchants/"+m.Merchant["id"].(string)+"/api-keys", obj{"name": "e2e"}, http.StatusCreated, &k)
	return s.client(map[string]string{"Authorization": "Bearer " + k.Key}), m.WebhookSecret
}

// receiver is the merchant's webhook endpoint.
type receiver struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	deliveries []delivery
}

type delivery struct {
	Header http.Header
	Body   []byte
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{t: t}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.deliveries = append(r.deliveries, delivery{Header: req.Header.Clone(), Body: body})
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) URL() string { return r.srv.URL + "/webhooks/cryptopay" }

// verified returns the deliveries so far after checking each signature the
// way a merchant would (auth.Verify over timestamp + "." + raw body).
func (r *receiver) verified(secret string, now time.Time) []webhookBody {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []webhookBody
	for _, d := range r.deliveries {
		ts, err := strconv.ParseInt(d.Header.Get(auth.HeaderTimestamp), 10, 64)
		require.NoError(r.t, err)
		require.NoError(r.t, auth.Verify(secret, ts, d.Body, d.Header.Get(auth.HeaderSignature), now, auth.DefaultSignatureTolerance),
			"webhook signature must verify with the merchant's secret")
		require.Error(r.t, auth.Verify("whsec_wrong", ts, d.Body, d.Header.Get(auth.HeaderSignature), now, 0))
		var b webhookBody
		require.NoError(r.t, json.Unmarshal(d.Body, &b))
		require.Equal(r.t, b.ID, d.Header.Get(auth.HeaderDelivery))
		require.Equal(r.t, b.Type, d.Header.Get(auth.HeaderEvent))
		require.Equal(r.t, "application/json", d.Header.Get("Content-Type"))
		out = append(out, b)
	}
	return out
}

type webhookBody struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Data      obj       `json:"data"`
}

func types(bs []webhookBody) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Type)
	}
	return out
}

func quoteOf(t *testing.T, v obj) obj {
	t.Helper()
	q, ok := v["quote"].(obj)
	require.True(t, ok, "quote: %v", v["quote"])
	return q
}

func baseUnits(t *testing.T, s any) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s.(string), 10)
	require.True(t, ok)
	return n
}

// TestXpubPaymentFlow: an admin onboards an xpub wallet and a merchant, the
// merchant creates an invoice, the customer picks USDT on Ethereum on the
// checkout and pays; the scanner sees the transfer, waits for confirmations
// and the merchant receives signed invoice.confirming / invoice.paid.
func TestXpubPaymentFlow(t *testing.T) {
	t.Parallel()
	s := newSystem(t)
	admin := s.login()

	// Wallet from the (public, test-only) Hardhat mnemonic's account xpub.
	xpub := hdwallettest.AccountXpub(invoicetest.Mnemonic, "m/44'/60'/0'")
	var preview struct {
		Addresses []string `json:"addresses"`
	}
	admin.call(http.MethodPost, "/api/admin/wallets/preview", obj{"family": "evm", "kind": "xpub", "xpub": xpub, "chain_id": "ethereum"}, http.StatusOK, &preview)
	require.Len(t, preview.Addresses, 3)
	var wallet obj
	admin.call(http.MethodPost, "/api/admin/wallets", obj{"name": "Main EVM", "family": "evm", "kind": "xpub", "xpub": xpub}, http.StatusCreated, &wallet)
	var ch obj
	admin.call(http.MethodPatch, "/api/admin/chains/ethereum", obj{"wallet_id": wallet["id"], "confirmations": 3, "enabled": true}, http.StatusOK, &ch)
	require.Equal(t, true, ch["payable"])

	merchant, secret := s.onboardMerchant(admin, "E2E Shop")

	// Merchant creates the invoice.
	var inv obj
	merchant.call(http.MethodPost, "/api/v1/invoices", obj{"amount": "25", "currency": "USD", "order_id": "E2E-1",
		"title": "Order E2E-1", "success_url": "https://shop.example/thanks"}, http.StatusCreated, &inv)
	id := inv["id"].(string)
	require.Equal(t, "http://localhost:3000/pay/"+id, inv["checkout_url"])

	// Customer opens the checkout and selects USDT on Ethereum.
	var cv obj
	s.client(nil).call(http.MethodGet, "/api/checkout/"+id, nil, http.StatusOK, &cv)
	require.Equal(t, "pending", cv["invoice"].(obj)["status"])
	require.Nil(t, cv["quote"])
	found := false
	for _, a := range cv["assets"].([]any) {
		if a.(obj)["id"] == "usdt-ethereum" {
			found = true
			assert.Equal(t, "25", a.(obj)["estimated_amount"])
		}
	}
	require.True(t, found, "usdt-ethereum is payable")
	s.client(nil).call(http.MethodPost, "/api/checkout/"+id+"/quote", obj{"asset_id": "usdt-ethereum"}, http.StatusOK, &cv)
	q := quoteOf(t, cv)
	addr := q["address"].(string)
	assert.Equal(t, preview.Addresses[0], addr, "first invoice gets xpub/0/0")
	assert.Equal(t, "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", addr)
	assert.Equal(t, "25", q["amount"])
	due := baseUnits(t, q["amount_base"])
	assert.Contains(t, q["payment_uri"], "ethereum:"+usdtEthereum)

	// The chain: the first tick starts at the head, then the payment lands.
	eth := s.chains["ethereum"]
	s.tick("ethereum")
	eth.Pay(1001, "0xe2e0000000000000000000000000000000000000000000000000000000000001", addr, usdtEthereum, due)
	eth.SetHead(1001)
	res := s.tick("ethereum")
	require.Equal(t, 1, res.NewTransfers)

	s.client(nil).call(http.MethodGet, "/api/checkout/"+id, nil, http.StatusOK, &cv)
	require.Equal(t, "confirming", cv["invoice"].(obj)["status"])
	transfers := cv["transfers"].([]any)
	require.Len(t, transfers, 1)
	tr := transfers[0].(obj)
	assert.Equal(t, "pending", tr["status"])
	assert.EqualValues(t, 1, tr["confirmations"])
	assert.EqualValues(t, 3, tr["required_confirmations"])
	assert.Equal(t, "https://etherscan.io/tx/0xe2e0000000000000000000000000000000000000000000000000000000000001", tr["explorer_url"])
	require.Equal(t, 1, s.deliver())

	// Two more blocks: confirmed → paid.
	eth.Advance(2)
	res = s.tick("ethereum")
	require.Equal(t, 1, res.Confirmed)
	s.client(nil).call(http.MethodGet, "/api/checkout/"+id, nil, http.StatusOK, &cv)
	require.Equal(t, "paid", cv["invoice"].(obj)["status"])
	require.NotNil(t, cv["invoice"].(obj)["paid_at"])
	require.Equal(t, 1, s.deliver())
	require.Zero(t, s.deliver(), "nothing left to deliver")

	// The merchant received exactly confirming then paid, correctly signed.
	got := s.merchantRx.verified(secret, s.clock.Now())
	require.Equal(t, []string{domain.EventConfirming, domain.EventPaid}, types(got))
	for _, b := range got {
		assert.Equal(t, id, b.Data["id"])
		assert.Equal(t, "E2E-1", b.Data["order_id"])
	}
	assert.Equal(t, "confirming", got[0].Data["status"])
	assert.Equal(t, "paid", got[1].Data["status"])
	assert.Equal(t, "25", quoteOf(t, got[1].Data)["amount_confirmed"])

	// The merchant API and the admin console agree.
	merchant.call(http.MethodGet, "/api/v1/invoices/"+id, nil, http.StatusOK, &inv)
	assert.Equal(t, "paid", inv["status"])
	var ov struct {
		Totals struct {
			Invoices     int    `json:"invoices"`
			PaidInvoices int    `json:"paid_invoices"`
			OpenInvoices int    `json:"open_invoices"`
			VolumeUSD    string `json:"volume_usd"`
		} `json:"totals"`
		ByAsset []obj `json:"by_asset"`
	}
	admin.call(http.MethodGet, "/api/admin/overview", nil, http.StatusOK, &ov)
	assert.Equal(t, 1, ov.Totals.Invoices)
	assert.Equal(t, 1, ov.Totals.PaidInvoices)
	assert.Equal(t, 0, ov.Totals.OpenInvoices)
	assert.Equal(t, "25", ov.Totals.VolumeUSD)
	require.Len(t, ov.ByAsset, 1)
	assert.Equal(t, "usdt-ethereum", ov.ByAsset[0]["asset_id"])

	var trs struct {
		Items []obj `json:"items"`
		Total int   `json:"total"`
	}
	admin.call(http.MethodGet, "/api/admin/transfers?matched=true&chain_id=ethereum", nil, http.StatusOK, &trs)
	require.Equal(t, 1, trs.Total)
	assert.Equal(t, id, trs.Items[0]["invoice_id"])
	assert.Equal(t, "confirmed", trs.Items[0]["status"])
	var evs struct {
		Items []obj `json:"items"`
	}
	admin.call(http.MethodGet, "/api/admin/webhook-events?invoice_id="+id, nil, http.StatusOK, &evs)
	require.Len(t, evs.Items, 2)
	for _, e := range evs.Items {
		assert.Equal(t, "delivered", e["status"])
		assert.EqualValues(t, 204, e["last_status_code"])
	}
	var w struct {
		Items []obj `json:"items"`
	}
	admin.call(http.MethodGet, "/api/admin/wallets", nil, http.StatusOK, &w)
	require.Len(t, w.Items, 1)
	assert.EqualValues(t, 1, w.Items[0]["next_index"], "one address handed out")
}

// TestStaticWalletPaymentFlow: a static Solana address tells invoices apart
// by a unique amount. An exact payment pays its invoice; a payment with a
// different amount is kept unmatched until the admin assigns it.
func TestStaticWalletPaymentFlow(t *testing.T) {
	t.Parallel()
	s := newSystem(t)
	admin := s.login()
	staticAddr := storetest.StaticAddresses[domain.FamilySolana]

	var wallet obj
	admin.call(http.MethodPost, "/api/admin/wallets", obj{"name": "Solana hot", "family": "solana", "kind": "static", "address": staticAddr}, http.StatusCreated, &wallet)
	admin.call(http.MethodPatch, "/api/admin/chains/solana", obj{"wallet_id": wallet["id"]}, http.StatusOK, nil)
	merchant, secret := s.onboardMerchant(admin, "Static Shop")

	// Two invoices of the same price, both pre-selecting USDC on Solana.
	var inv1, inv2 obj
	merchant.call(http.MethodPost, "/api/v1/invoices", obj{"amount": "10", "currency": "USD", "asset_id": "usdc-solana"}, http.StatusCreated, &inv1)
	merchant.call(http.MethodPost, "/api/v1/invoices", obj{"amount": "10", "currency": "USD", "asset_id": "usdc-solana"}, http.StatusCreated, &inv2)
	q1, q2 := quoteOf(t, inv1), quoteOf(t, inv2)
	assert.Equal(t, staticAddr, q1["address"])
	assert.Equal(t, staticAddr, q2["address"])
	assert.Equal(t, "static", q2["wallet_kind"])
	due1, due2 := baseUnits(t, q1["amount_base"]), baseUnits(t, q2["amount_base"])
	require.NotEqual(t, 0, due1.Cmp(due2), "unique amounts on one static address: %s vs %s", due1, due2)
	assert.Contains(t, q2["payment_uri"], "solana:"+staticAddr)

	sol := s.chains["solana"]
	s.tick("solana")
	// Exact amount of invoice 2 → matched to invoice 2 only.
	sol.Pay(250_000_001, "5e2eSig1", staticAddr, usdcSolana, due2)
	// A payment with an amount nobody expects → unmatched.
	wrong := new(big.Int).Sub(due1, big.NewInt(1))
	sol.Pay(250_000_001, "5e2eSig2", staticAddr, usdcSolana, wrong)
	sol.SetHead(250_000_001)
	res := s.tick("solana")
	require.Equal(t, 2, res.NewTransfers)

	var got obj
	merchant.call(http.MethodGet, "/api/v1/invoices/"+inv2["id"].(string), nil, http.StatusOK, &got)
	assert.Equal(t, "paid", got["status"], "Solana needs one confirmation")
	merchant.call(http.MethodGet, "/api/v1/invoices/"+inv1["id"].(string), nil, http.StatusOK, &got)
	assert.Equal(t, "pending", got["status"])

	// The admin finds the unmatched transfer and assigns it to invoice 1;
	// it is within the underpay tolerance, so invoice 1 is paid too.
	var un struct {
		Items []obj `json:"items"`
		Total int   `json:"total"`
	}
	admin.call(http.MethodGet, "/api/admin/transfers?matched=false&chain_id=solana", nil, http.StatusOK, &un)
	require.Equal(t, 1, un.Total)
	assert.Equal(t, wrong.String(), un.Items[0]["amount_base"])
	var assigned obj
	admin.call(http.MethodPost, "/api/admin/transfers/"+un.Items[0]["id"].(string)+"/assign", obj{"invoice_id": inv1["id"]}, http.StatusOK, &assigned)
	assert.Equal(t, inv1["id"], assigned["invoice_id"])
	merchant.call(http.MethodGet, "/api/v1/invoices/"+inv1["id"].(string), nil, http.StatusOK, &got)
	assert.Equal(t, "paid", got["status"])

	s.deliver()
	bodies := s.merchantRx.verified(secret, s.clock.Now())
	byInvoice := map[string][]string{}
	for _, b := range bodies {
		byInvoice[b.Data["id"].(string)] = append(byInvoice[b.Data["id"].(string)], b.Type)
	}
	// Both events of invoice 2 were enqueued in the same tick and delivered
	// in parallel: delivery order is not guaranteed (merchants must rely on
	// data.status, not on arrival order).
	assert.ElementsMatch(t, []string{domain.EventConfirming, domain.EventPaid}, byInvoice[inv2["id"].(string)])
	assert.Equal(t, []string{domain.EventPaid}, byInvoice[inv1["id"].(string)])

	// The audit log recorded the manual assignment.
	var audit struct {
		Items []obj `json:"items"`
	}
	admin.call(http.MethodGet, "/api/admin/audit-logs?action=transfer.assign", nil, http.StatusOK, &audit)
	require.Len(t, audit.Items, 1)
	assert.Equal(t, ownerEmail, audit.Items[0]["actor_email"])
}
