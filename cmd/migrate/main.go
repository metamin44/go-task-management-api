// Command migrate applies the embedded SQL migrations via the migration
// runner. Configuration comes from config.LoadDB (environment over optional
// .env) — database settings only, so migrations do not require application
// secrets such as AUTH_ACCESS_SECRET or SMTP credentials. Only secret-safe
// errors are printed; configuration, DSNs, and secrets are never logged. The
// migrate driver manages its own database connection — no application pgxpool
// is created here.
package main

import (
	"fmt"
	"os"

	"github.com/metamin44/go-task-management-api/internal/config"
	"github.com/metamin44/go-task-management-api/internal/migration"
	"github.com/metamin44/go-task-management-api/migrations"
)

func main() {
	db, err := config.LoadDB()
	if err != nil {
		// config errors contain variable names and safe explanations only.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := migration.Run(db, migrations.FS); err != nil {
		// runner errors are static and secret-safe.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("migrations applied successfully")
}
