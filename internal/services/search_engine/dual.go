// 遵循project_guide.md
package search_engine

import (
	"context"
	"errors"
)

// DualEngine runs both the legacy and ent backends, returns the legacy
// result to the caller, and emits a structured-log diff between the two
// for offline review. Used during the validation window between Phase 4
// (engine wired) and the final cutover to ent-only.
//
// Phase 0 ships only the type. The dual-run + diff logic lands in Phase
// 1 (Contacts projection) so we can validate per-entity before transactions
// (which are the highest-volume path).
type DualEngine struct {
	legacy *LegacyEngine
	ent    *EntEngine
}

// NewDualEngine wires the two backing engines.
func NewDualEngine(legacy *LegacyEngine, ent *EntEngine) *DualEngine {
	return &DualEngine{legacy: legacy, ent: ent}
}

func (*DualEngine) Mode() Mode { return ModeDual }

func (*DualEngine) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	return nil, errors.New("search_engine: DualEngine.Search not yet implemented (Phase 1)")
}

func (*DualEngine) SearchAdvanced(ctx context.Context, req AdvancedRequest) (*AdvancedResponse, error) {
	return nil, errors.New("search_engine: DualEngine.SearchAdvanced not yet implemented")
}
