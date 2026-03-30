// 遵循project_guide.md
package services

// coa_sync_reset_test.go — DB-backed integration tests for
// SyncDefaultAccountsForCompany and ResetCompanyCOA.
//
// All tests use an isolated in-memory SQLite database so they are fully
// independent and require no external DB connection.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"gobooks/internal/models"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func testCOADB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:coa_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.COATemplate{},
		&models.COATemplateAccount{},
		&models.JournalEntry{},
		&models.JournalLine{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedTemplate inserts the default COA template into the test DB.
func seedTemplate(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := SeedDefaultCOATemplate(db); err != nil {
		t.Fatal(err)
	}
}

// makeCompany creates a minimal Company row and returns its ID.
func makeCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{
		Name:                    "Test Co",
		EntityType:              models.EntityTypeIncorporated,
		BusinessType:            models.BusinessTypeProfessionalCorp,
		Industry:                models.IndustryConsulting,
		IncorporatedDate:        "2020-01-01",
		FiscalYearEnd:           "12-31",
		AccountCodeLength:       4,
		AccountCodeLengthLocked: true,
		IsActive:                true,
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// countAccounts returns the number of account rows for a company.
func countAccounts(t *testing.T, db *gorm.DB, companyID uint) int64 {
	t.Helper()
	var n int64
	db.Model(&models.Account{}).Where("company_id = ?", companyID).Count(&n)
	return n
}

// ── SyncDefaultAccountsForCompany ────────────────────────────────────────────

// TestSync_FreshCompanyAddsAll verifies that syncing a company with no existing
// accounts adds all 52 template accounts.
func TestSync_FreshCompanyAddsAll(t *testing.T) {
	db := testCOADB(t)
	seedTemplate(t, db)
	cid := makeCompany(t, db)

	added, updated, err := SyncDefaultAccountsForCompany(db, cid, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated != 0 {
		t.Errorf("expected 0 updates on fresh company, got %d", updated)
	}
	wantAdded := len(defaultTemplateAccounts)
	if added != wantAdded {
		t.Errorf("added = %d, want %d", added, wantAdded)
	}
	if n := countAccounts(t, db, cid); n != int64(wantAdded) {
		t.Errorf("account count = %d, want %d", n, wantAdded)
	}
}

// TestSync_Idempotent verifies that a second sync on a fully-provisioned company
// produces 0 adds and 0 updates.
func TestSync_Idempotent(t *testing.T) {
	db := testCOADB(t)
	seedTemplate(t, db)
	cid := makeCompany(t, db)

	// First sync: populate everything.
	if _, _, err := SyncDefaultAccountsForCompany(db, cid, 4); err != nil {
		t.Fatal(err)
	}

	// Second sync: should be a no-op.
	added, updated, err := SyncDefaultAccountsForCompany(db, cid, 4)
	if err != nil {
		t.Fatalf("unexpected error on second sync: %v", err)
	}
	if added != 0 || updated != 0 {
		t.Errorf("second sync: added=%d updated=%d, want both 0", added, updated)
	}
}

// TestSync_AddsMissingAccounts verifies that only missing accounts are inserted
// when a company already has a partial COA.
func TestSync_AddsMissingAccounts(t *testing.T) {
	db := testCOADB(t)
	seedTemplate(t, db)
	cid := makeCompany(t, db)

	// Pre-seed a small "old-style" COA (6 accounts).
	preexisting := []models.Account{
		{CompanyID: cid, Code: "1000", Name: "Cash", RootAccountType: models.RootAsset, DetailAccountType: models.DetailOtherCurrentAsset, IsActive: true, IsSystemDefault: true},
		{CompanyID: cid, Code: "1100", Name: "Bank", RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, IsActive: true, IsSystemDefault: true},
		{CompanyID: cid, Code: "2000", Name: "Accounts Payable", RootAccountType: models.RootLiability, DetailAccountType: models.DetailAccountsPayable, IsActive: true, IsSystemDefault: true},
		{CompanyID: cid, Code: "3000", Name: "Share Capital", RootAccountType: models.RootEquity, DetailAccountType: models.DetailShareCapital, IsActive: true, IsSystemDefault: true},
		{CompanyID: cid, Code: "4000", Name: "Revenue", RootAccountType: models.RootRevenue, DetailAccountType: models.DetailSalesRevenue, IsActive: true, IsSystemDefault: true},
		{CompanyID: cid, Code: "6100", Name: "Rent", RootAccountType: models.RootExpense, DetailAccountType: models.DetailRentExpense, IsActive: true, IsSystemDefault: true},
	}
	if err := db.Create(&preexisting).Error; err != nil {
		t.Fatal(err)
	}

	added, _, err := SyncDefaultAccountsForCompany(db, cid, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	total := countAccounts(t, db, cid)
	wantTotal := int64(len(defaultTemplateAccounts))
	if total != wantTotal {
		t.Errorf("total accounts = %d, want %d", total, wantTotal)
	}
	wantAdded := len(defaultTemplateAccounts) - len(preexisting)
	if added != wantAdded {
		t.Errorf("added = %d, want %d", added, wantAdded)
	}
}

// TestSync_UpdatesSystemDefaultNames verifies that outdated names on
// is_system_default accounts are corrected to match the template.
func TestSync_UpdatesSystemDefaultNames(t *testing.T) {
	db := testCOADB(t)
	seedTemplate(t, db)
	cid := makeCompany(t, db)

	// Insert one system-default account with a stale name.
	stale := models.Account{
		CompanyID:         cid,
		Code:              "1000",
		Name:              "Old Cash Name",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailOtherCurrentAsset,
		IsActive:          true,
		IsSystemDefault:   true,
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}

	_, updated, err := SyncDefaultAccountsForCompany(db, cid, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated != 1 {
		t.Errorf("expected 1 update (name correction), got %d", updated)
	}

	var refreshed models.Account
	db.Where("company_id = ? AND code = ?", cid, "1000").First(&refreshed)
	if refreshed.Name != "Cash" {
		t.Errorf("name after sync = %q, want %q", refreshed.Name, "Cash")
	}
}

// TestSync_SkipsUserCreatedAccounts verifies that accounts with
// is_system_default = false are never modified, even if their name differs
// from the template.
func TestSync_SkipsUserCreatedAccounts(t *testing.T) {
	db := testCOADB(t)
	seedTemplate(t, db)
	cid := makeCompany(t, db)

	// User-created account on a template code slot.
	userAcc := models.Account{
		CompanyID:         cid,
		Code:              "1000",
		Name:              "My Custom Cash Account",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailOtherCurrentAsset,
		IsActive:          true,
		IsSystemDefault:   false, // user-created
	}
	if err := db.Create(&userAcc).Error; err != nil {
		t.Fatal(err)
	}

	_, updated, err := SyncDefaultAccountsForCompany(db, cid, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated != 0 {
		t.Errorf("expected 0 updates (user account must not be touched), got %d", updated)
	}

	var unchanged models.Account
	db.Where("company_id = ? AND code = ?", cid, "1000").First(&unchanged)
	if unchanged.Name != "My Custom Cash Account" {
		t.Errorf("user account name was modified: got %q", unchanged.Name)
	}
}

// ── ResetCompanyCOA ───────────────────────────────────────────────────────────

// TestReset_FreshCompanyWorks verifies a reset on a company with no journal
// entries succeeds and produces the full template set.
func TestReset_FreshCompanyWorks(t *testing.T) {
	db := testCOADB(t)
	seedTemplate(t, db)
	cid := makeCompany(t, db)

	// Pre-populate with old partial COA.
	old := models.Account{
		CompanyID: cid, Code: "9999", Name: "Legacy",
		RootAccountType: models.RootExpense, DetailAccountType: models.DetailOtherExpense,
		IsActive: true, IsSystemDefault: false,
	}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return ResetCompanyCOA(tx, cid, 4)
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	n := countAccounts(t, db, cid)
	want := int64(len(defaultTemplateAccounts))
	if n != want {
		t.Errorf("after reset: account count = %d, want %d", n, want)
	}

	// Old "9999" account must be gone.
	var legacy models.Account
	if err := db.Where("company_id = ? AND code = ?", cid, "9999").First(&legacy).Error; err == nil {
		t.Errorf("legacy account 9999 still present after reset")
	}
}

// TestReset_BlockedWhenJournalLinesExist verifies that ErrAccountsInUse is
// returned when any account in the company is referenced by a journal line.
func TestReset_BlockedWhenJournalLinesExist(t *testing.T) {
	db := testCOADB(t)
	seedTemplate(t, db)
	cid := makeCompany(t, db)

	// Create one account.
	acc := models.Account{
		CompanyID: cid, Code: "1000", Name: "Cash",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailOtherCurrentAsset,
		IsActive: true, IsSystemDefault: true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}

	// Create a journal entry + line referencing that account.
	je := models.JournalEntry{CompanyID: cid, JournalNo: "JE-001", Status: models.JournalEntryStatusPosted}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}
	jl := models.JournalLine{
		CompanyID: cid, JournalEntryID: je.ID, AccountID: acc.ID,
	}
	if err := db.Create(&jl).Error; err != nil {
		t.Fatal(err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return ResetCompanyCOA(tx, cid, 4)
	})
	if !errors.Is(err, ErrAccountsInUse) {
		t.Errorf("expected ErrAccountsInUse, got: %v", err)
	}
}
