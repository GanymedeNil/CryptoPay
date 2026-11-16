package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
)

const (
	maxNameLen = 200
	maxURLLen  = 2048
)

func (a *API) adminListMerchants(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	f := store.MerchantFilter{Q: q.str("q")}
	f.Page, f.PageSize = q.page()
	if len(f.Q) > 200 {
		q.fields.add("q", "is too long")
	}
	if err := q.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.store.ListMerchants(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapPage(p, newMerchantJSON))
}

// checkName validates a display name (required, at most max characters).
func checkName(f fields, field, v string, max int) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		f.add(field, "is required")
	case utf8.RuneCountInString(v) > max:
		f.add(field, "is too long")
	case strings.ContainsAny(v, "\x00\r\n"):
		f.add(field, "contains invalid characters")
	}
	return v
}

// checkWebhookURL validates an optional merchant webhook URL.
func checkWebhookURL(f fields, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) > maxURLLen {
		f.add("webhook_url", "is too long")
		return v
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		f.add("webhook_url", "must be an absolute http or https URL")
	}
	return v
}

func (a *API) adminCreateMerchant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Name       string `json:"name"`
		WebhookURL string `json:"webhook_url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	name := checkName(f, "name", body.Name, maxNameLen)
	hook := checkWebhookURL(f, body.WebhookURL)
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	secret := auth.NewWebhookSecret()
	var m domain.Merchant
	err := a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if m, err = tx.CreateMerchant(ctx, domain.Merchant{Name: name, WebhookURL: hook, WebhookSecret: secret,
			Status: domain.MerchantActive}); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "merchant.create", TargetType: "merchant", TargetID: m.ID.String(),
			Detail: map[string]any{"name": name, "webhook_url": maskURLIfSet(hook)}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Merchant      merchantJSON `json:"merchant"`
		WebhookSecret string       `json:"webhook_secret"`
	}{newMerchantJSON(m), secret})
}

func maskURLIfSet(u string) string {
	if u == "" {
		return ""
	}
	return maskURL(u)
}

func (a *API) adminGetMerchant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	m, err := a.store.GetMerchant(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	keys, err := a.store.ListAPIKeys(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	wallets, err := a.store.ListMerchantWallets(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Merchant merchantJSON         `json:"merchant"`
		APIKeys  []apiKeyJSON         `json:"api_keys"`
		Wallets  []merchantWalletJSON `json:"wallets"`
	}{newMerchantJSON(m), mapSlice(keys, newAPIKeyJSON), mapSlice(wallets, newMerchantWalletJSON)})
}

// merchantAndChain loads the merchant {id} and chain {chain_id} of a
// merchant wallet route (404 when either does not exist).
func (a *API) merchantAndChain(r *http.Request) (domain.Merchant, domain.Chain, error) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		return domain.Merchant{}, domain.Chain{}, err
	}
	m, err := a.store.GetMerchant(ctx, id)
	if err != nil {
		return domain.Merchant{}, domain.Chain{}, err
	}
	c, err := a.store.GetChain(ctx, chi.URLParam(r, "chain_id"))
	if err != nil {
		return domain.Merchant{}, domain.Chain{}, err
	}
	return m, c, nil
}

// adminSetMerchantWallet gives a merchant its own receiving wallet on a
// chain (replacing an earlier one). Quotes already created keep their
// wallet and address; only new quotes use it.
func (a *API) adminSetMerchantWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, c, err := a.merchantAndChain(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		WalletID string `json:"wallet_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	var wid uuid.UUID
	if raw := strings.TrimSpace(body.WalletID); raw == "" {
		f.add("wallet_id", "is required")
	} else if wid, err = uuid.Parse(raw); err != nil {
		f.add("wallet_id", "must be a wallet id")
	} else if msg := a.checkWalletForChain(ctx, c, wid); msg != "" {
		f.add("wallet_id", msg)
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	var out store.MerchantWallet
	err = a.inTx(ctx, func(tx *store.Store) error {
		var prev any
		if p, err := tx.GetMerchantWallet(ctx, m.ID, c.ID); err == nil {
			prev = p.WalletID.String()
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		var err error
		if out, err = tx.SetMerchantWallet(ctx, m.ID, c.ID, wid); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "merchant.wallet.set", TargetType: "merchant", TargetID: m.ID.String(),
			Detail: map[string]any{"chain_id": c.ID, "wallet_id": wid.String(), "wallet_name": out.WalletName, "previous_wallet_id": prev}})
	})
	if errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrForeignKey) {
		// The wallet was deleted (or the merchant removed) concurrently.
		err = errField("wallet_id", "the wallet cannot be used on this chain")
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.hooks.ChainChanged != nil {
		a.hooks.ChainChanged(c.ID)
	}
	writeJSON(w, http.StatusOK, newMerchantWalletJSON(out))
}

// adminUnsetMerchantWallet removes a merchant's own wallet on a chain: the
// merchant falls back to the chain's default wallet. Idempotent.
func (a *API) adminUnsetMerchantWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, c, err := a.merchantAndChain(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	removed := false
	err = a.inTx(ctx, func(tx *store.Store) error {
		prev, err := tx.GetMerchantWallet(ctx, m.ID, c.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // nothing bound: nothing to change or audit
		}
		if err != nil {
			return err
		}
		if removed, err = tx.DeleteMerchantWallet(ctx, m.ID, c.ID); err != nil || !removed {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "merchant.wallet.unset", TargetType: "merchant", TargetID: m.ID.String(),
			Detail: map[string]any{"chain_id": c.ID, "wallet_id": prev.WalletID.String(), "wallet_name": prev.WalletName}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if removed && a.hooks.ChainChanged != nil {
		a.hooks.ChainChanged(c.ID)
	}
	noContent(w)
}

func (a *API) adminUpdateMerchant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		Name       *string `json:"name"`
		Status     *string `json:"status"`
		WebhookURL *string `json:"webhook_url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	var upd store.MerchantUpdate
	detail := map[string]any{}
	if body.Name != nil {
		n := checkName(f, "name", *body.Name, maxNameLen)
		upd.Name, detail["name"] = &n, n
	}
	if body.Status != nil {
		st := domain.MerchantStatus(*body.Status)
		if st != domain.MerchantActive && st != domain.MerchantDisabled {
			f.add("status", "must be active or disabled")
		}
		upd.Status, detail["status"] = &st, string(st)
	}
	if body.WebhookURL != nil {
		u := checkWebhookURL(f, *body.WebhookURL)
		upd.WebhookURL, detail["webhook_url"] = &u, maskURLIfSet(u)
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	var m domain.Merchant
	err = a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if m, err = tx.UpdateMerchant(ctx, id, upd); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "merchant.update", TargetType: "merchant", TargetID: id.String(), Detail: detail})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newMerchantJSON(m))
}

func (a *API) adminCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	name := checkName(f, "name", body.Name, 100)
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	plain, prefix, hash := auth.NewAPIKey()
	var k domain.APIKey
	err = a.inTx(ctx, func(tx *store.Store) error {
		if _, err := tx.GetMerchant(ctx, id); err != nil {
			return err
		}
		var err error
		if k, err = tx.CreateAPIKey(ctx, domain.APIKey{MerchantID: id, Name: name, Prefix: prefix, KeyHash: hash}); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "api_key.create", TargetType: "api_key", TargetID: k.ID.String(),
			Detail: map[string]any{"merchant_id": id.String(), "name": name, "prefix": prefix}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		APIKey apiKeyJSON `json:"api_key"`
		Key    string     `json:"key"`
	}{newAPIKeyJSON(k), plain})
}

func (a *API) adminRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	keyID, err := pathUUID(r, "key_id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	err = a.inTx(ctx, func(tx *store.Store) error {
		k, err := tx.RevokeAPIKey(ctx, id, keyID)
		if err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "api_key.revoke", TargetType: "api_key", TargetID: keyID.String(),
			Detail: map[string]any{"merchant_id": id.String(), "name": k.Name, "prefix": k.Prefix}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

func (a *API) adminRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
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
	secret := auth.NewWebhookSecret()
	err = a.inTx(ctx, func(tx *store.Store) error {
		if _, err := tx.RotateWebhookSecret(ctx, id, secret); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "merchant.rotate_webhook_secret", TargetType: "merchant", TargetID: id.String()})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		WebhookSecret string `json:"webhook_secret"`
	}{secret})
}
