// 遵循project_guide.md
package services

// vendor_refund_service.go — VendorRefund: cash received back from a vendor.
//
// Accounting rules:
//
//   Post (draft → posted):
//     Dr  BankAccountID    AmountBase   (cash received)
//     Cr  CreditAccountID  AmountBase   (prepayment asset or AP account)
//
//   Void (draft only):
//     Sets status=voided; no JE.
//
//   Reverse (posted only):
//     Creates reversal JE; marks status=reversed.
//
// State machine:
//   draft → posted → reversed
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
	ErrVendorRefundNotFound      = errors.New("vendor refund not found")
	ErrVendorRefundInvalidStatus = errors.New("action not allowed in current vendor refund status")
	ErrVendorRefundNoBank        = errors.New("bank account is required before posting")
	ErrVendorRefundNoCreditAcct  = errors.New("credit account is required before posting")
)

// ── Input types ───────────────────────────────────────────────────────────────

// VendorRefundInput holds all data needed to create or update a VendorRefund.
type VendorRefundInput struct {
	VendorID           uint
	SourceType         models.VendorRefundSourceType
	VendorPrepaymentID *uint
	VendorCreditNoteID *uint
	RefundDate         time.Time
	CurrencyCode       string
	ExchangeRate       decimal.Decimal
	Amount             decimal.Decimal
	BankAccountID      *uint
	CreditAccountID    *uint
	PaymentMethod      models.PaymentMethod
	Reference          string
	Memo               string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func nextVendorRefundNumber(db *gorm.DB, companyID uint) string {
	var last models.VendorRefund
	db.Where("company_id = ?", companyID).Order("id desc").Select("refund_number").First(&last)
	return NextDocumentNumber(last.RefundNumber, "VRF-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateVendorRefund creates a new draft vendor refund.
func CreateVendorRefund(db *gorm.DB, companyID uint, in VendorRefundInput) (*models.VendorRefund, error) {
	if in.VendorID == 0 {
		return nil, errors.New("vendor is required")
	}
	if !in.Amount.IsPositive() {
		return nil, errors.New("refund amount must be positive")
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	sourceType := in.SourceType
	if sourceType == "" {
		sourceType = models.VendorRefundSourceOther
	}

	vrf := models.VendorRefund{
		CompanyID:          companyID,
		VendorID:           in.VendorID,
		RefundNumber:       nextVendorRefundNumber(db, companyID),
		Status:             models.VendorRefundStatusDraft,
		SourceType:         sourceType,
		VendorPrepaymentID: in.VendorPrepaymentID,
		VendorCreditNoteID: in.VendorCreditNoteID,
		RefundDate:         in.RefundDate,
		CurrencyCode:       in.CurrencyCode,
		ExchangeRate:       rate,
		Amount:             in.Amount.Round(2),
		BankAccountID:      in.BankAccountID,
		CreditAccountID:    in.CreditAccountID,
		PaymentMethod:      in.PaymentMethod,
		Reference:          in.Reference,
		Memo:               in.Memo,
	}

	if err := db.Create(&vrf).Error; err != nil {
		return nil, fmt.Errorf("create vendor refund: %w", err)
	}
	return &vrf, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetVendorRefund loads a refund with vendor and accounts for the given company.
func GetVendorRefund(db *gorm.DB, companyID, vrfID uint) (*models.VendorRefund, error) {
	var vrf models.VendorRefund
	err := db.Preload("Vendor").Preload("BankAccount").Preload("CreditAccount").
		Where("id = ? AND company_id = ?", vrfID, companyID).First(&vrf).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVendorRefundNotFound
	}
	return &vrf, err
}

// ListVendorRefunds returns refunds for a company, newest first.
func ListVendorRefunds(db *gorm.DB, companyID uint, statusFilter string, vendorID uint) ([]models.VendorRefund, error) {
	q := db.Preload("Vendor").Where("company_id = ?", companyID)
	if statusFilter != "" {
		q = q.Where("status = ?", statusFilter)
	}
	if vendorID > 0 {
		q = q.Where("vendor_id = ?", vendorID)
	}
	var vrfs []models.VendorRefund
	err := q.Order("id desc").Find(&vrfs).Error
	return vrfs, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateVendorRefund updates a draft vendor refund.
func UpdateVendorRefund(db *gorm.DB, companyID, vrfID uint, in VendorRefundInput) (*models.VendorRefund, error) {
	var vrf models.VendorRefund
	if err := db.Where("id = ? AND company_id = ?", vrfID, companyID).First(&vrf).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVendorRefundNotFound
		}
		return nil, err
	}
	if vrf.Status != models.VendorRefundStatusDraft {
		return nil, fmt.Errorf("%w: only draft refunds may be edited", ErrVendorRefundInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	updates := map[string]any{
		"vendor_id":            in.VendorID,
		"source_type":          in.SourceType,
		"vendor_prepayment_id": in.VendorPrepaymentID,
		"vendor_credit_note_id": in.VendorCreditNoteID,
		"refund_date":          in.RefundDate,
		"currency_code":        in.CurrencyCode,
		"exchange_rate":        rate,
		"amount":               in.Amount.Round(2),
		"bank_account_id":      in.BankAccountID,
		"credit_account_id":    in.CreditAccountID,
		"payment_method":       in.PaymentMethod,
		"reference":            in.Reference,
		"memo":                 in.Memo,
	}
	if err := db.Model(&vrf).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update vendor refund: %w", err)
	}
	return &vrf, nil
}

// ── Post ──────────────────────────────────────────────────────────────────────

// PostVendorRefund transitions a draft refund to posted and generates a JE.
//
// Journal entry:
//
//	Dr  BankAccountID    AmountBase
//	Cr  CreditAccountID  AmountBase
func PostVendorRefund(db *gorm.DB, companyID, vrfID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var vrf models.VendorRefund
		if err := tx.Where("id = ? AND company_id = ?", vrfID, companyID).First(&vrf).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrVendorRefundNotFound
			}
			return err
		}
		if vrf.Status != models.VendorRefundStatusDraft {
			return fmt.Errorf("%w: only draft refunds can be posted", ErrVendorRefundInvalidStatus)
		}
		if vrf.BankAccountID == nil || *vrf.BankAccountID == 0 {
			return ErrVendorRefundNoBank
		}
		if vrf.CreditAccountID == nil || *vrf.CreditAccountID == 0 {
			return ErrVendorRefundNoCreditAcct
		}

		rate := vrf.ExchangeRate
		if rate.IsZero() || rate.IsNegative() {
			rate = decimal.NewFromInt(1)
		}
		amountBase := vrf.Amount.Mul(rate).Round(2)

		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  vrf.RefundDate,
			JournalNo:  "VRF – " + vrf.RefundNumber,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceVendorRefund,
			SourceID:   vrf.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create refund JE: %w", err)
		}

		lines := []models.JournalLine{
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *vrf.BankAccountID,
				Debit:          amountBase,
				Credit:         decimal.Zero,
				Memo:           vrf.RefundNumber + " – cash received from vendor",
			},
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *vrf.CreditAccountID,
				Debit:          decimal.Zero,
				Credit:         amountBase,
				Memo:           vrf.RefundNumber + " – vendor refund source",
			},
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create refund JE lines: %w", err)
		}

		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceVendorRefund,
			SourceID:     vrf.ID,
		}); err != nil {
			return fmt.Errorf("project refund to ledger: %w", err)
		}

		now := time.Now()
		updates := map[string]any{
			"status":           string(models.VendorRefundStatusPosted),
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"posted_at":        &now,
			"posted_by":        actor,
		}
		if actorID != nil {
			updates["posted_by_user_id"] = actorID
		}
		return tx.Model(&vrf).Updates(updates).Error
	})
}

// ── Void ──────────────────────────────────────────────────────────────────────

// VoidVendorRefund cancels a draft refund. No JE is generated.
func VoidVendorRefund(db *gorm.DB, companyID, vrfID uint) error {
	var vrf models.VendorRefund
	if err := db.Where("id = ? AND company_id = ?", vrfID, companyID).First(&vrf).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVendorRefundNotFound
		}
		return err
	}
	if vrf.Status != models.VendorRefundStatusDraft {
		return fmt.Errorf("%w: only draft refunds can be voided", ErrVendorRefundInvalidStatus)
	}
	return db.Model(&vrf).Update("status", models.VendorRefundStatusVoided).Error
}

// ── Reverse ───────────────────────────────────────────────────────────────────

// ReverseVendorRefund reverses a posted vendor refund by creating a reversal JE.
func ReverseVendorRefund(db *gorm.DB, companyID, vrfID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var vrf models.VendorRefund
		if err := tx.Where("id = ? AND company_id = ?", vrfID, companyID).First(&vrf).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrVendorRefundNotFound
			}
			return err
		}
		if vrf.Status != models.VendorRefundStatusPosted {
			return fmt.Errorf("%w: only posted refunds can be reversed", ErrVendorRefundInvalidStatus)
		}
		if vrf.JournalEntryID == nil {
			return fmt.Errorf("vendor refund %d has no journal entry to reverse", vrfID)
		}

		_, err := ReverseJournalEntry(tx, companyID, *vrf.JournalEntryID, time.Now())
		if err != nil {
			return fmt.Errorf("reverse vendor refund JE: %w", err)
		}

		return tx.Model(&vrf).Update("status", string(models.VendorRefundStatusReversed)).Error
	})
}
