// Package migrations embeds the SQL migration files for the migration runner.
package migrations

import "embed"

// FS holds all up/down SQL migration files, rooted at the migrations
// directory (entries are the bare filenames), as required by the runner's
// iofs source driver.
//
//go:embed *.sql
var FS embed.FS
