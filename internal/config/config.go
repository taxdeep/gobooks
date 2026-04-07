// 遵循project_guide.md
package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
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
	}

	if cfg.DBHost == "" || cfg.DBPort == "" || cfg.DBUser == "" || cfg.DBName == "" {
		return Config{}, fmt.Errorf("missing required DB config")
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

