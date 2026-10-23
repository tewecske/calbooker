// Package fixtures provides explicit database records for integration tests.
package fixtures

import (
	"context"
	"database/sql"
)

// RowQuerier is the narrow operation used by fixture inserts. It is satisfied
// by both *sql.DB and *sql.Tx, leaving transaction ownership with the caller.
type RowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// User contains explicit values for every users column.
type User struct {
	Email           *string
	Username        *string
	DisplayName     *string
	IsGuest         bool
	IsAdmin         bool
	Theme           string
	Locale          string
	CreatedAt       int64
	EmailVerifiedAt *int64
	Version         int64
}

// NewUser returns a safe non-guest fixture without credential data.
func NewUser() User {
	return User{
		Email:     String("fixture@example.test"),
		Username:  String("fixture-user"),
		Theme:     "light",
		Locale:    "en",
		CreatedAt: 1,
	}
}

// InsertUser inserts every users column explicitly and returns its generated ID.
func InsertUser(ctx context.Context, db RowQuerier, user User) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO users (
			email, username, display_name, is_guest, is_admin,
			theme, locale, created_at, email_verified_at, version
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`, user.Email, user.Username, user.DisplayName, user.IsGuest, user.IsAdmin, user.Theme, user.Locale, user.CreatedAt, user.EmailVerifiedAt, user.Version).Scan(&id)
	return id, err
}

// Group contains explicit values for every groups column.
type Group struct {
	Name       string
	NameNorm   string
	InviteCode string
	CreatedBy  *int64
	CreatedAt  int64
	Version    int64
}

// NewGroup returns a group fixture with an explicit invite code.
func NewGroup(createdBy int64) Group {
	return Group{
		Name:       "Fixture Group",
		NameNorm:   "fixture group",
		InviteCode: "fixture-invite",
		CreatedBy:  Int64(createdBy),
		CreatedAt:  1,
	}
}

// InsertGroup inserts every groups column explicitly and returns its generated ID.
func InsertGroup(ctx context.Context, db RowQuerier, group Group) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO groups (name, name_norm, invite_code, created_by, created_at, version)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, group.Name, group.NameNorm, group.InviteCode, group.CreatedBy, group.CreatedAt, group.Version).Scan(&id)
	return id, err
}

// InsertMembership inserts an explicit group membership row.
func InsertMembership(ctx context.Context, db RowQuerier, groupID, userID int64, role string, createdAt, version int64) error {
	return db.QueryRowContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, created_at, version)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, groupID, userID, role, createdAt, version).Scan(new(int64))
}

// String returns a pointer suitable for nullable fixture fields.
func String(value string) *string { return &value }

// Int64 returns a pointer suitable for nullable fixture fields.
func Int64(value int64) *int64 { return &value }
