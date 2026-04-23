// 遵循project_guide.md
package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"gobooks/internal/services/search_engine"
)

// Config holds all application configuration in one place.
// Keep it small and obvious for beginners.
type Config struct {
	Env      string
	Addr     string
	LogLevel string // LOG_LEVEL: DEBUG | INFO | WARN | ERROR (default: INFO)

	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string
	AISecretKey string

	// PublicBaseURL is the canonical public origin of this deployment.
	// Used to build payment provider return URLs (success / cancel).
	// Must be scheme+host with no trailing slash, e.g. "https://app.example.com".
	// Set via APP_PUBLIC_URL environment variable.
	// If empty, handlers fall back to the request host (logged as WARN).
	PublicBaseURL string

	// SearchEngine selects between the legacy fallback and the ent-backed
	// search projection.
	//
	// Phase 5+ default: "ent" — the new projection-backed engine is the
	// shipping path and `legacy` exists only as a temporary escape hatch.
	// We default to ent intentionally: in pre-prod windows a "default
	// legacy" trap silently leaves new code paths un-exercised and
	// accumulates migration debt across modules.
	//
	// Valid values:
	//   ent     → query search_documents projection (default)
	//   legacy  → return empty results + WARN log (sunset path; remove
	//             after one full release cycle of ent stability)
	//   dual    → reserved for legacy + ent diff comparison; not yet
	//             implemented — falls back to legacy at runtime
	//
	// Unknown values cause Load to fail fast — never silently degrade to
	// a different engine than the operator asked for.
	//
	// Read once at startup; SIGHUP reload intentionally unsupported.
	SearchEngine string
}

// Load reads .env (if present) and then reads environment variables.
// Environment variables always win.
func Load() (Config, error) {
	// .env is optional (nice for local dev). If it doesn't exist, ignore.
	_ = godotenv.Load()

	cfg := Config{
		Env:        getenv("APP_ENV", "dev"),
		Addr:       getenv("APP_ADDR", ":6768"),
		LogLevel:   getenv("LOG_LEVEL", "INFO"),
		DBHost:     getenv("DB_HOST", "localhost"),
		DBPort:     getenv("DB_PORT", "5432"),
		DBUser:     getenv("DB_USER", "gobooks"),
		DBPassword: getenv("DB_PASSWORD", "gobooks"),
		DBName:     getenv("DB_NAME", "gobooks"),
		DBSSLMode:  getenv("DB_SSLMODE", "disable"),
		AISecretKey:   getenv("AI_SECRET_KEY", ""),
		PublicBaseURL: getenv("APP_PUBLIC_URL", ""),
		// Empty string here is fine — search_engine.ParseMode resolves it
		// to DefaultMode (ent). Validation below catches typos.
		SearchEngine: os.Getenv("SEARCH_ENGINE"),
	}

	if cfg.DBHost == "" || cfg.DBPort == "" || cfg.DBUser == "" || cfg.DBName == "" {
		return Config{}, fmt.Errorf("missing required DB config")
	}

	// Fail-fast on unknown SEARCH_ENGINE values — silent fallback to a
	// different engine than the operator asked for is exactly the kind
	// of surprise that should crash startup, not be papered over.
	if _, err := search_engine.ParseMode(cfg.SearchEngine); err != nil {
		return Config{}, fmt.Errorf("invalid SEARCH_ENGINE: %w", err)
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

