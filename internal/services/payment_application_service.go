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
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

var (
	ErrPaymentTxnNotApplicable  = errors.New("payment transaction is not applicable to an invoice")
	ErrPaymentTxnAlreadyApplied = errors.New("payment transaction has already been applied")
	ErrPaymentExceedsBalance    = errors.New("payment amount exceeds invoice balance due")
	ErrRefundExceedsTotal       = errors.New("refund would restore balance beyond invoice total")
	ErrChargebackExceedsTotal   = errors.New("chargeback would restore balance beyond invoice total")
	ErrChannelInvoiceBlocked    = errors.New("channel-origin invoices cannot receive payment gateway application in this round")
)

// ── Unified payment action state ──────────────────────────────────────────────

// PaymentActionState summarizes the three independent state dimensions of a
// payment transaction for UI rendering. Computed once per transaction in the
// handler and passed to the template via the VM.
type PaymentActionState struct {
	// Accounting: posted or not.
	IsPosted   bool
	PostedJEID uint

	// Invoice application: applied or not (single-invoice path).
	IsApplied bool

	// Multi-allocation: has PaymentAllocation records (Batch 17+).
	IsMultiAllocated    bool
	MultiAllocatedTotal string // formatted string of total allocated so far

	// Reverse allocation (Batch 22): has PaymentReverseAllocation records.
	IsReverseAllocated        bool
	ReverseAllocatedTotal     string // formatted string of total restored so far

	// Action availability.
	CanPost                bool
	PostBlocker            string
	CanApply               bool
	ApplyBlocker           string
	CanMultiAllocate       bool
	MultiAllocateBlocker   string
	CanRefundApply         bool
	RefundBlocker          string
	CanChargebackApply     bool
	ChargebackApplyBlocker string
	CanUnapply             bool
	UnapplyBlocker         string

	// Batch 22: multi-alloc reverse apply availability.
	CanRefundReverseAllocate         bool
	RefundReverseAllocateBlocker     string
	CanChargebackReverseAllocate     bool
	ChargebackReverseAllocateBlocker string

	// Batch 23: payment reverse exception state.
	HasOpenReverseException bool
	ReverseExceptionID      uint
	ReverseExceptionType    string
	ReverseExceptionStatus  string
}

// ComputePaymentActionState produces a unified state for one transaction.
func ComputePaymentActionState(db *gorm.DB, companyID uint, txn models.PaymentTransaction) PaymentActionState {
	s := PaymentActionState{
		IsPosted:  txn.PostedJournalEntryID != nil,
		IsApplied: txn.AppliedInvoiceID != nil,
	}
	if txn.PostedJournalEntryID != nil {
		s.PostedJEID = *txn.PostedJournalEntryID
	}

	// Multi-allocation state (Batch 17).
	allocated := PaymentAllocatedTotal(db, companyID, txn.ID)
	if allocated.IsPositive() {
		s.IsMultiAllocated = true
		s.MultiAllocatedTotal = allocated.StringFixed(2)
	}

	// Reverse allocation state (Batch 22).
	revTotal := ReverseAllocTotalForTxn(db, companyID, txn.ID)
	if revTotal.IsPositive() {
		s.IsReverseAllocated = true
		s.ReverseAllocatedTotal = revTotal.StringFixed(2)
	}

	postErr := ValidatePaymentTransactionPostable(db, companyID, txn.ID)
	s.CanPost = postErr == nil
	if postErr != nil {
		s.PostBlocker = postErr.Error()
	}

	applyErr := ValidatePaymentTransactionApplicable(db, companyID, txn.ID)
	s.CanApply = applyErr == nil
	if applyErr != nil {
		s.ApplyBlocker = applyErr.Error()
	}

	multiErr := ValidatePaymentAllocatable(db, companyID, txn.ID)
	s.CanMultiAllocate = multiErr == nil
	if multiErr != nil {
		s.MultiAllocateBlocker = multiErr.Error()
	}

	refundErr := ValidateRefundTransactionApplicable(db, companyID, txn.ID)
	s.CanRefundApply = refundErr == nil
	if refundErr != nil {
		s.RefundBlocker = refundErr.Error()
	}

	chargebackErr := ValidateChargebackTransactionApplicable(db, companyID, txn.ID)
	s.CanChargebackApply = chargebackErr == nil
	if chargebackErr != nil {
		s.ChargebackApplyBlocker = chargebackErr.Error()
	}

	unapplyErr := ValidatePaymentTransactionUnapplicable(db, companyID, txn.ID)
	s.CanUnapply = unapplyErr == nil
	if unapplyErr != nil {
		s.UnapplyBlocker = unapplyErr.Error()
	}

	// Batch 22: multi-alloc reverse apply availability.
	refundRevErr := ValidateRefundReverseAllocatable(db, companyID, txn.ID)
	s.CanRefundReverseAllocate = refundRevErr == nil
	if refundRevErr != nil {
		s.RefundReverseAllocateBlocker = refundRevErr.Error()
	}

	cbRevErr := ValidateChargebackReverseAllocatable(db, companyID, txn.ID)
	s.CanChargebackReverseAllocate = cbRevErr == nil
	if cbRevErr != nil {
		s.ChargebackReverseAllocateBlocker = cbRevErr.Error()
	}

	// Batch 23: check for an active payment reverse exception on this txn.
	if activeEx, err := FindActiveReverseExceptionForTxn(db, companyID, txn.ID); err == nil && activeEx != nil {
		s.HasOpenReverseException = true
		s.ReverseExceptionID = activeEx.ID
		s.ReverseExceptionType = string(activeEx.ExceptionType)
		s.ReverseExceptionStatus = string(activeEx.Status)
	}

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
	// Block single-invoice apply if multi-allocation records already exist for
	// this transaction (Batch 17+).  The two paths are mutually exclusive.
	var allocCount int64
	db.Model(&models.PaymentAllocation{}).
		Where("company_id = ? AND payment_transaction_id = ?", companyID, txnID).
		Count(&allocCount)
	if allocCount > 0 {
		return fmt.Errorf("%w: transaction already has multi-invoice allocation records", ErrPaymentTxnAlreadyApplied)
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

	// Balance check: overpayments (txn.Amount > inv.BalanceDue) are allowed since
	// Batch 16 — the excess creates a CustomerCredit rather than being rejected.
	// An invoice at BalanceDue == 0 (status = paid) is still blocked above.
	_ = inv // suppress unused warning; inv is read above for status/channel checks

	return nil
}

// ── Application ──────────────────────────────────────────────────────────────

// Note: the GatewaySettlement path in gateway_settlement_service.go bypasses this helper
// intentionally so settlement can remain atomic in one transaction.
// If audit logging, events, or other side effects are added here in the future,
// keep the gateway-settlement path in sync explicitly.
//
// ApplyPaymentTransactionToInvoice reduces the invoice balance and updates status.
// Does NOT create new JE — the JE was already created during posting.
//
// Overpayment (txn.Amount > invoice.BalanceDue) is handled atomically:
//   - Invoice is applied up to its BalanceDue (marking it paid).
//   - Excess amount creates a CustomerCredit record (no JE; AR was already
//     credited in full during posting).
//   - The unique index on (company_id, source_payment_txn_id, source_application_inv_id)
//     prevents duplicate credits if this function is called more than once.
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
		// Lock txn row first — prevents concurrent double-apply.
		var lockedTxn models.PaymentTransaction
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", txnID, companyID),
		).First(&lockedTxn).Error; err != nil {
			return ErrPaymentTxnAlreadyApplied
		}
		if lockedTxn.AppliedInvoiceID != nil {
			return ErrPaymentTxnAlreadyApplied
		}

		// Lock invoice for update.
		var inv models.Invoice
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", invoiceID, companyID),
		).First(&inv).Error; err != nil {
			return fmt.Errorf("lock invoice: %w", err)
		}

		// Determine applied amount: cap at BalanceDue to prevent over-apply.
		applyAmount := txn.Amount
		excessAmount := decimal.Zero
		if txn.Amount.GreaterThan(inv.BalanceDue) {
			applyAmount = inv.BalanceDue
			excessAmount = txn.Amount.Sub(inv.BalanceDue)
		}

		// Reduce balance.
		newBalance := inv.BalanceDue.Sub(applyAmount)

		// Determine new status.
		var newStatus models.InvoiceStatus
		if newBalance.IsZero() {
			newStatus = models.InvoiceStatusPaid
		} else {
			newStatus = models.InvoiceStatusPartiallyPaid
		}

		// Update invoice.
		// Gateway payments are base-currency only (FX invoices are blocked at all 3 gateway
		// layers), so balance_due_base == balance_due for every invoice that reaches this path.
		// Mirroring the same value keeps both fields in sync without extra DB queries.
		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due":      newBalance,
			"balance_due_base": newBalance,
			"status":           string(newStatus),
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		// Mark transaction as applied, recording the exact invoice-applied portion.
		now := time.Now()
		if err := tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txnID, companyID).
			Updates(map[string]any{
				"applied_invoice_id": invoiceID,
				"applied_at":         now,
				"applied_amount":     applyAmount,
			}).Error; err != nil {
			return fmt.Errorf("mark applied: %w", err)
		}

		// Sync PaymentRequest to paid whenever a charge/capture is applied — even for
		// partial payments. Marking the request as paid (consumed) allows a second
		// PaymentRequest to be created for the remaining balance without being blocked
		// by the duplicate-active-request guard.
		if txn.PaymentRequestID != nil {
			if err := tx.Model(&models.PaymentRequest{}).
				Where("id = ? AND company_id = ?", *txn.PaymentRequestID, companyID).
				Update("status", models.PaymentRequestPaid).Error; err != nil {
				return fmt.Errorf("sync payment request status: %w", err)
			}
		}

		// Create CustomerCredit for overpayment excess.
		if excessAmount.IsPositive() {
			invID := invoiceID
			credit := models.CustomerCredit{
				CompanyID:              companyID,
				CustomerID:             inv.CustomerID,
				SourceType:             models.CreditSourceOverpayment,
				SourcePaymentTxnID:     &txnID,
				SourceApplicationInvID: &invID,
				OriginalAmount:         excessAmount,
				RemainingAmount:        excessAmount,
				CurrencyCode:           inv.CurrencyCode,
				Status:                 models.CustomerCreditActive,
			}
			if err := tx.Create(&credit).Error; err != nil {
				// Unique constraint fires if this exact overpayment already has a credit
				// (concurrent replay protection). Treat as already-handled, not a fatal error.
				if isUniqueConstraintError(err) {
					return ErrPaymentTxnAlreadyApplied
				}
				return fmt.Errorf("create customer credit: %w", err)
			}
		}

		// Audit log.
		cid := companyID
		auditDetails := map[string]any{
			"invoice_id":   invoiceID,
			"amount":       txn.Amount.StringFixed(2),
			"apply_amount": applyAmount.StringFixed(2),
			"new_balance":  newBalance.StringFixed(2),
			"new_status":   string(newStatus),
		}
		if excessAmount.IsPositive() {
			auditDetails["credit_created"] = excessAmount.StringFixed(2)
		}
		return WriteAuditLogWithContextDetails(tx, "payment.applied_to_invoice", "payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			auditDetails,
		)
	})
}

// isUniqueConstraintError returns true if the error is a duplicate-key / unique
// constraint violation from either PostgreSQL or SQLite.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE") ||
		strings.Contains(msg, "unique") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "23505")
}

// effectiveApplyAmount returns the amount that was actually applied to the invoice.
// For overpayments (txn.Amount > invoice.BalanceDue), only the invoice portion was
// applied; the field AppliedAmount stores that value.  Nil means a pre-Batch-16 row
// that had no overpayment — fall back to txn.Amount (safe: old rows were blocked if
// amount > balance, so txn.Amount == the applied portion for those rows).
func effectiveApplyAmount(txn models.PaymentTransaction) decimal.Decimal {
	if txn.AppliedAmount != nil {
		return *txn.AppliedAmount
	}
	return txn.Amount
}

// resolveOriginalCharge finds the charge/capture transaction that a reverse
// (refund or chargeback) txn reverses.  Search order:
//  1. OriginalTransactionID  — most direct; dispute-generated chargebacks use this.
//  2. Applied charge on the same PaymentRequest — fallback for refunds.
//
// Returns nil when the original cannot be resolved (caller falls back to
// txn.Amount-based arithmetic, preserving pre-Batch-16 behaviour).
func resolveOriginalCharge(db *gorm.DB, companyID uint, txn models.PaymentTransaction) *models.PaymentTransaction {
	if txn.OriginalTransactionID != nil {
		var orig models.PaymentTransaction
		if err := db.Where("id = ? AND company_id = ?", *txn.OriginalTransactionID, companyID).
			First(&orig).Error; err == nil &&
			(orig.TransactionType == models.TxnTypeCharge || orig.TransactionType == models.TxnTypeCapture) {
			return &orig
		}
	}
	if txn.PaymentRequestID != nil {
		var orig models.PaymentTransaction
		err := db.Where(
			"payment_request_id = ? AND company_id = ? AND transaction_type IN ? AND applied_invoice_id IS NOT NULL",
			*txn.PaymentRequestID, companyID,
			[]string{string(models.TxnTypeCharge), string(models.TxnTypeCapture)},
		).Order("id ASC").First(&orig).Error
		if err == nil {
			return &orig
		}
	}
	return nil
}

// effectiveReverseRestore computes the amount to restore to the invoice for a
// reverse (refund/chargeback) transaction.
//
// Two invariants are enforced when the original charge is found:
//  1. Over-reverse guard: reverse.Amount must not exceed originalCharge.Amount.
//     (Returning more than was collected is always an operator error.)
//  2. Overpayment cap: restore is capped at effectiveApplyAmount(originalCharge)
//     so that the excess that became a CustomerCredit is not pushed back to the invoice.
//
// If the original charge cannot be resolved, returns (txn.Amount, nil) — the
// pre-Batch-16 fall-through that preserves existing behaviour.
// On over-reverse violation returns (zero, ErrXxxExceedsTotal).
func effectiveReverseRestore(db *gorm.DB, companyID uint, txn models.PaymentTransaction, exceedErr error) (decimal.Decimal, error) {
	orig := resolveOriginalCharge(db, companyID, txn)
	if orig == nil {
		return txn.Amount, nil
	}
	if txn.Amount.GreaterThan(orig.Amount) {
		return decimal.Zero, fmt.Errorf("%w: reverse %s exceeds original charge %s",
			exceedErr, txn.Amount.StringFixed(2), orig.Amount.StringFixed(2))
	}
	restore := txn.Amount
	origApplied := effectiveApplyAmount(*orig)
	if restore.GreaterThan(origApplied) {
		restore = origApplied
	}
	return restore, nil
}

// reverseSourceHasMultiAllocation reports whether the reverse transaction is
// trying to restore invoice state from an original charge/capture that already
// uses Batch-17 multi-invoice allocations. That reverse path is intentionally
// blocked until there is an allocation-aware reversal model.
func reverseSourceHasMultiAllocation(db *gorm.DB, companyID uint, txn models.PaymentTransaction) (bool, error) {
	if !db.Migrator().HasTable(&models.PaymentAllocation{}) {
		return false, nil
	}
	if txn.OriginalTransactionID != nil {
		if total := PaymentAllocatedTotal(db, companyID, *txn.OriginalTransactionID); total.IsPositive() {
			return true, nil
		}
	}
	if txn.PaymentRequestID == nil {
		return false, nil
	}

	chargeTxnIDs := db.Model(&models.PaymentTransaction{}).
		Select("id").
		Where(
			"company_id = ? AND payment_request_id = ? AND transaction_type IN ?",
			companyID,
			*txn.PaymentRequestID,
			[]string{string(models.TxnTypeCharge), string(models.TxnTypeCapture)},
		)

	var allocCount int64
	if err := db.Model(&models.PaymentAllocation{}).
		Where("company_id = ? AND payment_transaction_id IN (?)", companyID, chargeTxnIDs).
		Count(&allocCount).Error; err != nil {
		return false, fmt.Errorf("check multi-allocation source: %w", err)
	}
	return allocCount > 0, nil
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
	// Mutual exclusion (Batch 22): block single-invoice path if multi-alloc reverse already applied.
	if reverseAllocCount(db, companyID, txnID) > 0 {
		return fmt.Errorf("%w: transaction already applied via multi-alloc reverse allocation path", ErrPaymentTxnAlreadyApplied)
	}
	hasMultiAlloc, err := reverseSourceHasMultiAllocation(db, companyID, txn)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPaymentTxnNotApplicable, err)
	}
	if hasMultiAlloc {
		return fmt.Errorf("%w: original payment was multi-invoice allocated; use the multi-alloc reverse path instead", ErrPaymentTxnNotApplicable)
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

	// Compute effective restore: capped at the invoice-applied portion of the
	// original charge, preventing overpayment excess from being pushed back to the invoice.
	restore, err := effectiveReverseRestore(db, companyID, txn, ErrRefundExceedsTotal)
	if err != nil {
		return err
	}
	newBalance := inv.BalanceDue.Add(restore)
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
		// Lock txn row first — prevents concurrent double-apply on the same reverse txn.
		// applyLockForUpdate issues SELECT ... FOR UPDATE on PostgreSQL; on SQLite it is a
		// no-op (SQLite serialises writes at the connection level).
		var lockedTxn models.PaymentTransaction
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", txnID, companyID),
		).First(&lockedTxn).Error; err != nil {
			return ErrPaymentTxnAlreadyApplied
		}
		// Re-check applied state under lock so a concurrent caller that passed the
		// pre-transaction validation is rejected here after the first apply commits.
		if lockedTxn.AppliedInvoiceID != nil {
			return ErrPaymentTxnAlreadyApplied
		}

		var inv models.Invoice
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", invoiceID, companyID),
		).First(&inv).Error; err != nil {
			return fmt.Errorf("lock invoice: %w", err)
		}

		// Re-compute effective restore under lock (same logic as validate).
		restore, restoreErr := effectiveReverseRestore(tx, companyID, txn, ErrRefundExceedsTotal)
		if restoreErr != nil {
			return restoreErr
		}
		newBalance := inv.BalanceDue.Add(restore)
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

		// Mirror balance_due_base = balance_due. Gateway refund only runs for base-currency
		// invoices (FX is blocked at all 3 gateway layers), so both fields track the same value.
		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due":      newBalance,
			"balance_due_base": newBalance,
			"status":           string(newStatus),
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		now := time.Now()
		if err := tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txnID, companyID).
			Updates(map[string]any{
				"applied_invoice_id": invoiceID,
				"applied_at":         now,
			}).Error; err != nil {
			return fmt.Errorf("mark refund applied: %w", err)
		}

		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "payment.refund_applied_to_invoice", "payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"invoice_id":     invoiceID,
				"amount":         txn.Amount.StringFixed(2),
				"restore_amount": restore.StringFixed(2),
				"new_balance":    newBalance.StringFixed(2),
				"new_status":     string(newStatus),
			},
		)
	})
}

// ── Chargeback application ────────────────────────────────────────────────────
//
// A chargeback is a forcible reversal of a prior payment by the card network.
// Semantically it is distinct from a refund (which is voluntary). Both result in
// the same invoice restore operation, but they use different GL accounts during
// posting (ChargebackAccountID vs RefundAccountID) and carry different audit labels.

// ChargebackApplicationBlocker returns the blocker reason, or empty if applicable.
func ChargebackApplicationBlocker(db *gorm.DB, companyID, txnID uint) string {
	err := ValidateChargebackTransactionApplicable(db, companyID, txnID)
	if err == nil {
		return ""
	}
	return err.Error()
}

// ValidateChargebackTransactionApplicable checks if a chargeback transaction can
// be applied to restore an invoice's BalanceDue.
//
// Chargeback apply requires the same conditions as refund apply, but for
// TxnTypeChargeback instead of TxnTypeRefund. The invoice linkage is resolved via
// the payment request on the original charge transaction (OriginalTransactionID).
func ValidateChargebackTransactionApplicable(db *gorm.DB, companyID, txnID uint) error {
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
	if txn.TransactionType != models.TxnTypeChargeback {
		return fmt.Errorf("%w: only chargeback transactions can be chargeback-applied (type=%s)", ErrPaymentTxnNotApplicable, txn.TransactionType)
	}
	if txn.Amount.IsZero() || txn.Amount.IsNegative() {
		return fmt.Errorf("%w: amount must be positive", ErrPaymentTxnNotApplicable)
	}
	// Mutual exclusion (Batch 22): block single-invoice path if multi-alloc reverse already applied.
	if reverseAllocCount(db, companyID, txnID) > 0 {
		return fmt.Errorf("%w: transaction already applied via multi-alloc reverse allocation path", ErrPaymentTxnAlreadyApplied)
	}
	hasMultiAlloc, err := reverseSourceHasMultiAllocation(db, companyID, txn)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPaymentTxnNotApplicable, err)
	}
	if hasMultiAlloc {
		return fmt.Errorf("%w: original payment was multi-invoice allocated; use the multi-alloc reverse path instead", ErrPaymentTxnNotApplicable)
	}

	// Resolve invoice: prefer payment request linkage; fall back to OriginalTransactionID.
	invoiceID, err := resolveChargebackInvoiceID(db, companyID, txn)
	if err != nil {
		return err
	}

	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&inv).Error; err != nil {
		return fmt.Errorf("%w: invoice not found", ErrPaymentTxnNotApplicable)
	}
	if inv.ChannelOrderID != nil {
		return ErrChannelInvoiceBlocked
	}
	if inv.Status == models.InvoiceStatusDraft || inv.Status == models.InvoiceStatusVoided {
		return fmt.Errorf("%w: invoice is %s", ErrPaymentTxnNotApplicable, inv.Status)
	}

	// Compute effective restore: capped at the invoice-applied portion of the
	// original charge, preventing overpayment excess from being pushed back to the invoice.
	restore, err := effectiveReverseRestore(db, companyID, txn, ErrChargebackExceedsTotal)
	if err != nil {
		return err
	}
	newBalance := inv.BalanceDue.Add(restore)
	if newBalance.GreaterThan(inv.Amount) {
		return fmt.Errorf("%w: chargeback %s + balance %s > total %s",
			ErrChargebackExceedsTotal, txn.Amount.StringFixed(2), inv.BalanceDue.StringFixed(2), inv.Amount.StringFixed(2))
	}

	return nil
}

// ApplyChargebackTransactionToInvoice restores the invoice BalanceDue for a
// chargeback transaction, marking the invoice as partially_paid or issued.
// Does NOT create a new JE — that was created during posting.
func ApplyChargebackTransactionToInvoice(db *gorm.DB, companyID, txnID uint, actor string) error {
	if err := ValidateChargebackTransactionApplicable(db, companyID, txnID); err != nil {
		return err
	}

	var txn models.PaymentTransaction
	db.First(&txn, txnID)

	invoiceID, err := resolveChargebackInvoiceID(db, companyID, txn)
	if err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Lock txn row first — prevents concurrent double-apply on the same reverse txn.
		// applyLockForUpdate issues SELECT ... FOR UPDATE on PostgreSQL; on SQLite it is a
		// no-op (SQLite serialises writes at the connection level).
		var lockedTxn models.PaymentTransaction
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", txnID, companyID),
		).First(&lockedTxn).Error; err != nil {
			return ErrPaymentTxnAlreadyApplied
		}
		// Re-check applied state under lock so a concurrent caller that passed the
		// pre-transaction validation is rejected here after the first apply commits.
		if lockedTxn.AppliedInvoiceID != nil {
			return ErrPaymentTxnAlreadyApplied
		}

		var inv models.Invoice
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", invoiceID, companyID),
		).First(&inv).Error; err != nil {
			return fmt.Errorf("lock invoice: %w", err)
		}

		// Re-compute effective restore under lock (same logic as validate).
		restore, restoreErr := effectiveReverseRestore(tx, companyID, txn, ErrChargebackExceedsTotal)
		if restoreErr != nil {
			return restoreErr
		}
		newBalance := inv.BalanceDue.Add(restore)
		if newBalance.GreaterThan(inv.Amount) {
			return ErrChargebackExceedsTotal
		}

		var newStatus models.InvoiceStatus
		if newBalance.Equal(inv.Amount) {
			newStatus = models.InvoiceStatusIssued
		} else if newBalance.IsPositive() {
			newStatus = models.InvoiceStatusPartiallyPaid
		} else {
			newStatus = inv.Status
		}

		// Mirror balance_due_base = balance_due. Gateway chargeback only runs for
		// base-currency invoices (FX is blocked at all gateway layers).
		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due":      newBalance,
			"balance_due_base": newBalance,
			"status":           string(newStatus),
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		now := time.Now()
		if err := tx.Model(&models.PaymentTransaction{}).
			Where("id = ? AND company_id = ?", txnID, companyID).
			Updates(map[string]any{
				"applied_invoice_id": invoiceID,
				"applied_at":         now,
			}).Error; err != nil {
			return fmt.Errorf("mark chargeback applied: %w", err)
		}

		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "payment.chargeback_applied_to_invoice", "payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"invoice_id":     invoiceID,
				"amount":         txn.Amount.StringFixed(2),
				"restore_amount": restore.StringFixed(2),
				"new_balance":    newBalance.StringFixed(2),
				"new_status":     string(newStatus),
			},
		)
	})
}

// resolveChargebackInvoiceID finds the invoice ID for a chargeback transaction.
// Checks payment request linkage first, then falls back to the original
// transaction's payment request (via OriginalTransactionID).
func resolveChargebackInvoiceID(db *gorm.DB, companyID uint, txn models.PaymentTransaction) (uint, error) {
	// Direct payment request linkage (e.g. manually created chargeback).
	if txn.PaymentRequestID != nil {
		var req models.PaymentRequest
		if err := db.Where("id = ? AND company_id = ?", *txn.PaymentRequestID, companyID).First(&req).Error; err != nil {
			return 0, fmt.Errorf("%w: payment request not found", ErrPaymentTxnNotApplicable)
		}
		if req.InvoiceID != nil {
			return *req.InvoiceID, nil
		}
	}
	// Fall back to original charge's payment request linkage.
	if txn.OriginalTransactionID != nil {
		var orig models.PaymentTransaction
		if err := db.Where("id = ? AND company_id = ?", *txn.OriginalTransactionID, companyID).First(&orig).Error; err != nil {
			return 0, fmt.Errorf("%w: original transaction not found", ErrPaymentTxnNotApplicable)
		}
		if orig.PaymentRequestID != nil {
			var req models.PaymentRequest
			if err := db.Where("id = ? AND company_id = ?", *orig.PaymentRequestID, companyID).First(&req).Error; err != nil {
				return 0, fmt.Errorf("%w: original payment request not found", ErrPaymentTxnNotApplicable)
			}
			if req.InvoiceID != nil {
				return *req.InvoiceID, nil
			}
		}
	}
	return 0, fmt.Errorf("%w: no invoice linkage found for chargeback", ErrPaymentTxnNotApplicable)
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

	// Unapply must not push BalanceDue above invoice.Amount.
	// Use the actual applied portion (AppliedAmount), not the raw transaction amount,
	// so that overpayments (where only part of the txn reduced the invoice) unapply correctly.
	restoreAmount := effectiveApplyAmount(txn)
	newBalance := inv.BalanceDue.Add(restoreAmount)
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

		// Use the actual applied portion (not raw txn.Amount) so overpayments
		// restore only what was taken from the invoice, not the full txn amount.
		restoreAmount := effectiveApplyAmount(txn)
		newBalance := inv.BalanceDue.Add(restoreAmount)
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

		// Mirror balance_due_base = balance_due. Gateway unapply only runs for base-currency
		// invoices (FX is blocked), so both fields must track the same restored value.
		if err := tx.Model(&inv).Updates(map[string]any{
			"balance_due":      newBalance,
			"balance_due_base": newBalance,
			"status":           string(newStatus),
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		// Clear applied state (including applied_amount).
		// Raw exec is used to reliably set all three columns to NULL across both
		// SQLite (tests) and PostgreSQL (production) without GORM skipping nil values.
		if err := tx.Exec(
			"UPDATE payment_transactions SET applied_invoice_id = NULL, applied_at = NULL, applied_amount = NULL WHERE id = ? AND company_id = ?",
			txnID, companyID,
		).Error; err != nil {
			return fmt.Errorf("clear applied state: %w", err)
		}

		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "payment.unapplied_from_invoice", "payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"invoice_id":     invoiceID,
				"restore_amount": restoreAmount.StringFixed(2),
				"new_balance":    newBalance.StringFixed(2),
				"new_status":     string(newStatus),
			},
		)
	})
}
