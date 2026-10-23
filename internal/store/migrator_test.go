package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"testing/fstest"
	"time"
)

func TestLoadMigrationsSortsAndValidates(t *testing.T) {
	tests := []struct {
		name    string
		files   fstest.MapFS
		want    []Migration
		wantErr error
	}{
		{
			name: "sorts files",
			files: fstest.MapFS{
				"000002_add_groups.up.sql":   {Data: []byte("CREATE TABLE groups (id BIGINT);\n")},
				"000001_create_users.up.sql": {Data: []byte("CREATE TABLE users (id BIGINT);\n")},
				"README.md":                  {Data: []byte("ignored")},
			},
			want: []Migration{
				{Version: 1, Description: "create_users", SQL: "CREATE TABLE users (id BIGINT);\n"},
				{Version: 2, Description: "add_groups", SQL: "CREATE TABLE groups (id BIGINT);\n"},
			},
		},
		{
			name:    "rejects invalid name",
			files:   fstest.MapFS{"000001_bad name.up.sql": {Data: []byte("SELECT 1;")}},
			wantErr: ErrInvalidMigrationName,
		},
		{
			name:    "rejects empty migration",
			files:   fstest.MapFS{"000001_empty.up.sql": {Data: []byte("  \n")}},
			wantErr: ErrInvalidMigrationName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := loadMigrations(test.files)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("loadMigrations() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("loadMigrations() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRunnerApplyInstallsPendingMigrations(t *testing.T) {
	database := &fakeDatabase{installed: map[int64]installedMigration{}}
	runner, err := newMigrator(database, fstest.MapFS{
		"000002_second.up.sql": {Data: []byte("SELECT 2;")},
		"000001_first.up.sql":  {Data: []byte("SELECT 1;")},
	})
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}

	if err := runner.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !database.committed {
		t.Fatal("Apply() did not commit transaction")
	}
	if got, want := database.appliedSQL, []string{"SELECT 1;", "SELECT 2;"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied SQL = %#v, want %#v", got, want)
	}
	if len(database.recorded) != 2 {
		t.Fatalf("recorded migrations = %d, want 2", len(database.recorded))
	}
}

func TestRunnerApplyRejectsMigrationDrift(t *testing.T) {
	database := &fakeDatabase{installed: map[int64]installedMigration{
		1: {description: "renamed", installedAt: time.Now()},
	}}
	runner, err := newMigrator(database, fstest.MapFS{
		"000001_first.up.sql": {Data: []byte("SELECT 1;")},
	})
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}

	err = runner.Apply(context.Background())
	if !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("Apply() error = %v, want %v", err, ErrMigrationDrift)
	}
	if database.committed {
		t.Fatal("Apply() committed drifted migration")
	}
}

func TestRunnerStatus(t *testing.T) {
	installedAt := time.Unix(100, 0)
	database := &fakeDatabase{installed: map[int64]installedMigration{
		1: {description: "first", installedAt: installedAt},
	}}
	runner, err := newMigrator(database, fstest.MapFS{
		"000001_first.up.sql":  {Data: []byte("SELECT 1;")},
		"000002_second.up.sql": {Data: []byte("SELECT 2;")},
	})
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}

	statuses, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(statuses) != 2 || !statuses[0].Installed || statuses[1].Installed {
		t.Fatalf("Status() = %#v, want first installed and second pending", statuses)
	}
	if statuses[0].InstalledAt == nil || !statuses[0].InstalledAt.Equal(installedAt) {
		t.Fatalf("Status()[0].InstalledAt = %v, want %v", statuses[0].InstalledAt, installedAt)
	}
}

type fakeDatabase struct {
	installed  map[int64]installedMigration
	appliedSQL []string
	recorded   []int64
	committed  bool
	rolledBack bool
}

func (d *fakeDatabase) BeginTx(context.Context, *sql.TxOptions) (migrationTx, error) {
	return &fakeTransaction{database: d}, nil
}

func (d *fakeDatabase) QueryContext(_ context.Context, query string, _ ...any) (migrationRows, error) {
	if query != installedSQL {
		return nil, errors.New("unexpected query: " + query)
	}
	return d.rows(), nil
}

func (d *fakeDatabase) rows() migrationRows {
	rows := make([][]any, 0, len(d.installed))
	for version, migration := range d.installed {
		rows = append(rows, []any{version, migration.description, migration.installedAt})
	}
	return &fakeRows{rows: rows}
}

type fakeTransaction struct {
	database *fakeDatabase
}

func (tx *fakeTransaction) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	switch query {
	case createLedgerSQL:
		return fakeResult{}, nil
	case insertMigration:
		version := args[0].(int64)
		tx.database.recorded = append(tx.database.recorded, version)
		tx.database.installed[version] = installedMigration{description: args[1].(string), installedAt: time.Unix(200, 0)}
		return fakeResult{}, nil
	default:
		if query == lockSQL {
			return fakeResult{}, nil
		}
		tx.database.appliedSQL = append(tx.database.appliedSQL, query)
		return fakeResult{}, nil
	}
}

func (tx *fakeTransaction) QueryContext(_ context.Context, query string, _ ...any) (migrationRows, error) {
	if query == lockSQL {
		return &fakeRows{rows: [][]any{{int64(1)}}}, nil
	}
	if query == installedSQL {
		return tx.database.rows(), nil
	}
	return nil, errors.New("unexpected query: " + query)
}

func (tx *fakeTransaction) Commit() error {
	tx.database.committed = true
	return nil
}

func (tx *fakeTransaction) Rollback() error {
	tx.database.rolledBack = true
	return nil
}

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 0, nil }

type fakeRows struct {
	rows   [][]any
	index  int
	closed bool
}

func (r *fakeRows) Next() bool {
	if r.index >= len(r.rows) {
		return false
	}
	r.index++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	values := r.rows[r.index-1]
	if len(dest) != len(values) {
		return errors.New("scan destination count mismatch")
	}
	for index, value := range values {
		switch target := dest[index].(type) {
		case *any:
			*target = value
		case *int64:
			*target = value.(int64)
		case *string:
			*target = value.(string)
		case *time.Time:
			*target = value.(time.Time)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

func (r *fakeRows) Err() error { return nil }

func (r *fakeRows) Close() error {
	r.closed = true
	return nil
}
