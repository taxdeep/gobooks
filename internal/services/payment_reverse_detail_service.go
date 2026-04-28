// 遵循project_guide.md
package services

// payment_reverse_detail_service.go — Batch 25: Payment reverse exception detail rollup.
//
// Provides a structured summary of the original and reverse payment allocation
// picture for a PaymentReverseException.  This is a READ-ONLY aggregation layer;
// it derives display data from existing truth tables and never persists anything.
//
// ─── Layer position ───────────────────────────────────────────────────────────
//
//   PaymentTransaction / PaymentAllocation         ← forward allocation truth
//   PaymentReverseAllocation                       ← reverse allocation truth (Batch 22)
//   PaymentReverseException                        ← exception truth (Batch 23)
//   PaymentReverseDetailRollup (THIS)              ← display-only aggregation
//
// ─── Allocation strategy detection ──────────────────────────────────────────
//
//   "multi"  — original charge has PaymentAllocation rows (Batch 17 multi-path)
//   "single" — original charge used the single-invoice path (AppliedInvoiceID set)
//   "none"   — no original charge linkage or original charge has no allocations
//
// ─── Reverse applied detection ────────────────────────────────────────────────
//
//   Multi path  : PaymentReverseAllocation rows exist for the reverse txn
//   Single path : ReverseTxn.AppliedInvoiceID is non-nil
//   None        : always false
//
// ─── Type-specific next-step guidance ────────────────────────────────────────
//
//   Each exception type gets a brief operator-facing guidance string describing
//   the recommended investigation or resolution path.  The guidance is static
//   per type and does not require DB access.
//
// ─── Not in Batch 25 ──────────────────────────────────────────────────────────
//   - No execution paths or override capabilities
//   - No new exception / JE / allocation creation
//   - No AI or SLA logic

import (
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Rollup types ──────────────────────────────────────────────────────────────

// PaymentReverseDetailRollup is the display-ready summary for a reverse exception
// detail page.  All fields are derived from existing truth tables.
// This struct is ephemeral — it is NEVER persisted.
type PaymentReverseDetailRollup struct {
	// Source transactions (nil when the exception has no link to a txn).
	ReverseTxn  *models.PaymentTransaction
	OriginalTxn *models.PaymentTransaction

	// AllocationStrategy describes how the original charge distributed its value.
	//   "multi"  — multi-invoice PaymentAllocation rows exist
	//   "single" — single-invoice path (AppliedInvoiceID on original txn)
	//   "none"   — no original txn or no allocation state found
	AllocationStrategy string

	// Forward allocation: how the original charge was applied to invoices.
	ForwardAllocLines []RollupLine
	ForwardTotal      decimal.Decimal

	// Reverse allocation: how the reverse txn restored invoice balances.
	// Empty when the reverse has not yet been applied.
	ReverseAllocLines []RollupLine
	ReverseTotal      decimal.Decimal

	// IsReverseApplied is true when the reverse txn has been applied to invoices
	// via either allocation path.
	IsReverseApplied bool

	// NextStepGuidance is a brief operator-facing description of the recommended
	// investigation or resolution path for this exception type.
	NextStepGuidance string
}

// RollupLine represents one invoice's share in a forward or reverse allocation.
type RollupLine struct {
	InvoiceID     uint
	InvoiceNumber string
	Amount        decimal.Decimal
}

// ── Public API ────────────────────────────────────────────────────────────────

// BuildPaymentReverseDetailRollup constructs a PaymentReverseDetailRollup for the
// given exception.  It loads all required data in a minimal number of queries.
//
// Errors are returned only for unexpected DB failures.  Missing optional links
// (e.g. no reverse txn) produce a valid rollup with nil/empty fields rather than
// an error.
func BuildPaymentReverseDetailRollup(
	db *gorm.DB,
	companyID uint,
	ex *models.PaymentReverseException,
) (*PaymentReverseDetailRollup, error) {
	if ex == nil {
		return nil, fmt.Errorf("exception is nil")
	}

	r := &PaymentReverseDetailRollup{
		AllocationStrategy: "none",
		NextStepGuidance:   prExceptionNextStepGuidance(ex.ExceptionType),
	}

	// ── Load linked transactions ──────────────────────────────────────────────

	if ex.ReverseTxnID != nil {
		var txn models.PaymentTransaction
		if err := db.Where("id = ? AND company_id = ?", *ex.ReverseTxnID, companyID).
			First(&txn).Error; err == nil {
			r.ReverseTxn = &txn
		}
	}

	if ex.OriginalTxnID != nil {
		var txn models.PaymentTransaction
		if err := db.Where("id = ? AND company_id = ?", *ex.OriginalTxnID, companyID).
			First(&txn).Error; err == nil {
			r.OriginalTxn = &txn
		}
	}

	// ── Detect allocation strategy and load forward lines ─────────────────────

	if r.OriginalTxn != nil {
		forwardAllocs, err := ListPaymentAllocations(db, companyID, r.OriginalTxn.ID)
		if err != nil {
			return nil, fmt.Errorf("load forward allocations: %w", err)
		}

		if len(forwardAllocs) > 0 {
			// Multi-invoice path.
			r.AllocationStrategy = "multi"
			invoiceIDs := make([]uint, len(forwardAllocs))
			for i, a := range forwardAllocs {
				invoiceIDs[i] = a.InvoiceID
			}
			invoiceNumbers, err := batchLoadInvoiceNumbers(db, companyID, invoiceIDs)
			if err != nil {
				return nil, fmt.Errorf("load invoice numbers for forward allocs: %w", err)
			}
			r.ForwardAllocLines = make([]RollupLine, len(forwardAllocs))
			for i, a := range forwardAllocs {
				r.ForwardAllocLines[i] = RollupLine{
					InvoiceID:     a.InvoiceID,
					InvoiceNumber: invoiceNumbers[a.InvoiceID],
					Amount:        a.AllocatedAmount,
				}
				r.ForwardTotal = r.ForwardTotal.Add(a.AllocatedAmount)
			}
		} else if r.OriginalTxn.AppliedInvoiceID != nil {
			// Single-invoice path.
			r.AllocationStrategy = "single"
			invoiceID := *r.OriginalTxn.AppliedInvoiceID
			amount := r.OriginalTxn.Amount
			if r.OriginalTxn.AppliedAmount != nil {
				amount = *r.OriginalTxn.AppliedAmount
			}
			invoiceNumbers, err := batchLoadInvoiceNumbers(db, companyID, []uint{invoiceID})
			if err != nil {
				return nil, fmt.Errorf("load invoice number for single-path forward alloc: %w", err)
			}
			r.ForwardAllocLines = []RollupLine{{
				InvoiceID:     invoiceID,
				InvoiceNumber: invoiceNumbers[invoiceID],
				Amount:        amount,
			}}
			r.ForwardTotal = amount
		}
		// else: strategy stays "none" — original txn exists but no allocations
	}

	// ── Load reverse allocation lines ─────────────────────────────────────────

	if r.ReverseTxn != nil {
		switch r.AllocationStrategy {
		case "multi":
			revAllocs, err := ListReverseAllocationsForTxn(db, companyID, r.ReverseTxn.ID)
			if err != nil {
				return nil, fmt.Errorf("load reverse allocations: %w", err)
			}
			if len(revAllocs) > 0 {
				r.IsReverseApplied = true
				invoiceIDs := make([]uint, len(revAllocs))
				for i, a := range revAllocs {
					invoiceIDs[i] = a.InvoiceID
				}
				invoiceNumbers, err := batchLoadInvoiceNumbers(db, companyID, invoiceIDs)
				if err != nil {
					return nil, fmt.Errorf("load invoice numbers for reverse allocs: %w", err)
				}
				r.ReverseAllocLines = make([]RollupLine, len(revAllocs))
				for i, a := range revAllocs {
					r.ReverseAllocLines[i] = RollupLine{
						InvoiceID:     a.InvoiceID,
						InvoiceNumber: invoiceNumbers[a.InvoiceID],
						Amount:        a.Amount,
					}
					r.ReverseTotal = r.ReverseTotal.Add(a.Amount)
				}
			}

		case "single":
			// Single-invoice reverse: ReverseTxn.AppliedInvoiceID set by the
			// single-invoice reverse-apply path.
			if r.ReverseTxn.AppliedInvoiceID != nil {
				r.IsReverseApplied = true
				invoiceID := *r.ReverseTxn.AppliedInvoiceID
				amount := r.ReverseTxn.Amount
				if r.ReverseTxn.AppliedAmount != nil {
					amount = *r.ReverseTxn.AppliedAmount
				}
				invoiceNumbers, err := batchLoadInvoiceNumbers(db, companyID, []uint{invoiceID})
				if err != nil {
					return nil, fmt.Errorf("load invoice number for single-path reverse alloc: %w", err)
				}
				r.ReverseAllocLines = []RollupLine{{
					InvoiceID:     invoiceID,
					InvoiceNumber: invoiceNumbers[invoiceID],
					Amount:        amount,
				}}
				r.ReverseTotal = amount
			}
		}
	}

	return r, nil
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// batchLoadInvoiceNumbers loads InvoiceNumber for each given invoice ID in a
// single query and returns a map[invoiceID]invoiceNumber.
// Missing invoice rows yield an empty string in the map (defensive; should not
// occur under normal company-isolated DB state).
func batchLoadInvoiceNumbers(db *gorm.DB, companyID uint, invoiceIDs []uint) (map[uint]string, error) {
	result := make(map[uint]string, len(invoiceIDs))
	if len(invoiceIDs) == 0 {
		return result, nil
	}

	// Deduplicate IDs.
	seen := make(map[uint]struct{}, len(invoiceIDs))
	deduped := invoiceIDs[:0]
	for _, id := range invoiceIDs {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			deduped = append(deduped, id)
		}
	}

	type row struct {
		ID            uint
		InvoiceNumber string
	}
	var rows []row
	if err := db.Model(&models.Invoice{}).
		Select("id, invoice_number").
		Where("id IN ? AND company_id = ?", deduped, companyID).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[r.ID] = r.InvoiceNumber
	}
	return result, nil
}

// prExceptionNextStepGuidance returns a brief operator-facing guidance string
// for the given exception type.  This is static per type.
func prExceptionNextStepGuidance(t models.PaymentReverseExceptionType) string {
	switch t {
	case models.PRExceptionReverseAllocationAmbiguous:
		return "The reverse transaction could not be linked to an original charge. " +
			"Locate the original charge/capture transaction, set its ID on the reverse " +
			"transaction's OriginalTransactionID field, then retry the allocation. " +
			"If no original transaction exists, dismiss this exception with a note."
	case models.PRExceptionAmountExceedsStrategy:
		return "The reversal amount exceeds the remaining reversible capacity of the " +
			"original charge's allocation. Check whether partial reversals have already " +
			"been applied. Adjust the reversal amount or manually restore the affected " +
			"invoices, then mark this exception resolved."
	case models.PRExceptionOverCreditBoundary:
		return "Applying this reversal would push an invoice's balance above its total " +
			"amount. Inspect the forward and reverse allocation lines below to identify " +
			"the discrepancy. Correct the invoice amount or the reversal amount, then " +
			"retry the allocation."
	case models.PRExceptionRequiresManualSplit:
		return "The system could not automatically split this reversal across invoices " +
			"(e.g. FX invoice or selective partial reversal). Manually restore each " +
			"affected invoice's balance via the invoice detail page, then mark this " +
			"exception resolved."
	case models.PRExceptionChainConflict:
		return "A conflicting reverse chain was detected — the original charge may " +
			"itself be a reversal, or a concurrent reverse apply created a conflict. " +
			"Review the linked transactions carefully. Dismiss if the reversal has " +
			"already been handled; otherwise resolve the chain manually."
	case models.PRExceptionUnsupportedMultiLayerReversal:
		return "This reversal points to another reversal transaction rather than " +
			"directly to a charge/capture. Multi-layer reversal chains are not " +
			"supported for automatic allocation. Trace back to the original charge " +
			"and manually restore the affected invoices."
	default:
		return "Review the linked transactions and allocation lines below. " +
			"If the reversal has been handled outside the system, mark this exception resolved."
	}
}
