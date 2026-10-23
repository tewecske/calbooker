package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/tewecske/calbooker/internal/service"
)

// AuditLogRepository stores administrator action history in PostgreSQL.
type AuditLogRepository struct {
	adapter *Adapter
}

// NewAuditLogRepository constructs an administrator audit repository.
func NewAuditLogRepository(db *sql.DB) (*AuditLogRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &AuditLogRepository{adapter: adapter}, nil
}

var _ service.AuditRepository = (*AuditLogRepository)(nil)

// CreateAuditEntry inserts one administrator action record.
func (r *AuditLogRepository) CreateAuditEntry(ctx context.Context, entry service.AuditEntry) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if entry.Action == "" || entry.OccurredAt <= 0 {
		return ErrInvalidData
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_log (
			occurred_at, actor_user_id, actor_email, action,
			target_type, target_id, detail, ip
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, entry.OccurredAt, entry.ActorUserID, entry.ActorEmail, entry.Action,
		entry.TargetType, entry.TargetID, entry.Detail, entry.IP); err != nil {
		return wrapOperation("create audit entry", err)
	}
	return nil
}

// ListAuditEntries returns one bounded, filtered page of administrator action
// history, newest first. Filters are applied in SQL and never interpolated.
func (r *AuditLogRepository) ListAuditEntries(ctx context.Context, query service.AuditQuery) (service.AuditPage, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.AuditPage{}, err
	}
	query = query.Normalize()

	conditions, args := auditConditions(query)
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_log"+where, args...).Scan(&total); err != nil {
		return service.AuditPage{}, wrapOperation("count audit entries", err)
	}
	limitPlaceholder := fmt.Sprintf("$%d", len(args)+1)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args)+2)
	pageArgs := append(append([]any{}, args...), query.Size, query.Offset())
	rows, err := db.QueryContext(ctx, `
		SELECT id, occurred_at, actor_user_id, actor_email, action,
			target_type, target_id, detail, ip
		FROM audit_log`+where+`
		ORDER BY occurred_at DESC, id DESC
		LIMIT `+limitPlaceholder+` OFFSET `+offsetPlaceholder, pageArgs...)
	if err != nil {
		return service.AuditPage{}, wrapOperation("list audit entries", err)
	}
	entries, err := scanAuditEntries(rows)
	if err != nil {
		return service.AuditPage{}, err
	}
	return service.AuditPage{Entries: entries, Total: total, Page: query.Page, Size: query.Size}, nil
}

func auditConditions(query service.AuditQuery) ([]string, []any) {
	var (
		conditions []string
		args       []any
	)
	if query.Action != "" {
		conditions = append(conditions, fmt.Sprintf("action ILIKE $%d", len(args)+1))
		args = append(args, service.AdminSearchPattern(query.Action))
	}
	if query.Actor != "" {
		conditions = append(conditions, fmt.Sprintf("actor_email ILIKE $%d", len(args)+1))
		args = append(args, service.AdminSearchPattern(query.Actor))
	}
	if query.Target != "" {
		placeholder := fmt.Sprintf("$%d", len(args)+1)
		conditions = append(conditions, "(target_id ILIKE "+placeholder+" OR target_type ILIKE "+placeholder+")")
		args = append(args, service.AdminSearchPattern(query.Target))
	}
	return conditions, args
}

func scanAuditEntries(rows *sql.Rows) ([]service.AuditEntry, error) {
	defer func() { _ = rows.Close() }()
	entries := make([]service.AuditEntry, 0)
	for rows.Next() {
		entry, err := scanAuditEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapOperation("iterate audit entries", err)
	}
	return entries, nil
}

func scanAuditEntry(row rowScanner) (service.AuditEntry, error) {
	var (
		entry       service.AuditEntry
		actorUserID sql.NullInt64
		actorEmail  sql.NullString
		targetType  sql.NullString
		targetID    sql.NullString
		detail      sql.NullString
		ip          sql.NullString
	)
	if err := row.Scan(&entry.ID, &entry.OccurredAt, &actorUserID, &actorEmail, &entry.Action,
		&targetType, &targetID, &detail, &ip); err != nil {
		return service.AuditEntry{}, wrapOperation("scan audit entry", err)
	}
	if entry.ID <= 0 || entry.Action == "" || entry.OccurredAt <= 0 {
		return service.AuditEntry{}, ErrInvalidData
	}
	entry.ActorUserID = nullableInt64(actorUserID)
	entry.ActorEmail = nullableString(actorEmail)
	entry.TargetType = nullableString(targetType)
	entry.TargetID = nullableString(targetID)
	entry.Detail = nullableString(detail)
	entry.IP = nullableString(ip)
	return entry, nil
}

func (r *AuditLogRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}
