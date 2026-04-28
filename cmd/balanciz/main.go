// 遵循project_guide.md
package main

import (
	"log"
	"time"

	"balanciz/internal/config"
	"balanciz/internal/db"
	"balanciz/internal/logging"
	"balanciz/internal/services"
	_ "balanciz/internal/services/pdf" // init() registers the system-template seeder with db.Migrate
	"balanciz/internal/version"
	"balanciz/internal/web"
)

func main() {
	// 初始化结构化日志（JSON 输出到 stdout）。必须在所有其他组件之前调用。
	// LOG_LEVEL 来自环境变量；若仅在 .env 中设置，SetLevel 会在 config.Load() 后修正。
	logging.Init()

	// Load configuration from .env / environment variables.
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}
	// Re-apply log level now that godotenv has loaded .env values into the environment.
	logging.SetLevel(cfg.LogLevel)

	if err := services.ConfigureAISecretKey(cfg.AISecretKey); err != nil {
		log.Fatalf("AI secret key config failed: %v", err)
	}

	// Connect to PostgreSQL (GORM) with retry/backoff.
	gormDB, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("db connect failed: %v", err)
	}

	// Run GORM AutoMigrate: safe schema additions (idempotent, never drops columns).
	// SQL file migrations (migrations/*.sql) must be applied separately via:
	//   go run ./cmd/balanciz-migrate
	// In Docker, the migrate service handles both phases before the app starts.
	if err := db.Migrate(gormDB); err != nil {
		log.Fatalf("db migrate failed: %v", err)
	}

	// Seed the default Chart of Accounts template (idempotent; no-op if already present).
	if err := services.SeedDefaultCOATemplate(gormDB); err != nil {
		log.Fatalf("coa template seed failed: %v", err)
	}

	// Start daily cleanup goroutine for system_logs (retain 30 days).
	go func() {
		// Run once immediately on startup to catch any accumulated old rows.
		if n, err := services.CleanupSystemLogs(gormDB, 30*24*time.Hour); err != nil {
			logging.L().Warn("system_logs cleanup failed", "err", err)
		} else if n > 0 {
			logging.L().Info("system_logs cleanup", "deleted", n)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := services.CleanupSystemLogs(gormDB, 30*24*time.Hour); err != nil {
				logging.L().Warn("system_logs cleanup failed", "err", err)
			} else if n > 0 {
				logging.L().Info("system_logs cleanup", "deleted", n)
			}
		}
	}()

	// Create and start the Fiber web server.
	app := web.NewServer(cfg, gormDB)
	logging.L().Info("starting server", "version", version.Version, "addr", cfg.Addr)
	if err := app.Listen(cfg.Addr); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

