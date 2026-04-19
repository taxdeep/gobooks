// 遵循project_guide.md
package inventory

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

// Historical GetOnHand now reports quantity AND weighted-avg cost AND
// total value at a point in time. Phase E3.
//
// Timeline:
//   day 1: receive 10 @ $4  → qty 10, avg 4, value 40
//   day 2: receive 10 @ $6  → qty 20, avg 5, value 100
//   day 3: issue 5 (@ avg 5)→ qty 15, avg 5, value 75
//
// Asking as-of day 1 should report qty 10, avg 4, value 40 — not the
// current-day state.
func TestGetOnHand_HistoricalReconstructsValue(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	must := func(err error) {
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base,
		UnitCost: decimal.NewFromInt(4), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
	})
	must(err)
	_, err = ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base.AddDate(0, 0, 1),
		UnitCost: decimal.NewFromInt(6), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 2,
	})
	must(err)
	_, err = IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: base.AddDate(0, 0, 2),
		SourceType: "invoice", SourceID: 1,
	})
	must(err)

	// As-of day 1: only the first receipt counts.
	asOf := base
	rows, err := GetOnHand(db, OnHandQuery{
		CompanyID: companyID, ItemID: itemID, AsOfDate: &asOf,
	})
	if err != nil {
		t.Fatalf("GetOnHand historical: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	if !rows[0].QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("as-of day 1 qty: got %s want 10", rows[0].QuantityOnHand)
	}
	if !rows[0].AverageCostBase.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("as-of day 1 avg: got %s want 4", rows[0].AverageCostBase)
	}
	if !rows[0].TotalValueBase.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("as-of day 1 value: got %s want 40", rows[0].TotalValueBase)
	}

	// As-of day 2: both receipts.
	asOf2 := base.AddDate(0, 0, 1)
	rows, err = GetOnHand(db, OnHandQuery{
		CompanyID: companyID, ItemID: itemID, AsOfDate: &asOf2,
	})
	if err != nil {
		t.Fatalf("GetOnHand historical: %v", err)
	}
	if !rows[0].QuantityOnHand.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("as-of day 2 qty: got %s want 20", rows[0].QuantityOnHand)
	}
	if !rows[0].AverageCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("as-of day 2 avg: got %s want 5", rows[0].AverageCostBase)
	}
	if !rows[0].TotalValueBase.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("as-of day 2 value: got %s want 100", rows[0].TotalValueBase)
	}

	// As-of day 3: after the issue.
	asOf3 := base.AddDate(0, 0, 2)
	rows, err = GetOnHand(db, OnHandQuery{
		CompanyID: companyID, ItemID: itemID, AsOfDate: &asOf3,
	})
	if err != nil {
		t.Fatalf("GetOnHand historical: %v", err)
	}
	if !rows[0].QuantityOnHand.Equal(decimal.NewFromInt(15)) {
		t.Fatalf("as-of day 3 qty: got %s want 15", rows[0].QuantityOnHand)
	}
	if !rows[0].AverageCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("as-of day 3 avg: got %s want 5 (unchanged on issue)", rows[0].AverageCostBase)
	}
	if !rows[0].TotalValueBase.Equal(decimal.NewFromInt(75)) {
		t.Fatalf("as-of day 3 value: got %s want 75", rows[0].TotalValueBase)
	}
}

// GetItemLedger now fills OpeningUnitCost / OpeningValueBase for
// historical date ranges (was zero before E3).
func TestGetItemLedger_OpeningAndClosingFromReplay(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base,
		UnitCost: decimal.NewFromInt(4), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base.AddDate(0, 0, 10),
		UnitCost: decimal.NewFromInt(6), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 2,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Query window: days 5..15. Opening state = end of day 4 (10 @ $4).
	// Closing state = end of day 15 (20 units, avg 5).
	whID := warehouseID
	report, err := GetItemLedger(db, ItemLedgerQuery{
		CompanyID:   companyID,
		ItemID:      itemID,
		WarehouseID: &whID,
		FromDate:    base.AddDate(0, 0, 5),
		ToDate:      base.AddDate(0, 0, 15),
	})
	if err != nil {
		t.Fatalf("GetItemLedger: %v", err)
	}

	if !report.OpeningQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("OpeningQuantity: got %s want 10", report.OpeningQuantity)
	}
	if !report.OpeningUnitCost.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("OpeningUnitCost: got %s want 4 (was 0 pre-E3)", report.OpeningUnitCost)
	}
	if !report.OpeningValueBase.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("OpeningValueBase: got %s want 40", report.OpeningValueBase)
	}
}

// FIFO company historical valuation uses layer state, NOT weighted-avg
// replay. Under FIFO, a sale of the oldest-layer units must leave the
// younger layer intact at its own cost — any weighted-avg replay would
// average the two and produce a wrong unit cost.
//
// Setup:
//   day 1: receive 10 @ $4 (layer L1)
//   day 2: receive 10 @ $6 (layer L2)
//   day 3: FIFO issue 6 → draws layer L1, L1.remaining = 4
//
// As-of day 3:
//   - qty = 4 + 10 = 14
//   - value = 4×4 + 10×6 = 76
//   - avg = 76 / 14 = 5.4286 (FIFO blended — NOT the weighted-avg 5.1429)
//
// If the code mistakenly used replayHistoricalValue, it would return a
// weighted-avg-shaped number; the test below locks the FIFO contract.
func TestHistoricalValueAt_FIFOCompany_UsesLayerState(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	// Flip the company to FIFO so historicalValueAt takes the layer path.
	if err := db.Model(&models.Company{}).Where("id = ?", companyID).
		Update("inventory_costing_method", models.InventoryCostingFIFO).Error; err != nil {
		t.Fatalf("set fifo: %v", err)
	}

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base,
		UnitCost: decimal.NewFromInt(4), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
	}); err != nil {
		t.Fatalf("seed layer 1: %v", err)
	}
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base.AddDate(0, 0, 1),
		UnitCost: decimal.NewFromInt(6), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 2,
	}); err != nil {
		t.Fatalf("seed layer 2: %v", err)
	}
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(6), MovementDate: base.AddDate(0, 0, 2),
		SourceType: "invoice", SourceID: 1,
		CostingMethod: CostingMethodFIFO,
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	asOf := base.AddDate(0, 0, 2) // after the FIFO issue
	rows, err := GetOnHand(db, OnHandQuery{
		CompanyID: companyID, ItemID: itemID, AsOfDate: &asOf,
	})
	if err != nil {
		t.Fatalf("GetOnHand historical: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	r := rows[0]

	if !r.QuantityOnHand.Equal(decimal.NewFromInt(14)) {
		t.Fatalf("qty: got %s want 14", r.QuantityOnHand)
	}
	// True FIFO value: 4×4 + 10×6 = 76.
	if !r.TotalValueBase.Equal(decimal.NewFromInt(76)) {
		t.Fatalf("value: got %s want 76 (FIFO layer-based)", r.TotalValueBase)
	}
	// avg = 76 / 14 = 5.4286 (rounded 4dp).
	wantAvg := decimal.RequireFromString("5.4286")
	if !r.AverageCostBase.Equal(wantAvg) {
		t.Fatalf("avg: got %s want %s (FIFO, NOT weighted-avg 5.1429)",
			r.AverageCostBase, wantAvg)
	}
}

// FIFO historical after a reversed issue: the layer draw is undone by
// asOfDate, so the layer appears fully populated. Verifies that the
// consumption reversal is respected in point-in-time computation.
func TestHistoricalValueAt_FIFOCompany_ReversalRestoresLayerAsOf(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	db.Model(&models.Company{}).Where("id = ?", companyID).
		Update("inventory_costing_method", models.InventoryCostingFIFO)

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base,
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: base.AddDate(0, 0, 1),
		SourceType: "invoice", SourceID: 1,
		CostingMethod: CostingMethodFIFO,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Reverse it on day 3 — a customer return.
	if _, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: issue.MovementID,
		MovementDate:       base.AddDate(0, 0, 2),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           1,
	}); err != nil {
		t.Fatalf("reversal: %v", err)
	}

	// As-of day 1 (before the issue): qty 10, value 50.
	d1 := base
	rows, err := GetOnHand(db, OnHandQuery{
		CompanyID: companyID, ItemID: itemID, AsOfDate: &d1,
	})
	if err != nil {
		t.Fatalf("GetOnHand d1: %v", err)
	}
	if !rows[0].QuantityOnHand.Equal(decimal.NewFromInt(10)) ||
		!rows[0].TotalValueBase.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("d1: got qty=%s value=%s want 10/50", rows[0].QuantityOnHand, rows[0].TotalValueBase)
	}

	// As-of day 2 (after issue, before reversal): qty 7, value 35.
	d2 := base.AddDate(0, 0, 1)
	rows, err = GetOnHand(db, OnHandQuery{
		CompanyID: companyID, ItemID: itemID, AsOfDate: &d2,
	})
	if err != nil {
		t.Fatalf("GetOnHand d2: %v", err)
	}
	if !rows[0].QuantityOnHand.Equal(decimal.NewFromInt(7)) ||
		!rows[0].TotalValueBase.Equal(decimal.NewFromInt(35)) {
		t.Fatalf("d2: got qty=%s value=%s want 7/35", rows[0].QuantityOnHand, rows[0].TotalValueBase)
	}

	// As-of day 3 (after reversal): qty 10, value 50 — the layer is
	// fully restored because the consumption rolled back by this date.
	d3 := base.AddDate(0, 0, 2)
	rows, err = GetOnHand(db, OnHandQuery{
		CompanyID: companyID, ItemID: itemID, AsOfDate: &d3,
	})
	if err != nil {
		t.Fatalf("GetOnHand d3: %v", err)
	}
	if !rows[0].QuantityOnHand.Equal(decimal.NewFromInt(10)) ||
		!rows[0].TotalValueBase.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("d3: got qty=%s value=%s want 10/50 (reversal restored layer)",
			rows[0].QuantityOnHand, rows[0].TotalValueBase)
	}
}

// Legacy rows with UnitCost set but no UnitCostBase still replay correctly.
// Exercises the fallback in replayHistoricalValue.
func TestReplay_FallsBackToUnitCostForLegacyRows(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	// Manually insert a movement without UnitCostBase (simulating a pre-
	// migration-056 row). Use non-positive ID assignment so GORM creates it.
	whIDVal := warehouseID
	unitCost := decimal.NewFromInt(7)
	// Note: we populate UnitCost but NOT UnitCostBase.
	legacy := models.InventoryMovement{
		CompanyID:     companyID,
		ItemID:        itemID,
		MovementType:  models.MovementTypeOpening,
		QuantityDelta: decimal.NewFromInt(5),
		UnitCost:      &unitCost,
		SourceType:    "opening",
		MovementDate:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		WarehouseID:   &whIDVal,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	qty, avg, value, err := replayHistoricalValue(db, companyID, itemID,
		&whIDVal, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !qty.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("qty: got %s want 5", qty)
	}
	if !avg.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("avg: got %s want 7 (legacy UnitCost fallback)", avg)
	}
	if !value.Equal(decimal.NewFromInt(35)) {
		t.Fatalf("value: got %s want 35", value)
	}
}
