// 遵循project_guide.md
package inventory

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// seedTrackedItem flips the standard fixture's product to a specific
// tracking mode. Returns the same IDs as seedTestFixture.
func seedTrackedItem(t *testing.T, db *gorm.DB, mode string) (companyID, itemID, warehouseID uint) {
	t.Helper()
	companyID, itemID, warehouseID = seedTestFixture(t, db)
	if err := db.Model(&models.ProductService{}).
		Where("id = ?", itemID).
		Update("tracking_mode", mode).Error; err != nil {
		t.Fatalf("set tracking_mode=%q: %v", mode, err)
	}
	return
}

// ── Lot inbound ──────────────────────────────────────────────────────────────

// Lot happy path: first receive creates a lot row with original=remaining=qty.
func TestReceiveStock_LotTracked_FirstReceiptCreatesLot(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)
	expiry := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity:     decimal.NewFromInt(10),
		MovementDate: time.Now(),
		UnitCost:     decimal.NewFromInt(5),
		ExchangeRate: decimal.NewFromInt(1),
		SourceType:   "bill", SourceID: 1,
		LotNumber:  "LOT-A",
		ExpiryDate: &expiry,
	})
	if err != nil {
		t.Fatalf("ReceiveStock: %v", err)
	}

	var lots []models.InventoryLot
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).Find(&lots)
	if len(lots) != 1 {
		t.Fatalf("lots: got %d want 1", len(lots))
	}
	l := lots[0]
	if l.LotNumber != "LOT-A" ||
		!l.OriginalQuantity.Equal(decimal.NewFromInt(10)) ||
		!l.RemainingQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("lot shape: %+v", l)
	}
	if l.ExpiryDate == nil || !l.ExpiryDate.Equal(expiry) {
		t.Fatalf("expiry: got %v want %v", l.ExpiryDate, expiry)
	}
}

// Lot top-up: second receive of the same lot adds to both original and
// remaining rather than creating a second row.
func TestReceiveStock_LotTracked_SecondReceiptTopsUp(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	in := func(qty int64, sid uint) ReceiveStockInput {
		return ReceiveStockInput{
			CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
			Quantity: decimal.NewFromInt(qty), MovementDate: time.Now(),
			UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
			SourceType: "bill", SourceID: sid, LotNumber: "LOT-TOPUP",
		}
	}
	if _, err := ReceiveStock(db, in(4, 1)); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := ReceiveStock(db, in(6, 2)); err != nil {
		t.Fatalf("second: %v", err)
	}

	var lots []models.InventoryLot
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).Find(&lots)
	if len(lots) != 1 {
		t.Fatalf("top-up should NOT create a second row: got %d", len(lots))
	}
	if !lots[0].OriginalQuantity.Equal(decimal.NewFromInt(10)) ||
		!lots[0].RemainingQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("top-up math: orig=%s rem=%s want 10/10",
			lots[0].OriginalQuantity, lots[0].RemainingQuantity)
	}
}

// Lot expiry mismatch on top-up is rejected: reusing a lot number for
// units with a different shelf life is a data-integrity issue.
func TestReceiveStock_LotTracked_ExpiryMismatchOnTopUpRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)
	e1 := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)
	e2 := time.Date(2027, 7, 15, 0, 0, 0, 0, time.UTC)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-X", ExpiryDate: &e1,
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 2,
		LotNumber: "LOT-X", ExpiryDate: &e2,
	})
	if err == nil {
		t.Fatalf("expected expiry mismatch error on top-up")
	}
}

// Lot-tracked missing LotNumber is rejected.
func TestReceiveStock_LotTracked_MissingLotRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		// No LotNumber
	})
	if !errors.Is(err, ErrTrackingDataMissing) {
		t.Fatalf("got %v, want ErrTrackingDataMissing", err)
	}
}

// ── Serial inbound ───────────────────────────────────────────────────────────

// Serial happy path: N serials recorded, all on_hand.
func TestReceiveStock_SerialTracked_CreatesOnHandUnits(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)
	e := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers:     []string{"SN-A", "SN-B", "SN-C"},
		SerialExpiryDates: []*time.Time{&e, nil, &e}, // nullable per-serial
	})
	if err != nil {
		t.Fatalf("ReceiveStock: %v", err)
	}

	var units []models.InventorySerialUnit
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Order("id asc").Find(&units)
	if len(units) != 3 {
		t.Fatalf("serial units: got %d want 3", len(units))
	}
	for _, u := range units {
		if u.CurrentState != models.SerialStateOnHand {
			t.Fatalf("serial %s: state=%s want on_hand", u.SerialNumber, u.CurrentState)
		}
	}
	if units[0].ExpiryDate == nil || !units[0].ExpiryDate.Equal(e) {
		t.Fatalf("SN-A expiry: %v", units[0].ExpiryDate)
	}
	if units[1].ExpiryDate != nil {
		t.Fatalf("SN-B expiry: should be nil, got %v", units[1].ExpiryDate)
	}
}

// Count mismatch: qty != len(serial_numbers) is rejected.
func TestReceiveStock_SerialTracked_CountMismatchRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers: []string{"SN-1", "SN-2"}, // only 2 for qty=3
	})
	if !errors.Is(err, ErrSerialCountMismatch) {
		t.Fatalf("got %v, want ErrSerialCountMismatch", err)
	}
}

// Duplicate live serial: receiving the same serial twice (still on-hand
// from the first receipt) is rejected.
func TestReceiveStock_SerialTracked_DuplicateLiveSerialRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	in := func(sid uint) ReceiveStockInput {
		return ReceiveStockInput{
			CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
			Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
			UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
			SourceType: "bill", SourceID: sid,
			SerialNumbers: []string{"SN-DUP"},
		}
	}
	if _, err := ReceiveStock(db, in(1)); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := ReceiveStock(db, in(2))
	if !errors.Is(err, ErrDuplicateSerialInbound) {
		t.Fatalf("got %v, want ErrDuplicateSerialInbound", err)
	}
}

// Missing serial list on serial-tracked item is rejected.
func TestReceiveStock_SerialTracked_MissingSerialsRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		// No SerialNumbers
	})
	if !errors.Is(err, ErrTrackingDataMissing) {
		t.Fatalf("got %v, want ErrTrackingDataMissing", err)
	}
}

// Mode mismatch: serial data for a lot-tracked item is rejected.
func TestReceiveStock_LotTracked_SerialDataMismatchRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber:     "LOT-X",
		SerialNumbers: []string{"SN-WRONG"}, // mixing modes
	})
	if !errors.Is(err, ErrTrackingModeMismatch) {
		t.Fatalf("got %v, want ErrTrackingModeMismatch", err)
	}
}

// ── Untracked item backward compat ───────────────────────────────────────────

// Untracked item receiving LOT data is rejected loudly (not silently
// dropped). Defense against misconfiguration that would otherwise lose
// provenance.
func TestReceiveStock_Untracked_LotDataSuppliedRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db) // default tracking=none

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "SNUCK-IN",
	})
	if !errors.Is(err, ErrTrackingDataOnUntrackedItem) {
		t.Fatalf("got %v, want ErrTrackingDataOnUntrackedItem", err)
	}
}

// Untracked item plain receive (no tracking data) still works —
// regression gate for the 95% of legacy callers.
func TestReceiveStock_Untracked_WithoutTrackingData_StillWorks(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
	})
	if err != nil {
		t.Fatalf("untracked plain receipt: %v", err)
	}
	// No lot or serial rows created.
	var lotCount, unitCount int64
	db.Model(&models.InventoryLot{}).Where("company_id = ?", companyID).Count(&lotCount)
	db.Model(&models.InventorySerialUnit{}).Where("company_id = ?", companyID).Count(&unitCount)
	if lotCount != 0 || unitCount != 0 {
		t.Fatalf("untracked receipt must not write lot/serial rows: lots=%d units=%d",
			lotCount, unitCount)
	}
}

// Company isolation: two companies receiving the same serial number is fine.
func TestReceiveStock_SerialTracked_CompanyIsolationAllowsSameSerial(t *testing.T) {
	db := testDB(t)

	// Company A with a serial-tracked item.
	companyA, itemA, whA := seedTrackedItem(t, db, models.TrackingSerial)

	// Company B with its own serial-tracked item.
	c2 := models.Company{Name: "Co2", IsActive: true}
	db.Create(&c2)
	wh2 := models.Warehouse{CompanyID: c2.ID, Name: "Main", IsActive: true}
	db.Create(&wh2)
	item2 := models.ProductService{
		CompanyID: c2.ID, Name: "Widget2",
		Type: models.ProductServiceTypeInventory, IsStockItem: true, IsActive: true,
		TrackingMode: models.TrackingSerial,
	}
	db.Create(&item2)

	in := func(cid, iid, wid uint, sid uint) ReceiveStockInput {
		return ReceiveStockInput{
			CompanyID: cid, ItemID: iid, WarehouseID: wid,
			Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
			UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
			SourceType: "bill", SourceID: sid,
			SerialNumbers: []string{"SN-SHARED"},
		}
	}
	if _, err := ReceiveStock(db, in(companyA, itemA, whA, 1)); err != nil {
		t.Fatalf("A: %v", err)
	}
	if _, err := ReceiveStock(db, in(c2.ID, item2.ID, wh2.ID, 1)); err != nil {
		t.Fatalf("B (same serial): %v", err)
	}

	// Each company has its own row.
	var count int64
	db.Model(&models.InventorySerialUnit{}).
		Where("serial_number = ? AND current_state = ?", "SN-SHARED", models.SerialStateOnHand).
		Count(&count)
	if count != 2 {
		t.Fatalf("cross-company isolation: got %d rows want 2", count)
	}
}
