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

// GetVendorCreditNote loads a credit note with vendor and accounts for the given company.
func GetVendorCreditNote(db *gorm.DB, companyID, vcnID uint) (*models.VendorCreditNote, error) {
	var vcn models.VendorCreditNote
	err := db.Preload("Vendor").Preload("APAccount").Preload("OffsetAccount").
		Preload("Bill").Preload("VendorReturn").
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
