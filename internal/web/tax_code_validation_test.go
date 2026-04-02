package web

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/pages"
)

func testTaxCodeValidationDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_tax_validation_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Company{}, &models.Account{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedTaxValidationCompany(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()

	company := models.Company{
		Name:                    name,
		EntityType:              models.EntityTypeIncorporated,
		BusinessType:            models.BusinessTypeRetail,
		Industry:                models.IndustryRetail,
		IncorporatedDate:        "2024-01-01",
		FiscalYearEnd:           "12-31",
		BusinessNumber:          "123456789",
		AddressLine:             "123 Main",
		City:                    "Vancouver",
		Province:                "BC",
		PostalCode:              "V6B1A1",
		Country:                 "CA",
		AccountCodeLength:       4,
		AccountCodeLengthLocked: true,
		IsActive:                true,
	}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	return company.ID
}

func seedTaxValidationAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
	t.Helper()

	row := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              code,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func TestValidateTaxCodeFormRejectsCrossCompanyAndWrongTypeAccounts(t *testing.T) {
	db := testTaxCodeValidationDB(t)
	companyA := seedTaxValidationCompany(t, db, "Acme")
	companyB := seedTaxValidationCompany(t, db, "Beta")

	salesLiabilityA := seedTaxValidationAccount(t, db, companyA, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	purchaseLiabilityA := seedTaxValidationAccount(t, db, companyA, "2200", models.RootLiability, models.DetailSalesTaxPayable)
	assetA := seedTaxValidationAccount(t, db, companyA, "1100", models.RootAsset, models.DetailOtherCurrentAsset)
	liabilityB := seedTaxValidationAccount(t, db, companyB, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	revenueA := seedTaxValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)

	vm := pages.SalesTaxVM{}
	_, _, _, valid := validateTaxCodeForm(db, companyA, &vm, "GST", "5", "none", "", fmt.Sprint(liabilityB), "")
	if valid {
		t.Fatal("expected cross-company sales account to be rejected")
	}
	if vm.SalesTaxAccountIDError == "" {
		t.Fatal("expected sales account error")
	}

	vm = pages.SalesTaxVM{}
	_, _, _, valid = validateTaxCodeForm(db, companyA, &vm, "GST", "5", "none", "", fmt.Sprint(revenueA), "")
	if valid {
		t.Fatal("expected wrong-type sales account to be rejected")
	}
	if vm.SalesTaxAccountIDError == "" {
		t.Fatal("expected sales account type error")
	}

	vm = pages.SalesTaxVM{}
	_, _, _, valid = validateTaxCodeForm(db, companyA, &vm, "GST", "5", "none", "", fmt.Sprint(salesLiabilityA), fmt.Sprint(liabilityB))
	if valid {
		t.Fatal("expected cross-company purchase recoverable account to be rejected")
	}
	if vm.PurchaseRecoverableAccountIDError == "" {
		t.Fatal("expected purchase account error")
	}

	// Asset account must be rejected for purchase recoverable (requires liability).
	vm = pages.SalesTaxVM{}
	_, _, _, valid = validateTaxCodeForm(db, companyA, &vm, "GST", "5", "none", "", fmt.Sprint(salesLiabilityA), fmt.Sprint(assetA))
	if valid {
		t.Fatal("expected asset account to be rejected for purchase recoverable")
	}
	if vm.PurchaseRecoverableAccountIDError == "" {
		t.Fatal("expected purchase account type error")
	}

	// Liability account must pass.
	vm = pages.SalesTaxVM{}
	_, _, _, valid = validateTaxCodeForm(db, companyA, &vm, "GST", "5", "none", "", fmt.Sprint(salesLiabilityA), fmt.Sprint(purchaseLiabilityA))
	if !valid {
		t.Fatalf("expected valid liability accounts to pass, got sales=%q purchase=%q", vm.SalesTaxAccountIDError, vm.PurchaseRecoverableAccountIDError)
	}
}

func TestValidateTaxCodeFormParsesRateAndIDs(t *testing.T) {
	db := testTaxCodeValidationDB(t)
	companyID := seedTaxValidationCompany(t, db, "Acme")
	salesLiability := seedTaxValidationAccount(t, db, companyID, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	purchaseLiability := seedTaxValidationAccount(t, db, companyID, "2200", models.RootLiability, models.DetailSalesTaxPayable)

	vm := pages.SalesTaxVM{}
	rate, salesAcctID, purchaseAcctID, valid := validateTaxCodeForm(db, companyID, &vm, "GST", "5", "partial", "50", fmt.Sprint(salesLiability), fmt.Sprint(purchaseLiability))
	if !valid {
		t.Fatalf("expected valid form, got %+v", vm)
	}
	if !rate.Equal(decimal.RequireFromString("0.05")) {
		t.Fatalf("unexpected rate: %s", rate)
	}
	if salesAcctID != salesLiability {
		t.Fatalf("unexpected sales account id: %d", salesAcctID)
	}
	if purchaseAcctID == nil || *purchaseAcctID != purchaseLiability {
		t.Fatalf("unexpected purchase account id: %v", purchaseAcctID)
	}
}
