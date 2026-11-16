package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/store"
	"cryptopay/internal/view"
)

// merchantCtx is the authenticated caller of the merchant API.
type merchantCtx struct {
	Key      domain.APIKey
	Merchant domain.Merchant
}

func merchantFrom(ctx context.Context) merchantCtx {
	m, _ := ctx.Value(keyMerchant).(merchantCtx)
	return m
}

func (a *API) merchantRoutes(r chi.Router) {
	r.Use(a.cors, a.merchantAuth)
	r.Post("/invoices", a.merchantCreateInvoice)
	r.Get("/invoices", a.merchantListInvoices)
	r.Get("/invoices/{id}", a.merchantGetInvoice)
	r.Post("/invoices/{id}/cancel", a.merchantCancelInvoice)
	r.Get("/assets", a.merchantListAssets)
}

// merchantAuth authenticates `Authorization: Bearer sk_live_…`: the key must
// exist, not be revoked, and belong to an active merchant (401 otherwise).
// Each key is rate limited.
func (a *API) merchantAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
		token = strings.TrimSpace(token)
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			a.fail(w, r, errUnauthorized("missing API key: send Authorization: Bearer <api key>"))
			return
		}
		if !auth.LooksLikeAPIKey(token) {
			a.fail(w, r, errUnauthorized("invalid API key"))
			return
		}
		key, merchant, err := a.store.LookupAPIKey(ctx, auth.HashToken(token))
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errUnauthorized("invalid or revoked API key"))
			return
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if merchant.Status != domain.MerchantActive {
			a.fail(w, r, errUnauthorized("the merchant account is disabled"))
			return
		}
		infoFrom(ctx).MerchantID = merchant.ID.String()
		if ok, wait := a.merchantLimit.allow(key.ID.String()); !ok {
			a.rateLimited(w, r, wait)
			return
		}
		if err := a.store.TouchAPIKey(ctx, key.ID); err != nil {
			a.log.WarnContext(ctx, "touch api key failed", "request_id", requestIDFrom(ctx), "error", err)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, keyMerchant, merchantCtx{Key: key, Merchant: merchant})))
	})
}

// createInvoiceBody is `CreateInvoice` of docs/api.md.
type createInvoiceBody struct {
	Amount        string          `json:"amount"`
	Currency      string          `json:"currency"`
	OrderID       string          `json:"order_id"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	AssetID       string          `json:"asset_id"`
	SuccessURL    string          `json:"success_url"`
	CancelURL     string          `json:"cancel_url"`
	CustomerEmail string          `json:"customer_email"`
	ExpiresIn     *int            `json:"expires_in"`
	Metadata      json.RawMessage `json:"metadata"`
}

func (b createInvoiceBody) params() invoice.CreateParams {
	return invoice.CreateParams{
		Amount: b.Amount, Currency: b.Currency, OrderID: b.OrderID, Title: b.Title, Description: b.Description,
		AssetID: b.AssetID, SuccessURL: b.SuccessURL, CancelURL: b.CancelURL, CustomerEmail: b.CustomerEmail,
		ExpiresIn: b.ExpiresIn, Metadata: b.Metadata,
	}
}

func (a *API) merchantCreateInvoice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body createInvoiceBody
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	m := merchantFrom(ctx)
	v, created, err := a.invoices.Create(ctx, m.Merchant, body.params())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, snap.invoice(v))
}

func (a *API) merchantGetInvoice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	v, err := a.invoices.GetViewForMerchant(ctx, merchantFrom(ctx).Merchant.ID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snap.invoice(v))
}

func (a *API) merchantListInvoices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := newQuery(r)
	status := q.oneOf("status", invoiceStatusNames()...)
	orderID := q.str("order_id")
	page, size := q.page()
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	mid := merchantFrom(ctx).Merchant.ID
	f := store.InvoiceFilter{MerchantID: &mid, OrderID: orderID, Page: page, PageSize: size}
	if status != "" {
		f.Statuses = []domain.InvoiceStatus{domain.InvoiceStatus(status)}
	}
	p, err := a.store.ListInvoices(ctx, f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapPage(p, snap.invoice))
}

func (a *API) merchantCancelInvoice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := decodeJSON(r, &struct{}{}); err != nil {
		a.fail(w, r, err)
		return
	}
	m := merchantFrom(ctx)
	// Scope first: another merchant's invoice is "not found".
	if _, err := a.invoices.GetViewForMerchant(ctx, m.Merchant.ID, id); err != nil {
		a.fail(w, r, err)
		return
	}
	v, err := a.invoices.Cancel(ctx, id, "api_key:"+m.Key.Prefix)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	snap, err := a.snapshot(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snap.invoice(v))
}

func (a *API) merchantListAssets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	assets, err := a.store.ListPayableAssets(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cat, err := a.store.LoadCatalog(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsJSON[view.AssetInfo]{Items: view.NewAssetInfos(assets, cat)})
}

func invoiceStatusNames() []string {
	out := make([]string, 0, len(store.AllInvoiceStatuses))
	for _, s := range store.AllInvoiceStatuses {
		out = append(out, string(s))
	}
	return out
}

// snapshot is the catalog and settings needed to render invoices.
type snapshot struct {
	cat      *store.Catalog
	settings domain.Settings
}

func (a *API) snapshot(ctx context.Context) (snapshot, error) {
	cat, err := a.store.LoadCatalog(ctx)
	if err != nil {
		return snapshot{}, err
	}
	st, err := a.store.GetSettings(ctx)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{cat: cat, settings: st}, nil
}

func (s snapshot) invoice(v store.InvoiceView) view.Invoice {
	return view.NewInvoice(v.Invoice, v.Quote, s.cat, s.settings)
}
