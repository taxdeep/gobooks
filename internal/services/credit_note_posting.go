// 遵循project_guide.md
package services

// credit_note_posting.go — IN.5 business-document-layer
// orchestration that wires a posted Credit Note to inventory
// restoration (on stock-item lines, legacy mode) and to the
// Dr Inventory / Cr COGS portion of the credit note JE.
//
// Rule #4 on the AR return path
// -----------------------------
// Pre-IN.5, a credit note for a stock-item return booked only the
// AR-side revenue reversal (Dr Revenue / Cr AR). The COGS-side
// reversal never happened — inventory stayed decremented and COGS
// stayed on the P&L as a permanent ghost. That was a Rule #4
// silent-swallow AND an actual accounting imbalance (P&L showed
// $N of COGS against $0 of net revenue for the returned goods).
//
// IN.5 closes the gap for LEGACY companies
// (shipment_required=false) only. Controlled-mode companies are
// deferred to Phase I.6 Return Receipt; their Credit Note post
// rejects stock-item lines loudly via
// ErrCreditNoteStockItemRequiresReturnReceipt.
//
// Authoritative cost via traced original movement
// -----------------------------------------------
// The Q1 design decision pinned during IN.5 design: the return's
// Dr Inventory amount MUST be the original sale's snapshot cost,
// not the product's current weighted average. IN.5 traces the
// CreditNoteLine back to the original InvoiceLine's
// inventory_movement (via OriginalInvoiceLineID) and reads that
// row's UnitCostBase directly. The return is then booked as a
// fresh ReceiveStock movement — NOT as an inventory.ReverseMovement
// — because ReverseMovement reverses the full original qty whereas
// a customer may return a partial qty (e.g. 4 of 10 sold). The
// traced-cost read gives us partial-return support while preserving
// authoritative-cost semantics by construction.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/inventory"
)

// creditNoteReturnResult pairs a credit-note line with the
// inventory module's reversal result. The caller (PostCreditNote)
// uses these to form Dr Inventory / Cr COGS JE fragments.
type creditNoteReturnResult struct {
	Line            models.CreditNoteLine
	InventoryValue  decimal.Decimal // absolute value, positive
	InventoryAcctID uint            // per-line, from ProductService
	COGSAcctID      uint            // per-line, from ProductService
}

// CreateCreditNoteInventoryReturns performs inventory.ReverseMovement
// for each stock-item line on a posted credit note. Must be called
// inside the caller's transaction. Returns one result per stock
// line processed; pure-service lines are silently skipped (no
// inventory effect, same legacy behavior as before IN.5).
//
// Requires each stock line to carry OriginalInvoiceLineID; this
// invariant is pre-validated by PostCreditNote so reaching this
// function with a stock line missing the trace is an internal
// error.
//
// Authoritative cost note: the function never sets UnitCost on the
// return — inventory.ReverseMovement looks up the original
// movement's unit_cost_base and copies it onto the reversal row.
// This is the whole point of routing through that verb instead of
// creating a fresh ReceiveStock call (which would use current
// weighted avg).
func CreateCreditNoteInventoryReturns(tx *gorm.DB, cn models.CreditNote) ([]creditNoteReturnResult, error) {
	if cn.InvoiceID == nil || *cn.InvoiceID == 0 {
		// PostCreditNote's pre-flight rejects standalone credit
		// notes with stock lines; if we reach here with no invoice
		// linkage it's a pure-service credit note.
		return nil, nil
	}
	if len(cn.Lines) == 0 {
		return nil, nil
	}

	version, err := nextIdempotencyVersion(tx, cn.CompanyID, "credit_note", cn.ID)
	if err != nil {
		return nil, fmt.Errorf("pick idempotency version: %w", err)
	}

	out := make([]creditNoteReturnResult, 0, len(cn.Lines))
	for _, line := range cn.Lines {
		if line.ProductService == nil || !line.ProductService.IsStockItem {
			continue
		}
		if line.OriginalInvoiceLineID == nil || *line.OriginalInvoiceLineID == 0 {
			return nil, fmt.Errorf("%w: line id=%d", ErrCreditNoteStockItemRequiresOriginalLine, line.ID)
		}
		// Accounts for JE side of the restoration. Both must be
		// configured on the ProductService; fail loud at post time
		// if not (mirror Expense IN.2 / Receipt H.3 pattern).
		if line.ProductService.InventoryAccountID == nil || *line.ProductService.InventoryAccountID == 0 {
			return nil, fmt.Errorf("credit note line id=%d: product has no inventory_account_id — configure the product/service",
				line.ID)
		}
		if line.ProductService.COGSAccountID == nil || *line.ProductService.COGSAccountID == 0 {
			return nil, fmt.Errorf("credit note line id=%d: product has no cogs_account_id — configure the product/service",
				line.ID)
		}

		// Locate the original sale's inventory movement. Scoped by
		// company + source_type='invoice' + the specific invoice_line
		// via source_line_id. Cross-tenant locked by company_id; the
		// query deliberately does NOT match on source_id (invoice
		// header) so bundle-expansion movements (which carry
		// source_line_id=0) won't accidentally match a non-bundle
		// credit note return.
		var orig models.InventoryMovement
		err := tx.Where(
			"company_id = ? AND source_type = ? AND source_id = ? AND source_line_id = ?",
			cn.CompanyID, string(models.LedgerSourceInvoice), *cn.InvoiceID, *line.OriginalInvoiceLineID,
		).First(&orig).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: line id=%d could not locate original movement",
				ErrCreditNoteOriginalLineMismatch, line.ID)
		}
		if err != nil {
			return nil, fmt.Errorf("load original movement for line %d: %w", line.ID, err)
		}

		// Authoritative cost: the original sale movement's
		// unit_cost_base. Holds whether the customer returns all or
		// part of the original qty.
		var originalCost decimal.Decimal
		if orig.UnitCostBase != nil {
			originalCost = *orig.UnitCostBase
		}
		warehouseID := uint(0)
		if orig.WarehouseID != nil {
			warehouseID = *orig.WarehouseID
		}
		if warehouseID == 0 {
			return nil, fmt.Errorf("credit note line id=%d: original movement has no warehouse", line.ID)
		}

		// Book a fresh ReceiveStock at the traced cost and the
		// return qty. Partial-return safe: qty here is
		// CreditNoteLine.Qty, which may be < the original sale qty.
		lineID := line.ID
		result, rErr := inventory.ReceiveStock(tx, inventory.ReceiveStockInput{
			CompanyID:      cn.CompanyID,
			ItemID:         *line.ProductServiceID,
			WarehouseID:    warehouseID,
			Quantity:       line.Qty,
			MovementDate:   cn.CreditNoteDate,
			UnitCost:       originalCost,
			ExchangeRate:   decimal.NewFromInt(1),
			SourceType:     string(models.LedgerSourceCreditNote),
			SourceID:       cn.ID,
			SourceLineID:   &lineID,
			IdempotencyKey: fmt.Sprintf("credit_note:%d:line:%d:v%d", cn.ID, line.ID, version),
			Memo:           "Customer return: " + cn.CreditNoteNumber,
		})
		if rErr != nil {
			return nil, fmt.Errorf("receive returned stock for line %d: %w", line.ID, translateInventoryErr(rErr))
		}

		invVal := result.InventoryValueBase

		out = append(out, creditNoteReturnResult{
			Line:            line,
			InventoryValue:  invVal,
			InventoryAcctID: *line.ProductService.InventoryAccountID,
			COGSAcctID:      *line.ProductService.COGSAccountID,
		})
	}
	return out, nil
}

// buildCreditNoteInventoryFragments produces Dr Inventory / Cr COGS
// fragments for each stock-line reversal. Each pair is
// self-balancing so appending these to a credit note's already-
// balanced revenue-reversal JE preserves the balance.
//
// Amount semantics: InventoryValue per result is the cost restored
// to inventory at the ORIGINAL sale's snapshot unit cost × Qty
// returned. That exact amount is removed from COGS — the P&L
// correction that was missing before IN.5.
func buildCreditNoteInventoryFragments(results []creditNoteReturnResult, cnNumber string) []PostingFragment {
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
				AccountID: r.InventoryAcctID,
				Debit:     r.InventoryValue,
				Memo:      "Inventory in (return): " + desc,
			},
			PostingFragment{
				AccountID: r.COGSAcctID,
				Credit:    r.InventoryValue,
				Memo:      "COGS reversal (return): " + desc,
			},
		)
	}
	return frags
}
