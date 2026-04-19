// 遵循project_guide.md
package services

// invoice_tax_truth_test.go — Batch 3.5 service-layer Tax Truth Chain tests.
//
// Coverage:
//   TestPostInvoice_InactiveTaxCode_RejectsClearError        — inactive tax at post time → clear error
//   TestPostInvoice_PurchaseOnlyTaxCode_Rejected             — purchase-only scope blocked at posting
//   TestPostInvoice_TaxTruth_UsesStoredLineTax               — JE uses l.LineTax, not recomputed value
//   TestPostInvoice_TaxAdjustment_RemainsConsistent          — adjusted LineTax drives JE credits
//   TestPostInvoice_TaxRateChange_DoesNotDivergJE            — rate changes after draft don't diverge AR/JE
//   TestPostInvoice_RejectPath_NoPartialJE_InactiveTax       — reject leaves invoice draft, no JE
//   TestPostInvoice_ARDebitEqualsRevenuePlusTaxCredits       — DR AR = CR Revenue + CR Tax (balance)

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

func testTaxTruthDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:tax_truth_%s?mode=memory&cache=shared", t.Name())
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

func seedTaxTruthCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{Name: "Tax Truth Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedTaxTruthCustomer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Tax Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedTaxTruthAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
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

func seedTaxTruthProduct(t *testing.T, db *gorm.DB, companyID, revenueAccountID uint) uint {
	t.Helper()
	ps := models.ProductService{
		CompanyID: companyID, Name: "Service", Type: "service",
		RevenueAccountID: revenueAccountID, IsActive: true,
	}
	if err := db.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}
	return ps.ID
}

// seedTaxTruthCode creates a TaxCode with the given scope, rate, and sales liability account.
func seedTaxTruthCode(t *testing.T, db *gorm.DB, companyID, salesAcctID uint, scope models.TaxScope, rate string) uint {
	t.Helper()
	tc := models.TaxCode{
		CompanyID:         companyID,
		Name:              fmt.Sprintf("Tax %s", rate),
		Code:              fmt.Sprintf("T%s", rate),
		TaxType:           "taxable",
		Rate:              decimal.RequireFromString(rate),
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

// seedTaxedInvoice seeds a posted-ready draft invoice with one taxed line.
// lineTax is the *stored* LineTax (as computed/adjusted at draft-save time).
// The invoice Amount = net + lineTax.
func seedTaxedInvoice(t *testing.T, db *gorm.DB, companyID, customerID, productID, taxCodeID uint, net, lineTax string) uint {
	t.Helper()
	netDec := decimal.RequireFromString(net)
	taxDec := decimal.RequireFromString(lineTax)
	total := netDec.Add(taxDec)

	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-TAX-%d", time.Now().UnixNano()%99999),
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Tax Customer",
		CustomerEmailSnapshot: "tax@example.com",
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
		Description:      "Taxed Service",
		Qty:              decimal.RequireFromString("1"),
		UnitPrice:        netDec,
		LineNet:          netDec,
		LineTax:          taxDec, // stored draft truth
		LineTotal:        total,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return inv.ID
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestPostInvoice_InactiveTaxCode_RejectsClearError verifies that an invoice
// line referencing an inactive tax code is blocked at post time with a message
// that mentions "inactive". This is the P1-C wiring test.
func TestPostInvoice_InactiveTaxCode_RejectsClearError(t *testing.T) {
	db := testTaxTruthDB(t)
	companyID := seedTaxTruthCompany(t, db)
	customerID := seedTaxTruthCustomer(t, db, companyID)

	seedTaxTruthAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedTaxTruthAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedTaxTruthAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedTaxTruthProduct(t, db, companyID, revID)
	taxCodeID := seedTaxTruthCode(t, db, companyID, taxAcctID, models.TaxScopeSales, "0.05")
	invoiceID := seedTaxedInvoice(t, db, companyID, customerID, productID, taxCodeID, "500.00", "25.00")

	// Deactivate the tax code after draft was saved (simulates code retired before posting).
	if err := db.Model(&models.TaxCode{}).Where("id = ?", taxCodeID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	err := PostInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("expected PostInvoice to fail for inactive tax code, got nil")
	}
	if !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("expected error to mention 'inactive', got: %v", err)
	}

	// Invoice must remain draft with no JE.
	var inv models.Invoice
	db.First(&inv, invoiceID)
	if inv.Status != models.InvoiceStatusDraft {
		t.Fatalf("invoice status should remain draft, got %s", inv.Status)
	}
	if inv.JournalEntryID != nil {
		t.Fatal("no JE should be created when post is rejected for inactive tax")
	}
}

// TestPostInvoice_PurchaseOnlyTaxCode_Rejected verifies that a line whose
// TaxCode.Scope == TaxScopePurchase is rejected at posting time. The code
// might have been saved to a draft before the scope guard existed (legacy
// scenario). PostInvoice must not proceed.
func TestPostInvoice_PurchaseOnlyTaxCode_Rejected(t *testing.T) {
	db := testTaxTruthDB(t)
	companyID := seedTaxTruthCompany(t, db)
	customerID := seedTaxTruthCustomer(t, db, companyID)

	seedTaxTruthAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedTaxTruthAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedTaxTruthAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedTaxTruthProduct(t, db, companyID, revID)
	// Create a purchase-only tax code directly (bypasses the web guard, simulates legacy data).
	purTaxID := seedTaxTruthCode(t, db, companyID, taxAcctID, models.TaxScopePurchase, "0.13")
	invoiceID := seedTaxedInvoice(t, db, companyID, customerID, productID, purTaxID, "500.00", "65.00")

	err := PostInvoice(db, companyID, invoiceID, "test", nil)
	if err == nil {
		t.Fatal("expected PostInvoice to fail for purchase-only tax code, got nil")
	}
	if !strings.Contains(err.Error(), "purchases only") {
		t.Fatalf("expected error to mention 'purchases only', got: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invoiceID)
	if inv.Status != models.InvoiceStatusDraft {
		t.Fatalf("invoice status should remain draft, got %s", inv.Status)
	}
	if inv.JournalEntryID != nil {
		t.Fatal("no JE should be created for purchase-only tax code on sales invoice")
	}
}

// TestPostInvoice_TaxTruth_UsesStoredLineTax verifies that BuildInvoiceFragments
// uses the stored l.LineTax (draft truth) as the tax credit, NOT a recomputed
// value from the current tax rate. This is the P1-B Method A invariant.
//
// Test setup:
//   Draft saves: net=1000, LineTax=50 (rate was 5%)
//   Then rate is changed to 10% before posting.
//   Expected: JE tax credit = 50 (stored), not 100 (current rate × net).
func TestPostInvoice_TaxTruth_UsesStoredLineTax(t *testing.T) {
	db := testTaxTruthDB(t)
	companyID := seedTaxTruthCompany(t, db)
	customerID := seedTaxTruthCustomer(t, db, companyID)

	seedTaxTruthAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedTaxTruthAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedTaxTruthAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedTaxTruthProduct(t, db, companyID, revID)
	taxCodeID := seedTaxTruthCode(t, db, companyID, taxAcctID, models.TaxScopeSales, "0.05")

	// Draft saved with 5% rate: net=1000, LineTax=50, Amount=1050.
	invoiceID := seedTaxedInvoice(t, db, companyID, customerID, productID, taxCodeID, "1000.00", "50.00")

	// Simulate rate change to 10% AFTER draft was saved.
	if err := db.Model(&models.TaxCode{}).Where("id = ?", taxCodeID).
		Update("rate", "0.10").Error; err != nil {
		t.Fatal(err)
	}

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

	// Find the tax credit line (posted to taxAcctID).
	var taxCredit decimal.Decimal
	var arDebit decimal.Decimal
	var revCredit decimal.Decimal
	for _, jl := range jeLines {
		if jl.Credit.GreaterThan(decimal.Zero) {
			if jl.AccountID == taxAcctID {
				taxCredit = jl.Credit
			} else {
				revCredit = revCredit.Add(jl.Credit)
			}
		}
		if jl.Debit.GreaterThan(decimal.Zero) {
			arDebit = arDebit.Add(jl.Debit)
		}
	}

	// Tax credit must be the stored LineTax (50), not recomputed (100).
	if !taxCredit.Equal(decimal.RequireFromString("50.00")) {
		t.Fatalf("tax credit: want 50.00 (stored LineTax), got %s (rate-change recompute would give 100.00)", taxCredit)
	}
	// Revenue credit must be the net.
	if !revCredit.Equal(decimal.RequireFromString("1000.00")) {
		t.Fatalf("revenue credit: want 1000.00, got %s", revCredit)
	}
	// AR debit must equal invoice Amount (set at draft-save time).
	if !arDebit.Equal(decimal.RequireFromString("1050.00")) {
		t.Fatalf("AR debit: want 1050.00, got %s", arDebit)
	}
}

// TestPostInvoice_TaxAdjustment_RemainsConsistent verifies that when a user
// applies a tax adjustment at draft-save time (overriding the computed LineTax),
// the posting uses the adjusted LineTax, keeping JE and AR consistent.
//
// Scenario: net=1000, computed tax=50, user adjusts to 48 (rounds to nearest dollar).
// Draft saves LineTax=48, Amount=1048.
// JE must: DR AR 1048, CR Revenue 1000, CR Tax 48.
func TestPostInvoice_TaxAdjustment_RemainsConsistent(t *testing.T) {
	db := testTaxTruthDB(t)
	companyID := seedTaxTruthCompany(t, db)
	customerID := seedTaxTruthCustomer(t, db, companyID)

	seedTaxTruthAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedTaxTruthAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedTaxTruthAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedTaxTruthProduct(t, db, companyID, revID)
	taxCodeID := seedTaxTruthCode(t, db, companyID, taxAcctID, models.TaxScopeSales, "0.05")

	// Draft with user-adjusted LineTax=48 (not the computed 50).
	invoiceID := seedTaxedInvoice(t, db, companyID, customerID, productID, taxCodeID, "1000.00", "48.00")

	if err := PostInvoice(db, companyID, invoiceID, "test", nil); err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invoiceID)

	var jeLines []models.JournalLine
	db.Where("journal_entry_id = ?", *inv.JournalEntryID).Find(&jeLines)

	var taxCredit, revCredit, arDebit decimal.Decimal
	for _, jl := range jeLines {
		if jl.Credit.GreaterThan(decimal.Zero) {
			if jl.AccountID == taxAcctID {
				taxCredit = taxCredit.Add(jl.Credit)
			} else {
				revCredit = revCredit.Add(jl.Credit)
			}
		}
		if jl.Debit.GreaterThan(decimal.Zero) {
			arDebit = arDebit.Add(jl.Debit)
		}
	}

	// All three sides must use the adjusted values.
	if !taxCredit.Equal(decimal.RequireFromString("48.00")) {
		t.Fatalf("tax credit: want 48.00 (adjusted), got %s", taxCredit)
	}
	if !revCredit.Equal(decimal.RequireFromString("1000.00")) {
		t.Fatalf("revenue credit: want 1000.00, got %s", revCredit)
	}
	if !arDebit.Equal(decimal.RequireFromString("1048.00")) {
		t.Fatalf("AR debit: want 1048.00, got %s", arDebit)
	}
	// JE must be balanced.
	if !arDebit.Equal(revCredit.Add(taxCredit)) {
		t.Fatalf("JE imbalance: AR debit %s ≠ revenue %s + tax %s", arDebit, revCredit, taxCredit)
	}
}

// TestPostInvoice_TaxRateChange_DoesNotDivergeJE verifies that a tax rate
// change between draft-save and posting does NOT cause a JE imbalance.
// Since BuildInvoiceFragments uses stored LineTax (Method A), the AR debit
// (= inv.Amount = net + stored tax) always equals Σ credits.
func TestPostInvoice_TaxRateChange_DoesNotDivergeJE(t *testing.T) {
	db := testTaxTruthDB(t)
	companyID := seedTaxTruthCompany(t, db)
	customerID := seedTaxTruthCustomer(t, db, companyID)

	seedTaxTruthAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedTaxTruthAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedTaxTruthAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedTaxTruthProduct(t, db, companyID, revID)
	taxCodeID := seedTaxTruthCode(t, db, companyID, taxAcctID, models.TaxScopeSales, "0.05")

	// Draft: net=800, LineTax=40 (5%), Amount=840.
	invoiceID := seedTaxedInvoice(t, db, companyID, customerID, productID, taxCodeID, "800.00", "40.00")

	// Change rate to 15% — would produce 120 if recomputed.
	if err := db.Model(&models.TaxCode{}).Where("id = ?", taxCodeID).
		Update("rate", "0.15").Error; err != nil {
		t.Fatal(err)
	}

	if err := PostInvoice(db, companyID, invoiceID, "test", nil); err != nil {
		t.Fatalf("PostInvoice should not fail after rate change when using stored LineTax: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invoiceID)

	var jeLines []models.JournalLine
	db.Where("journal_entry_id = ?", *inv.JournalEntryID).Find(&jeLines)

	totalDebit := decimal.Zero
	totalCredit := decimal.Zero
	for _, jl := range jeLines {
		totalDebit = totalDebit.Add(jl.Debit)
		totalCredit = totalCredit.Add(jl.Credit)
	}

	if !totalDebit.Equal(totalCredit) {
		t.Fatalf("JE imbalance after rate change: DR %s ≠ CR %s", totalDebit, totalCredit)
	}
	// Totals must match the invoice Amount (840, not rate-change value 920).
	if !totalDebit.Equal(decimal.RequireFromString("840.00")) {
		t.Fatalf("JE total debit: want 840.00 (stored Amount), got %s", totalDebit)
	}
}

// TestPostInvoice_RejectPath_NoPartialJE_InactiveTax verifies that a rejection
// due to inactive tax code leaves the invoice in draft and creates no JE at all.
// No partial persistence must occur.
func TestPostInvoice_RejectPath_NoPartialJE_InactiveTax(t *testing.T) {
	db := testTaxTruthDB(t)
	companyID := seedTaxTruthCompany(t, db)
	customerID := seedTaxTruthCustomer(t, db, companyID)

	seedTaxTruthAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedTaxTruthAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedTaxTruthAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedTaxTruthProduct(t, db, companyID, revID)
	taxCodeID := seedTaxTruthCode(t, db, companyID, taxAcctID, models.TaxScopeSales, "0.05")
	invoiceID := seedTaxedInvoice(t, db, companyID, customerID, productID, taxCodeID, "300.00", "15.00")

	if err := db.Model(&models.TaxCode{}).Where("id = ?", taxCodeID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	if err := PostInvoice(db, companyID, invoiceID, "test", nil); err == nil {
		t.Fatal("expected PostInvoice to fail for inactive tax code")
	}

	// No JE must exist for this invoice.
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, models.LedgerSourceInvoice, invoiceID).
		Count(&jeCount)
	if jeCount != 0 {
		t.Fatalf("expected 0 JEs, got %d — reject path must not partially persist", jeCount)
	}

	// Invoice stays draft.
	var inv models.Invoice
	db.First(&inv, invoiceID)
	if inv.Status != models.InvoiceStatusDraft {
		t.Fatalf("invoice should remain draft after rejection, got %s", inv.Status)
	}
	if inv.JournalEntryID != nil {
		t.Fatal("JournalEntryID must be nil after rejection")
	}
}

// TestPostInvoice_ARDebitEqualsRevenuePlusTaxCredits verifies double-entry
// balance for an item-based taxed invoice:
//   DR AR = CR Revenue + CR Tax Payable
// This is the core accounting integrity check under Method A.
func TestPostInvoice_ARDebitEqualsRevenuePlusTaxCredits(t *testing.T) {
	db := testTaxTruthDB(t)
	companyID := seedTaxTruthCompany(t, db)
	customerID := seedTaxTruthCustomer(t, db, companyID)

	seedTaxTruthAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedTaxTruthAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailServiceRevenue)
	taxAcctID := seedTaxTruthAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedTaxTruthProduct(t, db, companyID, revID)
	taxCodeID := seedTaxTruthCode(t, db, companyID, taxAcctID, models.TaxScopeSales, "0.07")

	// Net=2000, 7% tax=140, Amount=2140.
	invoiceID := seedTaxedInvoice(t, db, companyID, customerID, productID, taxCodeID, "2000.00", "140.00")

	if err := PostInvoice(db, companyID, invoiceID, "test", nil); err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invoiceID)

	var jeLines []models.JournalLine
	db.Where("journal_entry_id = ?", *inv.JournalEntryID).Find(&jeLines)

	totalDebit := decimal.Zero
	totalCredit := decimal.Zero
	for _, jl := range jeLines {
		totalDebit = totalDebit.Add(jl.Debit)
		totalCredit = totalCredit.Add(jl.Credit)
	}

	if !totalDebit.Equal(totalCredit) {
		t.Fatalf("JE not balanced: DR %s ≠ CR %s", totalDebit, totalCredit)
	}
	if !totalDebit.Equal(decimal.RequireFromString("2140.00")) {
		t.Fatalf("JE total: want 2140.00, got %s", totalDebit)
	}

	// AR debit = invoice Amount — verify it matches the stored Amount, not a recomputed value.
	if !inv.Amount.Equal(decimal.RequireFromString("2140.00")) {
		t.Fatalf("inv.Amount: want 2140.00, got %s", inv.Amount)
	}
}
