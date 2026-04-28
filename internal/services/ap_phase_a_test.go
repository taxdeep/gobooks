// 遵循project_guide.md
package services

// ap_phase_a_test.go — AP Phase A tests.
//
// PurchaseOrder tests:
//  1. Create draft — no JE, status=draft.
//  2. Confirm draft → confirmed.
//  3. Cancel confirmed.
//  4. Cannot edit confirmed PO.
//  5. Document numbering (PO-0001, PO-0002).
//  6. Company isolation.
//
// VendorPrepayment tests:
//  7. Create draft — no JE.
//  8. Post — creates Dr PrepaymentAsset / Cr Bank JE + 2 ledger entries.
//  9. Post without bank account → ErrVendorPrepaymentNoBank.
// 10. Post without prepayment account → ErrVendorPrepaymentNoAcct.
// 11. Void draft.
// 12. Cannot void posted.
// 13. Document numbering (PP-0001, PP-0002).
//
// VendorReturn tests:
// 14. Create draft — no JE.
// 15. Submit draft → submitted.
// 16. Approve submitted → approved.
// 17. Process approved → processed.
// 18. Cancel draft.
// 19. Company isolation.
//
// VendorCreditNote tests:
// 20. Create draft — no JE.
// 21. Post — creates Dr AP / Cr PurchaseReturns JE.
// 22. Post without AP account → ErrVendorCreditNoteNoAPAcct.
// 23. Void draft.
//
// VendorRefund tests:
// 24. Create draft — no JE.
// 25. Post — creates Dr Bank / Cr CreditAccount JE.
// 26. Void draft.
// 27. Reverse posted.
//
// APAging tests:
// 28. Empty DB returns empty aging.
// 29. Overdue bill appears in correct bucket.
// 30. Current bill (not yet due) appears in Current bucket.

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func phaseADB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Vendor{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Bill{},
		&models.BillLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.PurchaseOrder{},
		&models.PurchaseOrderLine{},
		&models.VendorPrepayment{},
		&models.VendorReturn{},
		&models.VendorCreditNote{},
		&models.VendorCreditNoteLine{},
		&models.VendorRefund{},
		&models.APCreditApplication{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// phaseASetup creates a company, two vendors, and accounting accounts.
func phaseASetup(t *testing.T, db *gorm.DB) (companyID uint, vendorA uint, vendorB uint, bankAcct uint, prepayAcct uint, apAcct uint, offsetAcct uint) {
	t.Helper()

	co := models.Company{Name: "Test Co AP"}
	db.Create(&co)
	companyID = co.ID

	va := models.Vendor{CompanyID: companyID, Name: "Vendor Alpha"}
	db.Create(&va)
	vendorA = va.ID

	vb := models.Vendor{CompanyID: companyID, Name: "Vendor Beta"}
	db.Create(&vb)
	vendorB = vb.ID

	bank := models.Account{CompanyID: companyID, Code: "1010", Name: "Bank", RootAccountType: models.RootAsset, IsActive: true}
	db.Create(&bank)
	bankAcct = bank.ID

	prepay := models.Account{CompanyID: companyID, Code: "1080", Name: "Vendor Prepayments", RootAccountType: models.RootAsset, IsActive: true}
	db.Create(&prepay)
	prepayAcct = prepay.ID

	ap := models.Account{CompanyID: companyID, Code: "2010", Name: "Accounts Payable", RootAccountType: models.RootLiability, IsActive: true}
	db.Create(&ap)
	apAcct = ap.ID

	offset := models.Account{CompanyID: companyID, Code: "5090", Name: "Purchase Returns", RootAccountType: models.RootExpense, IsActive: true}
	db.Create(&offset)
	offsetAcct = offset.ID

	return
}

// ── PurchaseOrder tests ───────────────────────────────────────────────────────

func TestPO_CreateDraft(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	po, err := CreatePurchaseOrder(db, companyID, POInput{
		VendorID: vendorA,
		PODate:   time.Now(),
		Lines: []POLineInput{
			{Description: "Widgets", Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(5)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if po.Status != models.POStatusDraft {
		t.Errorf("expected draft, got %s", po.Status)
	}
	if po.PONumber == "" {
		t.Error("PONumber should not be empty")
	}
	// Verify no JE created
	var count int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", companyID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs, got %d", count)
	}
	// Verify totals
	if !po.Amount.Equal(decimal.NewFromInt(50)) {
		t.Errorf("expected amount=50, got %s", po.Amount)
	}
}

func TestPO_ConfirmAndCancel(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	po, _ := CreatePurchaseOrder(db, companyID, POInput{
		VendorID: vendorA,
		PODate:   time.Now(),
		Lines:    []POLineInput{{Description: "Item", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)}},
	})

	if err := ConfirmPurchaseOrder(db, companyID, po.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ := GetPurchaseOrder(db, companyID, po.ID)
	if loaded.Status != models.POStatusConfirmed {
		t.Errorf("expected confirmed, got %s", loaded.Status)
	}

	if err := CancelPurchaseOrder(db, companyID, po.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ = GetPurchaseOrder(db, companyID, po.ID)
	if loaded.Status != models.POStatusCancelled {
		t.Errorf("expected cancelled, got %s", loaded.Status)
	}
}

func TestPO_CannotEditConfirmed(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	po, _ := CreatePurchaseOrder(db, companyID, POInput{
		VendorID: vendorA,
		PODate:   time.Now(),
		Lines:    []POLineInput{{Description: "Item", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100)}},
	})
	_ = ConfirmPurchaseOrder(db, companyID, po.ID)

	_, err := UpdatePurchaseOrder(db, companyID, po.ID, POInput{
		VendorID: vendorA,
		PODate:   time.Now(),
		Lines:    []POLineInput{{Description: "Changed", Qty: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(50)}},
	})
	if err == nil {
		t.Error("expected error editing confirmed PO")
	}
}

func TestPO_Numbering(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	line := []POLineInput{{Description: "Item", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)}}
	po1, _ := CreatePurchaseOrder(db, companyID, POInput{VendorID: vendorA, PODate: time.Now(), Lines: line})
	po2, _ := CreatePurchaseOrder(db, companyID, POInput{VendorID: vendorA, PODate: time.Now(), Lines: line})

	if po1.PONumber != "PO-0001" {
		t.Errorf("expected PO-0001, got %s", po1.PONumber)
	}
	if po2.PONumber != "PO-0002" {
		t.Errorf("expected PO-0002, got %s", po2.PONumber)
	}
}

func TestPO_CompanyIsolation(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	co2 := models.Company{Name: "Other Co"}
	db.Create(&co2)
	vendorOther := models.Vendor{CompanyID: co2.ID, Name: "Other Vendor"}
	db.Create(&vendorOther)

	po, _ := CreatePurchaseOrder(db, companyID, POInput{
		VendorID: vendorA,
		PODate:   time.Now(),
		Lines:    []POLineInput{{Description: "Item", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10)}},
	})

	// Try to load from other company
	_, err := GetPurchaseOrder(db, co2.ID, po.ID)
	if err != ErrPONotFound {
		t.Errorf("expected ErrPONotFound, got %v", err)
	}
}

// ── VendorPrepayment tests ────────────────────────────────────────────────────

func TestVendorPrepayment_CreateDraft(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	pp, err := CreateVendorPrepayment(db, companyID, VendorPrepaymentInput{
		VendorID:       vendorA,
		PrepaymentDate: time.Now(),
		Amount:         decimal.NewFromInt(500),
	})
	if err != nil {
		t.Fatal(err)
	}
	if pp.Status != models.VendorPrepaymentStatusDraft {
		t.Errorf("expected draft, got %s", pp.Status)
	}
	if pp.PrepaymentNumber == "" {
		t.Error("PrepaymentNumber should not be empty")
	}
	// No JE
	var count int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", companyID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs, got %d", count)
	}
}

func TestVendorPrepayment_Post(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, bankAcct, prepayAcct, _, _ := phaseASetup(t, db)

	pp, _ := CreateVendorPrepayment(db, companyID, VendorPrepaymentInput{
		VendorID:            vendorA,
		PrepaymentDate:      time.Now(),
		Amount:              decimal.NewFromInt(500),
		BankAccountID:       &bankAcct,
		PrepaymentAccountID: &prepayAcct,
	})

	if err := PostVendorPrepayment(db, companyID, pp.ID, "tester", nil); err != nil {
		t.Fatal(err)
	}

	loaded, _ := GetVendorPrepayment(db, companyID, pp.ID)
	if loaded.Status != models.VendorPrepaymentStatusPosted {
		t.Errorf("expected posted, got %s", loaded.Status)
	}
	if loaded.AmountBase.IsZero() {
		t.Error("AmountBase should be set after posting")
	}

	// Verify JE: 2 lines
	var je models.JournalEntry
	db.Where("source_type = ? AND source_id = ?", models.LedgerSourceVendorPrepayment, pp.ID).First(&je)
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&lines)
	if len(lines) != 2 {
		t.Errorf("expected 2 JE lines, got %d", len(lines))
	}

	// Dr PrepaymentAsset, Cr Bank
	var drLine, crLine models.JournalLine
	for _, l := range lines {
		if l.Debit.IsPositive() {
			drLine = l
		} else {
			crLine = l
		}
	}
	if drLine.AccountID != prepayAcct {
		t.Errorf("expected Dr prepay account %d, got %d", prepayAcct, drLine.AccountID)
	}
	if crLine.AccountID != bankAcct {
		t.Errorf("expected Cr bank account %d, got %d", bankAcct, crLine.AccountID)
	}
	if !drLine.Debit.Equal(decimal.NewFromInt(500)) {
		t.Errorf("expected Dr 500, got %s", drLine.Debit)
	}

	// Verify ledger entries
	var ledgerCount int64
	db.Model(&models.LedgerEntry{}).Where("company_id = ? AND source_type = ?", companyID, models.LedgerSourceVendorPrepayment).Count(&ledgerCount)
	if ledgerCount != 2 {
		t.Errorf("expected 2 ledger entries, got %d", ledgerCount)
	}
}

func TestVendorPrepayment_PostNoBank(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, prepayAcct, _, _ := phaseASetup(t, db)

	pp, _ := CreateVendorPrepayment(db, companyID, VendorPrepaymentInput{
		VendorID:            vendorA,
		PrepaymentDate:      time.Now(),
		Amount:              decimal.NewFromInt(500),
		PrepaymentAccountID: &prepayAcct,
	})

	if err := PostVendorPrepayment(db, companyID, pp.ID, "tester", nil); err != ErrVendorPrepaymentNoBank {
		t.Errorf("expected ErrVendorPrepaymentNoBank, got %v", err)
	}
}

func TestVendorPrepayment_PostNoAcct(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, bankAcct, _, _, _ := phaseASetup(t, db)

	pp, _ := CreateVendorPrepayment(db, companyID, VendorPrepaymentInput{
		VendorID:       vendorA,
		PrepaymentDate: time.Now(),
		Amount:         decimal.NewFromInt(500),
		BankAccountID:  &bankAcct,
	})

	if err := PostVendorPrepayment(db, companyID, pp.ID, "tester", nil); err != ErrVendorPrepaymentNoAcct {
		t.Errorf("expected ErrVendorPrepaymentNoAcct, got %v", err)
	}
}

func TestVendorPrepayment_VoidDraft(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	pp, _ := CreateVendorPrepayment(db, companyID, VendorPrepaymentInput{
		VendorID: vendorA, PrepaymentDate: time.Now(), Amount: decimal.NewFromInt(100),
	})

	if err := VoidVendorPrepayment(db, companyID, pp.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ := GetVendorPrepayment(db, companyID, pp.ID)
	if loaded.Status != models.VendorPrepaymentStatusVoided {
		t.Errorf("expected voided, got %s", loaded.Status)
	}
}

func TestVendorPrepayment_CannotVoidPosted(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, bankAcct, prepayAcct, _, _ := phaseASetup(t, db)

	pp, _ := CreateVendorPrepayment(db, companyID, VendorPrepaymentInput{
		VendorID: vendorA, PrepaymentDate: time.Now(), Amount: decimal.NewFromInt(100),
		BankAccountID: &bankAcct, PrepaymentAccountID: &prepayAcct,
	})
	_ = PostVendorPrepayment(db, companyID, pp.ID, "tester", nil)

	if err := VoidVendorPrepayment(db, companyID, pp.ID); err == nil {
		t.Error("expected error voiding posted prepayment")
	}
}

func TestVendorPrepayment_Numbering(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	pp1, _ := CreateVendorPrepayment(db, companyID, VendorPrepaymentInput{VendorID: vendorA, PrepaymentDate: time.Now(), Amount: decimal.NewFromInt(1)})
	pp2, _ := CreateVendorPrepayment(db, companyID, VendorPrepaymentInput{VendorID: vendorA, PrepaymentDate: time.Now(), Amount: decimal.NewFromInt(1)})

	if pp1.PrepaymentNumber != "PP-0001" {
		t.Errorf("expected PP-0001, got %s", pp1.PrepaymentNumber)
	}
	if pp2.PrepaymentNumber != "PP-0002" {
		t.Errorf("expected PP-0002, got %s", pp2.PrepaymentNumber)
	}
}

// ── VendorReturn tests ────────────────────────────────────────────────────────

func TestVendorReturn_StateMachine(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	vr, err := CreateVendorReturn(db, companyID, VendorReturnInput{
		VendorID: vendorA, ReturnDate: time.Now(), Amount: decimal.NewFromInt(50), Reason: "Defective",
	})
	if err != nil {
		t.Fatal(err)
	}
	if vr.Status != models.VendorReturnStatusDraft {
		t.Errorf("expected draft, got %s", vr.Status)
	}

	// No JE at any stage
	var count int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", companyID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs, got %d", count)
	}

	_ = SubmitVendorReturn(db, companyID, vr.ID)
	_ = ApproveVendorReturn(db, companyID, vr.ID)
	_ = ProcessVendorReturn(db, companyID, vr.ID)

	loaded, _ := GetVendorReturn(db, companyID, vr.ID)
	if loaded.Status != models.VendorReturnStatusProcessed {
		t.Errorf("expected processed, got %s", loaded.Status)
	}

	// Still no JE
	db.Model(&models.JournalEntry{}).Where("company_id = ?", companyID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs after full state machine, got %d", count)
	}
}

func TestVendorReturn_Cancel(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	vr, _ := CreateVendorReturn(db, companyID, VendorReturnInput{
		VendorID: vendorA, ReturnDate: time.Now(), Amount: decimal.NewFromInt(10),
	})
	_ = SubmitVendorReturn(db, companyID, vr.ID)
	if err := CancelVendorReturn(db, companyID, vr.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ := GetVendorReturn(db, companyID, vr.ID)
	if loaded.Status != models.VendorReturnStatusCancelled {
		t.Errorf("expected cancelled, got %s", loaded.Status)
	}
}

func TestVendorReturn_CompanyIsolation(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)
	co2 := models.Company{Name: "Other Co"}
	db.Create(&co2)

	vr, _ := CreateVendorReturn(db, companyID, VendorReturnInput{
		VendorID: vendorA, ReturnDate: time.Now(), Amount: decimal.NewFromInt(10),
	})

	_, err := GetVendorReturn(db, co2.ID, vr.ID)
	if err != ErrVendorReturnNotFound {
		t.Errorf("expected ErrVendorReturnNotFound, got %v", err)
	}
}

// ── VendorCreditNote tests ────────────────────────────────────────────────────

func TestVendorCreditNote_Post(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, apAcct, offsetAcct := phaseASetup(t, db)

	vcn, err := CreateVendorCreditNote(db, companyID, VendorCreditNoteInput{
		VendorID:        vendorA,
		CreditNoteDate:  time.Now(),
		Amount:          decimal.NewFromInt(200),
		APAccountID:     &apAcct,
		OffsetAccountID: &offsetAcct,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := PostVendorCreditNote(db, companyID, vcn.ID, "tester", nil); err != nil {
		t.Fatal(err)
	}

	loaded, _ := GetVendorCreditNote(db, companyID, vcn.ID)
	if loaded.Status != models.VendorCreditNoteStatusPosted {
		t.Errorf("expected posted, got %s", loaded.Status)
	}

	// Verify JE: Dr AP / Cr PurchaseReturns
	var lines []models.JournalLine
	db.Joins("JOIN journal_entries ON journal_lines.journal_entry_id = journal_entries.id").
		Where("journal_entries.source_type = ? AND journal_entries.source_id = ?", models.LedgerSourceVendorCreditNote, vcn.ID).
		Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JE lines, got %d", len(lines))
	}

	var drLine, crLine models.JournalLine
	for _, l := range lines {
		if l.Debit.IsPositive() {
			drLine = l
		} else {
			crLine = l
		}
	}
	if drLine.AccountID != apAcct {
		t.Errorf("expected Dr AP account %d, got %d", apAcct, drLine.AccountID)
	}
	if crLine.AccountID != offsetAcct {
		t.Errorf("expected Cr offset account %d, got %d", offsetAcct, crLine.AccountID)
	}
}

func TestVendorCreditNote_PostNoAPAcct(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, offsetAcct := phaseASetup(t, db)

	vcn, _ := CreateVendorCreditNote(db, companyID, VendorCreditNoteInput{
		VendorID: vendorA, CreditNoteDate: time.Now(), Amount: decimal.NewFromInt(100),
		OffsetAccountID: &offsetAcct,
	})

	if err := PostVendorCreditNote(db, companyID, vcn.ID, "tester", nil); err != ErrVendorCreditNoteNoAPAcct {
		t.Errorf("expected ErrVendorCreditNoteNoAPAcct, got %v", err)
	}
}

func TestVendorCreditNote_VoidDraft(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	vcn, _ := CreateVendorCreditNote(db, companyID, VendorCreditNoteInput{
		VendorID: vendorA, CreditNoteDate: time.Now(), Amount: decimal.NewFromInt(50),
	})

	if err := VoidVendorCreditNote(db, companyID, vcn.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ := GetVendorCreditNote(db, companyID, vcn.ID)
	if loaded.Status != models.VendorCreditNoteStatusVoided {
		t.Errorf("expected voided, got %s", loaded.Status)
	}
}

// ── VendorRefund tests ────────────────────────────────────────────────────────

func TestVendorRefund_Post(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, bankAcct, prepayAcct, _, _ := phaseASetup(t, db)

	vrf, err := CreateVendorRefund(db, companyID, VendorRefundInput{
		VendorID:        vendorA,
		SourceType:      models.VendorRefundSourcePrepayment,
		RefundDate:      time.Now(),
		Amount:          decimal.NewFromInt(300),
		BankAccountID:   &bankAcct,
		CreditAccountID: &prepayAcct,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := PostVendorRefund(db, companyID, vrf.ID, "tester", nil); err != nil {
		t.Fatal(err)
	}

	loaded, _ := GetVendorRefund(db, companyID, vrf.ID)
	if loaded.Status != models.VendorRefundStatusPosted {
		t.Errorf("expected posted, got %s", loaded.Status)
	}

	// Verify JE: Dr Bank / Cr CreditAccount
	var lines []models.JournalLine
	db.Joins("JOIN journal_entries ON journal_lines.journal_entry_id = journal_entries.id").
		Where("journal_entries.source_type = ? AND journal_entries.source_id = ?", models.LedgerSourceVendorRefund, vrf.ID).
		Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JE lines, got %d", len(lines))
	}

	var drLine models.JournalLine
	for _, l := range lines {
		if l.Debit.IsPositive() {
			drLine = l
		}
	}
	if drLine.AccountID != bankAcct {
		t.Errorf("expected Dr bank account %d, got %d", bankAcct, drLine.AccountID)
	}
}

func TestVendorRefund_VoidAndReverse(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, bankAcct, prepayAcct, _, _ := phaseASetup(t, db)

	// Test void draft
	vrf1, _ := CreateVendorRefund(db, companyID, VendorRefundInput{
		VendorID: vendorA, RefundDate: time.Now(), Amount: decimal.NewFromInt(50),
	})
	if err := VoidVendorRefund(db, companyID, vrf1.ID); err != nil {
		t.Fatal(err)
	}

	// Test reverse posted
	vrf2, _ := CreateVendorRefund(db, companyID, VendorRefundInput{
		VendorID: vendorA, RefundDate: time.Now(), Amount: decimal.NewFromInt(100),
		BankAccountID: &bankAcct, CreditAccountID: &prepayAcct,
	})
	_ = PostVendorRefund(db, companyID, vrf2.ID, "tester", nil)
	if err := ReverseVendorRefund(db, companyID, vrf2.ID, "tester", nil); err != nil {
		t.Fatal(err)
	}
	loaded, _ := GetVendorRefund(db, companyID, vrf2.ID)
	if loaded.Status != models.VendorRefundStatusReversed {
		t.Errorf("expected reversed, got %s", loaded.Status)
	}
}

// ── APAging tests ─────────────────────────────────────────────────────────────

func TestAPAging_Empty(t *testing.T) {
	db := phaseADB(t)
	companyID, _, _, _, _, _, _ := phaseASetup(t, db)

	aging, err := GetAPAging(db, companyID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(aging.Lines) != 0 {
		t.Errorf("expected 0 lines, got %d", len(aging.Lines))
	}
	if !aging.GrandTotal.IsZero() {
		t.Errorf("expected zero grand total, got %s", aging.GrandTotal)
	}
}

func TestAPAging_Buckets(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, vendorB, _, _, _, _ := phaseASetup(t, db)

	asOf := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)

	// Vendor A: bill overdue 45 days (Days31_60 bucket)
	due45 := asOf.AddDate(0, 0, -45)
	billA := models.Bill{
		CompanyID:  companyID,
		VendorID:   vendorA,
		BillNumber: "B001",
		BillDate:   due45,
		DueDate:    &due45,
		Status:     models.BillStatusPosted,
		BalanceDue: decimal.NewFromInt(400),
		Amount:     decimal.NewFromInt(400),
	}
	db.Create(&billA)

	// Vendor B: bill not yet due (Current bucket)
	dueFuture := asOf.AddDate(0, 0, 10)
	billB := models.Bill{
		CompanyID:  companyID,
		VendorID:   vendorB,
		BillNumber: "B002",
		BillDate:   asOf.AddDate(0, 0, -5),
		DueDate:    &dueFuture,
		Status:     models.BillStatusPartiallyPaid,
		BalanceDue: decimal.NewFromInt(250),
		Amount:     decimal.NewFromInt(500),
	}
	db.Create(&billB)

	aging, err := GetAPAging(db, companyID, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(aging.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(aging.Lines))
	}

	// Find vendor A line
	var lineA, lineB APAgedLine
	for _, l := range aging.Lines {
		if l.Vendor.ID == vendorA {
			lineA = l
		} else {
			lineB = l
		}
	}

	if !lineA.Days31_60.Equal(decimal.NewFromInt(400)) {
		t.Errorf("Vendor A: expected Days31_60=400, got %s", lineA.Days31_60)
	}
	if !lineA.Current.IsZero() {
		t.Errorf("Vendor A: expected Current=0, got %s", lineA.Current)
	}
	if !lineB.Current.Equal(decimal.NewFromInt(250)) {
		t.Errorf("Vendor B: expected Current=250, got %s", lineB.Current)
	}
	if !aging.GrandTotal.Equal(decimal.NewFromInt(650)) {
		t.Errorf("expected GrandTotal=650, got %s", aging.GrandTotal)
	}
}

func TestAPAging_ExcludesPaidAndDraft(t *testing.T) {
	db := phaseADB(t)
	companyID, vendorA, _, _, _, _, _ := phaseASetup(t, db)

	past := time.Now().AddDate(0, 0, -10)
	// Draft bill — should NOT appear
	billDraft := models.Bill{
		CompanyID:  companyID,
		VendorID:   vendorA,
		BillNumber: "BD01",
		BillDate:   past,
		DueDate:    &past,
		Status:     models.BillStatusDraft,
		BalanceDue: decimal.NewFromInt(100),
		Amount:     decimal.NewFromInt(100),
	}
	db.Create(&billDraft)

	// Paid bill — should NOT appear
	billPaid := models.Bill{
		CompanyID:  companyID,
		VendorID:   vendorA,
		BillNumber: "BP01",
		BillDate:   past,
		DueDate:    &past,
		Status:     models.BillStatusPaid,
		BalanceDue: decimal.Zero,
		Amount:     decimal.NewFromInt(200),
	}
	db.Create(&billPaid)

	aging, _ := GetAPAging(db, companyID, time.Now())
	if len(aging.Lines) != 0 {
		t.Errorf("expected 0 lines (paid+draft excluded), got %d", len(aging.Lines))
	}
}
