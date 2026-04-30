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
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/inventory"
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

// AdjustBillFragmentsForGRIRClearing is the Phase H slice H.4 Bill-side
// parallel of AdjustBillFragmentsForInventory. Used when
// companies.receipt_required=true: instead of redirecting stock-item
// debit from Expense → Inventory Asset, redirect it to the company's
// GR/IR clearing account. Non-stock items are untouched.
//
// Why: under Phase H Receipt-first semantics, Receipt post has
// already booked Dr Inventory / Cr GR/IR (H.3). Bill is now the
// financial claim only. If Bill also debited Inventory, we would
// double-count inventory asset. Instead Bill posts:
//
//	Dr GR/IR    (offsets Receipt's credit, at Bill line amount)
//	Cr AP       (Bill remains financial claim)
//
// H.4 does this at aggregate level — Bill total debits GR/IR, not
// per-Receipt matching. Any variance between Bill amount and the
// Receipt aggregate sits in the GR/IR balance until H.5 introduces
// receipt-specific matching and PPV.
//
// Shared-account note: this function mirrors AdjustBillFragmentsForInventory's
// pre-aggregation redirect pattern (map of old→new by account ID). If a
// stock line and a non-stock line happen to share the same expense
// account, both fragments get redirected — a pre-existing limitation of
// the legacy adjust function. Standard charts-of-accounts use distinct
// expense accounts for stock vs non-stock items, so this aliasing does
// not arise in practice; it is not introduced by H.4.
func AdjustBillFragmentsForGRIRClearing(frags []PostingFragment, bill models.Bill, grirAccountID uint) []PostingFragment {
	if grirAccountID == 0 {
		return frags
	}
	redirect := map[uint]uint{}
	for _, l := range bill.Lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}
		if l.ExpenseAccountID == nil {
			continue
		}
		redirect[*l.ExpenseAccountID] = grirAccountID
	}
	if len(redirect) == 0 {
		return frags
	}
	for i := range frags {
		if frags[i].Debit.IsPositive() {
			if newID, ok := redirect[frags[i].AccountID]; ok {
				frags[i].AccountID = newID
			}
		}
	}
	return frags
}

// billHasStockLine returns true if any Bill line is a stock item
// (IsStockItem=true on the linked ProductService). Small helper used
// by both the legacy inventory path and the H.4 receipt-required
// branch in PostBill.
func billHasStockLine(bill models.Bill) bool {
	for _, l := range bill.Lines {
		if l.ProductService != nil && l.ProductService.IsStockItem {
			return true
		}
	}
	return false
}

// ── Pre-flight stock validation (invoice) ────────────────────────────────────

// ErrTrackedItemNotSupportedByInvoice — Phase G.2 guard. A tracked
// item appeared on an invoice line (or within a bundle expansion), but
// the invoice flow cannot yet supply the required lot/serial
// selections. Rather than let the preview lie ("feasible=true") and
// the post then blow up in IssueStock, we fail LOUDLY at the preview
// with a remediation-actionable message. Support for tracked sales
// lands via the shipment-driven flow (Phase I).
var ErrTrackedItemNotSupportedByInvoice = errors.New("inventory: tracked items are not yet supported in the invoice flow; use the shipment-driven path when available (Phase I)")

// ValidateStockForInvoice checks that sufficient inventory exists for all
// stock items on the invoice, including bundle component items.
// warehouseID routes the check to a specific warehouse (nil = legacy path
// which sums across all warehouses for the company / item).
// Returns per-item outbound cost results and the expanded bundle components.
//
// Phase D cleanup: now backed by inventory.GetCostingPreview from the
// bounded-context module instead of the legacy CostingEngine. The returned
// OutboundResult is populated with UnitCostUsed only — the other fields
// were never read by downstream callers (BuildCOGSFragments / CreateSale-
// Movements both consume only the unit cost).
//
// Phase G.2: also rejects tracked items with
// ErrTrackedItemNotSupportedByInvoice at preview time so operators see
// a clear reason instead of a raw IssueStock sentinel at post time.
func ValidateStockForInvoice(db *gorm.DB, companyID uint, lines []models.InvoiceLine, warehouseID *uint) (
	outboundCosts map[uint]*OutboundResult,
	bundleExpansions []ExpandedComponent,
	err error,
) {
	outboundCosts = make(map[uint]*OutboundResult)

	// Phase G.2 pre-check: any tracked single-line item is an early
	// hard fail — the invoice layer has no channel to supply
	// LotSelections / SerialSelections.
	for _, l := range lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}
		if l.ProductService.TrackingMode != "" && l.ProductService.TrackingMode != models.TrackingNone {
			return nil, nil, fmt.Errorf("%w: line item %q (tracking_mode=%q)",
				ErrTrackedItemNotSupportedByInvoice, l.ProductService.Name, l.ProductService.TrackingMode)
		}
	}

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

	// Phase G.2: the same guard extended into bundle-expanded
	// components. A bundle whose inner item is tracked cannot be sold
	// via invoice today either — reject here with the same sentinel so
	// the caller surfaces the real blocker.
	for _, ec := range bundleExpansions {
		if ec.ComponentItem == nil {
			continue
		}
		if ec.ComponentItem.TrackingMode != "" && ec.ComponentItem.TrackingMode != models.TrackingNone {
			return nil, nil, fmt.Errorf("%w: bundle component %q (tracking_mode=%q)",
				ErrTrackedItemNotSupportedByInvoice, ec.ComponentItem.Name, ec.ComponentItem.TrackingMode)
		}
	}

	for _, ec := range bundleExpansions {
		required[ec.ComponentItem.ID] = required[ec.ComponentItem.ID].Add(ec.RequiredQty)
	}

	// Validate stock availability for all required items via the inventory
	// module's read-only preview. WarehouseID=0 means "aggregate across
	// warehouses" for the inventory module, mirroring the legacy nil path.
	whID := uint(0)
	if warehouseID != nil {
		whID = *warehouseID
	}
	for itemID, needQty := range required {
		preview, err := inventory.GetCostingPreview(db, inventory.CostingPreviewQuery{
			CompanyID:   companyID,
			ItemID:      itemID,
			WarehouseID: whID,
			Quantity:    needQty,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("costing preview for item %d: %w", itemID, err)
		}
		if !preview.Feasible {
			itemName := lookupStockItemLabel(itemID, lines, bundleExpansions)
			return nil, nil, fmt.Errorf(
				"insufficient inventory for %q: short by %s units",
				itemName, preview.ShortBy.String())
		}
		outboundCosts[itemID] = &OutboundResult{
			UnitCostUsed: preview.UnitCostBase,
			TotalCost:    preview.TotalCostBase,
		}
	}

	return outboundCosts, bundleExpansions, nil
}

// lookupStockItemLabel finds the display name for an item by scanning the
// invoice lines and bundle expansions. Returns "#<id>" if no label is found
// (defensive fallback that should not happen in practice).
func lookupStockItemLabel(itemID uint, lines []models.InvoiceLine, bundleExpansions []ExpandedComponent) string {
	for _, l := range lines {
		if l.ProductService != nil && l.ProductService.ID == itemID {
			return l.ProductService.Name
		}
	}
	for _, ec := range bundleExpansions {
		if ec.ComponentItem != nil && ec.ComponentItem.ID == itemID {
			return ec.ComponentItem.Name + " (bundle component)"
		}
	}
	return fmt.Sprintf("#%d", itemID)
}

// ── Transactional movement creators ──────────────────────────────────────────

// CreateSaleMovements records inventory outflows for stock items on a posted
// invoice. Handles both single stock items and bundle component items.
// warehouseID routes movements to a specific warehouse (nil = legacy path).
// Must be called inside the same transaction as the JE creation.
//
// Returns the authoritative unit cost per item_id. This is the keystone of
// the E0.2 hardening: the returned map is what PostInvoice uses to build
// COGS journal entries — the JE amount and the inventory movement's
// unit_cost_base are guaranteed to agree because they come from the same
// IssueStock call, not from two independent reads.
//
// The returned map keys are item IDs (not line IDs) and the OutboundResult
// is populated only with UnitCostUsed / TotalCost (the fields
// BuildCOGSFragments reads). If the same item appears on multiple lines or
// via bundle expansion, the last-written cost wins — but all IssueStock
// calls on the same item within one transaction see the same row-locked
// balance, so they all compute the same unit cost.
func CreateSaleMovements(tx *gorm.DB, companyID uint, inv models.Invoice,
	bundleExpansions []ExpandedComponent, warehouseID *uint) (map[uint]*OutboundResult, error) {

	costs := map[uint]*OutboundResult{}

	// Skip entirely when no stock is actually moving — avoids an
	// inventory_movements scan for service-only invoices whose test
	// fixtures may not even migrate that table.
	if !hasInvoiceStockActivity(inv, bundleExpansions) {
		return costs, nil
	}

	// Pick a fresh idempotency-key version for this post attempt so a
	// voided-and-re-posted invoice does not collide with its prior keys.
	version, err := nextIdempotencyVersion(tx, companyID, "invoice", inv.ID)
	if err != nil {
		return nil, fmt.Errorf("pick idempotency version: %w", err)
	}

	// 1. Single stock items.
	for _, l := range inv.Lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}
		// UOM-aware (Phase U3 — 2026-04-25). Use the snapshotted
		// QtyInStockUOM so the inventory module never has to convert.
		// Legacy invoice lines (pre-U2) carry QtyInStockUOM=0 and we
		// fall back to the raw Qty (which equals StockUOM 1:1 for
		// pre-UOM data — every existing line had factor=1 implicitly).
		stockQty := l.QtyInStockUOM
		if !stockQty.IsPositive() {
			stockQty = l.Qty
		}
		result, err := issueSaleLine(tx, companyID, inv, l.ProductService.ID, stockQty, warehouseID, l.ID, false, version)
		if err != nil {
			return nil, err
		}
		costs[l.ProductService.ID] = &OutboundResult{
			UnitCostUsed: result.UnitCostBase,
			TotalCost:    result.CostOfIssueBase,
		}
	}

	// 2. Bundle component items.
	for _, ec := range bundleExpansions {
		// Bundle expansions are derived, not linked to a specific invoice
		// line row — SourceLineID stays nil for them.
		result, err := issueSaleLine(tx, companyID, inv, ec.ComponentItem.ID, ec.RequiredQty, warehouseID, 0, true, version)
		if err != nil {
			return nil, err
		}
		costs[ec.ComponentItem.ID] = &OutboundResult{
			UnitCostUsed: result.UnitCostBase,
			TotalCost:    result.CostOfIssueBase,
		}
	}

	return costs, nil
}

// hasInvoiceStockActivity reports whether this invoice or its bundle
// expansions will trigger any stock movements at all. Used to skip the
// idempotency-version lookup (and its table scan) when nothing moves.
func hasInvoiceStockActivity(inv models.Invoice, bundleExpansions []ExpandedComponent) bool {
	for _, l := range inv.Lines {
		if l.ProductService != nil && l.ProductService.IsStockItem {
			return true
		}
	}
	for _, ec := range bundleExpansions {
		if ec.ComponentItem != nil {
			return true
		}
	}
	return false
}

// issueSaleLine delegates a single outbound to inventory.IssueStock and
// returns the result so the caller can capture the authoritative unit cost
// for COGS posting. The isBundle flag is used only to derive a distinct
// idempotency key per bundle component vs standalone line item — the same
// item ID can appear both directly on a line and via a bundle, so each
// needs its own key. version is the shared ":v<n>" suffix picked once per
// post attempt.
func issueSaleLine(tx *gorm.DB, companyID uint, inv models.Invoice, itemID uint,
	qty decimal.Decimal, warehouseID *uint, invoiceLineID uint, isBundle bool, version int) (*inventory.IssueStockResult, error) {

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
		in.IdempotencyKey = fmt.Sprintf("invoice:%d:bundle:item:%d:v%d", inv.ID, itemID, version)
	} else {
		in.IdempotencyKey = fmt.Sprintf("invoice:%d:line:%d:v%d", inv.ID, invoiceLineID, version)
	}

	result, err := inventory.IssueStock(tx, in)
	if err != nil {
		translated := translateInventoryErr(err)
		// Friendlier insufficient-stock error: enrich with the
		// product name and on-hand qty the operator needs to see to
		// act. Keeps the wrapped sentinel so `errors.Is(_, ErrInsufficientStock)`
		// continues to match for callers that care.
		if errors.Is(translated, ErrInsufficientStock) {
			return nil, wrapInsufficientStockErr(tx, companyID, itemID, warehouseID, qty)
		}
		return nil, fmt.Errorf("issue stock for item %d: %w", itemID, translated)
	}
	return result, nil
}

// wrapInsufficientStockErr loads the product name + on-hand qty for
// the (item, warehouse) pair and returns an ErrInsufficientStock-
// wrapped error whose message points operators at the specific
// item they need to replenish before the invoice can post.
// Best-effort on the lookups — missing product name / balance row
// falls back to safe defaults so the core "not enough stock"
// signal still surfaces.
func wrapInsufficientStockErr(tx *gorm.DB, companyID, itemID uint, warehouseID *uint, requestedQty decimal.Decimal) error {
	name := fmt.Sprintf("item #%d", itemID)
	var ps models.ProductService
	if err := tx.Select("name").
		Where("id = ? AND company_id = ?", itemID, companyID).
		First(&ps).Error; err == nil && ps.Name != "" {
		name = ps.Name
	}

	onHand := decimal.Zero
	q := tx.Model(&models.InventoryBalance{}).
		Select("quantity_on_hand").
		Where("company_id = ? AND item_id = ?", companyID, itemID)
	if warehouseID != nil && *warehouseID != 0 {
		q = q.Where("warehouse_id = ?", *warehouseID)
	} else {
		q = q.Where("warehouse_id IS NULL")
	}
	var bal models.InventoryBalance
	if err := q.First(&bal).Error; err == nil {
		onHand = bal.QuantityOnHand
	}

	return fmt.Errorf("%w: %q only has %s on hand — not enough stock to fulfill requested %s",
		ErrInsufficientStock, name, onHand.String(), requestedQty.String())
}

// CreatePurchaseMovements books inventory receipts for each stock-item line
// on a bill. Bundle items on bills are not expanded (bundles are sales-only).
// warehouseID routes movements to a specific warehouse (nil = legacy path).
// Must be called inside the same transaction as the JE creation.
//
// Phase D cleanup: pure facade over inventory.ReceiveStock. GL linkage
// resolves via source_type + source_id -> bill -> bill.journal_entry_id.
func CreatePurchaseMovements(tx *gorm.DB, companyID uint, bill models.Bill, warehouseID *uint) error {
	warehouseValue := uint(0)
	if warehouseID != nil {
		warehouseValue = *warehouseID
	}

	// Skip entirely when no bill line is a stock item — spares non-inventory
	// bills the inventory_movements scan below (and the test fixtures that
	// don't bother migrating that table).
	hasStock := false
	for _, l := range bill.Lines {
		if l.ProductService != nil && l.ProductService.IsStockItem {
			hasStock = true
			break
		}
	}
	if !hasStock {
		return nil
	}

	// Pick a fresh idempotency-key version for this post attempt so a
	// voided-and-re-posted bill does not collide with its prior keys.
	version, err := nextIdempotencyVersion(tx, companyID, "bill", bill.ID)
	if err != nil {
		return fmt.Errorf("pick idempotency version: %w", err)
	}

	movementCurrency := normalizeCurrencyCode(bill.CurrencyCode)
	movementRate := bill.ExchangeRate
	if movementRate.IsZero() {
		movementRate = decimal.NewFromInt(1)
	}

	for _, l := range bill.Lines {
		if l.ProductService == nil || !l.ProductService.IsStockItem {
			continue
		}

		lineID := l.ID
		// UOM-aware (Phase U3 — 2026-04-25). Inventory module counts in
		// ProductService.StockUOM; the line's QtyInStockUOM was computed
		// at save time (Qty × LineUOMFactor). UnitCost similarly converts
		// from per-LineUOM to per-StockUOM. Legacy lines (pre-U2) carry
		// LineUOMFactor=1 so the math degrades to the original Qty/UnitPrice.
		stockQty := l.QtyInStockUOM
		if !stockQty.IsPositive() {
			stockQty = l.Qty // legacy fallback
		}
		stockUnitCost := l.UnitPrice
		if l.LineUOMFactor.IsPositive() && !l.LineUOMFactor.Equal(decimal.NewFromInt(1)) {
			stockUnitCost = l.UnitPrice.Div(l.LineUOMFactor).Round(4)
		}
		in := inventory.ReceiveStockInput{
			CompanyID:      companyID,
			ItemID:         l.ProductService.ID,
			WarehouseID:    warehouseValue,
			Quantity:       stockQty,
			MovementDate:   bill.BillDate,
			UnitCost:       stockUnitCost,
			UoMCode:        l.LineUOM,
			UoMFactor:      l.LineUOMFactor,
			CurrencyCode:   movementCurrency,
			ExchangeRate:   movementRate,
			SourceType:     "bill",
			SourceID:       bill.ID,
			SourceLineID:   &lineID,
			IdempotencyKey: fmt.Sprintf("bill:%d:line:%d:v%d", bill.ID, l.ID, version),
			Memo:           "Purchase: " + bill.BillNumber,
		}
		// Phase G.4: forward lot-tracking receipt data so lot-tracked
		// items persist correctly into inventory_lots. Serial-tracked
		// items have no capture surface on BillLine today and will
		// continue to fail loudly at inventory.validateInboundTracking
		// (ErrTrackingDataMissing) — that guard is intended.
		if l.LotNumber != "" {
			in.LotNumber = l.LotNumber
		}
		if l.LotExpiryDate != nil {
			in.ExpiryDate = l.LotExpiryDate
		}
		if _, err := inventory.ReceiveStock(tx, in); err != nil {
			return fmt.Errorf("receive stock for item %d: %w", l.ProductService.ID, translateInventoryErr(err))
		}
	}
	return nil
}
