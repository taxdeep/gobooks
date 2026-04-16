// 遵循project_guide.md
package services

// vendor_credit_note_service.go — VendorCreditNote: vendor-issued credit reducing AP.
//
// Accounting rules:
//
//   Post (draft → posted):
//     Dr  APAccountID       AmountBase   (reduces AP liability)
//     Cr  OffsetAccountID   AmountBase   (purchase returns / adjustments)
//
//   Void (draft only):
//     Sets status=voided; no JE.
//
// State machine:
//   draft → posted → partially_applied → fully_applied
//          ↘ voided (from draft only)

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrVendorCreditNoteNotFound      = errors.New("vendor credit note not found")
	ErrVendorCreditNoteInvalidStatus = errors.New("action not allowed in current vendor credit note status")
	ErrVendorCreditNoteNoAPAcct      = errors.New("AP account is required before posting")
	ErrVendorCreditNoteNoOffsetAcct  = errors.New("offset account is required before posting")
	ErrVCNApplyAmountExceedsBalance  = errors.New("amount to apply exceeds vendor credit note remaining balance")
	ErrVCNApplyAmountExceedsBill     = errors.New("amount to apply exceeds bill balance due")
	ErrVCNVendorMismatch             = errors.New("vendor credit note and bill must belong to the same vendor")
	ErrVCNBillNotOpen                = errors.New("bill must be posted or partially paid to apply a credit")
)

// ── Input types ───────────────────────────────────────────────────────────────

// VendorCreditNoteInput holds all data needed to create or update a VendorCreditNote.
type VendorCreditNoteInput struct {
	VendorID       uint
	BillID         *uint
	VendorReturnID *uint
	CreditNoteDate time.Time
	CurrencyCode   string
	ExchangeRate   decimal.Decimal
	Amount         decimal.Decimal
	APAccountID    *uint
	OffsetAccountID *uint
	Reason         string
	Memo           string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func nextVendorCreditNoteNumber(db *gorm.DB, companyID uint) string {
	var last models.VendorCreditNote
	db.Where("company_id = ?", companyID).Order("id desc").Select("credit_note_number").First(&last)
	return NextDocumentNumber(last.CreditNoteNumber, "VCN-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateVendorCreditNote creates a new draft vendor credit note.
func CreateVendorCreditNote(db *gorm.DB, companyID uint, in VendorCreditNoteInput) (*models.VendorCreditNote, error) {
	if in.VendorID == 0 {
		return nil, errors.New("vendor is required")
	}
	if !in.Amount.IsPositive() {
		return nil, errors.New("credit note amount must be positive")
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	vcn := models.VendorCreditNote{
		CompanyID:        companyID,
		VendorID:         in.VendorID,
		BillID:           in.BillID,
		VendorReturnID:   in.VendorReturnID,
		CreditNoteNumber: nextVendorCreditNoteNumber(db, companyID),
		Status:           models.VendorCreditNoteStatusDraft,
		CreditNoteDate:   in.CreditNoteDate,
		CurrencyCode:     in.CurrencyCode,
		ExchangeRate:     rate,
		Amount:           in.Amount.Round(2),
		RemainingAmount:  in.Amount.Round(2),
		APAccountID:      in.APAccountID,
		OffsetAccountID:  in.OffsetAccountID,
		Reason:           in.Reason,
		Memo:             in.Memo,
	}

	if err := db.Create(&vcn).Error; err != nil {
		return nil, fmt.Errorf("create vendor credit note: %w", err)
	}
	return &vcn, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetVendorCreditNote loads a credit note with vendor, accounts, and applications for the given company.
func GetVendorCreditNote(db *gorm.DB, companyID, vcnID uint) (*models.VendorCreditNote, error) {
	var vcn models.VendorCreditNote
	err := db.Preload("Vendor").Preload("APAccount").Preload("OffsetAccount").
		Preload("Bill").Preload("VendorReturn").
		Preload("Applications").Preload("Applications.Bill").
		Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVendorCreditNoteNotFound
	}
	return &vcn, err
}

// ListVendorCreditNotes returns credit notes for a company, newest first.
func ListVendorCreditNotes(db *gorm.DB, companyID uint, statusFilter string, vendorID uint) ([]models.VendorCreditNote, error) {
	q := db.Preload("Vendor").Where("company_id = ?", companyID)
	if statusFilter != "" {
		q = q.Where("status = ?", statusFilter)
	}
	if vendorID > 0 {
		q = q.Where("vendor_id = ?", vendorID)
	}
	var vcns []models.VendorCreditNote
	err := q.Order("id desc").Find(&vcns).Error
	return vcns, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateVendorCreditNote updates a draft vendor credit note.
func UpdateVendorCreditNote(db *gorm.DB, companyID, vcnID uint, in VendorCreditNoteInput) (*models.VendorCreditNote, error) {
	var vcn models.VendorCreditNote
	if err := db.Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVendorCreditNoteNotFound
		}
		return nil, err
	}
	if vcn.Status != models.VendorCreditNoteStatusDraft {
		return nil, fmt.Errorf("%w: only draft credit notes may be edited", ErrVendorCreditNoteInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	updates := map[string]any{
		"vendor_id":        in.VendorID,
		"bill_id":          in.BillID,
		"vendor_return_id": in.VendorReturnID,
		"credit_note_date": in.CreditNoteDate,
		"currency_code":    in.CurrencyCode,
		"exchange_rate":    rate,
		"amount":           in.Amount.Round(2),
		"remaining_amount": in.Amount.Round(2),
		"ap_account_id":    in.APAccountID,
		"offset_account_id": in.OffsetAccountID,
		"reason":           in.Reason,
		"memo":             in.Memo,
	}
	if err := db.Model(&vcn).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update vendor credit note: %w", err)
	}
	return &vcn, nil
}

// ── Post ──────────────────────────────────────────────────────────────────────

// PostVendorCreditNote transitions a draft credit note to posted and generates a JE.
//
// Journal entry:
//
//	Dr  APAccountID       AmountBase   (reduces AP liability)
//	Cr  OffsetAccountID   AmountBase   (purchase returns / adjustments)
func PostVendorCreditNote(db *gorm.DB, companyID, vcnID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var vcn models.VendorCreditNote
		if err := tx.Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrVendorCreditNoteNotFound
			}
			return err
		}
		if vcn.Status != models.VendorCreditNoteStatusDraft {
			return fmt.Errorf("%w: only draft credit notes can be posted", ErrVendorCreditNoteInvalidStatus)
		}
		if vcn.APAccountID == nil || *vcn.APAccountID == 0 {
			return ErrVendorCreditNoteNoAPAcct
		}
		if vcn.OffsetAccountID == nil || *vcn.OffsetAccountID == 0 {
			return ErrVendorCreditNoteNoOffsetAcct
		}

		rate := vcn.ExchangeRate
		if rate.IsZero() || rate.IsNegative() {
			rate = decimal.NewFromInt(1)
		}
		amountBase := vcn.Amount.Mul(rate).Round(2)

		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  vcn.CreditNoteDate,
			JournalNo:  "VCN – " + vcn.CreditNoteNumber,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceVendorCreditNote,
			SourceID:   vcn.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create credit note JE: %w", err)
		}

		lines := []models.JournalLine{
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *vcn.APAccountID,
				Debit:          amountBase,
				Credit:         decimal.Zero,
				Memo:           vcn.CreditNoteNumber + " – AP reduction",
			},
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *vcn.OffsetAccountID,
				Debit:          decimal.Zero,
				Credit:         amountBase,
				Memo:           vcn.CreditNoteNumber + " – purchase return",
			},
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create credit note JE lines: %w", err)
		}

		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceVendorCreditNote,
			SourceID:     vcn.ID,
		}); err != nil {
			return fmt.Errorf("project credit note to ledger: %w", err)
		}

		now := time.Now()
		updates := map[string]any{
			"status":           string(models.VendorCreditNoteStatusPosted),
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"posted_at":        &now,
			"posted_by":        actor,
		}
		if actorID != nil {
			updates["posted_by_user_id"] = actorID
		}
		return tx.Model(&vcn).Updates(updates).Error
	})
}

// ── Void ──────────────────────────────────────────────────────────────────────

// VoidVendorCreditNote cancels a draft credit note. No JE is generated.
func VoidVendorCreditNote(db *gorm.DB, companyID, vcnID uint) error {
	var vcn models.VendorCreditNote
	if err := db.Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVendorCreditNoteNotFound
		}
		return err
	}
	if vcn.Status != models.VendorCreditNoteStatusDraft {
		return fmt.Errorf("%w: only draft credit notes can be voided", ErrVendorCreditNoteInvalidStatus)
	}
	return db.Model(&vcn).Update("status", models.VendorCreditNoteStatusVoided).Error
}

// ── Apply to Bill ─────────────────────────────────────────────────────────────

// ApplyVendorCreditNoteToBill applies a portion of a posted vendor credit note
// against an open bill, reducing the bill's BalanceDue.
//
// No new JE is created — the accounting reduction to AP already occurred when
// the credit note was posted (Dr AP / Cr Purchase Returns). This operation is
// purely an AP open-item allocation.
//
// Rules:
//   - VCN must be posted or partially_applied
//   - Bill must be posted or partially_paid (open)
//   - VCN.VendorID must match Bill.VendorID
//   - amountToApply must be ≤ VCN.RemainingAmount and ≤ Bill.BalanceDue
func ApplyVendorCreditNoteToBill(db *gorm.DB, companyID, vcnID, billID uint, amountToApply decimal.Decimal) error {
	if !amountToApply.IsPositive() {
		return errors.New("amount to apply must be positive")
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var vcn models.VendorCreditNote
		if err := tx.Where("id = ? AND company_id = ?", vcnID, companyID).First(&vcn).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrVendorCreditNoteNotFound
			}
			return err
		}
		if vcn.Status != models.VendorCreditNoteStatusPosted &&
			vcn.Status != models.VendorCreditNoteStatusPartiallyApplied {
			return fmt.Errorf("%w: current status is %s", ErrVendorCreditNoteInvalidStatus, vcn.Status)
		}
		if amountToApply.GreaterThan(vcn.RemainingAmount) {
			return fmt.Errorf("%w: applying %s but only %s remaining",
				ErrVCNApplyAmountExceedsBalance, amountToApply.StringFixed(2), vcn.RemainingAmount.StringFixed(2))
		}

		var bill models.Bill
		if err := tx.Where("id = ? AND company_id = ?", billID, companyID).First(&bill).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("bill not found")
			}
			return err
		}
		if bill.VendorID != vcn.VendorID {
			return ErrVCNVendorMismatch
		}
		if bill.Status != models.BillStatusPosted && bill.Status != models.BillStatusPartiallyPaid {
			return ErrVCNBillNotOpen
		}
		if amountToApply.GreaterThan(bill.BalanceDue) {
			return fmt.Errorf("%w: applying %s but bill balance is %s",
				ErrVCNApplyAmountExceedsBill, amountToApply.StringFixed(2), bill.BalanceDue.StringFixed(2))
		}

		// Compute base amount proportionally (using VCN's exchange rate).
		amountBase := amountToApply
		if vcn.Amount.GreaterThan(decimal.Zero) && vcn.AmountBase.GreaterThan(decimal.Zero) {
			ratio := amountToApply.Div(vcn.Amount)
			amountBase = vcn.AmountBase.Mul(ratio).Round(2)
		}

		now := time.Now()

		// 1. Create application record.
		app := models.APCreditApplication{
			CompanyID:          companyID,
			VendorCreditNoteID: vcnID,
			BillID:             billID,
			AmountApplied:      amountToApply,
			AmountAppliedBase:  amountBase,
			AppliedAt:          now,
		}
		if err := tx.Create(&app).Error; err != nil {
			return fmt.Errorf("create AP credit application: %w", err)
		}

		// 2. Update VCN remaining / applied amounts and status.
		newVCNRemaining := vcn.RemainingAmount.Sub(amountToApply)
		newVCNApplied := vcn.AppliedAmount.Add(amountToApply)
		newVCNStatus := models.VendorCreditNoteStatusPartiallyApplied
		if newVCNRemaining.IsZero() {
			newVCNStatus = models.VendorCreditNoteStatusFullyApplied
		}
		if err := tx.Model(&vcn).Updates(map[string]any{
			"remaining_amount": newVCNRemaining,
			"applied_amount":   newVCNApplied,
			"status":           string(newVCNStatus),
		}).Error; err != nil {
			return fmt.Errorf("update vendor credit note: %w", err)
		}

		// 3. Update Bill balance due and status.
		newBillBalance := bill.BalanceDue.Sub(amountToApply)
		newBillStatus := models.BillStatusPartiallyPaid
		if newBillBalance.IsZero() {
			newBillStatus = models.BillStatusPaid
		}
		if err := tx.Model(&bill).Updates(map[string]any{
			"balance_due": newBillBalance,
			"status":      string(newBillStatus),
		}).Error; err != nil {
			return fmt.Errorf("update bill: %w", err)
		}

		return nil
	})
}

// ListOpenBillsForVendor returns bills that can receive a credit application
// (status posted or partially_paid, balance_due > 0) for a given vendor.
func ListOpenBillsForVendor(db *gorm.DB, companyID, vendorID uint) ([]models.Bill, error) {
	var bills []models.Bill
	err := db.Where("company_id = ? AND vendor_id = ? AND status IN ? AND balance_due > 0",
		companyID, vendorID, []string{
			string(models.BillStatusPosted),
			string(models.BillStatusPartiallyPaid),
		}).
		Order("bill_date asc, id asc").
		Find(&bills).Error
	return bills, err
}

// ListVCNApplicationsForBill returns all AP credit applications for a given bill.
func ListVCNApplicationsForBill(db *gorm.DB, companyID, billID uint) ([]models.APCreditApplication, error) {
	var apps []models.APCreditApplication
	err := db.Where("company_id = ? AND bill_id = ?", companyID, billID).
		Order("applied_at asc").Find(&apps).Error
	return apps, err
}

// ReverseAPCreditApplication removes a single credit application, restoring
// the VCN's remaining balance and the bill's balance due.
// Only valid when neither the VCN nor the bill is voided.
func ReverseAPCreditApplication(db *gorm.DB, companyID, applicationID uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var app models.APCreditApplication
		if err := tx.Where("id = ? AND company_id = ?", applicationID, companyID).
			First(&app).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("credit application not found")
			}
			return fmt.Errorf("load application: %w", err)
		}

		var vcn models.VendorCreditNote
		if err := tx.Where("id = ? AND company_id = ?", app.VendorCreditNoteID, companyID).
			First(&vcn).Error; err != nil {
			return fmt.Errorf("load vendor credit note: %w", err)
		}
		if vcn.Status == models.VendorCreditNoteStatusVoided {
			return errors.New("cannot reverse: vendor credit note is voided")
		}

		var bill models.Bill
		if err := tx.Where("id = ? AND company_id = ?", app.BillID, companyID).
			First(&bill).Error; err != nil {
			return fmt.Errorf("load bill: %w", err)
		}
		if bill.Status == models.BillStatusVoided {
			return errors.New("cannot reverse: bill is voided")
		}

		// Restore VCN balance.
		newRemaining := vcn.RemainingAmount.Add(app.AmountApplied)
		newApplied := vcn.AppliedAmount.Sub(app.AmountApplied)
		if newApplied.IsNegative() {
			newApplied = decimal.Zero
		}
		newVCNStatus := models.VendorCreditNoteStatusPartiallyApplied
		if newApplied.IsZero() {
			newVCNStatus = models.VendorCreditNoteStatusPosted
		}
		if err := tx.Model(&vcn).Updates(map[string]any{
			"remaining_amount": newRemaining,
			"applied_amount":   newApplied,
			"status":           string(newVCNStatus),
		}).Error; err != nil {
			return fmt.Errorf("restore vendor credit note: %w", err)
		}

		// Restore Bill balance.
		newBillBalance := bill.BalanceDue.Add(app.AmountApplied)
		newBillStatus := models.BillStatusPartiallyPaid
		if newBillBalance.Equal(bill.Amount) {
			newBillStatus = models.BillStatusPosted
		}
		if err := tx.Model(&bill).Updates(map[string]any{
			"balance_due": newBillBalance,
			"status":      string(newBillStatus),
		}).Error; err != nil {
			return fmt.Errorf("restore bill: %w", err)
		}

		// Delete application.
		if err := tx.Delete(&app).Error; err != nil {
			return fmt.Errorf("delete application: %w", err)
		}
		return nil
	})
}
