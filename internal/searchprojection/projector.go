// 遵循project_guide.md
package searchprojection

import (
	"context"
	"errors"
)

// NoopProjector is the test / fallback Projector: it validates the input
// shape (same rules as the real projector) and then does nothing. Useful
// when:
//   - ent wiring is not available (e.g. during backfill dry-runs or CLI
//     tools that don't need to write);
//   - unit tests exercise producer code and want to assert "would have
//     projected this document" without involving ent.
//
// Mirrors EntProjector's company-mismatch check so the two implementations
// fail the same tests and surface the same class of bug.
type NoopProjector struct{}

func (NoopProjector) Upsert(ctx context.Context, companyID uint, doc Document) error {
	if companyID == 0 {
		return errors.New("searchprojection: companyID is required")
	}
	if doc.CompanyID != companyID {
		return ErrCompanyMismatch
	}
	return validateDocument(doc)
}

func (NoopProjector) Delete(ctx context.Context, companyID uint, entityType string, entityID uint) error {
	return validateDeleteArgs(companyID, entityType, entityID)
}

// applyNormalizer materialises the three normalised forms of a document
// into something the persistence layer can store. Pure function — kept
// here so future EntProjector and the test harness share the same
// normalisation pipeline.
type normalised struct {
	TitleNative   string
	TitleLatin    string
	TitleInitials string
	MemoNative    string
}

func applyNormalizer(n Normalizer, doc Document) normalised {
	if n == nil {
		n = AsciiNormalizer{}
	}
	return normalised{
		TitleNative:   n.Native(doc.Title),
		TitleLatin:    n.Latin(doc.Title),
		TitleInitials: n.Initials(doc.Title),
		MemoNative:    n.Native(doc.Memo),
	}
}
