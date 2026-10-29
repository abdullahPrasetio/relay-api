// Package migrations embeds the SQL migration files so the migrate command
// (and any packaged binary) can run them without the source tree.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
