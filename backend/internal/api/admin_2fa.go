package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/notify"
	"cryptopay/internal/store"
)

// ---- own two-factor authentication (docs/api.md "Two-factor authentication") ----

var (
	errTwoFactorEnabled    = errConflict("two-factor authentication is already enabled; turn it off first to set it up again")
	errTwoFactorNotEnabled = errConflict("two-factor authentication is not enabled for this account")
	errNoPendingSecret     = errConflict("start the setup first (POST /auth/2fa/setup)")
	errTwoFactorMandatory  = errConflict("two-factor authentication is required for every admin (setting require_2fa) and cannot be turned off")
)

// checkCurrentPassword verifies the caller's password for a two-factor
// change: 400 fields.current_password when it is missing or wrong (a wrong
// one counts towards the user's two-factor throttle), 429 while throttled.
func (a *API) checkCurrentPassword(ctx context.Context, user domain.AdminUser, password string) error {
	if blocked, _ := a.twoFactorFailures.blocked(user.ID.String()); blocked {
		return &Error{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: "too many failed attempts, try again later"}
	}
	if password == "" {
		return errField("current_password", "is required")
	}
	if len(password) > 1024 || a.hasher.Verify(user.PasswordHash, password) != nil {
		a.twoFactorFailures.fail(user.ID.String())
		return errField("current_password", "is incorrect")
	}
	return nil
}

// codeFailed counts a wrong code and returns the 400 fields.code error.
func (a *API) codeFailed(user domain.AdminUser) error {
	a.twoFactorFailures.fail(user.ID.String())
	return errField("code", "is incorrect or was already used")
}

// verifyCode checks a TOTP code (or, with allowRecovery, a recovery code,
// which is consumed) of the locked user inside tx. It returns the method
// used, or ok=false for a wrong code.
func (a *API) verifyCode(ctx context.Context, tx *store.Store, userID uuid.UUID, totp domain.AdminTOTP, code string, allowRecovery bool) (method string, ok bool, err error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		return "", false, nil
	}
	if step, valid := auth.VerifyTOTP(totp.Secret, code, a.now(), totp.LastStep); valid {
		if err := tx.AdvanceTOTPStep(ctx, userID, step); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return "", false, nil
			}
			return "", false, err
		}
		return methodTOTP, true, nil
	}
	if !allowRecovery {
		return "", false, nil
	}
	h := auth.HashRecoveryCode(userID.String(), code)
	if h == "" {
		return "", false, nil
	}
	if _, err := tx.UseRecoveryCode(ctx, userID, h); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return methodRecoveryCode, true, nil
}

type twoFactorSetupJSON struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
}

type recoveryCodesJSON struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// twoFactorSetup stores a new pending secret for the caller.
func (a *API) twoFactorSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		CurrentPassword string `json:"current_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	if ad.User.TwoFactorEnabled() {
		a.fail(w, r, errTwoFactorEnabled)
		return
	}
	if err := a.checkCurrentPassword(ctx, ad.User, body.CurrentPassword); err != nil {
		a.fail(w, r, err)
		return
	}
	secret := auth.NewTOTPSecret()
	err := a.inTx(ctx, func(tx *store.Store) error {
		if err := tx.SetPendingTOTPSecret(ctx, ad.User.ID, secret); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return errTwoFactorEnabled
			}
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "auth.2fa_setup", TargetType: "admin_user", TargetID: ad.User.ID.String()})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, twoFactorSetupJSON{Secret: secret, OTPAuthURI: auth.OTPAuthURI(ad.User.Email, secret)})
}

// twoFactorEnable confirms the pending secret with a code, enables
// two-factor authentication, returns fresh recovery codes (once) and signs
// out the caller's other sessions.
func (a *API) twoFactorEnable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	if strings.TrimSpace(body.Code) == "" {
		a.fail(w, r, errField("code", "is required"))
		return
	}
	ad, _ := adminFrom(ctx)
	if blocked, wait := a.twoFactorFailures.blocked(ad.User.ID.String()); blocked {
		a.rateLimited(w, r, wait)
		return
	}
	codes := auth.NewRecoveryCodes(auth.RecoveryCodeCount)
	var user domain.AdminUser
	wrong := false
	err := a.inTx(ctx, func(tx *store.Store) error {
		wrong = false
		totp, err := tx.GetAdminTOTPForUpdate(ctx, ad.User.ID)
		if err != nil {
			return err
		}
		switch {
		case totp.EnabledAt != nil:
			return errTwoFactorEnabled
		case totp.Secret == "":
			return errNoPendingSecret
		}
		step, ok := auth.VerifyTOTP(totp.Secret, body.Code, a.now(), totp.LastStep)
		if !ok {
			wrong = true
			return nil
		}
		if user, err = tx.EnableTOTP(ctx, ad.User.ID, step); err != nil {
			return err
		}
		if err := tx.ReplaceRecoveryCodes(ctx, ad.User.ID, hashCodes(ad.User.ID, codes)); err != nil {
			return err
		}
		n, err := tx.DeleteUserSessions(ctx, ad.User.ID, ad.Session.ID)
		if err != nil {
			return err
		}
		if _, err := tx.DeleteUserLoginChallenges(ctx, ad.User.ID); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, nil, auditEntry{Action: "auth.2fa_enable", TargetType: "admin_user", TargetID: ad.User.ID.String(),
			Detail: map[string]any{"other_sessions_revoked": n}}); err != nil {
			return err
		}
		a.notifySecurity(ctx, tx, user, notify.Event{Kind: notify.Kind2FAEnabled})
		return nil
	})
	if err == nil && wrong {
		err = a.codeFailed(ad.User)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
}

func hashCodes(userID uuid.UUID, codes []string) []string {
	out := make([]string, len(codes))
	for i, c := range codes {
		out[i] = auth.HashRecoveryCode(userID.String(), c)
	}
	return out
}

type passwordAndCode struct {
	CurrentPassword string `json:"current_password"`
	Code            string `json:"code"`
}

// twoFactorDisable turns the caller's two-factor authentication off
// (password + a code or recovery code); refused while require_2fa is on.
func (a *API) twoFactorDisable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body passwordAndCode
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	if required, err := a.twoFactorRequired(ctx); err != nil {
		a.fail(w, r, err)
		return
	} else if required {
		a.fail(w, r, errTwoFactorMandatory)
		return
	}
	if !ad.User.TwoFactorEnabled() {
		a.fail(w, r, errTwoFactorNotEnabled)
		return
	}
	if err := a.checkCurrentPassword(ctx, ad.User, body.CurrentPassword); err != nil {
		a.fail(w, r, err)
		return
	}
	wrong := false
	err := a.inTx(ctx, func(tx *store.Store) error {
		wrong = false
		totp, err := tx.GetAdminTOTPForUpdate(ctx, ad.User.ID)
		if err != nil {
			return err
		}
		if totp.EnabledAt == nil {
			return errTwoFactorNotEnabled
		}
		method, ok, err := a.verifyCode(ctx, tx, ad.User.ID, totp, body.Code, true)
		if err != nil {
			return err
		}
		if !ok {
			wrong = true
			return nil
		}
		if _, err := tx.ClearTOTP(ctx, ad.User.ID); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, nil, auditEntry{Action: "auth.2fa_disable", TargetType: "admin_user", TargetID: ad.User.ID.String(),
			Detail: map[string]any{"method": method}}); err != nil {
			return err
		}
		a.notifySecurity(ctx, tx, ad.User, notify.Event{Kind: notify.Kind2FADisabled})
		return nil
	})
	if err == nil && wrong {
		err = a.codeFailed(ad.User)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

// twoFactorRecoveryCodes replaces the caller's recovery codes (password + a
// current code from the authenticator app).
func (a *API) twoFactorRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body passwordAndCode
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	if !ad.User.TwoFactorEnabled() {
		a.fail(w, r, errTwoFactorNotEnabled)
		return
	}
	if err := a.checkCurrentPassword(ctx, ad.User, body.CurrentPassword); err != nil {
		a.fail(w, r, err)
		return
	}
	codes := auth.NewRecoveryCodes(auth.RecoveryCodeCount)
	wrong := false
	err := a.inTx(ctx, func(tx *store.Store) error {
		wrong = false
		totp, err := tx.GetAdminTOTPForUpdate(ctx, ad.User.ID)
		if err != nil {
			return err
		}
		if totp.EnabledAt == nil {
			return errTwoFactorNotEnabled
		}
		_, ok, err := a.verifyCode(ctx, tx, ad.User.ID, totp, body.Code, false)
		if err != nil {
			return err
		}
		if !ok {
			wrong = true
			return nil
		}
		if err := tx.ReplaceRecoveryCodes(ctx, ad.User.ID, hashCodes(ad.User.ID, codes)); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, nil, auditEntry{Action: "auth.2fa_recovery_codes", TargetType: "admin_user", TargetID: ad.User.ID.String(),
			Detail: map[string]any{"count": len(codes)}}); err != nil {
			return err
		}
		a.notifySecurity(ctx, tx, ad.User, notify.Event{Kind: notify.KindRecoveryCodes})
		return nil
	})
	if err == nil && wrong {
		err = a.codeFailed(ad.User)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
}

// adminResetTwoFactor (owner) removes another user's two-factor
// authentication and signs them out everywhere.
func (a *API) adminResetTwoFactor(w http.ResponseWriter, r *http.Request) {
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
	ad, _ := adminFrom(ctx)
	if id == ad.User.ID {
		a.fail(w, r, errConflict("you cannot reset your own two-factor authentication; turn it off on the account security page instead"))
		return
	}
	err = a.inTx(ctx, func(tx *store.Store) error {
		u, err := tx.GetAdminUser(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return errNotFound("user not found")
		}
		if err != nil {
			return err
		}
		reset, n, err := tx.ResetAdminTwoFactor(ctx, id)
		if err != nil {
			return err
		}
		if !reset {
			return errConflict("this user has not set up two-factor authentication")
		}
		if err := a.audit(ctx, tx, nil, auditEntry{Action: "user.2fa_reset", TargetType: "admin_user", TargetID: id.String(),
			Detail: map[string]any{"email": u.Email, "sessions_revoked": n}}); err != nil {
			return err
		}
		a.notifySecurity(ctx, tx, u, notify.Event{Kind: notify.Kind2FAReset, Actor: ad.User.Email})
		return nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

// ---- sessions ----

type sessionJSON struct {
	ID        string    `json:"id"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Current   bool      `json:"current"`
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ad, _ := adminFrom(ctx)
	list, err := a.store.ListUserSessions(ctx, ad.User.ID, a.now())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsJSON[sessionJSON]{Items: mapSlice(list, func(s domain.AdminSession) sessionJSON {
		return sessionJSON{ID: s.ID.String(), IP: s.IP, UserAgent: s.UserAgent, CreatedAt: s.CreatedAt.UTC(),
			ExpiresAt: s.ExpiresAt.UTC(), Current: s.ID == ad.Session.ID}
	})})
}

// deleteSession signs out one of the caller's own sessions (the current
// one included: then the cookie is cleared as on logout).
func (a *API) deleteSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	current := id == ad.Session.ID
	err = a.inTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteUserSession(ctx, ad.User.ID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errNotFound("session not found")
			}
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "auth.session_revoke", TargetType: "admin_user", TargetID: ad.User.ID.String(),
			Detail: map[string]any{"session_id": id.String(), "current": current}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if current {
		a.clearSessionCookie(w)
	}
	noContent(w)
}

func (a *API) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := decodeJSON(r, &struct{}{}); err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	var n int64
	err := a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if n, err = tx.DeleteUserSessions(ctx, ad.User.ID, ad.Session.ID); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "auth.sessions_revoke_others", TargetType: "admin_user",
			TargetID: ad.User.ID.String(), Detail: map[string]any{"revoked": n}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}
