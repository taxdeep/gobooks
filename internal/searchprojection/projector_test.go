// 遵循project_guide.md
package searchprojection

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestNoopProjector_RequiresMandatoryFields locks down the validation
// contract every Projector implementation must honour: company scope,
// entity identity, and minimum displayability. EntProjector reuses the
// same checks so the two pass and fail the same tests.
func TestNoopProjector_RequiresMandatoryFields(t *testing.T) {
	cases := []struct {
		name      string
		companyID uint
		doc       Document
		wantErr   string
	}{
		{
			name:      "missing entity type",
			companyID: 1,
			doc:       Document{CompanyID: 1, EntityID: 1, Title: "x", URLPath: "/x"},
			wantErr:   "EntityType",
		},
		{
			name:      "missing entity id",
			companyID: 1,
			doc:       Document{CompanyID: 1, EntityType: "invoice", Title: "x", URLPath: "/x"},
			wantErr:   "EntityID",
		},
		{
			name:      "missing title",
			companyID: 1,
			doc:       Document{CompanyID: 1, EntityType: "invoice", EntityID: 1, URLPath: "/x"},
			wantErr:   "Title",
		},
		{
			name:      "missing url",
			companyID: 1,
			doc:       Document{CompanyID: 1, EntityType: "invoice", EntityID: 1, Title: "x"},
			wantErr:   "URLPath",
		},
	}
	p := NoopProjector{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Upsert(context.Background(), tc.companyID, tc.doc)
			if err == nil {
				t.Fatalf("expected error mentioning %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestNoopProjector_AcceptsValidDocument(t *testing.T) {
	p := NoopProjector{}
	err := p.Upsert(context.Background(), 1, Document{
		CompanyID:  1,
		EntityType: "customer",
		EntityID:   42,
		Title:      "Acme Corp",
		URLPath:    "/customers/42",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestProjector_RejectsCompanyMismatch is the H2 hardening contract:
// when caller-supplied companyID disagrees with the document's
// CompanyID, the Projector MUST refuse to write and return
// ErrCompanyMismatch — not silently accept whichever value won.
func TestProjector_RejectsCompanyMismatch(t *testing.T) {
	p := NoopProjector{}
	doc := Document{
		CompanyID:  2, // entity belongs to company 2
		EntityType: "customer",
		EntityID:   42,
		Title:      "Acme",
		URLPath:    "/x",
	}
	// Caller claims authority over company 1.
	err := p.Upsert(context.Background(), 1, doc)
	if !errors.Is(err, ErrCompanyMismatch) {
		t.Fatalf("expected ErrCompanyMismatch, got %v", err)
	}
}

func TestProjector_ZeroCallerCompanyIDIsAnError(t *testing.T) {
	p := NoopProjector{}
	doc := Document{
		CompanyID: 1, EntityType: "customer", EntityID: 1,
		Title: "x", URLPath: "/x",
	}
	if err := p.Upsert(context.Background(), 0, doc); err == nil {
		t.Error("expected error for caller companyID == 0")
	}
}

func TestNoopProjector_DeleteValidation(t *testing.T) {
	p := NoopProjector{}
	if err := p.Delete(context.Background(), 0, "invoice", 1); err == nil {
		t.Error("expected error for zero companyID")
	}
	if err := p.Delete(context.Background(), 1, "", 1); err == nil {
		t.Error("expected error for empty entityType")
	}
	if err := p.Delete(context.Background(), 1, "invoice", 0); err == nil {
		t.Error("expected error for zero entityID")
	}
	if err := p.Delete(context.Background(), 1, "invoice", 1); err != nil {
		t.Errorf("unexpected error on valid delete: %v", err)
	}
}
