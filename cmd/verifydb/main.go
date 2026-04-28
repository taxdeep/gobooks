// 遵循project_guide.md
// verifydb loads .env and pings PostgreSQL (same path as the app).
package main

import (
	"fmt"
	"log"
	"os"

	"balanciz/internal/config"
	"balanciz/internal/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	gormDB, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		log.Fatalf("sql db: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("ping: %v", err)
	}
	fmt.Fprintf(os.Stdout, "OK: connected to %s:%s db=%s user=%s\n", cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser)
}
