package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// ResourceRepository persists resources in PostgreSQL. Every query filters
// by group.
type ResourceRepository struct {
	adapter *Adapter
}

// NewResourceRepository constructs resource storage over db.
func NewResourceRepository(db *sql.DB) (*ResourceRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &ResourceRepository{adapter: adapter}, nil
}

var _ service.ResourceRepository = (*ResourceRepository)(nil)

const resourceColumns = `r.id, r.group_id, r.name, r.kind_id, k.system_code, k.name_en, k.name_hu, r.description, r.max_concurrent_teams, r.archived_at, r.created_at, r.version`

const resourceFrom = `resources r JOIN resource_kinds k ON k.group_id = r.group_id AND k.id = r.kind_id`

// resourceFromRows joins the kind to a row set named r (a CTE).
const resourceFromRows = `r JOIN resource_kinds k ON k.group_id = r.group_id AND k.id = r.kind_id`

// CreateResource inserts one resource.
func (r *ResourceRepository) CreateResource(ctx context.Context, resource service.Resource) (service.Resource, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Resource{}, err
	}
	return scanResource(db.QueryRowContext(ctx, `
		WITH r AS (
			INSERT INTO resources (group_id, name, kind_id, description, max_concurrent_teams, archived_at, created_at, version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 0)
			RETURNING *
		)
		SELECT `+resourceColumns+` FROM `+resourceFromRows,
		resource.GroupID, resource.Name, resource.KindID, resource.Description, nullableLimit(resource.MaxConcurrentTeams), resource.ArchivedAt, resource.CreatedAt))
}

// FindResource returns one resource of the group, archived or not.
func (r *ResourceRepository) FindResource(ctx context.Context, groupID, resourceID int64) (service.Resource, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Resource{}, err
	}
	if groupID <= 0 || resourceID <= 0 {
		return service.Resource{}, service.ErrResourceNotFound
	}
	return scanResource(db.QueryRowContext(ctx, `SELECT `+resourceColumns+` FROM `+resourceFrom+` WHERE r.group_id = $1 AND r.id = $2`, groupID, resourceID))
}

// ListResources returns the group's resources by name, active ones first;
// archived ones only when includeArchived is true.
func (r *ResourceRepository) ListResources(ctx context.Context, groupID int64, includeArchived bool) ([]service.Resource, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT `+resourceColumns+`
		FROM `+resourceFrom+`
		WHERE r.group_id = $1 AND ($2 OR r.archived_at IS NULL)
		ORDER BY (r.archived_at IS NOT NULL), lower(r.name), r.id
	`, groupID, includeArchived)
	if err != nil {
		return nil, mapResourceError(err)
	}
	defer func() { _ = rows.Close() }()
	resources := make([]service.Resource, 0)
	for rows.Next() {
		resource, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return nil, mapResourceError(err)
	}
	return resources, nil
}

// UpdateResource writes the editable fields when expectedVersion is current.
func (r *ResourceRepository) UpdateResource(ctx context.Context, resource service.Resource, expectedVersion int64) (service.Resource, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Resource{}, err
	}
	updated, err := scanResource(db.QueryRowContext(ctx, `
		WITH r AS (
			UPDATE resources
			SET name = $1, kind_id = $2, description = $3, max_concurrent_teams = $4, archived_at = $5, version = version + 1
			WHERE group_id = $6 AND id = $7 AND version = $8
			RETURNING *
		)
		SELECT `+resourceColumns+` FROM `+resourceFromRows,
		resource.Name, resource.KindID, resource.Description, nullableLimit(resource.MaxConcurrentTeams), resource.ArchivedAt, resource.GroupID, resource.ID, expectedVersion))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, service.ErrResourceNotFound) {
		return service.Resource{}, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM resources WHERE group_id = $1 AND id = $2)`, resource.GroupID, resource.ID).Scan(&exists); err != nil {
		return service.Resource{}, mapResourceError(err)
	}
	return service.Resource{}, service.ClassifyStaleWrite(!exists)
}

func (r *ResourceRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func scanResource(row rowScanner) (service.Resource, error) {
	var (
		resource    service.Resource
		kindCode    sql.NullString
		description sql.NullString
		limit       sql.NullInt64
		archivedAt  sql.NullInt64
	)
	if err := row.Scan(&resource.ID, &resource.GroupID, &resource.Name, &resource.KindID, &kindCode, &resource.KindNameEN, &resource.KindNameHU, &description, &limit, &archivedAt, &resource.CreatedAt, &resource.Version); err != nil {
		return service.Resource{}, mapResourceError(err)
	}
	if resource.ID <= 0 || resource.GroupID <= 0 || resource.Version < 0 || resource.Name == "" || resource.KindID <= 0 || resource.KindNameEN == "" {
		return service.Resource{}, ErrInvalidData
	}
	resource.KindCode = kindCode.String
	resource.Description = nullableString(description)
	resource.ArchivedAt = nullableInt64(archivedAt)
	if limit.Valid {
		value := int(limit.Int64)
		resource.MaxConcurrentTeams = &value
	}
	return resource, nil
}

func nullableLimit(limit *int) any {
	if limit == nil {
		return nil
	}
	return *limit
}

func mapResourceError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrResourceNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "resources_active_name_unique" {
		return service.ErrResourceNameTaken
	}
	if errors.As(err, &pgErr) && (pgErr.ConstraintName == "resources_kind_fk" || (pgErr.Code == "23502" && pgErr.ColumnName == "kind_id")) {
		return service.ErrInvalidResourceKind
	}
	return ClassifyError(err)
}
