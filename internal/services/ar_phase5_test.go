// 遵循project_guide.md
package services

// ar_phase5_test.go — AR Phase 5: ARReturn lifecycle + ARRefund post/void/reverse tests.
//
// ARReturn tests:
//  1. Create draft return — no JE.
//  2. Submit → Approve → MarkProcessed state machine.
//  3. Submit → Reject.
//  4. Cancel from draft.
//  5. Cancel from submitted.
//  6. Cannot edit non-draft.
//  7. Company isolation.
//  8. Document numbering.
//
// ARRefund tests:
//  1. Create draft refund — no JE.
//  2. Post refund — creates Dr DebitAcct / Cr Bank JE; 2 ledger entries.
//  3. Cannot post without bank account.
//  4. Cannot post without debit account.
//  5. Void draft refund.
//  6. Cannot void posted refund.
//  7. Reverse posted refund — creates reversal JE; status=reversed.
//  8. Cannot reverse non-posted refund.
//  9. Company isolation.
// 10. Document numbering.

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func phase5DB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.ARReturn{},
		&models.ARRefund{},
		&models.ARAPControlMapping{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func p5Company(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{Name: "Phase5 Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p5Customer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer P5"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p5Invoice(t *testing.T, db *gorm.DB, companyID, custID uint) *models.Invoice {
	t.Helper()
	inv := models.Invoice{
		CompanyID:     companyID,
		CustomerID:    custID,
		InvoiceNumber: "INV-5001",
		InvoiceDate:   time.Now(),
		Status:        models.InvoiceStatusSent,
		Amount:        decimal.NewFromInt(300),
		Subtotal:      decimal.NewFromInt(300),
		BalanceDue:    decimal.NewFromInt(300),
		CurrencyCode:  "CAD",
		ExchangeRate:  decimal.NewFromInt(1),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return &inv
}

func p5BankAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              "1010",
		Name:              "Bank",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func p5ARAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              "1100",
		Name:              "Accounts Receivable",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailAccountsReceivable,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// ── ARReturn tests ────────────────────────────────────────────────────────────

func TestPhase5_CreateReturn(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	inv := p5Invoice(t, db, cid, custID)

	ret, err := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(100),
	})
	if err != nil {
		t.Fatalf("CreateARReturn: %v", err)
	}
	if ret.Status != models.ARReturnStatusDraft {
		t.Errorf("expected draft; got %s", ret.Status)
	}
	if ret.ReturnNumber == "" {
		t.Error("ReturnNumber must be set")
	}
	if ret.Reason != models.ARReturnReasonOther {
		t.Errorf("expected default reason 'other'; got %s", ret.Reason)
	}

	// No JE created.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Count(&jeCount)
	if jeCount != 0 {
		t.Errorf("expected 0 JEs after return creation; got %d", jeCount)
	}
}

func TestPhase5_CreateReturn_NoCustomer(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	inv := p5Invoice(t, db, cid, custID)

	_, err := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   0,
		InvoiceID:    inv.ID,
		ReturnAmount: decimal.NewFromInt(50),
	})
	if err == nil {
		t.Fatal("expected error for missing customer")
	}
}

func TestPhase5_ReturnStateMachine(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	inv := p5Invoice(t, db, cid, custID)

	ret, _ := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(100),
	})

	// draft → submitted
	if err := SubmitARReturn(db, cid, ret.ID); err != nil {
		t.Fatalf("SubmitARReturn: %v", err)
	}
	updated, _ := GetARReturn(db, cid, ret.ID)
	if updated.Status != models.ARReturnStatusSubmitted {
		t.Errorf("expected submitted; got %s", updated.Status)
	}

	// submitted → approved
	if err := ApproveARReturn(db, cid, ret.ID, "approver@test.com"); err != nil {
		t.Fatalf("ApproveARReturn: %v", err)
	}
	updated, _ = GetARReturn(db, cid, ret.ID)
	if updated.Status != models.ARReturnStatusApproved {
		t.Errorf("expected approved; got %s", updated.Status)
	}
	if updated.ApprovedBy != "approver@test.com" {
		t.Errorf("expected ApprovedBy set; got %q", updated.ApprovedBy)
	}
	if updated.ApprovedAt == nil {
		t.Error("ApprovedAt should be set")
	}

	// approved → processed
	if err := MarkReturnProcessed(db, cid, ret.ID); err != nil {
		t.Fatalf("MarkReturnProcessed: %v", err)
	}
	updated, _ = GetARReturn(db, cid, ret.ID)
	if updated.Status != models.ARReturnStatusProcessed {
		t.Errorf("expected processed; got %s", updated.Status)
	}
	if updated.ProcessedAt == nil {
		t.Error("ProcessedAt should be set")
	}
}

func TestPhase5_ReturnReject(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	inv := p5Invoice(t, db, cid, custID)

	ret, _ := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(50),
	})
	SubmitARReturn(db, cid, ret.ID)
	if err := RejectARReturn(db, cid, ret.ID); err != nil {
		t.Fatalf("RejectARReturn: %v", err)
	}
	updated, _ := GetARReturn(db, cid, ret.ID)
	if updated.Status != models.ARReturnStatusRejected {
		t.Errorf("expected rejected; got %s", updated.Status)
	}
}

func TestPhase5_ReturnCancelFromDraft(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	inv := p5Invoice(t, db, cid, custID)

	ret, _ := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(50),
	})
	if err := CancelARReturn(db, cid, ret.ID); err != nil {
		t.Fatalf("CancelARReturn from draft: %v", err)
	}
	updated, _ := GetARReturn(db, cid, ret.ID)
	if updated.Status != models.ARReturnStatusCancelled {
		t.Errorf("expected cancelled; got %s", updated.Status)
	}
}

func TestPhase5_ReturnCancelFromSubmitted(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	inv := p5Invoice(t, db, cid, custID)

	ret, _ := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(50),
	})
	SubmitARReturn(db, cid, ret.ID)
	if err := CancelARReturn(db, cid, ret.ID); err != nil {
		t.Fatalf("CancelARReturn from submitted: %v", err)
	}
	updated, _ := GetARReturn(db, cid, ret.ID)
	if updated.Status != models.ARReturnStatusCancelled {
		t.Errorf("expected cancelled; got %s", updated.Status)
	}
}

func TestPhase5_ReturnCannotEditNonDraft(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	inv := p5Invoice(t, db, cid, custID)

	ret, _ := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(50),
	})
	SubmitARReturn(db, cid, ret.ID)

	_, err := UpdateARReturn(db, cid, ret.ID, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(75),
	})
	if err == nil {
		t.Fatal("expected error editing submitted return")
	}
}

func TestPhase5_ReturnIsolation(t *testing.T) {
	db := phase5DB(t)
	cid1 := p5Company(t, db)
	cid2 := p5Company(t, db)
	custID := p5Customer(t, db, cid1)
	inv := p5Invoice(t, db, cid1, custID)

	ret, _ := CreateARReturn(db, cid1, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(50),
	})

	_, err := GetARReturn(db, cid2, ret.ID)
	if err != ErrReturnNotFound {
		t.Errorf("expected ErrReturnNotFound for wrong company; got %v", err)
	}
}

func TestPhase5_ReturnDocumentNumbering(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	inv := p5Invoice(t, db, cid, custID)

	r1, _ := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(50),
	})
	r2, _ := CreateARReturn(db, cid, ARReturnInput{
		CustomerID:   custID,
		InvoiceID:    inv.ID,
		ReturnDate:   time.Now(),
		CurrencyCode: "CAD",
		ReturnAmount: decimal.NewFromInt(60),
	})
	if r1.ReturnNumber != "RTN-0001" {
		t.Errorf("expected RTN-0001; got %s", r1.ReturnNumber)
	}
	if r2.ReturnNumber != "RTN-0002" {
		t.Errorf("expected RTN-0002; got %s", r2.ReturnNumber)
	}
}

// ── ARRefund tests ────────────────────────────────────────────────────────────

func TestPhase5_CreateRefund(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)

	ref, err := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:   custID,
		RefundDate:   time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(200),
	})
	if err != nil {
		t.Fatalf("CreateARRefund: %v", err)
	}
	if ref.Status != models.ARRefundStatusDraft {
		t.Errorf("expected draft; got %s", ref.Status)
	}
	if ref.RefundNumber == "" {
		t.Error("RefundNumber must be set")
	}

	// No JE.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Count(&jeCount)
	if jeCount != 0 {
		t.Errorf("expected 0 JEs; got %d", jeCount)
	}
}

func TestPhase5_PostRefund(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	bankID := p5BankAccount(t, db, cid)
	arID := p5ARAccount(t, db, cid)

	ref, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		RefundDate:    time.Now(),
		CurrencyCode:  "CAD",
		ExchangeRate:  decimal.NewFromInt(1),
		Amount:        decimal.NewFromInt(200),
	})

	if err := PostARRefund(db, cid, ref.ID, arID, "tester", nil); err != nil {
		t.Fatalf("PostARRefund: %v", err)
	}

	// Verify status.
	updated, _ := GetARRefund(db, cid, ref.ID)
	if updated.Status != models.ARRefundStatusPosted {
		t.Errorf("expected posted; got %s", updated.Status)
	}
	if updated.JournalEntryID == nil {
		t.Fatal("JournalEntryID must be set after posting")
	}

	// Verify JE lines: Dr AR / Cr Bank.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *updated.JournalEntryID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JE lines; got %d", len(lines))
	}

	var drLine, crLine *models.JournalLine
	for i := range lines {
		if lines[i].Debit.IsPositive() {
			drLine = &lines[i]
		}
		if lines[i].Credit.IsPositive() {
			crLine = &lines[i]
		}
	}
	if drLine == nil || crLine == nil {
		t.Fatal("must have one debit and one credit line")
	}
	if drLine.AccountID != arID {
		t.Errorf("Dr line should be AR account; got %d", drLine.AccountID)
	}
	if crLine.AccountID != bankID {
		t.Errorf("Cr line should be bank account; got %d", crLine.AccountID)
	}
	if !drLine.Debit.Equal(decimal.NewFromInt(200)) {
		t.Errorf("Dr amount should be 200; got %s", drLine.Debit)
	}

	// Verify 2 ledger entries.
	var leCount int64
	db.Model(&models.LedgerEntry{}).Where("journal_entry_id = ?", *updated.JournalEntryID).Count(&leCount)
	if leCount != 2 {
		t.Errorf("expected 2 ledger entries; got %d", leCount)
	}
}

func TestPhase5_PostRefund_NoBank(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	arID := p5ARAccount(t, db, cid)

	ref, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:   custID,
		RefundDate:   time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(100),
	})

	err := PostARRefund(db, cid, ref.ID, arID, "tester", nil)
	if err != ErrRefundNoBank {
		t.Errorf("expected ErrRefundNoBank; got %v", err)
	}
}

func TestPhase5_PostRefund_NoDebitAcct(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	bankID := p5BankAccount(t, db, cid)

	ref, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		RefundDate:    time.Now(),
		CurrencyCode:  "CAD",
		Amount:        decimal.NewFromInt(100),
	})

	err := PostARRefund(db, cid, ref.ID, 0, "tester", nil)
	if err != ErrRefundNoDebitAcct {
		t.Errorf("expected ErrRefundNoDebitAcct; got %v", err)
	}
}

func TestPhase5_VoidDraftRefund(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)

	ref, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:   custID,
		RefundDate:   time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(100),
	})

	if err := VoidARRefund(db, cid, ref.ID); err != nil {
		t.Fatalf("VoidARRefund: %v", err)
	}
	updated, _ := GetARRefund(db, cid, ref.ID)
	if updated.Status != models.ARRefundStatusVoided {
		t.Errorf("expected voided; got %s", updated.Status)
	}

	// No JE.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Count(&jeCount)
	if jeCount != 0 {
		t.Errorf("expected 0 JEs after void; got %d", jeCount)
	}
}

func TestPhase5_VoidPostedRefund_Fails(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	bankID := p5BankAccount(t, db, cid)
	arID := p5ARAccount(t, db, cid)

	ref, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		RefundDate:    time.Now(),
		CurrencyCode:  "CAD",
		Amount:        decimal.NewFromInt(100),
	})
	PostARRefund(db, cid, ref.ID, arID, "tester", nil)

	err := VoidARRefund(db, cid, ref.ID)
	if err == nil {
		t.Fatal("expected error voiding a posted refund")
	}
}

func TestPhase5_ReversePostedRefund(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)
	bankID := p5BankAccount(t, db, cid)
	arID := p5ARAccount(t, db, cid)

	ref, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		RefundDate:    time.Now(),
		CurrencyCode:  "CAD",
		ExchangeRate:  decimal.NewFromInt(1),
		Amount:        decimal.NewFromInt(150),
	})
	PostARRefund(db, cid, ref.ID, arID, "tester", nil)

	if err := ReverseARRefund(db, cid, ref.ID, "tester", nil); err != nil {
		t.Fatalf("ReverseARRefund: %v", err)
	}

	updated, _ := GetARRefund(db, cid, ref.ID)
	if updated.Status != models.ARRefundStatusReversed {
		t.Errorf("expected reversed; got %s", updated.Status)
	}

	// Should now have 2 JEs: original + reversal.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", cid).Count(&jeCount)
	if jeCount != 2 {
		t.Errorf("expected 2 JEs after reversal; got %d", jeCount)
	}
}

func TestPhase5_ReverseDraftRefund_Fails(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)

	ref, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:   custID,
		RefundDate:   time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(100),
	})

	err := ReverseARRefund(db, cid, ref.ID, "tester", nil)
	if err == nil {
		t.Fatal("expected error reversing a draft refund")
	}
}

func TestPhase5_RefundIsolation(t *testing.T) {
	db := phase5DB(t)
	cid1 := p5Company(t, db)
	cid2 := p5Company(t, db)
	custID := p5Customer(t, db, cid1)

	ref, _ := CreateARRefund(db, cid1, ARRefundInput{
		CustomerID:   custID,
		RefundDate:   time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(100),
	})

	_, err := GetARRefund(db, cid2, ref.ID)
	if err != ErrRefundNotFound {
		t.Errorf("expected ErrRefundNotFound for wrong company; got %v", err)
	}
}

func TestPhase5_RefundDocumentNumbering(t *testing.T) {
	db := phase5DB(t)
	cid := p5Company(t, db)
	custID := p5Customer(t, db, cid)

	r1, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:   custID,
		RefundDate:   time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(50),
	})
	r2, _ := CreateARRefund(db, cid, ARRefundInput{
		CustomerID:   custID,
		RefundDate:   time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(75),
	})
	if r1.RefundNumber != "RFD-0001" {
		t.Errorf("expected RFD-0001; got %s", r1.RefundNumber)
	}
	if r2.RefundNumber != "RFD-0002" {
		t.Errorf("expected RFD-0002; got %s", r2.RefundNumber)
	}
}
