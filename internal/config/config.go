// 遵循project_guide.md
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"gobooks/internal/services/search_engine"
)

// Config holds all application configuration in one place.
// Keep it small and obvious for beginners.
type Config struct {
	Env      string
	Addr     string
	LogLevel string // LOG_LEVEL: DEBUG | INFO | WARN | ERROR (default: INFO)

	DBHost      string
	DBPort      string
	DBUser      string
	DBPassword  string
	DBName      string
	DBSSLMode   string
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

	// SmartPicker learning and explainability controls.
	// Safe defaults keep deterministic local learning on and all AI behavior off.
	SmartPickerLearningEnabled         bool
	SmartPickerAILearningEnabled       bool
	SmartPickerAIHintAutoApply         bool
	SmartPickerTraceEnabled            bool
	SmartPickerDecisionTraceSampleRate float64

	// Provider-agnostic AI gateway foundation. Disabled unless explicitly enabled.
	AIGatewayEnabled       bool
	AIDefaultProvider      string
	AIDefaultCheapModel    string
	AIDefaultAdvancedModel string
	AIDefaultVisionModel   string
	AIMaxCostPerJob        float64
	AIMaxRequestsPerJob    int
}

// Load reads .env (if present) and then reads environment variables.
// Environment variables always win.
func Load() (Config, error) {
	// .env is optional (nice for local dev). If it doesn't exist, ignore.
	_ = godotenv.Load()

	cfg := Config{
		Env:           getenv("APP_ENV", "dev"),
		Addr:          getenv("APP_ADDR", ":6768"),
		LogLevel:      getenv("LOG_LEVEL", "INFO"),
		DBHost:        getenv("DB_HOST", "localhost"),
		DBPort:        getenv("DB_PORT", "5432"),
		DBUser:        getenv("DB_USER", "gobooks"),
		DBPassword:    getenv("DB_PASSWORD", "gobooks"),
		DBName:        getenv("DB_NAME", "gobooks"),
		DBSSLMode:     getenv("DB_SSLMODE", "disable"),
		AISecretKey:   getenv("AI_SECRET_KEY", ""),
		PublicBaseURL: getenv("APP_PUBLIC_URL", ""),
		// Empty string here is fine — search_engine.ParseMode resolves it
		// to DefaultMode (ent). Validation below catches typos.
		SearchEngine: os.Getenv("SEARCH_ENGINE"),

		SmartPickerLearningEnabled:         getenvBool("SMART_PICKER_LEARNING_ENABLED", true),
		SmartPickerAILearningEnabled:       getenvBool("SMART_PICKER_AI_LEARNING_ENABLED", false),
		SmartPickerAIHintAutoApply:         getenvBool("SMART_PICKER_AI_HINT_AUTO_APPLY", false),
		SmartPickerTraceEnabled:            getenvBool("SMART_PICKER_TRACE_ENABLED", false),
		SmartPickerDecisionTraceSampleRate: clamp01(getenvFloat("SMART_PICKER_DECISION_TRACE_SAMPLE_RATE", 0)),
		AIGatewayEnabled:                   getenvBool("AI_GATEWAY_ENABLED", false),
		AIDefaultProvider:                  getenv("AI_DEFAULT_PROVIDER", ""),
		AIDefaultCheapModel:                getenv("AI_DEFAULT_CHEAP_MODEL", ""),
		AIDefaultAdvancedModel:             getenv("AI_DEFAULT_ADVANCED_MODEL", ""),
		AIDefaultVisionModel:               getenv("AI_DEFAULT_VISION_MODEL", ""),
		AIMaxCostPerJob:                    getenvFloat("AI_MAX_COST_PER_JOB", 0),
		AIMaxRequestsPerJob:                getenvInt("AI_MAX_REQUESTS_PER_JOB", 0),
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

func getenvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func getenvFloat(key string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return n
}

func getenvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
