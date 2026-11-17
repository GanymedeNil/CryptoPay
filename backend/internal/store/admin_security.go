package store

import (
	"context"

	"github.com/google/uuid"
)

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
