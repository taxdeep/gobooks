// 遵循project_guide.md
package inventory

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// ── Lot inquiry ──────────────────────────────────────────────────────────────

// GetLotsForItem returns lots in FIFO order, excluding drained ones by
// default.
func TestGetLotsForItem_FIFOOrderExcludesDrained(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	recv := func(qty int64, lot string, dayOffset int, sid uint) {
		if _, err := ReceiveStock(db, ReceiveStockInput{
			CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
			Quantity: decimal.NewFromInt(qty), MovementDate: base.AddDate(0, 0, dayOffset),
			UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
			SourceType: "bill", SourceID: sid, LotNumber: lot,
		}); err != nil {
			t.Fatalf("seed %s: %v", lot, err)
		}
	}
	recv(10, "LOT-A", 0, 1) // oldest
	recv(10, "LOT-B", 1, 2)
	recv(10, "LOT-C", 2, 3) // newest

	// Issue all of LOT-B so its remaining drops to 0.
	var lotB models.InventoryLot
	db.Where("lot_number = ?", "LOT-B").First(&lotB)
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 99,
		LotSelections: []LotSelection{{LotID: lotB.ID, Quantity: decimal.NewFromInt(10)}},
	}); err != nil {
		t.Fatalf("drain B: %v", err)
	}

	// Default: excludes drained.
	rows, err := GetLotsForItem(db, companyID, itemID, false)
	if err != nil {
		t.Fatalf("GetLotsForItem: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 live lots (A,C), got %d", len(rows))
	}
	if rows[0].LotNumber != "LOT-A" || rows[1].LotNumber != "LOT-C" {
		t.Fatalf("FIFO order broken: %q %q", rows[0].LotNumber, rows[1].LotNumber)
	}

	// includeZero=true returns all three.
	rowsAll, err := GetLotsForItem(db, companyID, itemID, true)
	if err != nil {
		t.Fatalf("GetLotsForItem includeZero: %v", err)
	}
	if len(rowsAll) != 3 {
		t.Fatalf("includeZero: got %d want 3", len(rowsAll))
	}
}

// ── Serial inquiry ───────────────────────────────────────────────────────────

// GetSerialsForItem with a state filter returns only those serials.
func TestGetSerialsForItem_FilterByState(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers: []string{"SN-1", "SN-2", "SN-3"},
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	// Issue SN-2 so it's in state=issued.
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 5,
		SerialSelections: []string{"SN-2"},
	}); err != nil {
		t.Fatalf("issue SN-2: %v", err)
	}

	// Filter: on_hand only.
	onHand, err := GetSerialsForItem(db, companyID, itemID, []models.SerialState{models.SerialStateOnHand})
	if err != nil {
		t.Fatalf("GetSerialsForItem on_hand: %v", err)
	}
	if len(onHand) != 2 {
		t.Fatalf("on_hand count: got %d want 2", len(onHand))
	}
	// Filter: issued only.
	issued, err := GetSerialsForItem(db, companyID, itemID, []models.SerialState{models.SerialStateIssued})
	if err != nil {
		t.Fatalf("GetSerialsForItem issued: %v", err)
	}
	if len(issued) != 1 || issued[0].SerialNumber != "SN-2" {
		t.Fatalf("issued: %+v", issued)
	}
	// No filter: all 3.
	all, err := GetSerialsForItem(db, companyID, itemID, nil)
	if err != nil {
		t.Fatalf("GetSerialsForItem all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all count: got %d want 3", len(all))
	}
}

// ── Traceability ─────────────────────────────────────────────────────────────

// GetTracesForMovement returns every anchor tied to the outbound movement,
// enriched with lot_number / serial_number.
func TestGetTracesForMovement_EnrichesLotAndSerialNames(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-TRACE",
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	var lot models.InventoryLot
	db.Where("lot_number = ?", "LOT-TRACE").First(&lot)

	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(4), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 100,
		LotSelections: []LotSelection{{LotID: lot.ID, Quantity: decimal.NewFromInt(4)}},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	traces, err := GetTracesForMovement(db, companyID, issue.MovementID)
	if err != nil {
		t.Fatalf("GetTracesForMovement: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("traces: got %d want 1", len(traces))
	}
	if traces[0].LotNumber != "LOT-TRACE" {
		t.Fatalf("lot_number enrichment missing: %+v", traces[0])
	}
	if !traces[0].QuantityDrawn.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("qty_drawn: got %s want 4", traces[0].QuantityDrawn)
	}
	if traces[0].ReversedByMovementID != nil {
		t.Fatalf("fresh trace should not be reversed")
	}
}

// Reversed anchors still appear in traces, with ReversedByMovementID set
// so auditors see full history.
func TestGetTracesForMovement_ReversedAnchorsStillListed(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers: []string{"SN-TR"},
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		SerialSelections: []string{"SN-TR"},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: issue.MovementID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           1,
	}); err != nil {
		t.Fatalf("reverse: %v", err)
	}

	traces, err := GetTracesForMovement(db, companyID, issue.MovementID)
	if err != nil {
		t.Fatalf("GetTracesForMovement: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("traces: got %d want 1", len(traces))
	}
	if traces[0].ReversedByMovementID == nil {
		t.Fatalf("reversed anchor should carry ReversedByMovementID")
	}
	if traces[0].SerialNumber != "SN-TR" {
		t.Fatalf("serial enrichment: %q", traces[0].SerialNumber)
	}
}

// Company isolation on traces.
func TestGetTracesForMovement_CompanyIsolation(t *testing.T) {
	db := testDB(t)

	// Company A with a lot issue.
	companyA, itemA, whA := seedTrackedItem(t, db, models.TrackingLot)
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyA, ItemID: itemA, WarehouseID: whA,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-ISO",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var lotA models.InventoryLot
	db.First(&lotA)
	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyA, ItemID: itemA, WarehouseID: whA,
		Quantity: decimal.NewFromInt(2), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		LotSelections: []LotSelection{{LotID: lotA.ID, Quantity: decimal.NewFromInt(2)}},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Unrelated company tries to look up A's movement.
	companyB := uint(99999)
	traces, err := GetTracesForMovement(db, companyB, issue.MovementID)
	if err != nil {
		t.Fatalf("GetTracesForMovement cross-company: %v", err)
	}
	if len(traces) != 0 {
		t.Fatalf("cross-company trace leaked %d rows", len(traces))
	}
}

// ── Expiry visibility ────────────────────────────────────────────────────────

// GetExpiringLots returns lots expiring within N days, ordered by
// expiry ASC. Lots with NULL expiry or already-drained are excluded.
func TestGetExpiringLots_OrdersByExpiryExcludesDrainedAndNull(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)
	today := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	makeLot := func(lot string, expiry *time.Time, qty int64, sid uint) {
		if _, err := ReceiveStock(db, ReceiveStockInput{
			CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
			Quantity: decimal.NewFromInt(qty), MovementDate: today,
			UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
			SourceType: "bill", SourceID: sid,
			LotNumber: lot, ExpiryDate: expiry,
		}); err != nil {
			t.Fatalf("seed %s: %v", lot, err)
		}
	}

	in10 := today.AddDate(0, 0, 10)
	in30 := today.AddDate(0, 0, 30)
	in90 := today.AddDate(0, 0, 90)
	past := today.AddDate(0, 0, -5)

	makeLot("LOT-PAST", &past, 2, 1)          // expired
	makeLot("LOT-10D", &in10, 4, 2)           // expiring soon
	makeLot("LOT-30D", &in30, 4, 3)           // within 30d window
	makeLot("LOT-90D", &in90, 4, 4)           // outside 30d window
	makeLot("LOT-NO-EXPIRY", nil, 4, 5)       // no expiry — excluded

	rows, err := GetExpiringLots(db, companyID, today, 30)
	if err != nil {
		t.Fatalf("GetExpiringLots: %v", err)
	}
	// Expect: LOT-PAST (-5 days), LOT-10D (+10), LOT-30D (+30). In that order.
	if len(rows) != 3 {
		t.Fatalf("rows: got %d want 3 (%+v)", len(rows), rows)
	}
	if rows[0].LotNumber != "LOT-PAST" ||
		rows[1].LotNumber != "LOT-10D" ||
		rows[2].LotNumber != "LOT-30D" {
		t.Fatalf("order broken: %q %q %q",
			rows[0].LotNumber, rows[1].LotNumber, rows[2].LotNumber)
	}
	// Expired lot reports negative DaysUntilExpiry.
	if rows[0].DaysUntilExpiry > 0 {
		t.Fatalf("already-expired days: got %d want <=0", rows[0].DaysUntilExpiry)
	}
}

// Drained lots (remaining=0) are not reported as expiring even if
// their expiry_date falls in the window.
func TestGetExpiringLots_ExcludesDrainedLots(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)
	today := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	exp := today.AddDate(0, 0, 5)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: today,
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-GONE", ExpiryDate: &exp,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var lot models.InventoryLot
	db.Where("lot_number = ?", "LOT-GONE").First(&lot)
	// Drain it.
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: today,
		SourceType: "invoice", SourceID: 1,
		LotSelections: []LotSelection{{LotID: lot.ID, Quantity: decimal.NewFromInt(5)}},
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}

	rows, err := GetExpiringLots(db, companyID, today, 30)
	if err != nil {
		t.Fatalf("GetExpiringLots: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("drained lot leaked into expiring list: %+v", rows)
	}
}

// GetExpiringSerials returns only live-state serials (on_hand, reserved)
// in the window. Issued and void_archived serials are excluded.
func TestGetExpiringSerials_LiveStatesOnly(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)
	today := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	soon := today.AddDate(0, 0, 10)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(2), MovementDate: today,
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers:     []string{"SN-ALIVE", "SN-GOING-OUT"},
		SerialExpiryDates: []*time.Time{&soon, &soon},
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}

	// Issue SN-GOING-OUT so it transitions out of live state.
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: today,
		SourceType: "invoice", SourceID: 1,
		SerialSelections: []string{"SN-GOING-OUT"},
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	rows, err := GetExpiringSerials(db, companyID, today, 30)
	if err != nil {
		t.Fatalf("GetExpiringSerials: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1 (only SN-ALIVE)", len(rows))
	}
	if rows[0].SerialNumber != "SN-ALIVE" {
		t.Fatalf("wrong serial: %s", rows[0].SerialNumber)
	}
}

// Company isolation on expiring queries.
func TestGetExpiringLots_CompanyIsolation(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)
	today := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	soon := today.AddDate(0, 0, 5)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: today,
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-X", ExpiryDate: &soon,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Wrong company looks up: must get 0 rows.
	rows, err := GetExpiringLots(db, companyID+9999, today, 30)
	if err != nil {
		t.Fatalf("GetExpiringLots other-company: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("cross-company leak: %+v", rows)
	}
}
