package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeInfoServiceReportsLiveFacts(t *testing.T) {
	started := time.Unix(1_000, 0)
	service := NewRuntimeInfoService(started)
	service.now = func() time.Time { return started.Add(90 * time.Minute) }

	info := service.RuntimeInfo()
	if info.Version == "" || info.GoVersion == "" || info.GOOS == "" || info.GOARCH == "" {
		t.Fatalf("runtime info = %+v, want populated fields", info)
	}
	if info.StartedAt != started.Unix() || info.Uptime != 90*time.Minute {
		t.Fatalf("runtime timing = %d/%s, want %d/90m", info.StartedAt, info.Uptime, started.Unix())
	}
	if info.NumCPU <= 0 || info.GOMAXPROCS <= 0 || info.Goroutines <= 0 {
		t.Fatalf("runtime counts = %+v, want positive", info)
	}
}

func TestRuntimeInfoServiceMigrationsAreOptional(t *testing.T) {
	service := NewRuntimeInfoService(time.Now())
	migrations, err := service.Migrations(context.Background())
	if err != nil || migrations != nil {
		t.Fatalf("Migrations() = %v, %v; want nil, nil", migrations, err)
	}
}

func TestRuntimeInfoServiceUsesMigrationProvider(t *testing.T) {
	installedAt := time.Unix(5, 0)
	service := NewRuntimeInfoService(time.Now())
	service.SetMigrations(migrationStatusStub{infos: []MigrationInfo{{Rank: 1, Version: 1, Description: "create_users", Installed: true, InstalledAt: &installedAt}}})
	migrations, err := service.Migrations(context.Background())
	if err != nil {
		t.Fatalf("Migrations() error = %v", err)
	}
	if len(migrations) != 1 || migrations[0].Description != "create_users" {
		t.Fatalf("migrations = %+v, want one entry", migrations)
	}
}

func TestRuntimeInfoServiceRejectsNilContext(t *testing.T) {
	service := NewRuntimeInfoService(time.Now())
	if _, err := service.Migrations(nil); !errors.Is(err, ErrInvalidRuntimeContext) {
		t.Fatalf("Migrations(nil) error = %v, want %v", err, ErrInvalidRuntimeContext)
	}
}

func TestRuntimeInfoServiceHandlesNilReceiver(t *testing.T) {
	var service *RuntimeInfoService
	if info := service.RuntimeInfo(); info.Version != "" {
		t.Fatalf("nil receiver info = %+v, want zero", info)
	}
	if _, err := service.Migrations(context.Background()); err != nil {
		t.Fatalf("nil receiver Migrations() error = %v, want nil", err)
	}
}

type migrationStatusStub struct {
	infos []MigrationInfo
	err   error
}

func (s migrationStatusStub) MigrationStatus(context.Context) ([]MigrationInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.infos, nil
}
