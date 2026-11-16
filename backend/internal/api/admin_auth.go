package api

import (
	"errors"
	"net/http"
	"strings"

	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
)

const maxEmailLen = 254

// login checks e-mail and password. It spends the same time whether the
// account exists or not (VerifyDummy) and throttles repeated failures per
// (client IP, e-mail).
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	email := strings.TrimSpace(body.Email)
	f := fields{}
	if email == "" {
		f.add("email", "is required")
	} else if len(email) > maxEmailLen {
		f.add("email", "is too long")
	}
	if body.Password == "" {
		f.add("password", "is required")
	} else if len(body.Password) > 1024 {
		f.add("password", "is too long")
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}

	throttleKey := clientIPFrom(ctx) + "|" + strings.ToLower(email)
	if blocked, wait := a.loginFailures.blocked(throttleKey); blocked {
		a.log.WarnContext(ctx, "login throttled", "request_id", requestIDFrom(ctx), "ip", clientIPFrom(ctx))
		a.rateLimited(w, r, wait)
		return
	}

	user, err := a.store.GetAdminUserByEmail(ctx, email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.hasher.VerifyDummy(body.Password)
		a.loginFailed(w, r, throttleKey, "unknown e-mail")
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	if err := a.hasher.Verify(user.PasswordHash, body.Password); err != nil {
		a.loginFailed(w, r, throttleKey, "wrong password")
		return
	}
	if !user.Active {
		a.loginFailed(w, r, throttleKey, "inactive account")
		return
	}
	a.loginFailures.reset(throttleKey)

	token, hash := auth.NewSessionToken()
	expires := a.now().Add(SessionTTL)
	ua := r.UserAgent()
	if len(ua) > 512 {
		ua = ua[:512]
	}
	var sess domain.AdminSession
	err = a.inTx(ctx, func(tx *store.Store) error {
		var err error
		sess, err = tx.CreateSession(ctx, domain.AdminSession{
			UserID: user.ID, TokenHash: hash, IP: clientIPFrom(ctx), UserAgent: ua, ExpiresAt: expires,
		})
		if err != nil {
			return err
		}
		if a.hasher.NeedsRehash(user.PasswordHash) {
			if h, err := a.hasher.Hash(body.Password); err == nil {
				if _, err := tx.UpdateAdminUser(ctx, user.ID, store.AdminUserUpdate{PasswordHash: &h}); err != nil {
					return err
				}
			}
		}
		if err := tx.TouchAdminLogin(ctx, user.ID); err != nil {
			return err
		}
		if user, err = tx.GetAdminUser(ctx, user.ID); err != nil {
			return err
		}
		return a.audit(ctx, tx, &user, auditEntry{Action: "auth.login", TargetType: "admin_user", TargetID: user.ID.String()})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	infoFrom(ctx).AdminID = user.ID.String()
	a.setSessionCookie(w, token, sess.ExpiresAt)
	writeJSON(w, http.StatusOK, newAdminUserJSON(user))
}

func (a *API) loginFailed(w http.ResponseWriter, r *http.Request, throttleKey, reason string) {
	ctx := r.Context()
	a.loginFailures.fail(throttleKey)
	a.log.WarnContext(ctx, "admin login failed", "request_id", requestIDFrom(ctx), "ip", clientIPFrom(ctx), "reason", reason)
	a.fail(w, r, errUnauthorized("invalid e-mail or password"))
}

// logout deletes the session (if any) and clears the cookie. It always
// answers 204, also without a valid session.
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := decodeJSON(r, &struct{}{}); err != nil {
		a.fail(w, r, err)
		return
	}
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" && len(c.Value) <= 256 {
		hash := auth.HashToken(c.Value)
		sess, user, err := a.store.GetSessionByTokenHash(ctx, hash, a.now())
		if err := a.store.DeleteSessionByTokenHash(ctx, hash); err != nil {
			a.fail(w, r, err)
			return
		}
		if err == nil {
			infoFrom(ctx).AdminID = user.ID.String()
			if aerr := a.audit(ctx, a.store, &user, auditEntry{Action: "auth.logout", TargetType: "admin_user",
				TargetID: user.ID.String(), Detail: map[string]any{"session_id": sess.ID.String()}}); aerr != nil {
				a.log.ErrorContext(ctx, "write audit log failed", "request_id", requestIDFrom(ctx), "error", aerr)
			}
		}
	}
	a.clearSessionCookie(w)
	noContent(w)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	ad, _ := adminFrom(r.Context())
	writeJSON(w, http.StatusOK, newAdminUserJSON(ad.User))
}

// changePassword changes the caller's own password (any role) and signs out
// every other session of the user.
func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	if body.CurrentPassword == "" {
		f.add("current_password", "is required")
	}
	if err := auth.ValidatePassword(body.NewPassword); err != nil {
		f.add("new_password", passwordMessage(err))
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	ad, _ := adminFrom(ctx)
	if err := a.hasher.Verify(ad.User.PasswordHash, body.CurrentPassword); err != nil {
		a.fail(w, r, errField("current_password", "is incorrect"))
		return
	}
	hash, err := a.hasher.Hash(body.NewPassword)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	err = a.inTx(ctx, func(tx *store.Store) error {
		if _, err := tx.UpdateAdminUser(ctx, ad.User.ID, store.AdminUserUpdate{PasswordHash: &hash}); err != nil {
			return err
		}
		n, err := tx.DeleteUserSessions(ctx, ad.User.ID, ad.Session.ID)
		if err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "auth.password_change", TargetType: "admin_user",
			TargetID: ad.User.ID.String(), Detail: map[string]any{"other_sessions_revoked": n}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

// passwordMessage turns an auth.ValidatePassword error into a field message.
func passwordMessage(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	if msg == "" || errors.Is(err, auth.ErrWeakPassword) && msg == auth.ErrWeakPassword.Error() {
		return "must be at least 8 characters"
	}
	return msg
}
