// 遵循project_guide.md
package main

import (
	"log"

	"balanciz/internal/config"
	"balanciz/internal/db"
	"balanciz/internal/logging"
)

// balanciz-migrate is the canonical migration entry point.
//
// It runs two phases in order, both idempotent:
//
//  1. GORM AutoMigrate — creates/alters tables based on model structs.
//  2. SQL file migrations — applies tracked *.sql files from the migrations/
//     directory in alphabetical order, recording each in schema_migrations.
//
// Run this before starting the application in any environment:
//
//	go run ./cmd/balanciz-migrate          # local
//	./balanciz-migrate                     # binary
//	docker compose run --rm migrate       # docker
func main() {
	logging.Init()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}
	logging.SetLevel(cfg.LogLevel)

	gormDB, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}

	// Phase 1: GORM AutoMigrate (creates tables, adds columns — never drops).
	logging.L().Info("running GORM AutoMigrate")
	if err := db.Migrate(gormDB); err != nil {
		log.Fatalf("gorm migrate failed: %v", err)
	}
	logging.L().Info("GORM AutoMigrate complete")

	// Phase 2: SQL file migrations (tracked via schema_migrations table).
	logging.L().Info("applying SQL file migrations", "dir", "migrations")
	if err := db.ApplySQLMigrations(gormDB, "migrations"); err != nil {
		log.Fatalf("sql migrate failed: %v", err)
	}
	logging.L().Info("all migrations applied successfully")
}
