package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet/hdwallettest"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/store/storetest"
)

// accountWallet inserts an EVM xpub wallet for an account path of the test
// mnemonic (the harness' default EVM wallet is m/44'/60'/0').
func (h *harness) accountWallet(path string) domain.Wallet {
	h.t.Helper()
	return storetest.Wallet(h.t, h.Store, domain.FamilyEVM, domain.WalletXpub, func(w *domain.Wallet) {
		w.Name = "Shop wallet " + path
		w.Xpub = hdwallettest.AccountXpub(invoicetest.Mnemonic, path)
	})
}

func TestAdminMerchantWallets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)
	m := storetest.Merchant(t, h.Store, func(m *domain.Merchant) { m.Name = "Wallet Shop" })
	w := h.accountWallet("m/44'/60'/1'")
	w2 := h.accountWallet("m/44'/60'/2'")
	base := "/merchants/" + m.ID.String()

	detail := a.get(base).status(http.StatusOK).object()
	assert.Equal(t, []any{}, detail["wallets"])

	// Set: on a chain with a default wallet and on one without.
	got := a.put(base+"/wallets/ethereum", map[string]any{"wallet_id": w.ID.String()}).status(http.StatusOK).object()
	assert.Equal(t, map[string]any{"chain_id": "ethereum", "wallet_id": w.ID.String(), "wallet_name": w.Name, "kind": "xpub"}, got)
	a.put(base+"/wallets/base", map[string]any{"wallet_id": w.ID.String()}).status(http.StatusOK)
	assert.ElementsMatch(t, []string{"ethereum", "base"}, drain(h.changed), "the scanner is nudged")
	// Upsert: replacing the wallet of a chain.
	got = a.put(base+"/wallets/ethereum", map[string]any{"wallet_id": w2.ID.String()}).status(http.StatusOK).object()
	assert.Equal(t, w2.ID.String(), got["wallet_id"])

	var md struct {
		Wallets []map[string]any `json:"wallets"`
	}
	a.get(base).status(http.StatusOK).decode(&md)
	require.Len(t, md.Wallets, 2)
	assert.Equal(t, "base", md.Wallets[0]["chain_id"], "Base before Ethereum (by name)")
	assert.Equal(t, w2.ID.String(), md.Wallets[1]["wallet_id"])

	// Wallets list the merchants using them; chains count the bindings.
	var ws itemsJSON[map[string]any]
	a.get("/wallets").status(http.StatusOK).decode(&ws)
	for _, x := range ws.Items {
		switch x["id"] {
		case w.ID.String():
			assert.Equal(t, []any{map[string]any{"merchant_id": m.ID.String(), "merchant_name": "Wallet Shop", "chain_id": "base"}}, x["merchant_bindings"])
			assert.Equal(t, []any{}, x["chain_ids"])
		case h.EVMWallet.ID.String():
			assert.Equal(t, []any{}, x["merchant_bindings"])
		}
	}
	var chains itemsJSON[map[string]any]
	a.get("/chains").status(http.StatusOK).decode(&chains)
	for _, c := range chains.Items {
		switch c["id"] {
		case "base":
			assert.EqualValues(t, 1, c["merchant_wallets"])
			assert.Equal(t, false, c["payable"], "payable describes the default wallet")
			assert.Nil(t, c["wallet_id"])
		case "ethereum":
			assert.EqualValues(t, 1, c["merchant_wallets"])
			assert.Equal(t, true, c["payable"])
		case "tron":
			assert.EqualValues(t, 0, c["merchant_wallets"])
		}
	}

	// Audit.
	set := h.auditLogs("merchant.wallet.set")
	require.Len(t, set, 3)
	var d map[string]any
	require.NoError(t, json.Unmarshal(set[0].Detail, &d))
	assert.Equal(t, map[string]any{"chain_id": "ethereum", "wallet_id": w2.ID.String(), "wallet_name": w2.Name,
		"previous_wallet_id": w.ID.String()}, d)
	assert.Equal(t, "merchant", set[0].TargetType)
	assert.Equal(t, m.ID.String(), set[0].TargetID)
	assert.Equal(t, a.User.Email, set[0].ActorEmail)
	require.NoError(t, json.Unmarshal(set[2].Detail, &d))
	assert.Nil(t, d["previous_wallet_id"])

	// Unset: 204, falls back to the default; idempotent (no second audit entry).
	a.del(base + "/wallets/ethereum").status(http.StatusNoContent)
	a.del(base + "/wallets/ethereum").status(http.StatusNoContent)
	a.del(base + "/wallets/polygon").status(http.StatusNoContent)
	unset := h.auditLogs("merchant.wallet.unset")
	require.Len(t, unset, 1)
	require.NoError(t, json.Unmarshal(unset[0].Detail, &d))
	assert.Equal(t, "ethereum", d["chain_id"])
	assert.Equal(t, w2.ID.String(), d["wallet_id"])
	a.get(base).status(http.StatusOK).decode(&md)
	require.Len(t, md.Wallets, 1)
	assert.Equal(t, "base", md.Wallets[0]["chain_id"])

	// A wallet a merchant uses cannot be deleted; the error names the merchant.
	e := a.del("/wallets/"+w.ID.String()).apiError(http.StatusConflict, CodeConflict)
	assert.Contains(t, e.Message, "Wallet Shop")
	a.del(base + "/wallets/base").status(http.StatusNoContent)
	a.del("/wallets/" + w.ID.String()).status(http.StatusNoContent)
}

func TestAdminMerchantWalletValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleOwner)
	m := storetest.Merchant(t, h.Store)
	base := "/merchants/" + m.ID.String()
	btcStatic := storetest.Wallet(t, h.Store, domain.FamilyUTXO, domain.WalletStatic)

	for _, tc := range []struct {
		chain string
		body  map[string]any
		msg   string
	}{
		{"ethereum", map[string]any{}, "required"},
		{"ethereum", map[string]any{"wallet_id": "nope"}, "wallet id"},
		{"ethereum", map[string]any{"wallet_id": uuid.NewString()}, "unknown wallet"},
		{"ethereum", map[string]any{"wallet_id": h.TronWallet.ID.String()}, "tron wallet"},        // wrong family
		{"litecoin", map[string]any{"wallet_id": btcStatic.ID.String()}, "not valid on Litecoin"}, // static address of another chain
	} {
		e := a.put(base+"/wallets/"+tc.chain, tc.body).apiError(http.StatusBadRequest, CodeInvalidRequest)
		require.Contains(t, e.Fields, "wallet_id", "%v", tc.body)
		assert.Contains(t, e.Fields["wallet_id"], tc.msg)
	}
	a.put(base+"/wallets/ethereum", map[string]any{"wallet_id": h.EVMWallet.ID.String(), "extra": 1}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
	a.put(base+"/wallets/bitcoin", map[string]any{"wallet_id": btcStatic.ID.String()}).status(http.StatusOK)

	// Unknown merchant or chain → 404 (PUT and DELETE).
	body := map[string]any{"wallet_id": h.EVMWallet.ID.String()}
	a.put("/merchants/"+uuid.NewString()+"/wallets/ethereum", body).apiError(http.StatusNotFound, CodeNotFound)
	a.put("/merchants/not-a-uuid/wallets/ethereum", body).apiError(http.StatusNotFound, CodeNotFound)
	a.put(base+"/wallets/nope", body).apiError(http.StatusNotFound, CodeNotFound)
	a.del("/merchants/"+uuid.NewString()+"/wallets/ethereum").apiError(http.StatusNotFound, CodeNotFound)
	a.del(base+"/wallets/nope").apiError(http.StatusNotFound, CodeNotFound)
	assert.Len(t, h.auditLogs("merchant.wallet.set"), 1)
}

func TestAdminMerchantWalletRoles(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	viewer := h.newAdmin(domain.RoleViewer)
	m := storetest.Merchant(t, h.Store)
	path := "/merchants/" + m.ID.String() + "/wallets/ethereum"
	body := map[string]any{"wallet_id": h.EVMWallet.ID.String()}

	viewer.put(path, body).apiError(http.StatusForbidden, CodeForbidden)
	viewer.del(path).apiError(http.StatusForbidden, CodeForbidden)
	viewer.get("/merchants/" + m.ID.String()).status(http.StatusOK)

	// CSRF header required as for every admin mutation.
	admin := h.newAdmin(domain.RoleAdmin)
	h.do(http.MethodPut, "/api/admin"+path, body, sessionCookie(admin.Token)).apiError(http.StatusForbidden, CodeForbidden)
	h.do(http.MethodDelete, "/api/admin"+path, nil, sessionCookie(admin.Token)).apiError(http.StatusForbidden, CodeForbidden)
	h.do(http.MethodPut, "/api/admin"+path, body, csrfHeader()).apiError(http.StatusUnauthorized, CodeUnauthorized)
	assert.Empty(t, h.auditLogs("merchant.wallet.set"))
	admin.put(path, body).status(http.StatusOK)
}

// TestPayableAssetsPerMerchant: the merchant API asset list and the
// checkout asset list are those of the merchant (own wallet, else default).
func TestPayableAssetsPerMerchant(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	shop, shopKey := h.otherMerchant()
	storetest.BindWallet(t, h.Store, shop.ID, "base", h.accountWallet("m/44'/60'/1'").ID) // base: no default wallet

	ids := func(r *response) []string {
		var res itemsJSON[map[string]any]
		r.status(http.StatusOK).decode(&res)
		out := []string{}
		for _, x := range res.Items {
			out = append(out, x["id"].(string))
		}
		return out
	}
	mine := ids(h.do(http.MethodGet, "/api/v1/assets", nil, bearer(shopKey)))
	assert.Contains(t, mine, "usdc-base")
	assert.Contains(t, mine, "usdt-ethereum", "the default wallets apply too")
	theirs := ids(h.merchant(http.MethodGet, "/api/v1/assets", nil))
	assert.NotContains(t, theirs, "usdc-base")
	assert.Contains(t, theirs, "usdt-ethereum")
	assert.Len(t, mine, len(theirs)+2, "usdc-base and eth-base")

	// Checkout: the invoice's merchant decides.
	checkoutAssets := func(invoiceID string) []string {
		var cv struct {
			Assets []map[string]any `json:"assets"`
		}
		h.do(http.MethodGet, "/api/checkout/"+invoiceID, nil).status(http.StatusOK).decode(&cv)
		out := []string{}
		for _, x := range cv.Assets {
			out = append(out, x["id"].(string))
		}
		return out
	}
	shopInv := h.do(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "10", "currency": "USD"}, bearer(shopKey)).
		status(http.StatusCreated).object()["id"].(string)
	plainInv := h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "10", "currency": "USD"}).
		status(http.StatusCreated).object()["id"].(string)
	assert.Equal(t, mine, checkoutAssets(shopInv))
	assert.Equal(t, theirs, checkoutAssets(plainInv))
	h.do(http.MethodPost, "/api/checkout/"+plainInv+"/quote", map[string]any{"asset_id": "usdc-base"}).
		apiError(http.StatusUnprocessableEntity, CodeUnprocessable)
	var cv struct {
		Quote map[string]any `json:"quote"`
	}
	h.do(http.MethodPost, "/api/checkout/"+shopInv+"/quote", map[string]any{"asset_id": "usdc-base"}).status(http.StatusOK).decode(&cv)
	assert.Equal(t, "base", cv.Quote["chain_id"])

	// Creating with asset_id follows the same rule.
	h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "10", "currency": "USD", "asset_id": "usdc-base"}).
		apiError(http.StatusUnprocessableEntity, CodeUnprocessable)
	h.do(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "10", "currency": "USD", "asset_id": "usdc-base"}, bearer(shopKey)).
		status(http.StatusCreated)
}
