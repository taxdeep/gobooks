// 遵循project_guide.md
package services

// ar_refund_service.go — ARRefund: customer refund cash-outflow lifecycle.
//
// Accounting rules:
//
//   Post (draft → posted):
//     Dr  DebitAccountID      Amount × ExchangeRate   (AR / Deposit Liability / Credit account)
//     Cr  BankAccountID       Amount × ExchangeRate   (cash/bank paid out)
//
//   Void (draft only):
//     Sets status=voided; no JE.
//
//   Reverse (posted only):
//     Creates reversal JE; marks original JE status=reversed.
//     Sets status=reversed.
//
// State machine:
//   draft → posted → reversed
//          ↘ voided (from draft)

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
	ErrRefundNotFound      = errors.New("AR refund not found")
	ErrRefundInvalidStatus = errors.New("action not allowed in current refund status")
	ErrRefundNoBank        = errors.New("bank account is required before posting")
	ErrRefundNoDebitAcct   = errors.New("debit account is required before posting")
)

// ── Input types ───────────────────────────────────────────────────────────────

// ARRefundInput holds all data needed to create or update an ARRefund.
type ARRefundInput struct {
	CustomerID        uint
	BankAccountID     *uint
	SourceType        models.ARRefundSourceType
	CustomerDepositID *uint
	CustomerReceiptID *uint
	CreditNoteID      *uint
	ARReturnID        *uint
	RefundDate        time.Time
	CurrencyCode      string
	ExchangeRate      decimal.Decimal
	Amount            decimal.Decimal
	PaymentMethod     models.PaymentMethod
	Reference         string
	Memo              string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// nextRefundNumber derives the next refund document number for a company.
func nextRefundNumber(db *gorm.DB, companyID uint) string {
	var last models.ARRefund
	db.Where("company_id = ?", companyID).
		Order("id desc").
		Select("refund_number").
		First(&last)
	return NextDocumentNumber(last.RefundNumber, "RFD-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateARRefund creates a new draft refund. No JE is generated.
func CreateARRefund(db *gorm.DB, companyID uint, in ARRefundInput) (*models.ARRefund, error) {
	if in.CustomerID == 0 {
		return nil, errors.New("customer is required")
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
		sourceType = models.ARRefundSourceOther
	}
	pmeth := in.PaymentMethod
	if pmeth == "" {
		pmeth = models.PaymentMethodOther
	}

	ref := models.ARRefund{
		CompanyID:         companyID,
		CustomerID:        in.CustomerID,
		BankAccountID:     in.BankAccountID,
		SourceType:        sourceType,
		CustomerDepositID: in.CustomerDepositID,
		CustomerReceiptID: in.CustomerReceiptID,
		CreditNoteID:      in.CreditNoteID,
		ARReturnID:        in.ARReturnID,
		RefundNumber:      nextRefundNumber(db, companyID),
		Status:            models.ARRefundStatusDraft,
		RefundDate:        in.RefundDate,
		CurrencyCode:      in.CurrencyCode,
		ExchangeRate:      rate,
		Amount:            in.Amount.Round(2),
		PaymentMethod:     pmeth,
		Reference:         in.Reference,
		Memo:              in.Memo,
	}

	if err := db.Create(&ref).Error; err != nil {
		return nil, fmt.Errorf("create AR refund: %w", err)
	}
	return &ref, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetARRefund loads a refund with customer and accounts for the given company.
func GetARRefund(db *gorm.DB, companyID, refundID uint) (*models.ARRefund, error) {
	var ref models.ARRefund
	err := db.Preload("Customer").Preload("BankAccount").
		Where("id = ? AND company_id = ?", refundID, companyID).
		First(&ref).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRefundNotFound
	}
	return &ref, err
}

// ARRefundListFilter bundles the optional list-page filters.
type ARRefundListFilter struct {
	Status     string     // empty = all statuses
	CustomerID uint       // 0 = all customers
	DateFrom   *time.Time // nil = no lower bound on refund_date
	DateTo     *time.Time // nil = no upper bound on refund_date
}

// ListARRefunds returns refunds for a company, newest first.
func ListARRefunds(db *gorm.DB, companyID uint, f ARRefundListFilter) ([]models.ARRefund, error) {
	q := db.Preload("Customer").Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.CustomerID > 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.DateFrom != nil {
		q = q.Where("refund_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("refund_date <= ?", *f.DateTo)
	}
	var refunds []models.ARRefund
	err := q.Order("id desc").Find(&refunds).Error
	return refunds, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateARRefund updates a draft refund.
func UpdateARRefund(db *gorm.DB, companyID, refundID uint, in ARRefundInput) (*models.ARRefund, error) {
	var ref models.ARRefund
	if err := db.Where("id = ? AND company_id = ?", refundID, companyID).First(&ref).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRefundNotFound
		}
		return nil, err
	}
	if ref.Status != models.ARRefundStatusDraft {
		return nil, fmt.Errorf("%w: only draft refunds may be edited", ErrRefundInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}
	sourceType := in.SourceType
	if sourceType == "" {
		sourceType = models.ARRefundSourceOther
	}
	pmeth := in.PaymentMethod
	if pmeth == "" {
		pmeth = models.PaymentMethodOther
	}

	updates := map[string]any{
		"customer_id":         in.CustomerID,
		"bank_account_id":     in.BankAccountID,
		"source_type":         string(sourceType),
		"customer_deposit_id": in.CustomerDepositID,
		"customer_receipt_id": in.CustomerReceiptID,
		"credit_note_id":      in.CreditNoteID,
		"ar_return_id":        in.ARReturnID,
		"refund_date":         in.RefundDate,
		"currency_code":       in.CurrencyCode,
		"exchange_rate":       rate,
		"amount":              in.Amount.Round(2),
		"payment_method":      string(pmeth),
		"reference":           in.Reference,
		"memo":                in.Memo,
	}
	if err := db.Model(&ref).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update AR refund: %w", err)
	}
	return &ref, nil
}

// ── Post ──────────────────────────────────────────────────────────────────────

// PostARRefund transitions a draft refund to posted and generates a JE.
//
// Journal entry:
//
//	Dr  debitAccountID   Amount × ExchangeRate
//	Cr  BankAccountID    Amount × ExchangeRate
//
// debitAccountID is the account being debited (AR, DepositLiability, etc.).
func PostARRefund(db *gorm.DB, companyID, refundID uint, debitAccountID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// 1. Load and validate
		var ref models.ARRefund
		if err := tx.Where("id = ? AND company_id = ?", refundID, companyID).First(&ref).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRefundNotFound
			}
			return err
		}
		if ref.Status != models.ARRefundStatusDraft {
			return fmt.Errorf("%w: only draft refunds can be posted", ErrRefundInvalidStatus)
		}
		if debitAccountID == 0 {
			return ErrRefundNoDebitAcct
		}
		if ref.BankAccountID == nil || *ref.BankAccountID == 0 {
			return ErrRefundNoBank
		}

		// 2. Compute base amount
		rate := ref.ExchangeRate
		if rate.IsZero() || rate.IsNegative() {
			rate = decimal.NewFromInt(1)
		}
		amountBase := ref.Amount.Mul(rate).Round(2)

		// 3. Build journal entry header
		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  ref.RefundDate,
			JournalNo:  "RFD – " + ref.RefundNumber,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceARRefund,
			SourceID:   ref.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create refund JE: %w", err)
		}

		// 4. Build journal lines: Dr DebitAccount / Cr Bank
		lines := []models.JournalLine{
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      debitAccountID,
				Debit:          amountBase,
				Credit:         decimal.Zero,
				Memo:           ref.RefundNumber + " – refund debit",
			},
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *ref.BankAccountID,
				Debit:          decimal.Zero,
				Credit:         amountBase,
				Memo:           ref.RefundNumber + " – bank payout",
			},
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create refund JE lines: %w", err)
		}

		// 5. Project to ledger
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceARRefund,
			SourceID:     ref.ID,
		}); err != nil {
			return fmt.Errorf("project refund to ledger: %w", err)
		}

		// 6. Update refund record
		now := time.Now()
		updates := map[string]any{
			"status":           string(models.ARRefundStatusPosted),
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"posted_at":        &now,
			"posted_by":        actor,
		}
		if actorID != nil {
			updates["posted_by_user_id"] = actorID
		}
		if err := tx.Model(&ref).Updates(updates).Error; err != nil {
			return fmt.Errorf("update refund status: %w", err)
		}

		return nil
	})
}

// ── Void ──────────────────────────────────────────────────────────────────────

// VoidARRefund cancels a draft refund. No JE is generated.
func VoidARRefund(db *gorm.DB, companyID, refundID uint) error {
	var ref models.ARRefund
	if err := db.Where("id = ? AND company_id = ?", refundID, companyID).First(&ref).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRefundNotFound
		}
		return err
	}
	if ref.Status != models.ARRefundStatusDraft {
		return fmt.Errorf("%w: only draft refunds can be voided", ErrRefundInvalidStatus)
	}
	return db.Model(&ref).Update("status", models.ARRefundStatusVoided).Error
}

// ── Reverse ───────────────────────────────────────────────────────────────────

// ReverseARRefund reverses a posted refund by creating a reversal JE.
func ReverseARRefund(db *gorm.DB, companyID, refundID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var ref models.ARRefund
		if err := tx.Where("id = ? AND company_id = ?", refundID, companyID).First(&ref).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRefundNotFound
			}
			return err
		}
		if ref.Status != models.ARRefundStatusPosted {
			return fmt.Errorf("%w: only posted refunds can be reversed", ErrRefundInvalidStatus)
		}
		if ref.JournalEntryID == nil {
			return fmt.Errorf("refund %d has no journal entry to reverse", refundID)
		}

		reversalJEID, err := ReverseJournalEntry(tx, companyID, *ref.JournalEntryID, time.Now())
		if err != nil {
			return fmt.Errorf("reverse refund JE: %w", err)
		}

		if err := tx.Model(&ref).Update("status", string(models.ARRefundStatusReversed)).Error; err != nil {
			return fmt.Errorf("update refund status to reversed: %w", err)
		}

		_ = reversalJEID
		return nil
	})
}
