// 遵循project_guide.md
package services

// critical_fixes_test.go — tests for the 3 Critical fixes identified in the system audit.
//
// Coverage:
//   TestVoidInvoice_ClearsBalanceDue            — VoidInvoice zeroes balance_due and balance_due_base
//   TestVoidBill_BlockedBySettlementAllocation  — VoidBill blocked when a SettlementAllocation exists
//   TestVoidBill_ClearsBalanceDue               — VoidBill zeroes balance_due and balance_due_base
//   TestApplyUnapply_RestoresBalanceDueBase      — Apply then Unapply restores balance_due_base

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// ── Test 1: VoidInvoice clears balance_due / balance_due_base ────────────────

// TestVoidInvoice_ClearsBalanceDue verifies that after VoidInvoice, both
// balance_due and balance_due_base are zero on the voided invoice row.
//
// Setup: sent invoice, balance_due = 500, balance_due_base = 500.
// No payment transactions, no settlement allocations (void is allowed).
// After void: both balance fields must be 0.
func TestVoidInvoice_ClearsBalanceDue(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	arAccID := seedFIAccount(t, db, cid, "1200", models.RootAsset, models.DetailAccountsReceivable)

	cust := models.Customer{CompanyID: cid, Name: "Void Cust"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	// Create a posted JE (required by VoidInvoice).
	je := models.JournalEntry{
		CompanyID: cid,
		EntryDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		JournalNo: "INV-VOID-CLR",
		Status:    models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourceInvoice,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}

	// JE line — required so VoidInvoice can build the reversal.
	jeLine := models.JournalLine{
		CompanyID:      cid,
		JournalEntryID: je.ID,
		AccountID:      arAccID,
		Debit:          decimal.RequireFromString("500.00"),
		Credit:         decimal.Zero,
	}
	if err := db.Create(&jeLine).Error; err != nil {
		t.Fatal(err)
	}

	// Ledger entry — required by MarkLedgerEntriesReversed.
	le := models.LedgerEntry{
		CompanyID:      cid,
		JournalEntryID: je.ID,
		SourceType:     models.LedgerSourceInvoice,
		SourceID:       0, // will be linked after invoice is created
		AccountID:      arAccID,
		PostingDate:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		DebitAmount:    decimal.RequireFromString("500.00"),
		CreditAmount:   decimal.Zero,
		Status:         models.LedgerEntryStatusActive,
	}
	if err := db.Create(&le).Error; err != nil {
		t.Fatal(err)
	}

	// Invoice: sent, balance_due=500, balance_due_base=500.
	inv := models.Invoice{
		CompanyID:            cid,
		InvoiceNumber:        "INV-VOID-CLR",
		CustomerID:           cust.ID,
		InvoiceDate:          time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:               models.InvoiceStatusSent,
		CurrencyCode:         "CAD",
		Amount:               decimal.RequireFromString("500.00"),
		BalanceDue:           decimal.RequireFromString("500.00"),
		BalanceDueBase:       decimal.RequireFromString("500.00"),
		Subtotal:             decimal.RequireFromString("500.00"),
		TaxTotal:             decimal.Zero,
		JournalEntryID:       &je.ID,
		CustomerNameSnapshot: "Void Cust",
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	if err := VoidInvoice(db, cid, inv.ID, "tester", nil); err != nil {
		t.Fatalf("VoidInvoice failed: %v", err)
	}

	// Reload and check balance fields.
	var after models.Invoice
	if err := db.First(&after, inv.ID).Error; err != nil {
		t.Fatal(err)
	}

	if !after.BalanceDue.IsZero() {
		t.Errorf("balance_due after void: want 0, got %s", after.BalanceDue)
	}
	if !after.BalanceDueBase.IsZero() {
		t.Errorf("balance_due_base after void: want 0, got %s", after.BalanceDueBase)
	}
	if after.Status != models.InvoiceStatusVoided {
		t.Errorf("status after void: want voided, got %s", after.Status)
	}
}

// ── Test 2: VoidBill blocked by SettlementAllocation ─────────────────────────

// TestVoidBill_BlockedBySettlementAllocation verifies that VoidBill returns an
// error when a SettlementAllocation exists for the bill (Phase-4 payment applied).
func TestVoidBill_BlockedBySettlementAllocation(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	apAccID := seedFIAccount(t, db, cid, "2100", models.RootLiability, models.DetailAccountsPayable)

	vendor := models.Vendor{CompanyID: cid, Name: "Void Vendor"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}

	// JE for the bill posting.
	je := models.JournalEntry{
		CompanyID:  cid,
		EntryDate:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		JournalNo:  "BILL-VOID-ALLOC",
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourceBill,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}

	jeLine := models.JournalLine{
		CompanyID:      cid,
		JournalEntryID: je.ID,
		AccountID:      apAccID,
		Debit:          decimal.Zero,
		Credit:         decimal.RequireFromString("200.00"),
	}
	if err := db.Create(&jeLine).Error; err != nil {
		t.Fatal(err)
	}

	le := models.LedgerEntry{
		CompanyID:      cid,
		JournalEntryID: je.ID,
		SourceType:     models.LedgerSourceBill,
		AccountID:      apAccID,
		PostingDate:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		DebitAmount:    decimal.Zero,
		CreditAmount:   decimal.RequireFromString("200.00"),
		Status:         models.LedgerEntryStatusActive,
	}
	if err := db.Create(&le).Error; err != nil {
		t.Fatal(err)
	}

	bill := models.Bill{
		CompanyID:      cid,
		BillNumber:     "BILL-ALLOC",
		VendorID:       vendor.ID,
		BillDate:       time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:         models.BillStatusPosted,
		CurrencyCode:   "CAD",
		Amount:         decimal.RequireFromString("200.00"),
		BalanceDue:     decimal.RequireFromString("200.00"),
		BalanceDueBase: decimal.RequireFromString("200.00"),
		JournalEntryID: &je.ID,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}

	// Payment JE simulating RecordPayBills.
	payJE := models.JournalEntry{
		CompanyID:  cid,
		EntryDate:  time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		JournalNo:  "PAY-BILL-ALLOC",
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourcePayment,
	}
	if err := db.Create(&payJE).Error; err != nil {
		t.Fatal(err)
	}

	// Settlement allocation linking the payment JE to the bill.
	alloc := models.SettlementAllocation{
		CompanyID:        cid,
		JournalEntryID:   payJE.ID,
		DocumentType:     models.SettlementDocBill,
		DocumentID:       bill.ID,
		AmountApplied:    decimal.RequireFromString("200.00"),
		ARAPBaseReleased: decimal.RequireFromString("200.00"),
		BankBaseAmount:   decimal.RequireFromString("200.00"),
		SettlementRate:   decimal.NewFromInt(1),
	}
	if err := db.Create(&alloc).Error; err != nil {
		t.Fatal(err)
	}

	err := VoidBill(db, cid, bill.ID, "tester", nil)
	if err == nil {
		t.Fatal("expected VoidBill to be blocked by settlement allocation, got nil")
	}
	t.Logf("VoidBill correctly blocked: %v", err)
}

// ── Test 3: VoidBill clears balance_due / balance_due_base ───────────────────

// TestVoidBill_ClearsBalanceDue verifies that after VoidBill, both
// balance_due and balance_due_base are zero on the voided bill row.
func TestVoidBill_ClearsBalanceDue(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	apAccID := seedFIAccount(t, db, cid, "2100", models.RootLiability, models.DetailAccountsPayable)

	vendor := models.Vendor{CompanyID: cid, Name: "Void Vendor B"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}

	je := models.JournalEntry{
		CompanyID:  cid,
		EntryDate:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		JournalNo:  "BILL-VOID-CLR",
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourceBill,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}

	jeLine := models.JournalLine{
		CompanyID:      cid,
		JournalEntryID: je.ID,
		AccountID:      apAccID,
		Debit:          decimal.Zero,
		Credit:         decimal.RequireFromString("150.00"),
	}
	if err := db.Create(&jeLine).Error; err != nil {
		t.Fatal(err)
	}

	le := models.LedgerEntry{
		CompanyID:      cid,
		JournalEntryID: je.ID,
		SourceType:     models.LedgerSourceBill,
		AccountID:      apAccID,
		PostingDate:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		DebitAmount:    decimal.Zero,
		CreditAmount:   decimal.RequireFromString("150.00"),
		Status:         models.LedgerEntryStatusActive,
	}
	if err := db.Create(&le).Error; err != nil {
		t.Fatal(err)
	}

	bill := models.Bill{
		CompanyID:      cid,
		BillNumber:     "BILL-VOID-CLR",
		VendorID:       vendor.ID,
		BillDate:       time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:         models.BillStatusPosted,
		CurrencyCode:   "CAD",
		Amount:         decimal.RequireFromString("150.00"),
		BalanceDue:     decimal.RequireFromString("150.00"),
		BalanceDueBase: decimal.RequireFromString("150.00"),
		JournalEntryID: &je.ID,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}

	if err := VoidBill(db, cid, bill.ID, "tester", nil); err != nil {
		t.Fatalf("VoidBill failed: %v", err)
	}

	var after models.Bill
	if err := db.First(&after, bill.ID).Error; err != nil {
		t.Fatal(err)
	}

	if !after.BalanceDue.IsZero() {
		t.Errorf("balance_due after void: want 0, got %s", after.BalanceDue)
	}
	if !after.BalanceDueBase.IsZero() {
		t.Errorf("balance_due_base after void: want 0, got %s", after.BalanceDueBase)
	}
	if after.Status != models.BillStatusVoided {
		t.Errorf("status after void: want voided, got %s", after.Status)
	}
}

// ── Test 4: Apply then Unapply restores balance_due_base ─────────────────────

// TestApplyUnapply_RestoresBalanceDueBase verifies that after applying a gateway
// payment transaction to an invoice and then unapplying it, both balance_due and
// balance_due_base are fully restored.
//
// Scenario: CAD invoice (amount=300, balance_due=300, balance_due_base=300).
// Apply a charge transaction of 100.
// After apply: balance_due=200, balance_due_base=200.
// Unapply: balance_due=300, balance_due_base=300.
func TestApplyUnapply_RestoresBalanceDueBase(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	_ = seedFIAccount(t, db, cid, "1010", models.RootAsset, models.DetailBank)
	arAccID := seedFIAccount(t, db, cid, "1200", models.RootAsset, models.DetailAccountsReceivable)

	cust := models.Customer{CompanyID: cid, Name: "Apply Cust"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	// Invoice: sent, base-currency CAD, balance_due=300, balance_due_base=300.
	inv := models.Invoice{
		CompanyID:            cid,
		InvoiceNumber:        "INV-APPLY-001",
		CustomerID:           cust.ID,
		InvoiceDate:          time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:               models.InvoiceStatusSent,
		CurrencyCode:         "CAD",
		Amount:               decimal.RequireFromString("300.00"),
		AmountBase:           decimal.RequireFromString("300.00"),
		BalanceDue:           decimal.RequireFromString("300.00"),
		BalanceDueBase:       decimal.RequireFromString("300.00"),
		Subtotal:             decimal.RequireFromString("300.00"),
		TaxTotal:             decimal.Zero,
		CustomerNameSnapshot: "Apply Cust",
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	// Gateway account.
	gw := models.PaymentGatewayAccount{
		CompanyID:    cid,
		DisplayName:  "Stripe",
		ProviderType: "stripe",
		IsActive:     true,
	}
	if err := db.Create(&gw).Error; err != nil {
		t.Fatal(err)
	}

	// Payment request linked to the invoice.
	pr := models.PaymentRequest{
		CompanyID:        cid,
		GatewayAccountID: gw.ID,
		InvoiceID:        &inv.ID,
		CustomerID:       &cust.ID,
		Amount:           decimal.RequireFromString("100.00"),
		CurrencyCode:     "CAD",
		Status:           models.PaymentRequestCreated,
		Description:      "Test",
	}
	if err := db.Create(&pr).Error; err != nil {
		t.Fatal(err)
	}

	// JE representing the posted payment transaction (Dr GW Clearing, Cr AR).
	postedJE := models.JournalEntry{
		CompanyID:  cid,
		EntryDate:  time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		JournalNo:  "GW-TXN-001",
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourcePayment,
	}
	if err := db.Create(&postedJE).Error; err != nil {
		t.Fatal(err)
	}
	postedJELine := models.JournalLine{
		CompanyID:      cid,
		JournalEntryID: postedJE.ID,
		AccountID:      arAccID,
		Debit:          decimal.Zero,
		Credit:         decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&postedJELine).Error; err != nil {
		t.Fatal(err)
	}

	// Payment transaction: charge, posted, not yet applied.
	txn := models.PaymentTransaction{
		CompanyID:            cid,
		GatewayAccountID:     gw.ID,
		PaymentRequestID:     &pr.ID,
		TransactionType:      models.TxnTypeCharge,
		Amount:               decimal.RequireFromString("100.00"),
		CurrencyCode:         "CAD",
		RawPayload:           []byte("{}"),
		PostedJournalEntryID: &postedJE.ID,
		// AppliedInvoiceID is nil — not yet applied
	}
	if err := db.Create(&txn).Error; err != nil {
		t.Fatal(err)
	}

	// ── Apply ──────────────────────────────────────────────────────────────────

	if err := ApplyPaymentTransactionToInvoice(db, cid, txn.ID, "tester"); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	var afterApply models.Invoice
	if err := db.First(&afterApply, inv.ID).Error; err != nil {
		t.Fatal(err)
	}

	wantAfterApply := decimal.RequireFromString("200.00")
	if !afterApply.BalanceDue.Equal(wantAfterApply) {
		t.Errorf("after apply: balance_due want %s, got %s", wantAfterApply, afterApply.BalanceDue)
	}
	if !afterApply.BalanceDueBase.Equal(wantAfterApply) {
		t.Errorf("after apply: balance_due_base want %s, got %s", wantAfterApply, afterApply.BalanceDueBase)
	}
	if afterApply.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("after apply: status want partially_paid, got %s", afterApply.Status)
	}

	// ── Unapply ────────────────────────────────────────────────────────────────

	if err := UnapplyPaymentTransaction(db, cid, txn.ID, "tester"); err != nil {
		t.Fatalf("Unapply failed: %v", err)
	}

	var afterUnapply models.Invoice
	if err := db.First(&afterUnapply, inv.ID).Error; err != nil {
		t.Fatal(err)
	}

	wantAfterUnapply := decimal.RequireFromString("300.00")
	if !afterUnapply.BalanceDue.Equal(wantAfterUnapply) {
		t.Errorf("after unapply: balance_due want %s, got %s", wantAfterUnapply, afterUnapply.BalanceDue)
	}
	if !afterUnapply.BalanceDueBase.Equal(wantAfterUnapply) {
		t.Errorf("after unapply: balance_due_base want %s, got %s (CRITICAL-3 not fixed)",
			wantAfterUnapply, afterUnapply.BalanceDueBase)
	}
	if afterUnapply.Status != models.InvoiceStatusIssued {
		t.Errorf("after unapply: status want issued, got %s", afterUnapply.Status)
	}
}

// ── Test 5: ApplyRefundTransactionToInvoice syncs balance_due_base ───────────

// TestRefundApply_SyncsBalanceDueBase verifies that applying a refund transaction
// to an invoice correctly increases both balance_due and balance_due_base.
//
// Scenario: CAD invoice (amount=300), fully paid (balance_due=0, balance_due_base=0).
// Apply a refund of 100. After: balance_due=100, balance_due_base=100.
func TestRefundApply_SyncsBalanceDueBase(t *testing.T) {
	db := testFinancialIntegrityDB(t)
	cid := seedFICompany(t, db, "CAD")
	_ = seedFIAccount(t, db, cid, "1010", models.RootAsset, models.DetailBank)
	arAccID := seedFIAccount(t, db, cid, "1200", models.RootAsset, models.DetailAccountsReceivable)

	cust := models.Customer{CompanyID: cid, Name: "Refund Cust"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	// Fully-paid invoice: balance_due=0, balance_due_base=0.
	inv := models.Invoice{
		CompanyID:            cid,
		InvoiceNumber:        "INV-REFUND-001",
		CustomerID:           cust.ID,
		InvoiceDate:          time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:               models.InvoiceStatusPaid,
		CurrencyCode:         "CAD",
		Amount:               decimal.RequireFromString("300.00"),
		AmountBase:           decimal.RequireFromString("300.00"),
		BalanceDue:           decimal.Zero,
		BalanceDueBase:       decimal.Zero,
		Subtotal:             decimal.RequireFromString("300.00"),
		TaxTotal:             decimal.Zero,
		CustomerNameSnapshot: "Refund Cust",
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	// Gateway account.
	gw := models.PaymentGatewayAccount{
		CompanyID:    cid,
		DisplayName:  "Stripe",
		ProviderType: "stripe",
		IsActive:     true,
	}
	if err := db.Create(&gw).Error; err != nil {
		t.Fatal(err)
	}

	// Payment request linked to the invoice.
	pr := models.PaymentRequest{
		CompanyID:        cid,
		GatewayAccountID: gw.ID,
		InvoiceID:        &inv.ID,
		CustomerID:       &cust.ID,
		Amount:           decimal.RequireFromString("100.00"),
		CurrencyCode:     "CAD",
		Status:           models.PaymentRequestCreated,
		Description:      "Refund test",
	}
	if err := db.Create(&pr).Error; err != nil {
		t.Fatal(err)
	}

	// Refund JE (Dr AR, Cr GW Clearing).
	refundJE := models.JournalEntry{
		CompanyID:  cid,
		EntryDate:  time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		JournalNo:  "GW-REFUND-001",
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourcePayment,
	}
	if err := db.Create(&refundJE).Error; err != nil {
		t.Fatal(err)
	}
	refundJELine := models.JournalLine{
		CompanyID:      cid,
		JournalEntryID: refundJE.ID,
		AccountID:      arAccID,
		Debit:          decimal.RequireFromString("100.00"),
		Credit:         decimal.Zero,
	}
	if err := db.Create(&refundJELine).Error; err != nil {
		t.Fatal(err)
	}

	// Refund transaction: not yet applied to invoice.
	refundTxn := models.PaymentTransaction{
		CompanyID:            cid,
		GatewayAccountID:     gw.ID,
		PaymentRequestID:     &pr.ID,
		TransactionType:      models.TxnTypeRefund,
		Amount:               decimal.RequireFromString("100.00"),
		CurrencyCode:         "CAD",
		RawPayload:           []byte("{}"),
		PostedJournalEntryID: &refundJE.ID,
		// AppliedInvoiceID nil — not yet applied
	}
	if err := db.Create(&refundTxn).Error; err != nil {
		t.Fatal(err)
	}

	if err := ApplyRefundTransactionToInvoice(db, cid, refundTxn.ID, "tester"); err != nil {
		t.Fatalf("ApplyRefundTransactionToInvoice failed: %v", err)
	}

	var after models.Invoice
	if err := db.First(&after, inv.ID).Error; err != nil {
		t.Fatal(err)
	}

	want := decimal.RequireFromString("100.00")
	if !after.BalanceDue.Equal(want) {
		t.Errorf("after refund apply: balance_due want %s, got %s", want, after.BalanceDue)
	}
	if !after.BalanceDueBase.Equal(want) {
		t.Errorf("after refund apply: balance_due_base want %s, got %s", want, after.BalanceDueBase)
	}
	if after.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("after refund apply: status want partially_paid, got %s", after.Status)
	}
}
