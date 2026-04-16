// 遵循project_guide.md
package services

// financial_integrity_test.go — tests for the financial integrity fix pass.
//
// Coverage (Round 3):
//   TestReportDateFilter_TB              — TB excludes JEs outside the date range
//   TestReportDateFilter_IS              — IS excludes JEs outside the date range
//   TestReportDateFilter_BS              — BS excludes JEs outside the as-of date
//   TestReportDateFilter_UnpostedExcluded — unposted JEs are excluded from reports
//   TestGateway_ChannelInvoiceBlocked    — channel-origin invoices rejected by CreatePaymentRequestForInvoice
//   TestGateway_FXInvoiceBlocked         — FX invoices rejected by CreatePaymentRequestForInvoice
//   TestReceivePayment_AppearsInLedger   — RecordReceivePayment creates ledger_entries
//   TestPayBills_AppearsInLedger         — RecordPayBills creates ledger_entries
//   TestVoidInvoice_BlockedBySettlement  — VoidInvoice blocked when settlement allocation exists

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB helpers ────────────────────────────────────────────────────────────────

func testFinancialIntegrityDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:fin_integrity_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.Invoice{},
		&models.Bill{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.SettlementAllocation{},
		&models.CreditNoteApplication{}, // required by VoidInvoice credit-application reversal
		&models.APCreditApplication{},   // required by VoidBill credit-application reversal
		&models.PaymentReceipt{},
		&models.Currency{},
		&models.ExchangeRate{},
		&models.PaymentGatewayAccount{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
		&models.InventoryMovement{},
		&models.AuditLog{},
		&models.TaskInvoiceSource{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedFICompany(t *testing.T, db *gorm.DB, baseCurrency string) uint {
	t.Helper()
	c := models.Company{
		Name:              "FI Corp",
		AccountCodeLength: 4,
		IsActive:          true,
		BaseCurrencyCode:  baseCurrency,
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedFIAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
	t.Helper()
	acc := models.Account{
		CompanyID: companyID, Code: code, Name: code,
		RootAccountType: root, DetailAccountType: detail, IsActive: true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	return acc.ID
}

// seedFIJE creates a journal entry + one line and returns the JE ID.
func seedFIJE(t *testing.T, db *gorm.DB, companyID, accountID uint, date time.Time, status models.JournalEntryStatus, debit, credit string) uint {
	t.Helper()
	je := models.JournalEntry{
		CompanyID: companyID,
		EntryDate: date,
		JournalNo: fmt.Sprintf("JE-%s-%s", date.Format("060102"), status),
		Status:    status,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}
	line := models.JournalLine{
		CompanyID:      companyID,
		JournalEntryID: je.ID,
		AccountID:      accountID,
		Debit:          decimal.RequireFromString(debit),
		Credit:         decimal.RequireFromString(credit),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return je.ID
}

// ── Report date filter tests ──────────────────────────────────────────────────

// TestReportDateFilter_TB verifies that TrialBalance only aggregates JEs
// whose entry_date falls within [fromDate, toDate] inclusive.
// An older JE (January) must not bleed into a March query.
func TestReportDateFilter_TB(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	accID := seedFIAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)

	march15 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	jan10 := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)

	// March entry: $100 debit (inside range).
	seedFIJE(t, db, cid, accID, march15, models.JournalEntryStatusPosted, "100.00", "0.00")
	// January entry: $500 debit (outside March range — must NOT appear).
	seedFIJE(t, db, cid, accID, jan10, models.JournalEntryStatusPosted, "500.00", "0.00")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	rows, debits, _, err := TrialBalance(db, cid, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	want := decimal.RequireFromString("100.00")
	if !debits.Equal(want) {
		t.Errorf("TB debits: want %s, got %s (January entry leaked into March)", want, debits)
	}
}

// TestReportDateFilter_IS verifies IncomeStatementReport excludes out-of-range JEs.
func TestReportDateFilter_IS(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	accID := seedFIAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue)

	march15 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	jan10 := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)

	// March revenue credit: $300.
	seedFIJE(t, db, cid, accID, march15, models.JournalEntryStatusPosted, "0.00", "300.00")
	// January revenue credit: $1000 (must NOT appear in March IS).
	seedFIJE(t, db, cid, accID, jan10, models.JournalEntryStatusPosted, "0.00", "1000.00")

	report, err := IncomeStatementReport(db, cid,
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := decimal.RequireFromString("300.00")
	if !report.TotalRevenue.Equal(want) {
		t.Errorf("IS revenue: want %s, got %s (January entry leaked into March)", want, report.TotalRevenue)
	}
}

// TestReportDateFilter_BS verifies BalanceSheetReport excludes JEs after the as-of date.
func TestReportDateFilter_BS(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	accID := seedFIAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)

	mar31 := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	apr5 := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)

	// March entry: $200 (inside as-of Mar 31).
	seedFIJE(t, db, cid, accID, mar31, models.JournalEntryStatusPosted, "200.00", "0.00")
	// April entry: $800 (after as-of — must NOT appear in BS as-of Mar 31).
	seedFIJE(t, db, cid, accID, apr5, models.JournalEntryStatusPosted, "800.00", "0.00")

	report, err := BalanceSheetReport(db, cid, mar31)
	if err != nil {
		t.Fatal(err)
	}
	want := decimal.RequireFromString("200.00")
	if !report.TotalAssets.Equal(want) {
		t.Errorf("BS assets as-of Mar 31: want %s, got %s (April entry leaked)", want, report.TotalAssets)
	}
}

// TestReportDateFilter_UnpostedExcluded verifies that draft/unposted JEs are
// excluded from all reports (status != 'posted').
func TestReportDateFilter_UnpostedExcluded(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	accID := seedFIAccount(t, db, cid, "1000", models.RootAsset, models.DetailBank)

	march15 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	// Posted entry: $100 (should appear).
	seedFIJE(t, db, cid, accID, march15, models.JournalEntryStatusPosted, "100.00", "0.00")
	// Draft/unposted entry: $999 (must NOT appear in TB).
	seedFIJE(t, db, cid, accID, march15, models.JournalEntryStatusDraft, "999.00", "0.00")

	rows, debits, _, err := TrialBalance(db, cid,
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	want := decimal.RequireFromString("100.00")
	if !debits.Equal(want) {
		t.Errorf("TB debits: want %s, got %s (unposted JE included)", want, debits)
	}
}

// ── Payment gateway block tests ───────────────────────────────────────────────

// TestGateway_ChannelInvoiceBlocked verifies that CreatePaymentRequestForInvoice
// rejects invoices that originated from a channel order.
func TestGateway_ChannelInvoiceBlocked(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")

	cust := models.Customer{CompanyID: cid, Name: "Chan Customer"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	// Simulate a channel order by setting ChannelOrderID to a non-nil value.
	channelOrderID := uint(9001)
	inv := models.Invoice{
		CompanyID:      cid,
		InvoiceNumber:  "CH-001",
		CustomerID:     cust.ID,
		InvoiceDate:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:         models.InvoiceStatusSent,
		CurrencyCode:   "CAD",
		Amount:         decimal.RequireFromString("500.00"),
		BalanceDue:     decimal.RequireFromString("500.00"),
		ChannelOrderID: &channelOrderID,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	gw := models.PaymentGatewayAccount{
		CompanyID:    cid,
		DisplayName:  "Stripe",
		ProviderType: "stripe",
		IsActive:     true,
	}
	if err := db.Create(&gw).Error; err != nil {
		t.Fatal(err)
	}

	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID:        cid,
		InvoiceID:        inv.ID,
		GatewayAccountID: gw.ID,
	})
	if err == nil {
		t.Fatal("expected error for channel-origin invoice, got nil")
	}
	if err != ErrChannelInvoiceGatewayBlock {
		t.Errorf("expected ErrChannelInvoiceGatewayBlock, got: %v", err)
	}
}

// TestGateway_FXInvoiceBlocked verifies that CreatePaymentRequestForInvoice
// rejects invoices denominated in a foreign currency.
func TestGateway_FXInvoiceBlocked(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD") // base currency = CAD

	cust := models.Customer{CompanyID: cid, Name: "FX Customer"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	// USD invoice (foreign currency for a CAD-base company).
	inv := models.Invoice{
		CompanyID:     cid,
		InvoiceNumber: "FX-001",
		CustomerID:    cust.ID,
		InvoiceDate:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusSent,
		CurrencyCode:  "USD",
		Amount:        decimal.RequireFromString("400.00"),
		BalanceDue:    decimal.RequireFromString("400.00"),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	gw := models.PaymentGatewayAccount{
		CompanyID:    cid,
		DisplayName:  "Stripe",
		ProviderType: "stripe",
		IsActive:     true,
	}
	if err := db.Create(&gw).Error; err != nil {
		t.Fatal(err)
	}

	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID:        cid,
		InvoiceID:        inv.ID,
		GatewayAccountID: gw.ID,
	})
	if err == nil {
		t.Fatal("expected error for FX invoice, got nil")
	}
	if err != ErrFXInvoiceGatewayBlock {
		t.Errorf("expected ErrFXInvoiceGatewayBlock, got: %v", err)
	}
}

// ── Ledger projection tests ───────────────────────────────────────────────────

// TestReceivePayment_AppearsInLedger verifies that RecordReceivePayment (legacy path)
// creates ledger_entries so that the payment appears in TB/IS/BS reports.
func TestReceivePayment_AppearsInLedger(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")

	bankAccID := seedFIAccount(t, db, cid, "1010", models.RootAsset, models.DetailBank)
	arAccID := seedFIAccount(t, db, cid, "1200", models.RootAsset, models.DetailAccountsReceivable)

	cust := models.Customer{CompanyID: cid, Name: "Cust Ledger"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	jeID, err := RecordReceivePayment(db, ReceivePaymentInput{
		CompanyID:     cid,
		CustomerID:    cust.ID,
		EntryDate:     time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		BankAccountID: bankAccID,
		PaymentMethod: models.PaymentMethodWire,
		ARAccountID:   arAccID,
		Amount:        decimal.RequireFromString("250.00"),
		Memo:          "Test payment",
	})
	if err != nil {
		t.Fatalf("RecordReceivePayment failed: %v", err)
	}
	if jeID == 0 {
		t.Fatal("expected non-zero JE ID")
	}

	var count int64
	if err := db.Model(&models.LedgerEntry{}).
		Where("company_id = ? AND journal_entry_id = ?", cid, jeID).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("RecordReceivePayment did not create any ledger_entries (payment invisible to reports)")
	}
}

// TestPayBills_AppearsInLedger verifies that RecordPayBills creates ledger_entries
// so that vendor payments appear in TB/IS/BS reports.
func TestPayBills_AppearsInLedger(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")

	bankAccID := seedFIAccount(t, db, cid, "1010", models.RootAsset, models.DetailBank)
	apAccID := seedFIAccount(t, db, cid, "2100", models.RootLiability, models.DetailAccountsPayable)

	vendor := models.Vendor{CompanyID: cid, Name: "Vendor Ledger"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}

	bill := models.Bill{
		CompanyID:    cid,
		BillNumber:   "BILL-001",
		VendorID:     vendor.ID,
		BillDate:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:       models.BillStatusPosted,
		CurrencyCode: "CAD",
		Amount:       decimal.RequireFromString("150.00"),
		BalanceDue:   decimal.RequireFromString("150.00"),
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}

	jeID, err := RecordPayBills(db, PayBillsInput{
		CompanyID:     cid,
		EntryDate:     time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		BankAccountID: bankAccID,
		APAccountID:   apAccID,
		Bills: []BillPayment{{
			BillID: bill.ID,
			Amount: decimal.RequireFromString("150.00"),
		}},
		Memo: "Pay vendor",
	})
	if err != nil {
		t.Fatalf("RecordPayBills failed: %v", err)
	}
	if jeID == 0 {
		t.Fatal("expected non-zero JE ID")
	}

	var count int64
	if err := db.Model(&models.LedgerEntry{}).
		Where("company_id = ? AND journal_entry_id = ?", cid, jeID).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("RecordPayBills did not create any ledger_entries (payment invisible to reports)")
	}
}

// ── Void guard tests ──────────────────────────────────────────────────────────

// TestVoidInvoice_BlockedBySettlement verifies that VoidInvoice returns an error
// when a settlement_allocation exists for the invoice (Phase-4 payment applied).
func TestVoidInvoice_BlockedBySettlement(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")

	bankAccID := seedFIAccount(t, db, cid, "1010", models.RootAsset, models.DetailBank)
	arAccID := seedFIAccount(t, db, cid, "1200", models.RootAsset, models.DetailAccountsReceivable)
	_ = seedFIAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue)

	cust := models.Customer{CompanyID: cid, Name: "Cust Void"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	// Create a posted invoice with a journal entry (required by VoidInvoice).
	je := models.JournalEntry{
		CompanyID: cid,
		EntryDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		JournalNo: "INV-001",
		Status:    models.JournalEntryStatusPosted,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}

	inv := models.Invoice{
		CompanyID:      cid,
		InvoiceNumber:  "INV-001",
		CustomerID:     cust.ID,
		InvoiceDate:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:         models.InvoiceStatusSent,
		CurrencyCode:   "CAD",
		Amount:         decimal.RequireFromString("300.00"),
		BalanceDue:     decimal.RequireFromString("0.00"), // fully paid
		JournalEntryID: &je.ID,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	// Create a journal line on the JE so VoidInvoice can reverse it.
	jeLine := models.JournalLine{
		CompanyID:      cid,
		JournalEntryID: je.ID,
		AccountID:      arAccID,
		Debit:          decimal.RequireFromString("300.00"),
		Credit:         decimal.Zero,
	}
	if err := db.Create(&jeLine).Error; err != nil {
		t.Fatal(err)
	}

	// Create a ledger entry for the original JE (required by VoidInvoice reversal).
	le := models.LedgerEntry{
		CompanyID:      cid,
		JournalEntryID: je.ID,
		SourceType:     models.LedgerSourceInvoice,
		SourceID:       inv.ID,
		AccountID:      arAccID,
		PostingDate:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		DebitAmount:    decimal.RequireFromString("300.00"),
		CreditAmount:   decimal.Zero,
		Status:         models.LedgerEntryStatusActive,
	}
	if err := db.Create(&le).Error; err != nil {
		t.Fatal(err)
	}

	// Simulate a Phase-4 payment allocation: payment applied against the invoice.
	payJE, err := RecordReceivePayment(db, ReceivePaymentInput{
		CompanyID:     cid,
		CustomerID:    cust.ID,
		EntryDate:     time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		BankAccountID: bankAccID,
		PaymentMethod: models.PaymentMethodCheck,
		ARAccountID:   arAccID,
		Allocations: []InvoiceAllocation{{
			InvoiceID: inv.ID,
			Amount:    decimal.RequireFromString("300.00"),
		}},
	})
	if err != nil {
		t.Fatalf("RecordReceivePayment failed: %v", err)
	}
	if payJE == 0 {
		t.Fatal("expected non-zero payment JE ID")
	}

	// Now attempt to void the invoice — must be blocked by the settlement allocation.
	err = VoidInvoice(db, cid, inv.ID, "tester", nil)
	if err == nil {
		t.Fatal("expected VoidInvoice to return error due to settlement allocation, got nil")
	}
	t.Logf("VoidInvoice correctly blocked: %v", err)
}
