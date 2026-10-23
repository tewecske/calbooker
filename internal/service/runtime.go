package service

import (
	"context"
	"errors"
	"runtime"
	"time"
)

// ErrInvalidRuntimeContext identifies a missing runtime query context.
var ErrInvalidRuntimeContext = errors.New("service: invalid runtime context")

// BuildVersion is the application version. Release builds override it with
// -ldflags "-X github.com/tewecske/calbooker/internal/service.BuildVersion=<version>".
var BuildVersion = "dev"

// MigrationInfo describes one applied or pending database migration.
type MigrationInfo struct {
	Rank        int
	Version     int64
	Description string
	Installed   bool
	InstalledAt *time.Time
}

// MigrationStatusProvider reports migration installation state.
type MigrationStatusProvider interface {
	MigrationStatus(context.Context) ([]MigrationInfo, error)
}

// RuntimeInfo contains live process facts. It never includes credentials.
type RuntimeInfo struct {
	Version        string
	StartedAt      int64
	Uptime         time.Duration
	GoVersion      string
	GOOS           string
	GOARCH         string
	NumCPU         int
	GOMAXPROCS     int
	Goroutines     int
	HeapAllocBytes uint64
	SysBytes       uint64
	NumGC          uint32
}

// RuntimeInfoService reports live process and migration facts.
type RuntimeInfoService struct {
	version    string
	startedAt  time.Time
	migrations MigrationStatusProvider
	now        func() time.Time
}

// NewRuntimeInfoService constructs runtime reporting with the process start
// time captured at construction.
func NewRuntimeInfoService(startedAt time.Time) *RuntimeInfoService {
	return &RuntimeInfoService{version: BuildVersion, startedAt: startedAt, now: time.Now}
}

// SetMigrations attaches the migration status provider. It is optional and is
// intended to be set once during startup.
func (s *RuntimeInfoService) SetMigrations(provider MigrationStatusProvider) {
	if s == nil {
		return
	}
	s.migrations = provider
}

// RuntimeInfo returns a live snapshot of process runtime facts.
func (s *RuntimeInfoService) RuntimeInfo() RuntimeInfo {
	if s == nil {
		return RuntimeInfo{}
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	info := RuntimeInfo{
		Version:        s.version,
		StartedAt:      s.startedAt.Unix(),
		GoVersion:      runtime.Version(),
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		NumCPU:         runtime.NumCPU(),
		GOMAXPROCS:     runtime.GOMAXPROCS(0),
		Goroutines:     runtime.NumGoroutine(),
		HeapAllocBytes: stats.HeapAlloc,
		SysBytes:       stats.Sys,
		NumGC:          stats.NumGC,
	}
	if !s.startedAt.IsZero() {
		info.Uptime = now().Sub(s.startedAt)
	}
	return info
}

// Migrations returns migration status in version order. A missing provider
// returns no migrations rather than failing the system page.
func (s *RuntimeInfoService) Migrations(ctx context.Context) ([]MigrationInfo, error) {
	if ctx == nil {
		return nil, ErrInvalidRuntimeContext
	}
	if s == nil || s.migrations == nil {
		return nil, nil
	}
	return s.migrations.MigrationStatus(ctx)
}
