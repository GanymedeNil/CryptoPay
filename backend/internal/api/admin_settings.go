package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/invoice"
	"cryptopay/internal/store"
)

// ---- settings ----

// Ranges accepted by PUT /settings.
const (
	maxGracePeriodSeconds  = 7 * 24 * 3600
	maxUnderpayBps         = 1000 // 10 %
	maxMarkupBps           = 2000 // 20 %
	maxWebhookAttempts     = 50
	maxWebhookTimeoutSecs  = 60
	maxWalletConnectIDLen  = 128
	maxCheckoutBaseURLSize = 512
)

var walletConnectIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]*$`)

func (a *API) adminGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := a.store.GetSettings(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *API) adminPutSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		InvoiceTTLSeconds      *int    `json:"invoice_ttl_seconds"`
		GracePeriodSeconds     *int    `json:"grace_period_seconds"`
		UnderpayToleranceBps   *int    `json:"underpay_tolerance_bps"`
		StablecoinPeg          *bool   `json:"stablecoin_peg"`
		RateMarkupBps          *int    `json:"rate_markup_bps"`
		WebhookMaxAttempts     *int    `json:"webhook_max_attempts"`
		WebhookTimeoutSeconds  *int    `json:"webhook_timeout_seconds"`
		CheckoutBaseURL        *string `json:"checkout_base_url"`
		WalletConnectProjectID *string `json:"walletconnect_project_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	intIn := func(field string, v *int, lo, hi int) int {
		if v == nil {
			f.add(field, "is required")
			return 0
		}
		if *v < lo || *v > hi {
			f.add(field, fmt.Sprintf("must be between %d and %d", lo, hi))
		}
		return *v
	}
	var next domain.Settings
	next.InvoiceTTLSeconds = intIn("invoice_ttl_seconds", body.InvoiceTTLSeconds, invoice.MinExpiresIn, invoice.MaxExpiresIn)
	next.GracePeriodSeconds = intIn("grace_period_seconds", body.GracePeriodSeconds, 0, maxGracePeriodSeconds)
	next.UnderpayToleranceBps = intIn("underpay_tolerance_bps", body.UnderpayToleranceBps, 0, maxUnderpayBps)
	next.RateMarkupBps = intIn("rate_markup_bps", body.RateMarkupBps, 0, maxMarkupBps)
	next.WebhookMaxAttempts = intIn("webhook_max_attempts", body.WebhookMaxAttempts, 1, maxWebhookAttempts)
	next.WebhookTimeoutSeconds = intIn("webhook_timeout_seconds", body.WebhookTimeoutSeconds, 1, maxWebhookTimeoutSecs)
	if body.StablecoinPeg == nil {
		f.add("stablecoin_peg", "is required")
	} else {
		next.StablecoinPeg = *body.StablecoinPeg
	}
	if body.CheckoutBaseURL == nil {
		f.add("checkout_base_url", "is required")
	} else if origin, ok := httpOrigin(*body.CheckoutBaseURL); !ok {
		f.add("checkout_base_url", "must be an http(s) origin such as https://pay.example.com (no path, query or credentials)")
	} else {
		next.CheckoutBaseURL = origin
	}
	if body.WalletConnectProjectID == nil {
		f.add("walletconnect_project_id", "is required (\"\" when not used)")
	} else {
		id := strings.TrimSpace(*body.WalletConnectProjectID)
		if len(id) > maxWalletConnectIDLen || !walletConnectIDRE.MatchString(id) {
			f.add("walletconnect_project_id", "must be a WalletConnect project id (letters, digits, - and _)")
		}
		next.WalletConnectID = id
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	var out domain.Settings
	err := a.inTx(ctx, func(tx *store.Store) error {
		prev, err := tx.GetSettings(ctx)
		if err != nil {
			return err
		}
		if out, err = tx.PutSettings(ctx, next); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "settings.update", TargetType: "settings", TargetID: "settings",
			Detail: map[string]any{"changes": settingsDiff(prev, out)}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// httpOrigin validates an http(s) origin and returns it without a trailing
// slash.
func httpOrigin(raw string) (string, bool) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s == "" || len(s) > maxCheckoutBaseURLSize {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Opaque != "" || u.ForceQuery {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// settingsDiff lists the changed settings as {field: {from, to}}.
func settingsDiff(prev, next domain.Settings) map[string]any {
	out := map[string]any{}
	pv, nv := reflect.ValueOf(prev), reflect.ValueOf(next)
	t := pv.Type()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		a, b := pv.Field(i).Interface(), nv.Field(i).Interface()
		if a != b {
			out[name] = map[string]any{"from": a, "to": b}
		}
	}
	return out
}

// ---- users (owner only) ----

func (a *API) adminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListAdminUsers(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsJSON[adminUserJSON]{Items: mapSlice(users, newAdminUserJSON)})
}

func checkEmail(f fields, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		f.add("email", "is required")
		return v
	}
	addr, err := mail.ParseAddress(v)
	if len(v) > maxEmailLen || err != nil || addr.Address != v {
		f.add("email", "must be a valid e-mail address")
	}
	return v
}

func checkRole(f fields, v string) domain.Role {
	r := domain.Role(strings.TrimSpace(v))
	if !r.Valid() {
		f.add("role", "must be owner, admin or viewer")
	}
	return r
}

func (a *API) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	email := checkEmail(f, body.Email)
	role := checkRole(f, body.Role)
	if err := auth.ValidatePassword(body.Password); err != nil {
		f.add("password", passwordMessage(err))
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	hash, err := a.hasher.Hash(body.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var u domain.AdminUser
	err = a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if u, err = tx.CreateAdminUser(ctx, domain.AdminUser{Email: email, PasswordHash: hash, Role: role, Active: true}); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "user.create", TargetType: "admin_user", TargetID: u.ID.String(),
			Detail: map[string]any{"email": email, "role": string(role)}})
	})
	if store.IsConstraint(err, store.ConstraintAdminUserEmail) {
		a.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict, Message: "a user with this e-mail already exists",
			Fields: map[string]string{"email": "already exists"}})
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAdminUserJSON(u))
}

var errLastOwner = errConflict("the last active owner cannot be demoted, deactivated or deleted")

func (a *API) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		Role     *string `json:"role"`
		Active   *bool   `json:"active"`
		Password *string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	var upd store.AdminUserUpdate
	detail := map[string]any{}
	if body.Role != nil {
		role := checkRole(f, *body.Role)
		upd.Role, detail["role"] = &role, string(role)
	}
	if body.Active != nil {
		upd.Active, detail["active"] = body.Active, *body.Active
	}
	if body.Password != nil {
		if err := auth.ValidatePassword(*body.Password); err != nil {
			f.add("password", passwordMessage(err))
		}
		detail["password_changed"] = true
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	if id == ad.User.ID && body.Active != nil && !*body.Active {
		a.fail(w, r, errConflict("you cannot deactivate your own account"))
		return
	}
	if body.Password != nil {
		hash, err := a.hasher.Hash(*body.Password)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		upd.PasswordHash = &hash
	}
	var out domain.AdminUser
	err = a.inTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetAdminUser(ctx, id)
		if err != nil {
			return err
		}
		demotes := upd.Role != nil && *upd.Role != domain.RoleOwner
		deactivates := upd.Active != nil && !*upd.Active
		if cur.Role == domain.RoleOwner && cur.Active && (demotes || deactivates) {
			n, err := tx.CountActiveOwners(ctx) // locks the owner rows
			if err != nil {
				return err
			}
			if n <= 1 {
				return errLastOwner
			}
		}
		if out, err = tx.UpdateAdminUser(ctx, id, upd); err != nil {
			return err
		}
		if deactivates || upd.PasswordHash != nil {
			keep := uuid.Nil
			if id == ad.User.ID {
				keep = ad.Session.ID // changing your own password keeps you signed in here
			}
			n, err := tx.DeleteUserSessions(ctx, id, keep)
			if err != nil {
				return err
			}
			detail["sessions_revoked"] = n
		}
		detail["email"] = cur.Email
		return a.audit(ctx, tx, nil, auditEntry{Action: "user.update", TargetType: "admin_user", TargetID: id.String(), Detail: detail})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAdminUserJSON(out))
}

func (a *API) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	if id == ad.User.ID {
		a.fail(w, r, errConflict("you cannot delete your own account"))
		return
	}
	err = a.inTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetAdminUser(ctx, id)
		if err != nil {
			return err
		}
		if cur.Role == domain.RoleOwner && cur.Active {
			n, err := tx.CountActiveOwners(ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return errLastOwner
			}
		}
		if err := tx.DeleteAdminUser(ctx, id); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "user.delete", TargetType: "admin_user", TargetID: id.String(),
			Detail: map[string]any{"email": cur.Email, "role": string(cur.Role)}})
	})
	if err != nil {
		var e *Error
		if !errors.As(err, &e) && errors.Is(err, store.ErrNotFound) {
			err = errNotFound("user not found")
		}
		a.fail(w, r, err)
		return
	}
	noContent(w)
}
