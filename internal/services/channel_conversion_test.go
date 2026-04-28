// 遵循project_guide.md
package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testConversionDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:conv_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.ItemComponent{},
		&models.SalesChannelAccount{},
		&models.ItemChannelMapping{},
		&models.ChannelOrder{},
		&models.ChannelOrderLine{},
		&models.Invoice{},
		&models.InvoiceLine{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

type convSetup struct {
	companyID  uint
	customerID uint
	acctID     uint
	widgetID   uint
	bundleID   uint
}

func setupConversion(t *testing.T, db *gorm.DB) convSetup {
	t.Helper()
	co := models.Company{Name: "Conv Co", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Customer", AddrStreet1: "1 St"}
	db.Create(&cust)
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev", RootAccountType: models.RootRevenue, DetailAccountType: "revenue", IsActive: true}
	db.Create(&rev)

	widget := models.ProductService{CompanyID: co.ID, Name: "Widget", Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true}
	widget.ApplyTypeDefaults()
	db.Create(&widget)

	bundle := models.ProductService{CompanyID: co.ID, Name: "Kit", Type: models.ProductServiceTypeNonInventory, ItemStructureType: models.ItemStructureBundle, RevenueAccountID: rev.ID, CanBeSold: true, IsActive: true}
	db.Create(&bundle)

	acct := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&acct)

	// Create mappings.
	CreateItemMapping(db, &models.ItemChannelMapping{CompanyID: co.ID, ItemID: widget.ID, ChannelAccountID: acct.ID, ChannelType: models.ChannelTypeAmazon, ExternalSKU: "AMZ-W1", IsActive: true})
	CreateItemMapping(db, &models.ItemChannelMapping{CompanyID: co.ID, ItemID: bundle.ID, ChannelAccountID: acct.ID, ChannelType: models.ChannelTypeAmazon, ExternalSKU: "AMZ-KIT", IsActive: true})

	return convSetup{companyID: co.ID, customerID: cust.ID, acctID: acct.ID, widgetID: widget.ID, bundleID: bundle.ID}
}

func createTestOrder(t *testing.T, db *gorm.DB, s convSetup, skus ...string) uint {
	t.Helper()
	order := models.ChannelOrder{
		CompanyID: s.companyID, ChannelAccountID: s.acctID,
		ExternalOrderID: fmt.Sprintf("ORD-%d", time.Now().UnixNano()),
		OrderStatus: "imported", RawPayload: datatypes.JSON("{}"), ImportedAt: time.Now(),
	}
	var lines []models.ChannelOrderLine
	for _, sku := range skus {
		lines = append(lines, models.ChannelOrderLine{
			ExternalSKU: sku, Quantity: decimal.NewFromInt(2),
			ItemPrice: ptrDec(decimal.NewFromInt(50)), RawPayload: datatypes.JSON("{}"),
		})
	}
	CreateChannelOrderWithLines(db, &order, lines)
	return order.ID
}

func ptrDec(d decimal.Decimal) *decimal.Decimal { return &d }

// ── Small fix tests ──────────────────────────────────────────────────────────

func TestDuplicateMapping_Rejected(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)

	// Try to create a second mapping for the same SKU.
	err := CreateItemMapping(db, &models.ItemChannelMapping{
		CompanyID: s.companyID, ItemID: s.widgetID, ChannelAccountID: s.acctID,
		ChannelType: models.ChannelTypeAmazon, ExternalSKU: "AMZ-W1", IsActive: true,
	})
	if err == nil {
		t.Fatal("Expected duplicate mapping error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Expected 'already exists' error, got: %v", err)
	}
}

func TestResolver_DuplicateHit_NeedsReview(t *testing.T) {
	db := testConversionDB(t)
	co := models.Company{Name: "Dup Co", IsActive: true}
	db.Create(&co)
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev", RootAccountType: models.RootRevenue, DetailAccountType: "revenue", IsActive: true}
	db.Create(&rev)
	item1 := models.ProductService{CompanyID: co.ID, Name: "A", Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true}
	item1.ApplyTypeDefaults()
	db.Create(&item1)
	item2 := models.ProductService{CompanyID: co.ID, Name: "B", Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true}
	item2.ApplyTypeDefaults()
	db.Create(&item2)
	acct := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeManualImport, DisplayName: "M", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&acct)

	// Force-create two active mappings for the same SKU (bypassing uniqueness check).
	db.Create(&models.ItemChannelMapping{CompanyID: co.ID, ItemID: item1.ID, ChannelAccountID: acct.ID, ChannelType: models.ChannelTypeManualImport, ExternalSKU: "DUP-SKU", IsActive: true})
	db.Create(&models.ItemChannelMapping{CompanyID: co.ID, ItemID: item2.ID, ChannelAccountID: acct.ID, ChannelType: models.ChannelTypeManualImport, ExternalSKU: "DUP-SKU", IsActive: true})

	result, err := ResolveMappedItem(db, co.ID, acct.ID, "", "DUP-SKU")
	if err != nil {
		t.Fatal(err)
	}
	if result.MappingStatus != models.MappingStatusNeedsReview {
		t.Errorf("Expected needs_review, got %s", result.MappingStatus)
	}
}

// ── Conversion eligibility tests ─────────────────────────────────────────────

func TestConvertibility_AllMapped_OK(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)
	orderID := createTestOrder(t, db, s, "AMZ-W1", "AMZ-KIT")

	err := ValidateChannelOrderConvertible(db, s.companyID, orderID)
	if err != nil {
		t.Fatalf("Expected convertible, got: %v", err)
	}
}

func TestConvertibility_UnmappedLine_Blocked(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)
	orderID := createTestOrder(t, db, s, "AMZ-W1", "UNKNOWN-SKU")

	err := ValidateChannelOrderConvertible(db, s.companyID, orderID)
	if err == nil {
		t.Fatal("Expected non-convertible error for unmapped line")
	}
}

func TestConvertibility_AlreadyConverted_Blocked(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)
	orderID := createTestOrder(t, db, s, "AMZ-W1")

	// Convert once.
	ConvertChannelOrderToDraftInvoice(db, ConvertOptions{
		CompanyID: s.companyID, ChannelOrderID: orderID,
		CustomerID: s.customerID, InvoiceNumber: "INV-CONV-1", InvoiceDate: time.Now(),
	})

	// Try again.
	err := ValidateChannelOrderConvertible(db, s.companyID, orderID)
	if err == nil {
		t.Fatal("Expected already-converted error")
	}
}

// ── Conversion tests ─────────────────────────────────────────────────────────

func TestConvert_MappedExact_CreatesDraftInvoice(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)
	orderID := createTestOrder(t, db, s, "AMZ-W1")

	result, err := ConvertChannelOrderToDraftInvoice(db, ConvertOptions{
		CompanyID: s.companyID, ChannelOrderID: orderID,
		CustomerID: s.customerID, InvoiceNumber: "INV-CONV-2", InvoiceDate: time.Now(),
	})
	if err != nil {
		t.Fatalf("Conversion failed: %v", err)
	}
	if result.InvoiceID == 0 {
		t.Fatal("Invoice not created")
	}
	if result.LineCount != 1 {
		t.Errorf("Expected 1 line, got %d", result.LineCount)
	}

	// Verify invoice is draft.
	var inv models.Invoice
	db.First(&inv, result.InvoiceID)
	if inv.Status != models.InvoiceStatusDraft {
		t.Errorf("Expected draft, got %s", inv.Status)
	}
	if inv.CustomerID != s.customerID {
		t.Error("Wrong customer")
	}

	// Verify order is marked converted.
	convInvID := GetConvertedInvoiceID(db, s.companyID, orderID)
	if convInvID == nil || *convInvID != result.InvoiceID {
		t.Error("Order not marked as converted")
	}
}

func TestConvert_MappedBundle_CreatesDraftWithBundleLine(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)
	orderID := createTestOrder(t, db, s, "AMZ-KIT")

	result, err := ConvertChannelOrderToDraftInvoice(db, ConvertOptions{
		CompanyID: s.companyID, ChannelOrderID: orderID,
		CustomerID: s.customerID, InvoiceNumber: "INV-CONV-BUNDLE", InvoiceDate: time.Now(),
	})
	if err != nil {
		t.Fatalf("Conversion failed: %v", err)
	}

	// Verify invoice line references the bundle item.
	var lines []models.InvoiceLine
	db.Where("invoice_id = ?", result.InvoiceID).Find(&lines)
	if len(lines) != 1 {
		t.Fatalf("Expected 1 line, got %d", len(lines))
	}
	if lines[0].ProductServiceID == nil || *lines[0].ProductServiceID != s.bundleID {
		t.Error("Invoice line should reference bundle item")
	}
}

func TestConvert_CrossCompany_Blocked(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)
	orderID := createTestOrder(t, db, s, "AMZ-W1")

	otherCo := models.Company{Name: "Other Co", IsActive: true}
	db.Create(&otherCo)

	err := ValidateChannelOrderConvertible(db, otherCo.ID, orderID)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}

func TestConvert_DoubleConvert_Blocked(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)
	orderID := createTestOrder(t, db, s, "AMZ-W1")

	ConvertChannelOrderToDraftInvoice(db, ConvertOptions{
		CompanyID: s.companyID, ChannelOrderID: orderID,
		CustomerID: s.customerID, InvoiceNumber: "INV-DBL-1", InvoiceDate: time.Now(),
	})

	_, err := ConvertChannelOrderToDraftInvoice(db, ConvertOptions{
		CompanyID: s.companyID, ChannelOrderID: orderID,
		CustomerID: s.customerID, InvoiceNumber: "INV-DBL-2", InvoiceDate: time.Now(),
	})
	if err == nil {
		t.Fatal("Expected double-convert error")
	}
}

// ── Tax scope strip tests ─────────────────────────────────────────────────────

// TestConvert_PurchaseOnlyTax_StrippedFromLine verifies that when an item's
// DefaultTaxCodeID points to a purchase-only tax code, the conversion strips it:
// the resulting invoice line has TaxCodeID=nil and LineTax=0.
func TestConvert_PurchaseOnlyTax_StrippedFromLine(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)

	// Create a purchase-only tax code.
	taxAcct := models.Account{
		CompanyID: s.companyID, Code: "2310", Name: "Tax Payable",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailSalesTaxPayable, IsActive: true,
	}
	db.Create(&taxAcct)
	purTax := models.TaxCode{
		CompanyID:         s.companyID,
		Name:              "GST Purchase Only",
		Code:              "GST-P",
		TaxType:           "taxable",
		Rate:              decimal.NewFromFloat(0.05),
		Scope:             models.TaxScopePurchase,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      decimal.Zero,
		SalesTaxAccountID: taxAcct.ID,
		IsActive:          true,
	}
	db.Create(&purTax)

	// Assign purchase-only tax as default on the widget item.
	db.Model(&models.ProductService{}).Where("id = ?", s.widgetID).
		Update("default_tax_code_id", purTax.ID)

	orderID := createTestOrder(t, db, s, "AMZ-W1")
	result, err := ConvertChannelOrderToDraftInvoice(db, ConvertOptions{
		CompanyID: s.companyID, ChannelOrderID: orderID,
		CustomerID: s.customerID, InvoiceNumber: "INV-TAX-PUR", InvoiceDate: time.Now(),
	})
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}

	var lines []models.InvoiceLine
	db.Where("invoice_id = ?", result.InvoiceID).Find(&lines)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0].TaxCodeID != nil {
		t.Errorf("TaxCodeID should be nil (stripped), got %v", *lines[0].TaxCodeID)
	}
	if !lines[0].LineTax.IsZero() {
		t.Errorf("LineTax should be 0 (stripped), got %s", lines[0].LineTax.String())
	}
}

// TestConvert_InactiveTax_StrippedFromLine verifies that when an item's
// DefaultTaxCodeID points to an inactive tax code, the conversion strips it.
func TestConvert_InactiveTax_StrippedFromLine(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)

	taxAcct := models.Account{
		CompanyID: s.companyID, Code: "2311", Name: "Tax Payable 2",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailSalesTaxPayable, IsActive: true,
	}
	db.Create(&taxAcct)
	inactiveTax := models.TaxCode{
		CompanyID:         s.companyID,
		Name:              "Old GST",
		Code:              "GST-OLD",
		TaxType:           "taxable",
		Rate:              decimal.NewFromFloat(0.05),
		Scope:             models.TaxScopeSales,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      decimal.Zero,
		SalesTaxAccountID: taxAcct.ID,
		IsActive:          true,
	}
	db.Create(&inactiveTax)
	db.Model(&inactiveTax).Update("is_active", false)

	db.Model(&models.ProductService{}).Where("id = ?", s.widgetID).
		Update("default_tax_code_id", inactiveTax.ID)

	orderID := createTestOrder(t, db, s, "AMZ-W1")
	result, err := ConvertChannelOrderToDraftInvoice(db, ConvertOptions{
		CompanyID: s.companyID, ChannelOrderID: orderID,
		CustomerID: s.customerID, InvoiceNumber: "INV-TAX-INACT", InvoiceDate: time.Now(),
	})
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}

	var lines []models.InvoiceLine
	db.Where("invoice_id = ?", result.InvoiceID).Find(&lines)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0].TaxCodeID != nil {
		t.Errorf("TaxCodeID should be nil (stripped), got %v", *lines[0].TaxCodeID)
	}
	if !lines[0].LineTax.IsZero() {
		t.Errorf("LineTax should be 0 (stripped), got %s", lines[0].LineTax.String())
	}
}

// TestConvert_ValidSalesTax_CarriedThrough verifies that a valid sales-scoped
// active tax code on an item is properly applied to the converted invoice line.
func TestConvert_ValidSalesTax_CarriedThrough(t *testing.T) {
	db := testConversionDB(t)
	s := setupConversion(t, db)

	taxAcct := models.Account{
		CompanyID: s.companyID, Code: "2312", Name: "Tax Payable 3",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailSalesTaxPayable, IsActive: true,
	}
	db.Create(&taxAcct)
	salesTax := models.TaxCode{
		CompanyID:         s.companyID,
		Name:              "GST Sales",
		Code:              "GST-S",
		TaxType:           "taxable",
		Rate:              decimal.NewFromFloat(0.05),
		Scope:             models.TaxScopeSales,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      decimal.Zero,
		SalesTaxAccountID: taxAcct.ID,
		IsActive:          true,
	}
	db.Create(&salesTax)

	db.Model(&models.ProductService{}).Where("id = ?", s.widgetID).
		Update("default_tax_code_id", salesTax.ID)

	orderID := createTestOrder(t, db, s, "AMZ-W1")
	result, err := ConvertChannelOrderToDraftInvoice(db, ConvertOptions{
		CompanyID: s.companyID, ChannelOrderID: orderID,
		CustomerID: s.customerID, InvoiceNumber: "INV-TAX-SALES", InvoiceDate: time.Now(),
	})
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}

	var lines []models.InvoiceLine
	db.Where("invoice_id = ?", result.InvoiceID).Find(&lines)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0].TaxCodeID == nil {
		t.Error("TaxCodeID should be set for valid sales tax code")
	}
	if lines[0].LineTax.IsZero() {
		t.Error("LineTax should be non-zero for valid sales tax code")
	}
	// 2 units × $50 × 5% = $5.00
	expected := decimal.NewFromFloat(5.00)
	if !lines[0].LineTax.Equal(expected) {
		t.Errorf("LineTax: want %s, got %s", expected, lines[0].LineTax)
	}
}
