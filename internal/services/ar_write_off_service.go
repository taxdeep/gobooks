// 遵循project_guide.md
package services

// ar_write_off_service.go — ARWriteOff: bad-debt / uncollectible AR write-off lifecycle.
//
// Accounting rules:
//
//   Post (draft → posted):
//     Dr  ExpenseAccountID   Amount × ExchangeRate   (bad debt expense)
//     Cr  ARAccountID        Amount × ExchangeRate   (reduce AR)
//     + updates Invoice.BalanceDue by reducing the written-off amount (if invoice linked)
//
//   Void (draft only):
//     Sets status=voided; no JE.
//
//   Reverse (posted only):
//     Creates reversal JE; marks original JE reversed.
//     Sets status=reversed.
//     Restores Invoice.BalanceDue if invoice was linked.
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

	"balanciz/internal/models"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrWriteOffNotFound      = errors.New("AR write-off not found")
	ErrWriteOffInvalidStatus = errors.New("action not allowed in current write-off status")
	ErrWriteOffNoARAcct      = errors.New("AR account is required before posting")
	ErrWriteOffNoExpenseAcct = errors.New("expense account is required before posting")
)

// ── Input types ───────────────────────────────────────────────────────────────

// ARWriteOffInput holds all data needed to create or update an ARWriteOff.
type ARWriteOffInput struct {
	CustomerID       uint
	InvoiceID        *uint
	ARAccountID      *uint
	ExpenseAccountID *uint
	WriteOffDate     time.Time
	CurrencyCode     string
	ExchangeRate     decimal.Decimal
	Amount           decimal.Decimal
	Reason           string
	Memo             string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// nextWriteOffNumber derives the next write-off document number for a company.
func nextWriteOffNumber(db *gorm.DB, companyID uint) string {
	var last models.ARWriteOff
	db.Where("company_id = ?", companyID).
		Order("id desc").
		Select("write_off_number").
		First(&last)
	return NextDocumentNumber(last.WriteOffNumber, "WOF-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateARWriteOff creates a new draft write-off. No JE is generated.
func CreateARWriteOff(db *gorm.DB, companyID uint, in ARWriteOffInput) (*models.ARWriteOff, error) {
	if in.CustomerID == 0 {
		return nil, errors.New("customer is required")
	}
	if !in.Amount.IsPositive() {
		return nil, errors.New("write-off amount must be positive")
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	wo := models.ARWriteOff{
		CompanyID:        companyID,
		CustomerID:       in.CustomerID,
		InvoiceID:        in.InvoiceID,
		ARAccountID:      in.ARAccountID,
		ExpenseAccountID: in.ExpenseAccountID,
		WriteOffNumber:   nextWriteOffNumber(db, companyID),
		Status:           models.ARWriteOffStatusDraft,
		WriteOffDate:     in.WriteOffDate,
		CurrencyCode:     in.CurrencyCode,
		ExchangeRate:     rate,
		Amount:           in.Amount.Round(2),
		Reason:           in.Reason,
		Memo:             in.Memo,
	}

	if err := db.Create(&wo).Error; err != nil {
		return nil, fmt.Errorf("create AR write-off: %w", err)
	}
	return &wo, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetARWriteOff loads a write-off with customer and accounts for the given company.
func GetARWriteOff(db *gorm.DB, companyID, writeOffID uint) (*models.ARWriteOff, error) {
	var wo models.ARWriteOff
	err := db.Preload("Customer").Preload("ARAccount").Preload("ExpenseAccount").
		Where("id = ? AND company_id = ?", writeOffID, companyID).
		First(&wo).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWriteOffNotFound
	}
	return &wo, err
}

// ListARWriteOffs returns write-offs for a company, newest first.
func ListARWriteOffs(db *gorm.DB, companyID uint, statusFilter string, customerID uint) ([]models.ARWriteOff, error) {
	q := db.Preload("Customer").Where("company_id = ?", companyID)
	if statusFilter != "" {
		q = q.Where("status = ?", statusFilter)
	}
	if customerID > 0 {
		q = q.Where("customer_id = ?", customerID)
	}
	var wos []models.ARWriteOff
	err := q.Order("id desc").Find(&wos).Error
	return wos, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateARWriteOff updates a draft write-off.
func UpdateARWriteOff(db *gorm.DB, companyID, writeOffID uint, in ARWriteOffInput) (*models.ARWriteOff, error) {
	var wo models.ARWriteOff
	if err := db.Where("id = ? AND company_id = ?", writeOffID, companyID).First(&wo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrWriteOffNotFound
		}
		return nil, err
	}
	if wo.Status != models.ARWriteOffStatusDraft {
		return nil, fmt.Errorf("%w: only draft write-offs may be edited", ErrWriteOffInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	updates := map[string]any{
		"customer_id":        in.CustomerID,
		"invoice_id":         in.InvoiceID,
		"ar_account_id":      in.ARAccountID,
		"expense_account_id": in.ExpenseAccountID,
		"write_off_date":     in.WriteOffDate,
		"currency_code":      in.CurrencyCode,
		"exchange_rate":      rate,
		"amount":             in.Amount.Round(2),
		"reason":             in.Reason,
		"memo":               in.Memo,
	}
	if err := db.Model(&wo).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update AR write-off: %w", err)
	}
	return &wo, nil
}

// ── Post ──────────────────────────────────────────────────────────────────────

// PostARWriteOff transitions a draft write-off to posted and generates a JE.
//
// Journal entry:
//
//	Dr  ExpenseAccountID   Amount × ExchangeRate
//	Cr  ARAccountID        Amount × ExchangeRate
//
// If InvoiceID is set, Invoice.BalanceDue is reduced by the written-off amount.
func PostARWriteOff(db *gorm.DB, companyID, writeOffID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// 1. Load and validate
		var wo models.ARWriteOff
		if err := tx.Where("id = ? AND company_id = ?", writeOffID, companyID).First(&wo).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWriteOffNotFound
			}
			return err
		}
		if wo.Status != models.ARWriteOffStatusDraft {
			return fmt.Errorf("%w: only draft write-offs can be posted", ErrWriteOffInvalidStatus)
		}
		if wo.ARAccountID == nil || *wo.ARAccountID == 0 {
			return ErrWriteOffNoARAcct
		}
		if wo.ExpenseAccountID == nil || *wo.ExpenseAccountID == 0 {
			return ErrWriteOffNoExpenseAcct
		}

		// 2. Compute base amount
		rate := wo.ExchangeRate
		if rate.IsZero() || rate.IsNegative() {
			rate = decimal.NewFromInt(1)
		}
		amountBase := wo.Amount.Mul(rate).Round(2)

		// 3. Build journal entry header
		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  wo.WriteOffDate,
			JournalNo:  "WOF – " + wo.WriteOffNumber,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceARWriteOff,
			SourceID:   wo.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create write-off JE: %w", err)
		}

		// 4. Build journal lines: Dr Expense / Cr AR
		lines := []models.JournalLine{
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *wo.ExpenseAccountID,
				Debit:          amountBase,
				Credit:         decimal.Zero,
				Memo:           wo.WriteOffNumber + " – bad debt expense",
			},
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *wo.ARAccountID,
				Debit:          decimal.Zero,
				Credit:         amountBase,
				Memo:           wo.WriteOffNumber + " – AR reduction",
			},
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create write-off JE lines: %w", err)
		}

		// 5. Project to ledger
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceARWriteOff,
			SourceID:     wo.ID,
		}); err != nil {
			return fmt.Errorf("project write-off to ledger: %w", err)
		}

		// 6. Reduce Invoice.BalanceDue if linked
		if wo.InvoiceID != nil {
			var inv models.Invoice
			if err := tx.Where("id = ? AND company_id = ?", *wo.InvoiceID, companyID).
				First(&inv).Error; err == nil {
				newBal := inv.BalanceDue.Sub(amountBase)
				if newBal.IsNegative() {
					newBal = decimal.Zero
				}
				if err := tx.Model(&inv).Update("balance_due", newBal).Error; err != nil {
					return fmt.Errorf("reduce invoice balance_due: %w", err)
				}
			}
		}

		// 7. Update write-off record
		now := time.Now()
		updates := map[string]any{
			"status":           string(models.ARWriteOffStatusPosted),
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"posted_at":        &now,
			"posted_by":        actor,
		}
		if actorID != nil {
			updates["posted_by_user_id"] = actorID
		}
		if err := tx.Model(&wo).Updates(updates).Error; err != nil {
			return fmt.Errorf("update write-off status: %w", err)
		}

		return nil
	})
}

// ── Void ──────────────────────────────────────────────────────────────────────

// VoidARWriteOff cancels a draft write-off. No JE is generated.
func VoidARWriteOff(db *gorm.DB, companyID, writeOffID uint) error {
	var wo models.ARWriteOff
	if err := db.Where("id = ? AND company_id = ?", writeOffID, companyID).First(&wo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWriteOffNotFound
		}
		return err
	}
	if wo.Status != models.ARWriteOffStatusDraft {
		return fmt.Errorf("%w: only draft write-offs can be voided", ErrWriteOffInvalidStatus)
	}
	return db.Model(&wo).Update("status", models.ARWriteOffStatusVoided).Error
}

// ── Reverse ───────────────────────────────────────────────────────────────────

// ReverseARWriteOff reverses a posted write-off by creating a reversal JE.
// If InvoiceID is set, Invoice.BalanceDue is restored.
func ReverseARWriteOff(db *gorm.DB, companyID, writeOffID uint, actor string, actorID *uuid.UUID) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var wo models.ARWriteOff
		if err := tx.Where("id = ? AND company_id = ?", writeOffID, companyID).First(&wo).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWriteOffNotFound
			}
			return err
		}
		if wo.Status != models.ARWriteOffStatusPosted {
			return fmt.Errorf("%w: only posted write-offs can be reversed", ErrWriteOffInvalidStatus)
		}
		if wo.JournalEntryID == nil {
			return fmt.Errorf("write-off %d has no journal entry to reverse", writeOffID)
		}

		_, err := ReverseJournalEntry(tx, companyID, *wo.JournalEntryID, time.Now())
		if err != nil {
			return fmt.Errorf("reverse write-off JE: %w", err)
		}

		// Restore Invoice.BalanceDue
		if wo.InvoiceID != nil {
			rate := wo.ExchangeRate
			if rate.IsZero() || rate.IsNegative() {
				rate = decimal.NewFromInt(1)
			}
			amountBase := wo.Amount.Mul(rate).Round(2)
			var inv models.Invoice
			if err := tx.Where("id = ? AND company_id = ?", *wo.InvoiceID, companyID).
				First(&inv).Error; err == nil {
				newBal := inv.BalanceDue.Add(amountBase)
				if err := tx.Model(&inv).Update("balance_due", newBal).Error; err != nil {
					return fmt.Errorf("restore invoice balance_due: %w", err)
				}
			}
		}

		if err := tx.Model(&wo).Update("status", string(models.ARWriteOffStatusReversed)).Error; err != nil {
			return fmt.Errorf("update write-off status to reversed: %w", err)
		}

		return nil
	})
}
