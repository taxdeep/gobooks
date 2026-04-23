// 遵循project_guide.md
package searchprojection

import (
	"context"
	"errors"
	"fmt"

	"entgo.io/ent/dialect/sql"

	"gobooks/ent"
	"gobooks/ent/searchdocument"
	"gobooks/internal/logging"
)

// ErrCompanyMismatch is returned by Projector.Upsert when the supplied
// companyID does not match doc.CompanyID. Indicates a producer wrote
// the wrong CompanyID into the Document — almost always a programmer
// bug (e.g. handler skipped tenant-ownership check before loading the
// entity). Callers that see this error MUST log + investigate; never
// retry with a coerced value.
var ErrCompanyMismatch = errors.New("searchprojection: document CompanyID disagrees with caller-supplied companyID")

// EntProjector is the production Projector implementation. Writes to the
// search_documents table via the ent client. Safe for concurrent use —
// ent.Client is itself safe to share across goroutines.
//
// Contract recap (see interface.go):
//   - Upsert is idempotent on (CompanyID, EntityType, EntityID).
//   - Delete is a no-op when the row doesn't exist.
//   - Field validation lives on every entry point so producers can't slip
//     in malformed rows. The NoopProjector reuses the same validator so
//     both implementations fail the same tests.
type EntProjector struct {
	client     *ent.Client
	normalizer Normalizer
}

// NewEntProjector wires a real projector around an ent client + a
// Normalizer. Passing nil normalizer falls back to AsciiNormalizer, which
// is the Phase 0/1 production default until the CJK implementation lands.
func NewEntProjector(client *ent.Client, n Normalizer) (*EntProjector, error) {
	if client == nil {
		return nil, errors.New("searchprojection: ent client is required")
	}
	if n == nil {
		n = AsciiNormalizer{}
	}
	return &EntProjector{client: client, normalizer: n}, nil
}

// Upsert inserts the projection row or — on conflict with the company /
// entity-type / entity-id unique index — updates every column with the
// new values. PostgreSQL handles this atomically via ON CONFLICT DO UPDATE.
//
// companyID is the AUTHORITATIVE tenant scope passed in from the calling
// handler / backfill loop (sourced from session ctx, never user input).
// If doc.CompanyID disagrees, the call fails with ErrCompanyMismatch and
// nothing is written. This is the second line of defence after the
// producer's own load-with-company-filter; see internal/searchprojection/
// producers/contact.go for the contract.
func (p *EntProjector) Upsert(ctx context.Context, companyID uint, doc Document) error {
	if companyID == 0 {
		return errors.New("searchprojection: companyID is required")
	}
	if doc.CompanyID != companyID {
		logging.L().Error("searchprojection.Upsert company mismatch",
			"expected_company_id", companyID,
			"doc_company_id", doc.CompanyID,
			"entity_type", doc.EntityType,
			"entity_id", doc.EntityID)
		return ErrCompanyMismatch
	}
	if err := validateDocument(doc); err != nil {
		return err
	}
	norm := applyNormalizer(p.normalizer, doc)

	create := p.client.SearchDocument.Create().
		SetCompanyID(doc.CompanyID).
		SetEntityType(doc.EntityType).
		SetEntityID(doc.EntityID).
		SetDocNumber(doc.DocNumber).
		SetTitle(doc.Title).
		SetSubtitle(doc.Subtitle).
		SetTitleNative(norm.TitleNative).
		SetTitleLatin(norm.TitleLatin).
		SetTitleInitials(norm.TitleInitials).
		SetMemoNative(norm.MemoNative).
		SetAmount(doc.Amount).
		SetCurrency(doc.Currency).
		SetStatus(doc.Status).
		SetURLPath(doc.URLPath).
		SetProjectorVersion(CurrentProjectorVersion).
		SetNillableDocDate(doc.DocDate)

	// Conflict key is the triple (company_id, entity_type, entity_id);
	// see ent/schema/search_document.go for the unique-index declaration.
	// UpdateNewValues tells PostgreSQL to overwrite every user-supplied
	// column with the incoming value — the row is an eventually-consistent
	// mirror of the business truth, not a history log.
	if err := create.OnConflict(
		sql.ConflictColumns(
			searchdocument.FieldCompanyID,
			searchdocument.FieldEntityType,
			searchdocument.FieldEntityID,
		),
	).UpdateNewValues().Exec(ctx); err != nil {
		return fmt.Errorf("searchprojection: upsert %s/%d for company %d: %w",
			doc.EntityType, doc.EntityID, doc.CompanyID, err)
	}
	return nil
}

// Delete removes the projection row for the given entity, if present.
// Not finding a row is NOT an error — Delete is idempotent so callers
// can retry without special-casing.
func (p *EntProjector) Delete(ctx context.Context, companyID uint, entityType string, entityID uint) error {
	if err := validateDeleteArgs(companyID, entityType, entityID); err != nil {
		return err
	}
	_, err := p.client.SearchDocument.Delete().
		Where(
			searchdocument.CompanyIDEQ(companyID),
			searchdocument.EntityTypeEQ(entityType),
			searchdocument.EntityIDEQ(entityID),
		).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("searchprojection: delete %s/%d for company %d: %w",
			entityType, entityID, companyID, err)
	}
	return nil
}

// validateDocument is shared by EntProjector and NoopProjector so both
// reject the same shape of bad input. Exported-ish (lowercased but used
// across the package) to keep the check-list single-sourced.
func validateDocument(doc Document) error {
	if doc.CompanyID == 0 {
		return errors.New("searchprojection: CompanyID is required")
	}
	if doc.EntityType == "" {
		return errors.New("searchprojection: EntityType is required")
	}
	if doc.EntityID == 0 {
		return errors.New("searchprojection: EntityID is required")
	}
	if doc.Title == "" {
		return errors.New("searchprojection: Title is required")
	}
	if doc.URLPath == "" {
		return errors.New("searchprojection: URLPath is required")
	}
	return nil
}

func validateDeleteArgs(companyID uint, entityType string, entityID uint) error {
	if companyID == 0 {
		return errors.New("searchprojection: companyID is required")
	}
	if entityType == "" {
		return errors.New("searchprojection: entityType is required")
	}
	if entityID == 0 {
		return errors.New("searchprojection: entityID is required")
	}
	return nil
}
