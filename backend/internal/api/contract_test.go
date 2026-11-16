package api

import (
	"encoding/json"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet/hdwallettest"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/store/storetest"
)

// contractRun checks real responses against docs/api.md.
type contractRun struct {
	t        *testing.T
	doc      *contractDoc
	envelope *tsType
	seen     map[string]bool
}

// ok checks a success response of route ("METHOD /documented/path"):
// status as documented (200 unless stated) and body of the documented type
// (exact key sets, recursively).
func (c *contractRun) ok(route string, r *response) map[string]any {
	c.t.Helper()
	spec, found := c.doc.routes[route]
	require.True(c.t, found, "route %s is not in docs/api.md", route)
	c.seen[route] = true
	want := spec.Status
	if want == 0 {
		want = http.StatusOK
	}
	require.Equal(c.t, want, r.Code, "%s: %s", route, r.Body)
	if spec.Type == nil {
		assert.Empty(c.t, r.Body, "%s has no body", route)
		return nil
	}
	var v any
	require.NoError(c.t, json.Unmarshal(r.Body, &v), "%s: %s", route, r.Body)
	errs := c.doc.check(route, v, spec.Type)
	assert.Empty(c.t, errs, "%s: response does not match docs/api.md (%s)\n%s", route, spec.Raw, r.Body)
	m, _ := v.(map[string]any)
	return m
}

// fail checks an error response against the documented envelope.
func (c *contractRun) fail(name string, r *response, status int) {
	c.t.Helper()
	require.Equal(c.t, status, r.Code, "%s: %s", name, r.Body)
	var v any
	require.NoError(c.t, json.Unmarshal(r.Body, &v))
	assert.Empty(c.t, c.doc.check(name, v, c.envelope), "%s: %s", name, r.Body)
}

func nonEmpty(t *testing.T, m map[string]any, key string) {
	t.Helper()
	arr, ok := m[key].([]any)
	require.True(t, ok && len(arr) > 0, "%s must be non-empty for a meaningful contract check: %v", key, m[key])
}

// TestContractResponses calls every endpoint of docs/api.md through the real
// router and validates each response's key set and value kinds against the
// TypeScript interfaces in the document. It fails if a field is missing,
// extra, misnamed or of the wrong JSON type, or if an endpoint of the
// document was not exercised.
func TestContractResponses(t *testing.T) {
	t.Parallel()
	doc := loadContract(t)
	h := newHarness(t)
	c := &contractRun{t: t, doc: doc, envelope: doc.errorEnvelope(t), seen: map[string]bool{}}
	owner := h.newAdmin(domain.RoleOwner)

	// ---- merchant API ----
	inv := c.ok("POST /api/v1/invoices", h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{
		"amount": "10", "currency": "USD", "order_id": "C-1", "asset_id": "usdt-ethereum", "metadata": map[string]any{"k": "v"},
	}))
	invID := inv["id"].(string)
	quote := inv["quote"].(map[string]any)
	q := h.Quote(mustUUID(t, quote["id"]))
	h.Credit(q, 4_000_000, domain.TransferPending) // partial, with a transfer and a webhook event
	c.ok("GET /api/v1/invoices/{id}", h.merchant(http.MethodGet, "/api/v1/invoices/"+invID, nil))
	list := c.ok("GET /api/v1/invoices", h.merchant(http.MethodGet, "/api/v1/invoices", nil))
	nonEmpty(t, list, "items")
	pending := h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "1", "currency": "USD"}).object()
	c.ok("POST /api/v1/invoices/{id}/cancel", h.merchant(http.MethodPost, "/api/v1/invoices/"+pending["id"].(string)+"/cancel", nil))
	nonEmpty(t, c.ok("GET /api/v1/assets", h.merchant(http.MethodGet, "/api/v1/assets", nil)), "items")
	c.fail("merchant 401", h.do(http.MethodGet, "/api/v1/assets", nil), http.StatusUnauthorized)
	c.fail("merchant 400", h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{}), http.StatusBadRequest)
	c.fail("merchant 409", h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "11", "currency": "USD", "order_id": "C-1"}), http.StatusConflict)

	// ---- checkout API ----
	cv := c.ok("GET /api/checkout/{invoice_id}", h.do(http.MethodGet, "/api/checkout/"+invID, nil))
	nonEmpty(t, cv, "transfers")
	nonEmpty(t, cv, "assets")
	require.NotNil(t, cv["quote"])
	fresh := h.Create("10", "USD")
	c.ok("POST /api/checkout/{invoice_id}/quote", h.do(http.MethodPost, "/api/checkout/"+fresh.ID.String()+"/quote",
		map[string]any{"asset_id": "eth-ethereum"}))
	c.fail("checkout 404", h.do(http.MethodGet, "/api/checkout/nope", nil), http.StatusNotFound)
	c.fail("checkout 409", h.do(http.MethodPost, "/api/checkout/"+invID+"/quote", map[string]any{"asset_id": "usdt-tron"}), http.StatusConflict)

	// ---- admin: auth ----
	viewer := storetest.AdminUser(t, h.Store, domain.RoleViewer)
	login := h.do(http.MethodPost, "/api/admin/auth/login", map[string]any{"email": viewer.Email, "password": storetest.Password}, csrfHeader())
	c.ok("POST /api/admin/auth/login", login)
	vc := &adminClient{h: h, User: viewer, Token: sessionFrom(t, login).Value}
	c.ok("GET /api/admin/auth/me", vc.get("/auth/me"))
	c.ok("POST /api/admin/auth/password", vc.post("/auth/password", map[string]any{"current_password": storetest.Password, "new_password": "viewer-pass-2"}))
	c.fail("admin 403", vc.post("/merchants", map[string]any{"name": "x"}), http.StatusForbidden)
	c.ok("POST /api/admin/auth/logout", vc.post("/auth/logout", nil))
	c.fail("admin 401", vc.get("/auth/me"), http.StatusUnauthorized)

	// ---- admin: invoices, transfers ----
	c.ok("GET /api/admin/overview", owner.get("/overview"))
	nonEmpty(t, c.ok("GET /api/admin/invoices", owner.get("/invoices")), "items")
	c.ok("POST /api/admin/invoices", owner.post("/invoices", map[string]any{"merchant_id": h.Merchant.ID.String(), "amount": "3", "currency": "EUR"}))
	unmatched := storetest.Transfer(t, h.Store, func(x *domain.Transfer) {
		x.ChainID, x.AssetID, x.ToAddress, x.Amount = "ethereum", "usdt-ethereum", q.Address, big.NewInt(1_000_000)
		x.TokenContract = "0xdAC17F958D2ee523a2206206994597C13D831ec7"
	})
	nonEmpty(t, c.ok("GET /api/admin/transfers", owner.get("/transfers")), "items")
	c.ok("POST /api/admin/transfers/{id}/assign", owner.post("/transfers/"+unmatched.ID.String()+"/assign", map[string]any{"invoice_id": invID}))
	detail := c.ok("GET /api/admin/invoices/{id}", owner.get("/invoices/"+invID))
	for _, k := range []string{"quotes", "transfers", "webhook_events"} {
		nonEmpty(t, detail, k)
	}
	toCancel := h.Create("2", "USD")
	c.ok("POST /api/admin/invoices/{id}/cancel", owner.post("/invoices/"+toCancel.ID.String()+"/cancel", nil))
	c.ok("POST /api/admin/invoices/{id}/mark-paid", owner.post("/invoices/"+fresh.ID.String()+"/mark-paid", map[string]any{"note": "settled"}))
	c.fail("admin 400 fields", owner.post("/invoices/"+fresh.ID.String()+"/mark-paid", map[string]any{}), http.StatusBadRequest)

	// ---- admin: merchants ----
	nonEmpty(t, c.ok("GET /api/admin/merchants", owner.get("/merchants")), "items")
	m := c.ok("POST /api/admin/merchants", owner.post("/merchants", map[string]any{"name": "Contract Shop", "webhook_url": "https://shop.example/h"}))
	mid := m["merchant"].(map[string]any)["id"].(string)
	key := c.ok("POST /api/admin/merchants/{id}/api-keys", owner.post("/merchants/"+mid+"/api-keys", map[string]any{"name": "k"}))
	c.ok("PUT /api/admin/merchants/{id}/wallets/{chain_id}", owner.put("/merchants/"+mid+"/wallets/tron",
		map[string]any{"wallet_id": h.TronWallet.ID.String()}))
	md := c.ok("GET /api/admin/merchants/{id}", owner.get("/merchants/"+mid))
	nonEmpty(t, md, "api_keys")
	nonEmpty(t, md, "wallets")
	c.ok("PATCH /api/admin/merchants/{id}", owner.patch("/merchants/"+mid, map[string]any{"name": "Contract Shop 2"}))
	c.ok("POST /api/admin/merchants/{id}/webhook-secret", owner.post("/merchants/"+mid+"/webhook-secret", nil))
	c.ok("DELETE /api/admin/merchants/{id}/api-keys/{key_id}",
		owner.del("/merchants/"+mid+"/api-keys/"+key["api_key"].(map[string]any)["id"].(string)))

	// ---- admin: chains, assets, wallets ----
	chains := c.ok("GET /api/admin/chains", owner.get("/chains"))
	nonEmpty(t, chains, "items")
	c.ok("PATCH /api/admin/chains/{id}", owner.patch("/chains/ethereum", map[string]any{"rpc_urls": []string{"https://eth.example/v2/k"}}))
	c.ok("POST /api/admin/chains/{id}/test-rpc", owner.post("/chains/ethereum/test-rpc", map[string]any{}))
	nonEmpty(t, c.ok("GET /api/admin/assets", owner.get("/assets")), "items")
	c.ok("POST /api/admin/assets", owner.post("/assets", map[string]any{"chain_id": "polygon", "symbol": "DAI", "name": "Dai",
		"contract_address": "0x8f3Cf7ad23Cd3CaDbD9735AFf958023239c6A063", "decimals": 18, "quote_precision": 2,
		"rate_key": "dai", "is_stablecoin": true}))
	c.ok("PATCH /api/admin/assets/{id}", owner.patch("/assets/dai-polygon", map[string]any{"enabled": false}))
	wallets := c.ok("GET /api/admin/wallets", owner.get("/wallets"))
	nonEmpty(t, wallets, "items")
	bound := false
	for _, x := range wallets["items"].([]any) {
		if bs, _ := x.(map[string]any)["merchant_bindings"].([]any); len(bs) > 0 {
			bound = true
		}
	}
	require.True(t, bound, "a wallet with merchant bindings makes the contract check meaningful")
	c.ok("DELETE /api/admin/merchants/{id}/wallets/{chain_id}", owner.del("/merchants/"+mid+"/wallets/tron"))
	tronXpub := hdwallettest.AccountXpub(invoicetest.Mnemonic, "m/44'/195'/0'")
	c.ok("POST /api/admin/wallets/preview", owner.post("/wallets/preview", map[string]any{"family": "tron", "kind": "xpub", "xpub": tronXpub, "chain_id": "tron"}))
	w := c.ok("POST /api/admin/wallets", owner.post("/wallets", map[string]any{"name": "Tron xpub", "family": "tron", "kind": "xpub", "xpub": tronXpub}))
	c.fail("wallet 409 duplicate key", owner.post("/wallets", map[string]any{"name": "Again", "family": "tron", "kind": "xpub", "xpub": tronXpub}), http.StatusConflict)
	c.ok("PATCH /api/admin/wallets/{id}", owner.patch("/wallets/"+w["id"].(string), map[string]any{"name": "Tron xpub 2"}))
	c.ok("DELETE /api/admin/wallets/{id}", owner.del("/wallets/"+w["id"].(string)))

	// ---- admin: webhooks, settings, users, audit ----
	events := c.ok("GET /api/admin/webhook-events", owner.get("/webhook-events"))
	nonEmpty(t, events, "items")
	evID := events["items"].([]any)[0].(map[string]any)["id"].(string)
	c.ok("POST /api/admin/webhook-events/{id}/retry", owner.post("/webhook-events/"+evID+"/retry", nil))
	st := c.ok("GET /api/admin/settings", owner.get("/settings"))
	c.ok("PUT /api/admin/settings", owner.put("/settings", st))
	c.fail("settings 400", owner.put("/settings", map[string]any{}), http.StatusBadRequest)
	nonEmpty(t, c.ok("GET /api/admin/users", owner.get("/users")), "items")
	u := c.ok("POST /api/admin/users", owner.post("/users", map[string]any{"email": "contract@example.com", "password": "contract-pass", "role": "viewer"}))
	c.ok("PATCH /api/admin/users/{id}", owner.patch("/users/"+u["id"].(string), map[string]any{"role": "admin"}))
	c.ok("DELETE /api/admin/users/{id}", owner.del("/users/"+u["id"].(string)))
	nonEmpty(t, c.ok("GET /api/admin/audit-logs", owner.get("/audit-logs")), "items")

	// ---- router errors ----
	c.fail("404 route", h.do(http.MethodGet, "/api/nope", nil), http.StatusNotFound)
	c.fail("405", h.do(http.MethodPut, "/healthz", nil), http.StatusMethodNotAllowed)

	var missing []string
	for route := range doc.routes {
		if !c.seen[route] {
			missing = append(missing, route)
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "documented endpoints not exercised by the contract test:\n%s", strings.Join(missing, "\n"))
}
