package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/config"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/store/storetest"
)

func TestMerchantAuthFailures(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	revokedKey, revoked := storetest.APIKey(t, h.Store, h.Merchant.ID)
	_, err := h.Store.RevokeAPIKey(h.Ctx, h.Merchant.ID, revokedKey.ID)
	require.NoError(t, err)
	disabled := storetest.Merchant(t, h.Store, func(m *domain.Merchant) { m.Status = domain.MerchantDisabled })
	_, disabledKey := storetest.APIKey(t, h.Store, disabled.ID)

	cases := []struct {
		name string
		opts []reqOpt
	}{
		{"missing header", nil},
		{"wrong scheme", []reqOpt{header("Authorization", "Basic "+h.MerchKey)}},
		{"garbage key", []reqOpt{bearer("sk_live_short")}},
		{"unknown key", []reqOpt{bearer("sk_live_" + strings.Repeat("A", 43))}},
		{"revoked key", []reqOpt{bearer(revoked)}},
		{"disabled merchant", []reqOpt{bearer(disabledKey)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/v1/invoices", "/api/v1/assets", "/api/v1/does-not-exist"} {
				r := h.do(http.MethodGet, path, nil, tc.opts...)
				r.apiError(http.StatusUnauthorized, CodeUnauthorized)
				assert.Contains(t, r.Header.Get("WWW-Authenticate"), "Bearer")
			}
		})
	}
	// A valid key works and is touched.
	h.merchant(http.MethodGet, "/api/v1/assets", nil).status(http.StatusOK)
	keys, err := h.Store.ListAPIKeys(h.Ctx, h.Merchant.ID)
	require.NoError(t, err)
	touched := false
	for _, k := range keys {
		touched = touched || k.LastUsedAt != nil
	}
	assert.True(t, touched, "last_used_at recorded")
}

func TestMerchantCreateInvoiceIdempotency(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	body := map[string]any{"amount": "25.50", "currency": "USD", "order_id": "ORDER-1", "title": "Pro plan",
		"metadata": map[string]any{"plan": "pro"}, "success_url": "https://shop.example/ok"}

	first := h.merchant(http.MethodPost, "/api/v1/invoices", body).status(http.StatusCreated).object()
	assert.Equal(t, "25.5", first["amount"])
	assert.Equal(t, "USD", first["currency"])
	assert.Equal(t, "ORDER-1", first["order_id"])
	assert.Equal(t, "pending", first["status"])
	assert.Equal(t, map[string]any{"plan": "pro"}, first["metadata"])
	assert.Nil(t, first["quote"])
	assert.Equal(t, "http://localhost:3000/pay/"+first["id"].(string), first["checkout_url"])

	// Same order id, amount and currency → 200 with the same invoice.
	again := h.merchant(http.MethodPost, "/api/v1/invoices", body).status(http.StatusOK).object()
	assert.Equal(t, first["id"], again["id"])

	// Different amount → 409.
	body["amount"] = "30"
	h.merchant(http.MethodPost, "/api/v1/invoices", body).apiError(http.StatusConflict, CodeConflict)

	// Another merchant may use the same order id.
	_, otherKey := h.otherMerchant()
	h.do(http.MethodPost, "/api/v1/invoices", body, bearer(otherKey)).status(http.StatusCreated)
}

func TestMerchantCreateInvoiceWithAsset(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	inv := h.merchant(http.MethodPost, "/api/v1/invoices",
		map[string]any{"amount": "10", "currency": "USD", "asset_id": "usdt-ethereum"}).status(http.StatusCreated).object()
	q, ok := inv["quote"].(map[string]any)
	require.True(t, ok, "quote created: %v", inv)
	assert.Equal(t, "usdt-ethereum", q["asset_id"])
	assert.Equal(t, "10", q["amount"])
	assert.Equal(t, "10000000", q["amount_base"])
	assert.Equal(t, "evm", q["family"])

	// An asset that is not payable → 422, nothing persisted.
	h.merchant(http.MethodPost, "/api/v1/invoices",
		map[string]any{"amount": "10", "currency": "USD", "asset_id": "usdc-base", "order_id": "X"}).
		apiError(http.StatusUnprocessableEntity, CodeUnprocessable)
	_, err := h.Store.GetInvoiceByOrderID(h.Ctx, h.Merchant.ID, "X")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestMerchantValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cases := []struct {
		name   string
		body   any
		fields []string
	}{
		{"empty body", nil, []string{"amount", "currency"}},
		{"bad amount", map[string]any{"amount": "-1", "currency": "USD"}, []string{"amount"}},
		{"amount as number", map[string]any{"amount": 10, "currency": "USD"}, []string{"amount"}},
		{"unknown currency", map[string]any{"amount": "1", "currency": "XYZ"}, []string{"currency"}},
		{"bad urls", map[string]any{"amount": "1", "currency": "USD", "success_url": "javascript:alert(1)", "cancel_url": "ftp://x"},
			[]string{"success_url", "cancel_url"}},
		{"expires_in range", map[string]any{"amount": "1", "currency": "USD", "expires_in": 5}, []string{"expires_in"}},
		{"long order id", map[string]any{"amount": "1", "currency": "USD", "order_id": strings.Repeat("x", 129)}, []string{"order_id"}},
		{"metadata not object", map[string]any{"amount": "1", "currency": "USD", "metadata": []int{1}}, []string{"metadata"}},
		{"metadata too large", map[string]any{"amount": "1", "currency": "USD", "metadata": map[string]string{"x": strings.Repeat("a", 5000)}}, []string{"metadata"}},
		{"bad email", map[string]any{"amount": "1", "currency": "USD", "customer_email": "nope"}, []string{"customer_email"}},
		{"unknown field", map[string]any{"amount": "1", "currency": "USD", "colour": "red"}, []string{"colour"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := h.merchant(http.MethodPost, "/api/v1/invoices", tc.body).apiError(http.StatusBadRequest, CodeInvalidRequest)
			for _, f := range tc.fields {
				assert.Contains(t, e.Fields, f, "fields: %v", e.Fields)
			}
		})
	}

	t.Run("malformed json", func(t *testing.T) {
		h.merchant(http.MethodPost, "/api/v1/invoices", rawBody{"application/json", `{"amount":`}).
			apiError(http.StatusBadRequest, CodeInvalidRequest)
	})
	t.Run("trailing data", func(t *testing.T) {
		h.merchant(http.MethodPost, "/api/v1/invoices", rawBody{"application/json", `{"amount":"1","currency":"USD"} {}`}).
			apiError(http.StatusBadRequest, CodeInvalidRequest)
	})
	t.Run("not an object", func(t *testing.T) {
		h.merchant(http.MethodPost, "/api/v1/invoices", rawBody{"application/json", `[1,2]`}).
			apiError(http.StatusBadRequest, CodeInvalidRequest)
	})
	t.Run("wrong content type", func(t *testing.T) {
		e := h.merchant(http.MethodPost, "/api/v1/invoices", rawBody{"text/plain", `{"amount":"1","currency":"USD"}`}).
			apiError(http.StatusBadRequest, CodeInvalidRequest)
		assert.Contains(t, e.Message, "application/json")
	})
	t.Run("body too large", func(t *testing.T) {
		big := `{"amount":"1","currency":"USD","description":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
		e := h.merchant(http.MethodPost, "/api/v1/invoices", rawBody{"application/json", big}).
			apiError(http.StatusBadRequest, CodeInvalidRequest)
		assert.Contains(t, e.Message, "larger than")
	})
}

func TestMerchantScoping(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	other, otherKey := h.otherMerchant()
	theirs := h.do(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "5", "currency": "USD"}, bearer(otherKey)).
		status(http.StatusCreated).object()
	id := theirs["id"].(string)

	h.merchant(http.MethodGet, "/api/v1/invoices/"+id, nil).apiError(http.StatusNotFound, CodeNotFound)
	h.merchant(http.MethodPost, "/api/v1/invoices/"+id+"/cancel", nil).apiError(http.StatusNotFound, CodeNotFound)
	h.merchant(http.MethodGet, "/api/v1/invoices/not-a-uuid", nil).apiError(http.StatusNotFound, CodeNotFound)

	// The owner sees it; the other merchant's list does not include ours.
	h.do(http.MethodGet, "/api/v1/invoices/"+id, nil, bearer(otherKey)).status(http.StatusOK)
	h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "1", "currency": "USD"}).status(http.StatusCreated)
	var page pageJSON[map[string]any]
	h.do(http.MethodGet, "/api/v1/invoices", nil, bearer(otherKey)).status(http.StatusOK).decode(&page)
	require.Equal(t, 1, page.Total)
	assert.Equal(t, other.ID.String(), page.Items[0]["merchant_id"])

	inv, err := h.Store.GetInvoice(h.Ctx, mustUUID(t, id))
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePending, inv.Status, "not cancelled by the wrong merchant")
}

func TestMerchantListFiltersAndPaging(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for i := range 5 {
		h.merchant(http.MethodPost, "/api/v1/invoices",
			map[string]any{"amount": "1", "currency": "USD", "order_id": fmt.Sprintf("O-%d", i)}).status(http.StatusCreated)
		h.Step()
	}
	var p pageJSON[map[string]any]
	h.merchant(http.MethodGet, "/api/v1/invoices?page=2&page_size=2", nil).status(http.StatusOK).decode(&p)
	assert.Equal(t, 5, p.Total)
	assert.Equal(t, 2, p.Page)
	assert.Equal(t, 2, p.PageSize)
	require.Len(t, p.Items, 2)
	assert.Equal(t, "O-2", p.Items[0]["order_id"], "newest first")

	h.merchant(http.MethodGet, "/api/v1/invoices?order_id=O-3", nil).status(http.StatusOK).decode(&p)
	require.Equal(t, 1, p.Total)
	assert.Equal(t, "O-3", p.Items[0]["order_id"])

	id := p.Items[0]["id"].(string)
	h.merchant(http.MethodPost, "/api/v1/invoices/"+id+"/cancel", nil).status(http.StatusOK)
	h.merchant(http.MethodGet, "/api/v1/invoices?status=cancelled", nil).status(http.StatusOK).decode(&p)
	require.Equal(t, 1, p.Total)
	assert.Equal(t, "cancelled", p.Items[0]["status"])

	for _, bad := range []string{"status=nope", "page=0", "page_size=101", "page=x"} {
		e := h.merchant(http.MethodGet, "/api/v1/invoices?"+bad, nil).apiError(http.StatusBadRequest, CodeInvalidRequest)
		assert.NotEmpty(t, e.Fields, bad)
	}
}

func TestMerchantCancel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	v, q := h.Quoted("10", "USD", "usdt-ethereum")
	h.Credit(q, 1_000_000, domain.TransferPending) // partial: no longer cancellable
	h.merchant(http.MethodPost, "/api/v1/invoices/"+v.ID.String()+"/cancel", nil).apiError(http.StatusConflict, CodeConflict)

	inv := h.merchant(http.MethodPost, "/api/v1/invoices", map[string]any{"amount": "1", "currency": "USD"}).
		status(http.StatusCreated).object()
	out := h.merchant(http.MethodPost, "/api/v1/invoices/"+inv["id"].(string)+"/cancel", nil).status(http.StatusOK).object()
	assert.Equal(t, "cancelled", out["status"])
	// A body on cancel must still be an empty object.
	h.merchant(http.MethodPost, "/api/v1/invoices/"+inv["id"].(string)+"/cancel", map[string]any{"reason": "x"}).
		apiError(http.StatusBadRequest, CodeInvalidRequest)
}

func TestMerchantAssets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var res itemsJSON[map[string]any]
	h.merchant(http.MethodGet, "/api/v1/assets", nil).status(http.StatusOK).decode(&res)
	ids := map[string]bool{}
	for _, a := range res.Items {
		ids[a["id"].(string)] = true
	}
	assert.True(t, ids["usdt-ethereum"])
	assert.True(t, ids["usdt-tron"])
	assert.False(t, ids["usdc-base"], "base has no wallet")
	assert.False(t, ids["eth-sepolia"], "testnets are disabled")
}

func TestMerchantRateLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withLimits(Limits{MerchantKey: Limit{Rate: 1, Burst: 3}}))
	for range 3 {
		h.merchant(http.MethodGet, "/api/v1/assets", nil).status(http.StatusOK)
	}
	r := h.merchant(http.MethodGet, "/api/v1/assets", nil)
	r.apiError(http.StatusTooManyRequests, CodeRateLimited)
	assert.Equal(t, "1", r.Header.Get("Retry-After"))
	// Per key: another merchant is not affected.
	_, other := h.otherMerchant()
	h.do(http.MethodGet, "/api/v1/assets", nil, bearer(other)).status(http.StatusOK)
	// Tokens refill with time.
	h.Clock.Advance(2 * time.Second)
	h.merchant(http.MethodGet, "/api/v1/assets", nil).status(http.StatusOK)
}

func TestMerchantCORS(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withConfig(func(c *config.Config) { c.CORSOrigins = []string{"https://shop.example"} }))
	r := h.do(http.MethodOptions, "/api/v1/invoices", nil, header("Origin", "https://shop.example"),
		header("Access-Control-Request-Method", "POST"))
	r.status(http.StatusNoContent)
	assert.Equal(t, "https://shop.example", r.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, r.Header.Get("Access-Control-Allow-Headers"), "Authorization")
	assert.Empty(t, r.Header.Get("Access-Control-Allow-Credentials"))

	r = h.merchant(http.MethodGet, "/api/v1/assets", nil, header("Origin", "https://evil.example"))
	r.status(http.StatusOK)
	assert.Empty(t, r.Header.Get("Access-Control-Allow-Origin"))

	// Never on the admin or checkout APIs.
	r = h.do(http.MethodGet, "/api/admin/auth/me", nil, header("Origin", "https://shop.example"))
	assert.Empty(t, r.Header.Get("Access-Control-Allow-Origin"))
}
