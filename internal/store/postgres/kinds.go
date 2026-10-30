package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// KindRepository persists event kinds and resource kinds in PostgreSQL. Every
// query filters by group.
type KindRepository struct {
	adapter *Adapter
}

// NewKindRepository constructs kind storage over db.
func NewKindRepository(db *sql.DB) (*KindRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &KindRepository{adapter: adapter}, nil
}

var _ service.KindRepository = (*KindRepository)(nil)

// kindTable describes the fixed table names of one scope. They never come
// from user input.
type kindTable struct {
	table string
	// usedBy counts the rows that use a kind.
	usedBy string
}

func tableFor(scope service.KindScope) (kindTable, error) {
	switch scope {
	case service.KindScopeEvent:
		return kindTable{table: "event_kinds", usedBy: "events"}, nil
	case service.KindScopeResource:
		return kindTable{table: "resource_kinds", usedBy: "resources"}, nil
	}
	return kindTable{}, service.ErrInvalidKindScope
}

func (t kindTable) columns() string {
	return `k.id, k.group_id, k.system_code, k.name_en, k.name_hu, k.position, k.archived_at, k.created_at, k.version,
		(SELECT count(*) FROM ` + t.usedBy + ` u WHERE u.group_id = k.group_id AND u.kind_id = k.id)`
}

// ListKinds returns the group's kinds by position.
func (r *KindRepository) ListKinds(ctx context.Context, scope service.KindScope, groupID int64) ([]service.Kind, error) {
	db, table, err := r.open(ctx, scope)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+table.columns()+` FROM `+table.table+` k WHERE k.group_id = $1 ORDER BY k.position, k.id`, groupID)
	if err != nil {
		return nil, mapKindError(err)
	}
	defer func() { _ = rows.Close() }()
	kinds := make([]service.Kind, 0)
	for rows.Next() {
		kind, err := scanKind(rows)
		if err != nil {
			return nil, err
		}
		kinds = append(kinds, kind)
	}
	if err := rows.Err(); err != nil {
		return nil, mapKindError(err)
	}
	return kinds, nil
}

// FindKind returns one kind of the group, archived or not.
func (r *KindRepository) FindKind(ctx context.Context, scope service.KindScope, groupID, kindID int64) (service.Kind, error) {
	db, table, err := r.open(ctx, scope)
	if err != nil {
		return service.Kind{}, err
	}
	if groupID <= 0 || kindID <= 0 {
		return service.Kind{}, service.ErrKindNotFound
	}
	return scanKind(db.QueryRowContext(ctx, `SELECT `+table.columns()+` FROM `+table.table+` k WHERE k.group_id = $1 AND k.id = $2`, groupID, kindID))
}

// CreateKind appends one kind after the group's last position.
func (r *KindRepository) CreateKind(ctx context.Context, scope service.KindScope, kind service.Kind) (service.Kind, error) {
	db, table, err := r.open(ctx, scope)
	if err != nil {
		return service.Kind{}, err
	}
	var kindID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO `+table.table+` (group_id, name_en, name_hu, position, created_at, version)
		VALUES ($1, $2, $3, (SELECT COALESCE(MAX(position) + 1, 0) FROM `+table.table+` WHERE group_id = $1), $4, 0)
		RETURNING id
	`, kind.GroupID, kind.NameEN, kind.NameHU, kind.CreatedAt).Scan(&kindID); err != nil {
		return service.Kind{}, mapKindError(err)
	}
	return r.FindKind(ctx, scope, kind.GroupID, kindID)
}

// UpdateKind writes names and archive state when expectedVersion is current.
func (r *KindRepository) UpdateKind(ctx context.Context, scope service.KindScope, kind service.Kind, expectedVersion int64) (service.Kind, error) {
	db, table, err := r.open(ctx, scope)
	if err != nil {
		return service.Kind{}, err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE `+table.table+` SET name_en = $1, name_hu = $2, archived_at = $3, version = version + 1
		WHERE group_id = $4 AND id = $5 AND version = $6
	`, kind.NameEN, kind.NameHU, kind.ArchivedAt, kind.GroupID, kind.ID, expectedVersion)
	if err != nil {
		return service.Kind{}, mapKindError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return service.Kind{}, mapKindError(err)
	}
	if rows == 0 {
		return service.Kind{}, r.classifyStale(ctx, db, table, kind.GroupID, kind.ID)
	}
	return r.FindKind(ctx, scope, kind.GroupID, kind.ID)
}

// MoveKind swaps the kind with its neighbour by renumbering the group's
// positions.
func (r *KindRepository) MoveKind(ctx context.Context, scope service.KindScope, groupID, kindID int64, offset int) error {
	db, table, err := r.open(ctx, scope)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapKindError(err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM `+table.table+` WHERE group_id = $1 ORDER BY position, id FOR UPDATE`, groupID)
	if err != nil {
		return mapKindError(err)
	}
	var ids []int64
	at := -1
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return mapKindError(err)
		}
		if id == kindID {
			at = len(ids)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return mapKindError(err)
	}
	_ = rows.Close()
	if at < 0 {
		return service.ErrKindNotFound
	}
	to := at + offset
	if to < 0 || to >= len(ids) {
		return nil
	}
	ids[at], ids[to] = ids[to], ids[at]
	positions := make([]int32, len(ids))
	for i := range positions {
		positions[i] = int32(i)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE `+table.table+` k SET position = v.position
		FROM unnest($1::bigint[], $2::int[]) AS v(id, position)
		WHERE k.group_id = $3 AND k.id = v.id
	`, ids, positions, groupID); err != nil {
		return mapKindError(err)
	}
	if err := tx.Commit(); err != nil {
		return mapKindError(err)
	}
	return nil
}

// DeleteKind removes a kind nothing references.
func (r *KindRepository) DeleteKind(ctx context.Context, scope service.KindScope, groupID, kindID, expectedVersion int64) error {
	db, table, err := r.open(ctx, scope)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM `+table.table+` WHERE group_id = $1 AND id = $2 AND version = $3`, groupID, kindID, expectedVersion)
	if err != nil {
		return mapKindError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapKindError(err)
	}
	if rows == 0 {
		return r.classifyStale(ctx, db, table, groupID, kindID)
	}
	return nil
}

// SeedDefaultKinds inserts both default lists for a group, keeping rows that
// already carry a system code.
func (r *KindRepository) SeedDefaultKinds(ctx context.Context, groupID int64, createdAt int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	return SeedDefaultKinds(ctx, db, groupID, createdAt)
}

type kindExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// SeedDefaultKinds inserts the default event kinds and resource kinds of a
// group; rows with an existing system code are kept.
func SeedDefaultKinds(ctx context.Context, executor kindExecutor, groupID, createdAt int64) error {
	for _, scope := range []service.KindScope{service.KindScopeEvent, service.KindScopeResource} {
		table, err := tableFor(scope)
		if err != nil {
			return err
		}
		for position, kind := range service.DefaultKinds(scope) {
			if _, err := executor.ExecContext(ctx, `
				INSERT INTO `+table.table+` (group_id, system_code, name_en, name_hu, position, created_at)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (group_id, system_code) DO NOTHING
			`, groupID, kind.Code, kind.NameEN, kind.NameHU, position, createdAt); err != nil {
				return fmt.Errorf("seed %s: %w", table.table, mapKindError(err))
			}
		}
	}
	return nil
}

func (r *KindRepository) classifyStale(ctx context.Context, db *sql.DB, table kindTable, groupID, kindID int64) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+table.table+` WHERE group_id = $1 AND id = $2)`, groupID, kindID).Scan(&exists); err != nil {
		return mapKindError(err)
	}
	return service.ClassifyStaleWrite(!exists)
}

func (r *KindRepository) open(ctx context.Context, scope service.KindScope) (*sql.DB, kindTable, error) {
	table, err := tableFor(scope)
	if err != nil {
		return nil, kindTable{}, err
	}
	db, err := r.database(ctx)
	return db, table, err
}

func (r *KindRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func scanKind(row rowScanner) (service.Kind, error) {
	var (
		kind       service.Kind
		code       sql.NullString
		archivedAt sql.NullInt64
	)
	if err := row.Scan(&kind.ID, &kind.GroupID, &code, &kind.NameEN, &kind.NameHU, &kind.Position, &archivedAt, &kind.CreatedAt, &kind.Version, &kind.Uses); err != nil {
		return service.Kind{}, mapKindError(err)
	}
	if kind.ID <= 0 || kind.GroupID <= 0 || kind.Version < 0 || kind.NameEN == "" {
		return service.Kind{}, ErrInvalidData
	}
	kind.SystemCode = code.String
	kind.ArchivedAt = nullableInt64(archivedAt)
	return kind, nil
}

func mapKindError(err error) error {
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
		return service.ErrKindNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && (pgErr.ConstraintName == "event_kinds_active_name_unique" || pgErr.ConstraintName == "resource_kinds_active_name_unique"):
			return service.ErrKindNameTaken
		case pgErr.Code == "23503" && (pgErr.ConstraintName == "events_kind_fk" || pgErr.ConstraintName == "resources_kind_fk"):
			return service.ErrKindInUse
		}
	}
	return ClassifyError(err)
}
