// 遵循project_guide.md
package services

// vendor_return_shipment_posting.go — Phase I slice I.6b.2: the
// business-document-layer orchestration that wires a posted
// VendorReturnShipment to outflow truth via
// `inventory.IssueVendorReturn` (the narrow verb from I.6b.2a) and
// to the Dr AP / Cr Inventory JE.
//
// Three-layer split (H.3 / I.3 / I.6a.2 / I.6b.2 boundary)
// --------------------------------------------------------
//
//   POSTED VendorReturnShipment   (business document — models.VendorReturnShipment)
//         │
//         │  CreateVRSMovements projects each stock line:
//         │    - traces the ORIGINAL Bill movement via the chain
//         │      VRSLine → VendorCreditNoteLine →
//         │      VCNLine.OriginalBillLineID → BillLine →
//         │      inventory_movements (source_type='bill')
//         │    - calls inventory.IssueVendorReturn(OriginalMovementID,
//         │      Qty, WarehouseID) — the narrow verb internally
//         │      reads unit_cost_base and writes outflow at that cost
//         ▼
//   OUTFLOW TRUTH            (inventory_movements row,
//                             source_type='vendor_return_shipment',
//                             movement_type='vendor_return')
//         │
//         │  Internal to the inventory module: balance decrement +
//         │  unchanged average (weighted-avg outbound convention).
//         ▼
//   INVENTORY EFFECT         (inventory_balances — FIFO layer-sum
//                             drift is known-acceptable per
//                             IssueVendorReturn file doc)
//
// Separately, the business-document layer constructs the JE from
// the IssueVendorReturnResult:
//
//   Dr AP         (aggregated, amount = Σ OutflowValueBase)
//   Cr Inventory  (per line, amount = OutflowValueBase)
//
// Self-balancing as a whole. AP account comes from ResolveControlAccount
// (shared with VCN / Bill AP routing). The inventory package has zero
// knowledge of the AP account or the JE — Hard Rule #3 preserved by
// construction.
//
// Why AP + Inventory on ONE document (break from AR symmetry)
// -----------------------------------------------------------
// See file-level comment in vendor_return_shipment_service.go and the
// `LedgerSourceVendorReturnShipment` doc in ledger_entry.go. In short:
// the original Bill only has 2 legs (Dr Inventory / Cr AP), so the
// reversal naturally self-balances on one document. VCN under I.6b.3
// controlled mode will skip JE effect for stock lines, owning only
// non-stock / service / adjustment metadata.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/inventory"
)

// vrsResult pairs a VRSLine with the inventory module's outflow
// result. Used to build JE fragments.
type vrsResult struct {
	Line             models.VendorReturnShipmentLine
	OutflowValueBase decimal.Decimal // positive magnitude
	InventoryAcctID  uint            // per-line, from ProductService
}

// CreateVRSMovements iterates stock-item lines and calls
// `inventory.IssueVendorReturn` for each at the traced Bill cost.
// Non-stock lines skip. Returns the per-line outflow results for JE
// construction.
//
// Preconditions (enforced by PostVendorReturnShipment before call):
//   - r.Lines preloaded with ProductService + VendorCreditNoteLine
//   - r.VendorCreditNote preloaded (for BillID anchor)
//   - Each stock-item line carries VendorCreditNoteLineID
//
// Requires each stock line's VendorCreditNoteLine to carry
// OriginalBillLineID (the IN.6a field) — the cost-trace key.
//
// Warehouse note: VRS.WarehouseID is passed directly to the narrow
// verb; goods leave from wherever the VRS says they leave from,
// regardless of where the original Bill was received.
func CreateVRSMovements(tx *gorm.DB, r models.VendorReturnShipment) ([]vrsResult, error) {
	if len(r.Lines) == 0 {
		return nil, nil
	}
	if r.VendorCreditNote == nil {
		return nil, fmt.Errorf("vendor_return_shipment %d: VendorCreditNote not preloaded", r.ID)
	}
	if r.VendorCreditNote.BillID == nil || *r.VendorCreditNote.BillID == 0 {
		return nil, fmt.Errorf("%w: vcn id=%d",
			ErrVendorReturnShipmentVCNMissingBillAnchor, r.VendorCreditNote.ID)
	}

	out := make([]vrsResult, 0, len(r.Lines))
	for _, line := range r.Lines {
		if line.ProductService == nil {
			return nil, fmt.Errorf("vendor_return_shipment line %d: ProductService not preloaded", line.ID)
		}
		if !line.ProductService.IsStockItem {
			continue
		}
		if line.VendorCreditNoteLine == nil {
			return nil, fmt.Errorf("vendor_return_shipment line %d: VendorCreditNoteLine not preloaded or absent — required at post time per Q7 hard rule #2",
				line.ID)
		}
		if line.VendorCreditNoteLine.OriginalBillLineID == nil || *line.VendorCreditNoteLine.OriginalBillLineID == 0 {
			return nil, fmt.Errorf("%w: line id=%d vcn_line id=%d",
				ErrVendorReturnShipmentLineMissingOriginalBill,
				line.ID, line.VendorCreditNoteLine.ID)
		}
		if line.ProductService.InventoryAccountID == nil || *line.ProductService.InventoryAccountID == 0 {
			return nil, fmt.Errorf("vendor_return_shipment line id=%d: product has no inventory_account_id — configure the product/service",
				line.ID)
		}

		// Locate the original Bill inventory movement (the cost anchor
		// for the narrow verb's trace).
		var orig models.InventoryMovement
		qErr := tx.Where(
			"company_id = ? AND source_type = ? AND source_id = ? AND source_line_id = ?",
			r.CompanyID,
			string(models.LedgerSourceBill),
			*r.VendorCreditNote.BillID,
			*line.VendorCreditNoteLine.OriginalBillLineID,
		).First(&orig).Error
		if errors.Is(qErr, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: line id=%d bill_line_id=%d",
				ErrVendorReturnShipmentLineOriginalMovementNotFound,
				line.ID, *line.VendorCreditNoteLine.OriginalBillLineID)
		}
		if qErr != nil {
			return nil, fmt.Errorf("load original bill movement for line %d: %w", line.ID, qErr)
		}

		// Call the narrow traced-cost outflow verb (Q3). The verb
		// reads unit_cost_base from `orig` internally — we pass only
		// lineage (OriginalMovementID) + intent (Qty, WarehouseID).
		lineID := line.ID
		result, rErr := inventory.IssueVendorReturn(tx, inventory.IssueVendorReturnInput{
			CompanyID:          r.CompanyID,
			OriginalMovementID: orig.ID,
			Quantity:           line.Qty,
			MovementDate:       r.ShipDate,
			WarehouseID:        r.WarehouseID,
			SourceType:         string(models.LedgerSourceVendorReturnShipment),
			SourceID:           r.ID,
			SourceLineID:       &lineID,
			IdempotencyKey:     fmt.Sprintf("vendor_return_shipment:%d:line:%d:v1", r.ID, line.ID),
			Memo:               "Return to vendor: " + r.VendorReturnShipmentNumber,
		})
		if rErr != nil {
			return nil, fmt.Errorf("issue vendor return for line %d: %w", line.ID, translateInventoryErr(rErr))
		}

		out = append(out, vrsResult{
			Line:             line,
			OutflowValueBase: result.OutflowValueBase,
			InventoryAcctID:  *line.ProductService.InventoryAccountID,
		})
	}
	return out, nil
}

// ReverseVRSMovements reverses every original outflow movement for a
// voided VRS. Thin wrapper over the shared `reverseDocumentMovements`
// helper — same helper drives ReverseReceiptMovements /
// ReverseShipmentMovements / ReverseARReturnReceiptMovements.
func ReverseVRSMovements(tx *gorm.DB, companyID uint, r models.VendorReturnShipment) error {
	return reverseDocumentMovements(tx, companyID, reverseDocumentScope{
		sourceType:         string(models.LedgerSourceVendorReturnShipment),
		sourceID:           r.ID,
		reversalSourceType: "vendor_return_shipment_reversal",
		movementDate:       r.ShipDate,
		memo:               "Void: " + r.VendorReturnShipmentNumber,
		reason:             inventory.ReversalReasonCancellation,
	})
}

// buildVRSPostingFragments constructs JE fragments: Cr Inventory
// (per line, amount = OutflowValueBase) / Dr AP (aggregated total).
// Self-balancing.
func buildVRSPostingFragments(results []vrsResult, apAccountID uint, vrsNumber string) ([]PostingFragment, error) {
	if len(results) == 0 {
		return nil, nil
	}
	var frags []PostingFragment
	apDebit := decimal.Zero
	for _, res := range results {
		if !res.OutflowValueBase.IsPositive() {
			continue
		}
		desc := res.Line.Description
		if desc == "" && res.Line.ProductService != nil {
			desc = res.Line.ProductService.Name
		}
		frags = append(frags, PostingFragment{
			AccountID: res.InventoryAcctID,
			Credit:    res.OutflowValueBase,
			Memo:      "Inventory out (return to vendor): " + desc,
		})
		apDebit = apDebit.Add(res.OutflowValueBase)
	}
	if !apDebit.IsPositive() {
		return nil, nil
	}
	frags = append(frags, PostingFragment{
		AccountID: apAccountID,
		Debit:     apDebit,
		Memo:      "AP reduction (return to vendor): " + vrsNumber,
	})
	return frags, nil
}

// postVRSOutflowTruthAndJE runs the receipt_required=true branch of
// PostVendorReturnShipment. Returns the created JournalEntry ID
// (nil when the VRS has no stock lines — pure-service return is
// legit and produces no inventory or JE).
func postVRSOutflowTruthAndJE(tx *gorm.DB, companyID uint, r models.VendorReturnShipment) (*uint, error) {
	results, err := CreateVRSMovements(tx, r)
	if err != nil {
		return nil, fmt.Errorf("create vendor_return_shipment movements: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	// Resolve the AP control account for the JE. Shares routing with
	// VCN / Bill — VendorCredit doc-type.
	apAccount, err := ResolveControlAccount(tx, companyID, 0,
		models.ARAPDocTypeVendorCredit, "", false,
		models.DetailAccountsPayable, ErrNoAPAccount)
	if err != nil {
		return nil, err
	}

	frags, err := buildVRSPostingFragments(results, apAccount.ID, r.VendorReturnShipmentNumber)
	if err != nil {
		return nil, err
	}
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
			"vendor_return_shipment JE imbalance: debit %s, credit %s",
			debitSum.StringFixed(2), creditSum.StringFixed(2),
		)
	}

	je := models.JournalEntry{
		CompanyID:  companyID,
		EntryDate:  r.ShipDate,
		JournalNo:  r.VendorReturnShipmentNumber,
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourceVendorReturnShipment,
		SourceID:   r.ID,
	}
	if err := wrapUniqueViolation(tx.Create(&je).Error, "create vendor_return_shipment journal entry"); err != nil {
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
		if r.VendorID != nil && *r.VendorID != 0 {
			line.PartyType = models.PartyTypeVendor
			line.PartyID = *r.VendorID
		}
		if err := tx.Create(&line).Error; err != nil {
			return nil, fmt.Errorf("create vendor_return_shipment journal line: %w", err)
		}
		createdLines = append(createdLines, line)
	}

	if err := ProjectToLedger(tx, companyID, LedgerPostInput{
		JournalEntry: je,
		Lines:        createdLines,
		SourceType:   models.LedgerSourceVendorReturnShipment,
		SourceID:     r.ID,
	}); err != nil {
		return nil, fmt.Errorf("project vendor_return_shipment to ledger: %w", err)
	}
	return &je.ID, nil
}
