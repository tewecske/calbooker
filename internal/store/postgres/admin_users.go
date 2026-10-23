package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/tewecske/calbooker/internal/service"
)

// AdminUserRepository lists accounts for the administrator area.
type AdminUserRepository struct {
	adapter *Adapter
}

// NewAdminUserRepository constructs a PostgreSQL administrator list repository.
func NewAdminUserRepository(db *sql.DB) (*AdminUserRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &AdminUserRepository{adapter: adapter}, nil
}

var _ service.AdminUserRepository = (*AdminUserRepository)(nil)

// ListUsers returns one bounded, filtered, sorted page of accounts. Search,
// filters, sorting, and pagination are applied in SQL; the sort key is chosen
// from a fixed whitelist and never interpolated from user input.
func (r *AdminUserRepository) ListUsers(ctx context.Context, query service.AdminUserQuery) (service.AdminUserPage, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.AdminUserPage{}, err
	}
	query = query.Normalize()

	conditions, args := adminUserConditions(query)
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users"+where, args...).Scan(&total); err != nil {
		return service.AdminUserPage{}, ClassifyError(err)
	}

	limitPlaceholder := fmt.Sprintf("$%d", len(args)+1)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args)+2)
	pageArgs := append(append([]any{}, args...), query.Size, query.Offset())
	rows, err := db.QueryContext(
		ctx,
		userSelect+where+" ORDER BY "+adminUserOrderBy(query)+
			" LIMIT "+limitPlaceholder+" OFFSET "+offsetPlaceholder,
		pageArgs...,
	)
	if err != nil {
		return service.AdminUserPage{}, ClassifyError(err)
	}
	defer func() { _ = rows.Close() }()

	users := make([]service.User, 0, query.Size)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return service.AdminUserPage{}, ClassifyError(err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return service.AdminUserPage{}, ClassifyError(err)
	}
	return service.AdminUserPage{
		Users: users,
		Total: total,
		Page:  query.Page,
		Size:  query.Size,
	}, nil
}

func adminUserConditions(query service.AdminUserQuery) ([]string, []any) {
	var (
		conditions []string
		args       []any
	)
	if query.Search != "" {
		pattern := service.AdminSearchPattern(query.Search)
		placeholder := fmt.Sprintf("$%d", len(args)+1)
		conditions = append(conditions, "(email ILIKE "+placeholder+" OR username ILIKE "+placeholder+" OR display_name ILIKE "+placeholder+")")
		args = append(args, pattern)
	}
	if query.Admin == service.AdminFilterYes {
		conditions = append(conditions, fmt.Sprintf("is_admin = $%d", len(args)+1))
		args = append(args, true)
	}
	if query.Admin == service.AdminFilterNo {
		conditions = append(conditions, fmt.Sprintf("is_admin = $%d", len(args)+1))
		args = append(args, false)
	}
	if query.Guest == service.AdminFilterYes {
		conditions = append(conditions, fmt.Sprintf("kind = $%d", len(args)+1))
		args = append(args, string(service.UserKindGuest))
	}
	if query.Guest == service.AdminFilterNo {
		conditions = append(conditions, fmt.Sprintf("kind <> $%d", len(args)+1))
		args = append(args, string(service.UserKindGuest))
	}
	if query.Confirmed == service.AdminFilterYes {
		conditions = append(conditions, "email_verified_at IS NOT NULL")
	}
	if query.Confirmed == service.AdminFilterNo {
		conditions = append(conditions, "email_verified_at IS NULL")
	}
	return conditions, args
}

func adminUserOrderBy(query service.AdminUserQuery) string {
	direction := "ASC"
	if query.Direction == service.AdminDirectionDesc {
		direction = "DESC"
	}
	switch query.Sort {
	case service.AdminSortID:
		return "id " + direction
	case service.AdminSortEmail:
		return "email " + direction + " NULLS LAST, id ASC"
	case service.AdminSortUsername:
		return "username " + direction + " NULLS LAST, id ASC"
	case service.AdminSortDisplayName:
		return "display_name " + direction + " NULLS LAST, id ASC"
	default:
		return "created_at " + direction + ", id " + direction
	}
}

func (r *AdminUserRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}
