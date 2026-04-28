// 遵循project_guide.md
package services

// payment_reverse_allocation_service.go — Batch 22: Multi-allocated payment reverse allocation.
//
// Provides:
//   - ComputeReverseAllocationPlan:         compute proportional restore plan from original allocs
//   - ApplyRefundReverseAllocations:        refund → multi-alloc reverse apply
//   - ApplyChargebackReverseAllocations:    chargeback → multi-alloc reverse apply
//   - ApplyDisputeLostReverseAllocations:   dispute_lost (chargeback-style) → multi-alloc reverse apply
//   - ValidateRefundReverseAllocatable:     pre-flight check for refund reverse alloc
//   - ValidateChargebackReverseAllocatable: pre-flight check for chargeback reverse alloc
//   - ListReverseAllocationsForTxn:         load all reverse alloc records for a reverse txn
//
// ─── Design ───────────────────────────────────────────────────────────────────
//
// This service is the ONLY path through which a multi-allocated payment reversal
// touches invoice state.  It:
//
//  1. Resolves the original charge/capture via resolveOriginalCharge.
//  2. Loads the forward PaymentAllocation records from Batch 17.
//  3. Computes a proportional reverse plan — each invoice's share is
//     alloc.AllocatedAmount / totalAllocated × effectiveReversal.
//  4. Caps the total restore at the allocated total (overpayment excess that
//     became a CustomerCredit is NEVER pushed back to invoices).
//  5. In a single transaction: inserts PaymentReverseAllocation rows +
//     restores invoice BalanceDue + writes audit log.
//
// ─── Proportional split ──────────────────────────────────────────────────────
//
//   Given original allocations [600, 400] (total 1000) and reversal 500:
//     INV-A: 600/1000 × 500 = 300.00
//     INV-B: 400/1000 × 500 = 200.00
//
//   Last line always receives the remainder (effectiveReversal − Σ prior lines)
//   to guarantee the sum equals effectiveReversal exactly (avoids decimal drift).
//
// ─── Overpayment / credit portion ───────────────────────────────────────────
//
//   effectiveReversal = min(txn.Amount, totalAllocated)
//
//   If a charge of 1000 was allocated 800 to invoices and 200 became a
//   CustomerCredit, a full 1000 refund only restores 800 across the invoices.
//   The 200 credit portion is NOT restored to any invoice via this path.
//
// ─── Locking order (deadlock prevention) ────────────────────────────────────
//
//   1. Lock reverse txn (SELECT FOR UPDATE) — re-check not already applied.
//   2. Lock original txn (SELECT FOR UPDATE) — for isolation.
//   3. Lock all target invoices in ascending ID order — matches Batch 17 order.
//
// ─── Mutual exclusion ────────────────────────────────────────────────────────
//
//   - single-invoice reverse already applied (AppliedInvoiceID ≠ nil) blocks multi path.
//   - multi-alloc reverse already applied (PaymentReverseAllocation count > 0) blocks
//     single path (enforced in payment_application_service.go validators).
//
// ─── Not in Batch 22 ────────────────────────────────────────────────────────
//   - Customer credit reverse allocation
//   - Multi-currency reverse allocation
//   - Manual invoice assignment override
//   - Auto credit memo / writeoff

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
	// ErrReverseAllocAlreadyApplied fires when the reverse txn already has
	// PaymentReverseAllocation records (multi-path) or AppliedInvoiceID set (single-path).
	ErrReverseAllocAlreadyApplied = errors.New("reverse allocation already applied for this transaction")

	// ErrReverseAllocNoOriginalTxn fires when the reverse txn cannot be linked back
	// to an original charge/capture transaction.
	ErrReverseAllocNoOriginalTxn = errors.New("reverse transaction has no resolvable original charge/capture")

	// ErrReverseAllocNoAllocations fires when the original charge has no
	// PaymentAllocation records (i.e. it used the single-invoice path, not multi-alloc).
	ErrReverseAllocNoAllocations = errors.New("original charge has no multi-invoice allocation records; use the single-invoice reverse path instead")

	// ErrReverseAllocTxnNotReversible fires when the transaction type is not
	// refund or chargeback.
	ErrReverseAllocTxnNotReversible = errors.New("transaction type is not reversible (must be refund or chargeback)")

	// ErrReverseAllocTxnNotPosted fires when the reverse txn has not been posted.
	ErrReverseAllocTxnNotPosted = errors.New("reverse transaction must be posted before reverse allocation can be applied")

	// ErrReverseAllocInvoiceNotRestoreable fires when an invoice is in a state
	// (voided or draft) that cannot accept a balance restore.
	ErrReverseAllocInvoiceNotRestoreable = errors.New("invoice cannot receive a balance restore in its current state")

	// ErrReverseAllocWouldExceedInvoiceTotal fires when a per-invoice restore
	// would push BalanceDue above the invoice Amount.
	ErrReverseAllocWouldExceedInvoiceTotal = errors.New("reverse allocation would restore invoice balance beyond its total amount")

	// ErrReverseAllocExceedsReversibleTotal fires when the requested reversal,
	// after excluding overpayment excess, still exceeds the remaining
	// unreversed forward allocation total for the original charge.
	ErrReverseAllocExceedsReversibleTotal = errors.New("reverse allocation exceeds remaining reversible total")

	// ErrReverseAllocUnsupportedMultiLayerReversal fires when a refund/chargeback
	// points to another reversal transaction instead of directly to a
	// charge/capture. Batch 22 supports only one reverse layer.
	ErrReverseAllocUnsupportedMultiLayerReversal = errors.New("multi-layer reversal is not supported for reverse allocation")
)

// ── Plan types ────────────────────────────────────────────────────────────────

// ReverseAllocationLine describes how much to restore to one invoice.
type ReverseAllocationLine struct {
	InvoiceID           uint
	PaymentAllocationID uint
	Amount              decimal.Decimal
}

// ── Plan computation ──────────────────────────────────────────────────────────

// ComputeReverseAllocationPlan derives the proportional restore plan for a
// reverse transaction based on the original charge's PaymentAllocation records.
//
// The plan is deterministic and idempotent given the same inputs.  It is used
// both for pre-flight display and for execution inside the apply transaction.
//
// Overpayment guard: effectiveReversal = min(txn.Amount, totalAllocated).
// Any amount above totalAllocated (overpayment excess) is silently excluded
// because it became a CustomerCredit and must not be pushed back to invoices.
func ComputeReverseAllocationPlan(
	reversalAmount decimal.Decimal,
	originalAllocs []models.PaymentAllocation,
) ([]ReverseAllocationLine, error) {
	if len(originalAllocs) == 0 {
		return nil, ErrReverseAllocNoAllocations
	}

	// Compute total allocated to invoices by the original charge.
	totalAllocated := decimal.Zero
	for _, a := range originalAllocs {
		totalAllocated = totalAllocated.Add(a.AllocatedAmount)
	}
	if totalAllocated.IsZero() {
		return nil, ErrReverseAllocNoAllocations
	}

	// Cap at total allocated: overpayment excess is never pushed back to invoices.
	effectiveReversal := reversalAmount
	if effectiveReversal.GreaterThan(totalAllocated) {
		effectiveReversal = totalAllocated
	}

	if !effectiveReversal.IsPositive() {
		return nil, fmt.Errorf("effective reversal amount is zero")
	}

	// Sort by PaymentAllocation.ID ascending for deterministic order.
	sorted := make([]models.PaymentAllocation, len(originalAllocs))
	copy(sorted, originalAllocs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	lines := make([]ReverseAllocationLine, 0, len(sorted))
	running := decimal.Zero

	for i, alloc := range sorted {
		var amount decimal.Decimal
		if i == len(sorted)-1 {
			// Last line: exact remainder so the sum equals effectiveReversal.
			amount = effectiveReversal.Sub(running)
		} else {
			// Proportional share, rounded to 2 dp.
			proportion := alloc.AllocatedAmount.Div(totalAllocated)
			amount = proportion.Mul(effectiveReversal).Round(2)
			running = running.Add(amount)
		}
		if amount.IsZero() || amount.IsNegative() {
			// Skip zero-amount lines (can occur in pathological rounding edge cases).
			continue
		}
		lines = append(lines, ReverseAllocationLine{
			InvoiceID:           alloc.InvoiceID,
			PaymentAllocationID: alloc.ID,
			Amount:              amount,
		})
	}

	return lines, nil
}

// ── Validation ────────────────────────────────────────────────────────────────

// ValidateRefundReverseAllocatable checks whether a refund transaction can be
// applied via the multi-alloc reverse path.
func ValidateRefundReverseAllocatable(db *gorm.DB, companyID, txnID uint) error {
	return validateReverseAllocatable(db, companyID, txnID, models.TxnTypeRefund)
}

// ValidateChargebackReverseAllocatable checks whether a chargeback transaction
// can be applied via the multi-alloc reverse path.
func ValidateChargebackReverseAllocatable(db *gorm.DB, companyID, txnID uint) error {
	return validateReverseAllocatable(db, companyID, txnID, models.TxnTypeChargeback)
}

func validateReverseAllocatable(db *gorm.DB, companyID, txnID uint, expectedType models.PaymentTransactionType) error {
	var txn models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return fmt.Errorf("transaction not found")
	}
	if txn.TransactionType != expectedType {
		return fmt.Errorf("%w: got %s, want %s", ErrReverseAllocTxnNotReversible, txn.TransactionType, expectedType)
	}
	if txn.PostedJournalEntryID == nil {
		return ErrReverseAllocTxnNotPosted
	}
	// Already applied via single-invoice path?
	if txn.AppliedInvoiceID != nil {
		return fmt.Errorf("%w: already applied via single-invoice path", ErrReverseAllocAlreadyApplied)
	}
	// Already applied via multi-alloc path?
	if reverseAllocCount(db, companyID, txnID) > 0 {
		return fmt.Errorf("%w: already applied via multi-alloc reverse path", ErrReverseAllocAlreadyApplied)
	}
	// Resolve original charge. Reject reversal-of-reversal chains explicitly
	// before falling back to the broader PaymentRequest-based resolver.
	orig, err := resolveOriginalChargeForReverseAllocation(db, companyID, txn)
	if err != nil {
		return err
	}
	// Original charge must have multi-alloc records.
	allocs, err := ListPaymentAllocations(db, companyID, orig.ID)
	if err != nil {
		return fmt.Errorf("load original allocations: %w", err)
	}
	if len(allocs) == 0 {
		return ErrReverseAllocNoAllocations
	}
	remainingAllocs, totalAllocated, remainingReversible, err := remainingReverseAllocations(db, companyID, orig.ID, allocs)
	if err != nil {
		return fmt.Errorf("load remaining reversible allocations: %w", err)
	}
	if len(remainingAllocs) == 0 || !remainingReversible.IsPositive() {
		return fmt.Errorf("%w: remaining=0.00", ErrReverseAllocExceedsReversibleTotal)
	}
	effectiveReversal := effectiveReverseAllocationAmount(txn.Amount, totalAllocated)
	if effectiveReversal.GreaterThan(remainingReversible) {
		return fmt.Errorf("%w: reversal=%s remaining=%s",
			ErrReverseAllocExceedsReversibleTotal,
			effectiveReversal.StringFixed(2),
			remainingReversible.StringFixed(2),
		)
	}
	return nil
}

// ── Public apply entry points ────────────────────────────────────────────────

// ApplyRefundReverseAllocations applies a refund transaction across the invoices
// that were covered by the original charge's multi-allocation.
func ApplyRefundReverseAllocations(db *gorm.DB, companyID, txnID uint, actor string) error {
	return applyReverseAllocations(db, companyID, txnID, models.ReverseAllocRefund, actor)
}

// ApplyChargebackReverseAllocations applies a chargeback transaction across the
// invoices covered by the original charge's multi-allocation.
func ApplyChargebackReverseAllocations(db *gorm.DB, companyID, txnID uint, actor string) error {
	return applyReverseAllocations(db, companyID, txnID, models.ReverseAllocChargeback, actor)
}

// ApplyDisputeLostReverseAllocations applies a dispute-lost chargeback transaction
// across the invoices covered by the original charge's multi-allocation.
// The chargeback transaction is created by LoseGatewayDispute; this function
// performs the downstream invoice-restore step for the multi-alloc case.
func ApplyDisputeLostReverseAllocations(db *gorm.DB, companyID, txnID uint, actor string) error {
	return applyReverseAllocations(db, companyID, txnID, models.ReverseAllocDisputeLost, actor)
}

// ── Core apply implementation ─────────────────────────────────────────────────

func applyReverseAllocations(
	db *gorm.DB,
	companyID uint,
	txnID uint,
	reverseType models.ReverseAllocationType,
	actor string,
) error {
	actor = normalizeActor(actor)

	// ── Pre-transaction validation ─────────────────────────────────────────────

	var txn models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return fmt.Errorf("reverse transaction not found")
	}

	// Type check: only refund/chargeback transactions can reverse allocations.
	switch txn.TransactionType {
	case models.TxnTypeRefund, models.TxnTypeChargeback:
		// ok
	default:
		return fmt.Errorf("%w: got %s", ErrReverseAllocTxnNotReversible, txn.TransactionType)
	}
	if txn.PostedJournalEntryID == nil {
		return ErrReverseAllocTxnNotPosted
	}

	// Already applied via single-invoice path?
	if txn.AppliedInvoiceID != nil {
		return fmt.Errorf("%w: already applied via single-invoice path", ErrReverseAllocAlreadyApplied)
	}
	// Already applied via multi-alloc path?
	if reverseAllocCount(db, companyID, txnID) > 0 {
		return fmt.Errorf("%w: already applied via multi-alloc reverse path", ErrReverseAllocAlreadyApplied)
	}

	// ── Transaction ────────────────────────────────────────────────────────────

	return db.Transaction(func(tx *gorm.DB) error {
		// 1. Lock the reverse txn — re-check not already applied under lock.
		var lockedTxn models.PaymentTransaction
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", txnID, companyID),
		).First(&lockedTxn).Error; err != nil {
			return fmt.Errorf("lock reverse transaction: %w", err)
		}
		if lockedTxn.AppliedInvoiceID != nil {
			return fmt.Errorf("%w: concurrent single-invoice apply", ErrReverseAllocAlreadyApplied)
		}
		// Re-check unique constraint by trying to count existing rows under lock.
		if reverseAllocCount(tx, companyID, txnID) > 0 {
			return fmt.Errorf("%w: concurrent multi-alloc reverse apply", ErrReverseAllocAlreadyApplied)
		}

		// 2. Lock the original txn — prevents concurrent modification of its allocations.
		orig, err := resolveOriginalChargeForReverseAllocation(tx, companyID, lockedTxn)
		if err != nil {
			return err
		}
		var lockedOrig models.PaymentTransaction
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", orig.ID, companyID),
		).First(&lockedOrig).Error; err != nil {
			return fmt.Errorf("lock original transaction: %w", err)
		}

		originalAllocs, err := ListPaymentAllocations(tx, companyID, lockedOrig.ID)
		if err != nil {
			return fmt.Errorf("load original allocations: %w", err)
		}
		if len(originalAllocs) == 0 {
			return ErrReverseAllocNoAllocations
		}
		remainingAllocs, totalAllocated, remainingReversible, err := remainingReverseAllocations(
			tx, companyID, lockedOrig.ID, originalAllocs,
		)
		if err != nil {
			return fmt.Errorf("load remaining reversible allocations: %w", err)
		}
		effectiveReversal := effectiveReverseAllocationAmount(lockedTxn.Amount, totalAllocated)
		if !effectiveReversal.IsPositive() {
			return fmt.Errorf("effective reversal amount is zero")
		}
		if effectiveReversal.GreaterThan(remainingReversible) {
			return fmt.Errorf("%w: reversal=%s remaining=%s",
				ErrReverseAllocExceedsReversibleTotal,
				effectiveReversal.StringFixed(2),
				remainingReversible.StringFixed(2),
			)
		}
		plan, err := ComputeReverseAllocationPlan(effectiveReversal, remainingAllocs)
		if err != nil {
			return fmt.Errorf("compute reverse plan: %w", err)
		}
		appliedReverseType, err := resolvedReverseAllocationType(tx, companyID, lockedTxn, reverseType)
		if err != nil {
			return fmt.Errorf("resolve reverse type: %w", err)
		}

		// 3. Lock all target invoices in ascending ID order (deadlock prevention).
		invoiceIDs := make([]uint, len(plan))
		for i, line := range plan {
			invoiceIDs[i] = line.InvoiceID
		}
		sort.Slice(invoiceIDs, func(i, j int) bool { return invoiceIDs[i] < invoiceIDs[j] })

		invoiceMap := make(map[uint]models.Invoice, len(invoiceIDs))
		for _, id := range invoiceIDs {
			var inv models.Invoice
			if err := applyLockForUpdate(
				tx.Where("id = ? AND company_id = ?", id, companyID),
			).First(&inv).Error; err != nil {
				return fmt.Errorf("lock invoice %d: %w", id, err)
			}
			invoiceMap[id] = inv
		}

		// 4. Validate each invoice line under lock.
		for _, line := range plan {
			inv := invoiceMap[line.InvoiceID]
			if inv.Status == models.InvoiceStatusDraft || inv.Status == models.InvoiceStatusVoided {
				return fmt.Errorf("invoice %d: %w (status=%s)", line.InvoiceID, ErrReverseAllocInvoiceNotRestoreable, inv.Status)
			}
			newBalance := inv.BalanceDue.Add(line.Amount)
			if newBalance.GreaterThan(inv.Amount) {
				return fmt.Errorf("invoice %d: %w (current=%s, restore=%s, total=%s)",
					line.InvoiceID, ErrReverseAllocWouldExceedInvoiceTotal,
					inv.BalanceDue.StringFixed(2), line.Amount.StringFixed(2), inv.Amount.StringFixed(2))
			}
		}

		// 5. Apply each line: restore invoice balance + insert reverse alloc record.
		for _, line := range plan {
			inv := invoiceMap[line.InvoiceID]
			newBalance := inv.BalanceDue.Add(line.Amount)

			var newStatus models.InvoiceStatus
			if newBalance.Equal(inv.Amount) {
				newStatus = models.InvoiceStatusIssued
			} else if newBalance.IsPositive() {
				newStatus = models.InvoiceStatusPartiallyPaid
			} else {
				newStatus = inv.Status
			}

			// Gateway reverse operations are base-currency only (FX invoices are
			// blocked at all 3 gateway layers), so balance_due_base == balance_due.
			if err := tx.Model(&inv).Updates(map[string]any{
				"balance_due":      newBalance,
				"balance_due_base": newBalance,
				"status":           string(newStatus),
			}).Error; err != nil {
				return fmt.Errorf("restore invoice %d: %w", line.InvoiceID, err)
			}

			rev := models.PaymentReverseAllocation{
				CompanyID:           companyID,
				ReverseTxnID:        txnID,
				OriginalTxnID:       orig.ID,
				PaymentAllocationID: line.PaymentAllocationID,
				InvoiceID:           line.InvoiceID,
				Amount:              line.Amount,
				ReverseType:         appliedReverseType,
			}
			if err := tx.Create(&rev).Error; err != nil {
				if isUniqueConstraintError(err) {
					return fmt.Errorf("%w: concurrent apply on invoice %d",
						ErrReverseAllocAlreadyApplied, line.InvoiceID)
				}
				return fmt.Errorf("insert reverse allocation for invoice %d: %w", line.InvoiceID, err)
			}
		}

		// 6. Audit log.
		cid := companyID
		lineDetails := make([]map[string]any, len(plan))
		for i, line := range plan {
			lineDetails[i] = map[string]any{
				"invoice_id":            line.InvoiceID,
				"payment_allocation_id": line.PaymentAllocationID,
				"amount":                line.Amount.StringFixed(2),
			}
		}
		totalRestored := decimal.Zero
		for _, line := range plan {
			totalRestored = totalRestored.Add(line.Amount)
		}
		auditEvent := fmt.Sprintf("payment.%s_reverse_allocated", string(appliedReverseType))
		if err := WriteAuditLogWithContextDetails(tx,
			auditEvent,
			"payment_transaction", txnID, actor,
			map[string]any{"company_id": companyID},
			&cid, nil, nil,
			map[string]any{
				"reverse_type":    string(appliedReverseType),
				"reverse_txn_id":  txnID,
				"original_txn_id": orig.ID,
				"total_restored":  totalRestored.StringFixed(2),
				"invoice_count":   len(plan),
				"lines":           lineDetails,
			},
		); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}

		slog.Info("multi-alloc payment reverse allocated",
			"reverse_type", string(appliedReverseType),
			"reverse_txn_id", txnID,
			"original_txn_id", orig.ID,
			"total_restored", totalRestored.StringFixed(2),
			"invoice_count", len(plan),
		)
		return nil
	})
}

// ── Query helpers ─────────────────────────────────────────────────────────────

// ListReverseAllocationsForTxn returns all reverse allocation records for a
// given reverse transaction, oldest first.
func ListReverseAllocationsForTxn(
	db *gorm.DB,
	companyID, reverseTxnID uint,
) ([]models.PaymentReverseAllocation, error) {
	var rows []models.PaymentReverseAllocation
	err := db.
		Where("company_id = ? AND reverse_txn_id = ?", companyID, reverseTxnID).
		Order("id ASC").
		Find(&rows).Error
	return rows, err
}

// ReverseAllocTotalForTxn returns the sum of all reverse allocation amounts for
// a reverse transaction.  Returns zero when no records exist.
func ReverseAllocTotalForTxn(db *gorm.DB, companyID, reverseTxnID uint) decimal.Decimal {
	type res struct{ Total decimal.Decimal }
	var r res
	db.Model(&models.PaymentReverseAllocation{}).
		Select("COALESCE(SUM(amount), 0) AS total").
		Where("company_id = ? AND reverse_txn_id = ?", companyID, reverseTxnID).
		Scan(&r)
	return r.Total
}

// reverseAllocCount returns the number of PaymentReverseAllocation records for
// a given reverse transaction.  Used for idempotency checks.
func reverseAllocCount(db *gorm.DB, companyID, reverseTxnID uint) int64 {
	var count int64
	db.Model(&models.PaymentReverseAllocation{}).
		Where("company_id = ? AND reverse_txn_id = ?", companyID, reverseTxnID).
		Count(&count)
	return count
}

func effectiveReverseAllocationAmount(reversalAmount, totalAllocated decimal.Decimal) decimal.Decimal {
	effectiveReversal := reversalAmount
	if effectiveReversal.GreaterThan(totalAllocated) {
		effectiveReversal = totalAllocated
	}
	return effectiveReversal
}

func resolveOriginalChargeForReverseAllocation(
	db *gorm.DB,
	companyID uint,
	txn models.PaymentTransaction,
) (*models.PaymentTransaction, error) {
	if txn.OriginalTransactionID != nil {
		var directOrig models.PaymentTransaction
		if err := db.Where("id = ? AND company_id = ?", *txn.OriginalTransactionID, companyID).
			First(&directOrig).Error; err == nil {
			switch directOrig.TransactionType {
			case models.TxnTypeCharge, models.TxnTypeCapture:
				return &directOrig, nil
			case models.TxnTypeRefund, models.TxnTypeChargeback:
				return nil, ErrReverseAllocUnsupportedMultiLayerReversal
			}
		}
	}

	orig := resolveOriginalCharge(db, companyID, txn)
	if orig == nil {
		return nil, ErrReverseAllocNoOriginalTxn
	}
	return orig, nil
}

func remainingReverseAllocations(
	db *gorm.DB,
	companyID, originalTxnID uint,
	originalAllocs []models.PaymentAllocation,
) ([]models.PaymentAllocation, decimal.Decimal, decimal.Decimal, error) {
	type reversedRow struct {
		PaymentAllocationID uint
		Total               decimal.Decimal
	}

	var usedRows []reversedRow
	if err := db.Model(&models.PaymentReverseAllocation{}).
		Select("payment_allocation_id, COALESCE(SUM(amount), 0) AS total").
		Where("company_id = ? AND original_txn_id = ?", companyID, originalTxnID).
		Group("payment_allocation_id").
		Scan(&usedRows).Error; err != nil {
		return nil, decimal.Zero, decimal.Zero, err
	}

	usedByAlloc := make(map[uint]decimal.Decimal, len(usedRows))
	for _, row := range usedRows {
		usedByAlloc[row.PaymentAllocationID] = row.Total
	}

	remaining := make([]models.PaymentAllocation, 0, len(originalAllocs))
	totalAllocated := decimal.Zero
	totalRemaining := decimal.Zero

	for _, alloc := range originalAllocs {
		totalAllocated = totalAllocated.Add(alloc.AllocatedAmount)

		used := usedByAlloc[alloc.ID]
		if used.GreaterThan(alloc.AllocatedAmount) {
			return nil, decimal.Zero, decimal.Zero, fmt.Errorf(
				"%w: allocation %d already reversed beyond original amount",
				ErrReverseAllocExceedsReversibleTotal,
				alloc.ID,
			)
		}

		left := alloc.AllocatedAmount.Sub(used)
		if left.IsNegative() {
			return nil, decimal.Zero, decimal.Zero, fmt.Errorf(
				"%w: allocation %d has negative remaining reversible amount",
				ErrReverseAllocExceedsReversibleTotal,
				alloc.ID,
			)
		}
		if left.IsZero() {
			continue
		}

		allocCopy := alloc
		allocCopy.AllocatedAmount = left
		remaining = append(remaining, allocCopy)
		totalRemaining = totalRemaining.Add(left)
	}

	return remaining, totalAllocated, totalRemaining, nil
}

func resolvedReverseAllocationType(
	db *gorm.DB,
	companyID uint,
	txn models.PaymentTransaction,
	fallback models.ReverseAllocationType,
) (models.ReverseAllocationType, error) {
	if fallback == models.ReverseAllocDisputeLost {
		return fallback, nil
	}
	if fallback != models.ReverseAllocChargeback || txn.TransactionType != models.TxnTypeChargeback {
		return fallback, nil
	}

	var dispute models.GatewayDispute
	err := db.Where(
		"company_id = ? AND chargeback_transaction_id = ? AND status = ?",
		companyID, txn.ID, models.DisputeStatusLost,
	).First(&dispute).Error
	if err == nil {
		return models.ReverseAllocDisputeLost, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fallback, nil
	}
	return "", err
}
