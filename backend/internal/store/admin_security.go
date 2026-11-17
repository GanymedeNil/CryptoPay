package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

// ---- two-factor authentication ----
//
// The TOTP secret is stored encrypted (admin_users.totp_secret_enc) and is
// only ever read through GetAdminTOTP*; domain.AdminUser carries no more
// than TOTPEnabledAt.

// GetAdminTOTP returns a user's two-factor state with the secret decrypted.
func (s *Store) GetAdminTOTP(ctx context.Context, userID uuid.UUID) (domain.AdminTOTP, error) {
	return s.getAdminTOTP(ctx, "get admin totp", userID, "")
}

// GetAdminTOTPForUpdate is GetAdminTOTP that also locks the user row until
// the transaction ends (inside WithTx only).
func (s *Store) GetAdminTOTPForUpdate(ctx context.Context, userID uuid.UUID) (domain.AdminTOTP, error) {
	const op = "get admin totp for update"
	if err := s.requireTx(op); err != nil {
		return domain.AdminTOTP{}, err
	}
	return s.getAdminTOTP(ctx, op, userID, " FOR UPDATE")
}

func (s *Store) getAdminTOTP(ctx context.Context, op string, userID uuid.UUID, lock string) (domain.AdminTOTP, error) {
	var enc string
	var t domain.AdminTOTP
	err := s.db.QueryRow(ctx, `SELECT totp_secret_enc, totp_enabled_at, totp_last_step FROM admin_users WHERE id = $1`+lock,
		userID).Scan(&enc, &t.EnabledAt, &t.LastStep)
	if err != nil {
		return domain.AdminTOTP{}, wrap(op, err)
	}
	if t.Secret, err = s.box.Decrypt(enc); err != nil {
		return domain.AdminTOTP{}, fmt.Errorf("store: %s: decrypt secret: %w", op, err)
	}
	return t, nil
}

// SetPendingTOTPSecret stores a new, not yet confirmed TOTP secret
// (encrypted), replacing an earlier pending one. ErrConflict when two-factor
// authentication is already enabled for the user.
func (s *Store) SetPendingTOTPSecret(ctx context.Context, userID uuid.UUID, secret string) error {
	const op = "set pending totp secret"
	if secret == "" {
		return invalidf(op, "secret is required")
	}
	enc, err := s.box.Encrypt(secret)
	if err != nil {
		return fmt.Errorf("store: %s: encrypt: %w", op, err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE admin_users SET totp_secret_enc = $2, totp_last_step = 0, updated_at = $3
		WHERE id = $1 AND totp_enabled_at IS NULL`, userID, enc, s.Now())
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return s.errNoRow(ctx, op, "admin_users", userID)
	}
	return nil
}

// EnableTOTP turns the pending secret on and records step as the last used
// time step. ErrConflict when there is no pending secret or two-factor
// authentication is already enabled.
func (s *Store) EnableTOTP(ctx context.Context, userID uuid.UUID, step int64) (domain.AdminUser, error) {
	const op = "enable totp"
	now := s.Now()
	u, err := scanAdminUser(s.db.QueryRow(ctx, `
		UPDATE admin_users u SET totp_enabled_at = $2, totp_last_step = $3, updated_at = $2
		WHERE u.id = $1 AND u.totp_enabled_at IS NULL AND u.totp_secret_enc <> ''
		RETURNING `+adminUserCols, userID, now, step))
	if isNoRows(err) {
		return domain.AdminUser{}, s.errNoRow(ctx, op, "admin_users", userID)
	}
	return u, wrap(op, err)
}

// AdvanceTOTPStep records step as the last accepted time step, but only if
// it is newer than the one stored: ErrConflict means the code was already
// used (replay), ErrNotFound that the user does not exist. The comparison is
// a single conditional UPDATE, so two concurrent requests with the same code
// cannot both succeed.
func (s *Store) AdvanceTOTPStep(ctx context.Context, userID uuid.UUID, step int64) error {
	const op = "advance totp step"
	tag, err := s.db.Exec(ctx, `UPDATE admin_users SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, userID, step)
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return s.errNoRow(ctx, op, "admin_users", userID)
	}
	return nil
}

// ClearTOTP removes a user's secret (pending or enabled) and recovery codes.
// It reports whether there was anything to remove.
func (s *Store) ClearTOTP(ctx context.Context, userID uuid.UUID) (bool, error) {
	const op = "clear totp"
	var had bool
	err := s.db.QueryRow(ctx, `
		WITH old AS (SELECT id, totp_secret_enc <> '' OR totp_enabled_at IS NOT NULL AS had FROM admin_users WHERE id = $1 FOR UPDATE)
		UPDATE admin_users u SET totp_secret_enc = '', totp_enabled_at = NULL, totp_last_step = 0, updated_at = $2
		FROM old WHERE u.id = old.id
		RETURNING old.had`, userID, s.Now()).Scan(&had)
	if err != nil {
		return false, wrap(op, err)
	}
	n, err := s.db.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE user_id = $1`, userID)
	if err != nil {
		return false, wrap(op, err)
	}
	return had || n.RowsAffected() > 0, nil
}

// ResetAdminTwoFactor removes a user's TOTP secret (enabled or pending) and
// recovery codes and deletes all of the user's sessions and sign-in
// challenges (owner reset, `gateway reset-2fa`). reset is false — and
// nothing is changed — when the user had no two-factor authentication.
// Run it inside WithTx.
func (s *Store) ResetAdminTwoFactor(ctx context.Context, userID uuid.UUID) (reset bool, sessions int64, err error) {
	const op = "reset admin two-factor"
	if err := s.requireTx(op); err != nil {
		return false, 0, err
	}
	had, err := s.ClearTOTP(ctx, userID)
	if err != nil || !had {
		return false, 0, err
	}
	if sessions, err = s.DeleteUserSessions(ctx, userID, uuid.Nil); err != nil {
		return false, 0, err
	}
	if _, err := s.DeleteUserLoginChallenges(ctx, userID); err != nil {
		return false, 0, err
	}
	return true, sessions, nil
}

// ---- recovery codes ----

// ReplaceRecoveryCodes deletes the user's recovery codes and stores the
// given hashes as the new set. Run it inside WithTx.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes []string) error {
	const op = "replace recovery codes"
	if err := s.requireTx(op); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return wrap(op, err)
	}
	now := s.Now()
	for _, h := range hashes {
		if h == "" {
			return invalidf(op, "empty hash")
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO admin_recovery_codes (id, user_id, code_hash, created_at) VALUES ($1, $2, $3, $4)`,
			uuid.New(), userID, h, now); err != nil {
			return wrap(op, err)
		}
	}
	return nil
}

// UseRecoveryCode marks the user's unused recovery code with the given hash
// as used and returns how many unused codes are left. ErrNotFound when no
// such unused code exists (wrong, already used or another user's code).
func (s *Store) UseRecoveryCode(ctx context.Context, userID uuid.UUID, hash string) (remaining int, err error) {
	const op = "use recovery code"
	tag, err := s.db.Exec(ctx, `UPDATE admin_recovery_codes SET used_at = $3
		WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`, userID, hash, s.Now())
	if err != nil {
		return 0, wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return 0, wrap(op, ErrNotFound)
	}
	return s.CountRecoveryCodes(ctx, userID)
}

// CountRecoveryCodes returns the number of unused recovery codes of a user.
func (s *Store) CountRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID).Scan(&n)
	return n, wrap("count recovery codes", err)
}

// ---- login challenges ----

const challengeCols = `c.id, c.user_id, c.token_hash, c.attempts, c.ip, c.user_agent, c.expires_at, c.created_at`

func scanChallenge(row pgx.Row) (domain.LoginChallenge, error) {
	var c domain.LoginChallenge
	err := row.Scan(&c.ID, &c.UserID, &c.TokenHash, &c.Attempts, &c.IP, &c.UserAgent, &c.ExpiresAt, &c.CreatedAt)
	return c, err
}

// CreateLoginChallenge stores the second step of a sign-in. TokenHash must
// be the hex SHA-256 of the token given to the client (auth.HashToken).
func (s *Store) CreateLoginChallenge(ctx context.Context, c domain.LoginChallenge) (domain.LoginChallenge, error) {
	const op = "create login challenge"
	if c.TokenHash == "" || c.ExpiresAt.IsZero() {
		return domain.LoginChallenge{}, invalidf(op, "token hash and expiry are required")
	}
	out, err := scanChallenge(s.db.QueryRow(ctx, `
		INSERT INTO admin_login_challenges AS c (id, user_id, token_hash, attempts, ip, user_agent, expires_at, created_at)
		VALUES ($1, $2, $3, 0, $4, $5, $6, $7)
		RETURNING `+challengeCols,
		uuidOrNew(c.ID), c.UserID, c.TokenHash, cleanText(c.IP, 100), cleanText(c.UserAgent, 512),
		c.ExpiresAt.UTC(), timeOr(c.CreatedAt, s.Now())))
	return out, wrap(op, err)
}

// GetLoginChallenge returns the challenge with the given token hash if it
// has not expired at now; otherwise ErrNotFound.
func (s *Store) GetLoginChallenge(ctx context.Context, tokenHash string, now time.Time) (domain.LoginChallenge, error) {
	c, err := scanChallenge(s.db.QueryRow(ctx, `SELECT `+challengeCols+` FROM admin_login_challenges c
		WHERE c.token_hash = $1 AND c.expires_at > $2`, tokenHash, now.UTC()))
	return c, wrap("get login challenge", err)
}

// GetLoginChallengeForUpdate is GetLoginChallenge that also locks the row
// until the transaction ends (inside WithTx only).
func (s *Store) GetLoginChallengeForUpdate(ctx context.Context, tokenHash string, now time.Time) (domain.LoginChallenge, error) {
	const op = "get login challenge for update"
	if err := s.requireTx(op); err != nil {
		return domain.LoginChallenge{}, err
	}
	c, err := scanChallenge(s.db.QueryRow(ctx, `SELECT `+challengeCols+` FROM admin_login_challenges c
		WHERE c.token_hash = $1 AND c.expires_at > $2 FOR UPDATE`, tokenHash, now.UTC()))
	return c, wrap(op, err)
}

// CountChallengeFailure adds a wrong code to a challenge and returns the new
// number of attempts.
func (s *Store) CountChallengeFailure(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `UPDATE admin_login_challenges SET attempts = attempts + 1 WHERE id = $1 RETURNING attempts`, id).Scan(&n)
	return n, wrap("count challenge failure", err)
}

// DeleteLoginChallenge deletes a challenge (no error if it does not exist).
func (s *Store) DeleteLoginChallenge(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM admin_login_challenges WHERE id = $1`, id)
	return wrap("delete login challenge", err)
}

// DeleteUserLoginChallenges deletes every pending challenge of a user (after
// a password change, deactivation or a two-factor reset).
func (s *Store) DeleteUserLoginChallenges(ctx context.Context, userID uuid.UUID) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_login_challenges WHERE user_id = $1`, userID)
	if err != nil {
		return 0, wrap("delete user login challenges", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteExpiredLoginChallenges removes challenges that expired at or before now.
func (s *Store) DeleteExpiredLoginChallenges(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_login_challenges WHERE expires_at <= $1`, now.UTC())
	if err != nil {
		return 0, wrap("delete expired login challenges", err)
	}
	return tag.RowsAffected(), nil
}

// ---- session management ----

// ListUserSessions returns a user's sessions that have not expired at now,
// newest first.
func (s *Store) ListUserSessions(ctx context.Context, userID uuid.UUID, now time.Time) ([]domain.AdminSession, error) {
	const op = "list user sessions"
	rows, err := s.db.Query(ctx, `SELECT `+sessionCols+` FROM admin_sessions se
		WHERE se.user_id = $1 AND se.expires_at > $2 ORDER BY se.created_at DESC, se.id DESC`, userID, now.UTC())
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.AdminSession, error) { return scanSession(r) })
	return nonNil(out), wrap(op, err)
}

// DeleteUserSession deletes one session of a user. ErrNotFound when the
// session does not exist or belongs to someone else.
func (s *Store) DeleteUserSession(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_sessions WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return wrap("delete user session", err)
	}
	if tag.RowsAffected() == 0 {
		return wrap("delete user session", ErrNotFound)
	}
	return nil
}

// LoginHistory tells whether a user has signed in before (a successful
// sign-in in the audit log, or any session), and whether ip was used by any
// of those. Call it before recording the current sign-in.
func (s *Store) LoginHistory(ctx context.Context, userID uuid.UUID, ip string) (signedInBefore, ipSeen bool, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM audit_logs WHERE action = 'auth.login' AND target_id = $1)
				OR EXISTS (SELECT 1 FROM admin_sessions WHERE user_id = $2),
			EXISTS (SELECT 1 FROM audit_logs WHERE action = 'auth.login' AND target_id = $1 AND ip = $3)
				OR EXISTS (SELECT 1 FROM admin_sessions WHERE user_id = $2 AND ip = $3)`,
		userID.String(), userID, cleanText(ip, 100)).Scan(&signedInBefore, &ipSeen)
	return signedInBefore, ipSeen, wrap("login history", err)
}
