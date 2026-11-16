package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/config"
	"cryptopay/internal/domain"
	"cryptopay/internal/rates"
)

func TestCheckoutGet(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	v := h.Create("10", "USD", func(p *invoiceParams) { p.Title = "T-shirt" })
	r := h.do(http.MethodGet, "/api/checkout/"+v.ID.String(), nil).status(http.StatusOK)
	var cv struct {
		Invoice   map[string]any   `json:"invoice"`
		Assets    []map[string]any `json:"assets"`
		Quote     any              `json:"quote"`
		Transfers []any            `json:"transfers"`
	}
	r.decode(&cv)
	assert.Equal(t, "T-shirt", cv.Invoice["title"])
	assert.Equal(t, h.Merchant.Name, cv.Invoice["merchant_name"])
	assert.NotContains(t, cv.Invoice, "metadata", "no merchant-private fields")
	assert.NotContains(t, cv.Invoice, "customer_email")
	assert.Nil(t, cv.Quote)
	assert.NotEmpty(t, cv.Assets)
	assert.Equal(t, "no-store", r.Header.Get("Cache-Control"))

	h.do(http.MethodGet, "/api/checkout/not-a-uuid", nil).apiError(http.StatusNotFound, CodeNotFound)
	h.do(http.MethodGet, "/api/checkout/"+uuid.NewString(), nil).apiError(http.StatusNotFound, CodeNotFound)
	h.do(http.MethodDelete, "/api/checkout/"+v.ID.String(), nil).apiError(http.StatusMethodNotAllowed, CodeInvalidRequest)
}

func TestCheckoutQuote(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	v := h.Create("10", "USD")
	path := "/api/checkout/" + v.ID.String() + "/quote"

	e := h.do(http.MethodPost, path, map[string]any{}).apiError(http.StatusBadRequest, CodeInvalidRequest)
	assert.Contains(t, e.Fields, "asset_id")
	h.do(http.MethodPost, path, map[string]any{"asset_id": "nope"}).apiError(http.StatusUnprocessableEntity, CodeUnprocessable)
	h.do(http.MethodPost, "/api/checkout/"+uuid.NewString()+"/quote", map[string]any{"asset_id": "usdt-tron"}).
		apiError(http.StatusNotFound, CodeNotFound)

	var cv struct {
		Quote map[string]any `json:"quote"`
	}
	h.do(http.MethodPost, path, map[string]any{"asset_id": "usdt-tron"}).status(http.StatusOK).decode(&cv)
	require.NotNil(t, cv.Quote)
	assert.Equal(t, "usdt-tron", cv.Quote["asset_id"])
	assert.Equal(t, "static", cv.Quote["wallet_kind"])

	// Switch asset is allowed while no funds were seen.
	h.do(http.MethodPost, path, map[string]any{"asset_id": "usdt-ethereum"}).status(http.StatusOK).decode(&cv)
	assert.Equal(t, "usdt-ethereum", cv.Quote["asset_id"])
	q, err := h.Store.GetActiveQuote(h.Ctx, v.ID)
	require.NoError(t, err)
	h.Credit(q, 1_000_000, domain.TransferPending)
	h.do(http.MethodPost, path, map[string]any{"asset_id": "usdt-tron"}).apiError(http.StatusConflict, CodeConflict)

	// No rate → 422.
	v2 := h.Create("10", "USD")
	h.Rates.SetError(rates.ErrUnavailable)
	h.do(http.MethodPost, "/api/checkout/"+v2.ID.String()+"/quote", map[string]any{"asset_id": "eth-ethereum"}).
		apiError(http.StatusUnprocessableEntity, CodeUnprocessable)
	h.Rates.SetError(nil)

	// A closed invoice → 409.
	v3 := h.Create("10", "USD")
	_, err = h.Invoices.Cancel(h.Ctx, v3.ID, "test")
	require.NoError(t, err)
	h.do(http.MethodPost, "/api/checkout/"+v3.ID.String()+"/quote", map[string]any{"asset_id": "usdt-tron"}).
		apiError(http.StatusConflict, CodeConflict)
}

func TestCheckoutRateLimitPerIP(t *testing.T) {
	t.Parallel()
	h := newHarness(t,
		withLimits(Limits{CheckoutGet: Limit{Rate: 1, Burst: 2}, CheckoutQuote: Limit{Rate: 1, Burst: 1}}),
		withConfig(func(c *config.Config) { c.TrustProxyHeaders = true }))
	v := h.Create("10", "USD")
	get := "/api/checkout/" + v.ID.String()
	h.do(http.MethodGet, get, nil, fromIP("10.0.0.1")).status(http.StatusOK)
	h.do(http.MethodGet, get, nil, fromIP("10.0.0.1")).status(http.StatusOK)
	h.do(http.MethodGet, get, nil, fromIP("10.0.0.1")).apiError(http.StatusTooManyRequests, CodeRateLimited)
	// Another client IP behind the (trusted) proxy has its own bucket.
	h.do(http.MethodGet, get, nil, fromIP("10.0.0.1"), header("X-Forwarded-For", "203.0.113.9")).status(http.StatusOK)

	post := get + "/quote"
	h.do(http.MethodPost, post, map[string]any{"asset_id": "usdt-tron"}, fromIP("10.0.0.2")).status(http.StatusOK)
	h.do(http.MethodPost, post, map[string]any{"asset_id": "usdt-tron"}, fromIP("10.0.0.2")).
		apiError(http.StatusTooManyRequests, CodeRateLimited)
}
