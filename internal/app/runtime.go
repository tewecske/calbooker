package app

import (
	"context"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
)

// migrationStatusProvider adapts the migration runner to the service runtime
// port so the HTTP adapter never imports the store package.
type migrationStatusProvider struct {
	runner *store.Runner
}

// NewMigrationStatusProvider adapts a migration runner for the system page.
// A nil runner returns a nil provider.
func NewMigrationStatusProvider(runner *store.Runner) service.MigrationStatusProvider {
	if runner == nil {
		return nil
	}
	return migrationStatusProvider{runner: runner}
}

// MigrationStatus maps runner status into the service-facing migration info.
func (p migrationStatusProvider) MigrationStatus(ctx context.Context) ([]service.MigrationInfo, error) {
	statuses, err := p.runner.Status(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]service.MigrationInfo, 0, len(statuses))
	for rank, status := range statuses {
		info := service.MigrationInfo{
			Rank:        rank + 1,
			Version:     status.Version,
			Description: status.Description,
			Installed:   status.Installed,
		}
		if status.InstalledAt != nil {
			installedAt := *status.InstalledAt
			info.InstalledAt = &installedAt
		}
		infos = append(infos, info)
	}
	return infos, nil
}
