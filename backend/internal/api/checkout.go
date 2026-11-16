package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// checkoutRoutes is the public checkout API. The invoice UUID is the bearer
// secret; requests are rate limited per client IP (generously for the 3 s
// polling GET, strictly for quote creation).
func (a *API) checkoutRoutes(r chi.Router) {
	r.With(a.limitByIP(a.checkoutLimit)).Get("/{id}", a.checkoutGet)
	r.With(a.limitByIP(a.quoteLimit)).Post("/{id}/quote", a.checkoutQuote)
}

func (a *API) checkoutGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.invoices.Checkout(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c.View())
}

func (a *API) checkoutQuote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		AssetID string `json:"asset_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	assetID := strings.TrimSpace(body.AssetID)
	if assetID == "" {
		a.fail(w, r, errField("asset_id", "is required"))
		return
	}
	if _, err := a.invoices.SelectAsset(ctx, id, assetID); err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.invoices.Checkout(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c.View())
}
