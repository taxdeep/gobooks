// 遵循project_guide.md
package services

// shipment_service_test.go — Phase I slice I.2 contract tests.
//
// Locks three things:
//  1. Document-layer CRUD works: create with lines, read back, update
//     draft, list by company, delete draft, refuse post-state edits.
//  2. Status machine: draft → posted → voided. Non-path transitions
//     (e.g. posting a voided shipment, voiding a draft) are refused.
//  3. **Scope boundary for I.2**: Post and Void have zero side effects
//     on inventory (no movements, no cost layers, no balances, no
//     lots, no serial units) and no side effects on GL (no journal
//     entries, no journal lines). This prevents accidental I.3 slip
//     — the moment anyone adds an IssueStockFromShipment call inside
//     PostShipment, these assertions break in CI.
//  4. **Rail dormancy for I.2**: flipping companies.shipment_required
//     to TRUE does not change PostShipment / VoidShipment behavior.
//     Both paths remain byte-identical in I.2. The consumer that
//     makes shipment_required=true meaningful lands in I.3.

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"gobooks/internal/models"
	"gobooks/internal/services/inventory"
)

// testShipmentDocDB spins an in-memory DB with the full Phase I.2
// footprint plus the inventory / GL tables used by the boundary
// checks. The inventory tables are NOT expected to be written by
// I.2 code paths — they are present so that "assert zero rows"
// checks can actually run.
func testShipmentDocDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:ship_doc_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Warehouse{},
		&models.Customer{},
		&models.ProductService{},
		&models.Account{},
		&models.SalesOrder{},
		&models.SalesOrderLine{},
		&models.Shipment{},
		&models.ShipmentLine{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.InventoryLayerConsumption{},
		&models.InventoryTrackingConsumption{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.WaitingForInvoiceItem{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

type shipmentFixture struct {
	CompanyID   uint
	WarehouseID uint
	CustomerID  uint
	ItemID      uint
}

func seedShipmentFixture(t *testing.T, db *gorm.DB) shipmentFixture {
	t.Helper()
	co := models.Company{Name: "ship-doc-co", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	wh := models.Warehouse{CompanyID: co.ID, Name: "Main", Code: "MAIN", IsActive: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}
	c := models.Customer{CompanyID: co.ID, Name: "Acme Buyer", IsActive: true}
	if err := db.Create(&c).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	rev := models.Account{
		CompanyID:         co.ID,
		Code:              "4000",
		Name:              "Revenue",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: "sales_revenue",
		IsActive:          true,
	}
	if err := db.Create(&rev).Error; err != nil {
		t.Fatalf("seed revenue account: %v", err)
	}
	item := models.ProductService{
		CompanyID:        co.ID,
		Name:             "Widget",
		Type:             models.ProductServiceTypeInventory,
		RevenueAccountID: rev.ID,
		IsActive:         true,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return shipmentFixture{
		CompanyID:   co.ID,
		WarehouseID: wh.ID,
		CustomerID:  c.ID,
		ItemID:      item.ID,
	}
}

// assertNoInventoryOrGLEffectForShipment verifies the I.2 boundary:
// no inventory or GL artefact has been written for the given company.
// Sell-side mirror of receipt_service_test.go's
// assertNoInventoryOrGLEffect. Duplicated (not shared) so that if the
// two slices ever diverge in their boundary contract (e.g. I.5
// matching writes a clearing entry that H.5 doesn't), the assertions
// can track independently.
func assertNoInventoryOrGLEffectForShipment(t *testing.T, db *gorm.DB, companyID uint) {
	t.Helper()
	checks := []struct {
		table string
		model any
	}{
		{"inventory_movements", &models.InventoryMovement{}},
		{"inventory_balances", &models.InventoryBalance{}},
		{"inventory_cost_layers", &models.InventoryCostLayer{}},
		{"inventory_lots", &models.InventoryLot{}},
		{"inventory_serial_units", &models.InventorySerialUnit{}},
		{"journal_entries", &models.JournalEntry{}},
		{"journal_lines", &models.JournalLine{}},
	}
	for _, c := range checks {
		var n int64
		if err := db.Model(c.model).
			Where("company_id = ?", companyID).
			Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", c.table, err)
		}
		if n != 0 {
			t.Fatalf("I.2 boundary violated: %s has %d row(s) for company %d; I.2 must not produce inventory or GL artefacts",
				c.table, n, companyID)
		}
	}
}

func buildSimpleShipmentCreateInput(fx shipmentFixture) CreateShipmentInput {
	return CreateShipmentInput{
		CompanyID:      fx.CompanyID,
		ShipmentNumber: "SHIP-001",
		CustomerID:     &fx.CustomerID,
		WarehouseID:    fx.WarehouseID,
		ShipDate:       time.Now().UTC(),
		Memo:           "smoke",
		Reference:      "BOL-9001",
		Lines: []CreateShipmentLineInput{
			{
				SortOrder:        1,
				ProductServiceID: fx.ItemID,
				Description:      "Widget carton",
				Qty:              decimal.NewFromInt(7),
				Unit:             "ea",
			},
		},
		Actor: "admin@test",
	}
}

// ── Create / Read ────────────────────────────────────────────────────────────

func TestCreateShipment_PersistsHeaderAndLines(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)

	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.ID == 0 {
		t.Fatalf("expected non-zero ID")
	}
	if out.Status != models.ShipmentStatusDraft {
		t.Fatalf("status: got %q want draft", out.Status)
	}
	if len(out.Lines) != 1 {
		t.Fatalf("lines: got %d want 1", len(out.Lines))
	}
	if out.Lines[0].Qty.Cmp(decimal.NewFromInt(7)) != 0 {
		t.Fatalf("line qty: got %s want 7", out.Lines[0].Qty)
	}

	got, err := GetShipment(db, fx.CompanyID, out.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ShipmentNumber != "SHIP-001" || got.Reference != "BOL-9001" {
		t.Fatalf("round-trip fields: %+v", got)
	}
}

func TestCreateShipment_RejectsMissingWarehouse(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	in := buildSimpleShipmentCreateInput(fx)
	in.WarehouseID = 0
	if _, err := CreateShipment(db, in); err != ErrShipmentWarehouseRequired {
		t.Fatalf("got %v want ErrShipmentWarehouseRequired", err)
	}
}

func TestCreateShipment_RejectsMissingDate(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	in := buildSimpleShipmentCreateInput(fx)
	in.ShipDate = time.Time{}
	if _, err := CreateShipment(db, in); err != ErrShipmentDateRequired {
		t.Fatalf("got %v want ErrShipmentDateRequired", err)
	}
}

func TestCreateShipment_RejectsLineWithoutProduct(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	in := buildSimpleShipmentCreateInput(fx)
	in.Lines[0].ProductServiceID = 0
	_, err := CreateShipment(db, in)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !isErr(err, ErrShipmentLineProductRequired) {
		t.Fatalf("got %v want ErrShipmentLineProductRequired", err)
	}
}

func TestGetShipment_CompanyScopedNotFound(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := GetShipment(db, fx.CompanyID+999, out.ID); err != ErrShipmentNotFound {
		t.Fatalf("cross-company get: got %v want ErrShipmentNotFound", err)
	}
}

// ── Cross-company scope guards ───────────────────────────────────────────────

// Proves that referencing a warehouse owned by a DIFFERENT company is
// rejected before any Shipment write. Locks the boundary that I.3 /
// I.5 rely on: no Shipment can reach posted state carrying a cross-
// tenant reference that would later mis-attribute inventory or COGS.
func TestCreateShipment_RejectsCrossCompanyWarehouse(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)

	otherCo := models.Company{Name: "other", IsActive: true}
	if err := db.Create(&otherCo).Error; err != nil {
		t.Fatalf("seed other company: %v", err)
	}
	otherWh := models.Warehouse{CompanyID: otherCo.ID, Name: "other-wh", Code: "OTH", IsActive: true}
	if err := db.Create(&otherWh).Error; err != nil {
		t.Fatalf("seed other warehouse: %v", err)
	}

	in := buildSimpleShipmentCreateInput(fx)
	in.WarehouseID = otherWh.ID
	_, err := CreateShipment(db, in)
	if err == nil {
		t.Fatalf("expected cross-company rejection")
	}
	if !isErr(err, ErrShipmentCrossCompanyReference) {
		t.Fatalf("got %v want ErrShipmentCrossCompanyReference", err)
	}
}

// ── Update ───────────────────────────────────────────────────────────────────

func TestUpdateShipment_DraftSucceeds(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newMemo := "updated memo"
	newRef := "BOL-9002"
	updated, err := UpdateShipment(db, fx.CompanyID, out.ID, UpdateShipmentInput{
		Memo:      &newMemo,
		Reference: &newRef,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Memo != newMemo || updated.Reference != newRef {
		t.Fatalf("update did not apply: %+v", updated)
	}
}

func TestUpdateShipment_RefusedOnPosted(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("post: %v", err)
	}

	newMemo := "too late"
	_, err = UpdateShipment(db, fx.CompanyID, out.ID, UpdateShipmentInput{Memo: &newMemo})
	if err == nil {
		t.Fatalf("expected error updating posted shipment")
	}
	if !isErr(err, ErrShipmentNotDraft) {
		t.Fatalf("got %v want ErrShipmentNotDraft", err)
	}
}

// ── List ─────────────────────────────────────────────────────────────────────

func TestListShipments_CompanyScoped(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	for i := 0; i < 2; i++ {
		in := buildSimpleShipmentCreateInput(fx)
		in.ShipmentNumber = fmt.Sprintf("S%02d", i)
		if _, err := CreateShipment(db, in); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	otherCo := models.Company{Name: "other", IsActive: true}
	if err := db.Create(&otherCo).Error; err != nil {
		t.Fatalf("seed other company: %v", err)
	}
	otherWh := models.Warehouse{CompanyID: otherCo.ID, Name: "other-wh", Code: "OTH", IsActive: true}
	if err := db.Create(&otherWh).Error; err != nil {
		t.Fatalf("seed other warehouse: %v", err)
	}
	otherItem := models.ProductService{
		CompanyID: otherCo.ID, Name: "Other",
		Type: models.ProductServiceTypeInventory, IsActive: true,
	}
	otherItem.ApplyTypeDefaults()
	if err := db.Create(&otherItem).Error; err != nil {
		t.Fatalf("seed other item: %v", err)
	}
	if _, err := CreateShipment(db, CreateShipmentInput{
		CompanyID:      otherCo.ID,
		ShipmentNumber: "X-1",
		WarehouseID:    otherWh.ID,
		ShipDate:       time.Now().UTC(),
		Lines: []CreateShipmentLineInput{
			{ProductServiceID: otherItem.ID, Qty: decimal.NewFromInt(1)},
		},
	}); err != nil {
		t.Fatalf("create other: %v", err)
	}

	rows, err := ListShipments(db, fx.CompanyID, ListShipmentsFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("list: got %d rows want 2 (expected only fx.CompanyID's shipments)", len(rows))
	}
	for _, r := range rows {
		if r.CompanyID != fx.CompanyID {
			t.Fatalf("cross-tenant leak: row %d belongs to company %d", r.ID, r.CompanyID)
		}
	}
}

// ── Post / Void lifecycle ────────────────────────────────────────────────────

func TestPostShipment_FlipsStatusAndWritesAudit_NoInventoryOrGL(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	posted, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posted.Status != models.ShipmentStatusPosted {
		t.Fatalf("status: got %q want posted", posted.Status)
	}
	if posted.PostedAt == nil {
		t.Fatalf("PostedAt not set")
	}
	if posted.JournalEntryID != nil {
		t.Fatalf("I.2 post must not link a JE; got %d", *posted.JournalEntryID)
	}

	var logs []models.AuditLog
	db.Where("entity_type = ? AND entity_id = ? AND action = ?",
		"shipment", out.ID, "shipment.posted").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("audit: got %d rows want 1 (%+v)", len(logs), logs)
	}
	if logs[0].Actor != "admin@test" {
		t.Fatalf("audit actor: got %q want admin@test", logs[0].Actor)
	}

	// Boundary lock — I.2 must not touch inventory or GL.
	assertNoInventoryOrGLEffectForShipment(t, db, fx.CompanyID)
}

func TestPostShipment_RefusedWhenAlreadyPosted(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("first post: %v", err)
	}
	_, err = PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err == nil {
		t.Fatalf("expected error posting twice")
	}
	if !isErr(err, ErrShipmentAlreadyPosted) {
		t.Fatalf("got %v want ErrShipmentAlreadyPosted", err)
	}
}

func TestVoidShipment_FlipsStatusAndWritesAudit_NoInventoryOrGL(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("post: %v", err)
	}
	voided, err := VoidShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if voided.Status != models.ShipmentStatusVoided {
		t.Fatalf("status: got %q want voided", voided.Status)
	}
	if voided.VoidedAt == nil {
		t.Fatalf("VoidedAt not set")
	}

	var logs []models.AuditLog
	db.Where("entity_type = ? AND entity_id = ? AND action = ?",
		"shipment", out.ID, "shipment.voided").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("void audit: got %d rows want 1", len(logs))
	}

	assertNoInventoryOrGLEffectForShipment(t, db, fx.CompanyID)
}

func TestVoidShipment_RefusedOnDraft(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = VoidShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err == nil {
		t.Fatalf("expected void to fail on draft")
	}
	if !isErr(err, ErrShipmentNotPosted) {
		t.Fatalf("got %v want ErrShipmentNotPosted", err)
	}
}

// ── Delete ───────────────────────────────────────────────────────────────────

func TestDeleteShipment_DraftSucceeds(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := DeleteShipment(db, fx.CompanyID, out.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := GetShipment(db, fx.CompanyID, out.ID); err != ErrShipmentNotFound {
		t.Fatalf("after delete: got %v want ErrShipmentNotFound", err)
	}
	var lineCount int64
	db.Model(&models.ShipmentLine{}).Where("shipment_id = ?", out.ID).Count(&lineCount)
	if lineCount != 0 {
		t.Fatalf("lines: got %d want 0 after delete", lineCount)
	}
}

func TestDeleteShipment_RefusedOnPosted(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("post: %v", err)
	}
	err = DeleteShipment(db, fx.CompanyID, out.ID)
	if err == nil {
		t.Fatalf("expected delete to fail on posted shipment")
	}
	if !isErr(err, ErrShipmentNotDraft) {
		t.Fatalf("got %v want ErrShipmentNotDraft", err)
	}
}

// ── I.3 flag-off regression lock (byte-identical to I.2) ────────────────────

// Under `shipment_required=false` (the default), PostShipment remains
// a pure document-layer status flip. This locks the I.3 exit condition
// "Under shipment_required=false, legacy behavior is byte-identical"
// for the Shipment side: legacy companies continue with Invoice-
// forms-COGS and Shipment produces no parallel inventory / GL effect.
func TestPostShipment_FlagOff_ByteIdenticalToI2_NoInventoryOrGL(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", false).Error; err != nil {
		t.Fatalf("set flag off: %v", err)
	}
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	posted, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posted.JournalEntryID != nil {
		t.Fatalf("flag=false post must not link a JE; got %d", *posted.JournalEntryID)
	}
	assertNoInventoryOrGLEffectForShipment(t, db, fx.CompanyID)

	// Also confirm no WFI rows were written.
	var wfiCount int64
	db.Model(&models.WaitingForInvoiceItem{}).Where("company_id = ?", fx.CompanyID).Count(&wfiCount)
	if wfiCount != 0 {
		t.Fatalf("flag=false must not write WFI rows; got %d", wfiCount)
	}
}

// ── I.3 flag-on happy path: the 3-layer chain materialises ──────────────────

type shipmentFlagOnAccounts struct {
	InventoryAccountID uint
	COGSAccountID      uint
}

// seedShipmentFlagOnFixture primes a company fully for PostShipment
// under flag=true: rail flipped ON, InventoryAccountID + COGSAccountID
// configured on the stock item, and pre-stock deposited in the
// warehouse so IssueStock has layers to peel. Returns the two
// relevant account IDs for assertion use.
func seedShipmentFlagOnFixture(t *testing.T, db *gorm.DB, fx shipmentFixture, preStockQty decimal.Decimal, preStockCost decimal.Decimal) shipmentFlagOnAccounts {
	t.Helper()
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("flip shipment_required: %v", err)
	}
	invAcct := models.Account{
		CompanyID:         fx.CompanyID,
		Code:              "1300",
		Name:              "Inventory Asset",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailInventory,
		IsActive:          true,
	}
	if err := db.Create(&invAcct).Error; err != nil {
		t.Fatalf("seed inventory account: %v", err)
	}
	cogsAcct := models.Account{
		CompanyID:         fx.CompanyID,
		Code:              "5000",
		Name:              "Cost of Goods Sold",
		RootAccountType:   models.RootCostOfSales,
		DetailAccountType: models.DetailCostOfGoodsSold,
		IsActive:          true,
	}
	if err := db.Create(&cogsAcct).Error; err != nil {
		t.Fatalf("seed COGS account: %v", err)
	}
	if err := db.Model(&models.ProductService{}).
		Where("id = ?", fx.ItemID).
		Updates(map[string]any{
			"inventory_account_id": invAcct.ID,
			"cogs_account_id":      cogsAcct.ID,
		}).Error; err != nil {
		t.Fatalf("wire accounts on item: %v", err)
	}

	// Pre-stock via inventory.ReceiveStock so there is layer to peel.
	_, err := inventoryReceiveForTest(db, fx.CompanyID, fx.ItemID, fx.WarehouseID, preStockQty, preStockCost)
	if err != nil {
		t.Fatalf("pre-stock: %v", err)
	}
	return shipmentFlagOnAccounts{InventoryAccountID: invAcct.ID, COGSAccountID: cogsAcct.ID}
}

// inventoryReceiveForTest is a thin test helper around inventory.ReceiveStock
// so the flag-on fixture can seed a warehouse balance without reaching
// across the receipt / bill document layer. Keeps the test independent
// of H.2/H.3 wiring — this test is about I.3, not inbound.
func inventoryReceiveForTest(db *gorm.DB, companyID, itemID, warehouseID uint, qty, unitCost decimal.Decimal) (*inventory.ReceiveStockResult, error) {
	return inventory.ReceiveStock(db, inventory.ReceiveStockInput{
		CompanyID:      companyID,
		ItemID:         itemID,
		WarehouseID:    warehouseID,
		Quantity:       qty,
		MovementDate:   time.Now().UTC(),
		UnitCost:       unitCost,
		ExchangeRate:   decimal.NewFromInt(1),
		SourceType:     "test_seed",
		SourceID:       0,
		IdempotencyKey: fmt.Sprintf("test_seed:%d:%d:%d", companyID, itemID, warehouseID),
		Memo:           "pre-stock for I.3 test",
	})
}

func TestPostShipment_FlagOn_ProducesIssueTruthCOGSJournalAndWFI(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	accts := seedShipmentFlagOnFixture(t, db, fx,
		decimal.NewFromInt(100), decimal.NewFromFloat(3.00))

	// Create shipment for qty=7 @ cost-derived-from-inventory (3.00 per unit).
	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	posted, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// (1) Document layer: status flipped + JE linked.
	if posted.Status != models.ShipmentStatusPosted {
		t.Fatalf("status: got %q", posted.Status)
	}
	if posted.JournalEntryID == nil {
		t.Fatalf("JE not linked; flag=true with stock lines must link JE")
	}

	// (2) Issue truth: one inventory_movements row source_type='shipment'.
	var mvs []models.InventoryMovement
	if err := db.Where("company_id = ? AND source_type = ? AND source_id = ?",
		fx.CompanyID, "shipment", out.ID).Find(&mvs).Error; err != nil {
		t.Fatalf("load movements: %v", err)
	}
	if len(mvs) != 1 {
		t.Fatalf("movements: got %d want 1", len(mvs))
	}
	// Sign convention: IssueStock books a negative delta (stock leaves).
	if !mvs[0].QuantityDelta.Equal(decimal.NewFromInt(-7)) {
		t.Fatalf("qty_delta: got %s want -7", mvs[0].QuantityDelta)
	}

	// (3) Inventory effect: balance decremented from 100 → 93.
	var bal models.InventoryBalance
	if err := db.Where("company_id = ? AND item_id = ?",
		fx.CompanyID, fx.ItemID).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(93)) {
		t.Fatalf("on_hand: got %s want 93 (100-7)", bal.QuantityOnHand)
	}

	// (4) JE: Dr COGS + Cr Inventory, equal amounts = 7 * 3.00 = 21.00.
	var je models.JournalEntry
	if err := db.Preload("Lines").
		Where("id = ?", *posted.JournalEntryID).
		First(&je).Error; err != nil {
		t.Fatalf("load JE: %v", err)
	}
	if je.SourceType != models.LedgerSourceShipment || je.SourceID != out.ID {
		t.Fatalf("JE source linkage: got %s/%d want shipment/%d", je.SourceType, je.SourceID, out.ID)
	}
	var cogsDebit, invCredit decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case accts.COGSAccountID:
			cogsDebit = cogsDebit.Add(l.Debit)
		case accts.InventoryAccountID:
			invCredit = invCredit.Add(l.Credit)
		}
	}
	want := decimal.NewFromFloat(21.00)
	if !cogsDebit.Equal(want) {
		t.Fatalf("COGS debit: got %s want %s", cogsDebit, want)
	}
	if !invCredit.Equal(want) {
		t.Fatalf("Inventory credit: got %s want %s", invCredit, want)
	}

	// (5) Waiting-for-invoice item: one open row per stock line.
	var wfis []models.WaitingForInvoiceItem
	if err := db.Where("company_id = ? AND shipment_id = ?",
		fx.CompanyID, out.ID).Find(&wfis).Error; err != nil {
		t.Fatalf("load WFI: %v", err)
	}
	if len(wfis) != 1 {
		t.Fatalf("WFI rows: got %d want 1", len(wfis))
	}
	if wfis[0].Status != models.WaitingForInvoiceStatusOpen {
		t.Fatalf("WFI status: got %q want open", wfis[0].Status)
	}
	if !wfis[0].QtyPending.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("WFI qty_pending: got %s want 7", wfis[0].QtyPending)
	}
	if !wfis[0].UnitCostBase.Equal(decimal.NewFromFloat(3.00)) {
		t.Fatalf("WFI unit_cost_base: got %s want 3.00", wfis[0].UnitCostBase)
	}
	if wfis[0].ShipmentLineID != out.Lines[0].ID {
		t.Fatalf("WFI shipment_line_id: got %d want %d", wfis[0].ShipmentLineID, out.Lines[0].ID)
	}
}

func TestPostShipment_FlagOn_MissingCOGSAccount_Rejected(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	accts := seedShipmentFlagOnFixture(t, db, fx,
		decimal.NewFromInt(100), decimal.NewFromFloat(3.00))
	// Clear COGSAccountID so the post-time check fails.
	if err := db.Model(&models.ProductService{}).
		Where("id = ?", fx.ItemID).
		Update("cogs_account_id", nil).Error; err != nil {
		t.Fatalf("clear cogs acct: %v", err)
	}
	_ = accts

	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if !isErr(err, ErrShipmentCOGSAccountMissing) {
		t.Fatalf("got %v want ErrShipmentCOGSAccountMissing", err)
	}
	// Shipment must remain in draft — tx rolled back.
	var stillDraft models.Shipment
	db.First(&stillDraft, out.ID)
	if stillDraft.Status != models.ShipmentStatusDraft {
		t.Fatalf("status: got %q want draft (tx should roll back)", stillDraft.Status)
	}
}

func TestPostShipment_FlagOn_ServiceOnlyShipment_NoJEBooked(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	seedShipmentFlagOnFixture(t, db, fx,
		decimal.NewFromInt(100), decimal.NewFromFloat(3.00))

	// Replace the stock item with a service item.
	svc := models.ProductService{
		CompanyID: fx.CompanyID, Name: "Delivery labour",
		Type: models.ProductServiceTypeService, IsActive: true,
	}
	svc.ApplyTypeDefaults()
	if err := db.Create(&svc).Error; err != nil {
		t.Fatalf("seed service: %v", err)
	}
	in := buildSimpleShipmentCreateInput(fx)
	in.Lines[0].ProductServiceID = svc.ID
	in.Lines[0].Description = "Expedited delivery"
	out, err := CreateShipment(db, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	posted, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posted.JournalEntryID != nil {
		t.Fatalf("service-only shipment must not link JE; got %d", *posted.JournalEntryID)
	}
	// No movement, no WFI row.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ?", fx.CompanyID, "shipment").
		Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("service-only produced %d movements want 0", mvCount)
	}
	var wfiCount int64
	db.Model(&models.WaitingForInvoiceItem{}).
		Where("company_id = ? AND shipment_id = ?", fx.CompanyID, out.ID).
		Count(&wfiCount)
	if wfiCount != 0 {
		t.Fatalf("service-only shipment produced %d WFI rows want 0", wfiCount)
	}
}

func TestVoidShipment_FlagOn_ReversesJournalMovementsAndVoidsWFI(t *testing.T) {
	db := testShipmentDocDB(t)
	fx := seedShipmentFixture(t, db)
	seedShipmentFlagOnFixture(t, db, fx,
		decimal.NewFromInt(100), decimal.NewFromFloat(3.00))

	out, err := CreateShipment(db, buildSimpleShipmentCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	posted, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	origJEID := *posted.JournalEntryID

	voided, err := VoidShipment(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if voided.Status != models.ShipmentStatusVoided {
		t.Fatalf("status: got %q", voided.Status)
	}

	// Original JE flipped to reversed.
	var origJE models.JournalEntry
	if err := db.First(&origJE, origJEID).Error; err != nil {
		t.Fatalf("load orig JE: %v", err)
	}
	if origJE.Status != models.JournalEntryStatusReversed {
		t.Fatalf("orig JE status: got %q want reversed", origJE.Status)
	}

	// Reversal JE exists.
	var revJEs []models.JournalEntry
	db.Where("reversed_from_id = ?", origJEID).Find(&revJEs)
	if len(revJEs) != 1 {
		t.Fatalf("reversal JEs: got %d want 1", len(revJEs))
	}

	// Reversal inventory movement exists (source_type='shipment_reversal').
	var revMvs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?",
		fx.CompanyID, "shipment_reversal").Find(&revMvs)
	if len(revMvs) != 1 {
		t.Fatalf("reversal movements: got %d want 1", len(revMvs))
	}

	// Balance back to 100 (pre-stock).
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?",
		fx.CompanyID, fx.ItemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("balance after void: got %s want 100", bal.QuantityOnHand)
	}

	// WFI row flipped to voided.
	var wfi models.WaitingForInvoiceItem
	if err := db.Where("company_id = ? AND shipment_id = ?",
		fx.CompanyID, out.ID).First(&wfi).Error; err != nil {
		t.Fatalf("load WFI: %v", err)
	}
	if wfi.Status != models.WaitingForInvoiceStatusVoided {
		t.Fatalf("WFI status after void: got %q want voided", wfi.Status)
	}
}
