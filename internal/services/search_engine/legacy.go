// 遵循project_guide.md
package search_engine

import (
	"context"

	"gobooks/internal/logging"
)

// LegacyEngine is the temporary fallback for /api/global-search while
// the original SmartPicker fan-out continues to serve per-entity
// pickers (the SmartPicker HTTP path is unchanged and unaffected).
//
// Phase 5 contract: LegacyEngine.Search NEVER fails. It returns an
// empty SearchResponse and emits a WARN log line with enough context to
// identify what was lost. The HTTP handler MUST treat empty results
// from this engine as "no matches" rather than an error condition —
// 500s here would block the dropdown UI in production for what is, by
// our own design choice, a deliberately-stubbed code path.
//
// Sunset: this engine exists only to give operators an escape hatch
// during the ent rollout. Once ent has been the default for one full
// release cycle without incident, delete LegacyEngine entirely along
// with the SEARCH_ENGINE flag's "legacy" value.
type LegacyEngine struct{}

// NewLegacyEngine returns the empty-result fallback engine.
func NewLegacyEngine() *LegacyEngine { return &LegacyEngine{} }

func (*LegacyEngine) Mode() Mode { return ModeLegacy }

// Search returns an empty candidate list. Logs WARN so an operator who
// has accidentally pinned to legacy can spot the cause of "the search
// box never returns anything." The query and company context go into
// the log line for triage.
func (*LegacyEngine) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	logging.L().Warn(
		"search_engine.LegacyEngine: empty result — SEARCH_ENGINE=legacy is a temporary fallback, switch to ent for projection-backed search",
		"company_id", req.CompanyID,
		"query", req.Query,
		"limit", req.Limit,
	)
	return &SearchResponse{
		Candidates: []Candidate{},
		Source:     "legacy_empty",
	}, nil
}

// SearchAdvanced mirrors Search's empty-fallback contract for the
// /advanced-search page. Same WARN log so operators see the cause.
func (*LegacyEngine) SearchAdvanced(ctx context.Context, req AdvancedRequest) (*AdvancedResponse, error) {
	logging.L().Warn(
		"search_engine.LegacyEngine: empty advanced search result — SEARCH_ENGINE=legacy is a temporary fallback, switch to ent",
		"company_id", req.CompanyID,
		"query", req.Query,
		"entity_type", req.EntityType,
	)
	return &AdvancedResponse{
		Rows:     []Candidate{},
		Total:    0,
		Page:     req.Page,
		PageSize: req.PageSize,
	}, nil
}
