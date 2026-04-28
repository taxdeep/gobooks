// 遵循project_guide.md
package services

// vendor_credit_note_posting.go — IN.6a business-document-layer
// orchestration wiring a posted Vendor Credit Note to inventory
// outflow (on stock-item lines, legacy mode) and to the Dr Offset /
// Cr Inventory portion of the credit note JE.
//
// Rule #4 on the AP return path
// -----------------------------
// Pre-IN.6, a vendor credit note for a stock-item return booked only
// the AP-side adjustment (Dr AP / Cr PurchaseReturns). The
// Inventory-side reversal never happened — goods stayed on the books
// even after being physically returned to the vendor. That was a
// Rule #4 silent-swallow AND an accounting imbalance (Inventory
// overstated against a matching AP reversal that never removed the
// asset).
//
// IN.6a closes the gap for LEGACY companies
// (receipt_required=false) only. Controlled-mode companies are
// deferred to a future Vendor Return Receipt slice; their VCN post
// rejects stock-item lines loudly via
// ErrVendorCreditNoteStockItemRequiresReturnReceipt.
//
// Authoritative cost via inventory.ReverseMovement
// ------------------------------------------------
// Unlike IN.5 (AR side), which used ReceiveStock at a traced cost,
// IN.6a uses ReverseMovement on the original Bill inventory movement.
// ReverseMovement reads the original's snapshot unit_cost_base and
// reverses at that exact cost — the authoritative-cost property by
// construction. The trade-off: ReverseMovement reverses the ORIGINAL
// qty in full, which means partial returns (line qty < original
// qty) are not supported in this slice and are rejected with
// ErrVendorCreditNotePartialReturnNotSupported.
//
// Why not symmetric-with-IN.5?
// ----------------------------
// The inventory module exposes ReceiveStock(UnitCost) as an inflow
// verb but IssueStock deliberately does NOT accept a caller-supplied
// cost — "callers never pass a cost on outflow". Implementing the
// partial-return path for AP would either (a) extend IssueStock
// with a UnitCostOverride (API surface change, scope creep beyond
// IN.6a) or (b) book at current WA/FIFO with a PPV adjustment leg
// (unsound accounting shape). We pick the clean full-qty-only path
// here and defer partial-return support to a follow-up slice.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/inventory"
)

// vcnReturnResult pairs a VCN line with the inventory module's
// reversal result. PostVendorCreditNote uses these to form
// Dr Offset / Cr Inventory JE fragments.
type vcnReturnResult struct {
	Line            models.VendorCreditNoteLine
	InventoryValue  decimal.Decimal // absolute value, positive
	InventoryAcctID uint            // per-line, from ProductService
}

// CreateVendorCreditNoteInventoryReturns performs
// inventory.ReverseMovement for each stock-item line on a posted
// vendor credit note. Must be called inside the caller's transaction.
// Returns one result per stock line processed; pure-service lines
// are silently skipped (no inventory effect, header-only legacy
// behaviour applies).
//
// Requires each stock line to carry OriginalBillLineID; this
// invariant is pre-validated by PostVendorCreditNote so reaching
// this function with a stock line missing the trace is an internal
// error.
//
// Requires each stock line's Qty to equal the original Bill
// movement's QuantityDelta — partial returns fall out here with
// ErrVendorCreditNotePartialReturnNotSupported.
func CreateVendorCreditNoteInventoryReturns(tx *gorm.DB, vcn models.VendorCreditNote) ([]vcnReturnResult, error) {
	if vcn.BillID == nil || *vcn.BillID == 0 {
		// PostVendorCreditNote's pre-flight rejects standalone VCNs
		// with stock lines; if we reach here with no bill linkage it
		// is a pure-service credit note.
		return nil, nil
	}
	if len(vcn.Lines) == 0 {
		return nil, nil
	}

	out := make([]vcnReturnResult, 0, len(vcn.Lines))
	for _, line := range vcn.Lines {
		if line.ProductService == nil || !line.ProductService.IsStockItem {
			continue
		}
		if line.OriginalBillLineID == nil || *line.OriginalBillLineID == 0 {
			return nil, fmt.Errorf("%w: line id=%d", ErrVendorCreditNoteStockItemRequiresOriginalLine, line.ID)
		}
		// Inventory account required for the JE side; fail loud at
		// post time (mirror IN.5 / IN.2 pattern).
		if line.ProductService.InventoryAccountID == nil || *line.ProductService.InventoryAccountID == 0 {
			return nil, fmt.Errorf("vendor credit note line id=%d: product has no inventory_account_id — configure the product/service",
				line.ID)
		}

		// Locate the original purchase's inventory movement. Scoped
		// by company + source_type='bill' + (source_id, source_line_id)
		// to the specific BillLine. Cross-tenant locked by
		// company_id. No match on bundle-expansion rows
		// (source_line_id=0) so bundle-component moves cannot be
		// mistaken for a non-bundle VCN return.
		var orig models.InventoryMovement
		err := tx.Where(
			"company_id = ? AND source_type = ? AND source_id = ? AND source_line_id = ?",
			vcn.CompanyID, string(models.LedgerSourceBill), *vcn.BillID, *line.OriginalBillLineID,
		).First(&orig).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: line id=%d could not locate original movement",
				ErrVendorCreditNoteOriginalLineMismatch, line.ID)
		}
		if err != nil {
			return nil, fmt.Errorf("load original movement for vcn line %d: %w", line.ID, err)
		}

		// Full-qty-only gate. ReverseMovement flips the sign of the
		// ORIGINAL QuantityDelta wholesale; supporting a partial qty
		// here would require either extending IssueStock with a
		// caller-supplied cost (scope beyond IN.6a) or booking at
		// current WA with a PPV balancing entry (unsound). Reject
		// loudly and direct the user to the runbook.
		if !line.Qty.Equal(orig.QuantityDelta) {
			return nil, fmt.Errorf("%w: line id=%d qty=%s, original movement qty=%s",
				ErrVendorCreditNotePartialReturnNotSupported,
				line.ID, line.Qty.String(), orig.QuantityDelta.String())
		}

		// Book the reversal. ReverseMovement reads orig.UnitCostBase
		// as the snapshot cost and writes a negative-delta row at
		// that cost — inventory out, at the original purchase price,
		// exactly like "we gave back the stuff we paid $X for".
		result, rErr := inventory.ReverseMovement(tx, inventory.ReverseMovementInput{
			CompanyID:          vcn.CompanyID,
			OriginalMovementID: orig.ID,
			SourceType:         string(models.LedgerSourceVendorCreditNote),
			SourceID:           vcn.ID,
			MovementDate:       vcn.CreditNoteDate,
			IdempotencyKey:     fmt.Sprintf("vendor_credit_note:%d:line:%d", vcn.ID, line.ID),
			Memo:               "Return to vendor: " + vcn.CreditNoteNumber,
		})
		if rErr != nil {
			return nil, fmt.Errorf("reverse bill movement for vcn line %d: %w", line.ID, translateInventoryErr(rErr))
		}

		// ReverseMovement.ReversalValueBase is signed (negative =
		// outflow, opposite of the original inflow). Take absolute
		// value for the Cr Inventory fragment amount.
		invVal := result.ReversalValueBase.Abs()

		out = append(out, vcnReturnResult{
			Line:            line,
			InventoryValue:  invVal,
			InventoryAcctID: *line.ProductService.InventoryAccountID,
		})
	}
	return out, nil
}

// buildVendorCreditNoteInventoryFragments produces Dr Offset /
// Cr Inventory fragments for each stock-line reversal. Each pair is
// self-balancing so appending these to a VCN's already-balanced
// Dr AP / Cr Offset header JE preserves the balance. Net effect:
// header Cr Offset cancels appended Dr Offset for the stock portion
// → the stock portion nets to Dr AP / Cr Inventory, which is the
// correct shape for a physical return.
func buildVendorCreditNoteInventoryFragments(results []vcnReturnResult, offsetAcctID uint, vcnNumber string) []PostingFragment {
	if len(results) == 0 {
		return nil
	}
	frags := make([]PostingFragment, 0, len(results)*2)
	for _, r := range results {
		if !r.InventoryValue.IsPositive() {
			continue
		}
		desc := r.Line.Description
		if desc == "" && r.Line.ProductService != nil {
			desc = r.Line.ProductService.Name
		}
		frags = append(frags,
			PostingFragment{
				AccountID: offsetAcctID,
				Debit:     r.InventoryValue,
				Memo:      "Purchase return (stock portion): " + desc,
			},
			PostingFragment{
				AccountID: r.InventoryAcctID,
				Credit:    r.InventoryValue,
				Memo:      "Inventory out (return): " + desc,
			},
		)
	}
	return frags
}
