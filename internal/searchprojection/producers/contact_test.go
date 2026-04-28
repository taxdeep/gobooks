// 遵循project_guide.md
package producers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/searchprojection"
)

// recordingProjector captures every Upsert / Delete call so tests can
// assert on the Document shape without needing a running Postgres +
// ent client. Mirrors searchprojection.Projector's contract and reuses
// its validators.
type recordingProjector struct {
	mu      sync.Mutex
	upserts []searchprojection.Document
	deletes []deleteKey
	// upsertErr / deleteErr let individual tests simulate persistence failures.
	upsertErr error
	deleteErr error
}

type deleteKey struct {
	CompanyID  uint
	EntityType string
	EntityID   uint
}

func (r *recordingProjector) Upsert(_ context.Context, companyID uint, d searchprojection.Document) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.CompanyID != companyID {
		return searchprojection.ErrCompanyMismatch
	}
	r.upserts = append(r.upserts, d)
	return r.upsertErr
}

func (r *recordingProjector) Delete(_ context.Context, companyID uint, entityType string, entityID uint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletes = append(r.deletes, deleteKey{companyID, entityType, entityID})
	return r.deleteErr
}

func newProducerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Customer{}, &models.Vendor{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// ── Customer ─────────────────────────────────────────────────────────

func TestCustomerDocument_Mapping(t *testing.T) {
	now := time.Now()
	c := models.Customer{
		ID:             42,
		CompanyID:      7,
		Name:           "ACME Trading Ltd.",
		Email:          "ops@acme.com",
		AddrStreet1:    "123 Main St",
		AddrCity:       "Burnaby",
		AddrProvince:   "BC",
		AddrPostalCode: "V5G 1A1",
		AddrCountry:    "Canada",
		IsActive:       true,
		CreatedAt:      now,
	}
	doc := CustomerDocument(c)
	if doc.EntityType != EntityTypeCustomer {
		t.Errorf("EntityType = %q, want %q", doc.EntityType, EntityTypeCustomer)
	}
	if doc.EntityID != 42 {
		t.Errorf("EntityID = %d, want 42", doc.EntityID)
	}
	if doc.CompanyID != 7 {
		t.Errorf("CompanyID = %d, want 7", doc.CompanyID)
	}
	if doc.Title != "ACME Trading Ltd." {
		t.Errorf("Title = %q, want %q", doc.Title, "ACME Trading Ltd.")
	}
	if doc.Subtitle != "Customer · ops@acme.com" {
		t.Errorf("Subtitle = %q", doc.Subtitle)
	}
	if doc.Status != "active" {
		t.Errorf("Status = %q, want active", doc.Status)
	}
	if doc.URLPath != "/customers/42" {
		t.Errorf("URLPath = %q", doc.URLPath)
	}
	if doc.Memo == "" || doc.Memo == c.Name {
		t.Errorf("Memo should contain address parts, got %q", doc.Memo)
	}
}

func TestCustomerDocument_InactiveGetsInactiveStatus(t *testing.T) {
	c := models.Customer{
		ID: 1, CompanyID: 1, Name: "X", IsActive: false,
	}
	doc := CustomerDocument(c)
	if doc.Status != "inactive" {
		t.Errorf("Status = %q, want inactive", doc.Status)
	}
}

func TestCustomerDocument_OmitsEmailFromSubtitleWhenMissing(t *testing.T) {
	c := models.Customer{ID: 1, CompanyID: 1, Name: "X", IsActive: true}
	doc := CustomerDocument(c)
	if doc.Subtitle != "Customer" {
		t.Errorf("Subtitle = %q, want plain 'Customer'", doc.Subtitle)
	}
}

func TestProjectCustomer_LoadsFromDBAndUpserts(t *testing.T) {
	db := newProducerTestDB(t)
	c := &models.Customer{
		CompanyID: 1,
		Name:      "Acme",
		Email:     "a@b.com",
		IsActive:  true,
	}
	if err := db.Create(c).Error; err != nil {
		t.Fatal(err)
	}
	rec := &recordingProjector{}
	if err := ProjectCustomer(context.Background(), db, rec, c.CompanyID, c.ID); err != nil {
		t.Fatalf("ProjectCustomer: %v", err)
	}
	if len(rec.upserts) != 1 {
		t.Fatalf("upserts=%d, want 1", len(rec.upserts))
	}
	if rec.upserts[0].EntityID != c.ID || rec.upserts[0].Title != "Acme" {
		t.Errorf("unexpected doc: %+v", rec.upserts[0])
	}
}

func TestProjectCustomer_NilProjectorIsNoop(t *testing.T) {
	db := newProducerTestDB(t)
	if err := ProjectCustomer(context.Background(), db, nil, 1, 9999); err != nil {
		t.Errorf("nil projector should be no-op, got %v", err)
	}
}

func TestProjectCustomer_MissingCustomerReturnsError(t *testing.T) {
	db := newProducerTestDB(t)
	rec := &recordingProjector{}
	err := ProjectCustomer(context.Background(), db, rec, 1, 9999)
	if err == nil {
		t.Error("expected error for missing customer")
	}
	if len(rec.upserts) != 0 {
		t.Error("should not upsert when load fails")
	}
}

// TestProjectCustomer_RejectsCrossTenantID is the H1 hardening contract:
// a handler that mistakenly passes a customer ID belonging to company B
// while the request is for company A must NOT result in B's row being
// projected. The producer rejects with ErrEntityNotInCompany.
func TestProjectCustomer_RejectsCrossTenantID(t *testing.T) {
	db := newProducerTestDB(t)
	companyA := &models.Customer{CompanyID: 1, Name: "Co-A Customer", IsActive: true}
	companyB := &models.Customer{CompanyID: 2, Name: "Co-B Customer", IsActive: true}
	if err := db.Create(companyA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(companyB).Error; err != nil {
		t.Fatal(err)
	}
	rec := &recordingProjector{}
	// Authenticated as company 1, but caller supplies B's customer ID.
	err := ProjectCustomer(context.Background(), db, rec, 1, companyB.ID)
	if !errors.Is(err, ErrEntityNotInCompany) {
		t.Errorf("expected ErrEntityNotInCompany, got %v", err)
	}
	if len(rec.upserts) != 0 {
		t.Errorf("nothing should be upserted on cross-tenant attempt, got %d", len(rec.upserts))
	}
}

func TestProjectCustomer_ZeroCompanyIDIsAnError(t *testing.T) {
	db := newProducerTestDB(t)
	rec := &recordingProjector{}
	if err := ProjectCustomer(context.Background(), db, rec, 0, 1); err == nil {
		t.Error("expected error for companyID == 0")
	}
}

func TestDeleteCustomerProjection_PassesTriple(t *testing.T) {
	rec := &recordingProjector{}
	if err := DeleteCustomerProjection(context.Background(), rec, 7, 42); err != nil {
		t.Fatal(err)
	}
	if len(rec.deletes) != 1 {
		t.Fatalf("deletes=%d, want 1", len(rec.deletes))
	}
	got := rec.deletes[0]
	if got.CompanyID != 7 || got.EntityType != EntityTypeCustomer || got.EntityID != 42 {
		t.Errorf("unexpected delete: %+v", got)
	}
}

// ── Vendor ───────────────────────────────────────────────────────────

func TestVendorDocument_Mapping(t *testing.T) {
	now := time.Now()
	v := models.Vendor{
		ID: 11, CompanyID: 3, Name: "Lighting Geek Technologies Inc.",
		Email: "ap@lgtek.com", Phone: "604-555-0199",
		Address: "456 Industrial Way", Notes: "Monthly SaaS invoice",
		IsActive: true, CreatedAt: now,
	}
	doc := VendorDocument(v)
	if doc.EntityType != EntityTypeVendor {
		t.Errorf("EntityType = %q", doc.EntityType)
	}
	if doc.Title != v.Name {
		t.Errorf("Title mismatch")
	}
	if doc.Subtitle != "Vendor · ap@lgtek.com" {
		t.Errorf("Subtitle = %q", doc.Subtitle)
	}
	if doc.URLPath != "/vendors/11" {
		t.Errorf("URLPath = %q", doc.URLPath)
	}
	// memo should concatenate Address + Notes
	if doc.Memo == "" {
		t.Errorf("Memo should concat Address + Notes, got empty")
	}
}

func TestVendorDocument_FallsBackToPhoneWhenEmailMissing(t *testing.T) {
	v := models.Vendor{
		ID: 1, CompanyID: 1, Name: "Bare Vendor",
		Phone: "604-555-0100", IsActive: true,
	}
	doc := VendorDocument(v)
	if doc.Subtitle != "Vendor · 604-555-0100" {
		t.Errorf("Subtitle = %q", doc.Subtitle)
	}
}

func TestProjectVendor_LoadsFromDBAndUpserts(t *testing.T) {
	db := newProducerTestDB(t)
	v := &models.Vendor{CompanyID: 1, Name: "LGTek", IsActive: true}
	if err := db.Create(v).Error; err != nil {
		t.Fatal(err)
	}
	rec := &recordingProjector{}
	if err := ProjectVendor(context.Background(), db, rec, v.CompanyID, v.ID); err != nil {
		t.Fatal(err)
	}
	if len(rec.upserts) != 1 || rec.upserts[0].Title != "LGTek" {
		t.Errorf("unexpected upserts: %+v", rec.upserts)
	}
}

func TestProjectVendor_RejectsCrossTenantID(t *testing.T) {
	db := newProducerTestDB(t)
	a := &models.Vendor{CompanyID: 1, Name: "A", IsActive: true}
	b := &models.Vendor{CompanyID: 2, Name: "B", IsActive: true}
	if err := db.Create(a).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(b).Error; err != nil {
		t.Fatal(err)
	}
	rec := &recordingProjector{}
	err := ProjectVendor(context.Background(), db, rec, 1, b.ID)
	if !errors.Is(err, ErrEntityNotInCompany) {
		t.Errorf("expected ErrEntityNotInCompany, got %v", err)
	}
	if len(rec.upserts) != 0 {
		t.Error("must not upsert cross-tenant vendor")
	}
}
