// 遵循project_guide.md
package services

// inventory_posting.go — Inventory integration with the posting engine.
//
// All costing logic is delegated to the CostingEngine interface.
// Bundle lines are expanded into component-level stock operations.
//
// This file provides:
//   - Fragment builders for COGS (invoice) and inventory receipt (bill)
//   - Pre-flight stock validation (invoice) with bundle expansion
//   - Transactional movement creators that call CostingEngine

import (
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services/inventory"
)

// ── COGS fragment builder (invoice sale) ─────────────────────────────────────

// BuildCOGSFragments generates Dr COGS / Cr Inventory Asset fragments for
// inventory items on an invoice. Handles both single stock items and bundle
// component items. outboundCosts maps component_item_id → OutboundResult.
//
// Bundle lines: COGS is generated for each component item, not the bundle itself.
// Single stock lines: COGS is generated for the line item directly.
func BuildCOGSFragments(lines []models.InvoiceLine, outboundCosts map[uint]*OutboundResult, bundleExpansions []ExpandedComponent) []PostingFragment {
	var frags []PostingFragment

	// 1. Single stock items (non-bundle).
	for _, l := range lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}
		if l.ProductService.COGSAccountID == nil || l.ProductService.InventoryAccountID == nil {
			continue
		}
		result, ok := outboundCosts[l.ProductService.ID]
		if !ok || result == nil {
			continue
		}
		cogsAmount := l.Qty.Mul(result.UnitCostUsed).RoundBank(2)
		if cogsAmount.IsZero() {
			continue
		}
		frags = append(frags,
			PostingFragment{AccountID: *l.ProductService.COGSAccountID, Debit: cogsAmount, Credit: decimal.Zero, Memo: "COGS: " + l.Description},
			PostingFragment{AccountID: *l.ProductService.InventoryAccountID, Debit: decimal.Zero, Credit: cogsAmount, Memo: "Inventory out: " + l.Description},
		)
	}

	// 2. Bundle component items.
	for _, ec := range bundleExpansions {
		if ec.ComponentItem == nil || ec.ComponentItem.COGSAccountID == nil || ec.ComponentItem.InventoryAccountID == nil {
			continue
		}
		result, ok := outboundCosts[ec.ComponentItem.ID]
		if !ok || result == nil {
			continue
		}
		cogsAmount := ec.RequiredQty.Mul(result.UnitCostUsed).RoundBank(2)
		if cogsAmount.IsZero() {
			continue
		}
		frags = append(frags,
			PostingFragment{AccountID: *ec.ComponentItem.COGSAccountID, Debit: cogsAmount, Credit: decimal.Zero, Memo: "COGS (bundle): " + ec.ComponentItem.Name},
			PostingFragment{AccountID: *ec.ComponentItem.InventoryAccountID, Debit: decimal.Zero, Credit: cogsAmount, Memo: "Inventory out (bundle): " + ec.ComponentItem.Name},
		)
	}

	return frags
}

// ── Bill inventory fragment adjustment ───────────────────────────────────────

// AdjustBillFragmentsForInventory modifies bill posting fragments so that
// inventory items debit the Inventory Asset account instead of the Expense account.
// Non-inventory items are left unchanged. Bundle items on bills are not supported.
func AdjustBillFragmentsForInventory(frags []PostingFragment, bill models.Bill) []PostingFragment {
	invAcctMap := map[uint]uint{}
	for _, l := range bill.Lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}
		if l.ExpenseAccountID == nil || l.ProductService.InventoryAccountID == nil {
			continue
		}
		invAcctMap[*l.ExpenseAccountID] = *l.ProductService.InventoryAccountID
	}
	if len(invAcctMap) == 0 {
		return frags
	}
	for i := range frags {
		if frags[i].Debit.IsPositive() {
			if invAcctID, ok := invAcctMap[frags[i].AccountID]; ok {
				frags[i].AccountID = invAcctID
			}
		}
	}
	return frags
}

// ── Pre-flight stock validation (invoice) ────────────────────────────────────

// ValidateStockForInvoice checks that sufficient inventory exists for all
// stock items on the invoice, including bundle component items.
// warehouseID routes the check to a specific warehouse (nil = legacy path).
// Returns per-item outbound cost results and the expanded bundle components.
func ValidateStockForInvoice(db *gorm.DB, companyID uint, lines []models.InvoiceLine, warehouseID *uint) (
	outboundCosts map[uint]*OutboundResult,
	bundleExpansions []ExpandedComponent,
	err error,
) {
	engine, err := ResolveCostingEngineForCompany(db, companyID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve costing engine: %w", err)
	}

	outboundCosts = make(map[uint]*OutboundResult)

	// Aggregate required quantities per item from single stock lines.
	required := map[uint]decimal.Decimal{}
	for _, l := range lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}
		required[l.ProductService.ID] = required[l.ProductService.ID].Add(l.Qty)
	}

	// Expand bundle lines and add component requirements.
	bundleExpansions, err = ExpandBundleLinesForInvoice(db, companyID, lines)
	if err != nil {
		return nil, nil, fmt.Errorf("expand bundle lines: %w", err)
	}
	for _, ec := range bundleExpansions {
		required[ec.ComponentItem.ID] = required[ec.ComponentItem.ID].Add(ec.RequiredQty)
	}

	// Validate stock availability for all required items.
	for itemID, needQty := range required {
		req := OutboundRequest{
			CompanyID:    companyID,
			ItemID:       itemID,
			Quantity:     needQty,
			MovementType: models.MovementTypeSale,
			WarehouseID:  warehouseID,
		}
		if warehouseID == nil {
			req.LocationType = models.LocationTypeInternal
		}
		result, err := engine.PreviewOutbound(db, req)
		if err != nil {
			itemName := fmt.Sprintf("#%d", itemID)
			for _, l := range lines {
				if l.ProductService != nil && l.ProductService.ID == itemID {
					itemName = l.ProductService.Name
					break
				}
			}
			// Also check bundle components for name.
			for _, ec := range bundleExpansions {
				if ec.ComponentItem != nil && ec.ComponentItem.ID == itemID {
					itemName = ec.ComponentItem.Name + " (bundle component)"
					break
				}
			}
			return nil, nil, fmt.Errorf("insufficient inventory for %q: %w", itemName, err)
		}
		outboundCosts[itemID] = result
	}

	return outboundCosts, bundleExpansions, nil
}

// ── Transactional movement creators ──────────────────────────────────────────

// CreateSaleMovements records inventory outflows for stock items on a posted
// invoice. Handles both single stock items and bundle component items.
// warehouseID routes movements to a specific warehouse (nil = legacy path).
// Must be called inside the same transaction as the JE creation.
//
// Phase D.0 slice 8: pure facade over inventory.IssueStock; the legacy
// journal_entry_id follow-up UPDATE is gone alongside the column. The
// jeID parameter is retained in the signature but unused; callers that
// need GL linkage resolve it via invoice.journal_entry_id.
func CreateSaleMovements(tx *gorm.DB, companyID uint, inv models.Invoice, jeID uint,
	outboundCosts map[uint]*OutboundResult, bundleExpansions []ExpandedComponent, warehouseID *uint) error {

	_ = jeID          // see function comment
	_ = outboundCosts // passed through to BuildCOGSFragments upstream; facade itself no longer reads it

	// 1. Single stock items.
	for _, l := range inv.Lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}
		if err := issueSaleLine(tx, companyID, inv, l.ProductService.ID, l.Qty, warehouseID, l.ID, false); err != nil {
			return err
		}
	}

	// 2. Bundle component items.
	for _, ec := range bundleExpansions {
		// Bundle expansions are derived, not linked to a specific invoice
		// line row — SourceLineID stays nil for them.
		if err := issueSaleLine(tx, companyID, inv, ec.ComponentItem.ID, ec.RequiredQty, warehouseID, 0, true); err != nil {
			return err
		}
	}

	return nil
}

// issueSaleLine delegates a single outbound to inventory.IssueStock.
// The isBundle flag is used only to derive a distinct idempotency key per
// bundle component vs standalone line item — the same item ID can appear
// both directly on a line and via a bundle, so each needs its own key.
func issueSaleLine(tx *gorm.DB, companyID uint, inv models.Invoice, itemID uint,
	qty decimal.Decimal, warehouseID *uint, invoiceLineID uint, isBundle bool) error {

	warehouseValue := uint(0)
	if warehouseID != nil {
		warehouseValue = *warehouseID
	}

	in := inventory.IssueStockInput{
		CompanyID:    companyID,
		ItemID:       itemID,
		WarehouseID:  warehouseValue,
		Quantity:     qty,
		MovementDate: inv.InvoiceDate,
		SourceType:   "invoice",
		SourceID:     inv.ID,
		Memo:         "Sale: " + inv.InvoiceNumber,
	}
	if invoiceLineID != 0 {
		lineID := invoiceLineID
		in.SourceLineID = &lineID
	}
	if isBundle {
		in.IdempotencyKey = fmt.Sprintf("invoice:%d:bundle:item:%d:v1", inv.ID, itemID)
	} else {
		in.IdempotencyKey = fmt.Sprintf("invoice:%d:line:%d:v1", inv.ID, invoiceLineID)
	}

	if _, err := inventory.IssueStock(tx, in); err != nil {
		return fmt.Errorf("issue stock for item %d: %w", itemID, translateInventoryErr(err))
	}
	return nil
}

// CreatePurchaseMovements records inventory inflows for stock items on a posted
// bill. Bundle items on bills are not expanded (bundles are sales-only).
// warehouseID routes movements to a specific warehouse (nil = legacy path).
// Must be called inside the same transaction as the JE creation.
// CreatePurchaseMovements books inventory receipts for each stock-item line
// on a bill. Phase D.0 slice 8: pure facade — the legacy journal_entry_id
// follow-up UPDATE was retired alongside the column itself.
//
// The jeID argument is retained in the signature for backward compatibility
// with existing callers, but is now deliberately unused: GL linkage
// resolves via source_type + source_id -> bill -> bill.journal_entry_id.
func CreatePurchaseMovements(tx *gorm.DB, companyID uint, bill models.Bill, jeID uint, warehouseID *uint) error {
	_ = jeID // intentionally unused; see function comment
	warehouseValue := uint(0)
	if warehouseID != nil {
		warehouseValue = *warehouseID
	}

	for _, l := range bill.Lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}

		lineID := l.ID
		in := inventory.ReceiveStockInput{
			CompanyID:    companyID,
			ItemID:       l.ProductService.ID,
			WarehouseID:  warehouseValue,
			Quantity:     l.Qty,
			MovementDate: bill.BillDate,
			UnitCost:     l.UnitPrice,
			ExchangeRate: decimal.NewFromInt(1),
			SourceType:   "bill",
			SourceID:     bill.ID,
			SourceLineID: &lineID,
			IdempotencyKey: fmt.Sprintf("bill:%d:line:%d:v1", bill.ID, l.ID),
			Memo:           "Purchase: " + bill.BillNumber,
		}
		if _, err := inventory.ReceiveStock(tx, in); err != nil {
			return fmt.Errorf("receive stock for item %d: %w", l.ProductService.ID, translateInventoryErr(err))
		}
	}
	return nil
}
