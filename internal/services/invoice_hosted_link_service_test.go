// 遵循project_guide.md
package services

// invoice_hosted_link_service_test.go — Service-layer tests for Batch 6 hosted invoice links.
//
// Coverage:
//   TestCreateHostedLink_HappyPath             — creates link, returns plaintext, hashes stored
//   TestCreateHostedLink_TokenNotStoredInPlain  — plaintext never appears in DB row
//   TestCreateHostedLink_DuplicateBlocked       — second create returns ErrActiveLinkExists
//   TestCreateHostedLink_CompanyIsolation       — wrong company_id → error
//   TestRevokeHostedLink_HappyPath             — active link becomes revoked
//   TestRevokeHostedLink_NoActiveLink          — returns ErrNoActiveLink
//   TestRevokeHostedLink_CompanyIsolation      — cannot revoke another company's link
//   TestRegenerateHostedLink_HappyPath         — old token invalidated, new plaintext returned
//   TestRegenerateHostedLink_NoExistingLink    — works even with no prior active link
//   TestRegenerateHostedLink_CompanyIsolation  — wrong company → error
//   TestGetActiveHostedLink_Found              — returns active link
//   TestGetActiveHostedLink_NotFound           — returns ErrNoActiveLink
//   TestValidateHostedToken_ValidToken         — returns link
//   TestValidateHostedToken_InvalidToken       — random string → ErrInvalidHostedToken
//   TestValidateHostedToken_RevokedToken       — revoked link → ErrInvalidHostedToken
//   TestValidateHostedToken_ExpiredToken       — expired link → ErrInvalidHostedToken
//   TestValidateHostedToken_EmptyToken         — empty string → ErrInvalidHostedToken
//   TestRecordHostedLinkView_Increments        — view_count +1, last_viewed_at set

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── test DB ──────────────────────────────────────────────────────────────────

func hostedLinkTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:svc_hosted_link_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Invoice{},
		&models.InvoiceHostedLink{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedHostedCompanyAndInvoice(t *testing.T, db *gorm.DB, status models.InvoiceStatus) (models.Company, models.Invoice) {
	t.Helper()
	co := models.Company{Name: "Test Co", BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Cust"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		InvoiceNumber: "INV-001",
		Status:        status,
	}
	db.Create(&inv)
	return co, inv
}

// ── CreateHostedLink ─────────────────────────────────────────────────────────

func TestCreateHostedLink_HappyPath(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)

	plaintext, link, err := CreateHostedLink(db, co.ID, inv.ID, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plaintext == "" {
		t.Fatal("expected non-empty plaintext token")
	}
	if link == nil || link.ID == 0 {
		t.Fatal("expected link with ID")
	}
	if link.Status != models.InvoiceHostedLinkStatusActive {
		t.Fatalf("expected active, got %q", link.Status)
	}
}

func TestCreateHostedLink_TokenNotStoredInPlain(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)

	plaintext, link, err := CreateHostedLink(db, co.ID, inv.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The token_hash stored in the DB must not equal the plaintext.
	if link.TokenHash == plaintext {
		t.Fatal("plaintext token must not be stored in DB — only hash should be stored")
	}
	// The hash should be the sha256 of the plaintext.
	expected := hashHostedToken(plaintext)
	if link.TokenHash != expected {
		t.Fatalf("token_hash mismatch: got %q, want sha256(%q)=%q", link.TokenHash, plaintext, expected)
	}
}

func TestCreateHostedLink_DuplicateBlocked(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)

	if _, _, err := CreateHostedLink(db, co.ID, inv.ID, nil); err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	_, _, err := CreateHostedLink(db, co.ID, inv.ID, nil)
	if !errors.Is(err, ErrActiveLinkExists) {
		t.Fatalf("expected ErrActiveLinkExists, got %v", err)
	}
}

func TestCreateHostedLink_CompanyIsolation(t *testing.T) {
	db := hostedLinkTestDB(t)
	_, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)

	otherCo := models.Company{Name: "Other Co", BaseCurrencyCode: "USD"}
	db.Create(&otherCo)

	_, _, err := CreateHostedLink(db, otherCo.ID, inv.ID, nil)
	if err == nil {
		t.Fatal("expected error when company_id does not match invoice")
	}
}

// ── RevokeHostedLink ─────────────────────────────────────────────────────────

func TestRevokeHostedLink_HappyPath(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)
	CreateHostedLink(db, co.ID, inv.ID, nil)

	if err := RevokeHostedLink(db, co.ID, inv.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify link is now revoked in DB.
	var link models.InvoiceHostedLink
	db.Where("invoice_id = ?", inv.ID).First(&link)
	if link.Status != models.InvoiceHostedLinkStatusRevoked {
		t.Fatalf("expected revoked, got %q", link.Status)
	}
	if link.RevokedAt == nil {
		t.Fatal("expected revoked_at to be set")
	}
}

func TestRevokeHostedLink_NoActiveLink(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)

	err := RevokeHostedLink(db, co.ID, inv.ID)
	if !errors.Is(err, ErrNoActiveLink) {
		t.Fatalf("expected ErrNoActiveLink, got %v", err)
	}
}

func TestRevokeHostedLink_CompanyIsolation(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)
	CreateHostedLink(db, co.ID, inv.ID, nil)

	otherCo := models.Company{Name: "Other Co", BaseCurrencyCode: "USD"}
	db.Create(&otherCo)

	err := RevokeHostedLink(db, otherCo.ID, inv.ID)
	// Should return ErrNoActiveLink because filter includes company_id.
	if !errors.Is(err, ErrNoActiveLink) {
		t.Fatalf("expected ErrNoActiveLink for cross-company revoke, got %v", err)
	}
}

// ── RegenerateHostedLink ─────────────────────────────────────────────────────

func TestRegenerateHostedLink_HappyPath(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)

	pt1, _, _ := CreateHostedLink(db, co.ID, inv.ID, nil)

	pt2, link2, err := RegenerateHostedLink(db, co.ID, inv.ID, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt2 == "" || pt2 == pt1 {
		t.Fatal("expected a different non-empty plaintext token after regenerate")
	}
	if link2.Status != models.InvoiceHostedLinkStatusActive {
		t.Fatalf("new link should be active, got %q", link2.Status)
	}

	// Old token must no longer be valid.
	if _, err := ValidateHostedToken(db, pt1); !errors.Is(err, ErrInvalidHostedToken) {
		t.Fatalf("old token should be invalid after regenerate, got err=%v", err)
	}
	// New token must be valid.
	if _, err := ValidateHostedToken(db, pt2); err != nil {
		t.Fatalf("new token should be valid, got %v", err)
	}
}

func TestRegenerateHostedLink_NoExistingLink(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)

	// Regenerate with no prior active link — should still create one.
	pt, link, err := RegenerateHostedLink(db, co.ID, inv.ID, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt == "" || link == nil {
		t.Fatal("expected plaintext and link")
	}
}

func TestRegenerateHostedLink_CompanyIsolation(t *testing.T) {
	db := hostedLinkTestDB(t)
	_, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)
	otherCo := models.Company{Name: "Other Co", BaseCurrencyCode: "USD"}
	db.Create(&otherCo)

	_, _, err := RegenerateHostedLink(db, otherCo.ID, inv.ID, nil)
	if err == nil {
		t.Fatal("expected error when company_id does not match invoice")
	}
}

// ── GetActiveHostedLink ──────────────────────────────────────────────────────

func TestGetActiveHostedLink_Found(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)
	CreateHostedLink(db, co.ID, inv.ID, nil)

	link, err := GetActiveHostedLink(db, co.ID, inv.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if link.Status != models.InvoiceHostedLinkStatusActive {
		t.Fatalf("expected active, got %q", link.Status)
	}
}

func TestGetActiveHostedLink_NotFound(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)

	_, err := GetActiveHostedLink(db, co.ID, inv.ID)
	if !errors.Is(err, ErrNoActiveLink) {
		t.Fatalf("expected ErrNoActiveLink, got %v", err)
	}
}

// ── ValidateHostedToken ──────────────────────────────────────────────────────

func TestValidateHostedToken_ValidToken(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)
	plaintext, _, _ := CreateHostedLink(db, co.ID, inv.ID, nil)

	link, err := ValidateHostedToken(db, plaintext)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if link.InvoiceID != inv.ID {
		t.Fatalf("expected invoice %d, got %d", inv.ID, link.InvoiceID)
	}
}

func TestValidateHostedToken_InvalidToken(t *testing.T) {
	db := hostedLinkTestDB(t)
	// Completely random / non-existent token.
	_, err := ValidateHostedToken(db, "notarealtokenthatexists")
	if !errors.Is(err, ErrInvalidHostedToken) {
		t.Fatalf("expected ErrInvalidHostedToken, got %v", err)
	}
}

func TestValidateHostedToken_RevokedToken(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)
	plaintext, _, _ := CreateHostedLink(db, co.ID, inv.ID, nil)
	RevokeHostedLink(db, co.ID, inv.ID)

	_, err := ValidateHostedToken(db, plaintext)
	if !errors.Is(err, ErrInvalidHostedToken) {
		t.Fatalf("expected ErrInvalidHostedToken for revoked token, got %v", err)
	}
}

func TestValidateHostedToken_ExpiredToken(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)
	plaintext, link, _ := CreateHostedLink(db, co.ID, inv.ID, nil)

	// Set expires_at to the past.
	past := time.Now().Add(-time.Hour)
	db.Model(&models.InvoiceHostedLink{}).
		Where("id = ?", link.ID).
		Update("expires_at", past)

	_, err := ValidateHostedToken(db, plaintext)
	if !errors.Is(err, ErrInvalidHostedToken) {
		t.Fatalf("expected ErrInvalidHostedToken for expired token, got %v", err)
	}
}

func TestValidateHostedToken_EmptyToken(t *testing.T) {
	db := hostedLinkTestDB(t)
	_, err := ValidateHostedToken(db, "")
	if !errors.Is(err, ErrInvalidHostedToken) {
		t.Fatalf("expected ErrInvalidHostedToken for empty token, got %v", err)
	}
}

// ── RecordHostedLinkView ─────────────────────────────────────────────────────

func TestRecordHostedLinkView_Increments(t *testing.T) {
	db := hostedLinkTestDB(t)
	co, inv := seedHostedCompanyAndInvoice(t, db, models.InvoiceStatusIssued)
	_, link, _ := CreateHostedLink(db, co.ID, inv.ID, nil)

	if err := RecordHostedLinkView(db, link.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := RecordHostedLinkView(db, link.ID); err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}

	var updated models.InvoiceHostedLink
	db.First(&updated, link.ID)
	if updated.ViewCount != 2 {
		t.Fatalf("expected view_count=2, got %d", updated.ViewCount)
	}
	if updated.LastViewedAt == nil {
		t.Fatal("expected last_viewed_at to be set")
	}
}
