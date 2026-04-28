// 遵循project_guide.md
package services

// customer_credit_service.go — Batch 16: Customer credit balance management.
//
// A CustomerCredit arises from an overpayment: when a payment amount exceeds
// the invoice BalanceDue, the excess is stored here.  Credits can later be
// applied to reduce the BalanceDue on future invoices for the same customer.
//
// Accounting note:
//   Credit creation and application do NOT generate Journal Entries.  The AR
//   account was already credited in full during the original charge/capture
//   posting (Dr GW Clearing, Cr AR for the full payment amount).  Applying the
//   credit to a future invoice is a pure business-layer operation that reduces
//   BalanceDue; the macro AR balance remains consistent across the full chain.
//
// Concurrency:
//   ApplyCustomerCreditToInvoice locks the credit row first (SELECT FOR UPDATE
//   on PostgreSQL, no-op on SQLite), then the invoice row, then re-checks both
//   RemainingAmount and BalanceDue under lock before committing.

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrCreditNotFound       = errors.New("customer credit not found or does not belong to this company")
	ErrCreditExhausted      = errors.New("customer credit is exhausted (remaining amount is zero)")
	ErrCreditAlreadyApplied = errors.New("customer credit has already been fully consumed")
	ErrCreditAmountInvalid  = errors.New("credit apply amount must be positive")
	ErrCreditExceedsBalance = errors.New("credit apply amount exceeds credit remaining balance")
	ErrCreditExceedsInvoice = errors.New("credit apply amount exceeds invoice balance due")
	ErrCreditCurrencyMismatch = errors.New("credit currency does not match invoice currency; cross-currency credit application is not supported")
	ErrCreditCustomerMismatch = errors.New("credit belongs to a different customer than the invoice")
	ErrCreditChannelInvoice   = errors.New("channel-origin invoices cannot receive customer credit application")
	ErrCreditInvoiceStatus    = errors.New("invoice status does not allow credit application")
)

// ── Query ────────────────────────────────────────────────────────────────────

// ListCustomerCredits returns active and exhausted credits for one customer,
// newest first.
func ListCustomerCredits(db *gorm.DB, companyID, customerID uint) ([]models.CustomerCredit, error) {
	var credits []models.CustomerCredit
	err := db.
		Where("company_id = ? AND customer_id = ?", companyID, customerID).
		Order("created_at DESC, id DESC").
		Find(&credits).Error
	return credits, err
}

// ListActiveCustomerCredits returns only credits with status = active.
func ListActiveCustomerCredits(db *gorm.DB, companyID, customerID uint) ([]models.CustomerCredit, error) {
	var credits []models.CustomerCredit
	err := db.
		Where("company_id = ? AND customer_id = ? AND status = ?",
			companyID, customerID, string(models.CustomerCreditActive)).
		Order("created_at ASC, id ASC").
		Find(&credits).Error
	return credits, err
}

// GetCustomerCredit loads a credit scoped to a company.
func GetCustomerCredit(db *gorm.DB, companyID, creditID uint) (*models.CustomerCredit, error) {
	var c models.CustomerCredit
	if err := db.Where("id = ? AND company_id = ?", creditID, companyID).First(&c).Error; err != nil {
		return nil, ErrCreditNotFound
	}
	return &c, nil
}

// CustomerCreditTotalRemaining sums RemainingAmount across all active credits
// for one customer.  Used for AR-facing display only; not an accounting figure.
func CustomerCreditTotalRemaining(db *gorm.DB, companyID, customerID uint) (decimal.Decimal, error) {
	type result struct{ Total decimal.Decimal }
	var r result
	err := db.Model(&models.CustomerCredit{}).
		Select("COALESCE(SUM(remaining_amount), 0) AS total").
		Where("company_id = ? AND customer_id = ? AND status = ?",
			companyID, customerID, string(models.CustomerCreditActive)).
		Scan(&r).Error
	return r.Total, err
}

// ── Validation ───────────────────────────────────────────────────────────────

// ValidateCreditApplicable checks whether creditID can be applied to invoiceID
// for the given amount.  Mirrors the transactional re-checks in
// ApplyCustomerCreditToInvoice but runs outside a transaction for UI state.
func ValidateCreditApplicable(db *gorm.DB, companyID, creditID, invoiceID uint, amount decimal.Decimal) error {
	var credit models.CustomerCredit
	if err := db.Where("id = ? AND company_id = ?", creditID, companyID).First(&credit).Error; err != nil {
		return ErrCreditNotFound
	}
	if credit.Status == models.CustomerCreditExhausted || credit.RemainingAmount.IsZero() {
		return ErrCreditExhausted
	}
	if !amount.IsPositive() {
		return ErrCreditAmountInvalid
	}
	if amount.GreaterThan(credit.RemainingAmount) {
		return ErrCreditExceedsBalance
	}

	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&inv).Error; err != nil {
		return fmt.Errorf("%w: invoice not found", ErrCreditInvoiceStatus)
	}
	if inv.ChannelOrderID != nil {
		return ErrCreditChannelInvoice
	}
	switch inv.Status {
	case models.InvoiceStatusDraft, models.InvoiceStatusVoided, models.InvoiceStatusPaid:
		return fmt.Errorf("%w: invoice is %s", ErrCreditInvoiceStatus, inv.Status)
	}
	if inv.CustomerID != credit.CustomerID {
		return ErrCreditCustomerMismatch
	}
	if !currencyCodesMatch(credit.CurrencyCode, inv.CurrencyCode) {
		return ErrCreditCurrencyMismatch
	}
	if amount.GreaterThan(inv.BalanceDue) {
		return ErrCreditExceedsInvoice
	}
	return nil
}

// currencyCodesMatch returns true when both codes are the same, treating "" as
// the company base currency (equal to other empty strings).
func currencyCodesMatch(a, b string) bool {
	return a == b
}

// ── Application ──────────────────────────────────────────────────────────────

// ApplyCustomerCreditToInvoice consumes amount from a CustomerCredit and
// reduces the target invoice's BalanceDue by the same amount.
//
// Atomicity: the credit row and invoice row are both locked (SELECT FOR UPDATE
// on PostgreSQL) before any mutation. Failure rolls back both.
//
// Constraints enforced transactionally:
//   - credit.RemainingAmount >= amount  (no over-consumption)
//   - invoice.BalanceDue    >= amount  (no over-application)
//   - credit.CustomerID == invoice.CustomerID
//   - currency codes match
//   - invoice is not a channel-origin invoice
func ApplyCustomerCreditToInvoice(db *gorm.DB, companyID, creditID, invoiceID uint, amount decimal.Decimal, actor string) error {
	if !amount.IsPositive() {
		return ErrCreditAmountInvalid
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// 1. Lock credit row.
		var credit models.CustomerCredit
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", creditID, companyID),
		).First(&credit).Error; err != nil {
			return ErrCreditNotFound
		}
		// Re-check status under lock.
		if credit.Status == models.CustomerCreditExhausted || credit.RemainingAmount.IsZero() {
			return ErrCreditExhausted
		}
		if amount.GreaterThan(credit.RemainingAmount) {
			return ErrCreditExceedsBalance
		}

		// 2. Lock invoice row.
		var inv models.Invoice
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", invoiceID, companyID),
		).First(&inv).Error; err != nil {
			return fmt.Errorf("%w: invoice not found", ErrCreditInvoiceStatus)
		}
		if inv.ChannelOrderID != nil {
			return ErrCreditChannelInvoice
		}
		switch inv.Status {
		case models.InvoiceStatusDraft, models.InvoiceStatusVoided, models.InvoiceStatusPaid:
			return fmt.Errorf("%w: invoice is %s", ErrCreditInvoiceStatus, inv.Status)
		}
		if inv.CustomerID != credit.CustomerID {
			return ErrCreditCustomerMismatch
		}
		if !currencyCodesMatch(credit.CurrencyCode, inv.CurrencyCode) {
			return ErrCreditCurrencyMismatch
		}
		if amount.GreaterThan(inv.BalanceDue) {
			return ErrCreditExceedsInvoice
		}

		// 3. Compute new credit remaining.
		newRemaining := credit.RemainingAmount.Sub(amount)
		newCreditStatus := models.CustomerCreditActive
		if newRemaining.IsZero() {
			newCreditStatus = models.CustomerCreditExhausted
		}

		// 4. Update credit.
		if err := tx.Model(&credit).Updates(map[string]any{
			"remaining_amount": newRemaining,
			"status":           string(newCreditStatus),
		}).Error; err != nil {
			return fmt.Errorf("update credit: %w", err)
		}

		// 5. Compute new invoice balance.
		newBalance := inv.BalanceDue.Sub(amount)
		var newInvStatus models.InvoiceStatus
		if newBalance.IsZero() {
			newInvStatus = models.InvoiceStatusPaid
		} else {
			newInvStatus = models.InvoiceStatusPartiallyPaid
		}

		// 6. Update invoice.
		// Credit applies only to base-currency invoices (same restriction as gateway apply;
		// FX credit cross-currency is not supported in Batch 16).
		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due":      newBalance,
			"balance_due_base": newBalance,
			"status":           string(newInvStatus),
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		// 7. Create immutable application record.
		app := models.CustomerCreditApplication{
			CompanyID:        companyID,
			CustomerCreditID: creditID,
			InvoiceID:        invoiceID,
			Amount:           amount,
		}
		if err := tx.Create(&app).Error; err != nil {
			return fmt.Errorf("create credit application: %w", err)
		}

		// 8. Audit log.
		cid := companyID
		if err := WriteAuditLogWithContextDetails(tx, "credit.applied_to_invoice", "customer_credit", creditID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"invoice_id":       invoiceID,
				"amount":           amount.StringFixed(2),
				"new_inv_balance":  newBalance.StringFixed(2),
				"new_inv_status":   string(newInvStatus),
				"credit_remaining": newRemaining.StringFixed(2),
			},
		); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}

		slog.Info("customer credit applied",
			"credit_id", creditID,
			"invoice_id", invoiceID,
			"amount", amount.StringFixed(2),
			"credit_remaining", newRemaining.StringFixed(2),
		)
		return nil
	})
}

// ListCreditApplications returns all application records for a credit.
func ListCreditApplications(db *gorm.DB, companyID, creditID uint) ([]models.CustomerCreditApplication, error) {
	var apps []models.CustomerCreditApplication
	err := db.
		Where("company_id = ? AND customer_credit_id = ?", companyID, creditID).
		Order("created_at ASC").
		Find(&apps).Error
	return apps, err
}
