// Command migrate applies the embedded SQL migrations via the migration
// runner. Configuration comes from config.Load (environment over optional
// .env). Only secret-safe errors are printed; configuration, DSNs, and
// secrets are never logged. The migrate driver manages its own database
// connection — no application pgxpool is created here.
package main

import (
	"fmt"
	"os"

	"github.com/metamin44/go-task-management-api/internal/config"
	"github.com/metamin44/go-task-management-api/internal/migration"
	"github.com/metamin44/go-task-management-api/migrations"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// config errors contain variable names and safe explanations only.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := migration.Run(cfg.DB, migrations.FS); err != nil {
		// runner errors are static and secret-safe.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("migrations applied successfully")
}
