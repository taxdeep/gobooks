// 遵循project_guide.md
package services

// customer_deposit_service.go — CustomerDeposit: pre-invoice cash receipt lifecycle.
//
// Accounting rules:
//
//   Post (draft → posted/unapplied):
//     Dr  BankAccount          Amount (base)
//     Cr  DepositLiability     Amount (base)
//
//   Apply to Invoice (posted/partially_applied → partially_applied/fully_applied):
//     Dr  DepositLiability     AmountApplied (base)
//     Cr  ARAccount            AmountApplied (base)
//     + updates Invoice.BalanceDue and Invoice payment status
//     + updates Deposit.BalanceRemaining
//
//   Void (draft or posted/unapplied):
//     If posted: creates reversal JE; marks deposit JE status=reversed
//     Sets deposit status=voided, BalanceRemaining=0

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Errors ─────────────────────────────────────────────────────────────────

var (
	ErrDepositNotFound            = errors.New("customer deposit not found")
	ErrDepositInvalidStatus       = errors.New("action not allowed in current deposit status")
	ErrDepositNoBank              = errors.New("bank account is required before posting")
	ErrDepositNoLiability         = errors.New("deposit liability account is required before posting")
	ErrDepositInsufficientBalance = errors.New("apply amount exceeds deposit balance remaining")
	ErrDepositAlreadyApplied      = errors.New("cannot void a deposit that has been partially or fully applied")
)

// ── Input types ──────────────────────────────────────────────────────────────

// CustomerDepositInput holds all data needed to create or update a CustomerDeposit.
type CustomerDepositInput struct {
	CustomerID                uint
	SalesOrderID              *uint
	BankAccountID             *uint
	DepositLiabilityAccountID *uint
	DepositDate               time.Time
	CurrencyCode              string
	ExchangeRate              decimal.Decimal // 1.0 for base currency
	Amount                    decimal.Decimal
	PaymentMethod             models.PaymentMethod
	Reference                 string
	Memo                      string
}

// ApplyDepositInput holds data for applying a deposit to an invoice.
type ApplyDepositInput struct {
	DepositID     uint
	InvoiceID     uint
	AmountApplied decimal.Decimal
	Actor         string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// allocateDepositNumber draws the next deposit number from the shared
// NumberingSetting (`customer_deposit` module — default DEP0001). The
// caller is responsible for committing the surrounding transaction; the
// counter is advanced via BumpCustomerDepositNextNumberAfterCreate so a
// failed create-then-rollback also rolls back the counter bump.
func allocateDepositNumber(db *gorm.DB, companyID uint) (string, error) {
	n, err := SuggestNextCustomerDepositNumber(db, companyID)
	if err != nil {
		return "", fmt.Errorf("suggest deposit number: %w", err)
	}
	return n, nil
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateCustomerDeposit creates a new draft deposit. No JE is generated.
func CreateCustomerDeposit(db *gorm.DB, companyID uint, in CustomerDepositInput) (*models.CustomerDeposit, error) {
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

	depNumber, err := allocateDepositNumber(db, companyID)
	if err != nil {
		return nil, err
	}

	dep := models.CustomerDeposit{
		CompanyID:                 companyID,
		CustomerID:                in.CustomerID,
		SalesOrderID:              in.SalesOrderID,
		BankAccountID:             in.BankAccountID,
		DepositLiabilityAccountID: in.DepositLiabilityAccountID,
		DepositNumber:             depNumber,
		Status:                    models.CustomerDepositStatusDraft,
		// Source defaults to manual — operator-created via /deposits/new.
		// The Receive Payment overpayment path explicitly sets it to
		// "overpayment" instead.
		Source:           models.DepositSourceManual,
		DepositDate:      in.DepositDate,
		CurrencyCode:     in.CurrencyCode,
		ExchangeRate:     rate,
		Amount:           in.Amount.Round(2),
		AmountBase:       decimal.Zero, // set at posting
		BalanceRemaining: decimal.Zero, // set at posting
		PaymentMethod:    in.PaymentMethod,
		Reference:        in.Reference,
		Memo:             in.Memo,
	}
	if dep.PaymentMethod == "" {
		dep.PaymentMethod = models.PaymentMethodOther
	}

	if err := db.Create(&dep).Error; err != nil {
		return nil, fmt.Errorf("create deposit: %w", err)
	}
	if err := BumpCustomerDepositNextNumberAfterCreate(db, companyID); err != nil {
		return nil, fmt.Errorf("bump deposit counter: %w", err)
	}
	return &dep, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetCustomerDeposit loads a deposit with its customer for the given company.
func GetCustomerDeposit(db *gorm.DB, companyID, depositID uint) (*models.CustomerDeposit, error) {
	var dep models.CustomerDeposit
	err := db.Preload("Customer").Preload("BankAccount").Preload("DepositLiabilityAccount").
		Where("id = ? AND company_id = ?", depositID, companyID).
		First(&dep).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrDepositNotFound
	}
	return &dep, err
}

// CustomerDepositListFilter bundles the optional list-page filters.
type CustomerDepositListFilter struct {
	Status     string     // empty = all statuses
	CustomerID uint       // 0 = all customers
	DateFrom   *time.Time // nil = no lower bound on deposit_date
	DateTo     *time.Time // nil = no upper bound on deposit_date
}

// ListCustomerDeposits returns deposits for a company, newest first.
func ListCustomerDeposits(db *gorm.DB, companyID uint, f CustomerDepositListFilter) ([]models.CustomerDeposit, error) {
	q := db.Preload("Customer").Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.CustomerID > 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.DateFrom != nil {
		q = q.Where("deposit_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("deposit_date <= ?", *f.DateTo)
	}
	var deps []models.CustomerDeposit
	err := q.Order("id desc").Find(&deps).Error
	return deps, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateCustomerDeposit updates a draft deposit.
func UpdateCustomerDeposit(db *gorm.DB, companyID, depositID uint, in CustomerDepositInput) (*models.CustomerDeposit, error) {
	var dep models.CustomerDeposit
	if err := db.Where("id = ? AND company_id = ?", depositID, companyID).First(&dep).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDepositNotFound
		}
		return nil, err
	}
	if dep.Status != models.CustomerDepositStatusDraft {
		return nil, fmt.Errorf("%w: only draft deposits may be edited", ErrDepositInvalidStatus)
	}

	rate := in.ExchangeRate
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	updates := map[string]any{
		"customer_id":                  in.CustomerID,
		"sales_order_id":               in.SalesOrderID,
		"bank_account_id":              in.BankAccountID,
		"deposit_liability_account_id": in.DepositLiabilityAccountID,
		"deposit_date":                 in.DepositDate,
		"currency_code":                in.CurrencyCode,
		"exchange_rate":                rate,
		"amount":                       in.Amount.Round(2),
		"payment_method":               in.PaymentMethod,
		"reference":                    in.Reference,
		"memo":                         in.Memo,
	}
	if err := db.Model(&dep).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update deposit: %w", err)
	}
	return &dep, nil
}

// ── Post ──────────────────────────────────────────────────────────────────────

// PostCustomerDeposit transitions a draft deposit to posted (unapplied) and
// creates a double-entry journal entry.
//
//	Dr  BankAccount          Amount×Rate (base)
//	Cr  DepositLiability     Amount×Rate (base)
func PostCustomerDeposit(db *gorm.DB, companyID, depositID uint, actor string, actorID *uuid.UUID) error {
	var dep models.CustomerDeposit
	if err := db.Where("id = ? AND company_id = ?", depositID, companyID).First(&dep).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDepositNotFound
		}
		return err
	}
	if dep.Status != models.CustomerDepositStatusDraft {
		return fmt.Errorf("%w: only draft deposits can be posted", ErrDepositInvalidStatus)
	}
	if dep.BankAccountID == nil || *dep.BankAccountID == 0 {
		return ErrDepositNoBank
	}

	// Compute base-currency amount.
	rate := dep.ExchangeRate
	if rate.IsZero() || rate.IsNegative() {
		rate = decimal.NewFromInt(1)
	}
	amountBase := dep.Amount.Mul(rate).Round(2)

	return db.Transaction(func(tx *gorm.DB) error {
		cash, err := resolveCashPostingCurrency(tx, companyID, *dep.BankAccountID, dep.CurrencyCode, rate)
		if err != nil {
			return err
		}
		txAmount := amountBase
		if cash.BankIsForeign {
			txAmount = dep.Amount.Round(2)
		}

		// Auto-resolve the system Customer Deposits liability account
		// when the operator didn't pick one (default in the new design).
		// The id is captured back onto the deposit row so subsequent
		// apply / void operations don't have to re-resolve.
		liabAccID := uint(0)
		if dep.DepositLiabilityAccountID != nil {
			liabAccID = *dep.DepositLiabilityAccountID
		}
		if liabAccID == 0 {
			id, err := EnsureCustomerDepositsAccount(tx, companyID)
			if err != nil {
				return fmt.Errorf("resolve customer deposits account: %w", err)
			}
			liabAccID = id
		}
		je := models.JournalEntry{
			CompanyID:               companyID,
			EntryDate:               dep.DepositDate,
			JournalNo:               "DEP – " + dep.DepositNumber,
			Status:                  models.JournalEntryStatusPosted,
			TransactionCurrencyCode: cash.TransactionCurrencyCode,
			ExchangeRate:            cash.ExchangeRate,
			ExchangeRateDate:        dep.DepositDate,
			ExchangeRateSource:      cashExchangeRateSource(cash),
			SourceType:              models.LedgerSourceCustomerDeposit,
			SourceID:                dep.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create deposit JE: %w", err)
		}

		lines := []models.JournalLine{
			// Dr Bank / Cash
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      *dep.BankAccountID,
				TxDebit:        txAmount,
				TxCredit:       decimal.Zero,
				Debit:          amountBase,
				Credit:         decimal.Zero,
				Memo:           dep.DepositNumber + " – customer deposit received",
			},
			// Cr Customer Deposits liability
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      liabAccID,
				TxDebit:        decimal.Zero,
				TxCredit:       txAmount,
				Debit:          decimal.Zero,
				Credit:         amountBase,
				Memo:           dep.DepositNumber + " – deposit liability",
			},
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create deposit JE lines: %w", err)
		}

		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceCustomerDeposit,
			SourceID:     dep.ID,
		}); err != nil {
			return fmt.Errorf("project deposit to ledger: %w", err)
		}

		now := time.Now()
		updates := map[string]any{
			"status":                       string(models.CustomerDepositStatusPosted),
			"journal_entry_id":             je.ID,
			"deposit_liability_account_id": liabAccID,
			"amount_base":                  amountBase,
			"balance_remaining":            dep.Amount.Round(2), // remaining in doc currency
			"posted_at":                    &now,
			"posted_by":                    actor,
		}
		if actorID != nil {
			updates["posted_by_user_id"] = actorID
		}
		if err := tx.Model(&dep).Updates(updates).Error; err != nil {
			return fmt.Errorf("update deposit status: %w", err)
		}
		return nil
	})
}

// ── Apply to Invoice ──────────────────────────────────────────────────────────

// ApplyDepositToInvoice applies a portion of a posted deposit to an open invoice.
//
//	Dr  DepositLiability     AmountApplied×Rate (base)
//	Cr  ARAccount            AmountApplied×Rate (base)
//
// The invoice's BalanceDue is reduced and the deposit's BalanceRemaining is
// updated. Status transitions happen automatically.
func ApplyDepositToInvoice(db *gorm.DB, companyID uint, in ApplyDepositInput, actorID *uuid.UUID) error {
	if !in.AmountApplied.IsPositive() {
		return errors.New("apply amount must be positive")
	}

	var dep models.CustomerDeposit
	if err := db.Where("id = ? AND company_id = ?", in.DepositID, companyID).First(&dep).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDepositNotFound
		}
		return err
	}
	if dep.Status != models.CustomerDepositStatusPosted &&
		dep.Status != models.CustomerDepositStatusPartiallyApplied {
		return fmt.Errorf("%w: deposit must be posted/unapplied or partially applied", ErrDepositInvalidStatus)
	}
	// Resolve the liability account: explicit pick wins, otherwise fall
	// back to the system Customer Deposits account (matches the new
	// auto-resolved create+post flow).
	liabAccID := uint(0)
	if dep.DepositLiabilityAccountID != nil {
		liabAccID = *dep.DepositLiabilityAccountID
	}
	if liabAccID == 0 {
		id, err := EnsureCustomerDepositsAccount(db, companyID)
		if err != nil {
			return fmt.Errorf("resolve customer deposits account: %w", err)
		}
		liabAccID = id
	}
	if in.AmountApplied.GreaterThan(dep.BalanceRemaining) {
		return ErrDepositInsufficientBalance
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

	// Load AR account.
	arAccount, err := ResolveControlAccount(db, companyID, 0,
		models.ARAPDocTypeInvoice, dep.CurrencyCode, dep.CurrencyCode != "" && dep.CurrencyCode != inv.CurrencyCode,
		models.DetailAccountsReceivable, ErrNoARAccount)
	if err != nil {
		return fmt.Errorf("resolve AR account: %w", err)
	}

	// Base amount for the application JE.
	rate := dep.ExchangeRate
	if rate.IsZero() || rate.IsNegative() {
		rate = decimal.NewFromInt(1)
	}
	amountAppliedBase := in.AmountApplied.Mul(rate).Round(2)

	return db.Transaction(func(tx *gorm.DB) error {
		// Create application JE.
		je := models.JournalEntry{
			CompanyID:  companyID,
			EntryDate:  time.Now(),
			JournalNo:  "DEP-APP – " + dep.DepositNumber,
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceDepositApplication,
			SourceID:   dep.ID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create application JE: %w", err)
		}

		lines := []models.JournalLine{
			// Dr Customer Deposits liability (reduces the liability)
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      liabAccID,
				Debit:          amountAppliedBase,
				Credit:         decimal.Zero,
				Memo:           fmt.Sprintf("Apply %s against invoice %s", dep.DepositNumber, inv.InvoiceNumber),
			},
			// Cr AR (reduces the invoice balance)
			{
				CompanyID:      companyID,
				JournalEntryID: je.ID,
				AccountID:      arAccount.ID,
				Debit:          decimal.Zero,
				Credit:         amountAppliedBase,
				Memo:           fmt.Sprintf("Apply deposit %s against invoice %s", dep.DepositNumber, inv.InvoiceNumber),
			},
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create application JE lines: %w", err)
		}

		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        lines,
			SourceType:   models.LedgerSourceDepositApplication,
			SourceID:     dep.ID,
		}); err != nil {
			return fmt.Errorf("project application to ledger: %w", err)
		}

		// Record the CustomerDepositApplication.
		app := models.CustomerDepositApplication{
			CompanyID:         companyID,
			CustomerDepositID: dep.ID,
			InvoiceID:         inv.ID,
			AmountApplied:     in.AmountApplied.Round(2),
			AmountAppliedBase: amountAppliedBase,
			AppliedAt:         time.Now(),
			AppliedBy:         in.Actor,
		}
		if err := tx.Create(&app).Error; err != nil {
			return fmt.Errorf("create deposit application record: %w", err)
		}

		// Update deposit balance.
		newBalance := dep.BalanceRemaining.Sub(in.AmountApplied).Round(2)
		var newDepStatus models.CustomerDepositStatus
		if newBalance.IsZero() {
			newDepStatus = models.CustomerDepositStatusFullyApplied
		} else {
			newDepStatus = models.CustomerDepositStatusPartiallyApplied
		}
		if err := tx.Model(&dep).Updates(map[string]any{
			"balance_remaining": newBalance,
			"status":            string(newDepStatus),
		}).Error; err != nil {
			return fmt.Errorf("update deposit balance: %w", err)
		}

		// Update invoice BalanceDue.
		newBalanceDue := inv.BalanceDue.Sub(in.AmountApplied).Round(2)
		if err := tx.Model(&inv).Update("balance_due", newBalanceDue).Error; err != nil {
			return fmt.Errorf("update invoice balance due: %w", err)
		}

		return nil
	})
}

// ── Void ──────────────────────────────────────────────────────────────────────

// VoidCustomerDeposit voids a draft or posted (unapplied) deposit.
//
// If posted: creates a reversal JE; marks original JE reversed.
// Applied deposits cannot be voided — unapply first.
func VoidCustomerDeposit(db *gorm.DB, companyID, depositID uint, actor string) error {
	var dep models.CustomerDeposit
	if err := db.Where("id = ? AND company_id = ?", depositID, companyID).First(&dep).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDepositNotFound
		}
		return err
	}

	switch dep.Status {
	case models.CustomerDepositStatusDraft:
		// Simply mark voided — no JE to reverse.
		return db.Model(&dep).Update("status", models.CustomerDepositStatusVoided).Error

	case models.CustomerDepositStatusPosted:
		// Posted = fully unapplied; safe to void.
		return db.Transaction(func(tx *gorm.DB) error {
			if dep.JournalEntryID == nil {
				return errors.New("deposit has no linked journal entry")
			}
			// Create reversal JE.
			reversalID, err := ReverseJournalEntry(tx, companyID, *dep.JournalEntryID, time.Now())
			if err != nil {
				return fmt.Errorf("reverse deposit JE: %w", err)
			}
			_ = reversalID

			// Mark original JE reversed.
			if err := tx.Model(&models.JournalEntry{}).
				Where("id = ? AND company_id = ?", *dep.JournalEntryID, companyID).
				Update("status", models.JournalEntryStatusReversed).Error; err != nil {
				return fmt.Errorf("mark original JE reversed: %w", err)
			}

			// Mark deposit voided.
			if err := tx.Model(&dep).Updates(map[string]any{
				"status":            string(models.CustomerDepositStatusVoided),
				"balance_remaining": decimal.Zero,
			}).Error; err != nil {
				return fmt.Errorf("void deposit: %w", err)
			}
			return nil
		})

	case models.CustomerDepositStatusPartiallyApplied, models.CustomerDepositStatusFullyApplied:
		return ErrDepositAlreadyApplied

	default:
		return fmt.Errorf("%w: cannot void from status %s", ErrDepositInvalidStatus, dep.Status)
	}
}
