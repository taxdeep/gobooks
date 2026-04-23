// 遵循project_guide.md
//
// Package searchprojection owns the denormalized read model that powers the
// upgraded SmartPicker / global-search UI. It is the only package allowed
// to write to the search_documents / search_recent_queries /
// search_usage_stats tables.
//
// Architecture:
//
//	┌──────────────────────┐
//	│ services.* (GORM)    │ writes business truth: invoices, customers, …
//	└──────────┬───────────┘
//	           │ post-commit, explicit call
//	           ▼
//	┌──────────────────────┐
//	│ searchprojection     │ updates search_documents (ent / pgx)
//	│  Projector.Upsert    │ via the Normalizer interface for i18n flex
//	│  Projector.Delete    │
//	└──────────┬───────────┘
//	           │ read
//	           ▼
//	┌──────────────────────┐
//	│ services/search_eng. │ legacy | dual | ent — chosen by feature flag
//	└──────────────────────┘
//
// Phase 0 (this commit): interface + AsciiNormalizer + skeleton Upsert/
// Delete that no caller invokes yet. The HTTP / SmartPicker path is
// untouched. Phase 1 fills in the customer + vendor projector calls and
// flips the feature flag to `dual` for cross-checking.
package searchprojection

import (
	"context"
	"time"
)

// Document is the input shape for Projector.Upsert. Producers (services
// that own a particular entity type) build one of these and hand it off
// — the projector is responsible for normalisation, version stamping,
// and persistence.
//
// Fields without comments map 1:1 to columns in search_documents.
type Document struct {
	CompanyID  uint
	EntityType string // "invoice" | "customer" | "vendor" | "product_service" | …
	EntityID   uint

	DocNumber string // raw, not lowercased — the Normalizer handles it
	Title     string // primary display text
	Subtitle  string // secondary line, pre-formatted by producer
	Memo      string // optional descriptive text used as low-priority match field

	DocDate  *time.Time // recency signal; nil for non-time-keyed entities
	Amount   string     // pre-formatted ("$1,234.56"); empty for non-money entities
	Currency string
	Status   string

	URLPath string // detail-page URL — caller knows the routing convention
}

// Projector is the write interface used by domain services after a
// successful business commit. Implementations MUST be company-scoped:
// every method takes an explicit companyID and rejects calls where the
// document's CompanyID disagrees, so a producer that mis-loaded an
// entity from a foreign tenant can never silently project the row.
type Projector interface {
	// Upsert inserts or updates the projection row for
	// (companyID, doc.EntityType, doc.EntityID). The companyID parameter
	// is the AUTHORITATIVE tenant scope from the request context;
	// doc.CompanyID is the loaded entity's stored value. The two MUST
	// match — if they don't, Upsert logs at ERROR and returns
	// ErrCompanyMismatch without touching the projection table.
	//
	// Idempotent — safe to retry. Returns nil on success; transient DB
	// errors are surfaced (callers in handlers log + continue; backfill
	// continues to the next row).
	Upsert(ctx context.Context, companyID uint, doc Document) error

	// Delete removes the projection row for (companyID, entityType, entityID).
	// No-op if the row doesn't exist.
	Delete(ctx context.Context, companyID uint, entityType string, entityID uint) error
}

// CurrentProjectorVersion stamps every row written by today's projector.
// Bump this constant whenever the projector's output shape changes
// (e.g. a new normalisation method, additional fields in subtitle);
// the reconciler picks up rows where projector_version < current and
// re-runs Upsert to refresh them.
const CurrentProjectorVersion = 1
