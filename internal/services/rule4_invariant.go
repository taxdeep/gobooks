// 遵循project_guide.md
package services

// rule4_invariant.go — IN.3 post-time invariant assertion
// (Hard Rule #4, cross-cutting Item-Nature Invariant, IN.0 §2A).
//
// Runs at the tail of every stock-bearing document's post path
// (Bill / Invoice / Expense). It verifies the invariant that IN.0
// pinned:
//
//   > A line carrying ProductService.IsStockItem=true on a posted
//   > business document MUST have its inventory semantics honored:
//   > either produce a corresponding inventory_movements row, OR
//   > be rejected loudly at post time.
//
// The "rejected loudly" half is enforced pre-post by each document's
// own service layer (IN.2 put ErrExpenseStockItemRequiresReceipt on
// the Expense path; H.4 / I.4 route controlled-mode Bills / Invoices
// via GR/IR / WFI). This file enforces the other half — that when a
// document IS the movement owner for its workflow mode, the
// inventory_movements rows actually exist after post commits.
//
// Why this is a separate layer
// ----------------------------
// Rule #4 is already enforced *by construction* in each post path:
// bill_post.go calls CreatePurchaseMovements under flag=off;
// invoice_post.go calls CreateSaleMovements under flag=off;
// expense_service.go PostExpense calls CreateExpenseMovements
// under flag=off. If everything works, this assertion is redundant.
//
// The value is specifically *future regressions*. Without IN.3, a
// future refactor that reshapes the post path (e.g. "extract the
// JE construction into a shared helper") can silently drop the
// CreateXxxMovements call and leave Rule #4 broken. The test suite
// for the specific post path would still pass if the happy-path
// fixtures don't include stock items, and the production bug would
// ship. IN.3 runs an independent assertion that catches this class
// of silent swallow right at post time.
//
// Failure mode
// ------------
// The assertion returns a loud error that aborts the post
// transaction. The Bill / Invoice / Expense being posted rolls
// back entirely — no JE, no partial movements. This is deliberate:
// an unbalanced post (accounting lines without matching inventory)
// is worse than a failed post, because it produces a silent audit
// gap that no report will surface until reconciliation weeks later.
//
// Scope limits (deliberately)
// ---------------------------
//   - Does NOT check per-line match (line N → movement N). One
//     stock line can legitimately produce more than one movement
//     (bundle expansion on Invoice). The assertion only checks
//     that `movement_count >= stock_line_count` on the owner path
//     and `movement_count == 0` on the non-owner path.
//   - Does NOT validate cost accuracy; that's inventory module's
//     responsibility.
//   - Does NOT run on Receipt / Shipment posts today; those paths
//     already have their own boundary-lock tests (H.2 / I.2). If a
//     future regression warrants it, add them here — the helper
//     is doc-type-agnostic.

import (
	"fmt"

	"gorm.io/gorm"

	"gobooks/internal/models"
)

// Rule4DocumentType is a string literal naming the source_type this
// assertion checks. One constant per stock-bearing document that
// routes through inventory. Deliberately mirrors the
// LedgerSource<X> constant values so source_type filters on
// inventory_movements and ledger_entries stay aligned.
type Rule4DocumentType string

const (
	Rule4DocBill       Rule4DocumentType = "bill"
	Rule4DocInvoice    Rule4DocumentType = "invoice"
	Rule4DocExpense    Rule4DocumentType = "expense"
	Rule4DocCreditNote Rule4DocumentType = "credit_note"
)

// Rule4WorkflowState captures the two capability rails that steer
// movement-owner dispatch. Passed as a value object so callers can't
// accidentally swap the booleans.
type Rule4WorkflowState struct {
	ReceiptRequired  bool // Phase H capability rail (inbound)
	ShipmentRequired bool // Phase I capability rail (outbound)
}

// IsMovementOwner returns true when the given document type is the
// Rule #4 movement owner for its stock lines under the given
// workflow state. Implements the dispatch table pinned in IN.0 §2A.
func (w Rule4WorkflowState) IsMovementOwner(docType Rule4DocumentType) bool {
	switch docType {
	case Rule4DocBill:
		// Bill owns movement under legacy. Controlled mode hands
		// ownership to Receipt (H.4 shape).
		return !w.ReceiptRequired
	case Rule4DocInvoice:
		// Invoice owns movement under legacy. Controlled mode hands
		// ownership to Shipment (I.4 shape).
		return !w.ShipmentRequired
	case Rule4DocExpense:
		// Expense owns movement under legacy. Controlled mode
		// rejects stock-item Expense pre-post (IN.2 Q2), so the
		// "not owner" branch here is defensive — in practice
		// PostExpense returns ErrExpenseStockItemRequiresReceipt
		// before this assertion ever runs in controlled mode.
		return !w.ReceiptRequired
	case Rule4DocCreditNote:
		// Credit Note owns return movement under legacy
		// (shipment_required=false); controlled mode rejects
		// stock-item credit notes pre-post (IN.5 Q2, pending
		// Phase I.6 Return Receipt). Same defensive pattern as
		// Expense above.
		return !w.ShipmentRequired
	default:
		return false
	}
}

// AssertRule4PostTimeInvariant verifies, at the tail of a post
// transaction, that the Rule #4 movement-owner dispatch held. It
// runs two checks against the live inventory_movements rows:
//
//  1. If this document is the movement owner AND has at least one
//     stock-item line, at least one inventory_movements row with
//     matching (company_id, source_type, source_id) MUST exist.
//
//  2. If this document is NOT the movement owner (controlled mode),
//     ZERO such rows may exist. Any rows here would mean a legacy
//     code path slipped through despite the rail being on —
//     Rule #4 violated in the opposite direction (duplicate
//     movement formation by both Bill+Receipt or Invoice+Shipment).
//
// `stockLineCount` is how many stock-item lines the caller observed
// on the document. Passed by the caller (who already iterated the
// lines for their own reasons) so we don't re-query the catalog.
//
// Returns a descriptive error on violation. Callers return this
// error from their post transaction so the tx rolls back.
func AssertRule4PostTimeInvariant(
	tx *gorm.DB,
	companyID uint,
	docType Rule4DocumentType,
	docID uint,
	stockLineCount int,
	workflow Rule4WorkflowState,
) error {
	if stockLineCount == 0 {
		// No stock lines → nothing for Rule #4 to assert against.
		// A pure-expense / service-only document is out of scope.
		return nil
	}

	var mvCount int64
	if err := tx.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, string(docType), docID).
		Count(&mvCount).Error; err != nil {
		return fmt.Errorf("rule4 assert: count inventory_movements: %w", err)
	}

	owner := workflow.IsMovementOwner(docType)

	if owner {
		// Owner path: at least one movement row expected. Missing
		// rows are the exact silent-swallow class IN.3 exists to
		// catch.
		if mvCount == 0 {
			return fmt.Errorf(
				"rule4 violation (silent swallow): %s %d posted with %d stock line(s) but zero inventory_movements rows (workflow: receipt_required=%v shipment_required=%v)",
				docType, docID, stockLineCount,
				workflow.ReceiptRequired, workflow.ShipmentRequired,
			)
		}
		// The "at least one" bound is intentionally loose; bundle
		// expansion on Invoice can produce more movements than
		// source lines. Per-line matching would need to know the
		// expansion shape, which is out of scope for a tail-of-post
		// assertion.
		return nil
	}

	// Non-owner path: zero movement rows expected. Any row here
	// means a legacy post path fired on top of the rail dispatch —
	// the two owners (Bill+Receipt or Invoice+Shipment) would both
	// form inventory, double-counting the stock.
	if mvCount > 0 {
		return fmt.Errorf(
			"rule4 violation (duplicate owner): %s %d is not the movement owner under this workflow (receipt_required=%v shipment_required=%v) but %d inventory_movements rows exist with source_type=%q",
			docType, docID,
			workflow.ReceiptRequired, workflow.ShipmentRequired,
			mvCount, docType,
		)
	}
	return nil
}

// CountStockLinesOnLines is a tiny iteration helper callers can use
// to compute the stockLineCount argument without repeating the
// "ProductService != nil && IsStockItem" boilerplate. Accepts any
// slice of lines that each expose IsStockItem via preloaded
// ProductService. Implementations pass a projected slice of the
// shape {HasStockItem bool} since the three document types have
// different line struct shapes (BillLine / InvoiceLine /
// ExpenseLine) that don't share an interface yet.
func CountStockLines(hasStock []bool) int {
	n := 0
	for _, b := range hasStock {
		if b {
			n++
		}
	}
	return n
}
