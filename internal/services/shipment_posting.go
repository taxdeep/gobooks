// 遵循project_guide.md
package services

// shipment_posting.go — Phase I slice I.3: business-document-layer
// orchestration that wires a posted Shipment to issue truth (via
// inventory.IssueStock) and to the Dr COGS / Cr Inventory journal,
// plus the waiting_for_invoice operational queue creation.
//
// Three-layer split (I.3 boundary lock)
// -------------------------------------
//
//   POSTED SHIPMENT        (business document — models.Shipment)
//         │
//         │  CreateShipmentMovements projects each stock line into
//         │  an IssueStockInput. The inventory module owns the
//         │  OUT verb (IssueStock), which returns an issue-truth
//         │  record ID and — critically — the AUTHORITATIVE
//         │  unit_cost_base (FIFO peel or moving-average), which
//         │  is the only cost figure allowed to drive the JE.
//         ▼
//   ISSUE TRUTH            (inventory_movements row, source_type='shipment')
//         │
//         │  Internal to the inventory module: cost-layer retire /
//         │  balance decrement. The business-document layer never
//         │  mutates inventory_balances or cost layers directly.
//         ▼
//   INVENTORY EFFECT       (inventory_balances, inventory_cost_layers,
//                           inventory_lots, inventory_serial_units)
//
// Separately, the business-document layer reads IssueStockResult and
// constructs a journal: Dr COGS (per line, amount =
// CostOfIssueBase returned by inventory) / Cr Inventory (per line,
// same amount). No aggregation account is needed on the sell side
// — there is no GR/IR analog in Phase I.B because revenue and cost
// are independent (no variance surface to clear).
//
// Authoritative-cost principle (INVENTORY_MODULE_API.md §2)
// ---------------------------------------------------------
// Outbound cost is determined by the inventory module at issue time.
// The business-document layer does not declare a cost on
// ShipmentLine (ShipmentLine has no UnitCost column by design). The
// JE amount is the `CostOfIssueBase` returned by inventory, not a
// preview, not a recomputation. This is the sell-side mirror of H.3's
// "InventoryValueBase returned by ReceiveStock" contract.

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services/inventory"
)

var (
	// ErrShipmentInventoryAccountMissing — a stock-item line on the
	// shipment has no inventory_account_id configured on its
	// ProductService. The credit side of the COGS journal cannot be
	// booked without it, so post fails early with a clear pointer at
	// the product-service catalog as the remediation site.
	ErrShipmentInventoryAccountMissing = errors.New("shipment: stock-item line has no inventory_account_id — configure the product/service")

	// ErrShipmentCOGSAccountMissing — a stock-item line on the
	// shipment has no cogs_account_id configured on its ProductService.
	// The debit side of the COGS journal cannot be booked without it,
	// so post fails early. Same remediation surface as
	// ErrShipmentInventoryAccountMissing.
	ErrShipmentCOGSAccountMissing = errors.New("shipment: stock-item line has no cogs_account_id — configure the product/service")
)

// issueTruthResult pairs a single line's inventory return with its
// source line — enough for the JE construction and WFI creation
// steps that follow.
type issueTruthResult struct {
	Line   models.ShipmentLine
	Result inventory.IssueStockResult
}

// CreateShipmentMovements is the shipment-side facade over
// inventory.IssueStock. Sell-side mirror of CreateReceiptMovements
// / CreatePurchaseMovements from H.3.
//
// Iterates each stock-item line on the shipment and books one
// inventory.IssueStock call. Non-stock lines are skipped. Returns
// the per-line (ShipmentLine, IssueStockResult) pairs so the
// caller can aggregate cost for the JE and create waiting_for_invoice
// items in base currency.
//
// The function is GL-agnostic: no journal entry, no fragments, no
// account IDs touched. GL construction is the next step in the
// pipeline, owned by PostShipment.
//
// Tracked items: lot/serial selections are NOT carried on
// ShipmentLine in I.3 — the inventory module's validateOutboundTracking
// guard will fail loud (ErrTrackingDataMissing) for any item whose
// tracking_mode is 'lot' or 'serial'. That is intentional for this
// slice: the selection payload shape lands with its own slice when a
// real admin / UI surface is built for it.
func CreateShipmentMovements(tx *gorm.DB, shipment models.Shipment) ([]issueTruthResult, error) {
	if len(shipment.Lines) == 0 {
		return nil, nil
	}

	version, err := nextIdempotencyVersion(tx, shipment.CompanyID, "shipment", shipment.ID)
	if err != nil {
		return nil, fmt.Errorf("pick idempotency version: %w", err)
	}

	out := make([]issueTruthResult, 0, len(shipment.Lines))
	for _, line := range shipment.Lines {
		if line.ProductService == nil {
			return nil, fmt.Errorf("shipment line %d: ProductService not preloaded", line.ID)
		}
		if !line.ProductService.IsStockItem {
			continue
		}
		if !line.Qty.IsPositive() {
			// Zero-qty lines produce no cost, no movement, no WFI row.
			continue
		}

		lineID := line.ID
		in := inventory.IssueStockInput{
			CompanyID:      shipment.CompanyID,
			ItemID:         line.ProductServiceID,
			WarehouseID:    shipment.WarehouseID,
			Quantity:       line.Qty,
			MovementDate:   shipment.ShipDate,
			SourceType:     string(models.LedgerSourceShipment),
			SourceID:       shipment.ID,
			SourceLineID:   &lineID,
			IdempotencyKey: fmt.Sprintf("shipment:%d:line:%d:v%d", shipment.ID, line.ID, version),
			Memo:           "Shipment: " + shipment.ShipmentNumber,
		}

		result, err := inventory.IssueStock(tx, in)
		if err != nil {
			return nil, fmt.Errorf("issue stock for item %d: %w", line.ProductServiceID, translateInventoryErr(err))
		}
		out = append(out, issueTruthResult{Line: line, Result: *result})
	}
	return out, nil
}

// ReverseShipmentMovements reverses every original shipment movement
// for a voided shipment. Thin wrapper around the shared
// reverseDocumentMovements helper (same helper drives
// ReverseSaleMovements / ReversePurchaseMovements /
// ReverseReceiptMovements).
func ReverseShipmentMovements(tx *gorm.DB, companyID uint, shipment models.Shipment) error {
	return reverseDocumentMovements(tx, companyID, reverseDocumentScope{
		sourceType:         string(models.LedgerSourceShipment),
		sourceID:           shipment.ID,
		reversalSourceType: "shipment_reversal",
		movementDate:       shipment.ShipDate,
		memo:               "Void: " + shipment.ShipmentNumber,
		reason:             inventory.ReversalReasonCancellation,
	})
}

// buildShipmentPostingFragments constructs the JE fragments for a
// posted shipment: Dr COGS (per line, amount = CostOfIssueBase
// returned by inventory.IssueStock) / Cr Inventory (per line, same
// amount). Paired per line — no aggregation account like GR/IR,
// because Phase I.B has no variance surface between cost and revenue
// to clear against.
//
// The inventory module returned authoritative base-currency values.
// The business-document layer takes those as-is — it does NOT
// recompute from unit_cost × qty, mirroring the H.3 principle and
// Phase E0's "COGS from IssueStock return, not preview".
func buildShipmentPostingFragments(results []issueTruthResult) ([]PostingFragment, error) {
	var frags []PostingFragment
	for _, r := range results {
		if r.Line.ProductService == nil {
			return nil, fmt.Errorf("shipment line %d: ProductService not preloaded", r.Line.ID)
		}
		if r.Line.ProductService.COGSAccountID == nil {
			return nil, fmt.Errorf("%w: line=%d item=%d",
				ErrShipmentCOGSAccountMissing, r.Line.ID, r.Line.ProductServiceID)
		}
		if r.Line.ProductService.InventoryAccountID == nil {
			return nil, fmt.Errorf("%w: line=%d item=%d",
				ErrShipmentInventoryAccountMissing, r.Line.ID, r.Line.ProductServiceID)
		}
		amount := r.Result.CostOfIssueBase
		if !amount.IsPositive() {
			// Zero-cost lines (qty=0 or unit_cost=0 from layer peel)
			// contribute nothing to either side — skip silently.
			continue
		}
		desc := r.Line.Description
		if desc == "" {
			desc = r.Line.ProductService.Name
		}
		frags = append(frags,
			PostingFragment{
				AccountID: *r.Line.ProductService.COGSAccountID,
				Debit:     amount,
				Memo:      "COGS (shipment): " + desc,
			},
			PostingFragment{
				AccountID: *r.Line.ProductService.InventoryAccountID,
				Credit:    amount,
				Memo:      "Inventory out (shipment): " + desc,
			},
		)
	}
	return frags, nil
}
