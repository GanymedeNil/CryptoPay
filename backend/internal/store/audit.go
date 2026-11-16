package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"cryptopay/internal/domain"
)

const auditCols = `l.id, l.actor_id, l.actor_email, l.action, l.target_type, l.target_id, l.detail::text, l.ip, l.created_at`

func scanAudit(row pgx.Row) (domain.AuditLog, error) {
	var l domain.AuditLog
	err := row.Scan(&l.ID, &l.ActorID, &l.ActorEmail, &l.Action, &l.TargetType, &l.TargetID, jsonScan{&l.Detail}, &l.IP, &l.CreatedAt)
	return l, err
}

// InsertAuditLog appends an audit entry (ID is assigned by the database;
// nil Detail → {}; zero CreatedAt → store clock).
func (s *Store) InsertAuditLog(ctx context.Context, l domain.AuditLog) (domain.AuditLog, error) {
	const op = "insert audit log"
	if l.Action == "" {
		return domain.AuditLog{}, invalidf(op, "action is required")
	}
	if len(l.Detail) > 0 && !json.Valid(l.Detail) {
		return domain.AuditLog{}, invalidf(op, "detail must be valid JSON")
	}
	out, err := scanAudit(s.db.QueryRow(ctx, `
		INSERT INTO audit_logs AS l (actor_id, actor_email, action, target_type, target_id, detail, ip, created_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)
		RETURNING `+auditCols,
		l.ActorID, l.ActorEmail, l.Action, l.TargetType, l.TargetID, jsonText(l.Detail, "{}"), cleanText(l.IP, 100),
		timeOr(l.CreatedAt, s.Now())))
	return out, wrap(op, err)
}

// AuditFilter selects entries for ListAuditLogs.
type AuditFilter struct {
	Action     string // exact action, e.g. "chain.update"
	Actor      string // case-insensitive substring of the actor e-mail
	TargetType string // exact
	TargetID   string // exact
	Page       int    // 1-based
	PageSize   int
}

// ListAuditLogs returns a page of audit entries, newest first.
func (s *Store) ListAuditLogs(ctx context.Context, f AuditFilter) (Page[domain.AuditLog], error) {
	const op = "list audit logs"
	page, size, offset := normPage(f.Page, f.PageSize)
	out := Page[domain.AuditLog]{Items: []domain.AuditLog{}, Page: page, PageSize: size}
	var c conds
	if f.Action != "" {
		c.add("l.action = " + c.arg(f.Action))
	}
	if a := strings.TrimSpace(f.Actor); a != "" {
		c.add("l.actor_email ILIKE '%' || " + c.arg(likeEscape(a)) + " || '%'")
	}
	if f.TargetType != "" {
		c.add("l.target_type = " + c.arg(f.TargetType))
	}
	if f.TargetID != "" {
		c.add("l.target_id = " + c.arg(f.TargetID))
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM audit_logs l`+c.where(), c.args...).Scan(&out.Total); err != nil {
		return out, wrap(op, err)
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+auditCols+` FROM audit_logs l`+c.where()+
		fmt.Sprintf(` ORDER BY l.created_at DESC, l.id DESC LIMIT %d OFFSET %d`, size, offset), c.args...)
	if err != nil {
		return out, wrap(op, err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.AuditLog, error) { return scanAudit(r) })
	if err != nil {
		return out, wrap(op, err)
	}
	out.Items = nonNil(items)
	return out, nil
}
