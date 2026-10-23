// Package migrations embeds ordered database migrations.
package migrations

import "embed"

// FS contains migration source files and their documentation.
//
// Migration files use names such as 000001_create_users.up.sql. The migration
// runner ignores files that do not use the .up.sql suffix.
//
//go:embed *
var FS embed.FS
