// 遵循project_guide.md
//
// balanciz-reset drops all Balanciz application tables (and the company_role enum) in the configured
// PostgreSQL database/schema so the next app start can run AutoMigrate on a clean slate.
//
// Usage (from repo root, with .env present):
//
//	go run ./cmd/balanciz-reset -print-target
//	go run ./cmd/balanciz-reset -yes -confirm-db=YOUR_DATABASE_NAME
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"balanciz/internal/config"
	"balanciz/internal/db"
)

func main() {
	printTarget := flag.Bool("print-target", false, "connect using .env, print database/schema/user, and exit (no changes)")
	yes := flag.Bool("yes", false, "required to perform destructive drop (deletes ALL Balanciz tables in the target schema)")
	confirmDB := flag.String("confirm-db", "", "must match current_database() exactly (safety check against wrong .env)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}

	gormDB, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}

	target, err := db.QueryResetTarget(gormDB)
	if err != nil {
		log.Fatalf("could not read target: %v", err)
	}

	printBanner(target, cfg)

	if *printTarget {
		fmt.Println("No changes made (-print-target).")
		os.Exit(0)
	}

	if !*yes {
		log.Println("Refusing to drop: pass -yes to confirm (see WARNING above).")
		os.Exit(1)
	}
	if strings.TrimSpace(*confirmDB) == "" {
		log.Fatal("Refusing to drop: set -confirm-db to the exact database name shown above (prevents wrong .env).")
	}
	if *confirmDB != target.DatabaseName {
		log.Fatalf("Refusing to drop: -confirm-db=%q does not match current_database()=%q", *confirmDB, target.DatabaseName)
	}

	if err := db.DropAllApplicationObjects(gormDB); err != nil {
		log.Fatalf("drop failed: %v", err)
	}

	log.Println("OK: all Balanciz application tables removed. Start balanciz; AutoMigrate will recreate schema.")
}

func printBanner(target db.ResetTarget, cfg config.Config) {
	w := strings.Repeat("=", 72)
	fmt.Println(w)
	fmt.Println("WARNING: DESTRUCTIVE DATABASE RESET (development use)")
	fmt.Println(w)
	fmt.Printf("  Database (current_database): %s\n", target.DatabaseName)
	fmt.Printf("  Schema (current_schema):     %s\n", target.SchemaName)
	fmt.Printf("  Session user:                %s\n", target.SessionUser)
	fmt.Printf("  Config host:port (from .env): %s:%s\n", cfg.DBHost, cfg.DBPort)
	fmt.Printf("  Config DB name (DB_NAME):    %s\n", cfg.DBName)
	fmt.Println()
	fmt.Println("  The following SQL will be executed (same schema as search_path):")
	fmt.Println()
	fmt.Println(strings.TrimSpace(db.ApplicationTablesSQL))
	fmt.Println()
	fmt.Println("  DROP TYPE IF EXISTS company_role CASCADE;")
	fmt.Println()
	fmt.Println(w)
}
