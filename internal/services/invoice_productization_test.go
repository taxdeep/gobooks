// 遵循project_guide.md
package services

// invoice_productization_test.go — Batch 3 service-layer integration tests.
//
// Verifies the full truth chain for Invoice Productization:
//   ProductService → revenue account / tax code → posting fragments →
//   JE aggregation → AR initialization → payment outstanding → AR aging.
//
// Coverage:
//   TestPostInvoice_UsesProductServiceRevenueAccount  — JE credit = product's revenue account
//   TestPostInvoice_WithTaxCode_CorrectTaxLine        — JE tax credit = TaxCode.SalesTaxAccountID
//   TestPostInvoice_FreeFormLine_RejectsClearError    — nil ProductServiceID → clear error
//   TestPostInvoice_MixedLines_FreeFormCausesFailure  — any nil line blocks the whole invoice
//   TestPartialPayment_AfterItemInvoice_CorrectOutstanding — balance_due + status after partial
//   TestARReport_NotBrokenByItemLine                  — posted item invoice visible in AR aging
//   TestPostInvoice_InactiveProduct_RejectsClearError — inactive product blocked at posting

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

func testProductizationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:productization_%s?mode=memory&cache=shared", t.Name())
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

// seedProdCompany creates a company with a base currency set (required for
// FX-safe posting and payment settlement).
func seedProdCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{
		Name:             "Prod Co",
		BaseCurrencyCode: "CAD",
		IsActive:         true,
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedProdCustomer creates a minimal customer for the given company.
func seedProdCustomer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedProdAccount creates an account and returns its ID.
func seedProdAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              code,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// seedProdService creates an active ProductService with the given revenue account.
func seedProdService(t *testing.T, db *gorm.DB, companyID, revenueAccountID uint) uint {
	t.Helper()
	ps := models.ProductService{
		CompanyID:        companyID,
		Name:             "Test Service",
		Type:             "service",
		RevenueAccountID: revenueAccountID,
		IsActive:         true,
	}
	if err := db.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}
	return ps.ID
}

// seedProdInvoice seeds a draft invoice with one line linked to productID.
// net is the line amount (no tax). Returns invoiceID.
func seedProdInvoice(t *testing.T, db *gorm.DB, companyID, customerID, productID uint, net string) uint {
	t.Helper()
	netDec := decimal.RequireFromString(net)
	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-P-%d", time.Now().UnixNano()%99999),
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Test Customer",
		CustomerEmailSnapshot: "test@example.com",
		InvoiceDate:           time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Subtotal:              netDec,
		TaxTotal:              decimal.Zero,
		Amount:                netDec,
		BalanceDue:            netDec,
		BalanceDueBase:        netDec,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	line := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: &productID,
		Description:      "Test Service",
		Qty:              decimal.RequireFromString("1"),
		UnitPrice:        netDec,
		LineNet:          netDec,
		LineTax:          decimal.Zero,
		LineTotal:        netDec,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return inv.ID
}

// seedProdInvoiceTaxed seeds a draft invoice with one taxed line.
// net is the pre-tax amount; taxAmt is the tax amount. Returns invoiceID.
func seedProdInvoiceTaxed(t *testing.T, db *gorm.DB, companyID, customerID, productID, taxCodeID uint, net, taxAmt string) uint {
	t.Helper()
	netDec := decimal.RequireFromString(net)
	taxDec := decimal.RequireFromString(taxAmt)
	total := netDec.Add(taxDec)
	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-TX-%d", time.Now().UnixNano()%99999),
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Test Customer",
		CustomerEmailSnapshot: "test@example.com",
		InvoiceDate:           time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Subtotal:              netDec,
		TaxTotal:              taxDec,
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
		Description:      "Test Service (taxed)",
		Qty:              decimal.RequireFromString("1"),
		UnitPrice:        netDec,
		LineNet:          netDec,
		LineTax:          taxDec,
		LineTotal:        total,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return inv.ID
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestPostInvoice_UsesProductServiceRevenueAccount verifies that the revenue
// credit line in the JE uses the ProductService's revenue account — not any
// other account. This is the core posting truth: the UI cannot substitute a
// different revenue account; it always comes from the product at post time.
func TestPostInvoice_UsesProductServiceRevenueAccount(t *testing.T) {
	db := testProductizationDB(t)
	companyID := seedProdCompany(t, db)
	customerID := seedProdCustomer(t, db, companyID)

	seedProdAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	// Two revenue accounts — we verify only the product's account appears in JE.
	revID := seedProdAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	seedProdAccount(t, db, companyID, "4200", models.RootRevenue, models.DetailServiceRevenue) // red herring

	productID := seedProdService(t, db, companyID, revID)
	invoiceID := seedProdInvoice(t, db, companyID, customerID, productID, "500.00")

	if err := PostInvoice(db, companyID, invoiceID, "test", nil); err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invoiceID)
	if inv.JournalEntryID == nil {
		t.Fatal("JournalEntryID not set after posting")
	}

	var jeLines []models.JournalLine
	db.Where("journal_entry_id = ?", *inv.JournalEntryID).Find(&jeLines)

	// Locate the single credit line — for a tax-free service invoice it is the
	// revenue credit. Verify its AccountID matches the product's revenue account.
	var creditAccountIDs []uint
	for _, jl := range jeLines {
		if jl.Credit.GreaterThan(decimal.Zero) {
			creditAccountIDs = append(creditAccountIDs, jl.AccountID)
		}
	}
	if len(creditAccountIDs) != 1 {
		t.Fatalf("expected 1 credit line, got %d", len(creditAccountIDs))
	}
	if creditAccountIDs[0] != revID {
		t.Fatalf("credit AccountID: want %d (product's revenue account), got %d", revID, creditAccountIDs[0])
	}
}

// TestPostInvoice_WithTaxCode_CorrectTaxLine verifies that when a line has a
// tax code, the JE includes a credit line against TaxCode.SalesTaxAccountID,
// separate from the revenue credit.
func TestPostInvoice_WithTaxCode_CorrectTaxLine(t *testing.T) {
	db := testProductizationDB(t)
	companyID := seedProdCompany(t, db)
	customerID := seedProdCustomer(t, db, companyID)

	seedProdAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedProdAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedProdAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)

	// Tax code: 10% GST, sales scope, posting account = taxAcctID.
	taxCode := models.TaxCode{
		CompanyID:         companyID,
		Name:              "GST 10%",
		Code:              "GST",
		TaxType:           "taxable",
		Rate:              decimal.RequireFromString("0.10"),
		Scope:             models.TaxScopeSales,
		SalesTaxAccountID: taxAcctID,
		IsActive:          true,
	}
	if err := db.Create(&taxCode).Error; err != nil {
		t.Fatal(err)
	}

	productID := seedProdService(t, db, companyID, revID)
	// Net 1000, tax 100 (10%), total 1100.
	invoiceID := seedProdInvoiceTaxed(t, db, companyID, customerID, productID, taxCode.ID, "1000.00", "100.00")

	if err := PostInvoice(db, companyID, invoiceID, "test", nil); err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invoiceID)
	if inv.JournalEntryID == nil {
		t.Fatal("JournalEntryID not set after posting")
	}

	var jeLines []models.JournalLine
	db.Where("journal_entry_id = ?", *inv.JournalEntryID).Find(&jeLines)

	// Expected structure: DR AR 1100, CR Revenue 1000, CR Tax Payable 100.
	// Verify both credit accounts are present and correct.
	creditByAccount := map[uint]decimal.Decimal{}
	for _, jl := range jeLines {
		if jl.Credit.GreaterThan(decimal.Zero) {
			creditByAccount[jl.AccountID] = creditByAccount[jl.AccountID].Add(jl.Credit)
		}
	}

	if len(creditByAccount) != 2 {
		t.Fatalf("expected 2 credit lines (revenue + tax), got %d", len(creditByAccount))
	}

	revCredit, hasRev := creditByAccount[revID]
	if !hasRev {
		t.Fatalf("no credit line for revenue account %d", revID)
	}
	if !revCredit.Equal(decimal.RequireFromString("1000.00")) {
		t.Fatalf("revenue credit: want 1000.00, got %s", revCredit)
	}

	taxCredit, hasTax := creditByAccount[taxAcctID]
	if !hasTax {
		t.Fatalf("no credit line for tax account %d (TaxCode.SalesTaxAccountID)", taxAcctID)
	}
	if !taxCredit.Equal(decimal.RequireFromString("100.00")) {
		t.Fatalf("tax credit: want 100.00, got %s", taxCredit)
	}
}

// TestPostInvoice_FreeFormLine_RejectsClearError verifies that a line with no
// ProductServiceID is rejected by PostInvoice with a message that clearly
// communicates why: "has no product/service".
func TestPostInvoice_FreeFormLine_RejectsClearError(t *testing.T) {
	db := testProductizationDB(t)
	companyID := seedProdCompany(t, db)
	customerID := seedProdCustomer(t, db, companyID)

	seedProdAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)

	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         "INV-FREEFORM-1",
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Test Customer",
		CustomerEmailSnapshot: "test@example.com",
		InvoiceDate:           time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Subtotal:              decimal.RequireFromString("200.00"),
		TaxTotal:              decimal.Zero,
		Amount:                decimal.RequireFromString("200.00"),
		BalanceDue:            decimal.RequireFromString("200.00"),
		BalanceDueBase:        decimal.RequireFromString("200.00"),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	// Line with nil ProductServiceID — free-form.
	line := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: nil,
		Description:      "Custom consulting",
		Qty:              decimal.RequireFromString("2"),
		UnitPrice:        decimal.RequireFromString("100.00"),
		LineNet:          decimal.RequireFromString("200.00"),
		LineTax:          decimal.Zero,
		LineTotal:        decimal.RequireFromString("200.00"),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	err := PostInvoice(db, companyID, inv.ID, "test", nil)
	if err == nil {
		t.Fatal("expected PostInvoice to fail for free-form line, got nil")
	}
	if !strings.Contains(err.Error(), "has no product/service") {
		t.Fatalf("expected clear 'has no product/service' message, got: %v", err)
	}

	// Invoice must remain draft — no partial state created.
	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.Status != models.InvoiceStatusDraft {
		t.Fatalf("invoice status should still be draft, got %s", reloaded.Status)
	}
	if reloaded.JournalEntryID != nil {
		t.Fatal("JournalEntryID should be nil — no JE must be created on failure")
	}
}

// TestPostInvoice_MixedLines_FreeFormCausesFailure verifies that a single
// free-form line in an otherwise valid invoice blocks the entire posting — no
// partial JE is created for the valid lines.
func TestPostInvoice_MixedLines_FreeFormCausesFailure(t *testing.T) {
	db := testProductizationDB(t)
	companyID := seedProdCompany(t, db)
	customerID := seedProdCustomer(t, db, companyID)

	seedProdAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedProdAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	productID := seedProdService(t, db, companyID, revID)

	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         "INV-MIXED-1",
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Test Customer",
		CustomerEmailSnapshot: "test@example.com",
		InvoiceDate:           time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Subtotal:              decimal.RequireFromString("600.00"),
		TaxTotal:              decimal.Zero,
		Amount:                decimal.RequireFromString("600.00"),
		BalanceDue:            decimal.RequireFromString("600.00"),
		BalanceDueBase:        decimal.RequireFromString("600.00"),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	// Line 1: valid product line.
	line1 := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: &productID,
		Description:      "Service A",
		Qty:              decimal.RequireFromString("1"),
		UnitPrice:        decimal.RequireFromString("400.00"),
		LineNet:          decimal.RequireFromString("400.00"),
		LineTax:          decimal.Zero,
		LineTotal:        decimal.RequireFromString("400.00"),
	}
	// Line 2: free-form (nil ProductServiceID) — should block posting.
	line2 := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        2,
		ProductServiceID: nil,
		Description:      "Ad-hoc expense",
		Qty:              decimal.RequireFromString("1"),
		UnitPrice:        decimal.RequireFromString("200.00"),
		LineNet:          decimal.RequireFromString("200.00"),
		LineTax:          decimal.Zero,
		LineTotal:        decimal.RequireFromString("200.00"),
	}
	if err := db.Create(&line1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&line2).Error; err != nil {
		t.Fatal(err)
	}

	err := PostInvoice(db, companyID, inv.ID, "test", nil)
	if err == nil {
		t.Fatal("expected PostInvoice to fail for mixed invoice with free-form line")
	}

	// No JE must exist for this invoice — posting is all-or-nothing.
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, models.LedgerSourceInvoice, inv.ID).
		Count(&jeCount)
	if jeCount != 0 {
		t.Fatalf("expected 0 JEs created, got %d — posting must not partially persist", jeCount)
	}

	// Invoice must remain draft.
	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.Status != models.InvoiceStatusDraft {
		t.Fatalf("invoice status should still be draft, got %s", reloaded.Status)
	}
}

// TestPartialPayment_AfterItemInvoice_CorrectOutstanding posts an item-based
// invoice and applies a partial payment via Phase-4 allocation. Verifies that
// balance_due reflects the remaining amount and status is partially_paid.
func TestPartialPayment_AfterItemInvoice_CorrectOutstanding(t *testing.T) {
	db := testProductizationDB(t)
	companyID := seedProdCompany(t, db)
	customerID := seedProdCustomer(t, db, companyID)

	arID := seedProdAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	bankID := seedProdAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	revID := seedProdAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)

	productID := seedProdService(t, db, companyID, revID)
	invoiceID := seedProdInvoice(t, db, companyID, customerID, productID, "1000.00")

	// Post the invoice — sets status to issued and balance_due = 1000.00.
	if err := PostInvoice(db, companyID, invoiceID, "test", nil); err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	// Apply a partial payment of 300.00 via Phase-4 allocation.
	_, err := RecordReceivePayment(db, ReceivePaymentInput{
		CompanyID:     companyID,
		CustomerID:    customerID,
		EntryDate:     time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		BankAccountID: bankID,
		PaymentMethod: models.PaymentMethodWire,
		ARAccountID:   arID,
		Allocations: []InvoiceAllocation{
			{InvoiceID: invoiceID, Amount: decimal.RequireFromString("300.00")},
		},
	})
	if err != nil {
		t.Fatalf("RecordReceivePayment failed: %v", err)
	}

	var inv models.Invoice
	if err := db.First(&inv, invoiceID).Error; err != nil {
		t.Fatal(err)
	}

	// Status must be partially_paid (not paid, not still issued).
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Fatalf("expected status partially_paid, got %s", inv.Status)
	}

	// Remaining balance: 1000 - 300 = 700.
	wantBalance := decimal.RequireFromString("700.00")
	if !inv.BalanceDue.Equal(wantBalance) {
		t.Fatalf("BalanceDue: want %s, got %s", wantBalance, inv.BalanceDue)
	}
	if !inv.BalanceDueBase.Equal(wantBalance) {
		t.Fatalf("BalanceDueBase: want %s, got %s", wantBalance, inv.BalanceDueBase)
	}
}

// TestARReport_NotBrokenByItemLine verifies that a posted item-based invoice
// appears in the AR aging report with the correct outstanding balance. This
// guards against regressions where productization changes accidentally exclude
// item-based invoices from the report query.
func TestARReport_NotBrokenByItemLine(t *testing.T) {
	db := testProductizationDB(t)
	companyID := seedProdCompany(t, db)
	customerID := seedProdCustomer(t, db, companyID)

	seedProdAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedProdAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)

	productID := seedProdService(t, db, companyID, revID)
	invoiceID := seedProdInvoice(t, db, companyID, customerID, productID, "750.00")

	// Post the invoice — transitions to issued with balance_due set.
	if err := PostInvoice(db, companyID, invoiceID, "test", nil); err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	// AR aging as of one month after invoice date — invoice should be in Current bucket.
	asOf := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	report, err := BuildARAgingReport(db, companyID, asOf)
	if err != nil {
		t.Fatalf("BuildARAgingReport failed: %v", err)
	}

	if len(report.Rows) == 0 {
		t.Fatal("AR aging report is empty — item-based invoice not included")
	}

	// Locate the detail row for our invoice.
	var found *ARAgingDetailRow
	for i := range report.Rows {
		for j := range report.Rows[i].DetailRows {
			if report.Rows[i].DetailRows[j].InvoiceID == invoiceID {
				found = &report.Rows[i].DetailRows[j]
				break
			}
		}
	}
	if found == nil {
		t.Fatalf("invoice %d not found in AR aging report rows", invoiceID)
	}

	wantBalance := decimal.RequireFromString("750.00")
	if !found.BalanceDue.Equal(wantBalance) {
		t.Fatalf("AR aging BalanceDue: want %s, got %s", wantBalance, found.BalanceDue)
	}
}

// TestPostInvoice_InactiveProduct_RejectsClearError verifies that an invoice
// line referencing an inactive product is blocked at post time with a message
// that names the product status. This is the posting-layer guard complementing
// the draft-save guard in handleInvoiceSaveDraft.
func TestPostInvoice_InactiveProduct_RejectsClearError(t *testing.T) {
	db := testProductizationDB(t)
	companyID := seedProdCompany(t, db)
	customerID := seedProdCustomer(t, db, companyID)

	seedProdAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedProdAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)

	productID := seedProdService(t, db, companyID, revID)
	invoiceID := seedProdInvoice(t, db, companyID, customerID, productID, "300.00")

	// Mark the product inactive after the draft was saved.
	if err := db.Model(&models.ProductService{}).Where("id = ?", productID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	err := PostInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("expected PostInvoice to fail for inactive product, got nil")
	}
	if !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("expected error to mention 'inactive', got: %v", err)
	}

	// Invoice must remain draft with no JE.
	var inv models.Invoice
	db.First(&inv, invoiceID)
	if inv.Status != models.InvoiceStatusDraft {
		t.Fatalf("invoice status should still be draft, got %s", inv.Status)
	}
	if inv.JournalEntryID != nil {
		t.Fatal("JournalEntryID should be nil — no JE must be created for inactive product")
	}
}
