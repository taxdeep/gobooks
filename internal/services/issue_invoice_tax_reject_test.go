// 遵循project_guide.md
package services

// issue_invoice_tax_reject_test.go — Batch 3.6 IssueInvoice pre-flight tax tests.
//
// Verifies that tax rejections (inactive, purchase-only) happen BEFORE any
// snapshot / IssuedAt / JE is written — no half-success state.
//
// Coverage:
//   TestIssueInvoice_InactiveTaxCode_NoMetadataWritten    — inactive tax → draft, no IssuedAt
//   TestIssueInvoice_PurchaseOnlyTaxCode_NoMetadataWritten — purchase-only tax → draft, no IssuedAt
//   TestIssueInvoice_RejectPath_NoJE_NoSnapshot           — reject leaves no JE, no snapshot

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

func testIssueRejectDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:issue_reject_%s?mode=memory&cache=shared", t.Name())
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

func seedIssueRejectCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{Name: "Issue Reject Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedIssueRejectCustomer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedIssueRejectAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
	t.Helper()
	a := models.Account{
		CompanyID: companyID, Code: code, Name: code,
		RootAccountType: root, DetailAccountType: detail, IsActive: true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// seedIssueRejectTaxCode creates a TaxCode with the given scope.
func seedIssueRejectTaxCode(t *testing.T, db *gorm.DB, companyID, salesAcctID uint, scope models.TaxScope) uint {
	t.Helper()
	tc := models.TaxCode{
		CompanyID:         companyID,
		Name:              fmt.Sprintf("Tax-%s", scope),
		Code:              fmt.Sprintf("T-%s", scope),
		TaxType:           "taxable",
		Rate:              decimal.RequireFromString("0.05"),
		Scope:             scope,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      decimal.Zero,
		SalesTaxAccountID: salesAcctID,
		IsActive:          true,
	}
	if err := db.Create(&tc).Error; err != nil {
		t.Fatal(err)
	}
	return tc.ID
}

// seedIssueRejectInvoice creates a draft invoice with one taxed line using the given taxCodeID.
// net + lineTax = Amount/BalanceDue.
func seedIssueRejectInvoice(t *testing.T, db *gorm.DB, companyID, customerID, productID, taxCodeID uint) uint {
	t.Helper()
	net := decimal.RequireFromString("500.00")
	tax := decimal.RequireFromString("25.00")
	total := net.Add(tax)

	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-IR-%d", time.Now().UnixNano()%99999),
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Test Customer",
		CustomerEmailSnapshot: "test@example.com",
		InvoiceDate:           time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Subtotal:              net,
		TaxTotal:              tax,
		Amount:                total,
		BalanceDue:            total,
		BalanceDueBase:        total,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	line := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: &productID,
		TaxCodeID:        &taxCodeID,
		Description:      "Taxed Service",
		Qty:              decimal.RequireFromString("1"),
		UnitPrice:        net,
		LineNet:          net,
		LineTax:          tax,
		LineTotal:        total,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return inv.ID
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestIssueInvoice_InactiveTaxCode_NoMetadataWritten verifies that when a line
// references an inactive tax code, IssueInvoice returns an error AND does not
// write IssuedAt, CustomerNameSnapshot changes, or any JE to the database.
// This is the P1-A test: pre-flight catch before snapshot writes.
func TestIssueInvoice_InactiveTaxCode_NoMetadataWritten(t *testing.T) {
	db := testIssueRejectDB(t)
	companyID := seedIssueRejectCompany(t, db)
	customerID := seedIssueRejectCustomer(t, db, companyID)

	seedIssueRejectAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedIssueRejectAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedIssueRejectAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)

	ps := models.ProductService{
		CompanyID: companyID, Name: "Service", Type: "service",
		RevenueAccountID: revID, IsActive: true,
	}
	if err := db.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}

	taxCodeID := seedIssueRejectTaxCode(t, db, companyID, taxAcctID, models.TaxScopeSales)
	invoiceID := seedIssueRejectInvoice(t, db, companyID, customerID, ps.ID, taxCodeID)

	// Deactivate the tax code AFTER draft was saved.
	if err := db.Model(&models.TaxCode{}).Where("id = ?", taxCodeID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	// Snapshot the invoice state before the rejected IssueInvoice call.
	var before models.Invoice
	db.First(&before, invoiceID)

	_, err := IssueInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("expected IssueInvoice to fail for inactive tax code, got nil")
	}
	if !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("expected 'inactive' in error, got: %v", err)
	}

	// Invoice must still be draft — no status change.
	var after models.Invoice
	db.First(&after, invoiceID)

	if after.Status != models.InvoiceStatusDraft {
		t.Fatalf("invoice status should remain draft, got %s", after.Status)
	}
	// IssuedAt must not have been written.
	if after.IssuedAt != nil {
		t.Fatalf("IssuedAt must be nil after rejection, got %v", after.IssuedAt)
	}
	// JournalEntryID must not have been set.
	if after.JournalEntryID != nil {
		t.Fatalf("JournalEntryID must be nil after rejection, got %v", after.JournalEntryID)
	}
	// CustomerNameSnapshot must match the pre-rejection snapshot exactly — IssueInvoice
	// must not have overwritten it during its (now aborted) metadata phase.
	if after.CustomerNameSnapshot != before.CustomerNameSnapshot {
		t.Fatalf("CustomerNameSnapshot changed after rejection: want %q, got %q",
			before.CustomerNameSnapshot, after.CustomerNameSnapshot)
	}

	// No JE must exist for this invoice.
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, models.LedgerSourceInvoice, invoiceID).
		Count(&jeCount)
	if jeCount != 0 {
		t.Fatalf("expected 0 JEs after rejection, got %d", jeCount)
	}
}

// TestIssueInvoice_PurchaseOnlyTaxCode_NoMetadataWritten verifies that when a
// line references a purchase-only tax code, IssueInvoice rejects cleanly
// without persisting any issued-state metadata.
func TestIssueInvoice_PurchaseOnlyTaxCode_NoMetadataWritten(t *testing.T) {
	db := testIssueRejectDB(t)
	companyID := seedIssueRejectCompany(t, db)
	customerID := seedIssueRejectCustomer(t, db, companyID)

	seedIssueRejectAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedIssueRejectAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedIssueRejectAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)

	ps := models.ProductService{
		CompanyID: companyID, Name: "Service", Type: "service",
		RevenueAccountID: revID, IsActive: true,
	}
	if err := db.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}

	// Create a purchase-only tax code (simulates legacy data that bypassed the web guard).
	purTaxID := seedIssueRejectTaxCode(t, db, companyID, taxAcctID, models.TaxScopePurchase)
	invoiceID := seedIssueRejectInvoice(t, db, companyID, customerID, ps.ID, purTaxID)

	_, err := IssueInvoice(db, companyID, invoiceID)
	if err == nil {
		t.Fatal("expected IssueInvoice to fail for purchase-only tax code, got nil")
	}
	if !strings.Contains(err.Error(), "purchases only") {
		t.Fatalf("expected 'purchases only' in error, got: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invoiceID)

	if inv.Status != models.InvoiceStatusDraft {
		t.Fatalf("invoice status should remain draft, got %s", inv.Status)
	}
	if inv.IssuedAt != nil {
		t.Fatalf("IssuedAt must be nil after rejection, got %v", inv.IssuedAt)
	}
	if inv.JournalEntryID != nil {
		t.Fatalf("JournalEntryID must be nil after rejection, got %v", inv.JournalEntryID)
	}
}

// TestIssueInvoice_RejectPath_NoJE_NoSnapshot is a combined integrity check:
// inactive tax → IssueInvoice fails → zero JEs created, zero journal lines,
// invoice remains draft with original CustomerNameSnapshot.
func TestIssueInvoice_RejectPath_NoJE_NoSnapshot(t *testing.T) {
	db := testIssueRejectDB(t)
	companyID := seedIssueRejectCompany(t, db)

	// Customer with a distinctive name — we verify it is NOT overwritten.
	origName := "Original Customer Name"
	cust := models.Customer{CompanyID: companyID, Name: origName}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	seedIssueRejectAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedIssueRejectAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedIssueRejectAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)

	ps := models.ProductService{
		CompanyID: companyID, Name: "Service", Type: "service",
		RevenueAccountID: revID, IsActive: true,
	}
	if err := db.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}

	taxCodeID := seedIssueRejectTaxCode(t, db, companyID, taxAcctID, models.TaxScopeSales)

	// Create invoice with a deliberately stale CustomerNameSnapshot.
	staleSnapshot := "Stale Snapshot Name"
	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         "INV-REJECT-SNAP-1",
		CustomerID:            cust.ID,
		CustomerNameSnapshot:  staleSnapshot,
		CustomerEmailSnapshot: "orig@example.com",
		InvoiceDate:           time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Subtotal:              decimal.RequireFromString("500.00"),
		TaxTotal:              decimal.RequireFromString("25.00"),
		Amount:                decimal.RequireFromString("525.00"),
		BalanceDue:            decimal.RequireFromString("525.00"),
		BalanceDueBase:        decimal.RequireFromString("525.00"),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	line := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: &ps.ID,
		TaxCodeID:        &taxCodeID,
		Description:      "Service",
		Qty:              decimal.RequireFromString("1"),
		UnitPrice:        decimal.RequireFromString("500.00"),
		LineNet:          decimal.RequireFromString("500.00"),
		LineTax:          decimal.RequireFromString("25.00"),
		LineTotal:        decimal.RequireFromString("525.00"),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	// Deactivate the tax code to trigger rejection.
	if err := db.Model(&models.TaxCode{}).Where("id = ?", taxCodeID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := IssueInvoice(db, companyID, inv.ID); err == nil {
		t.Fatal("expected IssueInvoice to fail, got nil")
	}

	// CustomerNameSnapshot must NOT have been updated to the current customer name.
	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.CustomerNameSnapshot != staleSnapshot {
		t.Fatalf("CustomerNameSnapshot must remain %q (stale), got %q — IssueInvoice must not write snapshots before validation",
			staleSnapshot, reloaded.CustomerNameSnapshot)
	}

	// No journal entries must have been created.
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, models.LedgerSourceInvoice, inv.ID).
		Count(&jeCount)
	if jeCount != 0 {
		t.Fatalf("expected 0 JEs after rejection, got %d — no partial posting", jeCount)
	}

	// No journal lines either.
	var jlCount int64
	db.Model(&models.JournalLine{}).Where("company_id = ?", companyID).Count(&jlCount)
	if jlCount != 0 {
		t.Fatalf("expected 0 journal lines after rejection, got %d", jlCount)
	}
}
