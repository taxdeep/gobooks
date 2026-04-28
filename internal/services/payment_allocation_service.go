// 遵循project_guide.md
package services

// payment_allocation_service.go — Batch 17: Multi-invoice payment allocation.
//
// Provides two allocation entry points:
//
//  1. AllocatePaymentToMultipleInvoices: splits a single charge/capture
//     transaction across multiple invoices for the same customer.
//
//  2. AllocateCustomerCreditToMultipleInvoices: applies a single CustomerCredit
//     across multiple invoices for the same customer.
//
// Invariants:
//   - Company isolation: all objects must share companyID.
//   - Customer isolation: all target invoices must belong to the same customer
//     as the payment/credit source.
//   - No over-allocation: Σ allocation amounts ≤ source remaining.
//   - No over-application: each invoice allocation ≤ invoice.BalanceDue.
//   - Unique constraint (uq_payment_alloc) prevents duplicate (txn, invoice) pairs.
//   - Locking order: source row first (lowest ID), then invoices in ascending ID
//     order to prevent deadlocks.
//   - No new JE is created; accounting was settled during posting.
//
// Compatibility:
//   - The single-invoice path (ApplyPaymentTransactionToInvoice) sets
//     PaymentTransaction.AppliedInvoiceID and does NOT create PaymentAllocation rows.
//   - This multi-invoice path creates PaymentAllocation rows and leaves
//     PaymentTransaction.AppliedInvoiceID nil.
//   - The two paths are mutually exclusive per transaction.

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrAllocNoLines                = errors.New("allocation must include at least one invoice line")
	ErrAllocZeroAmount             = errors.New("each allocation line amount must be positive")
	ErrAllocExceedsRemaining       = errors.New("total allocation amount exceeds payment remaining balance")
	ErrAllocExceedsInvoiceBalance  = errors.New("allocation amount exceeds invoice balance due")
	ErrAllocInvoiceStatus          = errors.New("invoice status does not allow allocation")
	ErrAllocCustomerMismatch       = errors.New("all invoices must belong to the same customer as the payment source")
	ErrAllocChannelInvoice         = errors.New("channel-origin invoices cannot receive payment allocation")
	ErrAllocCurrencyMismatch       = errors.New("invoice currency does not match credit currency; cross-currency allocation is not supported")
	ErrAllocTxnNotAllocatable      = errors.New("payment transaction is not allocatable: must be a posted charge or capture with no prior single-invoice apply")
	ErrAllocDuplicateInvoice       = errors.New("duplicate invoice in allocation lines")
	ErrCreditAllocDuplicateInvoice = errors.New("customer credit already has a multi-allocation for this invoice")
)

// AllocationLine specifies how much of a source (payment or credit) to apply to
// one invoice.
type AllocationLine struct {
	InvoiceID uint
	Amount    decimal.Decimal
}

// ── Payment multi-allocation query helpers ────────────────────────────────────

// PaymentAllocatedTotal returns the sum already allocated from txnID via
// PaymentAllocation records.  Returns zero when no allocations exist.
func PaymentAllocatedTotal(db *gorm.DB, companyID, txnID uint) decimal.Decimal {
	type res struct{ Total decimal.Decimal }
	var r res
	db.Model(&models.PaymentAllocation{}).
		Select("COALESCE(SUM(allocated_amount), 0) AS total").
		Where("company_id = ? AND payment_transaction_id = ?", companyID, txnID).
		Scan(&r)
	return r.Total
}

// ListPaymentAllocations returns all allocation records for one transaction.
func ListPaymentAllocations(db *gorm.DB, companyID, txnID uint) ([]models.PaymentAllocation, error) {
	var rows []models.PaymentAllocation
	err := db.
		Where("company_id = ? AND payment_transaction_id = ?", companyID, txnID).
		Order("id ASC").
		Find(&rows).Error
	return rows, err
}

// ── Validation ────────────────────────────────────────────────────────────────

// ValidatePaymentAllocatable checks if txnID can accept multi-invoice allocations.
// Separate from ValidatePaymentTransactionApplicable (single-invoice path).
func ValidatePaymentAllocatable(db *gorm.DB, companyID, txnID uint) error {
	var txn models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return fmt.Errorf("transaction not found")
	}
	if txn.PostedJournalEntryID == nil {
		return fmt.Errorf("%w: transaction not yet posted", ErrAllocTxnNotAllocatable)
	}
	if !applicableTransactionTypes[txn.TransactionType] {
		return fmt.Errorf("%w: type must be charge or capture (got %s)", ErrAllocTxnNotAllocatable, txn.TransactionType)
	}
	if txn.Amount.IsZero() || txn.Amount.IsNegative() {
		return fmt.Errorf("%w: transaction amount must be positive", ErrAllocTxnNotAllocatable)
	}
	// Block if already applied via single-invoice path.
	if txn.AppliedInvoiceID != nil {
		return fmt.Errorf("%w: transaction already applied via single-invoice path", ErrAllocTxnNotAllocatable)
	}
	if _, _, err := paymentTransactionSourceCustomerID(db, companyID, txn); err != nil {
		return err
	}
	return nil
}

// validateAllocationLines checks the lines slice for basic structural validity
// (no empty set, no zero amounts, no duplicate invoice IDs).
func validateAllocationLines(lines []AllocationLine) error {
	if len(lines) == 0 {
		return ErrAllocNoLines
	}
	seen := make(map[uint]struct{}, len(lines))
	for _, l := range lines {
		if !l.Amount.IsPositive() {
			return fmt.Errorf("%w (invoice %d: %s)", ErrAllocZeroAmount, l.InvoiceID, l.Amount.StringFixed(2))
		}
		if _, dup := seen[l.InvoiceID]; dup {
			return fmt.Errorf("%w: invoice %d appears more than once", ErrAllocDuplicateInvoice, l.InvoiceID)
		}
		seen[l.InvoiceID] = struct{}{}
	}
	return nil
}

// sortedInvoiceIDs returns the invoice IDs from lines in ascending order for
// consistent locking (prevents deadlocks when concurrent allocation runs overlap).
func sortedInvoiceIDs(lines []AllocationLine) []uint {
	ids := make([]uint, len(lines))
	for i, l := range lines {
		ids[i] = l.InvoiceID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// paymentTransactionSourceCustomerID resolves the source customer for a payment
// transaction when the transaction is linked to a payment request. A missing
// request/invoice linkage is treated as a blocker because the backend can no
// longer prove the source/customer relationship.
func paymentTransactionSourceCustomerID(db *gorm.DB, companyID uint, txn models.PaymentTransaction) (uint, bool, error) {
	if txn.PaymentRequestID == nil {
		return 0, false, nil
	}

	var req models.PaymentRequest
	if err := db.Where("id = ? AND company_id = ?", *txn.PaymentRequestID, companyID).First(&req).Error; err != nil {
		return 0, false, fmt.Errorf("%w: payment request not found", ErrAllocTxnNotAllocatable)
	}
	if req.CustomerID != nil {
		return *req.CustomerID, true, nil
	}
	if req.InvoiceID != nil {
		var inv models.Invoice
		if err := db.Where("id = ? AND company_id = ?", *req.InvoiceID, companyID).First(&inv).Error; err != nil {
			return 0, false, fmt.Errorf("%w: payment request invoice not found", ErrAllocTxnNotAllocatable)
		}
		return inv.CustomerID, true, nil
	}
	return 0, false, nil
}

func creditAlreadyAppliedToInvoice(db *gorm.DB, companyID, creditID, invoiceID uint) (bool, error) {
	var count int64
	if err := db.Model(&models.CustomerCreditApplication{}).
		Where("company_id = ? AND customer_credit_id = ? AND invoice_id = ?", companyID, creditID, invoiceID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ── Payment multi-allocation ──────────────────────────────────────────────────

// AllocatePaymentToMultipleInvoices distributes a portion of a payment
// transaction across multiple invoices in a single atomic transaction.
//
// Business rules enforced:
//   - txn must be a posted charge/capture not yet applied via single-invoice path
//   - all invoices must be same company, same customer as the payment's customer
//   - Σ lines[i].Amount ≤ txn.Amount − already-allocated total
//   - each line amount ≤ invoice.BalanceDue at commit time
//   - invoices must be in allocatable status (not draft/voided/paid)
//
// On success each invoice's BalanceDue is reduced and a PaymentAllocation record
// is inserted.  The unique index blocks a duplicate (txn, invoice) submit.
func AllocatePaymentToMultipleInvoices(
	db *gorm.DB,
	companyID uint,
	txnID uint,
	lines []AllocationLine,
	actor string,
) error {
	// Pre-transaction structural validation.
	if err := validateAllocationLines(lines); err != nil {
		return err
	}
	if err := ValidatePaymentAllocatable(db, companyID, txnID); err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// 1. Lock the source transaction (lowest lock first).
		var txn models.PaymentTransaction
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", txnID, companyID),
		).First(&txn).Error; err != nil {
			return fmt.Errorf("lock payment transaction: %w", err)
		}
		// Re-check under lock.
		if txn.AppliedInvoiceID != nil {
			return fmt.Errorf("%w: transaction already applied via single-invoice path", ErrAllocTxnNotAllocatable)
		}
		sourceCustomerID, sourceHasCustomer, err := paymentTransactionSourceCustomerID(tx, companyID, txn)
		if err != nil {
			return err
		}

		// 2. Compute remaining allocatable amount under lock.
		alreadyAllocated := PaymentAllocatedTotal(tx, companyID, txnID)
		remaining := txn.Amount.Sub(alreadyAllocated)

		totalRequested := decimal.Zero
		for _, l := range lines {
			totalRequested = totalRequested.Add(l.Amount)
		}
		if totalRequested.GreaterThan(remaining) {
			return fmt.Errorf("%w: requested %s > remaining %s",
				ErrAllocExceedsRemaining, totalRequested.StringFixed(2), remaining.StringFixed(2))
		}

		// 3. Lock all target invoices in ascending ID order (deadlock prevention).
		invoiceMap := make(map[uint]models.Invoice, len(lines))
		for _, id := range sortedInvoiceIDs(lines) {
			var inv models.Invoice
			if err := applyLockForUpdate(
				tx.Where("id = ? AND company_id = ?", id, companyID),
			).First(&inv).Error; err != nil {
				return fmt.Errorf("lock invoice %d: %w", id, err)
			}
			invoiceMap[inv.ID] = inv
		}

		// 4. Validate each line against its locked invoice.
		// Resolve customer from the source when available; otherwise all targets
		// must at least stay within one customer boundary.
		var expectedCustomerID uint
		for i, l := range lines {
			inv := invoiceMap[l.InvoiceID]
			if sourceHasCustomer {
				expectedCustomerID = sourceCustomerID
			} else if i == 0 {
				expectedCustomerID = inv.CustomerID
			}
			if err := validateInvoiceForAllocation(inv, l.Amount, expectedCustomerID); err != nil {
				return fmt.Errorf("invoice %d: %w", l.InvoiceID, err)
			}
		}

		// 5. Apply all changes.
		for _, l := range lines {
			inv := invoiceMap[l.InvoiceID]
			newBalance := inv.BalanceDue.Sub(l.Amount)

			var newStatus models.InvoiceStatus
			if newBalance.IsZero() {
				newStatus = models.InvoiceStatusPaid
			} else {
				newStatus = models.InvoiceStatusPartiallyPaid
			}

			if err := tx.Model(&inv).Updates(map[string]any{
				"balance_due":      newBalance,
				"balance_due_base": newBalance,
				"status":           string(newStatus),
			}).Error; err != nil {
				return fmt.Errorf("update invoice %d: %w", l.InvoiceID, err)
			}

			alloc := models.PaymentAllocation{
				CompanyID:            companyID,
				PaymentTransactionID: txnID,
				InvoiceID:            l.InvoiceID,
				AllocatedAmount:      l.Amount,
			}
			if err := tx.Create(&alloc).Error; err != nil {
				if isUniqueConstraintError(err) {
					return fmt.Errorf("invoice %d: %w", l.InvoiceID, ErrAllocDuplicateInvoice)
				}
				return fmt.Errorf("create allocation for invoice %d: %w", l.InvoiceID, err)
			}
		}

		// 6. Audit log.
		cid := companyID
		lineDetails := make([]map[string]any, len(lines))
		for i, l := range lines {
			lineDetails[i] = map[string]any{
				"invoice_id": l.InvoiceID,
				"amount":     l.Amount.StringFixed(2),
			}
		}
		if err := WriteAuditLogWithContextDetails(tx,
			"payment.multi_allocated",
			"payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"total_allocated": totalRequested.StringFixed(2),
				"lines":           lineDetails,
			},
		); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}

		slog.Info("payment multi-allocated",
			"txn_id", txnID,
			"total", totalRequested.StringFixed(2),
			"invoice_count", len(lines),
		)
		return nil
	})
}

// ── Customer credit multi-allocation ─────────────────────────────────────────

// AllocateCustomerCreditToMultipleInvoices applies a single CustomerCredit
// across multiple invoices in a single atomic transaction.
//
// Business rules enforced:
//   - credit must be active (not exhausted)
//   - all invoices must be same company, same customer as the credit
//   - same currency (cross-currency not supported)
//   - Σ lines[i].Amount ≤ credit.RemainingAmount
//   - each line amount ≤ invoice.BalanceDue
//   - invoices must be in allocatable status
//
// On success each invoice's BalanceDue is reduced, a CustomerCreditApplication
// record is inserted per invoice, and credit.RemainingAmount is decremented.
func AllocateCustomerCreditToMultipleInvoices(
	db *gorm.DB,
	companyID uint,
	creditID uint,
	lines []AllocationLine,
	actor string,
) error {
	// Pre-transaction structural validation.
	if err := validateAllocationLines(lines); err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// 1. Lock the source credit.
		var credit models.CustomerCredit
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", creditID, companyID),
		).First(&credit).Error; err != nil {
			return ErrCreditNotFound
		}
		// Re-check under lock.
		if credit.Status == models.CustomerCreditExhausted || credit.RemainingAmount.IsZero() {
			return ErrCreditExhausted
		}

		totalRequested := decimal.Zero
		for _, l := range lines {
			totalRequested = totalRequested.Add(l.Amount)
		}
		if totalRequested.GreaterThan(credit.RemainingAmount) {
			return fmt.Errorf("%w: requested %s > remaining %s",
				ErrCreditExceedsBalance, totalRequested.StringFixed(2), credit.RemainingAmount.StringFixed(2))
		}

		// 2. Lock all target invoices in ascending ID order.
		invoiceMap := make(map[uint]models.Invoice, len(lines))
		for _, id := range sortedInvoiceIDs(lines) {
			var inv models.Invoice
			if err := applyLockForUpdate(
				tx.Where("id = ? AND company_id = ?", id, companyID),
			).First(&inv).Error; err != nil {
				return fmt.Errorf("lock invoice %d: %w", id, err)
			}
			invoiceMap[inv.ID] = inv
		}

		// 3. Validate each line.
		for i, l := range lines {
			inv := invoiceMap[l.InvoiceID]
			// Customer isolation.
			if i == 0 {
				// (will be checked per-line below via ErrCreditCustomerMismatch)
			}
			if inv.CustomerID != credit.CustomerID {
				return fmt.Errorf("invoice %d: %w", l.InvoiceID, ErrCreditCustomerMismatch)
			}
			alreadyApplied, checkErr := creditAlreadyAppliedToInvoice(tx, companyID, creditID, l.InvoiceID)
			if checkErr != nil {
				return fmt.Errorf("invoice %d: check existing credit applications: %w", l.InvoiceID, checkErr)
			}
			if alreadyApplied {
				return fmt.Errorf("invoice %d: %w", l.InvoiceID, ErrCreditAllocDuplicateInvoice)
			}
			if !currencyCodesMatch(credit.CurrencyCode, inv.CurrencyCode) {
				return fmt.Errorf("invoice %d: %w", l.InvoiceID, ErrAllocCurrencyMismatch)
			}
			if inv.ChannelOrderID != nil {
				return fmt.Errorf("invoice %d: %w", l.InvoiceID, ErrAllocChannelInvoice)
			}
			switch inv.Status {
			case models.InvoiceStatusDraft, models.InvoiceStatusVoided, models.InvoiceStatusPaid:
				return fmt.Errorf("invoice %d: %w (status=%s)", l.InvoiceID, ErrAllocInvoiceStatus, inv.Status)
			}
			if l.Amount.GreaterThan(inv.BalanceDue) {
				return fmt.Errorf("invoice %d: %w (amount=%s, balance=%s)",
					l.InvoiceID, ErrAllocExceedsInvoiceBalance, l.Amount.StringFixed(2), inv.BalanceDue.StringFixed(2))
			}
		}

		// 4. Apply all changes.
		for _, l := range lines {
			inv := invoiceMap[l.InvoiceID]
			newBalance := inv.BalanceDue.Sub(l.Amount)

			var newStatus models.InvoiceStatus
			if newBalance.IsZero() {
				newStatus = models.InvoiceStatusPaid
			} else {
				newStatus = models.InvoiceStatusPartiallyPaid
			}

			if err := tx.Model(&inv).Updates(map[string]any{
				"balance_due":      newBalance,
				"balance_due_base": newBalance,
				"status":           string(newStatus),
			}).Error; err != nil {
				return fmt.Errorf("update invoice %d: %w", l.InvoiceID, err)
			}

			app := models.CustomerCreditApplication{
				CompanyID:        companyID,
				CustomerCreditID: creditID,
				InvoiceID:        l.InvoiceID,
				Amount:           l.Amount,
			}
			if err := tx.Create(&app).Error; err != nil {
				return fmt.Errorf("create credit application for invoice %d: %w", l.InvoiceID, err)
			}
		}

		// 5. Update credit remaining amount.
		newRemaining := credit.RemainingAmount.Sub(totalRequested)
		newCreditStatus := models.CustomerCreditActive
		if newRemaining.IsZero() {
			newCreditStatus = models.CustomerCreditExhausted
		}
		if err := tx.Model(&credit).Updates(map[string]any{
			"remaining_amount": newRemaining,
			"status":           string(newCreditStatus),
		}).Error; err != nil {
			return fmt.Errorf("update credit remaining: %w", err)
		}

		// 6. Audit log.
		cid := companyID
		lineDetails := make([]map[string]any, len(lines))
		for i, l := range lines {
			lineDetails[i] = map[string]any{
				"invoice_id": l.InvoiceID,
				"amount":     l.Amount.StringFixed(2),
			}
		}
		if err := WriteAuditLogWithContextDetails(tx,
			"credit.multi_allocated",
			"customer_credit", creditID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"total_applied":    totalRequested.StringFixed(2),
				"credit_remaining": newRemaining.StringFixed(2),
				"lines":            lineDetails,
			},
		); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}

		slog.Info("customer credit multi-allocated",
			"credit_id", creditID,
			"total", totalRequested.StringFixed(2),
			"invoice_count", len(lines),
		)
		return nil
	})
}

// ── Shared invoice validation ─────────────────────────────────────────────────

// validateInvoiceForAllocation checks that a locked invoice can accept a
// payment allocation of amount, given the expected customer.
func validateInvoiceForAllocation(inv models.Invoice, amount decimal.Decimal, expectedCustomerID uint) error {
	if inv.CustomerID != expectedCustomerID {
		return ErrAllocCustomerMismatch
	}
	if inv.ChannelOrderID != nil {
		return ErrAllocChannelInvoice
	}
	switch inv.Status {
	case models.InvoiceStatusDraft, models.InvoiceStatusVoided, models.InvoiceStatusPaid:
		return fmt.Errorf("%w (status=%s)", ErrAllocInvoiceStatus, inv.Status)
	}
	if amount.GreaterThan(inv.BalanceDue) {
		return fmt.Errorf("%w (amount=%s, balance=%s)",
			ErrAllocExceedsInvoiceBalance, amount.StringFixed(2), inv.BalanceDue.StringFixed(2))
	}
	return nil
}

// ── Open invoices helper for UI ───────────────────────────────────────────────

// ListAllocatableInvoicesForCustomer returns invoices for a customer that can
// receive payment or credit allocations (status: issued, sent, partially_paid, overdue).
// Excludes voided, draft, paid, and channel-origin invoices.
func ListAllocatableInvoicesForCustomer(db *gorm.DB, companyID, customerID uint) ([]models.Invoice, error) {
	var invoices []models.Invoice
	err := db.
		Where("company_id = ? AND customer_id = ? AND status IN ? AND channel_order_id IS NULL",
			companyID, customerID,
			[]string{
				string(models.InvoiceStatusIssued),
				string(models.InvoiceStatusSent),
				string(models.InvoiceStatusPartiallyPaid),
				string(models.InvoiceStatusOverdue),
			}).
		Order("invoice_date ASC, id ASC").
		Find(&invoices).Error
	return invoices, err
}
