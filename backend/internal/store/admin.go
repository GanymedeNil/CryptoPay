package store

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

// ---- admin users ----

const adminUserCols = `u.id, u.email, u.password_hash, u.role, u.active, u.last_login_at, u.created_at, u.updated_at`

func scanAdminUser(row pgx.Row) (domain.AdminUser, error) {
	var u domain.AdminUser
	var role string
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &role, &u.Active, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	u.Role = domain.Role(role)
	return u, err
}

// AdminUserUpdate lists the user fields to change (nil = unchanged).
type AdminUserUpdate struct {
	Email        *string
	PasswordHash *string
	Role         *domain.Role
	Active       *bool
}

// CreateAdminUser inserts an admin user. Email is stored trimmed as given
// and is unique case-insensitively (ErrConflict, ConstraintAdminUserEmail).
// Active is stored as given: set it to true for a usable account.
func (s *Store) CreateAdminUser(ctx context.Context, u domain.AdminUser) (domain.AdminUser, error) {
	const op = "create admin user"
	u.Email = strings.TrimSpace(u.Email)
	if u.Email == "" {
		return domain.AdminUser{}, invalidf(op, "email is required")
	}
	if !u.Role.Valid() {
		return domain.AdminUser{}, invalidf(op, "invalid role %q", u.Role)
	}
	if u.PasswordHash == "" {
		return domain.AdminUser{}, invalidf(op, "password hash is required")
	}
	now := s.Now()
	var out domain.AdminUser
	err := s.guarded(ctx, func(db dbtx) error {
		var err error
		out, err = scanAdminUser(db.QueryRow(ctx, `
			INSERT INTO admin_users AS u (id, email, password_hash, role, active, last_login_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING `+adminUserCols,
			uuidOrNew(u.ID), u.Email, u.PasswordHash, string(u.Role), u.Active, utcPtr(u.LastLoginAt),
			timeOr(u.CreatedAt, now), timeOr(u.UpdatedAt, now)))
		return err
	})
	return out, wrap(op, err)
}

// GetAdminUser returns a user by id.
func (s *Store) GetAdminUser(ctx context.Context, id uuid.UUID) (domain.AdminUser, error) {
	u, err := scanAdminUser(s.db.QueryRow(ctx, `SELECT `+adminUserCols+` FROM admin_users u WHERE u.id = $1`, id))
	return u, wrap("get admin user", err)
}

// GetAdminUserByEmail returns a user by e-mail, compared case-insensitively.
func (s *Store) GetAdminUserByEmail(ctx context.Context, email string) (domain.AdminUser, error) {
	u, err := scanAdminUser(s.db.QueryRow(ctx,
		`SELECT `+adminUserCols+` FROM admin_users u WHERE lower(u.email) = lower($1)`, strings.TrimSpace(email)))
	return u, wrap("get admin user by email", err)
}

// ListAdminUsers returns all admin users, oldest first.
func (s *Store) ListAdminUsers(ctx context.Context) ([]domain.AdminUser, error) {
	rows, err := s.db.Query(ctx, `SELECT `+adminUserCols+` FROM admin_users u ORDER BY u.created_at, u.id`)
	if err != nil {
		return nil, wrap("list admin users", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.AdminUser, error) { return scanAdminUser(r) })
	return nonNil(out), wrap("list admin users", err)
}

// CountAdminUsers returns the number of admin users (active or not).
func (s *Store) CountAdminUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM admin_users`).Scan(&n)
	return n, wrap("count admin users", err)
}

// UpdateAdminUser changes the given fields and returns the updated user.
//
// Guarding "the last active owner cannot be demoted/deactivated/deleted" is
// the caller's job: do it inside WithTx with CountActiveOwners, which locks
// the owner rows so two concurrent demotions cannot both succeed.
func (s *Store) UpdateAdminUser(ctx context.Context, id uuid.UUID, upd AdminUserUpdate) (domain.AdminUser, error) {
	const op = "update admin user"
	var email, role *string
	if upd.Email != nil {
		e := strings.TrimSpace(*upd.Email)
		if e == "" {
			return domain.AdminUser{}, invalidf(op, "email is required")
		}
		email = &e
	}
	if upd.Role != nil {
		if !upd.Role.Valid() {
			return domain.AdminUser{}, invalidf(op, "invalid role %q", *upd.Role)
		}
		r := string(*upd.Role)
		role = &r
	}
	if upd.PasswordHash != nil && *upd.PasswordHash == "" {
		return domain.AdminUser{}, invalidf(op, "password hash is required")
	}
	var out domain.AdminUser
	err := s.guarded(ctx, func(db dbtx) error {
		var err error
		out, err = scanAdminUser(db.QueryRow(ctx, `
			UPDATE admin_users u SET
				email         = COALESCE($2, u.email),
				password_hash = COALESCE($3, u.password_hash),
				role          = COALESCE($4, u.role),
				active        = COALESCE($5, u.active),
				updated_at    = $6
			WHERE u.id = $1
			RETURNING `+adminUserCols,
			id, email, upd.PasswordHash, role, upd.Active, s.Now()))
		return err
	})
	return out, wrap(op, err)
}

// DeleteAdminUser deletes a user (and, by cascade, its sessions).
func (s *Store) DeleteAdminUser(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_users WHERE id = $1`, id)
	if err != nil {
		return wrapDelete("delete admin user", err)
	}
	if tag.RowsAffected() == 0 {
		return wrap("delete admin user", ErrNotFound)
	}
	return nil
}

// CountActiveOwners returns the number of active users with role owner.
// Inside WithTx it locks those rows (SELECT … FOR UPDATE) until the
// transaction ends, which makes "is this the last owner?" checks race-free.
func (s *Store) CountActiveOwners(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM admin_users WHERE role = 'owner' AND active ORDER BY id FOR UPDATE`)
	if err != nil {
		return 0, wrap("count active owners", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	return len(ids), wrap("count active owners", err)
}

// TouchAdminLogin sets last_login_at to the store clock's now.
func (s *Store) TouchAdminLogin(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE admin_users SET last_login_at = $2 WHERE id = $1`, id, s.Now())
	if err != nil {
		return wrap("touch admin login", err)
	}
	if tag.RowsAffected() == 0 {
		return wrap("touch admin login", ErrNotFound)
	}
	return nil
}

// EnsureOwner creates an active owner with the given e-mail and password
// hash only if there are no admin users at all (first-run bootstrap). It
// reports whether a user was created. Safe to call on every startup and
// from concurrent instances.
func (s *Store) EnsureOwner(ctx context.Context, email, passwordHash string) (bool, error) {
	const op = "ensure owner"
	email = strings.TrimSpace(email)
	if email == "" || passwordHash == "" {
		return false, invalidf(op, "email and password hash are required")
	}
	created := false
	err := s.WithTx(ctx, func(tx *Store) error {
		// Serialise concurrent bootstrappers on a transaction advisory lock.
		if _, err := tx.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cryptopay/ensure-owner'))`); err != nil {
			return wrap(op, err)
		}
		n, err := tx.CountAdminUsers(ctx)
		if err != nil || n > 0 {
			return err
		}
		if _, err := tx.CreateAdminUser(ctx, domain.AdminUser{
			Email: email, PasswordHash: passwordHash, Role: domain.RoleOwner, Active: true,
		}); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

// ---- sessions ----

const sessionCols = `se.id, se.user_id, se.token_hash, se.ip, se.user_agent, se.expires_at, se.created_at`

func scanSession(row pgx.Row) (domain.AdminSession, error) {
	var x domain.AdminSession
	err := row.Scan(&x.ID, &x.UserID, &x.TokenHash, &x.IP, &x.UserAgent, &x.ExpiresAt, &x.CreatedAt)
	return x, err
}

// CreateSession stores a session. TokenHash must be the hex SHA-256 of the
// cookie token (auth.HashToken); it is unique.
func (s *Store) CreateSession(ctx context.Context, x domain.AdminSession) (domain.AdminSession, error) {
	const op = "create session"
	if x.TokenHash == "" || x.ExpiresAt.IsZero() {
		return domain.AdminSession{}, invalidf(op, "token hash and expiry are required")
	}
	out, err := scanSession(s.db.QueryRow(ctx, `
		INSERT INTO admin_sessions AS se (id, user_id, token_hash, ip, user_agent, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+sessionCols,
		uuidOrNew(x.ID), x.UserID, x.TokenHash, cleanText(x.IP, 100), cleanText(x.UserAgent, 512),
		x.ExpiresAt.UTC(), timeOr(x.CreatedAt, s.Now())))
	return out, wrap(op, err)
}

// GetSessionByTokenHash returns the session with the given token hash and
// its user, but only if the session has not expired at now and the user is
// active; otherwise ErrNotFound.
func (s *Store) GetSessionByTokenHash(ctx context.Context, tokenHash string, now time.Time) (domain.AdminSession, domain.AdminUser, error) {
	row := s.db.QueryRow(ctx, `
		SELECT `+sessionCols+`, `+adminUserCols+`
		FROM admin_sessions se JOIN admin_users u ON u.id = se.user_id
		WHERE se.token_hash = $1 AND se.expires_at > $2 AND u.active`, tokenHash, now.UTC())
	var x domain.AdminSession
	var u domain.AdminUser
	var role string
	err := row.Scan(&x.ID, &x.UserID, &x.TokenHash, &x.IP, &x.UserAgent, &x.ExpiresAt, &x.CreatedAt,
		&u.ID, &u.Email, &u.PasswordHash, &role, &u.Active, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	u.Role = domain.Role(role)
	if err != nil {
		return domain.AdminSession{}, domain.AdminUser{}, wrap("get session", err)
	}
	return x, u, nil
}

// ExtendSession moves a session's expiry (sliding sessions).
func (s *Store) ExtendSession(ctx context.Context, id uuid.UUID, expiresAt time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE admin_sessions SET expires_at = $2 WHERE id = $1`, id, expiresAt.UTC())
	if err != nil {
		return wrap("extend session", err)
	}
	if tag.RowsAffected() == 0 {
		return wrap("extend session", ErrNotFound)
	}
	return nil
}

// DeleteSession deletes a session by id (no error if it does not exist).
func (s *Store) DeleteSession(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM admin_sessions WHERE id = $1`, id)
	return wrap("delete session", err)
}

// DeleteSessionByTokenHash deletes a session by token hash (logout); no
// error if it does not exist.
func (s *Store) DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM admin_sessions WHERE token_hash = $1`, tokenHash)
	return wrap("delete session", err)
}

// DeleteExpiredSessions removes sessions that expired at or before now and
// returns how many were deleted.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_sessions WHERE expires_at <= $1`, now.UTC())
	if err != nil {
		return 0, wrap("delete expired sessions", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteUserSessions removes every session of a user except the one with id
// except (pass uuid.Nil to remove all), e.g. after a password change or
// deactivation. Returns the number deleted.
func (s *Store) DeleteUserSessions(ctx context.Context, userID, except uuid.UUID) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id = $1 AND id <> $2`, userID, except)
	if err != nil {
		return 0, wrap("delete user sessions", err)
	}
	return tag.RowsAffected(), nil
}
