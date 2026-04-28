// 遵循project_guide.md
package inventory

// queries.go — OUT contract (read-only queries). Phase D.0 slice 7.
//
// These functions are the public read side of the inventory bounded
// context. They never write; every mutation goes through the IN verbs
// in receive.go / issue.go / adjust.go / transfer.go / reverse.go.
//
// Phase D.0 implements the five queries that operate on data already
// captured by slices 1-6 (OnHand, Movements, ItemLedger, Valuation,
// CostingPreview). ExplodeBOM and GetAvailableForBuild are declared
// here for API stability but return an explicit "not yet implemented"
// error until Phase D.1 adds the product_components table.

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ErrNotImplemented surfaces the BOM-dependent OUT queries before
// Phase D.1 wires the product_components table in.
var ErrNotImplemented = errors.New("inventory: query not implemented until Phase D.1 BOM work")

// ── GetOnHand ────────────────────────────────────────────────────────────────

// GetOnHand returns one row per (item, warehouse) matching the query. When
// AsOfDate is nil the cached InventoryBalance rows are returned verbatim
// — the common case and the fastest. When AsOfDate is set the historical
// quantity is reconstructed from the movement ledger (expensive; avoid in
// hot paths) with the caveat that AverageCost is not re-derived and stays
// blank on historical rows. Full historical valuation is a Phase E task.
func GetOnHand(db *gorm.DB, q OnHandQuery) ([]OnHandRow, error) {
	if q.CompanyID == 0 {
		return nil, fmt.Errorf("inventory.GetOnHand: CompanyID required")
	}

	if q.AsOfDate != nil {
		return getOnHandHistorical(db, q)
	}

	dbq := db.Model(&models.InventoryBalance{}).Where("company_id = ?", q.CompanyID)
	if q.ItemID != 0 {
		dbq = dbq.Where("item_id = ?", q.ItemID)
	}
	if q.WarehouseID != 0 {
		dbq = dbq.Where("warehouse_id = ?", q.WarehouseID)
	}
	var balances []models.InventoryBalance
	if err := dbq.Find(&balances).Error; err != nil {
		return nil, fmt.Errorf("inventory.GetOnHand: %w", err)
	}

	rows := make([]OnHandRow, 0, len(balances))
	for _, bal := range balances {
		if !q.IncludeZero && bal.QuantityOnHand.IsZero() && bal.QuantityReserved.IsZero() {
			continue
		}
		whID := uint(0)
		if bal.WarehouseID != nil {
			whID = *bal.WarehouseID
		}
		// QuantityReserved now surfaces directly from the balance row
		// (Phase E1, migration 058). Available = OnHand − Reserved.
		rows = append(rows, OnHandRow{
			ItemID:            bal.ItemID,
			WarehouseID:       whID,
			QuantityOnHand:    bal.QuantityOnHand,
			QuantityReserved:  bal.QuantityReserved,
			QuantityAvailable: bal.QuantityOnHand.Sub(bal.QuantityReserved),
			AverageCostBase:   bal.AverageCost,
			TotalValueBase:    bal.QuantityOnHand.Mul(bal.AverageCost).RoundBank(2),
		})
	}
	return rows, nil
}

// getOnHandHistorical reconstructs on-hand quantity, weighted-average cost
// and total value at AsOfDate. Quantity comes from summing
// QuantityDelta ≤ AsOfDate; average cost is replayed via
// replayHistoricalValue so historical statements can render dollars, not
// just units. Phase E3.
func getOnHandHistorical(db *gorm.DB, q OnHandQuery) ([]OnHandRow, error) {
	// First pick which (item, warehouse) pairs have any movement by the
	// AsOfDate — this bounds the replay work. For each pair, replay the
	// movements to derive (qty, avg, value).
	type scopeRow struct {
		ItemID      uint
		WarehouseID *uint
	}
	var scopes []scopeRow

	dbq := db.Model(&models.InventoryMovement{}).
		Select("item_id, warehouse_id").
		Where("company_id = ? AND movement_date <= ?", q.CompanyID, *q.AsOfDate).
		Group("item_id, warehouse_id")
	if q.ItemID != 0 {
		dbq = dbq.Where("item_id = ?", q.ItemID)
	}
	if q.WarehouseID != 0 {
		dbq = dbq.Where("warehouse_id = ?", q.WarehouseID)
	}
	if err := dbq.Scan(&scopes).Error; err != nil {
		return nil, fmt.Errorf("inventory.GetOnHand historical: %w", err)
	}

	result := make([]OnHandRow, 0, len(scopes))
	for _, s := range scopes {
		qty, avg, value, err := historicalValueAt(db, q.CompanyID, s.ItemID, s.WarehouseID, *q.AsOfDate)
		if err != nil {
			return nil, err
		}
		if !q.IncludeZero && qty.IsZero() {
			continue
		}
		whID := uint(0)
		if s.WarehouseID != nil {
			whID = *s.WarehouseID
		}
		result = append(result, OnHandRow{
			ItemID:          s.ItemID,
			WarehouseID:     whID,
			QuantityOnHand:  qty,
			AverageCostBase: avg,
			TotalValueBase:  value,
		})
	}
	return result, nil
}

// historicalValueAt routes to the right algorithm based on the company's
// costing method. FIFO companies get layer-based point-in-time value
// (correct per-layer state); weighted-average companies get replay-based
// value (running avg across the full movement history).
//
// E3 hardening: moving-average replay must NOT be served to FIFO
// companies — that would silently alias FIFO semantics behind
// weighted-avg numbers. See INVENTORY_MODULE_API.md §4.1 "FIFO historical
// valuation goes through layers".
func historicalValueAt(db *gorm.DB, companyID, itemID uint, warehouseID *uint, asOfDate time.Time) (qty, avg, value decimal.Decimal, err error) {
	method := companyCostingMethod(db, companyID)
	if method == models.InventoryCostingFIFO {
		return historicalFIFOValue(db, companyID, itemID, warehouseID, asOfDate)
	}
	return replayHistoricalValue(db, companyID, itemID, warehouseID, asOfDate)
}

// companyCostingMethod looks up the company's configured costing method.
// Defaults to moving-average for companies whose row can't be loaded (the
// legacy path, and an explicit choice: it's the algorithm replayHistorical
// is correct for, so treating unknown as weighted-avg is safe).
func companyCostingMethod(db *gorm.DB, companyID uint) string {
	var c models.Company
	if err := db.Select("id", "inventory_costing_method").First(&c, companyID).Error; err != nil {
		return models.InventoryCostingMovingAverage
	}
	if c.InventoryCostingMethod == "" {
		return models.InventoryCostingMovingAverage
	}
	return c.InventoryCostingMethod
}

// historicalFIFOValue reconstructs point-in-time FIFO value from the cost
// layer table + consumption log. For each layer that existed by asOfDate,
// the function:
//
//  1. Starts from OriginalQuantity.
//  2. Subtracts every consumption row whose issue movement_date ≤ asOfDate.
//  3. Adds back each subtraction whose reversal movement_date ≤ asOfDate.
//
// The result is the layer's RemainingQuantity *as-of that date*. Summing
// (remaining × unit_cost_base) across layers gives the true FIFO value at
// that point in time — no weighted-avg approximation.
//
// Layers received strictly after asOfDate are excluded; consumption rows
// whose issue movement post-dates asOfDate are ignored entirely.
func historicalFIFOValue(db *gorm.DB, companyID, itemID uint, warehouseID *uint, asOfDate time.Time) (qty, avg, value decimal.Decimal, err error) {
	lq := db.Where("company_id = ? AND item_id = ? AND received_date <= ?",
		companyID, itemID, asOfDate)
	if warehouseID != nil {
		lq = lq.Where("warehouse_id = ?", *warehouseID)
	}
	var layers []models.InventoryCostLayer
	if err := lq.Find(&layers).Error; err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("inventory: load layers: %w", err)
	}
	if len(layers) == 0 {
		return decimal.Zero, decimal.Zero, decimal.Zero, nil
	}

	// Gather layer IDs so we can fetch consumption in one query.
	layerIDs := make([]uint, 0, len(layers))
	for _, l := range layers {
		layerIDs = append(layerIDs, l.ID)
	}

	// Pull consumption rows for these layers and join each side's
	// movement_date so we can filter by asOfDate in Go.
	type consumptionRow struct {
		LayerID       uint
		QuantityDrawn decimal.Decimal
		IssueDate     time.Time
		ReversalDate  *time.Time
	}
	var rows []consumptionRow
	raw := db.Table("inventory_layer_consumption AS c").
		Select(`c.layer_id, c.quantity_drawn,
			im.movement_date AS issue_date,
			rm.movement_date AS reversal_date`).
		Joins("JOIN inventory_movements AS im ON im.id = c.issue_movement_id").
		Joins("LEFT JOIN inventory_movements AS rm ON rm.id = c.reversed_by_movement_id").
		Where("c.company_id = ? AND c.layer_id IN ?", companyID, layerIDs)
	if err := raw.Scan(&rows).Error; err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("inventory: load consumption: %w", err)
	}

	// Net-drawn per layer at asOfDate.
	netDrawn := map[uint]decimal.Decimal{}
	for _, r := range rows {
		if r.IssueDate.After(asOfDate) {
			continue // consumption hadn't happened yet
		}
		// Reversal only "counts" if it happened by asOfDate too.
		if r.ReversalDate != nil && !r.ReversalDate.After(asOfDate) {
			continue // draw was undone by asOfDate → net zero
		}
		netDrawn[r.LayerID] = netDrawn[r.LayerID].Add(r.QuantityDrawn)
	}

	for _, l := range layers {
		drawn := netDrawn[l.ID]
		remaining := l.OriginalQuantity.Sub(drawn)
		if !remaining.IsPositive() {
			continue
		}
		qty = qty.Add(remaining)
		value = value.Add(remaining.Mul(l.UnitCostBase))
	}

	if qty.IsPositive() {
		avg = value.Div(qty).Round(4)
		value = value.RoundBank(2)
	} else {
		qty = decimal.Zero
		avg = decimal.Zero
		value = decimal.Zero
	}
	return qty, avg, value, nil
}

// replayHistoricalValue walks every movement for (company, item,
// warehouse) up to and including asOfDate, in chronological order, and
// returns the final (quantity, average unit cost, total value).
//
// Weighted-average ONLY. FIFO companies must route through
// historicalFIFOValue — calling this function for a FIFO company would
// silently produce a weighted-avg approximation instead of true FIFO
// state. The public entry point historicalValueAt enforces the dispatch.
//
// Rules:
//
//   - Positive delta (receipt): value += delta × UnitCostBase;
//     qty += delta.
//   - Negative delta (issue / reversal): value += delta × UnitCostBase
//     (delta is negative, so this reduces); qty += delta.
//
// The critical invariant is that the *recorded* UnitCostBase on each
// movement is always "the cost that applied at that event" — the running
// avg at issue time for outbound events, the receipt cost for inbound
// events, or the snapshot cost for reversal events. Using it verbatim in
// the replay therefore preserves the correct running avg at every step.
//
// warehouseID=nil aggregates across warehouses.
func replayHistoricalValue(db *gorm.DB, companyID, itemID uint, warehouseID *uint, asOfDate time.Time) (qty, avg, value decimal.Decimal, err error) {
	q := db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND item_id = ? AND movement_date <= ?",
			companyID, itemID, asOfDate)
	if warehouseID != nil {
		q = q.Where("warehouse_id = ?", *warehouseID)
	}
	var movs []models.InventoryMovement
	if err := q.Order("movement_date asc, id asc").Find(&movs).Error; err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("inventory: load movements for replay: %w", err)
	}

	for _, m := range movs {
		unit := decimal.Zero
		if m.UnitCostBase != nil {
			unit = *m.UnitCostBase
		} else if m.UnitCost != nil {
			// Legacy rows may not have UnitCostBase set; fall back to UnitCost
			// (doc currency = base currency for pre-FX rows).
			unit = *m.UnitCost
		}
		delta := m.QuantityDelta
		if delta.IsZero() {
			continue
		}
		qty = qty.Add(delta)
		// Value follows the signed delta: positive delta adds cost, negative
		// delta removes cost at its recorded unit rate.
		value = value.Add(delta.Mul(unit))
	}

	if qty.IsPositive() {
		avg = value.Div(qty).Round(4)
		value = value.RoundBank(2)
	} else {
		// Zero or negative on-hand: avg is not meaningful.
		avg = decimal.Zero
		value = decimal.Zero
	}
	return qty, avg, value, nil
}

// ── GetMovements ─────────────────────────────────────────────────────────────

// GetMovements returns the movement ledger with a full filter set and
// optional running balance. Total count is returned alongside the page
// so callers can paginate without a separate COUNT query.
func GetMovements(db *gorm.DB, q MovementQuery) ([]MovementRow, int64, error) {
	if q.CompanyID == 0 {
		return nil, 0, fmt.Errorf("inventory.GetMovements: CompanyID required")
	}

	dbq := db.Model(&models.InventoryMovement{}).Where("company_id = ?", q.CompanyID)
	if q.ItemID != nil {
		dbq = dbq.Where("item_id = ?", *q.ItemID)
	}
	if q.WarehouseID != nil {
		dbq = dbq.Where("warehouse_id = ?", *q.WarehouseID)
	}
	if q.FromDate != nil {
		dbq = dbq.Where("movement_date >= ?", *q.FromDate)
	}
	if q.ToDate != nil {
		dbq = dbq.Where("movement_date <= ?", *q.ToDate)
	}
	if q.SourceType != "" {
		dbq = dbq.Where("source_type = ?", q.SourceType)
	}
	if q.SourceID != nil {
		dbq = dbq.Where("source_id = ?", *q.SourceID)
	}
	if q.Direction != nil {
		switch *q.Direction {
		case MovementDirectionIn:
			dbq = dbq.Where("quantity_delta > 0")
		case MovementDirectionOut:
			dbq = dbq.Where("quantity_delta < 0")
		}
	}

	var total int64
	if err := dbq.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("inventory.GetMovements count: %w", err)
	}

	pageQ := dbq.Order("movement_date asc, id asc")
	if q.Limit > 0 {
		pageQ = pageQ.Limit(q.Limit)
	}
	if q.Offset > 0 {
		pageQ = pageQ.Offset(q.Offset)
	}
	var movs []models.InventoryMovement
	if err := pageQ.Find(&movs).Error; err != nil {
		return nil, 0, fmt.Errorf("inventory.GetMovements: %w", err)
	}

	rows := make([]MovementRow, 0, len(movs))
	var runQty, runValue decimal.Decimal
	for _, m := range movs {
		unitCost := decimal.Zero
		if m.UnitCostBase != nil {
			unitCost = *m.UnitCostBase
		} else if m.UnitCost != nil {
			unitCost = *m.UnitCost
		}
		totalCost := m.QuantityDelta.Mul(unitCost).RoundBank(2)

		row := MovementRow{
			ID:            m.ID,
			MovementDate:  m.MovementDate,
			ItemID:        m.ItemID,
			WarehouseID:   m.WarehouseID,
			MovementType:  string(m.MovementType),
			QuantityDelta: m.QuantityDelta,
			UnitCostBase:  unitCost,
			TotalCostBase: totalCost,
			SourceType:    m.SourceType,
			SourceID:      m.SourceID,
			SourceLineID:  m.SourceLineID,
			Memo:          m.Memo,
			ActorUserID:   m.ActorUserID,
			CreatedAt:     m.CreatedAt,
		}
		if q.IncludeRunningBalance {
			runQty = runQty.Add(m.QuantityDelta)
			runValue = runValue.Add(totalCost)
			row.RunningQuantity = runQty
			row.RunningValueBase = runValue
		}
		rows = append(rows, row)
	}
	return rows, total, nil
}

// ── GetItemLedger ────────────────────────────────────────────────────────────

// GetItemLedger returns the per-item ledger for a date range: opening
// balance before FromDate, every movement within the window, closing
// balance as of ToDate, and the period totals useful for reconciliation.
func GetItemLedger(db *gorm.DB, q ItemLedgerQuery) (*ItemLedgerReport, error) {
	if q.CompanyID == 0 || q.ItemID == 0 {
		return nil, fmt.Errorf("inventory.GetItemLedger: CompanyID and ItemID required")
	}

	// Opening: reconstruct state strictly before FromDate. Routes to
	// historicalFIFOValue for FIFO companies or weighted-avg replay
	// otherwise (see historicalValueAt).
	openingCutoff := q.FromDate.Add(-1 * 24 * time.Hour)
	openingQty, openingUnit, openingValue, err := historicalValueAt(
		db, q.CompanyID, q.ItemID, q.WarehouseID, openingCutoff)
	if err != nil {
		return nil, err
	}

	// Closing: reconstruct state up to and including ToDate. Prefer the
	// authoritative cached InventoryBalance when ToDate is today (or
	// later) so current-period statements match what GetOnHand returns
	// without replay.
	closingQty, closingUnitCost, closingValue, err := historicalValueAt(
		db, q.CompanyID, q.ItemID, q.WarehouseID, q.ToDate)
	if err != nil {
		return nil, err
	}
	if isRecent(q.ToDate) {
		balQ := db.Model(&models.InventoryBalance{}).Where("company_id = ? AND item_id = ?", q.CompanyID, q.ItemID)
		if q.WarehouseID != nil {
			balQ = balQ.Where("warehouse_id = ?", *q.WarehouseID)
		}
		var balances []models.InventoryBalance
		balQ.Find(&balances)
		var totalQty, totalValue decimal.Decimal
		for _, b := range balances {
			totalQty = totalQty.Add(b.QuantityOnHand)
			totalValue = totalValue.Add(b.QuantityOnHand.Mul(b.AverageCost))
		}
		if totalQty.IsPositive() {
			closingQty = totalQty
			closingUnitCost = totalValue.Div(totalQty).Round(4)
			closingValue = totalValue.RoundBank(2)
		}
	}

	// Period movements.
	movementRows, _, err := GetMovements(db, MovementQuery{
		CompanyID:             q.CompanyID,
		ItemID:                &q.ItemID,
		WarehouseID:           q.WarehouseID,
		FromDate:              &q.FromDate,
		ToDate:                &q.ToDate,
		IncludeRunningBalance: true,
	})
	if err != nil {
		return nil, err
	}

	// Totals over the period.
	var inQty, inValue, outQty, outCost decimal.Decimal
	for _, m := range movementRows {
		switch {
		case m.QuantityDelta.IsPositive():
			inQty = inQty.Add(m.QuantityDelta)
			inValue = inValue.Add(m.TotalCostBase)
		case m.QuantityDelta.IsNegative():
			outQty = outQty.Add(m.QuantityDelta.Abs())
			outCost = outCost.Add(m.TotalCostBase.Abs())
		}
	}

	return &ItemLedgerReport{
		ItemID:           q.ItemID,
		WarehouseID:      q.WarehouseID,
		OpeningQuantity:  openingQty,
		OpeningValueBase: openingValue,
		OpeningUnitCost:  openingUnit, // Phase E3: replayed weighted-avg
		Movements:        movementRows,
		ClosingQuantity:  closingQty,
		ClosingValueBase: closingValue,
		ClosingUnitCost:  closingUnitCost,
		TotalInQty:       inQty,
		TotalInValue:     inValue,
		TotalOutQty:      outQty,
		TotalOutCostBase: outCost,
	}, nil
}

// ── GetValuationSnapshot ─────────────────────────────────────────────────────

// GetValuationSnapshot totals inventory value across items / warehouses.
// Phase D.0 supports current valuation only (ignores AsOfDate); point-in-
// time historical valuation is a Phase E task.
func GetValuationSnapshot(db *gorm.DB, q ValuationQuery) ([]ValuationRow, decimal.Decimal, error) {
	if q.CompanyID == 0 {
		return nil, decimal.Zero, fmt.Errorf("inventory.GetValuationSnapshot: CompanyID required")
	}

	dbq := db.Model(&models.InventoryBalance{}).
		Where("company_id = ? AND quantity_on_hand <> 0", q.CompanyID)
	if q.WarehouseID != nil {
		dbq = dbq.Where("warehouse_id = ?", *q.WarehouseID)
	}
	var balances []models.InventoryBalance
	if err := dbq.Find(&balances).Error; err != nil {
		return nil, decimal.Zero, fmt.Errorf("inventory.GetValuationSnapshot: %w", err)
	}

	// Aggregate. Each balance contributes qty × avg; group key per GroupBy.
	groups := map[string]*ValuationRow{}
	var grandTotal decimal.Decimal
	for _, b := range balances {
		value := b.QuantityOnHand.Mul(b.AverageCost).RoundBank(2)
		grandTotal = grandTotal.Add(value)

		key := ""
		switch q.GroupBy {
		case ValuationGroupByItem:
			key = fmt.Sprintf("item:%d", b.ItemID)
		case ValuationGroupByWarehouse:
			if b.WarehouseID != nil {
				key = fmt.Sprintf("warehouse:%d", *b.WarehouseID)
			} else {
				key = "warehouse:unassigned"
			}
		case ValuationGroupByCategory:
			// Category grouping needs a join to product_services.category
			// which isn't modeled cleanly today. Collapse to a single
			// "uncategorised" bucket until that's first-class.
			key = "category:uncategorised"
		default:
			key = "total"
		}

		if g, ok := groups[key]; ok {
			g.Quantity = g.Quantity.Add(b.QuantityOnHand)
			g.ValueBase = g.ValueBase.Add(value)
		} else {
			groups[key] = &ValuationRow{
				GroupKey:   key,
				GroupLabel: key,
				Quantity:   b.QuantityOnHand,
				ValueBase:  value,
			}
		}
	}

	rows := make([]ValuationRow, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, *g)
	}
	return rows, grandTotal.RoundBank(2), nil
}

// ── GetCostingPreview ────────────────────────────────────────────────────────

// GetCostingPreview reports the cost a hypothetical IssueStock would book,
// without touching state. Used for quotations and margin previews.
//
// WarehouseID=0 means "aggregate across all warehouses for this company /
// item" — useful for legacy single-warehouse callers and any company that
// has not opted into multi-warehouse routing. The returned UnitCostBase is
// the company-level weighted-average cost (sum(qty × avg) / sum(qty)) and
// the feasibility check uses the summed on-hand quantity.
func GetCostingPreview(db *gorm.DB, q CostingPreviewQuery) (*CostingPreviewResult, error) {
	if q.CompanyID == 0 || q.ItemID == 0 {
		return nil, fmt.Errorf("inventory.GetCostingPreview: CompanyID and ItemID required")
	}
	if !q.Quantity.IsPositive() {
		return nil, ErrNegativeQuantity
	}

	var whPtr *uint
	if q.WarehouseID != 0 {
		id := q.WarehouseID
		whPtr = &id
	}
	bal, err := lookupComponentBalance(db, q.CompanyID, q.ItemID, whPtr)
	if err != nil {
		return nil, fmt.Errorf("inventory.GetCostingPreview: %w", err)
	}

	feasible := bal.QuantityOnHand.GreaterThanOrEqual(q.Quantity)
	short := decimal.Zero
	if !feasible {
		short = q.Quantity.Sub(bal.QuantityOnHand)
	}
	return &CostingPreviewResult{
		UnitCostBase:  bal.AverageCost,
		TotalCostBase: q.Quantity.Mul(bal.AverageCost).RoundBank(2),
		Feasible:      feasible,
		ShortBy:       short,
	}, nil
}

// ── ExplodeBOM ───────────────────────────────────────────────────────────────

// bomExplodeMaxDepth caps recursion to guard against pathological BOMs and
// any undetected cycles (belt + braces alongside the visited-set check).
// Five levels comfortably covers "sub-assembly → component" chains; anything
// deeper is a design smell worth surfacing as an error.
const bomExplodeMaxDepth = 5

// ExplodeBOM recursively expands a parent product into its components using
// the item_components table. MultiLevel=false returns only direct children;
// MultiLevel=true recurses until every row is a leaf (no further
// item_components rows). Cycles are blocked with a visited-path set;
// exceeding bomExplodeMaxDepth returns ErrBOMTooDeep.
//
// Optional enrichments:
//   - IncludeCostEstimate populates EstimatedUnitCostBase /
//     EstimatedTotalCostBase from the component's current weighted-average
//     cost at whichever warehouse is keyed (or zero when WarehouseID nil).
//   - IncludeAvailability populates AvailableQuantity and ShortBy against
//     the target warehouse. Requires WarehouseID non-nil.
func ExplodeBOM(db *gorm.DB, q BOMExplodeQuery) ([]BOMExplodeRow, error) {
	if q.CompanyID == 0 || q.ParentItemID == 0 {
		return nil, fmt.Errorf("inventory.ExplodeBOM: CompanyID and ParentItemID required")
	}
	if !q.Quantity.IsPositive() {
		return nil, ErrNegativeQuantity
	}
	if q.IncludeAvailability && q.WarehouseID == nil {
		return nil, fmt.Errorf("inventory.ExplodeBOM: WarehouseID required when IncludeAvailability=true")
	}

	rows := make([]BOMExplodeRow, 0, 4)
	visited := map[uint]bool{q.ParentItemID: true}
	path := []uint{q.ParentItemID}

	if err := explodeWalk(db, q, q.ParentItemID, q.Quantity, 0, visited, path, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// explodeWalk is the recursive step. It reads item_components rows for the
// current parent and (depending on MultiLevel) either appends each component
// as a leaf or recurses into it. visited/path are carried by value-ish:
// visited is mutated on entry/exit to allow sibling branches to re-visit
// products that appear in multiple sub-trees (only pure cycles are blocked).
func explodeWalk(db *gorm.DB, q BOMExplodeQuery,
	parentID uint, parentQty decimal.Decimal, depth int,
	visited map[uint]bool, path []uint,
	out *[]BOMExplodeRow,
) error {
	if depth >= bomExplodeMaxDepth {
		return ErrBOMTooDeep
	}

	var comps []models.ItemComponent
	err := db.Preload("ComponentItem").
		Where("company_id = ? AND parent_item_id = ?", q.CompanyID, parentID).
		Order("sort_order asc, id asc").
		Find(&comps).Error
	if err != nil {
		return fmt.Errorf("inventory.ExplodeBOM: load components: %w", err)
	}

	for _, c := range comps {
		if visited[c.ComponentItemID] {
			return fmt.Errorf("%w: %d -> %d", ErrBOMCycle, parentID, c.ComponentItemID)
		}

		// Scrap is not yet on ItemComponent (it lives on the future
		// product_components schema we no longer need — item_components is
		// already the canonical table). Treat scrap as zero; when a scrap
		// column is added to item_components this is the one place to read
		// it.
		scrap := decimal.Zero
		effectiveQty := parentQty.Mul(c.Quantity)

		childPath := append(append([]uint(nil), path...), c.ComponentItemID)
		row := BOMExplodeRow{
			ComponentItemID: c.ComponentItemID,
			Depth:           depth,
			Path:            childPath,
			QuantityPerUnit: c.Quantity,
			TotalQuantity:   effectiveQty,
			ScrapPct:        scrap,
		}

		// Only recurse into this component when it's itself a parent with
		// rows in item_components AND MultiLevel is enabled AND we haven't
		// exhausted depth. Otherwise treat as a leaf and emit the row.
		recurse := false
		if q.MultiLevel {
			var childCount int64
			db.Model(&models.ItemComponent{}).
				Where("company_id = ? AND parent_item_id = ?", q.CompanyID, c.ComponentItemID).
				Count(&childCount)
			recurse = childCount > 0
		}

		if recurse {
			// Guard cycle detection: visit on descent, un-visit on return,
			// so a component appearing in two sibling sub-trees doesn't
			// spuriously trip as a cycle.
			visited[c.ComponentItemID] = true
			if err := explodeWalk(db, q, c.ComponentItemID, effectiveQty, depth+1, visited, childPath, out); err != nil {
				return err
			}
			delete(visited, c.ComponentItemID)
			continue
		}

		// Enrichments for leaf rows.
		if q.IncludeCostEstimate || q.IncludeAvailability {
			bal, err := lookupComponentBalance(db, q.CompanyID, c.ComponentItemID, q.WarehouseID)
			if err != nil {
				return err
			}
			if q.IncludeCostEstimate {
				unit := bal.AverageCost
				total := effectiveQty.Mul(unit).RoundBank(2)
				row.EstimatedUnitCostBase = &unit
				row.EstimatedTotalCostBase = &total
			}
			if q.IncludeAvailability {
				avail := bal.QuantityOnHand
				row.AvailableQuantity = &avail
				if avail.LessThan(effectiveQty) {
					short := effectiveQty.Sub(avail)
					row.ShortBy = &short
				}
			}
		}

		*out = append(*out, row)
	}
	return nil
}

// lookupComponentBalance reads the balance for (company, item, warehouse).
// When warehouseID is nil the balance is summed across warehouses so cost
// estimates reflect blended company-level avg.
func lookupComponentBalance(db *gorm.DB, companyID, itemID uint, warehouseID *uint) (models.InventoryBalance, error) {
	if warehouseID != nil {
		var bal models.InventoryBalance
		err := db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
			companyID, itemID, *warehouseID).First(&bal).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.InventoryBalance{}, nil
		}
		return bal, err
	}

	// Aggregate across warehouses.
	var balances []models.InventoryBalance
	if err := db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Find(&balances).Error; err != nil {
		return models.InventoryBalance{}, err
	}
	var totalQty, totalValue decimal.Decimal
	for _, b := range balances {
		totalQty = totalQty.Add(b.QuantityOnHand)
		totalValue = totalValue.Add(b.QuantityOnHand.Mul(b.AverageCost))
	}
	agg := models.InventoryBalance{CompanyID: companyID, ItemID: itemID, QuantityOnHand: totalQty}
	if totalQty.IsPositive() {
		agg.AverageCost = totalValue.Div(totalQty).Round(4)
	}
	return agg, nil
}

// ── GetAvailableForBuild ─────────────────────────────────────────────────────

// GetAvailableForBuild reports the maximum quantity of parent_item that can
// be built at warehouseID given current component stock. Returns the
// bottleneck component — the one that caps the answer. Suitable for the
// "build" UI to show "you can assemble N units; you'd need M more of
// component X to reach the quantity you want".
func GetAvailableForBuild(db *gorm.DB, companyID, parentItemID, warehouseID uint) (decimal.Decimal, uint, error) {
	if companyID == 0 || parentItemID == 0 {
		return decimal.Zero, 0, fmt.Errorf("inventory.GetAvailableForBuild: CompanyID and ParentItemID required")
	}

	var comps []models.ItemComponent
	if err := db.
		Where("company_id = ? AND parent_item_id = ?", companyID, parentItemID).
		Find(&comps).Error; err != nil {
		return decimal.Zero, 0, fmt.Errorf("inventory.GetAvailableForBuild: load components: %w", err)
	}
	if len(comps) == 0 {
		return decimal.Zero, 0, fmt.Errorf("inventory.GetAvailableForBuild: parent %d has no components", parentItemID)
	}

	var whPtr *uint
	if warehouseID != 0 {
		id := warehouseID
		whPtr = &id
	}

	var (
		maxBuildable     decimal.Decimal
		bottleneckItemID uint
		first            = true
	)
	for _, c := range comps {
		bal, err := lookupComponentBalance(db, companyID, c.ComponentItemID, whPtr)
		if err != nil {
			return decimal.Zero, 0, err
		}
		// Maximum parent units this component can support = on_hand / per_unit.
		// Guard divide-by-zero on a misconfigured component row.
		if !c.Quantity.IsPositive() {
			continue
		}
		possible := bal.QuantityOnHand.Div(c.Quantity).Floor()
		if first || possible.LessThan(maxBuildable) {
			maxBuildable = possible
			bottleneckItemID = c.ComponentItemID
			first = false
		}
	}
	if first {
		return decimal.Zero, 0, fmt.Errorf("inventory.GetAvailableForBuild: no valid components with positive per-unit quantity")
	}
	if maxBuildable.IsNegative() {
		maxBuildable = decimal.Zero
	}
	return maxBuildable, bottleneckItemID, nil
}

// ── Internal helpers ─────────────────────────────────────────────────────────

func ptrTime(t time.Time) *time.Time { return &t }
func isRecent(t time.Time) bool {
	return t.IsZero() || !t.Before(time.Now().Add(-24*time.Hour))
}
