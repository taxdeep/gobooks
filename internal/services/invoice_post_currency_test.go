// 遵循project_guide.md
package services

// invoice_post_currency_test.go — Phase 3 multi-currency posting tests.
//
// Tests cover:
//   - Foreign-currency invoice: amounts are FX-scaled to base currency in the JE
//   - Foreign-currency invoice: system_key AR account (ar_USD) is used when available
//   - Foreign-currency invoice: missing exchange rate returns a descriptive error
//   - Foreign-currency bill: amounts are FX-scaled to base currency in the JE
//   - Base-currency invoice regression: existing behavior is unaffected

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

// ── DB helper ─────────────────────────────────────────────────────────────────

// testFXPostingDB creates an isolated SQLite in-memory DB with all models needed
// for multi-currency posting tests.
func testFXPostingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:fxposting_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Bill{},
		&models.BillLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.Currency{},
		&models.CompanyCurrency{},
		&models.ExchangeRate{},
		&models.CustomerAllowedCurrency{},
		&models.VendorAllowedCurrency{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

// seedFXCustomer creates a customer with multi_allowed currency policy so that
// FX posting tests are not blocked by the single-currency enforcement check.
func seedFXCustomer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{
		CompanyID:      companyID,
		Name:           "FX Test Customer",
		CurrencyPolicy: models.CustomerCurrencyPolicyMultiAllowed,
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedFXVendor creates a vendor with multi_allowed currency policy.
func seedFXVendor(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	v := models.Vendor{
		CompanyID:      companyID,
		Name:           "FX Test Vendor",
		CurrencyPolicy: models.VendorCurrencyPolicyMultiAllowed,
	}
	if err := db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v.ID
}

// seedFXCompany creates a company with the given base currency code.
func seedFXCompany(t *testing.T, db *gorm.DB, baseCurrency string) uint {
	t.Helper()
	c := models.Company{
		Name:              "FX Test Co",
		AccountCodeLength: 4,
		IsActive:          true,
		BaseCurrencyCode:  baseCurrency,
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedFXInvoice creates a draft invoice with the given currency code and amount.
func seedFXInvoice(t *testing.T, db *gorm.DB, companyID, customerID, psID uint, currencyCode, amount string) uint {
	t.Helper()
	amt := decimal.RequireFromString(amount)
	inv := models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: fmt.Sprintf("INV-FX-%d", customerID),
		CustomerID:    customerID,
		InvoiceDate:   time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusDraft,
		Amount:        amt,
		Subtotal:      amt,
		TaxTotal:      decimal.Zero,
		CurrencyCode:  currencyCode,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	line := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: &psID,
		Description:      "FX Service",
		Qty:              decimal.NewFromInt(1),
		UnitPrice:        amt,
		LineNet:          amt,
		LineTax:          decimal.Zero,
		LineTotal:        amt,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return inv.ID
}

// seedFXBill creates a draft bill with the given currency code and amount.
func seedFXBill(t *testing.T, db *gorm.DB, companyID, vendorID, expenseAcctID uint, currencyCode, amount string) uint {
	t.Helper()
	amt := decimal.RequireFromString(amount)
	bill := models.Bill{
		CompanyID:    companyID,
		BillNumber:   fmt.Sprintf("BILL-FX-%d", vendorID),
		VendorID:     vendorID,
		BillDate:     time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
		Status:       models.BillStatusDraft,
		Amount:       amt,
		Subtotal:     amt,
		TaxTotal:     decimal.Zero,
		CurrencyCode: currencyCode,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}
	line := models.BillLine{
		CompanyID:        companyID,
		BillID:           bill.ID,
		SortOrder:        1,
		Description:      "FX Expense",
		Qty:              decimal.NewFromInt(1),
		UnitPrice:        amt,
		LineNet:          amt,
		LineTax:          decimal.Zero,
		LineTotal:        amt,
		ExpenseAccountID: &expenseAcctID,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return bill.ID
}

// seedFXAccount creates an account with an optional system_key (pass "" for none).
func seedFXAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType, systemKey string, currencyCode string) uint {
	t.Helper()
	acc := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              code,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
	}
	if systemKey != "" {
		acc.SystemKey = &systemKey
		acc.IsSystemGenerated = true
		acc.CurrencyMode = models.CurrencyModeFixedForeign
		acc.CurrencyCode = &currencyCode
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	return acc.ID
}

// seedFXProductService creates a product/service linked to the given revenue account.
func seedFXProductService(t *testing.T, db *gorm.DB, companyID, revenueAcctID uint) uint {
	t.Helper()
	p := models.ProductService{
		CompanyID:        companyID,
		Name:             "FX Product",
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revenueAcctID,
		IsActive:         true,
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestPostInvoice_ForeignCurrency_FXScaling verifies that a USD invoice posted
// against a CAD-base company produces JE lines in CAD (scaled by the exchange rate)
// and that the invoice's base-currency fields are snapshotted correctly.
func TestPostInvoice_ForeignCurrency_FXScaling(t *testing.T) {
	db := testFXPostingDB(t)
	cid := seedFXCompany(t, db, "CAD")
	custID := seedFXCustomer(t, db, cid)

	// Accounts: base CAD AR (fallback) + dedicated USD AR (system key) + revenue.
	seedFXAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable, "", "")
	usdAR := seedFXAccount(t, db, cid, "1150", models.RootAsset, models.DetailAccountsReceivable, "ar_USD", "USD")
	revID := seedFXAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue, "", "")
	psID := seedFXProductService(t, db, cid, revID)

	// Exchange rate: 1 USD = 1.37 CAD (USD→CAD stored as "foreign→base").
	insertRate(t, db, nil, "USD", "CAD", fxRate(1.37), fxDate(2024, 6, 15))

	// Invoice: $100.00 USD.
	invID := seedFXInvoice(t, db, cid, custID, psID, "USD", "100.00")

	if err := PostInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	// Reload invoice and check base-currency snapshots.
	var inv models.Invoice
	db.First(&inv, invID)
	if !inv.ExchangeRate.Equal(fxRate(1.37)) {
		t.Errorf("exchange_rate: want 1.37, got %s", inv.ExchangeRate)
	}
	if !inv.AmountBase.Equal(fxRate(137.00)) {
		t.Errorf("amount_base: want 137.00, got %s", inv.AmountBase)
	}
	if !inv.SubtotalBase.Equal(fxRate(137.00)) {
		t.Errorf("subtotal_base: want 137.00, got %s", inv.SubtotalBase)
	}
	if !inv.TaxTotalBase.Equal(decimal.Zero) {
		t.Errorf("tax_total_base: want 0, got %s", inv.TaxTotalBase)
	}

	// JE lines: AR debit = 137.00 CAD, revenue credit = 137.00 CAD.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *inv.JournalEntryID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 journal lines, got %d", len(lines))
	}
	var arDebit, revCredit decimal.Decimal
	for _, l := range lines {
		if l.AccountID == usdAR {
			arDebit = l.Debit
		} else {
			revCredit = l.Credit
		}
	}
	if !arDebit.Equal(fxRate(137.00)) {
		t.Errorf("AR debit (base): want 137.00, got %s", arDebit)
	}
	if !revCredit.Equal(fxRate(137.00)) {
		t.Errorf("revenue credit (base): want 137.00, got %s", revCredit)
	}
}

// TestPostInvoice_ForeignCurrency_UsesSystemKeyAR verifies that the USD system AR
// account (system_key="ar_USD") is preferred over the generic CAD AR account.
func TestPostInvoice_ForeignCurrency_UsesSystemKeyAR(t *testing.T) {
	db := testFXPostingDB(t)
	cid := seedFXCompany(t, db, "CAD")
	custID := seedFXCustomer(t, db, cid)

	cadAR := seedFXAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable, "", "")
	usdAR := seedFXAccount(t, db, cid, "1150", models.RootAsset, models.DetailAccountsReceivable, "ar_USD", "USD")
	revID := seedFXAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue, "", "")
	psID := seedFXProductService(t, db, cid, revID)

	insertRate(t, db, nil, "USD", "CAD", fxRate(1.37), fxDate(2024, 6, 15))
	invID := seedFXInvoice(t, db, cid, custID, psID, "USD", "100.00")

	if err := PostInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invID)

	// The AR line must be on the USD AR account, not the CAD AR account.
	var arLine models.JournalLine
	db.Where("journal_entry_id = ? AND debit > 0", *inv.JournalEntryID).First(&arLine)
	if arLine.AccountID == cadAR {
		t.Error("expected USD AR account to be used; got CAD AR account instead")
	}
	if arLine.AccountID != usdAR {
		t.Errorf("expected AR account ID %d (USD AR), got %d", usdAR, arLine.AccountID)
	}
}

// TestPostInvoice_ForeignCurrency_MissingRateErrors verifies that posting a
// foreign-currency invoice without an exchange rate returns a descriptive error.
func TestPostInvoice_ForeignCurrency_MissingRateErrors(t *testing.T) {
	db := testFXPostingDB(t)
	cid := seedFXCompany(t, db, "CAD")
	custID := seedFXCustomer(t, db, cid)

	seedFXAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable, "", "")
	revID := seedFXAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue, "", "")
	psID := seedFXProductService(t, db, cid, revID)
	// No exchange rate seeded.
	invID := seedFXInvoice(t, db, cid, custID, psID, "USD", "100.00")

	err := PostInvoice(db, cid, invID, "tester", nil)
	if err == nil {
		t.Fatal("expected error for missing exchange rate, got nil")
	}
	if !strings.Contains(err.Error(), "exchange rate") {
		t.Errorf("expected 'exchange rate' in error, got: %v", err)
	}
}

func TestPostInvoice_ForeignCurrency_UsesPersistedManualExchangeRate(t *testing.T) {
	db := testFXPostingDB(t)
	cid := seedFXCompany(t, db, "CAD")
	custID := seedFXCustomer(t, db, cid)

	seedFXAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable, "", "")
	revID := seedFXAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue, "", "")
	psID := seedFXProductService(t, db, cid, revID)
	invID := seedFXInvoice(t, db, cid, custID, psID, "USD", "100.00")
	if err := db.Model(&models.Invoice{}).
		Where("id = ?", invID).
		Update("exchange_rate", fxRate(1.42)).Error; err != nil {
		t.Fatal(err)
	}

	if err := PostInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	var inv models.Invoice
	if err := db.First(&inv, invID).Error; err != nil {
		t.Fatal(err)
	}
	if !inv.ExchangeRate.Equal(fxRate(1.42)) {
		t.Fatalf("expected persisted manual rate 1.42, got %s", inv.ExchangeRate)
	}
	if !inv.AmountBase.Equal(fxRate(142.00)) {
		t.Fatalf("expected amount_base 142.00, got %s", inv.AmountBase)
	}
}

// TestPostBill_ForeignCurrency_FXScaling verifies that a USD bill posted against
// a CAD-base company produces JE lines in CAD and snapshots base-currency amounts.
func TestPostBill_ForeignCurrency_FXScaling(t *testing.T) {
	db := testFXPostingDB(t)
	cid := seedFXCompany(t, db, "CAD")
	vendID := seedFXVendor(t, db, cid)

	// Accounts: base CAD AP (fallback) + dedicated USD AP (system key) + expense.
	seedFXAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable, "", "")
	usdAP := seedFXAccount(t, db, cid, "2050", models.RootLiability, models.DetailAccountsPayable, "ap_USD", "USD")
	expID := seedFXAccount(t, db, cid, "6000", models.RootExpense, models.DetailOtherExpense, "", "")

	// Exchange rate: 1 USD = 1.37 CAD.
	insertRate(t, db, nil, "USD", "CAD", fxRate(1.37), fxDate(2024, 6, 15))

	// Bill: $200.00 USD.
	billID := seedFXBill(t, db, cid, vendID, expID, "USD", "200.00")

	if err := PostBill(db, cid, billID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}

	var bill models.Bill
	db.First(&bill, billID)
	if !bill.ExchangeRate.Equal(fxRate(1.37)) {
		t.Errorf("exchange_rate: want 1.37, got %s", bill.ExchangeRate)
	}
	if !bill.AmountBase.Equal(fxRate(274.00)) {
		t.Errorf("amount_base: want 274.00, got %s", bill.AmountBase)
	}
	// bill.Amount must preserve the original document-currency total ($200 USD).
	if !bill.Amount.Equal(fxRate(200.00)) {
		t.Errorf("amount (document currency): want 200.00, got %s", bill.Amount)
	}

	// JE: AP credit = 274.00 CAD, expense debit = 274.00 CAD.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *bill.JournalEntryID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 journal lines, got %d", len(lines))
	}
	var apCredit, expDebit decimal.Decimal
	for _, l := range lines {
		if l.AccountID == usdAP {
			apCredit = l.Credit
		} else {
			expDebit = l.Debit
		}
	}
	if !apCredit.Equal(fxRate(274.00)) {
		t.Errorf("AP credit (base): want 274.00, got %s", apCredit)
	}
	if !expDebit.Equal(fxRate(274.00)) {
		t.Errorf("expense debit (base): want 274.00, got %s", expDebit)
	}
}

func TestPostBill_ForeignCurrency_UsesPersistedManualExchangeRate(t *testing.T) {
	db := testFXPostingDB(t)
	cid := seedFXCompany(t, db, "CAD")
	vendID := seedFXVendor(t, db, cid)

	seedFXAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable, "", "")
	expID := seedFXAccount(t, db, cid, "6000", models.RootExpense, models.DetailOtherExpense, "", "")
	billID := seedFXBill(t, db, cid, vendID, expID, "USD", "200.00")
	if err := db.Model(&models.Bill{}).
		Where("id = ?", billID).
		Update("exchange_rate", fxRate(1.42)).Error; err != nil {
		t.Fatal(err)
	}

	if err := PostBill(db, cid, billID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}

	var bill models.Bill
	if err := db.First(&bill, billID).Error; err != nil {
		t.Fatal(err)
	}
	if !bill.ExchangeRate.Equal(fxRate(1.42)) {
		t.Fatalf("expected persisted manual rate 1.42, got %s", bill.ExchangeRate)
	}
	if !bill.AmountBase.Equal(fxRate(284.00)) {
		t.Fatalf("expected amount_base 284.00, got %s", bill.AmountBase)
	}
}

// TestPostInvoice_BaseCurrency_BaseFieldsBackfilled verifies that a base-currency
// invoice (no CurrencyCode) still gets its base-currency fields snapshotted at posting.
func TestPostInvoice_BaseCurrency_BaseFieldsBackfilled(t *testing.T) {
	db := testFXPostingDB(t)
	cid := seedFXCompany(t, db, "CAD")
	custID := seedCustomer(t, db, cid)

	seedFXAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable, "", "")
	revID := seedFXAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue, "", "")
	psID := seedFXProductService(t, db, cid, revID)
	// No CurrencyCode (base currency invoice).
	invID := seedFXInvoice(t, db, cid, custID, psID, "", "500.00")

	if err := PostInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invID)
	// Exchange rate stays 1 (not overwritten).
	if !inv.ExchangeRate.Equal(decimal.NewFromInt(1)) {
		t.Errorf("exchange_rate: want 1, got %s", inv.ExchangeRate)
	}
	// Base amounts equal document amounts.
	if !inv.AmountBase.Equal(fxRate(500.00)) {
		t.Errorf("amount_base: want 500.00, got %s", inv.AmountBase)
	}
	if !inv.SubtotalBase.Equal(fxRate(500.00)) {
		t.Errorf("subtotal_base: want 500.00, got %s", inv.SubtotalBase)
	}
}
