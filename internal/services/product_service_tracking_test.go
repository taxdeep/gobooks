// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"gobooks/internal/models"
)

func testTrackingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:ptrack_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.ProductService{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.AuditLog{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// Seeding helpers — kept minimal so each test reads top-down.

func seedTrackingCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{
		Name:            "TC",
		IsActive:        true,
		TrackingEnabled: true, // F1 tests test the per-item guard, not the capability gate
	}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	return co.ID
}

// seedTrackingCompanyGateOff creates a company with tracking capability
// left at its default (FALSE). Used by the G.1 gate-specific tests that
// must see the gate reject ChangeTrackingMode before reaching item-level
// checks.
func seedTrackingCompanyGateOff(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "TC-gate-off", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	return co.ID
}

func seedRevenueAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              "4000",
		Name:              "Revenue",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: "sales_revenue",
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("seed revenue: %v", err)
	}
	return a.ID
}

func seedStockItem(t *testing.T, db *gorm.DB, companyID uint, name string) *models.ProductService {
	t.Helper()
	revID := seedRevenueAccount(t, db, companyID)
	item := models.ProductService{
		CompanyID:        companyID,
		Name:             name,
		Type:             models.ProductServiceTypeInventory,
		RevenueAccountID: revID,
		IsActive:         true,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed stock item: %v", err)
	}
	return &item
}

func seedTrackingServiceItem(t *testing.T, db *gorm.DB, companyID uint, name string) *models.ProductService {
	t.Helper()
	revID := seedRevenueAccount(t, db, companyID)
	item := models.ProductService{
		CompanyID:        companyID,
		Name:             name,
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revID,
		IsActive:         true,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed service item: %v", err)
	}
	return &item
}

// ApplyTypeDefaults forces tracking_mode to "none" for non-stock items
// regardless of what the caller pre-set. Stock items default to "none"
// too (explicit flip happens via ChangeTrackingMode).
func TestApplyTypeDefaults_NonStockForcedToNone(t *testing.T) {
	svc := &models.ProductService{
		Type:         models.ProductServiceTypeService,
		TrackingMode: models.TrackingLot, // pretend a caller tried to sneak this in
	}
	svc.ApplyTypeDefaults()
	if svc.TrackingMode != models.TrackingNone {
		t.Fatalf("service item tracking_mode: got %q want %q", svc.TrackingMode, models.TrackingNone)
	}

	nonInv := &models.ProductService{
		Type:         models.ProductServiceTypeNonInventory,
		TrackingMode: models.TrackingSerial,
	}
	nonInv.ApplyTypeDefaults()
	if nonInv.TrackingMode != models.TrackingNone {
		t.Fatalf("non-inventory tracking_mode: got %q want %q", nonInv.TrackingMode, models.TrackingNone)
	}

	otherCharge := &models.ProductService{
		Type:         models.ProductServiceTypeOtherCharge,
		TrackingMode: models.TrackingLot,
	}
	otherCharge.ApplyTypeDefaults()
	if otherCharge.TrackingMode != models.TrackingNone {
		t.Fatalf("other-charge tracking_mode: got %q want %q", otherCharge.TrackingMode, models.TrackingNone)
	}
}

// ValidateTrackingMode rejects lot/serial on non-stock items at the
// model layer. This is the layer just above the DB CHECK constraint and
// catches bad values before any INSERT happens.
func TestValidateTrackingMode_NonStockRejectsLotSerial(t *testing.T) {
	svc := &models.ProductService{
		Name:         "Consulting",
		Type:         models.ProductServiceTypeService,
		IsStockItem:  false,
		TrackingMode: models.TrackingLot,
	}
	if err := svc.ValidateTrackingMode(); err == nil {
		t.Fatalf("non-stock + lot: expected error")
	}

	svc.TrackingMode = models.TrackingSerial
	if err := svc.ValidateTrackingMode(); err == nil {
		t.Fatalf("non-stock + serial: expected error")
	}

	svc.TrackingMode = models.TrackingNone
	if err := svc.ValidateTrackingMode(); err != nil {
		t.Fatalf("non-stock + none: expected ok, got %v", err)
	}
}

// ValidateTrackingMode accepts all three modes on stock items.
func TestValidateTrackingMode_StockItemAcceptsAllModes(t *testing.T) {
	for _, mode := range []string{models.TrackingNone, models.TrackingLot, models.TrackingSerial} {
		item := &models.ProductService{
			Name:         "Widget",
			Type:         models.ProductServiceTypeInventory,
			IsStockItem:  true,
			TrackingMode: mode,
		}
		if err := item.ValidateTrackingMode(); err != nil {
			t.Fatalf("stock + %q: expected ok, got %v", mode, err)
		}
	}
}

// ValidateTrackingMode rejects unknown string values.
func TestValidateTrackingMode_UnknownValueRejected(t *testing.T) {
	item := &models.ProductService{
		Name:         "Widget",
		Type:         models.ProductServiceTypeInventory,
		IsStockItem:  true,
		TrackingMode: "fifo", // accidentally wrote a costing method here
	}
	if err := item.ValidateTrackingMode(); err == nil {
		t.Fatalf("unknown mode: expected error")
	}
}

// Happy path for ChangeTrackingMode: stock item with zero stock flips
// from none to lot; audit log captures the before/after.
func TestChangeTrackingMode_StockItemNoStock_Succeeds(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompany(t, db)
	item := seedStockItem(t, db, cid, "Widget")

	err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid,
		ItemID:    item.ID,
		NewMode:   models.TrackingLot,
		Actor:     "ops@test",
	})
	if err != nil {
		t.Fatalf("ChangeTrackingMode: %v", err)
	}

	var reloaded models.ProductService
	db.First(&reloaded, item.ID)
	if reloaded.TrackingMode != models.TrackingLot {
		t.Fatalf("tracking_mode after change: got %q want %q", reloaded.TrackingMode, models.TrackingLot)
	}

	// Audit log written.
	var logs []models.AuditLog
	db.Where("entity_type = ? AND entity_id = ? AND action = ?",
		"product_service", item.ID, "product_service.tracking_mode.changed").
		Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("audit rows: got %d want 1", len(logs))
	}
	if logs[0].Actor != "ops@test" {
		t.Fatalf("audit actor: got %q", logs[0].Actor)
	}
}

// Guard: non-stock items cannot be flipped to lot/serial via the
// service path either — layered defence on top of ValidateTrackingMode.
func TestChangeTrackingMode_NonStockRejected(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompany(t, db)
	svc := seedTrackingServiceItem(t, db, cid, "Consulting")

	err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid,
		ItemID:    svc.ID,
		NewMode:   models.TrackingLot,
	})
	if !errors.Is(err, ErrTrackingModeInvalidForItem) {
		t.Fatalf("got %v, want ErrTrackingModeInvalidForItem", err)
	}

	// tracking_mode unchanged.
	var reloaded models.ProductService
	db.First(&reloaded, svc.ID)
	if reloaded.TrackingMode != models.TrackingNone {
		t.Fatalf("service item tracking_mode mutated: %q", reloaded.TrackingMode)
	}
}

// Guard: on-hand > 0 blocks the flip. Phase F1 does not ship a
// conversion tool; operators must drain stock first.
func TestChangeTrackingMode_BlockedByOnHand(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompany(t, db)
	item := seedStockItem(t, db, cid, "Widget")

	// Seed a non-zero on-hand balance.
	bal := models.InventoryBalance{
		CompanyID:      cid,
		ItemID:         item.ID,
		QuantityOnHand: decimal.NewFromInt(5),
		AverageCost:    decimal.NewFromInt(10),
	}
	db.Create(&bal)

	err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid,
		ItemID:    item.ID,
		NewMode:   models.TrackingLot,
	})
	if !errors.Is(err, ErrTrackingModeHasStock) {
		t.Fatalf("got %v, want ErrTrackingModeHasStock", err)
	}

	var reloaded models.ProductService
	db.First(&reloaded, item.ID)
	if reloaded.TrackingMode != models.TrackingNone {
		t.Fatalf("tracking_mode should not change when on-hand present: got %q", reloaded.TrackingMode)
	}
}

// Guard: positive layer.remaining_quantity also blocks the flip (matters
// for FIFO companies whose on-hand might be zero after issues but whose
// layer inventory still has committed cost cover).
func TestChangeTrackingMode_BlockedByLayerRemaining(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompany(t, db)
	item := seedStockItem(t, db, cid, "Widget")

	// Zero on-hand balance (drained) but a layer with remaining — an
	// edge case that would leak if we only checked on-hand.
	db.Create(&models.InventoryBalance{
		CompanyID: cid, ItemID: item.ID,
		QuantityOnHand: decimal.Zero, AverageCost: decimal.Zero,
	})
	db.Create(&models.InventoryCostLayer{
		CompanyID: cid, ItemID: item.ID,
		SourceMovementID: 1, // FK anchor; test does not actually reference it
		OriginalQuantity: decimal.NewFromInt(3),
		RemainingQuantity: decimal.NewFromInt(3),
		UnitCostBase:     decimal.NewFromInt(5),
		ReceivedDate:     time.Now(),
	})

	err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid,
		ItemID:    item.ID,
		NewMode:   models.TrackingSerial,
	})
	if !errors.Is(err, ErrTrackingModeHasStock) {
		t.Fatalf("got %v, want ErrTrackingModeHasStock", err)
	}
}

// A no-op change (NewMode == current) succeeds silently and does NOT
// write an audit row (nothing changed).
func TestChangeTrackingMode_NoOpWhenSameMode(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompany(t, db)
	item := seedStockItem(t, db, cid, "Widget")

	err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid,
		ItemID:    item.ID,
		NewMode:   models.TrackingNone, // already is none
	})
	if err != nil {
		t.Fatalf("no-op change: got error %v", err)
	}

	var logs int64
	db.Model(&models.AuditLog{}).
		Where("entity_type = ? AND action = ?",
			"product_service", "product_service.tracking_mode.changed").
		Count(&logs)
	if logs != 0 {
		t.Fatalf("no-op must not write audit: got %d rows", logs)
	}
}

// ── Phase G slice G.1 — company tracking capability gate ─────────────────────

// Gate OFF: ChangeTrackingMode to lot/serial is rejected by the
// capability gate BEFORE any item-level check runs. Guarantees the
// operator gets a remediation-actionable error ("enable capability
// first") rather than a per-item error that doesn't explain why.
func TestChangeTrackingMode_BlockedByCapabilityGate(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompanyGateOff(t, db)
	// Use a stock item with zero on-hand so the only reason the flip
	// could fail is the capability gate.
	revID := seedRevenueAccount(t, db, cid)
	item := models.ProductService{
		CompanyID:        cid,
		Name:             "Widget",
		Type:             models.ProductServiceTypeInventory,
		RevenueAccountID: revID,
		IsActive:         true,
	}
	item.ApplyTypeDefaults()
	db.Create(&item)

	err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid, ItemID: item.ID,
		NewMode: models.TrackingLot,
	})
	if !errors.Is(err, ErrTrackingCapabilityNotEnabled) {
		t.Fatalf("got %v, want ErrTrackingCapabilityNotEnabled", err)
	}

	var reloaded models.ProductService
	db.First(&reloaded, item.ID)
	if reloaded.TrackingMode != models.TrackingNone {
		t.Fatalf("tracking_mode must not change when gate is off: %q", reloaded.TrackingMode)
	}
}

// Gate OFF but caller sets NewMode=none: this is the "undo tracking"
// direction. Must be allowed unconditionally — flipping toward 'none'
// never introduces tracking truth, it reduces it. Important so a
// company that enables, uses, then wants to wind down, can always do
// so without re-enabling just to disable.
func TestChangeTrackingMode_GateOffButReturnToNone_Allowed(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompanyGateOff(t, db)
	revID := seedRevenueAccount(t, db, cid)
	// Simulate an item that was tracked when the gate was on, then
	// someone turned the gate off (the disable flow normally blocks
	// this but we're directly setting state to test the inverse).
	item := models.ProductService{
		CompanyID:        cid,
		Name:             "LegacyTracked",
		Type:             models.ProductServiceTypeInventory,
		RevenueAccountID: revID,
		IsActive:         true,
		TrackingMode:     models.TrackingLot,
	}
	item.IsStockItem = true
	db.Create(&item)

	err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid, ItemID: item.ID,
		NewMode: models.TrackingNone,
	})
	if err != nil {
		t.Fatalf("flip to none under gate-off: got %v, want nil", err)
	}

	var reloaded models.ProductService
	db.First(&reloaded, item.ID)
	if reloaded.TrackingMode != models.TrackingNone {
		t.Fatalf("tracking_mode should be none after flip-down: %q", reloaded.TrackingMode)
	}
}

// ChangeCompanyTrackingCapability happy path: enabling writes an audit
// row, and subsequent ChangeTrackingMode calls are no longer blocked by
// the gate.
func TestChangeCompanyTrackingCapability_EnableWritesAuditAndUnlocksGate(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompanyGateOff(t, db)
	revID := seedRevenueAccount(t, db, cid)
	item := models.ProductService{
		CompanyID: cid, Name: "Widget",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: revID, IsActive: true,
	}
	item.ApplyTypeDefaults()
	db.Create(&item)

	// Flip gate ON.
	if err := ChangeCompanyTrackingCapability(db, ChangeCompanyTrackingCapabilityInput{
		CompanyID: cid, Enabled: true, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("enable: %v", err)
	}

	// Audit row exists.
	var logs []models.AuditLog
	db.Where("entity_type = ? AND entity_id = ? AND action = ?",
		"company", cid, "company.tracking_capability.enabled").Find(&logs)
	if len(logs) != 1 || logs[0].Actor != "admin@test" {
		t.Fatalf("audit: got %+v want 1 row by admin@test", logs)
	}

	// Now ChangeTrackingMode to lot is permitted (company state + item
	// state are both legitimate).
	if err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid, ItemID: item.ID, NewMode: models.TrackingLot,
	}); err != nil {
		t.Fatalf("post-enable ChangeTrackingMode: %v", err)
	}
}

// Disabling is blocked when any item is still tracked: refusing to
// silently orphan live tracking truth.
func TestChangeCompanyTrackingCapability_DisableBlockedByTrackedItems(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompany(t, db) // gate ON
	item := seedStockItem(t, db, cid, "Widget")
	// Item legitimately becomes tracked.
	if err := ChangeTrackingMode(db, ChangeTrackingModeInput{
		CompanyID: cid, ItemID: item.ID, NewMode: models.TrackingSerial,
	}); err != nil {
		t.Fatalf("enable per-item tracking: %v", err)
	}

	err := ChangeCompanyTrackingCapability(db, ChangeCompanyTrackingCapabilityInput{
		CompanyID: cid, Enabled: false, Actor: "admin@test",
	})
	if !errors.Is(err, ErrTrackingCapabilityHasTrackedItems) {
		t.Fatalf("got %v, want ErrTrackingCapabilityHasTrackedItems", err)
	}

	// Company state unchanged.
	var company models.Company
	db.First(&company, cid)
	if !company.TrackingEnabled {
		t.Fatalf("TrackingEnabled was flipped despite refusal")
	}
}

// Disabling succeeds when every item is back to tracking_mode='none'.
// Exercises the inverse — a company that decides tracking wasn't for
// them after all can wind the capability back down.
func TestChangeCompanyTrackingCapability_DisableAllowedWhenNoItemsTracked(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompany(t, db) // gate ON, no tracked items yet

	if err := ChangeCompanyTrackingCapability(db, ChangeCompanyTrackingCapabilityInput{
		CompanyID: cid, Enabled: false, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	var company models.Company
	db.First(&company, cid)
	if company.TrackingEnabled {
		t.Fatalf("expected TrackingEnabled=false after disable")
	}
	// Audit row captures the flip.
	var logs []models.AuditLog
	db.Where("entity_type = ? AND action = ?",
		"company", "company.tracking_capability.disabled").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("disable audit: got %d rows want 1", len(logs))
	}
}

// No-op flip (already in target state) writes no audit and does nothing.
func TestChangeCompanyTrackingCapability_NoOpWhenAlreadyInState(t *testing.T) {
	db := testTrackingDB(t)
	cid := seedTrackingCompanyGateOff(t, db) // already off

	if err := ChangeCompanyTrackingCapability(db, ChangeCompanyTrackingCapabilityInput{
		CompanyID: cid, Enabled: false, Actor: "admin@test",
	}); err != nil {
		t.Fatalf("no-op disable: %v", err)
	}

	var logs int64
	db.Model(&models.AuditLog{}).
		Where("entity_type = ? AND action LIKE ?",
			"company", "company.tracking_capability.%").Count(&logs)
	if logs != 0 {
		t.Fatalf("no-op flip must not write audit: got %d rows", logs)
	}
}
