// 遵循project_guide.md
package inventory

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// seedFIFOCompany flips the standard test fixture to FIFO costing so
// reconcile will run.
func seedFIFOCompany(t *testing.T, db *gorm.DB) (companyID, itemID, warehouseID uint) {
	t.Helper()
	companyID, itemID, warehouseID = seedTestFixture(t, db)
	if err := db.Model(&models.Company{}).Where("id = ?", companyID).
		Update("inventory_costing_method", models.InventoryCostingFIFO).Error; err != nil {
		t.Fatalf("set fifo: %v", err)
	}
	return companyID, itemID, warehouseID
}

// A FIFO company with normal post-E2.1 traffic has zero drift.
func TestInspectFIFOLayerDrift_FreshData_NoDrift(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedFIFOCompany(t, db)
	seedLayeredReceipts(t, db, companyID, itemID, warehouseID, [][2]int64{
		{10, 4}, {10, 6},
	})
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		CostingMethod: CostingMethodFIFO,
	}); err != nil {
		t.Fatalf("fresh issue: %v", err)
	}

	reports, err := InspectFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("InspectFIFOLayerDrift: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("expected zero drift on fresh data; got %+v", reports)
	}
}

// Inspect refuses to run against a moving-average company — drift is
// expected there by design, and the repair would be actively wrong.
func TestInspectFIFOLayerDrift_RejectsNonFIFOCompany(t *testing.T) {
	db := testDB(t)
	companyID, _, _ := seedTestFixture(t, db) // defaults to moving_average

	_, err := InspectFIFOLayerDrift(db, companyID)
	if err == nil {
		t.Fatalf("expected error for non-FIFO company")
	}
	if !strings.Contains(err.Error(), "only applies to FIFO") {
		t.Fatalf("error message unhelpful: %v", err)
	}
}

// Genesis case: positive on-hand, zero layer rows. Inspect reports the
// cell as eligible for auto-repair; Repair synthesizes a single layer
// at the balance's current average cost and brings drift to zero.
func TestRepairFIFOLayerDrift_GenesisNoLayers_SynthesizesLayer(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedFIFOCompany(t, db)

	// Seed a receipt so a balance and an inbound movement exist.
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	// Simulate a company that pre-dates the layer table by deleting the
	// layer row that ReceiveStock wrote. The balance still says 10 @ avg
	// 5; SUM(remaining) becomes 0; drift = +10 with zero layer rows.
	if err := db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Delete(&models.InventoryCostLayer{}).Error; err != nil {
		t.Fatalf("simulate legacy: %v", err)
	}

	// Inspect.
	reports, err := InspectFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 drift report, got %d (%+v)", len(reports), reports)
	}
	r := reports[0]
	if !r.Drift.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("drift: got %s want +10", r.Drift)
	}
	if r.LayerRowCount != 0 {
		t.Fatalf("LayerRowCount: got %d want 0", r.LayerRowCount)
	}
	if r.Repaired {
		t.Fatalf("Inspect must not claim Repaired")
	}
	if !strings.Contains(r.Notes, "genesis") {
		t.Fatalf("Notes should classify as genesis: %q", r.Notes)
	}

	// Repair.
	reports, err = RepairFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("repair report count: got %d want 1", len(reports))
	}
	if !reports[0].Repaired {
		t.Fatalf("expected Repaired=true; notes=%q", reports[0].Notes)
	}

	// Drift should now be zero on a fresh Inspect.
	reports, err = InspectFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("post-repair Inspect: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("expected zero drift after genesis repair; got %+v", reports)
	}

	// Verify the synthesized layer: one row, original=remaining=10, cost=5.
	var layers []models.InventoryCostLayer
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).Find(&layers)
	if len(layers) != 1 {
		t.Fatalf("expected 1 synthesized layer, got %d", len(layers))
	}
	if !layers[0].OriginalQuantity.Equal(decimal.NewFromInt(10)) ||
		!layers[0].RemainingQuantity.Equal(decimal.NewFromInt(10)) ||
		!layers[0].UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("genesis layer shape: got orig=%s rem=%s cost=%s",
			layers[0].OriginalQuantity, layers[0].RemainingQuantity, layers[0].UnitCostBase)
	}
}

// Positive drift with existing layers — e.g. a post-E2.1 reversal of a
// pre-E2.1 issue left on-hand up but layers stale. Inspect reports it
// clearly; Repair refuses to guess which layer to restore to and
// returns a human-actionable note.
func TestRepairFIFOLayerDrift_PositiveWithLayers_NotAutoRepaired(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedFIFOCompany(t, db)
	seedLayeredReceipts(t, db, companyID, itemID, warehouseID, [][2]int64{{10, 5}})

	// Manually create the drift: drain the layer (simulating a pre-E2.1
	// FIFO issue's layer effect) but leave on-hand at 10 (simulating a
	// later reversal restoring on-hand via snapshot cost). Drift = +7.
	if err := db.Model(&models.InventoryCostLayer{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID).
		Update("remaining_quantity", decimal.NewFromInt(3)).Error; err != nil {
		t.Fatalf("drain layer: %v", err)
	}

	reports, err := RepairFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("report count: got %d want 1", len(reports))
	}
	r := reports[0]
	if !r.Drift.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("drift: got %s want +7", r.Drift)
	}
	if r.Repaired {
		t.Fatalf("Repair must NOT auto-fix positive-with-layers case")
	}
	if !strings.Contains(r.Notes, "cannot auto-restore") {
		t.Fatalf("Notes should explain the refusal: %q", r.Notes)
	}

	// Layer state unchanged after Repair attempt.
	var layer models.InventoryCostLayer
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&layer)
	if !layer.RemainingQuantity.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("layer must not be mutated by refused repair: got %s", layer.RemainingQuantity)
	}
}

// Negative drift (layers > on-hand) surfaces in the report with a
// diagnostic note and is NOT auto-repaired.
func TestRepairFIFOLayerDrift_NegativeDrift_ReportedNotRepaired(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedFIFOCompany(t, db)
	seedLayeredReceipts(t, db, companyID, itemID, warehouseID, [][2]int64{{10, 5}})

	// Drain on-hand to simulate a double-reversal or hand-edit scenario.
	// Layer remaining stays at 10; on-hand drops to 3. Drift = -7.
	db.Model(&models.InventoryBalance{}).
		Where("company_id = ? AND item_id = ? AND warehouse_id = ?", companyID, itemID, warehouseID).
		Update("quantity_on_hand", decimal.NewFromInt(3))

	reports, err := RepairFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("report count: got %d want 1", len(reports))
	}
	r := reports[0]
	if r.Repaired {
		t.Fatalf("negative drift must not be auto-repaired")
	}
	if !r.Drift.Equal(decimal.NewFromInt(-7)) {
		t.Fatalf("drift: got %s want -7", r.Drift)
	}
	if !strings.Contains(r.Notes, "negative drift") {
		t.Fatalf("Notes should explain: %q", r.Notes)
	}
}

// H2: the synthesized genesis layer must declare its provenance so
// downstream readers (reports, audit, traceability) don't misattribute
// it to the anchor movement. The IsSynthetic/ProvenanceType fields are
// the authoritative markers; source_movement_id is FK anchor only.
func TestRepairFIFOLayerDrift_GenesisLayer_HasExplicitProvenance(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedFIFOCompany(t, db)

	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	// Simulate pre-migration-059 state: wipe the layer row ReceiveStock
	// wrote, leaving positive on-hand with zero layers.
	if err := db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Delete(&models.InventoryCostLayer{}).Error; err != nil {
		t.Fatalf("simulate legacy: %v", err)
	}

	if _, err := RepairFIFOLayerDrift(db, companyID); err != nil {
		t.Fatalf("Repair: %v", err)
	}

	var layers []models.InventoryCostLayer
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).Find(&layers)
	if len(layers) != 1 {
		t.Fatalf("expected 1 synthesized layer, got %d", len(layers))
	}
	l := layers[0]
	if !l.IsSynthetic {
		t.Fatalf("synthesized layer should set IsSynthetic=true")
	}
	if l.ProvenanceType != models.ProvenanceSyntheticGenesis {
		t.Fatalf("ProvenanceType: got %q want %q", l.ProvenanceType, models.ProvenanceSyntheticGenesis)
	}
	// SourceMovementID is populated (FK anchor) but its meaning is
	// *anchor only* — readers must consult ProvenanceType. We assert it's
	// non-zero (FK satisfied) without treating it as provenance.
	if l.SourceMovementID == 0 {
		t.Fatalf("SourceMovementID must be a valid FK anchor, even on synthetic rows")
	}
}

// H2: idempotency. A second Repair run on a cell already fixed by a
// first run must be a no-op — no new synthetic layer, no mutation of
// existing rows, drift stays at zero.
func TestRepairFIFOLayerDrift_GenesisRepair_Idempotent(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedFIFOCompany(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Delete(&models.InventoryCostLayer{})

	// First repair: synthesizes a layer.
	first, err := RepairFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("first repair: %v", err)
	}
	if len(first) != 1 || !first[0].Repaired {
		t.Fatalf("first repair should mark Repaired=true; got %+v", first)
	}

	// Snapshot post-repair state.
	var layersAfterFirst []models.InventoryCostLayer
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Order("id asc").Find(&layersAfterFirst)

	// Second repair: must be a no-op because drift is already zero.
	second, err := RepairFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second repair must return zero reports (drift already 0); got %+v", second)
	}

	// No extra synthetic layer, no mutation of existing row.
	var layersAfterSecond []models.InventoryCostLayer
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Order("id asc").Find(&layersAfterSecond)
	if len(layersAfterSecond) != len(layersAfterFirst) {
		t.Fatalf("second repair should not add layers: first=%d second=%d",
			len(layersAfterFirst), len(layersAfterSecond))
	}
	if len(layersAfterSecond) != 1 {
		t.Fatalf("expected exactly 1 synthetic layer after both repairs, got %d", len(layersAfterSecond))
	}
	if !layersAfterSecond[0].RemainingQuantity.Equal(layersAfterFirst[0].RemainingQuantity) {
		t.Fatalf("second repair mutated remaining: was %s, now %s",
			layersAfterFirst[0].RemainingQuantity, layersAfterSecond[0].RemainingQuantity)
	}
	if layersAfterSecond[0].ID != layersAfterFirst[0].ID {
		t.Fatalf("second repair replaced the synthetic layer: was id=%d, now id=%d",
			layersAfterFirst[0].ID, layersAfterSecond[0].ID)
	}
}

// H2: transaction rollback. When pickGenesisSourceMovement fails (no
// inbound movement exists), nothing about the cell changes — no layer
// created, balance untouched.
func TestRepairFIFOLayerDrift_GenesisRollback_NoPartialState(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedFIFOCompany(t, db)

	// Create a balance without any corresponding inbound movement.
	whVal := warehouseID
	bal := models.InventoryBalance{
		CompanyID:      companyID,
		ItemID:         itemID,
		WarehouseID:    &whVal,
		QuantityOnHand: decimal.NewFromInt(5),
		AverageCost:    decimal.NewFromInt(10),
	}
	if err := db.Create(&bal).Error; err != nil {
		t.Fatalf("seed orphan balance: %v", err)
	}

	reports, err := RepairFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("Repair (surfacing genesis failure): %v", err)
	}
	if len(reports) != 1 || reports[0].Repaired {
		t.Fatalf("expected 1 unrepaired report; got %+v", reports)
	}

	// State invariants: no layer created, balance unchanged.
	var layerCount int64
	db.Model(&models.InventoryCostLayer{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID).
		Count(&layerCount)
	if layerCount != 0 {
		t.Fatalf("genesis rollback must not leave layer rows: got %d", layerCount)
	}
	var reloaded models.InventoryBalance
	db.First(&reloaded, bal.ID)
	if !reloaded.QuantityOnHand.Equal(decimal.NewFromInt(5)) ||
		!reloaded.AverageCost.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("genesis rollback mutated the balance: qty=%s avg=%s",
			reloaded.QuantityOnHand, reloaded.AverageCost)
	}
}

// Genesis repair refuses to invent stock from nothing — if there are no
// inbound movements for the cell (data anomaly), Repair reports the
// failure instead of silently succeeding.
func TestRepairFIFOLayerDrift_GenesisWithoutInboundMovement_RefusesToFabricate(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedFIFOCompany(t, db)

	// Create a balance row with positive on-hand but NO movements and
	// NO layers. This is an impossible state for normal traffic; it
	// models a data anomaly that operators should see.
	whVal := warehouseID
	bal := models.InventoryBalance{
		CompanyID:      companyID,
		ItemID:         itemID,
		WarehouseID:    &whVal,
		QuantityOnHand: decimal.NewFromInt(5),
		AverageCost:    decimal.NewFromInt(10),
	}
	if err := db.Create(&bal).Error; err != nil {
		t.Fatalf("seed orphan balance: %v", err)
	}

	reports, err := RepairFIFOLayerDrift(db, companyID)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("report count: got %d want 1", len(reports))
	}
	if reports[0].Repaired {
		t.Fatalf("must NOT repair when no inbound movement exists")
	}
	if !strings.Contains(reports[0].Notes, "genesis repair failed") {
		t.Fatalf("Notes should explain failure: %q", reports[0].Notes)
	}
}
