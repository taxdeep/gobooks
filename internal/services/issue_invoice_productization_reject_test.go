// 遵循project_guide.md
package services

// issue_invoice_productization_reject_test.go — IssueInvoice productization preflight tests.
//
// Verifies that free-form line and inactive product rejections happen BEFORE
// any snapshot / IssuedAt / JE is written — no half-success state.
//
// Coverage:
//   TestIssueInvoice_FreeFormLine_NoMetadataWritten         — nil ProductServiceID → draft, no IssuedAt
//   TestIssueInvoice_InactiveProduct_NoMetadataWritten      — inactive product → draft, no IssuedAt
//   TestIssueInvoice_MixedLines_FreeFormCausesCleanReject   — valid + free-form → all metadata absent
//   TestIssueInvoice_MixedLines_InactiveProductCleanReject  — valid + inactive product → all metadata absent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func testProdRejectDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:prod_reject_%s?mode=memory&cache=shared", t.Name())
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
		&models.ItemComponent{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.PaymentReceipt{},
		&models.SettlementAllocation{},
		&models.AuditLog{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.PaymentTransaction{},
		&models.TaskInvoiceSource{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedProdRejectFixture seeds company + customer + AR account + revenue account.
// Returns (companyID, customerID, arAccountID, revAccountID).
func seedProdRejectFixture(t *testing.T, db *gorm.DB) (companyID, customerID, arAccountID, revAccountID uint) {
	t.Helper()
	co := models.Company{Name: "ProdReject Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	cust := models.Customer{CompanyID: co.ID, Name: "Test Customer"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	ar := models.Account{
		CompanyID: co.ID, Code: "1100", Name: "AR",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, IsActive: true,
	}
	if err := db.Create(&ar).Error; err != nil {
		t.Fatal(err)
	}
	rev := models.Account{
		CompanyID: co.ID, Code: "4100", Name: "Revenue",
		RootAccountType: models.RootRevenue, DetailAccountType: models.DetailServiceRevenue, IsActive: true,
	}
	if err := db.Create(&rev).Error; err != nil {
		t.Fatal(err)
	}
	return co.ID, cust.ID, ar.ID, rev.ID
}

// seedProdRejectProduct creates an active product linked to revAccountID.
func seedProdRejectProduct(t *testing.T, db *gorm.DB, companyID, revAccountID uint) uint {
	t.Helper()
	ps := models.ProductService{
		CompanyID: companyID, Name: "Service", Type: "service",
		RevenueAccountID: revAccountID, IsActive: true,
	}
	if err := db.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}
	return ps.ID
}

// seedProdRejectInvoice creates a draft invoice with the given lines pre-seeded.
// Caller provides lines (InvoiceID not needed — set here).
func seedProdRejectInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint, lines []models.InvoiceLine) uint {
	t.Helper()
	var subtotal, taxTotal decimal.Decimal
	for _, l := range lines {
		subtotal = subtotal.Add(l.LineNet)
		taxTotal = taxTotal.Add(l.LineTax)
	}
	amount := subtotal.Add(taxTotal)

	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-PR-%d", time.Now().UnixNano()%99999),
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Original Snapshot",
		CustomerEmailSnapshot: "orig@example.com",
		InvoiceDate:           time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Subtotal:              subtotal,
		TaxTotal:              taxTotal,
		Amount:                amount,
		BalanceDue:            amount,
		BalanceDueBase:        amount,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	for i := range lines {
		lines[i].InvoiceID = inv.ID
		lines[i].CompanyID = companyID
		if lines[i].SortOrder == 0 {
			lines[i].SortOrder = uint(i + 1)
		}
		if err := db.Create(&lines[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return inv.ID
}

// assertNoMetadata checks all the "no half-success" invariants after a rejected IssueInvoice.
func assertNoMetadata(t *testing.T, db *gorm.DB, companyID, invoiceID uint, originalSnapshot string) {
	t.Helper()
	var inv models.Invoice
	db.First(&inv, invoiceID)

	if inv.Status != models.InvoiceStatusDraft {
		t.Errorf("invoice status should remain draft, got %s", inv.Status)
	}
	if inv.IssuedAt != nil {
		t.Errorf("IssuedAt must be nil after rejection, got %v", inv.IssuedAt)
	}
	if inv.JournalEntryID != nil {
		t.Errorf("JournalEntryID must be nil after rejection, got %v", inv.JournalEntryID)
	}
	if inv.CustomerNameSnapshot != originalSnapshot {
		t.Errorf("CustomerNameSnapshot changed after rejection: want %q, got %q",
			originalSnapshot, inv.CustomerNameSnapshot)
	}

	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, models.LedgerSourceInvoice, invoiceID).
		Count(&jeCount)
	if jeCount != 0 {
		t.Errorf("expected 0 JEs after rejection, got %d", jeCount)
	}

	var jlCount int64
	db.Model(&models.JournalLine{}).Where("company_id = ?", companyID).Count(&jlCount)
	if jlCount != 0 {
		t.Errorf("expected 0 journal lines after rejection, got %d", jlCount)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestIssueInvoice_FreeFormLine_NoMetadataWritten verifies that a draft invoice
// with a free-form line (nil ProductServiceID) is rejected by IssueInvoice
// BEFORE any snapshot, IssuedAt, or JE is written.
func TestIssueInvoice_FreeFormLine_NoMetadataWritten(t *testing.T) {
	db := testProdRejectDB(t)
	companyID, customerID, _, _ := seedProdRejectFixture(t, db)

	net := decimal.RequireFromString("300.00")
	invoiceID := seedProdRejectInvoice(t, db, companyID, customerID, []models.InvoiceLine{
		{
			// No ProductServiceID — free-form line.
			Description: "Ad-hoc service",
			Qty:         decimal.RequireFromString("1"),
			UnitPrice:   net,
			LineNet:     net,
			LineTax:     decimal.Zero,
			LineTotal:   net,
		},
	})

	_, err := IssueInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("expected IssueInvoice to fail for free-form line, got nil")
	}
	if !strings.Contains(err.Error(), "no product/service") {
		t.Fatalf("expected 'no product/service' in error, got: %v", err)
	}

	assertNoMetadata(t, db, companyID, invoiceID, "Original Snapshot")
}

// TestIssueInvoice_InactiveProduct_NoMetadataWritten verifies that a draft invoice
// whose line references an inactive ProductService is rejected BEFORE any metadata
// write.
func TestIssueInvoice_InactiveProduct_NoMetadataWritten(t *testing.T) {
	db := testProdRejectDB(t)
	companyID, customerID, _, revAccountID := seedProdRejectFixture(t, db)
	productID := seedProdRejectProduct(t, db, companyID, revAccountID)

	net := decimal.RequireFromString("400.00")
	invoiceID := seedProdRejectInvoice(t, db, companyID, customerID, []models.InvoiceLine{
		{
			ProductServiceID: &productID,
			Description:      "Service",
			Qty:              decimal.RequireFromString("1"),
			UnitPrice:        net,
			LineNet:          net,
			LineTax:          decimal.Zero,
			LineTotal:        net,
		},
	})

	// Deactivate the product after the draft was saved.
	if err := db.Model(&models.ProductService{}).Where("id = ?", productID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	_, err := IssueInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("expected IssueInvoice to fail for inactive product, got nil")
	}
	if !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("expected 'inactive' in error, got: %v", err)
	}

	assertNoMetadata(t, db, companyID, invoiceID, "Original Snapshot")
}

// TestIssueInvoice_MixedLines_FreeFormCausesCleanReject verifies that a mix of
// a valid line and a free-form line results in clean rejection — no metadata
// is written even though one line would pass.
func TestIssueInvoice_MixedLines_FreeFormCausesCleanReject(t *testing.T) {
	db := testProdRejectDB(t)
	companyID, customerID, _, revAccountID := seedProdRejectFixture(t, db)
	productID := seedProdRejectProduct(t, db, companyID, revAccountID)

	net1 := decimal.RequireFromString("200.00")
	net2 := decimal.RequireFromString("100.00")
	invoiceID := seedProdRejectInvoice(t, db, companyID, customerID, []models.InvoiceLine{
		{
			// Valid product line.
			ProductServiceID: &productID,
			Description:      "Consulting",
			Qty:              decimal.RequireFromString("1"),
			UnitPrice:        net1,
			LineNet:          net1,
			LineTax:          decimal.Zero,
			LineTotal:        net1,
			SortOrder:        1,
		},
		{
			// Free-form line — no product.
			Description: "Ad-hoc item",
			Qty:         decimal.RequireFromString("1"),
			UnitPrice:   net2,
			LineNet:     net2,
			LineTax:     decimal.Zero,
			LineTotal:   net2,
			SortOrder:   2,
		},
	})

	_, err := IssueInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("expected IssueInvoice to fail for free-form line, got nil")
	}
	if !strings.Contains(err.Error(), "no product/service") {
		t.Fatalf("expected 'no product/service' in error, got: %v", err)
	}

	assertNoMetadata(t, db, companyID, invoiceID, "Original Snapshot")
}

// TestIssueInvoice_MixedLines_InactiveProductCleanReject verifies that a mix of
// a valid line and an inactive product line results in clean rejection with no
// half-success metadata.
func TestIssueInvoice_MixedLines_InactiveProductCleanReject(t *testing.T) {
	db := testProdRejectDB(t)
	companyID, customerID, _, revAccountID := seedProdRejectFixture(t, db)

	activeProductID := seedProdRejectProduct(t, db, companyID, revAccountID)

	// Second product that will be deactivated.
	ps2 := models.ProductService{
		CompanyID: companyID, Name: "Old Service", Type: "service",
		RevenueAccountID: revAccountID, IsActive: true,
	}
	if err := db.Create(&ps2).Error; err != nil {
		t.Fatal(err)
	}
	inactiveProductID := ps2.ID

	net1 := decimal.RequireFromString("500.00")
	net2 := decimal.RequireFromString("250.00")
	invoiceID := seedProdRejectInvoice(t, db, companyID, customerID, []models.InvoiceLine{
		{
			ProductServiceID: &activeProductID,
			Description:      "Active service",
			Qty:              decimal.RequireFromString("1"),
			UnitPrice:        net1,
			LineNet:          net1,
			LineTax:          decimal.Zero,
			LineTotal:        net1,
			SortOrder:        1,
		},
		{
			ProductServiceID: &inactiveProductID,
			Description:      "Discontinued service",
			Qty:              decimal.RequireFromString("1"),
			UnitPrice:        net2,
			LineNet:          net2,
			LineTax:          decimal.Zero,
			LineTotal:        net2,
			SortOrder:        2,
		},
	})

	// Deactivate the second product.
	if err := db.Model(&models.ProductService{}).Where("id = ?", inactiveProductID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	_, err := IssueInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("expected IssueInvoice to fail for inactive product, got nil")
	}
	if !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("expected 'inactive' in error, got: %v", err)
	}

	assertNoMetadata(t, db, companyID, invoiceID, "Original Snapshot")
}
