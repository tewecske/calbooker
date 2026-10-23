// Package templates contains embedded server-rendered HTML templates.
package templates

import "embed"

// FS contains all application templates.
//
//go:embed *.html
var FS embed.FS
