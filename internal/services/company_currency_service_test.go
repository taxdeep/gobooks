// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func testCompanyCurrencyDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:company_currency_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Currency{},
		&models.CompanyCurrency{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

func seedCompanyForCurrency(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{
		Name:              "Acme Corp",
		AccountCodeLength: 4,
		IsActive:          true,
		BaseCurrencyCode:  "CAD",
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedCurrency(t *testing.T, db *gorm.DB, code, name string) {
	t.Helper()
	cur := models.Currency{Code: code, Name: name, Symbol: "$", DecimalPlaces: 2, IsActive: true}
	if err := db.Create(&cur).Error; err != nil {
		t.Fatal(err)
	}
}

func seedFxAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) {
	t.Helper()
	acc := models.Account{
		CompanyID: companyID, Code: code, Name: code,
		RootAccountType: root, DetailAccountType: detail, IsActive: true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestAddCompanyCurrency_CreatesARAndAP verifies that AddCompanyCurrency creates
// both an AR and an AP account with the correct currency metadata.
func TestAddCompanyCurrency_CreatesARAndAP(t *testing.T) {
	db := testCompanyCurrencyDB(t)
	companyID := seedCompanyForCurrency(t, db)
	seedCurrency(t, db, "USD", "US Dollar")

	// Seed a base AR/AP so the code-finding algorithm has a reference point.
	seedFxAccount(t, db, companyID, "1200", models.RootAsset, models.DetailAccountsReceivable)
	seedFxAccount(t, db, companyID, "2000", models.RootLiability, models.DetailAccountsPayable)

	if err := AddCompanyCurrency(db, companyID, "USD"); err != nil {
		t.Fatalf("AddCompanyCurrency: %v", err)
	}

	// Verify company_currencies row.
	var cc models.CompanyCurrency
	if err := db.Where("company_id = ? AND currency_code = ?", companyID, "USD").First(&cc).Error; err != nil {
		t.Fatalf("company_currencies row not found: %v", err)
	}
	if !cc.IsActive {
		t.Error("expected company_currencies.is_active = true")
	}

	// Verify AR account.
	var ar models.Account
	if err := db.Where("company_id = ? AND system_key = ?", companyID, "ar_USD").First(&ar).Error; err != nil {
		t.Fatalf("AR account not found: %v", err)
	}
	if ar.RootAccountType != models.RootAsset {
		t.Errorf("AR root expected asset, got %s", ar.RootAccountType)
	}
	if ar.DetailAccountType != models.DetailAccountsReceivable {
		t.Errorf("AR detail expected accounts_receivable, got %s", ar.DetailAccountType)
	}
	if ar.CurrencyMode != models.CurrencyModeFixedForeign {
		t.Errorf("AR currency_mode expected fixed_foreign, got %s", ar.CurrencyMode)
	}
	if ar.CurrencyCode == nil || *ar.CurrencyCode != "USD" {
		t.Errorf("AR currency_code expected USD, got %v", ar.CurrencyCode)
	}
	if !ar.IsSystemGenerated {
		t.Error("AR is_system_generated expected true")
	}

	// Verify AP account.
	var ap models.Account
	if err := db.Where("company_id = ? AND system_key = ?", companyID, "ap_USD").First(&ap).Error; err != nil {
		t.Fatalf("AP account not found: %v", err)
	}
	if ap.RootAccountType != models.RootLiability {
		t.Errorf("AP root expected liability, got %s", ap.RootAccountType)
	}
	if ap.DetailAccountType != models.DetailAccountsPayable {
		t.Errorf("AP detail expected accounts_payable, got %s", ap.DetailAccountType)
	}
	if ap.CurrencyMode != models.CurrencyModeFixedForeign {
		t.Errorf("AP currency_mode expected fixed_foreign, got %s", ap.CurrencyMode)
	}
}

// TestAddCompanyCurrency_Idempotent verifies that calling AddCompanyCurrency twice
// does not create duplicate accounts or company_currency rows.
func TestAddCompanyCurrency_Idempotent(t *testing.T) {
	db := testCompanyCurrencyDB(t)
	companyID := seedCompanyForCurrency(t, db)
	seedCurrency(t, db, "USD", "US Dollar")

	for i := 0; i < 2; i++ {
		if err := AddCompanyCurrency(db, companyID, "USD"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}

	var ccCount int64
	db.Model(&models.CompanyCurrency{}).Where("company_id = ? AND currency_code = ?", companyID, "USD").Count(&ccCount)
	if ccCount != 1 {
		t.Errorf("expected 1 company_currency row, got %d", ccCount)
	}

	var arCount, apCount int64
	db.Model(&models.Account{}).Where("company_id = ? AND system_key = ?", companyID, "ar_USD").Count(&arCount)
	db.Model(&models.Account{}).Where("company_id = ? AND system_key = ?", companyID, "ap_USD").Count(&apCount)
	if arCount != 1 {
		t.Errorf("expected 1 AR account, got %d", arCount)
	}
	if apCount != 1 {
		t.Errorf("expected 1 AP account, got %d", apCount)
	}
}

// TestAddCompanyCurrency_RejectsBaseCurrency verifies that the base currency
// cannot be added as a foreign currency.
func TestAddCompanyCurrency_RejectsBaseCurrency(t *testing.T) {
	db := testCompanyCurrencyDB(t)
	companyID := seedCompanyForCurrency(t, db)
	seedCurrency(t, db, "CAD", "Canadian Dollar")

	err := AddCompanyCurrency(db, companyID, "CAD")
	if err == nil {
		t.Fatal("expected error when adding base currency, got nil")
	}
}

// TestAddCompanyCurrency_RejectsUnknownCurrency verifies that an unknown currency
// code is rejected.
func TestAddCompanyCurrency_RejectsUnknownCurrency(t *testing.T) {
	db := testCompanyCurrencyDB(t)
	companyID := seedCompanyForCurrency(t, db)
	// XYZ not seeded in currencies table.

	err := AddCompanyCurrency(db, companyID, "XYZ")
	if err == nil {
		t.Fatal("expected error for unknown currency, got nil")
	}
}

// TestEnableMultiCurrency verifies the flag is set on the company.
func TestEnableMultiCurrency(t *testing.T) {
	db := testCompanyCurrencyDB(t)
	companyID := seedCompanyForCurrency(t, db)

	// Initially false (default).
	var before models.Company
	db.First(&before, companyID)
	if before.MultiCurrencyEnabled {
		t.Error("expected multi_currency_enabled = false initially")
	}

	if err := EnableMultiCurrency(db, companyID); err != nil {
		t.Fatalf("EnableMultiCurrency: %v", err)
	}

	var after models.Company
	db.First(&after, companyID)
	if !after.MultiCurrencyEnabled {
		t.Error("expected multi_currency_enabled = true after enable")
	}
}

// TestFindNextAccountCode_UsesDetailTypeMax verifies that findNextAccountCode
// picks the next slot after the maximum code of the same detail type, not all accounts.
func TestFindNextAccountCode_UsesDetailTypeMax(t *testing.T) {
	db := testCompanyCurrencyDB(t)
	companyID := seedCompanyForCurrency(t, db)

	// Seed AR accounts at 1200 and 1210.
	seedFxAccount(t, db, companyID, "1200", models.RootAsset, models.DetailAccountsReceivable)
	seedFxAccount(t, db, companyID, "1210", models.RootAsset, models.DetailAccountsReceivable)
	// Seed a bank account at 1211 (occupying the first candidate slot after 1210).
	seedFxAccount(t, db, companyID, "1211", models.RootAsset, models.DetailBank)

	code, err := findNextAccountCode(db, companyID, 4, models.RootAsset, models.DetailAccountsReceivable)
	if err != nil {
		t.Fatalf("findNextAccountCode: %v", err)
	}
	// 1211 is taken by a bank account; next free should be 1212.
	if code != "1212" {
		t.Errorf("expected 1212, got %s", code)
	}
}

// ── ValidateCurrencyMode tests (pure logic, no DB) ────────────────────────────

func TestValidateCurrencyMode_BaseOnly_NoCode(t *testing.T) {
	if err := models.ValidateCurrencyMode(models.CurrencyModeBaseOnly, nil); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCurrencyMode_BaseOnly_WithCode_Errors(t *testing.T) {
	code := "USD"
	if err := models.ValidateCurrencyMode(models.CurrencyModeBaseOnly, &code); err == nil {
		t.Error("expected error when base_only has a currency code")
	}
}

func TestValidateCurrencyMode_FixedForeign_ValidCode(t *testing.T) {
	code := "USD"
	if err := models.ValidateCurrencyMode(models.CurrencyModeFixedForeign, &code); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCurrencyMode_FixedForeign_NoCode_Errors(t *testing.T) {
	if err := models.ValidateCurrencyMode(models.CurrencyModeFixedForeign, nil); err == nil {
		t.Error("expected error when fixed_foreign has no currency code")
	}
}

func TestValidateCurrencyMode_FixedForeign_BadFormat_Errors(t *testing.T) {
	cases := []string{"us", "USDD", "123", "us1"}
	for _, c := range cases {
		code := c
		if err := models.ValidateCurrencyMode(models.CurrencyModeFixedForeign, &code); err == nil {
			t.Errorf("expected error for code %q, got nil", c)
		}
	}
}

func TestValidateCurrencyMode_InvalidMode_Errors(t *testing.T) {
	code := "USD"
	if err := models.ValidateCurrencyMode("bogus", &code); err == nil {
		t.Error("expected error for invalid mode")
	}
}
