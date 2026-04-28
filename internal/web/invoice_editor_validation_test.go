package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func testInvoiceEditorValidationDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_invoice_validation_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedValidationCompany(t *testing.T, db *gorm.DB, name string) uint {
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

func seedValidationCustomer(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()

	row := models.Customer{CompanyID: companyID, Name: name}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func seedValidationAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
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

func seedValidationTaxCode(t *testing.T, db *gorm.DB, companyID, liabilityAccountID uint, name string) uint {
	t.Helper()

	row := models.TaxCode{
		CompanyID:         companyID,
		Name:              name,
		Code:              name,
		TaxType:           "taxable",
		Rate:              mustDecimal(t, "0.05"),
		Scope:             models.TaxScopeBoth,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      mustDecimal(t, "0"),
		SalesTaxAccountID: liabilityAccountID,
		IsActive:          true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func seedValidationProduct(t *testing.T, db *gorm.DB, companyID, revenueAccountID uint, name string) uint {
	t.Helper()

	row := models.ProductService{
		CompanyID:        companyID,
		Name:             name,
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revenueAccountID,
		IsActive:         true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func mustDecimal(t *testing.T, raw string) decimal.Decimal {
	t.Helper()

	v, err := decimal.NewFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestValidateInvoiceDraftReferencesRejectsCrossCompanyReferences(t *testing.T) {
	db := testInvoiceEditorValidationDB(t)
	server := &Server{DB: db}

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	customerA := seedValidationCustomer(t, db, companyA, "Customer A")
	customerB := seedValidationCustomer(t, db, companyB, "Customer B")
	revenueA := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	revenueB := seedValidationAccount(t, db, companyB, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityA := seedValidationAccount(t, db, companyA, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	liabilityB := seedValidationAccount(t, db, companyB, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	productB := seedValidationProduct(t, db, companyB, revenueB, "Beta Service")
	taxCodeB := seedValidationTaxCode(t, db, companyB, liabilityB, "GST-B")
	_ = seedValidationProduct(t, db, companyA, revenueA, "Acme Service")
	_ = seedValidationTaxCode(t, db, companyA, liabilityA, "GST-A")

	productLine := []parsedInvoiceLine{{ProductServiceID: &productB}}
	if err := server.validateInvoiceDraftReferences(companyA, customerA, productLine); err == nil || !strings.Contains(err.Error(), "invalid product/service") {
		t.Fatalf("expected product/service validation error, got %v", err)
	}

	taxLine := []parsedInvoiceLine{{TaxCodeID: &taxCodeB}}
	if err := server.validateInvoiceDraftReferences(companyA, customerA, taxLine); err == nil || !strings.Contains(err.Error(), "invalid tax code") {
		t.Fatalf("expected tax code validation error, got %v", err)
	}

	if err := server.validateInvoiceDraftReferences(companyA, customerB, nil); err == nil || !strings.Contains(err.Error(), "customer is not valid") {
		t.Fatalf("expected customer validation error, got %v", err)
	}
}
