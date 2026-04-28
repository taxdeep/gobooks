// 遵循project_guide.md
package inventory

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// GetOnHand returns a single row per (item, warehouse) with the right
// quantity, average cost, and derived value. Receipts, issues, and
// transfers should all be reflected.
func TestGetOnHand_ReflectsLedger(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	rows, err := GetOnHand(db, OnHandQuery{CompanyID: companyID})
	if err != nil {
		t.Fatalf("GetOnHand: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	r := rows[0]
	if !r.QuantityOnHand.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("QuantityOnHand: got %s want 7", r.QuantityOnHand)
	}
	if !r.AverageCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("AverageCostBase: got %s want 5", r.AverageCostBase)
	}
	if !r.TotalValueBase.Equal(decimal.NewFromInt(35)) {
		t.Fatalf("TotalValueBase: got %s want 35", r.TotalValueBase)
	}
}

// Zero balances are hidden by default; IncludeZero surfaces them.
func TestGetOnHand_ZeroHiddenByDefault(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	// Receive then fully issue → balance row has qty=0.
	seedReceive(t, db, companyID, itemID, warehouseID, 5, 10)
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	rows, err := GetOnHand(db, OnHandQuery{CompanyID: companyID})
	if err != nil {
		t.Fatalf("GetOnHand: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("zero-qty row shown when IncludeZero=false: got %d rows", len(rows))
	}

	rows, err = GetOnHand(db, OnHandQuery{CompanyID: companyID, IncludeZero: true})
	if err != nil {
		t.Fatalf("GetOnHand IncludeZero: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("IncludeZero=true: got %d rows want 1", len(rows))
	}
}

// Invariant: SUM(QuantityDelta) for (item, warehouse) == balance.QuantityOnHand.
// This is the core consistency guarantee of the ledger — violations mean
// something wrote past the API. Run the check after a mixed workload.
func TestInvariant_SumDeltaEqualsBalance(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	seedReceive(t, db, companyID, itemID, warehouseID, 20, 5)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 8)
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(7), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := AdjustStock(db, AdjustStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		QuantityDelta: decimal.NewFromInt(-2), MovementDate: time.Now(),
		Reason: AdjustmentReasonDamage,
	}); err != nil {
		t.Fatalf("adjust: %v", err)
	}

	// Sum deltas from the ledger.
	var deltaSum decimal.Decimal
	type r struct {
		S decimal.Decimal
	}
	var out r
	db.Model(&models.InventoryMovement{}).
		Select("COALESCE(SUM(quantity_delta), 0) AS s").
		Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
			companyID, itemID, warehouseID).
		Scan(&out)
	deltaSum = out.S

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)

	if !deltaSum.Equal(bal.QuantityOnHand) {
		t.Fatalf("invariant violated: SUM(delta)=%s balance=%s",
			deltaSum, bal.QuantityOnHand)
	}
}

// GetMovements with direction=out returns only negative-delta rows.
func TestGetMovements_DirectionFilter(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	out := MovementDirectionOut
	rows, _, err := GetMovements(db, MovementQuery{
		CompanyID: companyID, Direction: &out,
	})
	if err != nil {
		t.Fatalf("GetMovements: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	if !rows[0].QuantityDelta.IsNegative() {
		t.Fatalf("direction=out returned a non-negative row: %s", rows[0].QuantityDelta)
	}
}

// GetMovements running-balance: the cumulative quantity after the last
// movement matches the live InventoryBalance.
func TestGetMovements_RunningBalanceMatchesLive(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	seedReceive(t, db, companyID, itemID, warehouseID, 5, 7)
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(4), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	rows, _, err := GetMovements(db, MovementQuery{
		CompanyID: companyID, ItemID: &itemID, IncludeRunningBalance: true,
	})
	if err != nil {
		t.Fatalf("GetMovements: %v", err)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)

	if !rows[len(rows)-1].RunningQuantity.Equal(bal.QuantityOnHand) {
		t.Fatalf("running balance != live balance: got %s want %s",
			rows[len(rows)-1].RunningQuantity, bal.QuantityOnHand)
	}
}

// GetItemLedger period totals match the sum of inbound / outbound
// movements in the window.
func TestGetItemLedger_PeriodTotals(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	// Movements within the window.
	now := time.Now()
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(4), MovementDate: now,
		SourceType: "invoice", SourceID: 1,
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	report, err := GetItemLedger(db, ItemLedgerQuery{
		CompanyID: companyID,
		ItemID:    itemID,
		FromDate:  now.Add(-24 * time.Hour),
		ToDate:    now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetItemLedger: %v", err)
	}
	if !report.TotalInQty.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("TotalInQty: got %s want 10", report.TotalInQty)
	}
	if !report.TotalOutQty.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("TotalOutQty: got %s want 4", report.TotalOutQty)
	}
	// CostOfSales over the window: 4 × 5 = 20
	if !report.TotalOutCostBase.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("TotalOutCostBase: got %s want 20", report.TotalOutCostBase)
	}
	if !report.ClosingQuantity.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("ClosingQuantity: got %s want 6", report.ClosingQuantity)
	}
}

// GetValuationSnapshot sums all non-zero balances; grand total should
// match Σ(qty × avg).
func TestGetValuationSnapshot_GrandTotalMatches(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5) // value 50
	// Mixed unit cost — receive 4 @ $7.50 to land at a non-integer avg.
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(4), MovementDate: time.Now(),
		UnitCost: decimal.NewFromFloat(7.5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 99,
	}); err != nil {
		t.Fatalf("second receive: %v", err)
	}

	rows, grand, err := GetValuationSnapshot(db, ValuationQuery{
		CompanyID: companyID, GroupBy: ValuationGroupByItem,
	})
	if err != nil {
		t.Fatalf("GetValuationSnapshot: %v", err)
	}
	// Grand total = 14 × 5.7143 = 80.0002 → rounded to 80.00
	if !grand.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("grand total: got %s want 80", grand)
	}
	// One group keyed by item.
	if len(rows) != 1 {
		t.Fatalf("groups: got %d want 1", len(rows))
	}
	if !rows[0].Quantity.Equal(decimal.NewFromInt(14)) {
		t.Fatalf("group qty: got %s want 14", rows[0].Quantity)
	}
}

// GetCostingPreview reports feasibility and the base-currency cost without
// mutating anything.
func TestGetCostingPreview_FeasibleAndInfeasible(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 5, 10)

	feas, err := GetCostingPreview(db, CostingPreviewQuery{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3),
	})
	if err != nil {
		t.Fatalf("feasible preview: %v", err)
	}
	if !feas.Feasible {
		t.Fatalf("expected feasible, got infeasible")
	}
	if !feas.TotalCostBase.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("TotalCostBase: got %s want 30", feas.TotalCostBase)
	}

	infeas, err := GetCostingPreview(db, CostingPreviewQuery{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10),
	})
	if err != nil {
		t.Fatalf("infeasible preview: %v", err)
	}
	if infeas.Feasible {
		t.Fatalf("expected infeasible, got feasible")
	}
	if !infeas.ShortBy.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("ShortBy: got %s want 5", infeas.ShortBy)
	}

	// No balance state was mutated.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("preview mutated balance: %s", bal.QuantityOnHand)
	}
}

// (BOM stub test removed — both ExplodeBOM and GetAvailableForBuild are
// fully implemented in Phase D.1; see bom_test.go for their real coverage.)
