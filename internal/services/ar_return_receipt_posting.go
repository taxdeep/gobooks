// 遵循project_guide.md
package services

// ar_return_receipt_posting.go — Phase I slice I.6a.2: the
// business-document-layer orchestration that wires a posted
// ARReturnReceipt to receive truth (inventory.ReceiveStock at the
// TRACED original-sale cost) and to the Dr Inventory / Cr COGS JE.
//
// Three-layer split (same boundary as H.3 / I.3)
// ----------------------------------------------
//
//   POSTED ARReturnReceipt   (business document — models.ARReturnReceipt)
//         │
//         │  CreateARReturnReceiptMovements projects each stock line:
//         │    - traces the ORIGINAL sale movement via the chain
//         │      ARReturnReceiptLine → CreditNoteLine →
//         │      CreditNoteLine.OriginalInvoiceLineID → InvoiceLine
//         │      → inventory_movements (source_type='invoice')
//         │    - reads the original movement's unit_cost_base as the
//         │      authoritative return cost
//         │    - calls inventory.ReceiveStock at (WarehouseID of the
//         │      ARReturnReceipt, ProductServiceID of the line, Qty,
//         │      UnitCost = traced cost)
//         ▼
//   RECEIVE TRUTH            (inventory_movements row,
//                             source_type='ar_return_receipt')
//         │
//         │  Internal to the inventory module: cost layer / balance
//         │  machinery — the business-document layer never mutates
//         │  inventory_balances or cost layers directly.
//         ▼
//   INVENTORY EFFECT         (inventory_balances, inventory_cost_layers)
//
// Separately, the business-document layer constructs the JE from
// ReceiveStockResult.InventoryValueBase:
//
//   Dr Inventory  (per line, amount = InventoryValueBase)
//   Cr COGS       (per line, amount = InventoryValueBase)
//
// Self-balancing per line (same amount on both sides). The inventory
// package has zero knowledge of the COGS / Inventory accounts or the
// JE — Hard Rule #3 is preserved by construction.
//
// Why traced cost and not current weighted-avg
// --------------------------------------------
// Same reasoning as IN.5 (credit_note_posting.go): March's COGS
// reverses at March's cost, never today's drifted weighted-average.
// ARReturnReceipt inherits this by reading the same trace field
// (CreditNoteLine.OriginalInvoiceLineID). Partial returns fall out
// naturally — the cost is a rate applied to the return qty.
//
// Why ReceiveStock (fresh inbound) and not ReverseMovement
// --------------------------------------------------------
// Same reason as IN.5: ReverseMovement would reverse the FULL
// original qty. Partial customer returns (e.g. 4 of 10 sold) need
// the rate × qty shape that a fresh ReceiveStock at the traced cost
// provides. The traced-cost read gives us both partial-return support
// AND authoritative-cost semantics by construction.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/inventory"
)

// arReturnReceiptResult pairs an ARReturnReceiptLine with the
// inventory module's receive result. Used to build JE fragments.
type arReturnReceiptResult struct {
	Line            models.ARReturnReceiptLine
	InventoryValue  decimal.Decimal // absolute value, positive
	InventoryAcctID uint            // per-line, from ProductService
	COGSAcctID      uint            // per-line, from ProductService
}

// CreateARReturnReceiptMovements performs inventory.ReceiveStock for
// each stock-item line on a posted ARReturnReceipt at the TRACED
// original-sale cost. Must be called inside the caller's transaction.
// Returns one result per stock line processed; pure-service lines
// skipped silently.
//
// Preconditions (enforced by PostARReturnReceipt before calling):
//   - ARReturnReceipt.Lines preloaded with ProductService + CreditNoteLine
//   - ARReturnReceipt.CreditNote preloaded (for InvoiceID trace anchor)
//   - Each stock-item line carries CreditNoteLineID
//
// Requires each stock line's CreditNoteLine to carry
// OriginalInvoiceLineID (the IN.5 field) — this is the key used to
// locate the original sale's inventory_movement. Missing trace =
// ErrARReturnReceiptLineMissingOriginalInvoiceLine.
//
// Warehouse note: the ReceiveStock call uses ARReturnReceipt's
// WarehouseID, NOT the original sale's warehouse. Goods go back to
// wherever the physical return actually landed, captured on the
// Return Receipt header.
func CreateARReturnReceiptMovements(tx *gorm.DB, r models.ARReturnReceipt) ([]arReturnReceiptResult, error) {
	if len(r.Lines) == 0 {
		return nil, nil
	}
	if r.CreditNote == nil {
		return nil, fmt.Errorf("ar_return_receipt %d: CreditNote not preloaded", r.ID)
	}
	if r.CreditNote.InvoiceID == nil || *r.CreditNote.InvoiceID == 0 {
		// The linked CreditNote has no Invoice anchor — cost tracing
		// cannot proceed. Standalone CreditNotes without an invoice
		// link aren't a Return Receipt candidate under IN.5's model.
		return nil, fmt.Errorf("ar_return_receipt %d: linked credit_note %d has no Invoice anchor — cost trace broken",
			r.ID, r.CreditNote.ID)
	}

	version, err := nextIdempotencyVersion(tx, r.CompanyID, "ar_return_receipt", r.ID)
	if err != nil {
		return nil, fmt.Errorf("pick idempotency version: %w", err)
	}

	out := make([]arReturnReceiptResult, 0, len(r.Lines))
	for _, line := range r.Lines {
		if line.ProductService == nil {
			return nil, fmt.Errorf("ar_return_receipt line %d: ProductService not preloaded", line.ID)
		}
		if !line.ProductService.IsStockItem {
			continue
		}
		if line.CreditNoteLine == nil {
			return nil, fmt.Errorf("ar_return_receipt line %d: CreditNoteLine not preloaded or absent — required at post time per Q7 hard rule #2",
				line.ID)
		}
		if line.CreditNoteLine.OriginalInvoiceLineID == nil || *line.CreditNoteLine.OriginalInvoiceLineID == 0 {
			return nil, fmt.Errorf("%w: line id=%d credit_note_line id=%d",
				ErrARReturnReceiptLineMissingOriginalInvoiceLine,
				line.ID, line.CreditNoteLine.ID)
		}
		// Accounts for JE construction. Both required on ProductService;
		// fail loud at post (mirror IN.5 / Receipt H.3 pattern).
		if line.ProductService.InventoryAccountID == nil || *line.ProductService.InventoryAccountID == 0 {
			return nil, fmt.Errorf("ar_return_receipt line id=%d: product has no inventory_account_id — configure the product/service",
				line.ID)
		}
		if line.ProductService.COGSAccountID == nil || *line.ProductService.COGSAccountID == 0 {
			return nil, fmt.Errorf("ar_return_receipt line id=%d: product has no cogs_account_id — configure the product/service",
				line.ID)
		}

		// Locate the original sale's inventory movement. Rail-aware:
		//
		//   - Controlled mode (shipment_required=true at sale time):
		//     Invoice post did NOT form inventory (I.4 decoupling).
		//     The original movement has source_type='shipment' and is
		//     indexed by (shipment_id, shipment_line_id) — reachable
		//     via InvoiceLine.ShipmentLineID → ShipmentLine.ShipmentID
		//     (the I.5 chain).
		//
		//   - Legacy fallback (shipment_required was false when the
		//     sale posted; now flipped to true, and an operator uses
		//     ARReturnReceipt for pre-flip sales): Invoice post formed
		//     inventory at source_type='invoice', indexed by
		//     (invoice_id, invoice_line_id). Use that if no
		//     ShipmentLineID is present on the InvoiceLine.
		//
		// Either way, the traced row's `unit_cost_base` is the
		// authoritative original cost used for the return leg.
		var invLine models.InvoiceLine
		if err := tx.Where("id = ? AND company_id = ?",
			*line.CreditNoteLine.OriginalInvoiceLineID, r.CompanyID).
			First(&invLine).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("%w: line id=%d invoice_line id=%d not found",
					ErrARReturnReceiptLineOriginalMovementNotFound,
					line.ID, *line.CreditNoteLine.OriginalInvoiceLineID)
			}
			return nil, fmt.Errorf("load invoice line for ar_return line %d: %w", line.ID, err)
		}

		var orig models.InventoryMovement
		var qErr error
		if invLine.ShipmentLineID != nil && *invLine.ShipmentLineID != 0 {
			// I.5 chain — traced shipment movement.
			var shipLine models.ShipmentLine
			if err := tx.Where("id = ? AND company_id = ?",
				*invLine.ShipmentLineID, r.CompanyID).
				First(&shipLine).Error; err != nil {
				return nil, fmt.Errorf("load shipment line %d for ar_return line %d: %w",
					*invLine.ShipmentLineID, line.ID, err)
			}
			qErr = tx.Where(
				"company_id = ? AND source_type = ? AND source_id = ? AND source_line_id = ?",
				r.CompanyID,
				string(models.LedgerSourceShipment),
				shipLine.ShipmentID,
				shipLine.ID,
			).First(&orig).Error
		} else {
			// Legacy fallback — invoice-sourced movement.
			qErr = tx.Where(
				"company_id = ? AND source_type = ? AND source_id = ? AND source_line_id = ?",
				r.CompanyID,
				string(models.LedgerSourceInvoice),
				*r.CreditNote.InvoiceID,
				*line.CreditNoteLine.OriginalInvoiceLineID,
			).First(&orig).Error
		}
		if errors.Is(qErr, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: line id=%d invoice_line_id=%d",
				ErrARReturnReceiptLineOriginalMovementNotFound,
				line.ID, *line.CreditNoteLine.OriginalInvoiceLineID)
		}
		if qErr != nil {
			return nil, fmt.Errorf("load original movement for line %d: %w", line.ID, qErr)
		}

		var originalCost decimal.Decimal
		if orig.UnitCostBase != nil {
			originalCost = *orig.UnitCostBase
		}

		// Book a fresh ReceiveStock at (this return's warehouse, traced
		// cost, return qty). Partial-return safe.
		lineID := line.ID
		result, rErr := inventory.ReceiveStock(tx, inventory.ReceiveStockInput{
			CompanyID:      r.CompanyID,
			ItemID:         line.ProductServiceID,
			WarehouseID:    r.WarehouseID,
			Quantity:       line.Qty,
			MovementDate:   r.ReturnDate,
			UnitCost:       originalCost,
			ExchangeRate:   decimal.NewFromInt(1),
			SourceType:     string(models.LedgerSourceARReturnReceipt),
			SourceID:       r.ID,
			SourceLineID:   &lineID,
			IdempotencyKey: fmt.Sprintf("ar_return_receipt:%d:line:%d:v%d", r.ID, line.ID, version),
			Memo:           "AR return receipt: " + r.ReturnReceiptNumber,
		})
		if rErr != nil {
			return nil, fmt.Errorf("receive returned stock for line %d: %w", line.ID, translateInventoryErr(rErr))
		}

		out = append(out, arReturnReceiptResult{
			Line:            line,
			InventoryValue:  result.InventoryValueBase,
			InventoryAcctID: *line.ProductService.InventoryAccountID,
			COGSAcctID:      *line.ProductService.COGSAccountID,
		})
	}
	return out, nil
}

// ReverseARReturnReceiptMovements reverses every original movement
// for a voided ARReturnReceipt. Thin wrapper over the shared
// reverseDocumentMovements helper — same helper drives
// ReverseReceiptMovements / ReverseShipmentMovements / etc.
func ReverseARReturnReceiptMovements(tx *gorm.DB, companyID uint, r models.ARReturnReceipt) error {
	return reverseDocumentMovements(tx, companyID, reverseDocumentScope{
		sourceType:         string(models.LedgerSourceARReturnReceipt),
		sourceID:           r.ID,
		reversalSourceType: "ar_return_receipt_reversal",
		movementDate:       r.ReturnDate,
		memo:               "Void: " + r.ReturnReceiptNumber,
		reason:             inventory.ReversalReasonCancellation,
	})
}

// buildARReturnReceiptPostingFragments constructs the JE fragments:
// Dr Inventory (per line, amount = InventoryValueBase from
// inventory.ReceiveStock) / Cr COGS (per line, same amount —
// self-balancing).
//
// Each pair is self-balancing, so the total JE is balanced by
// construction. AggregateJournalLines flattens lines sharing the
// same account before the caller runs the double-entry balance
// assertion.
func buildARReturnReceiptPostingFragments(results []arReturnReceiptResult, rrNumber string) []PostingFragment {
	if len(results) == 0 {
		return nil
	}
	frags := make([]PostingFragment, 0, len(results)*2)
	for _, res := range results {
		if !res.InventoryValue.IsPositive() {
			continue
		}
		desc := res.Line.Description
		if desc == "" && res.Line.ProductService != nil {
			desc = res.Line.ProductService.Name
		}
		frags = append(frags,
			PostingFragment{
				AccountID: res.InventoryAcctID,
				Debit:     res.InventoryValue,
				Memo:      "Inventory in (AR return): " + desc,
			},
			PostingFragment{
				AccountID: res.COGSAcctID,
				Credit:    res.InventoryValue,
				Memo:      "COGS reversal (AR return): " + desc,
			},
		)
	}
	return frags
}

// postARReturnReceiptReceiveTruthAndJE runs the shipment_required=true
// branch of PostARReturnReceipt: project lines into receive truth +
// build + post the JE. Returns the created JournalEntry ID (nil when
// the receipt has no stock lines — a pure-service return is legit and
// produces no inventory or JE).
func postARReturnReceiptReceiveTruthAndJE(tx *gorm.DB, companyID uint, r models.ARReturnReceipt) (*uint, error) {
	results, err := CreateARReturnReceiptMovements(tx, r)
	if err != nil {
		return nil, fmt.Errorf("create ar_return_receipt movements: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	frags := buildARReturnReceiptPostingFragments(results, r.ReturnReceiptNumber)
	if len(frags) == 0 {
		return nil, nil
	}
	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return nil, fmt.Errorf("aggregate journal lines: %w", err)
	}
	debitSum := sumPostingDebits(jeLines)
	creditSum := sumPostingCredits(jeLines)
	if !debitSum.Equal(creditSum) {
		return nil, fmt.Errorf(
			"ar_return_receipt JE imbalance: debit %s, credit %s",
			debitSum.StringFixed(2), creditSum.StringFixed(2),
		)
	}

	je := models.JournalEntry{
		CompanyID:  companyID,
		EntryDate:  r.ReturnDate,
		JournalNo:  r.ReturnReceiptNumber,
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourceARReturnReceipt,
		SourceID:   r.ID,
	}
	if err := wrapUniqueViolation(tx.Create(&je).Error, "create ar_return_receipt journal entry"); err != nil {
		return nil, fmt.Errorf("create journal entry: %w", err)
	}

	createdLines := make([]models.JournalLine, 0, len(jeLines))
	for _, jl := range jeLines {
		line := models.JournalLine{
			CompanyID:      companyID,
			JournalEntryID: je.ID,
			AccountID:      jl.AccountID,
			TxDebit:        jl.Debit,
			TxCredit:       jl.Credit,
			Debit:          jl.Debit,
			Credit:         jl.Credit,
			Memo:           jl.Memo,
		}
		// Customer linkage at party level so the return line is
		// traceable back to the customer on reports.
		if r.CustomerID != nil && *r.CustomerID != 0 {
			line.PartyType = models.PartyTypeCustomer
			line.PartyID = *r.CustomerID
		}
		if err := tx.Create(&line).Error; err != nil {
			return nil, fmt.Errorf("create ar_return_receipt journal line: %w", err)
		}
		createdLines = append(createdLines, line)
	}

	if err := ProjectToLedger(tx, companyID, LedgerPostInput{
		JournalEntry: je,
		Lines:        createdLines,
		SourceType:   models.LedgerSourceARReturnReceipt,
		SourceID:     r.ID,
	}); err != nil {
		return nil, fmt.Errorf("project ar_return_receipt to ledger: %w", err)
	}
	return &je.ID, nil
}
