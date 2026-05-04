// 遵循project_guide.md
package services

// customer_receipt_service.go — CustomerReceipt: formal AR-side cash receipt lifecycle.
//
// Accounting rules:
//
//   Confirm (draft → confirmed/unapplied):
//     Dr  BankAccount     Amount×Rate (base)
//     Cr  ARAccount       Amount×Rate (base)
//
//   Apply to Invoice (confirmed/partially_applied → partially_applied/fully_applied):
//     NO JE — AR account was already hit at confirm time.
//     Creates PaymentApplication record; reduces Invoice.BalanceDue;
//     reduces Receipt.UnappliedAmount; updates statuses.
//
//   Unapply (reverses an active PaymentApplication):
//     NO JE — restores Invoice.BalanceDue; restores Receipt.UnappliedAmount; updates statuses.
//
//   Reverse (confirmed/partially/fully_applied → reversed):
//     Only allowed if UnappliedAmount == Amount (fully unapplied — no active applications).
//     Creates reversal JE; marks original JE reversed; sets status=reversed.
//
//   Void (draft → voided):
//     No JE. Status=voided.

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
	ErrReceiptNotFound           = errors.New("customer receipt not found")
	ErrReceiptInvalidStatus      = errors.New("action not allowed in current receipt status")
	ErrReceiptNoBank             = errors.New("bank account is required before confirming")
	ErrReceiptInsufficientAmount = errors.New("apply amount exceeds receipt unapplied amount")
	ErrReceiptHasApplications    = errors.New("cannot reverse a receipt with active applications; unapply first")
	ErrApplicationNotFound       = errors.New("payment application not found")
)

// ── Input types ───────────────────────────────────────────────────────────────

// CustomerReceiptInput holds all data needed to create or update a CustomerReceipt.
type CustomerReceiptInput struct {
	CustomerID    uint
	BankAccountID *uint
	ReceiptDate   time.Time
	CurrencyCode  string
	ExchangeRate  decimal.Decimal // 1.0 for base currency
	Amount        decimal.Decimal
	PaymentMethod models.PaymentMethod
	Reference     string
	Memo          string
}

// ApplyReceiptInput holds data for applying a receipt to an invoice.
type ApplyReceiptInput struct {
	ReceiptID     uint
	InvoiceID     uint
	AmountApplied decimal.Decimal
	Actor         string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// nextReceiptNumber derives the next receipt document number for a company.
func nextReceiptNumber(db *gorm.DB, companyID uint) string {
	var last models.CustomerReceipt
	db.Where("company_id = ?", companyID).
		Order("id desc").
		Select("receipt_number").
		First(&last)
	return NextDocumentNumber(last.ReceiptNumber, "RCT-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateCustomerReceipt creates a new draft receipt. No JE is generated.
func CreateCustomerReceipt(db *gorm.DB, companyID uint, in CustomerReceiptInput) (*models.CustomerReceipt, error) {
	if in.CustomerID == 0 {
		return nil, errors.New("customer is required")
	}
	if !in.Amount.IsPositive() {
		return nil, errors.New("amount must be positive")
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	rcpt := models.CustomerReceipt{
		CompanyID:       companyID,
		CustomerID:      in.CustomerID,
		BankAccountID:   in.BankAccountID,
		ReceiptNumber:   nextReceiptNumber(db, companyID),
		Status:          models.CustomerReceiptStatusDraft,
		ReceiptDate:     in.ReceiptDate,
		CurrencyCode:    in.CurrencyCode,
		ExchangeRate:    rate,
		Amount:          in.Amount.Round(2),
		AmountBase:      decimal.Zero, // set at confirm
		UnappliedAmount: decimal.Zero, // set at confirm
		PaymentMethod:   in.PaymentMethod,
		Reference:       in.Reference,
		Memo:            in.Memo,
	}
	if rcpt.PaymentMethod == "" {
		rcpt.PaymentMethod = models.PaymentMethodOther
	}

	if err := db.Create(&rcpt).Error; err != nil {
		return nil, fmt.Errorf("create receipt: %w", err)
	}
	return &rcpt, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetCustomerReceipt loads a receipt with its customer for the given company.
func GetCustomerReceipt(db *gorm.DB, companyID, receiptID uint) (*models.CustomerReceipt, error) {
	var rcpt models.CustomerReceipt
	err := db.Preload("Customer").Preload("BankAccount").
		Where("id = ? AND company_id = ?", receiptID, companyID).
		First(&rcpt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrReceiptNotFound
	}
	return &rcpt, err
}

// CustomerReceiptListFilter bundles the optional list-page filters.
// Mirrors SalesOrderListFilter so the AR list pages stay structurally
// aligned.
type CustomerReceiptListFilter struct {
	Status     string     // empty = all statuses
	CustomerID uint       // 0 = all customers
	DateFrom   *time.Time // nil = no lower bound on receipt_date
	DateTo     *time.Time // nil = no upper bound on receipt_date
}

// ListCustomerReceipts returns receipts for a company, newest first.
// All filters are optional — see CustomerReceiptListFilter.
func ListCustomerReceipts(db *gorm.DB, companyID uint, f CustomerReceiptListFilter) ([]models.CustomerReceipt, error) {
	q := db.Preload("Customer").Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.CustomerID > 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.DateFrom != nil {
		q = q.Where("receipt_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("receipt_date <= ?", *f.DateTo)
	}
	var receipts []models.CustomerReceipt
	err := q.Order("id desc").Find(&receipts).Error
	return receipts, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateCustomerReceipt updates a draft receipt.
func UpdateCustomerReceipt(db *gorm.DB, companyID, receiptID uint, in CustomerReceiptInput) (*models.CustomerReceipt, error) {
	var rcpt models.CustomerReceipt
	if err := db.Where("id = ? AND company_id = ?", receiptID, companyID).First(&rcpt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReceiptNotFound
		}
		return nil, err
	}
	if rcpt.Status != models.CustomerReceiptStatusDraft {
		return nil, fmt.Errorf("%w: only draft receipts may be edited", ErrReceiptInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	updates := map[string]any{
		"customer_id":     in.CustomerID,
		"bank_account_id": in.BankAccountID,
		"receipt_date":    in.ReceiptDate,
		"currency_code":   in.CurrencyCode,
		"exchange_rate":   rate,
		"amount":          in.Amount.Round(2),
		"payment_method":  in.PaymentMethod,
		"reference":       in.Reference,
		"memo":            in.Memo,
	}
	if err := db.Model(&rcpt).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update receipt: %w", err)
	}
	return &rcpt, nil
}

// ── Confirm ───────────────────────────────────────────────────────────────────

// ConfirmCustomerReceipt transitions a draft receipt to confirmed (unapplied) and
// creates a double-entry journal entry.
//
//	Dr  BankAccount     Amount×Rate (base)
//	Cr  ARAccount       Amount×Rate (base)
func ConfirmCustomerReceipt(db *gorm.DB, companyID, receiptID uint, actor string, actorID *uuid.UUID) error {
	var rcpt models.CustomerReceipt
	if err := db.Where("id = ? AND company_id = ?", receiptID, companyID).First(&rcpt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReceiptNotFound
		}
		return err
	}
	if rcpt.Status != models.CustomerReceiptStatusDraft {
		return fmt.Errorf("%w: only draft receipts can be confirmed", ErrReceiptInvalidStatus)
	}
	if rcpt.BankAccountID == nil || *rcpt.BankAccountID == 0 {
		return ErrReceiptNoBank
	}

	// Resolve AR control account.
	arAccount, err := ResolveControlAccount(db, companyID, 0,
		models.ARAPDocTypeInvoice, rcpt.CurrencyCode, false,
		models.DetailAccountsReceivable, ErrNoARAccount)
	if err != nil {
		return fmt.Errorf("resolve AR account: %w", err)
	}

	// Compute base-currency amount.
	rate := rcpt.ExchangeRate
	if rate.IsZero() || rate.IsNegative() {
		rate = decimal.NewFromInt(1)
	}
	amountBase := rcpt.Amount.Mul(rate).Round(2)

	return db.Transaction(func(tx *gorm.DB) error {
		cash, err := resolveCashPostingCurrency(tx, companyID, *rcpt.BankAccountID, rcpt.CurrencyCode, rate)
		if err != nil {
			return err
		}
		txAmount := amountBase
		if cash.BankIsForeign {
			txAmount = rcpt.Amount.Round(2)
		}

		je := models.JournalEntry{
			CompanyID:               companyID,
			EntryDate:               rcpt.ReceiptDate,
			JournalNo:               "RCT – " + rcpt.ReceiptNumber,
			Status:                  models.JournalEntryStatusPosted,
			TransactionCurrencyCode: cash.TransactionCurrencyCode,
			ExchangeRate:            cash.ExchangeRate,
			ExchangeRateDate:        rcpt.ReceiptDate,
			ExchangeRateSource:      cashExchangeRateSource(cash),
			SourceType:              models.LedgerSourceCustomerReceipt,
			SourceID:                rcpt.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create receipt JE: %w", err)
		}

		lines := []models.JournalLine{
			// Dr Bank / Cash
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *rcpt.BankAccountID,
				TxDebit:        txAmount,
				TxCredit:       decimal.Zero,
				Debit:          amountBase,
				Credit:         decimal.Zero,
				Memo:           rcpt.ReceiptNumber + " – cash received",
			},
			// Cr AR
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      arAccount.ID,
				TxDebit:        decimal.Zero,
				TxCredit:       txAmount,
				Debit:          decimal.Zero,
				Credit:         amountBase,
				Memo:           rcpt.ReceiptNumber + " – AR reduction",
			},
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create receipt JE lines: %w", err)
		}

		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceCustomerReceipt,
			SourceID:     rcpt.ID,
		}); err != nil {
			return fmt.Errorf("project receipt to ledger: %w", err)
		}

		now := time.Now()
		updates := map[string]any{
			"status":           string(models.CustomerReceiptStatusConfirmed),
			"journal_entry_id": je.ID,
			"amount_base":      amountBase,
			"unapplied_amount": rcpt.Amount.Round(2), // full amount unapplied initially
			"confirmed_at":     &now,
			"confirmed_by":     actor,
		}
		if actorID != nil {
			updates["confirmed_by_user_id"] = actorID
		}
		if err := tx.Model(&rcpt).Updates(updates).Error; err != nil {
			return fmt.Errorf("update receipt status: %w", err)
		}
		return nil
	})
}

// ── Apply to Invoice ──────────────────────────────────────────────────────────

// ApplyReceiptToInvoice records a PaymentApplication against an open invoice.
//
// No JE is created — the AR account was already reduced at confirmation time.
// This operation only moves money from "unapplied cash" to "applied to invoice".
func ApplyReceiptToInvoice(db *gorm.DB, companyID uint, in ApplyReceiptInput) error {
	if !in.AmountApplied.IsPositive() {
		return errors.New("apply amount must be positive")
	}

	var rcpt models.CustomerReceipt
	if err := db.Where("id = ? AND company_id = ?", in.ReceiptID, companyID).First(&rcpt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReceiptNotFound
		}
		return err
	}
	if rcpt.Status != models.CustomerReceiptStatusConfirmed &&
		rcpt.Status != models.CustomerReceiptStatusPartiallyApplied {
		return fmt.Errorf("%w: receipt must be confirmed or partially applied", ErrReceiptInvalidStatus)
	}
	if in.AmountApplied.GreaterThan(rcpt.UnappliedAmount) {
		return ErrReceiptInsufficientAmount
	}

	// Load invoice.
	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", in.InvoiceID, companyID).First(&inv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("invoice not found")
		}
		return err
	}
	if inv.BalanceDue.LessThan(in.AmountApplied) {
		return errors.New("apply amount exceeds invoice balance due")
	}

	rate := rcpt.ExchangeRate
	if rate.IsZero() || rate.IsNegative() {
		rate = decimal.NewFromInt(1)
	}
	amountAppliedBase := in.AmountApplied.Mul(rate).Round(2)

	return db.Transaction(func(tx *gorm.DB) error {
		// Create PaymentApplication record.
		app := models.PaymentApplication{
			CompanyID:         companyID,
			SourceType:        models.PaymentApplicationSourceReceipt,
			CustomerReceiptID: &rcpt.ID,
			InvoiceID:         inv.ID,
			Status:            models.PaymentApplicationStatusActive,
			AmountApplied:     in.AmountApplied.Round(2),
			AmountAppliedBase: amountAppliedBase,
			AppliedAt:         time.Now(),
			AppliedBy:         in.Actor,
		}
		if err := tx.Create(&app).Error; err != nil {
			return fmt.Errorf("create payment application: %w", err)
		}

		// Update receipt unapplied amount and status.
		newUnapplied := rcpt.UnappliedAmount.Sub(in.AmountApplied).Round(2)
		var newReceiptStatus models.CustomerReceiptStatus
		if newUnapplied.IsZero() {
			newReceiptStatus = models.CustomerReceiptStatusFullyApplied
		} else {
			newReceiptStatus = models.CustomerReceiptStatusPartiallyApplied
		}
		if err := tx.Model(&rcpt).Updates(map[string]any{
			"unapplied_amount": newUnapplied,
			"status":           string(newReceiptStatus),
		}).Error; err != nil {
			return fmt.Errorf("update receipt unapplied amount: %w", err)
		}

		// Update invoice BalanceDue.
		newBalanceDue := inv.BalanceDue.Sub(in.AmountApplied).Round(2)
		if err := tx.Model(&inv).Update("balance_due", newBalanceDue).Error; err != nil {
			return fmt.Errorf("update invoice balance due: %w", err)
		}

		return nil
	})
}

// ── Unapply ───────────────────────────────────────────────────────────────────

// UnapplyReceipt reverses an active PaymentApplication.
//
// No JE is created. Invoice.BalanceDue and Receipt.UnappliedAmount are restored.
func UnapplyReceipt(db *gorm.DB, companyID, applicationID uint, actor string) error {
	var app models.PaymentApplication
	if err := db.Preload("Invoice").
		Where("id = ? AND company_id = ?", applicationID, companyID).
		First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrApplicationNotFound
		}
		return err
	}
	if app.Status != models.PaymentApplicationStatusActive {
		return fmt.Errorf("%w: application is not active", ErrReceiptInvalidStatus)
	}
	if app.SourceType != models.PaymentApplicationSourceReceipt || app.CustomerReceiptID == nil {
		return errors.New("application is not linked to a customer receipt")
	}

	var rcpt models.CustomerReceipt
	if err := db.Where("id = ? AND company_id = ?", *app.CustomerReceiptID, companyID).First(&rcpt).Error; err != nil {
		return ErrReceiptNotFound
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Reverse application.
		now := time.Now()
		if err := tx.Model(&app).Updates(map[string]any{
			"status":      string(models.PaymentApplicationStatusReversed),
			"reversed_at": &now,
			"reversed_by": actor,
		}).Error; err != nil {
			return fmt.Errorf("reverse application: %w", err)
		}

		// Restore receipt unapplied amount and status.
		newUnapplied := rcpt.UnappliedAmount.Add(app.AmountApplied).Round(2)
		var newReceiptStatus models.CustomerReceiptStatus
		if newUnapplied.Equal(rcpt.Amount) {
			newReceiptStatus = models.CustomerReceiptStatusConfirmed
		} else {
			newReceiptStatus = models.CustomerReceiptStatusPartiallyApplied
		}
		if err := tx.Model(&rcpt).Updates(map[string]any{
			"unapplied_amount": newUnapplied,
			"status":           string(newReceiptStatus),
		}).Error; err != nil {
			return fmt.Errorf("restore receipt unapplied amount: %w", err)
		}

		// Restore invoice BalanceDue.
		if err := tx.Model(&app.Invoice).
			Update("balance_due", app.Invoice.BalanceDue.Add(app.AmountApplied).Round(2)).Error; err != nil {
			return fmt.Errorf("restore invoice balance due: %w", err)
		}

		return nil
	})
}

// ── Reverse ───────────────────────────────────────────────────────────────────

// ReverseCustomerReceipt reverses a confirmed (fully unapplied) receipt.
//
// Only allowed when UnappliedAmount == Amount (no active applications remain).
// Creates a reversal JE; marks the original JE reversed; sets status=reversed.
func ReverseCustomerReceipt(db *gorm.DB, companyID, receiptID uint, actor string) error {
	var rcpt models.CustomerReceipt
	if err := db.Where("id = ? AND company_id = ?", receiptID, companyID).First(&rcpt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReceiptNotFound
		}
		return err
	}

	// Only confirmed (unapplied) receipts may be reversed.
	if rcpt.Status != models.CustomerReceiptStatusConfirmed {
		return fmt.Errorf("%w: only confirmed (unapplied) receipts can be reversed; unapply all applications first", ErrReceiptInvalidStatus)
	}
	if rcpt.JournalEntryID == nil {
		return errors.New("receipt has no linked journal entry")
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Create reversal JE.
		reversalID, err := ReverseJournalEntry(tx, companyID, *rcpt.JournalEntryID, time.Now())
		if err != nil {
			return fmt.Errorf("reverse receipt JE: %w", err)
		}
		_ = reversalID

		// Mark original JE reversed.
		if err := tx.Model(&models.JournalEntry{}).
			Where("id = ? AND company_id = ?", *rcpt.JournalEntryID, companyID).
			Update("status", models.JournalEntryStatusReversed).Error; err != nil {
			return fmt.Errorf("mark original JE reversed: %w", err)
		}

		// Set receipt status to reversed.
		if err := tx.Model(&rcpt).Updates(map[string]any{
			"status":           string(models.CustomerReceiptStatusReversed),
			"unapplied_amount": decimal.Zero,
		}).Error; err != nil {
			return fmt.Errorf("set receipt reversed: %w", err)
		}
		return nil
	})
}

// ── Void ──────────────────────────────────────────────────────────────────────

// VoidCustomerReceipt voids a draft receipt. No JE is created.
func VoidCustomerReceipt(db *gorm.DB, companyID, receiptID uint) error {
	var rcpt models.CustomerReceipt
	if err := db.Where("id = ? AND company_id = ?", receiptID, companyID).First(&rcpt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReceiptNotFound
		}
		return err
	}
	if rcpt.Status != models.CustomerReceiptStatusDraft {
		return fmt.Errorf("%w: only draft receipts can be voided", ErrReceiptInvalidStatus)
	}
	return db.Model(&rcpt).Update("status", models.CustomerReceiptStatusVoided).Error
}

// ── List Applications ─────────────────────────────────────────────────────────

// ListReceiptApplications returns all PaymentApplication records for a receipt.
func ListReceiptApplications(db *gorm.DB, companyID, receiptID uint) ([]models.PaymentApplication, error) {
	var apps []models.PaymentApplication
	err := db.Preload("Invoice").
		Where("company_id = ? AND customer_receipt_id = ?", companyID, receiptID).
		Order("id asc").Find(&apps).Error
	return apps, err
}
