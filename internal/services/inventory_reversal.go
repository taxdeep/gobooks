// 遵循project_guide.md
package services

// inventory_reversal.go — Inventory movement reversal for voided documents.
//
// When an invoice or bill is voided, the JE reversal is handled by VoidInvoice/VoidBill.
// This file handles the corresponding inventory movement reversals that must occur
// in the SAME transaction as the JE reversal.
//
// Reversal movement design:
//   - source_type = "invoice_reversal" or "bill_reversal" (clearly distinguishable)
//   - source_id = original document ID (traceability to source document)
//   - journal_entry_id = reversal JE ID (links to the accounting side)
//   - Reversal movements are new rows — original movements are never modified/deleted
//
// Invoice void (sale reversal):
//   Original: sale movement (qty_delta = -N)
//   Reversal: inbound via CostingEngine.ApplyInbound (qty_delta = +N, restores stock)
//   Cost: uses the original sale movement's unit_cost (what was charged as COGS)
//
// Bill void (purchase reversal):
//   Original: purchase movement (qty_delta = +N)
//   Reversal: outbound via CostingEngine.ApplyOutbound (qty_delta = -N, reduces stock)
//   Cost: uses the original purchase movement's unit_cost
//   Blocked if insufficient stock (negative inventory not allowed)

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services/inventory"
)

// ReverseSaleMovements creates reversal inventory movements for a voided invoice.
//
// Thin facade over inventory.ReverseMovement. For each original sale
// movement the facade books one reversal, inheriting the original's
// UnitCostBase (snapshot cost) — March's COGS reverses at March's cost,
// not the current-period weighted average.
func ReverseSaleMovements(tx *gorm.DB, companyID uint, inv models.Invoice) error {
	return reverseDocumentMovements(tx, companyID,
		reverseDocumentScope{
			sourceType:         "invoice",
			sourceID:           inv.ID,
			reversalSourceType: "invoice_reversal",
			movementDate:       inv.InvoiceDate,
			memo:               "Void: " + inv.InvoiceNumber,
			reason:             inventory.ReversalReasonCancellation,
		})
}

// ReversePurchaseMovements creates reversal inventory movements for a voided bill.
//
// Facade over inventory.ReverseMovement. Returns an ErrInsufficientStock-
// mapped error (via translateInventoryErr) if any original receipt cannot
// be fully reversed because its units were partially consumed by a later
// sale.
func ReversePurchaseMovements(tx *gorm.DB, companyID uint, bill models.Bill) error {
	return reverseDocumentMovements(tx, companyID,
		reverseDocumentScope{
			sourceType:         "bill",
			sourceID:           bill.ID,
			reversalSourceType: "bill_reversal",
			movementDate:       bill.BillDate,
			memo:               "Void: " + bill.BillNumber,
			reason:             inventory.ReversalReasonCancellation,
		})
}

// reverseDocumentScope captures the header-level context shared by every
// reversal movement generated from a single voided document. Kept private to
// the facade — the new inventory API works one movement at a time.
type reverseDocumentScope struct {
	sourceType         string // original document source_type ("invoice" or "bill")
	sourceID           uint   // original document ID
	reversalSourceType string // reversal movement's source_type
	movementDate       time.Time
	memo               string
	reason             inventory.ReversalReason
}

// reverseDocumentMovements iterates every original movement from the given
// document and books a reversal for each. Skips already-reversed rows so the
// function is safe to call on a partially-reversed document (defensive; not
// expected in the current posting engine but cheap to guard).
func reverseDocumentMovements(tx *gorm.DB, companyID uint, scope reverseDocumentScope) error {
	var origs []models.InventoryMovement
	if err := tx.Where("company_id = ? AND source_type = ? AND source_id = ?",
		companyID, scope.sourceType, scope.sourceID).
		Find(&origs).Error; err != nil {
		return fmt.Errorf("load %s movements: %w", scope.sourceType, err)
	}
	if len(origs) == 0 {
		return nil // service-only invoice or non-inventory bill
	}

	for _, orig := range origs {
		if orig.QuantityDelta.IsZero() {
			continue
		}
		if orig.ReversedByMovementID != nil {
			continue
		}
		if _, err := inventory.ReverseMovement(tx, inventory.ReverseMovementInput{
			CompanyID:          companyID,
			OriginalMovementID: orig.ID,
			MovementDate:       scope.movementDate,
			Reason:             scope.reason,
			SourceType:         scope.reversalSourceType,
			SourceID:           scope.sourceID,
			IdempotencyKey:     fmt.Sprintf("%s:%d:reverse:%d:v1", scope.sourceType, scope.sourceID, orig.ID),
			Memo:               scope.memo,
		}); err != nil {
			return fmt.Errorf("reverse movement %d: %w", orig.ID, translateInventoryErr(err))
		}
	}
	return nil
}
