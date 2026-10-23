package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	createLedgerSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY,
		description TEXT NOT NULL,
		installed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`
	lockSQL         = "SELECT pg_advisory_xact_lock($1)"
	installedSQL    = "SELECT version, description, installed_at FROM schema_migrations ORDER BY version"
	insertMigration = "INSERT INTO schema_migrations (version, description) VALUES ($1, $2)"
	defaultLockKey  = int64(672328)
	migrationNameRE = `^(\d{6})_([a-z0-9][a-z0-9_-]*)\.up\.sql$`
)

var (
	// ErrNilDatabase identifies a missing database handle.
	ErrNilDatabase = errors.New("store: nil database")
	// ErrNilMigrationFS identifies a missing migration filesystem.
	ErrNilMigrationFS = errors.New("store: nil migration filesystem")
	// ErrInvalidMigrationName identifies a migration file with an unsafe name.
	ErrInvalidMigrationName = errors.New("store: invalid migration name")
	// ErrDuplicateMigration identifies duplicate migration versions.
	ErrDuplicateMigration = errors.New("store: duplicate migration version")
	// ErrMigrationDrift identifies a changed migration after installation.
	ErrMigrationDrift = errors.New("store: migration metadata drift")

	migrationNamePattern = regexp.MustCompile(migrationNameRE)
)

type migrationRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type migrationTx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (migrationRows, error)
	Commit() error
	Rollback() error
}

type migrationDatabase interface {
	BeginTx(context.Context, *sql.TxOptions) (migrationTx, error)
	QueryContext(context.Context, string, ...any) (migrationRows, error)
}

type sqlDatabase struct {
	db *sql.DB
}

func (d sqlDatabase) BeginTx(ctx context.Context, options *sql.TxOptions) (migrationTx, error) {
	tx, err := d.db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return sqlTransaction{tx: tx}, nil
}

func (d sqlDatabase) QueryContext(ctx context.Context, query string, args ...any) (migrationRows, error) {
	return d.db.QueryContext(ctx, query, args...)
}

type sqlTransaction struct {
	tx *sql.Tx
}

func (t sqlTransaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

func (t sqlTransaction) QueryContext(ctx context.Context, query string, args ...any) (migrationRows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

func (t sqlTransaction) Commit() error {
	return t.tx.Commit()
}

func (t sqlTransaction) Rollback() error {
	return t.tx.Rollback()
}

// Migration describes one ordered database migration.
type Migration struct {
	Version     int64
	Description string
	SQL         string
}

// MigrationStatus describes whether a source migration is installed.
type MigrationStatus struct {
	Version     int64
	Description string
	Installed   bool
	InstalledAt *time.Time
}

// Runner applies embedded migrations and reports their installation state.
type Runner struct {
	db      migrationDatabase
	source  fs.FS
	lockKey int64
}

// NewMigrator creates a migration runner backed by database/sql.
func NewMigrator(db *sql.DB, source fs.FS) (*Runner, error) {
	if db == nil {
		return nil, ErrNilDatabase
	}
	return newMigrator(sqlDatabase{db: db}, source)
}

func newMigrator(db migrationDatabase, source fs.FS) (*Runner, error) {
	if db == nil {
		return nil, ErrNilDatabase
	}
	if source == nil {
		return nil, ErrNilMigrationFS
	}
	return &Runner{db: db, source: source, lockKey: defaultLockKey}, nil
}

// Apply installs all source migrations not present in the ledger.
func (r *Runner) Apply(ctx context.Context) error {
	if ctx == nil {
		return errors.New("store: nil migration context")
	}

	migrations, err := loadMigrations(r.source)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, createLedgerSQL); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	if err := acquireMigrationLock(ctx, tx, r.lockKey); err != nil {
		return err
	}
	installed, err := readInstalled(ctx, tx)
	if err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	if err := validateInstalled(migrations, installed); err != nil {
		return err
	}

	for _, migration := range migrations {
		if _, exists := installed[migration.Version]; exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			return fmt.Errorf("apply migration %06d: %w", migration.Version, err)
		}
		if _, err := tx.ExecContext(ctx, insertMigration, migration.Version, migration.Description); err != nil {
			return fmt.Errorf("record migration %06d: %w", migration.Version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// Status reports source migrations and their ledger state in version order.
func (r *Runner) Status(ctx context.Context) ([]MigrationStatus, error) {
	if ctx == nil {
		return nil, errors.New("store: nil migration context")
	}

	migrations, err := loadMigrations(r.source)
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, installedSQL)
	if err != nil {
		return nil, fmt.Errorf("read migration ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()

	installed, err := scanInstalled(rows)
	if err != nil {
		return nil, fmt.Errorf("scan migration ledger: %w", err)
	}
	if err := validateInstalled(migrations, installed); err != nil {
		return nil, err
	}

	statuses := make([]MigrationStatus, 0, len(migrations))
	for _, migration := range migrations {
		status := MigrationStatus{
			Version:     migration.Version,
			Description: migration.Description,
		}
		if entry, exists := installed[migration.Version]; exists {
			status.Installed = true
			installedAt := entry.installedAt
			status.InstalledAt = &installedAt
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

type installedMigration struct {
	description string
	installedAt time.Time
}

func loadMigrations(source fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int64]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		match := migrationNamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidMigrationName, entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("%w: %s", ErrInvalidMigrationName, entry.Name())
		}
		if _, exists := seen[version]; exists {
			return nil, fmt.Errorf("%w: %06d", ErrDuplicateMigration, version)
		}
		seen[version] = struct{}{}

		sqlBytes, err := fs.ReadFile(source, path.Join(".", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(sqlBytes)) == "" {
			return nil, fmt.Errorf("%w: empty migration %s", ErrInvalidMigrationName, entry.Name())
		}
		migrations = append(migrations, Migration{
			Version:     version,
			Description: match[2],
			SQL:         string(sqlBytes),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

func acquireMigrationLock(ctx context.Context, tx migrationTx, lockKey int64) error {
	rows, err := tx.QueryContext(ctx, lockSQL, lockKey)
	if err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := rows.Scan(new(any)); err != nil {
			return fmt.Errorf("scan migration lock: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read migration lock: %w", err)
	}
	return nil
}

func readInstalled(ctx context.Context, tx migrationTx) (map[int64]installedMigration, error) {
	rows, err := tx.QueryContext(ctx, installedSQL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanInstalled(rows)
}

func scanInstalled(rows migrationRows) (map[int64]installedMigration, error) {
	installed := make(map[int64]installedMigration)
	for rows.Next() {
		var version int64
		var description string
		var installedAt time.Time
		if err := rows.Scan(&version, &description, &installedAt); err != nil {
			return nil, err
		}
		if _, exists := installed[version]; exists {
			return nil, fmt.Errorf("%w: duplicate ledger version %06d", ErrMigrationDrift, version)
		}
		installed[version] = installedMigration{description: description, installedAt: installedAt}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return installed, nil
}

func validateInstalled(migrations []Migration, installed map[int64]installedMigration) error {
	source := make(map[int64]string, len(migrations))
	for _, migration := range migrations {
		source[migration.Version] = migration.Description
	}
	for version, entry := range installed {
		description, exists := source[version]
		if !exists || description != entry.description {
			return fmt.Errorf("%w: version %06d", ErrMigrationDrift, version)
		}
	}
	return nil
}
