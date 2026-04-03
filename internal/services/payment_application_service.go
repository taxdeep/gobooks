// 遵循project_guide.md
package services

// payment_application_service.go — Apply posted charge/capture transactions to invoices.
//
// Application reduces invoice.BalanceDue and transitions invoice status to
// partially_paid or paid. This is a separate action from posting: posting
// creates the JE (Dr GW Clearing, Cr AR); application settles the invoice.
//
// Scope (current round):
//   - Only charge/capture transactions can be applied
//   - Full application only (txn.Amount <= invoice.BalanceDue)
//   - One transaction → one invoice (no split allocation)
//   - Channel-origin invoices are blocked (they use channel clearing, not GW clearing)
//   - No overpayment / credit handling
//
// AR resolution note:
//   Current payment posting uses the first active AR account as a transitional
//   approach. Future: invoices may carry their own receivable account reference
//   for direct AR resolution during posting.
//
// Charge vs capture semantics:
//   This round treats both as receivable-settling events for direct-pay invoices.
//   Provider lifecycle differences (authorize → capture) are deferred to future
//   provider-specific connectors.
//
// Refund note:
//   Refund transactions are payment-side refund accounting (Dr Refund, Cr GW Clearing).
//   They are NOT equivalent to credit notes or invoice/revenue reversal.
//
// Status revert strategy (simplified):
//   When a full refund or full unapply restores BalanceDue to invoice.Amount,
//   the invoice reverts to InvoiceStatusIssued regardless of its previous
//   pre-payment state (sent, overdue, etc.). This is an intentional simplification:
//   the original pre-payment status (sent/overdue) is not tracked as a separate
//   "restore target." Future enhancement: store the pre-payment status on the
//   invoice (e.g. StatusBeforePayment) so revert can restore the exact prior state.

import (
	"errors"
	"fmt"
	"time"

	"gobooks/internal/models"
	"gorm.io/gorm"
)

var (
	ErrPaymentTxnNotApplicable  = errors.New("payment transaction is not applicable to an invoice")
	ErrPaymentTxnAlreadyApplied = errors.New("payment transaction has already been applied")
	ErrPaymentExceedsBalance    = errors.New("payment amount exceeds invoice balance due")
	ErrRefundExceedsTotal       = errors.New("refund would restore balance beyond invoice total")
	ErrChannelInvoiceBlocked    = errors.New("channel-origin invoices cannot receive payment gateway application in this round")
)

// ── Unified payment action state ──────────────────────────────────────────────

// PaymentActionState summarizes the three independent state dimensions of a
// payment transaction for UI rendering. Computed once per transaction in the
// handler and passed to the template via the VM.
type PaymentActionState struct {
	// Accounting: posted or not.
	IsPosted bool
	PostedJEID uint

	// Invoice application: applied or not.
	IsApplied bool

	// Action availability.
	CanPost         bool
	PostBlocker     string
	CanApply        bool
	ApplyBlocker    string
	CanRefundApply  bool
	RefundBlocker   string
	CanUnapply      bool
	UnapplyBlocker  string
}

// ComputePaymentActionState produces a unified state for one transaction.
func ComputePaymentActionState(db *gorm.DB, companyID uint, txn models.PaymentTransaction) PaymentActionState {
	s := PaymentActionState{
		IsPosted:   txn.PostedJournalEntryID != nil,
		IsApplied:  txn.AppliedInvoiceID != nil,
	}
	if txn.PostedJournalEntryID != nil {
		s.PostedJEID = *txn.PostedJournalEntryID
	}

	postErr := ValidatePaymentTransactionPostable(db, companyID, txn.ID)
	s.CanPost = postErr == nil
	if postErr != nil { s.PostBlocker = postErr.Error() }

	applyErr := ValidatePaymentTransactionApplicable(db, companyID, txn.ID)
	s.CanApply = applyErr == nil
	if applyErr != nil { s.ApplyBlocker = applyErr.Error() }

	refundErr := ValidateRefundTransactionApplicable(db, companyID, txn.ID)
	s.CanRefundApply = refundErr == nil
	if refundErr != nil { s.RefundBlocker = refundErr.Error() }

	unapplyErr := ValidatePaymentTransactionUnapplicable(db, companyID, txn.ID)
	s.CanUnapply = unapplyErr == nil
	if unapplyErr != nil { s.UnapplyBlocker = unapplyErr.Error() }

	return s
}

// applicableTransactionTypes are the types that can be applied to invoices.
var applicableTransactionTypes = map[models.PaymentTransactionType]bool{
	models.TxnTypeCharge:  true,
	models.TxnTypeCapture: true,
}

// ── Validation ───────────────────────────────────────────────────────────────

// ApplicationBlocker returns a human-readable reason why the transaction can't
// be applied, or empty string if it can.
func ApplicationBlocker(db *gorm.DB, companyID, txnID uint) string {
	err := ValidatePaymentTransactionApplicable(db, companyID, txnID)
	if err == nil {
		return ""
	}
	return err.Error()
}

// ValidatePaymentTransactionApplicable checks if a transaction can be applied.
func ValidatePaymentTransactionApplicable(db *gorm.DB, companyID, txnID uint) error {
	var txn models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return fmt.Errorf("transaction not found")
	}

	if txn.PostedJournalEntryID == nil {
		return fmt.Errorf("%w: not yet posted", ErrPaymentTxnNotApplicable)
	}
	if txn.AppliedInvoiceID != nil {
		return ErrPaymentTxnAlreadyApplied
	}
	if !applicableTransactionTypes[txn.TransactionType] {
		return fmt.Errorf("%w: only charge/capture can be applied (type=%s)", ErrPaymentTxnNotApplicable, txn.TransactionType)
	}
	if txn.Amount.IsZero() || txn.Amount.IsNegative() {
		return fmt.Errorf("%w: amount must be positive", ErrPaymentTxnNotApplicable)
	}

	// Resolve invoice via payment request.
	if txn.PaymentRequestID == nil {
		return fmt.Errorf("%w: no payment request linkage", ErrPaymentTxnNotApplicable)
	}
	var req models.PaymentRequest
	if err := db.Where("id = ? AND company_id = ?", *txn.PaymentRequestID, companyID).First(&req).Error; err != nil {
		return fmt.Errorf("%w: payment request not found", ErrPaymentTxnNotApplicable)
	}
	if req.InvoiceID == nil {
		return fmt.Errorf("%w: payment request has no invoice linkage", ErrPaymentTxnNotApplicable)
	}

	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", *req.InvoiceID, companyID).First(&inv).Error; err != nil {
		return fmt.Errorf("%w: invoice not found", ErrPaymentTxnNotApplicable)
	}

	// Block channel-origin invoices.
	if inv.ChannelOrderID != nil {
		return ErrChannelInvoiceBlocked
	}

	// Block non-payable invoice statuses.
	switch inv.Status {
	case models.InvoiceStatusDraft:
		return fmt.Errorf("%w: invoice is still a draft", ErrPaymentTxnNotApplicable)
	case models.InvoiceStatusVoided:
		return fmt.Errorf("%w: invoice is voided", ErrPaymentTxnNotApplicable)
	case models.InvoiceStatusPaid:
		return fmt.Errorf("%w: invoice is already fully paid", ErrPaymentTxnNotApplicable)
	}

	// Amount check.
	if txn.Amount.GreaterThan(inv.BalanceDue) {
		return fmt.Errorf("%w: payment %s > balance due %s",
			ErrPaymentExceedsBalance, txn.Amount.StringFixed(2), inv.BalanceDue.StringFixed(2))
	}

	return nil
}

// ── Application ──────────────────────────────────────────────────────────────

// ApplyPaymentTransactionToInvoice reduces the invoice balance and updates status.
// Does NOT create new JE — the JE was already created during posting.
func ApplyPaymentTransactionToInvoice(db *gorm.DB, companyID, txnID uint, actor string) error {
	if err := ValidatePaymentTransactionApplicable(db, companyID, txnID); err != nil {
		return err
	}

	var txn models.PaymentTransaction
	db.First(&txn, txnID)
	var req models.PaymentRequest
	db.First(&req, *txn.PaymentRequestID)
	invoiceID := *req.InvoiceID

	return db.Transaction(func(tx *gorm.DB) error {
		// Lock invoice for update.
		var inv models.Invoice
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", invoiceID, companyID),
		).First(&inv).Error; err != nil {
			return fmt.Errorf("lock invoice: %w", err)
		}

		// Re-check balance under lock.
		if txn.Amount.GreaterThan(inv.BalanceDue) {
			return ErrPaymentExceedsBalance
		}

		// Reduce balance.
		newBalance := inv.BalanceDue.Sub(txn.Amount)

		// Determine new status.
		var newStatus models.InvoiceStatus
		if newBalance.IsZero() {
			newStatus = models.InvoiceStatusPaid
		} else {
			newStatus = models.InvoiceStatusPartiallyPaid
		}

		// Update invoice.
		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due": newBalance,
			"status":      string(newStatus),
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		// Mark transaction as applied.
		now := time.Now()
		if err := tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txnID, companyID).
			Updates(map[string]any{
				"applied_invoice_id": invoiceID,
				"applied_at":        now,
			}).Error; err != nil {
			return fmt.Errorf("mark applied: %w", err)
		}

		// Audit log.
		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "payment.applied_to_invoice", "payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"invoice_id":  invoiceID,
				"amount":      txn.Amount.StringFixed(2),
				"new_balance": newBalance.StringFixed(2),
				"new_status":  string(newStatus),
			},
		)
	})
}

// ── Refund application ───────────────────────────────────────────────────────

// RefundApplicationBlocker returns the blocker reason, or empty if applicable.
func RefundApplicationBlocker(db *gorm.DB, companyID, txnID uint) string {
	err := ValidateRefundTransactionApplicable(db, companyID, txnID)
	if err == nil {
		return ""
	}
	return err.Error()
}

// ValidateRefundTransactionApplicable checks if a refund transaction can be
// applied to its linked invoice (restoring BalanceDue).
func ValidateRefundTransactionApplicable(db *gorm.DB, companyID, txnID uint) error {
	var txn models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return fmt.Errorf("transaction not found")
	}

	if txn.PostedJournalEntryID == nil {
		return fmt.Errorf("%w: not yet posted", ErrPaymentTxnNotApplicable)
	}
	if txn.AppliedInvoiceID != nil {
		return ErrPaymentTxnAlreadyApplied
	}
	if txn.TransactionType != models.TxnTypeRefund {
		return fmt.Errorf("%w: only refund transactions can be refund-applied (type=%s)", ErrPaymentTxnNotApplicable, txn.TransactionType)
	}
	if txn.Amount.IsZero() || txn.Amount.IsNegative() {
		return fmt.Errorf("%w: amount must be positive", ErrPaymentTxnNotApplicable)
	}

	// Resolve invoice via payment request.
	if txn.PaymentRequestID == nil {
		return fmt.Errorf("%w: no payment request linkage", ErrPaymentTxnNotApplicable)
	}
	var req models.PaymentRequest
	if err := db.Where("id = ? AND company_id = ?", *txn.PaymentRequestID, companyID).First(&req).Error; err != nil {
		return fmt.Errorf("%w: payment request not found", ErrPaymentTxnNotApplicable)
	}
	if req.InvoiceID == nil {
		return fmt.Errorf("%w: payment request has no invoice linkage", ErrPaymentTxnNotApplicable)
	}

	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", *req.InvoiceID, companyID).First(&inv).Error; err != nil {
		return fmt.Errorf("%w: invoice not found", ErrPaymentTxnNotApplicable)
	}

	if inv.ChannelOrderID != nil {
		return ErrChannelInvoiceBlocked
	}
	if inv.Status == models.InvoiceStatusDraft || inv.Status == models.InvoiceStatusVoided {
		return fmt.Errorf("%w: invoice is %s", ErrPaymentTxnNotApplicable, inv.Status)
	}

	// Refund must not push BalanceDue above invoice.Amount.
	newBalance := inv.BalanceDue.Add(txn.Amount)
	if newBalance.GreaterThan(inv.Amount) {
		return fmt.Errorf("%w: refund %s + balance %s > total %s",
			ErrRefundExceedsTotal, txn.Amount.StringFixed(2), inv.BalanceDue.StringFixed(2), inv.Amount.StringFixed(2))
	}

	return nil
}

// ApplyRefundTransactionToInvoice increases the invoice balance and may revert
// status from paid/partially_paid. Does NOT create new JE.
func ApplyRefundTransactionToInvoice(db *gorm.DB, companyID, txnID uint, actor string) error {
	if err := ValidateRefundTransactionApplicable(db, companyID, txnID); err != nil {
		return err
	}

	var txn models.PaymentTransaction
	db.First(&txn, txnID)
	var req models.PaymentRequest
	db.First(&req, *txn.PaymentRequestID)
	invoiceID := *req.InvoiceID

	return db.Transaction(func(tx *gorm.DB) error {
		var inv models.Invoice
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", invoiceID, companyID),
		).First(&inv).Error; err != nil {
			return fmt.Errorf("lock invoice: %w", err)
		}

		newBalance := inv.BalanceDue.Add(txn.Amount)
		if newBalance.GreaterThan(inv.Amount) {
			return ErrRefundExceedsTotal
		}

		var newStatus models.InvoiceStatus
		if newBalance.Equal(inv.Amount) {
			newStatus = models.InvoiceStatusIssued
		} else if newBalance.IsPositive() {
			newStatus = models.InvoiceStatusPartiallyPaid
		} else {
			newStatus = inv.Status
		}

		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due": newBalance,
			"status":      string(newStatus),
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		now := time.Now()
		if err := tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txnID, companyID).
			Updates(map[string]any{
				"applied_invoice_id": invoiceID,
				"applied_at":        now,
			}).Error; err != nil {
			return fmt.Errorf("mark refund applied: %w", err)
		}

		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "payment.refund_applied_to_invoice", "payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"invoice_id":  invoiceID,
				"amount":      txn.Amount.StringFixed(2),
				"new_balance": newBalance.StringFixed(2),
				"new_status":  string(newStatus),
			},
		)
	})
}

// ── Unapply (reverse a previous charge/capture application) ──────────────────

var ErrPaymentTxnNotUnapplicable = errors.New("payment transaction cannot be unapplied")

// UnapplyBlocker returns a human-readable reason, or empty if unapplicable.
func UnapplyBlocker(db *gorm.DB, companyID, txnID uint) string {
	err := ValidatePaymentTransactionUnapplicable(db, companyID, txnID)
	if err == nil {
		return ""
	}
	return err.Error()
}

// ValidatePaymentTransactionUnapplicable checks if a previously applied
// charge/capture transaction can be unapplied (reversed at the invoice state
// layer only — no JE changes).
func ValidatePaymentTransactionUnapplicable(db *gorm.DB, companyID, txnID uint) error {
	var txn models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return fmt.Errorf("transaction not found")
	}
	if txn.PostedJournalEntryID == nil {
		return fmt.Errorf("%w: not posted", ErrPaymentTxnNotUnapplicable)
	}
	if txn.AppliedInvoiceID == nil {
		return fmt.Errorf("%w: not applied", ErrPaymentTxnNotUnapplicable)
	}
	if !applicableTransactionTypes[txn.TransactionType] {
		return fmt.Errorf("%w: only charge/capture can be unapplied (type=%s)", ErrPaymentTxnNotUnapplicable, txn.TransactionType)
	}

	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", *txn.AppliedInvoiceID, companyID).First(&inv).Error; err != nil {
		return fmt.Errorf("%w: invoice not found", ErrPaymentTxnNotUnapplicable)
	}
	if inv.ChannelOrderID != nil {
		return ErrChannelInvoiceBlocked
	}
	if inv.Status == models.InvoiceStatusDraft || inv.Status == models.InvoiceStatusVoided {
		return fmt.Errorf("%w: invoice is %s", ErrPaymentTxnNotUnapplicable, inv.Status)
	}

	// Unapply must not push BalanceDue above Amount.
	newBalance := inv.BalanceDue.Add(txn.Amount)
	if newBalance.GreaterThan(inv.Amount) {
		return fmt.Errorf("%w: would exceed invoice total", ErrPaymentTxnNotUnapplicable)
	}

	return nil
}

// UnapplyPaymentTransaction reverses a charge/capture application: restores
// invoice BalanceDue and clears the transaction's applied state.
// Does NOT create or modify any JE.
func UnapplyPaymentTransaction(db *gorm.DB, companyID, txnID uint, actor string) error {
	if err := ValidatePaymentTransactionUnapplicable(db, companyID, txnID); err != nil {
		return err
	}

	var txn models.PaymentTransaction
	db.First(&txn, txnID)
	invoiceID := *txn.AppliedInvoiceID

	return db.Transaction(func(tx *gorm.DB) error {
		var inv models.Invoice
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", invoiceID, companyID),
		).First(&inv).Error; err != nil {
			return fmt.Errorf("lock invoice: %w", err)
		}

		newBalance := inv.BalanceDue.Add(txn.Amount)
		if newBalance.GreaterThan(inv.Amount) {
			return fmt.Errorf("unapply would exceed invoice total")
		}

		var newStatus models.InvoiceStatus
		if newBalance.Equal(inv.Amount) {
			newStatus = models.InvoiceStatusIssued
		} else if newBalance.IsPositive() && !newBalance.Equal(inv.Amount) {
			newStatus = models.InvoiceStatusPartiallyPaid
		} else {
			newStatus = inv.Status
		}

		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due": newBalance,
			"status":      string(newStatus),
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		// Clear applied state.
		if err := tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txnID, companyID).
			Updates(map[string]any{
				"applied_invoice_id": nil,
				"applied_at":        nil,
			}).Error; err != nil {
			return fmt.Errorf("clear applied state: %w", err)
		}

		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "payment.unapplied_from_invoice", "payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"invoice_id":  invoiceID,
				"amount":      txn.Amount.StringFixed(2),
				"new_balance": newBalance.StringFixed(2),
				"new_status":  string(newStatus),
			},
		)
	})
}
