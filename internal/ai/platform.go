// 遵循project_guide.md
// Package ai is the application-level AI entry point.
//
// All AI completions in Balanciz must go through Platform.Complete() —
// direct calls to services.OpenAICompatibleChatCompletion from handlers are
// forbidden. This enforces:
//   - Named, versioned prompts via the registry (no inline prompt strings in handlers)
//   - Consistent per-company settings load + Enabled guard
//   - A single slog instrumentation point
package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/services"
)

// ErrAIDisabled is returned when the company has not configured or enabled AI.
var ErrAIDisabled = errors.New("AI is not enabled for this company")

// Platform is the application-level AI service.
// Create one per Server via New(db) and store it in Server.AIAssist.
type Platform struct {
	db *gorm.DB
}

// New creates a Platform backed by the given database handle.
func New(db *gorm.DB) *Platform {
	return &Platform{db: db}
}

// Complete renders the named prompt template with vars and calls the
// company's configured AI provider. It returns the plain-text response.
//
// companyID is always sourced from the authenticated session — never from
// request parameters.
//
// Returned errors:
//   - ErrAIDisabled  — company has AI disabled or not configured
//   - fmt.Errorf     — unknown prompt key, template render failure, provider error
func (p *Platform) Complete(_ context.Context, companyID uint, promptKey string, vars map[string]string) (string, error) {
	row, err := services.LoadAIConnectionSettings(p.db, companyID)
	if err != nil {
		return "", fmt.Errorf("ai: load settings: %w", err)
	}
	if !row.Enabled {
		return "", ErrAIDisabled
	}

	prompt, ok := registry[promptKey]
	if !ok {
		return "", fmt.Errorf("ai: unknown prompt key %q", promptKey)
	}

	system, user := prompt.render(vars)

	start := time.Now()
	result, err := services.OpenAICompatibleChatCompletion(row, user, system)
	elapsed := time.Since(start)

	if err != nil {
		slog.Warn("ai.complete failed",
			"prompt_key", promptKey,
			"company_id", companyID,
			"elapsed_ms", elapsed.Milliseconds(),
			"error", err,
		)
		return "", fmt.Errorf("ai: completion failed: %w", err)
	}

	slog.Info("ai.complete",
		"prompt_key", promptKey,
		"company_id", companyID,
		"elapsed_ms", elapsed.Milliseconds(),
		"response_len", len(result),
	)
	return result, nil
}

// ── Prompt definition ────────────────────────────────────────────────────────

// promptDef holds a named prompt as separate system and user templates.
// Variables are substituted with {{key}} syntax.
type promptDef struct {
	system string
	user   string
}

// render replaces {{key}} placeholders in system and user templates.
func (pd promptDef) render(vars map[string]string) (system, user string) {
	system = pd.system
	user = pd.user
	for k, v := range vars {
		placeholder := "{{" + k + "}}"
		system = strings.ReplaceAll(system, placeholder, v)
		user = strings.ReplaceAll(user, placeholder, v)
	}
	return system, user
}
