// 遵循project_guide.md
//
// Package search_engine selects between the legacy SmartPicker fan-out and
// the upcoming ent-backed projection at request time. Driven by the
// SEARCH_ENGINE environment variable read once at server start (config.Config).
//
// Modes:
//
//	legacy → existing SmartPicker providers (status quo)
//	dual   → call legacy AND ent; return legacy results, log diffs (Phase 1+)
//	ent    → call ent only (post-validation flip)
//
// Phase 0 ships only the selector + the legacy delegate. Dual / ent
// implementations are stubs that fall back to legacy with a logged warning.
package search_engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Mode identifies which search backend the selector should dispatch to.
type Mode string

const (
	ModeLegacy Mode = "legacy"
	ModeDual   Mode = "dual"
	ModeEnt    Mode = "ent"
)

// DefaultMode is the value used when the SEARCH_ENGINE env var is empty.
// Phase 5: defaults to ent so the new projection-backed engine is the
// shipping default; legacy stays only as an explicit short-term fallback.
//
// Rationale: in pre-prod / test windows we don't want a "default = legacy"
// trap that silently leaves new code paths un-exercised. Whichever module
// completes its minimum viable closure should become the default — old
// paths exist as opt-in escape hatches with documented sunset dates.
const DefaultMode = ModeEnt

// ErrUnknownMode is returned by ParseMode when the input doesn't match
// any known engine. Operator-facing config typos must surface loudly at
// startup — never silently degrade to a different engine than asked for.
var ErrUnknownMode = errors.New("search_engine: unknown mode")

// ParseMode normalises a free-form string (typically the env var value)
// into a Mode + validation error.
//
//   - empty string  → DefaultMode (ModeEnt), no error
//   - "legacy" / "dual" / "ent" → matching Mode, no error (case-insensitive)
//   - anything else → "", ErrUnknownMode wrapped with the offending input
func ParseMode(raw string) (Mode, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return DefaultMode, nil
	case string(ModeLegacy):
		return ModeLegacy, nil
	case string(ModeDual):
		return ModeDual, nil
	case string(ModeEnt):
		return ModeEnt, nil
	default:
		return "", fmt.Errorf("%w: %q (valid: legacy | dual | ent)", ErrUnknownMode, raw)
	}
}

// Engine is the contract every backend must satisfy. Phase 0 has only one
// method (Search) so the selector compiles; Phase 4 adds Recents / Stats
// when the projection-backed engine actually has more to offer than legacy.
type Engine interface {
	// Mode returns the backend's identity for logging / metrics.
	Mode() Mode

	// Search executes a global search for the given query in the given
	// company. The Phase 0 contract is intentionally minimal — Phase 4
	// expands it once the response shape is agreed.
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}

// SearchRequest is the input shape passed through the selector. Kept tiny
// in Phase 0 — Phase 4 adds entity-type filters, group-restriction flags,
// pagination cursor, etc.
type SearchRequest struct {
	CompanyID uint
	Query     string
	Limit     int
}

// SearchResponse holds the raw candidate list. Mirrors SmartPickerItem
// shape conceptually but lives in this package to keep search_engine
// callable from non-web code (e.g. CLI / batch jobs in later phases).
type SearchResponse struct {
	Candidates []Candidate
	// Source identifies whether the result came from recent-bucket
	// ordering (empty query), exact-code match, or substring match —
	// used for debug / dual-run diffing.
	Source string
}

// Candidate is the unified shape for the upgraded SmartPicker / global
// search response. Extends the original SmartPickerItem with the five
// fields that make grouped navigation-style results possible:
//
//   - GroupKey    programmatic bucket ("transactions" / "contacts" / …)
//   - GroupLabel  display string for the group header
//   - ActionKind  "navigate" (open URL) vs "select" (fill a form field)
//   - URL         detail-page URL, used when ActionKind == "navigate"
//   - EntityType  exact row type ("invoice" / "customer" / …) — drives
//                 icon + per-type rendering
//
// Existing form-fill callers (Invoice line-item picker, etc.) ignore
// the navigation fields and treat the candidate as selectable — so the
// upgrade is backwards-compatible with the legacy SmartPickerItem.
type Candidate struct {
	ID         string
	Primary    string
	Secondary  string
	GroupKey   string
	GroupLabel string
	ActionKind string // "navigate" | "select"
	URL        string
	EntityType string
	Payload    map[string]string
}

// Action kind constants. Centralised so grep finds all sites that rely
// on the discriminator.
const (
	ActionNavigate = "navigate"
	ActionSelect   = "select"
)

// Group key constants — match the taxonomy the UI dropdown renders.
const (
	GroupJumpTo       = "jump_to"
	GroupTransactions = "transactions"
	GroupContacts     = "contacts"
	GroupProducts     = "products"
)

// Selector picks the right Engine implementation for the configured Mode
// and dispatches calls. Initialise once at server startup; concurrent
// callers share the same Selector.
type Selector struct {
	mode    Mode
	legacy  Engine
	dual    Engine
	entImpl Engine
}

// NewSelector wires the available engine implementations and the mode.
// `legacy` is mandatory; `dual` and `entImpl` may be nil during the
// transitional rollout (selector falls back to legacy).
func NewSelector(mode Mode, legacy, dual, entImpl Engine) *Selector {
	return &Selector{
		mode:    mode,
		legacy:  legacy,
		dual:    dual,
		entImpl: entImpl,
	}
}

// Mode reports which backend Selector currently dispatches to.
func (s *Selector) Mode() Mode { return s.mode }

// Search dispatches to the engine the selector is configured for.
// Falls back to legacy if the requested engine isn't wired yet.
func (s *Selector) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if s == nil || s.legacy == nil {
		return nil, fmt.Errorf("search_engine: selector not initialised")
	}
	switch s.mode {
	case ModeDual:
		if s.dual != nil {
			return s.dual.Search(ctx, req)
		}
		return s.legacy.Search(ctx, req)
	case ModeEnt:
		if s.entImpl != nil {
			return s.entImpl.Search(ctx, req)
		}
		return s.legacy.Search(ctx, req)
	default:
		return s.legacy.Search(ctx, req)
	}
}
