// 遵循project_guide.md
package config

import (
	"errors"
	"strings"
	"testing"

	"gobooks/internal/services/search_engine"
)

// setRequiredDBEnv stubs the DB env vars Load() asserts so SearchEngine
// validation is the only thing under test.
func setRequiredDBEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_USER", "u")
	t.Setenv("DB_NAME", "d")
}

// Required by Phase 5.0: empty SEARCH_ENGINE → ent (DefaultMode).
func TestLoad_DefaultSearchEngineIsEnt(t *testing.T) {
	setRequiredDBEnv(t)
	t.Setenv("SEARCH_ENGINE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mode, parseErr := search_engine.ParseMode(cfg.SearchEngine)
	if parseErr != nil {
		t.Fatalf("ParseMode after Load: %v", parseErr)
	}
	if mode != search_engine.ModeEnt {
		t.Errorf("default mode = %q, want ent", mode)
	}
}

// Required by Phase 5.0: explicit SEARCH_ENGINE=ent works (selects EntEngine path).
func TestLoad_ExplicitEntPasses(t *testing.T) {
	setRequiredDBEnv(t)
	t.Setenv("SEARCH_ENGINE", "ent")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mode, _ := search_engine.ParseMode(cfg.SearchEngine)
	if mode != search_engine.ModeEnt {
		t.Errorf("explicit ent → %q, want ent", mode)
	}
}

// Required by Phase 5.0: explicit SEARCH_ENGINE=legacy still loads
// (legacy is the documented short-term fallback). Selector behaviour
// for legacy mode is asserted in search_engine package tests.
func TestLoad_ExplicitLegacyPasses(t *testing.T) {
	setRequiredDBEnv(t)
	t.Setenv("SEARCH_ENGINE", "legacy")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mode, _ := search_engine.ParseMode(cfg.SearchEngine)
	if mode != search_engine.ModeLegacy {
		t.Errorf("explicit legacy → %q, want legacy", mode)
	}
}

// Required by Phase 5.0: invalid SEARCH_ENGINE crashes Load (fail fast).
func TestLoad_InvalidSearchEngineFailsFast(t *testing.T) {
	setRequiredDBEnv(t)
	t.Setenv("SEARCH_ENGINE", "elasticsearch")

	_, err := Load()
	if err == nil {
		t.Fatal("Load should reject unknown SEARCH_ENGINE; got nil")
	}
	// Error message should mention the offending value so an operator
	// reading boot logs can fix it without spelunking.
	if !strings.Contains(err.Error(), "elasticsearch") {
		t.Errorf("error %q should mention the bad value", err)
	}
	// And it should wrap the search_engine sentinel so callers can
	// errors.Is against it programmatically.
	if !errors.Is(err, search_engine.ErrUnknownMode) {
		t.Errorf("error %v should wrap ErrUnknownMode", err)
	}
}
