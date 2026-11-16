package api

import (
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/address"
	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet/hdwallettest"
	"cryptopay/internal/invoice/invoicetest"
	"cryptopay/internal/store/storetest"
)

// bip32Xprv is the BIP-32 test vector 1 master PRIVATE key.
const bip32Xprv = "xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi"

func TestAdminMerchantsAndSecretsShownOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)

	e := a.post("/merchants", map[string]any{"name": "", "webhook_url": "ftp://x"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "name")
	assert.Contains(t, e.Fields, "webhook_url")

	var created struct {
		Merchant      map[string]any `json:"merchant"`
		WebhookSecret string         `json:"webhook_secret"`
	}
	a.post("/merchants", map[string]any{"name": "Acme", "webhook_url": "https://acme.example/hook?token=abc123"}).
		status(http.StatusCreated).decode(&created)
	require.True(t, strings.HasPrefix(created.WebhookSecret, "whsec_"))
	assert.Equal(t, true, created.Merchant["has_webhook_secret"])
	id := created.Merchant["id"].(string)

	var key struct {
		APIKey map[string]any `json:"api_key"`
		Key    string         `json:"key"`
	}
	a.post("/merchants/"+id+"/api-keys", map[string]any{"name": "backend"}).status(http.StatusCreated).decode(&key)
	require.True(t, strings.HasPrefix(key.Key, "sk_live_"))
	assert.True(t, strings.HasPrefix(key.Key, key.APIKey["prefix"].(string)))
	a.post("/merchants/"+id+"/api-keys", map[string]any{}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	a.post("/merchants/"+uuid.NewString()+"/api-keys", map[string]any{"name": "x"}).apiError(http.StatusNotFound, CodeNotFound)

	// The new key works on the merchant API.
	h.do(http.MethodGet, "/api/v1/assets", nil, bearer(key.Key)).status(http.StatusOK)

	// Never shown again.
	detail := a.get("/merchants/" + id).status(http.StatusOK)
	list := a.get("/merchants?q=acme").status(http.StatusOK)
	for _, r := range []*response{detail, list} {
		assert.NotContains(t, string(r.Body), created.WebhookSecret)
		assert.NotContains(t, string(r.Body), key.Key)
		assert.NotContains(t, string(r.Body), "key_hash")
	}
	var d struct {
		Merchant map[string]any   `json:"merchant"`
		APIKeys  []map[string]any `json:"api_keys"`
	}
	detail.decode(&d)
	require.Len(t, d.APIKeys, 1)
	assert.Equal(t, "backend", d.APIKeys[0]["name"])

	// Rotation returns a new secret once.
	var rot struct {
		WebhookSecret string `json:"webhook_secret"`
	}
	a.post("/merchants/"+id+"/webhook-secret", nil).status(http.StatusOK).decode(&rot)
	assert.NotEqual(t, created.WebhookSecret, rot.WebhookSecret)
	m, err := h.Store.GetMerchant(h.Ctx, uuid.MustParse(id))
	require.NoError(t, err)
	assert.Equal(t, rot.WebhookSecret, m.WebhookSecret)

	// Disabling the merchant locks its keys out; revoking a key too.
	out := a.patch("/merchants/"+id, map[string]any{"status": "disabled", "name": "Acme Inc"}).status(http.StatusOK).object()
	assert.Equal(t, "disabled", out["status"])
	assert.Equal(t, "Acme Inc", out["name"])
	h.do(http.MethodGet, "/api/v1/assets", nil, bearer(key.Key)).apiError(http.StatusUnauthorized, CodeUnauthorized)
	a.patch("/merchants/"+id, map[string]any{"status": "active"}).status(http.StatusOK)
	a.patch("/merchants/"+id, map[string]any{"status": "paused"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	a.del("/merchants/" + id + "/api-keys/" + d.APIKeys[0]["id"].(string)).status(http.StatusNoContent)
	h.do(http.MethodGet, "/api/v1/assets", nil, bearer(key.Key)).apiError(http.StatusUnauthorized, CodeUnauthorized)
	a.del("/merchants/"+h.Merchant.ID.String()+"/api-keys/"+d.APIKeys[0]["id"].(string)).apiError(http.StatusNotFound, CodeNotFound)

	// Audit trail without secrets.
	audit := h.allAuditText()
	for _, secret := range []string{created.WebhookSecret, rot.WebhookSecret, key.Key, "abc123", "token="} {
		assert.NotContains(t, audit, secret)
	}
	for _, action := range []string{"merchant.create", "merchant.update", "api_key.create", "api_key.revoke", "merchant.rotate_webhook_secret"} {
		logs := h.auditLogs(action)
		require.NotEmpty(t, logs, action)
		assert.Equal(t, a.User.Email, logs[0].ActorEmail)
		assert.Equal(t, "192.0.2.1", logs[0].IP)
	}
}

func TestAdminChainsRPCMasking(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleOwner)

	ch := a.patch("/chains/ethereum", map[string]any{"rpc_urls": []string{
		"https://eth-mainnet.g.alchemy.com/v2/SECRETKEY",
		"https://user:pw@api.trongrid.io?apikey=SECRET2",
		"https://rpc.example.org",
	}}).status(http.StatusOK).object()
	assert.Equal(t, []any{"https://eth-mainnet.g.alchemy.com/…", "https://api.trongrid.io/…", "https://rpc.example.org"}, ch["rpc_urls"])
	assert.Equal(t, "database", ch["rpc_source"])
	assert.Equal(t, true, ch["payable"])
	assert.Contains(t, h.reg.Invalidated(), "ethereum")
	assert.Equal(t, "ethereum", <-h.changed)

	list := a.get("/chains").status(http.StatusOK)
	for _, secret := range []string{"SECRETKEY", "SECRET2", "user:pw"} {
		assert.NotContains(t, string(list.Body), secret)
		assert.NotContains(t, h.allAuditText(), secret)
	}
	var enc string
	require.NoError(t, h.Store.Pool().QueryRow(h.Ctx, `SELECT rpc_urls_enc FROM chains WHERE id = 'ethereum'`).Scan(&enc))
	assert.True(t, strings.HasPrefix(enc, "v1:"), "stored encrypted")
	assert.NotContains(t, enc, "SECRETKEY")

	// [] resets to the built-in default.
	ch = a.patch("/chains/ethereum", map[string]any{"rpc_urls": []string{}}).status(http.StatusOK).object()
	assert.Equal(t, "default", ch["rpc_source"])
	assert.Equal(t, []any{"https://ethereum-rpc.publicnode.com"}, ch["rpc_urls"])

	for _, tc := range []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"rpc_urls": []string{"ftp://x.example"}}, "rpc_urls"},
		{map[string]any{"rpc_urls": []string{"wss://x.example"}}, "rpc_urls"},
		{map[string]any{"rpc_urls": []string{"not a url"}}, "rpc_urls"},
		{map[string]any{"confirmations": 0}, "confirmations"},
		{map[string]any{"poll_interval_ms": 100}, "poll_interval_ms"},
		{map[string]any{"scan_batch": 0}, "scan_batch"},
		{map[string]any{"wallet_id": "nope"}, "wallet_id"},
		{map[string]any{"wallet_id": uuid.NewString()}, "wallet_id"},
		{map[string]any{"wallet_id": h.TronWallet.ID.String()}, "wallet_id"}, // wrong family
		{map[string]any{"enabled": "yes"}, "enabled"},
	} {
		e := a.patch("/chains/ethereum", tc.body).apiError(http.StatusBadRequest, CodeInvalidRequest)
		assert.Contains(t, e.Fields, tc.field, "%v", tc.body)
	}
	a.patch("/chains/nope", map[string]any{"enabled": true}).apiError(http.StatusNotFound, CodeNotFound)

	// Wallet assignment.
	ch = a.patch("/chains/base", map[string]any{"wallet_id": h.EVMWallet.ID.String(), "confirmations": 5, "poll_interval_ms": 2000, "scan_batch": 10}).
		status(http.StatusOK).object()
	assert.Equal(t, h.EVMWallet.ID.String(), ch["wallet_id"])
	assert.Equal(t, true, ch["payable"])
	assert.EqualValues(t, 5, ch["confirmations"])
	assert.EqualValues(t, 2000, ch["poll_interval_ms"])
	ch = a.patch("/chains/base", map[string]any{"wallet_id": nil}).status(http.StatusOK).object()
	assert.Nil(t, ch["wallet_id"])
	assert.Equal(t, false, ch["payable"])
	// A static Bitcoin address is not valid on Litecoin.
	btcStatic := storetest.Wallet(t, h.Store, domain.FamilyUTXO, domain.WalletStatic)
	e := a.patch("/chains/litecoin", map[string]any{"wallet_id": btcStatic.ID.String()}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields["wallet_id"], "not valid")
	a.patch("/chains/bitcoin", map[string]any{"wallet_id": btcStatic.ID.String()}).status(http.StatusOK)

	logs := h.auditLogs("chain.update")
	require.NotEmpty(t, logs)
}

func TestAdminChainRPCSourceEnv(t *testing.T) {
	// Not parallel: t.Setenv.
	t.Setenv("RPC_SEPOLIA", "https://sepolia.infura.io/v3/ENVSECRET")
	h := newHarness(t)
	a := h.newAdmin(domain.RoleViewer)
	var res itemsJSON[map[string]any]
	a.get("/chains").status(http.StatusOK).decode(&res)
	found := false
	for _, c := range res.Items {
		if c["id"] == "sepolia" {
			found = true
			assert.Equal(t, "env", c["rpc_source"])
			assert.Equal(t, []any{"https://sepolia.infura.io/…"}, c["rpc_urls"])
			assert.Equal(t, false, c["payable"], "disabled testnet")
		}
	}
	assert.True(t, found)
}

func TestAdminTestRPC(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)
	res := a.post("/chains/ethereum/test-rpc", nil).status(http.StatusOK).object()
	assert.Equal(t, map[string]any{"ok": true, "head": float64(123456), "latency_ms": float64(42), "error": ""}, res)
	a.post("/chains/ethereum/test-rpc", map[string]any{"rpc_url": "https://eth.example/v2/KEY"}).status(http.StatusOK)
	assert.Equal(t, []string{"", "https://eth.example/v2/KEY"}, h.reg.probes)

	h.reg.err = errProbe
	res = a.post("/chains/ethereum/test-rpc", map[string]any{}).status(http.StatusOK).object()
	assert.Equal(t, false, res["ok"])
	assert.NotEmpty(t, res["error"])
	assert.NotContains(t, res["error"], "SECRETKEY")

	a.post("/chains/ethereum/test-rpc", map[string]any{"rpc_url": "gopher://x"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	a.post("/chains/nope/test-rpc", map[string]any{}).apiError(http.StatusNotFound, CodeNotFound)
	assert.NotContains(t, h.allAuditText(), "KEY\"")
}

func TestAdminAssets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)
	var list itemsJSON[map[string]any]
	a.get("/assets").status(http.StatusOK).decode(&list)
	byID := map[string]map[string]any{}
	for _, x := range list.Items {
		byID[x["id"].(string)] = x
	}
	require.Contains(t, byID, "usdt-tron")
	assert.Equal(t, "1", byID["usdt-tron"]["rate_usd"])
	assert.Equal(t, "50000", byID["btc-bitcoin"]["rate_usd"])
	assert.Nil(t, byID["ltc-litecoin"]["rate_usd"], "no rate configured")
	assert.EqualValues(t, 2, byID["usdt-tron"]["quote_precision"])

	body := map[string]any{"chain_id": "ethereum", "symbol": "PYUSD", "name": "PayPal USD",
		"contract_address": "0x6c3ea9036406852006290770bedfcaba0e23a0e8", "decimals": 6, "quote_precision": 2,
		"rate_key": "paypal-usd", "is_stablecoin": true}
	created := a.post("/assets", body).status(http.StatusCreated).object()
	assert.Equal(t, "pyusd-ethereum", created["id"])
	want, err := address.ParseEVM("0x6c3ea9036406852006290770bedfcaba0e23a0e8")
	require.NoError(t, err)
	assert.Equal(t, want, created["contract_address"], "EIP-55")
	assert.NotEqual(t, strings.ToLower(want), want)
	assert.Equal(t, true, created["enabled"])
	assert.Nil(t, created["rate_usd"])
	a.post("/assets", body).apiError(http.StatusConflict, CodeConflict)
	body["symbol"] = "PYUSD2"
	e := a.post("/assets", body).apiError(http.StatusConflict, CodeConflict)
	assert.Contains(t, e.Fields, "contract_address")

	for _, tc := range []struct {
		mod   func(map[string]any)
		field string
	}{
		{func(m map[string]any) { m["chain_id"] = "nope" }, "chain_id"},
		{func(m map[string]any) { m["contract_address"] = "0x123" }, "contract_address"},
		{func(m map[string]any) { m["contract_address"] = "" }, "contract_address"},
		{func(m map[string]any) { m["quote_precision"] = 7 }, "quote_precision"},
		{func(m map[string]any) { delete(m, "decimals") }, "decimals"},
		{func(m map[string]any) { m["symbol"] = "BAD SYMBOL" }, "symbol"},
		{func(m map[string]any) { m["rate_key"] = "" }, "rate_key"},
	} {
		b := map[string]any{"chain_id": "tron", "symbol": "XT", "name": "X token", "contract_address": "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf",
			"decimals": 6, "quote_precision": 2, "rate_key": "x", "is_stablecoin": false}
		tc.mod(b)
		e := a.post("/assets", b).apiError(http.StatusBadRequest, CodeInvalidRequest)
		assert.Contains(t, e.Fields, tc.field)
	}

	upd := a.patch("/assets/pyusd-ethereum", map[string]any{"enabled": false, "sort_order": 5, "name": "PYUSD", "rate_key": "tether"}).
		status(http.StatusOK).object()
	assert.Equal(t, false, upd["enabled"])
	assert.EqualValues(t, 5, upd["sort_order"])
	assert.Equal(t, "1", upd["rate_usd"])
	a.patch("/assets/pyusd-ethereum", map[string]any{"quote_precision": 9}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	a.patch("/assets/nope", map[string]any{"enabled": true}).apiError(http.StatusNotFound, CodeNotFound)
	assert.NotEmpty(t, h.auditLogs("asset.create"))
	assert.NotEmpty(t, h.auditLogs("asset.update"))
}

func TestAdminWallets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)
	evmXpub := hdwallettest.AccountXpub(invoicetest.Mnemonic, "m/44'/60'/0'")

	var prev struct {
		Addresses []string `json:"addresses"`
	}
	a.post("/wallets/preview", map[string]any{"family": "evm", "kind": "xpub", "xpub": evmXpub, "chain_id": "base"}).
		status(http.StatusOK).decode(&prev)
	assert.Equal(t, []string{
		"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		"0x70997970C51812dc3A010C7d01b50e0d17dc79C8",
		"0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
	}, prev.Addresses)
	a.post("/wallets/preview", map[string]any{"family": "tron", "kind": "static", "address": "41" + strings.Repeat("0", 40), "chain_id": "tron"}).
		status(http.StatusOK).decode(&prev)
	require.Len(t, prev.Addresses, 1)
	assert.True(t, strings.HasPrefix(prev.Addresses[0], "T"), "normalised to base58")

	for _, tc := range []struct {
		body  map[string]any
		field string
		msg   string
	}{
		{map[string]any{"family": "evm", "kind": "xpub", "xpub": bip32Xprv, "chain_id": "ethereum"}, "xpub", "PRIVATE"},
		{map[string]any{"family": "evm", "kind": "xpub", "xpub": "xpub-garbage", "chain_id": "ethereum"}, "xpub", ""},
		{map[string]any{"family": "solana", "kind": "xpub", "xpub": evmXpub, "chain_id": "solana"}, "kind", "static"},
		{map[string]any{"family": "evm", "kind": "xpub", "xpub": evmXpub, "chain_id": "tron"}, "chain_id", ""},
		{map[string]any{"family": "evm", "kind": "xpub", "xpub": evmXpub}, "chain_id", ""},
		{map[string]any{"family": "evm", "kind": "static", "address": "0x1234", "chain_id": "ethereum"}, "address", ""},
		{map[string]any{"family": "dogecoin", "kind": "static", "address": "D", "chain_id": "ethereum"}, "family", ""},
	} {
		e := a.post("/wallets/preview", tc.body).apiError(http.StatusBadRequest, CodeInvalidRequest)
		require.Contains(t, e.Fields, tc.field, "%v", tc.body)
		assert.Contains(t, e.Fields[tc.field], tc.msg)
		assert.NotContains(t, e.Fields[tc.field], bip32Xprv[10:30], "key material never echoed")
	}

	w := a.post("/wallets", map[string]any{"name": "Treasury EVM", "family": "evm", "kind": "xpub", "xpub": evmXpub}).
		status(http.StatusCreated).object()
	assert.Equal(t, []any{}, w["chain_ids"])
	assert.Equal(t, evmXpub, w["xpub"])
	assert.Equal(t, "", w["address"])
	assert.EqualValues(t, 0, w["next_index"])

	st := a.post("/wallets", map[string]any{"name": "Hot", "family": "evm", "kind": "static", "address": strings.ToLower("0x52908400098527886E0F7030069857D2E4169EE7")}).
		status(http.StatusCreated).object()
	assert.Equal(t, "0x52908400098527886E0F7030069857D2E4169EE7", st["address"])
	ltc, err := address.SegwitEncode("ltc", 0, make([]byte, 20))
	require.NoError(t, err)
	a.post("/wallets", map[string]any{"name": "LTC", "family": "utxo", "kind": "static", "address": ltc}).
		status(http.StatusCreated)
	a.post("/wallets", map[string]any{"name": "SOL", "family": "solana", "kind": "static", "address": storetest.StaticAddresses[domain.FamilySolana]}).
		status(http.StatusCreated)

	for _, body := range []map[string]any{
		{"name": "bad", "family": "evm", "kind": "xpub", "xpub": bip32Xprv},
		{"name": "bad", "family": "solana", "kind": "xpub", "xpub": evmXpub},
		{"name": "", "family": "evm", "kind": "static", "address": "0x52908400098527886E0F7030069857D2E4169EE7"},
		{"name": "bad", "family": "utxo", "kind": "static", "address": "0x52908400098527886E0F7030069857D2E4169EE7"},
		{"name": "bad", "family": "evm", "kind": "static", "address": "0x52908400098527886E0F7030069857D2E4169EE7", "xpub": evmXpub},
	} {
		a.post("/wallets", body).apiError(http.StatusBadRequest, CodeInvalidRequest)
	}
	assert.NotContains(t, h.allAuditText(), bip32Xprv)
	assert.NotContains(t, h.allAuditText(), evmXpub, "xpubs are not copied into the audit log")

	wid := w["id"].(string)
	renamed := a.patch("/wallets/"+wid, map[string]any{"name": "Renamed"}).status(http.StatusOK).object()
	assert.Equal(t, "Renamed", renamed["name"])
	a.patch("/wallets/"+wid, map[string]any{"name": " "}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	a.patch("/wallets/"+wid, map[string]any{"xpub": evmXpub}).apiError(http.StatusBadRequest, CodeInvalidRequest)

	var ws itemsJSON[map[string]any]
	a.get("/wallets").status(http.StatusOK).decode(&ws)
	assert.Len(t, ws.Items, 4+4) // invoicetest wallets + 4 created here

	// Delete: an unused wallet goes; one referenced by quotes is kept.
	a.patch("/chains/arbitrum", map[string]any{"wallet_id": wid}).status(http.StatusOK)
	a.del("/wallets/" + wid).status(http.StatusNoContent)
	c, err := h.Store.GetChain(h.Ctx, "arbitrum")
	require.NoError(t, err)
	assert.Nil(t, c.WalletID, "unassigned")
	assert.Contains(t, drain(h.changed), "arbitrum")
	h.Quoted("10", "USD", "usdt-ethereum")
	a.del("/wallets/"+h.EVMWallet.ID.String()).apiError(http.StatusConflict, CodeConflict)
	a.del("/wallets/"+uuid.NewString()).apiError(http.StatusNotFound, CodeNotFound)
	for _, action := range []string{"wallet.create", "wallet.update", "wallet.delete"} {
		assert.NotEmpty(t, h.auditLogs(action), action)
	}
}

func TestAdminInvoices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)
	other := storetest.Merchant(t, h.Store, func(m *domain.Merchant) { m.Name = "Other Shop" })

	v1, q1 := h.Quoted("10", "USD", "usdt-ethereum")
	tr := h.Credit(q1, 10_000_000, domain.TransferConfirmed) // paid
	h.Clock.Advance(time.Hour)
	created := a.post("/invoices", map[string]any{"merchant_id": other.ID.String(), "amount": "99", "currency": "EUR", "order_id": "ADM-1"}).
		status(http.StatusCreated).object()
	assert.Equal(t, "Other Shop", created["merchant_name"])
	assert.Equal(t, other.ID.String(), created["merchant_id"])
	a.post("/invoices", map[string]any{"merchant_id": other.ID.String(), "amount": "99", "currency": "EUR", "order_id": "ADM-1"}).
		status(http.StatusOK)
	e := a.post("/invoices", map[string]any{"merchant_id": uuid.NewString(), "amount": "1", "currency": "USD"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "merchant_id")
	e = a.post("/invoices", map[string]any{"amount": "1", "currency": "USD"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "merchant_id")
	e = a.post("/invoices", map[string]any{"merchant_id": other.ID.String(), "amount": "0", "currency": "USD"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "amount")

	var p pageJSON[map[string]any]
	a.get("/invoices").status(http.StatusOK).decode(&p)
	assert.Equal(t, 2, p.Total)
	a.get("/invoices?status=paid").status(http.StatusOK).decode(&p)
	require.Equal(t, 1, p.Total)
	assert.Equal(t, v1.ID.String(), p.Items[0]["id"])
	assert.Equal(t, h.Merchant.Name, p.Items[0]["merchant_name"])
	a.get("/invoices?merchant_id=" + other.ID.String()).status(http.StatusOK).decode(&p)
	require.Equal(t, 1, p.Total)
	for _, q := range []string{"ADM-1", strings.ToLower(q1.Address), tr.TxHash, v1.ID.String()} {
		a.get("/invoices?q=" + url.QueryEscape(q)).status(http.StatusOK).decode(&p)
		assert.Equal(t, 1, p.Total, q)
	}
	from := invoicetest.Start.Add(30 * time.Minute).Format(time.RFC3339)
	a.get("/invoices?from=" + url.QueryEscape(from)).status(http.StatusOK).decode(&p)
	assert.Equal(t, 1, p.Total)
	a.get("/invoices?to=" + url.QueryEscape(from)).status(http.StatusOK).decode(&p)
	assert.Equal(t, 1, p.Total)
	for _, bad := range []string{"merchant_id=x", "from=yesterday", "status=done", "page_size=0",
		"from=2026-03-02T00:00:00Z&to=2026-03-01T00:00:00Z"} {
		a.get("/invoices?"+bad).apiError(http.StatusBadRequest, CodeInvalidRequest)
	}

	var d map[string]any
	a.get("/invoices/" + v1.ID.String()).status(http.StatusOK).decode(&d)
	assert.Equal(t, "paid", d["status"])
	assert.Len(t, d["quotes"], 1)
	assert.Len(t, d["transfers"], 1)
	events := d["webhook_events"].([]any)
	require.NotEmpty(t, events)
	ev := events[0].(map[string]any)
	assert.Equal(t, "invoice.paid", ev["event_type"])
	assert.IsType(t, map[string]any{}, ev["payload"])
	a.get("/invoices/"+uuid.NewString()).apiError(http.StatusNotFound, CodeNotFound)
	a.get("/invoices/nope").apiError(http.StatusNotFound, CodeNotFound)

	// Cancel / mark paid.
	cid := created["id"].(string)
	a.post("/invoices/"+v1.ID.String()+"/cancel", nil).apiError(http.StatusConflict, CodeConflict)
	a.post("/invoices/"+v1.ID.String()+"/mark-paid", map[string]any{"note": "again"}).apiError(http.StatusConflict, CodeConflict)
	v3 := h.Create("5", "USD")
	a.post("/invoices/"+v3.ID.String()+"/mark-paid", map[string]any{}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	paid := a.post("/invoices/"+v3.ID.String()+"/mark-paid", map[string]any{"note": "paid by bank transfer"}).status(http.StatusOK).object()
	assert.Equal(t, "paid", paid["status"])
	assert.Equal(t, h.Merchant.Name, paid["merchant_name"])
	out := a.post("/invoices/"+cid+"/cancel", nil).status(http.StatusOK).object()
	assert.Equal(t, "cancelled", out["status"])
	a.post("/invoices/"+cid+"/mark-paid", map[string]any{"note": "x"}).apiError(http.StatusConflict, CodeConflict)

	logs := h.auditLogs("invoice.mark_paid")
	require.Len(t, logs, 1)
	assert.Contains(t, string(logs[0].Detail), "paid by bank transfer")
	assert.NotEmpty(t, h.auditLogs("invoice.cancel"))
	assert.Len(t, h.auditLogs("invoice.create"), 1, "replay is not audited")
}

func TestAdminTransfers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)
	v, q := h.Quoted("10", "USD", "usdt-tron")
	unmatched := storetest.Transfer(t, h.Store, func(x *domain.Transfer) {
		x.ChainID, x.AssetID, x.ToAddress = "tron", "usdt-tron", q.Address
		x.TokenContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
		x.Amount = big.NewInt(123)
	})
	unknownToken := storetest.Transfer(t, h.Store)

	var p pageJSON[map[string]any]
	a.get("/transfers?matched=false&chain_id=tron").status(http.StatusOK).decode(&p)
	assert.Equal(t, 2, p.Total)
	a.get("/transfers?q=" + unmatched.TxHash).status(http.StatusOK).decode(&p)
	require.Equal(t, 1, p.Total)
	assert.Nil(t, p.Items[0]["invoice_id"])
	assert.Equal(t, "USDT", p.Items[0]["symbol"])
	assert.Equal(t, "0.000123", p.Items[0]["amount"])
	a.get("/transfers?matched=maybe").apiError(http.StatusBadRequest, CodeInvalidRequest)
	a.get("/transfers?status=lost").apiError(http.StatusBadRequest, CodeInvalidRequest)

	tr := a.post("/transfers/"+unmatched.ID.String()+"/assign", map[string]any{"invoice_id": v.ID.String()}).status(http.StatusOK).object()
	assert.Equal(t, v.ID.String(), tr["invoice_id"])
	assert.Equal(t, q.ID.String(), tr["quote_id"])
	a.post("/transfers/"+unmatched.ID.String()+"/assign", map[string]any{"invoice_id": v.ID.String()}).apiError(http.StatusConflict, CodeConflict)
	a.post("/transfers/"+unknownToken.ID.String()+"/assign", map[string]any{"invoice_id": v.ID.String()}).
		apiError(http.StatusUnprocessableEntity, CodeUnprocessable)
	a.post("/transfers/"+unknownToken.ID.String()+"/assign", map[string]any{"invoice_id": "x"}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	a.post("/transfers/"+uuid.NewString()+"/assign", map[string]any{"invoice_id": v.ID.String()}).apiError(http.StatusNotFound, CodeNotFound)
	a.get("/transfers?matched=true").status(http.StatusOK).decode(&p)
	assert.Equal(t, 1, p.Total)
	assert.Len(t, h.auditLogs("transfer.assign"), 1)
}

func TestAdminWebhookEvents(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)
	v := h.Create("10", "USD")
	_, err := h.Invoices.Cancel(h.Ctx, v.ID, "test")
	require.NoError(t, err)

	var p pageJSON[map[string]any]
	a.get("/webhook-events?invoice_id=" + v.ID.String()).status(http.StatusOK).decode(&p)
	require.Equal(t, 1, p.Total)
	ev := p.Items[0]
	assert.Equal(t, "invoice.cancelled", ev["event_type"])
	payload := ev["payload"].(map[string]any)
	assert.Equal(t, ev["id"], payload["id"])
	a.get("/webhook-events?status=delivered").status(http.StatusOK).decode(&p)
	assert.Equal(t, 0, p.Total)
	a.get("/webhook-events?merchant_id=" + h.Merchant.ID.String() + "&status=pending").status(http.StatusOK).decode(&p)
	assert.Equal(t, 1, p.Total)
	a.get("/webhook-events?status=x").apiError(http.StatusBadRequest, CodeInvalidRequest)

	before := h.kicks.Load()
	out := a.post("/webhook-events/"+ev["id"].(string)+"/retry", nil).status(http.StatusOK).object()
	assert.Equal(t, "pending", out["status"])
	assert.Equal(t, before+1, h.kicks.Load(), "dispatcher kicked")
	a.post("/webhook-events/"+uuid.NewString()+"/retry", nil).apiError(http.StatusNotFound, CodeNotFound)
	assert.Len(t, h.auditLogs("webhook.retry"), 1)
}

func TestAdminSettings(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleAdmin)
	var st domain.Settings
	a.get("/settings").status(http.StatusOK).decode(&st)
	assert.Equal(t, domain.DefaultSettings(), st)

	st.CheckoutBaseURL = "https://pay.example.com/"
	st.InvoiceTTLSeconds = 900
	st.WalletConnectID = "abc123def456"
	var out domain.Settings
	a.put("/settings", st).status(http.StatusOK).decode(&out)
	assert.Equal(t, "https://pay.example.com", out.CheckoutBaseURL)
	assert.Equal(t, 900, out.InvoiceTTLSeconds)

	inv := h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "1", "currency": "USD"}).status(http.StatusCreated).object()
	assert.True(t, strings.HasPrefix(inv["checkout_url"].(string), "https://pay.example.com/pay/"))

	for field, val := range map[string]any{
		"invoice_ttl_seconds": 10, "grace_period_seconds": -1, "underpay_tolerance_bps": 5000, "rate_markup_bps": -5,
		"webhook_max_attempts": 0, "webhook_timeout_seconds": 600, "checkout_base_url": "https://pay.example.com/checkout",
		"walletconnect_project_id": "bad id!",
	} {
		body := map[string]any{"invoice_ttl_seconds": 900, "grace_period_seconds": 3600, "underpay_tolerance_bps": 50,
			"stablecoin_peg": true, "rate_markup_bps": 0, "webhook_max_attempts": 10, "webhook_timeout_seconds": 10,
			"checkout_base_url": "https://pay.example.com", "walletconnect_project_id": ""}
		body[field] = val
		e := a.put("/settings", body).apiError(http.StatusBadRequest, CodeInvalidRequest)
		assert.Contains(t, e.Fields, field)
	}
	e := a.put("/settings", map[string]any{"invoice_ttl_seconds": 900}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "stablecoin_peg")
	assert.Contains(t, e.Fields, "checkout_base_url")
	for _, u := range []string{"ftp://x", "https://user:pw@pay.example.com", "https://pay.example.com?x=1", "pay.example.com"} {
		b := st
		b.CheckoutBaseURL = u
		a.put("/settings", b).apiError(http.StatusBadRequest, CodeInvalidRequest)
	}
	logs := h.auditLogs("settings.update")
	require.Len(t, logs, 1)
	assert.Contains(t, string(logs[0].Detail), "invoice_ttl_seconds")
}

func TestAdminOverviewAndAudit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newAdmin(domain.RoleViewer)
	_, q := h.Quoted("10", "USD", "usdt-ethereum")
	h.Credit(q, 10_000_000, domain.TransferConfirmed)

	var o overviewJSON
	a.get("/overview").status(http.StatusOK).decode(&o)
	assert.Equal(t, 30, o.RangeDays)
	assert.Len(t, o.Daily, 30)
	assert.Equal(t, 1, o.Totals.PaidInvoices)
	assert.Equal(t, "10", o.Totals.VolumeUSD)
	assert.Len(t, o.ByStatus, 7)
	require.Len(t, o.ByAsset, 1)
	assert.Equal(t, "usdt-ethereum", o.ByAsset[0].AssetID)
	a.get("/overview?days=7").status(http.StatusOK).decode(&o)
	assert.Len(t, o.Daily, 7)
	for _, bad := range []string{"0", "366", "x"} {
		a.get("/overview?days="+bad).apiError(http.StatusBadRequest, CodeInvalidRequest)
	}

	owner := h.newAdmin(domain.RoleOwner)
	owner.post("/merchants", map[string]any{"name": "A"}).status(http.StatusCreated)
	owner.post("/merchants", map[string]any{"name": "B"}).status(http.StatusCreated)
	owner.put("/settings", domain.DefaultSettings()).status(http.StatusOK)
	var p pageJSON[auditLogJSON]
	a.get("/audit-logs?action=merchant.create").status(http.StatusOK).decode(&p)
	assert.Equal(t, 2, p.Total)
	assert.Equal(t, owner.User.Email, p.Items[0].ActorEmail)
	a.get("/audit-logs?actor=" + url.QueryEscape(strings.Split(owner.User.Email, "@")[0])).status(http.StatusOK).decode(&p)
	assert.Equal(t, 3, p.Total)
	a.get("/audit-logs?page_size=1&page=2").status(http.StatusOK).decode(&p)
	assert.Len(t, p.Items, 1)
	assert.Equal(t, 2, p.Page)
}

func TestRouterErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := h.do(http.MethodGet, "/nope", nil)
	r.apiError(http.StatusNotFound, CodeNotFound)
	assert.Equal(t, "nosniff", r.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", r.Header.Get("X-Frame-Options"))
	assert.NotEmpty(t, r.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "no-store", r.Header.Get("Cache-Control"))
	assert.Len(t, r.Header.Get("X-Request-ID"), 24)

	h.do(http.MethodGet, "/api/admin/nope", nil).apiError(http.StatusNotFound, CodeNotFound)
	h.do(http.MethodPost, "/healthz", nil).apiError(http.StatusMethodNotAllowed, CodeInvalidRequest)
	a := h.newAdmin(domain.RoleOwner)
	a.do(http.MethodPut, "/merchants", map[string]any{}).apiError(http.StatusMethodNotAllowed, CodeInvalidRequest)

	// A sane incoming request id is echoed.
	r = h.do(http.MethodGet, "/healthz", nil, header("X-Request-ID", "abc-123"))
	r.status(http.StatusOK)
	assert.Equal(t, "abc-123", r.Header.Get("X-Request-ID"))
	assert.Equal(t, map[string]any{"status": "ok"}, r.object())
	r = h.do(http.MethodGet, "/healthz", nil, header("X-Request-ID", "bad id <script>"))
	assert.NotEqual(t, "bad id <script>", r.Header.Get("X-Request-ID"))

	h.do(http.MethodGet, "/readyz", nil).status(http.StatusOK)
}

func TestDefaultLimitsApply(t *testing.T) {
	t.Parallel()
	l := Limits{}.withDefaults()
	assert.Equal(t, DefaultLimits, l)
	l = Limits{MerchantKey: Limit{Rate: 1, Burst: 1}}.withDefaults()
	assert.Equal(t, Limit{Rate: 1, Burst: 1}, l.MerchantKey)
	assert.Equal(t, DefaultLimits.CheckoutGet, l.CheckoutGet)
	_ = fmt.Sprint(l)
}

// drain returns what is queued on ch without blocking.
func drain(ch chan string) []string {
	var out []string
	for {
		select {
		case v := <-ch:
			out = append(out, v)
		default:
			return out
		}
	}
}
