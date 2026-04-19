// 遵循project_guide.md
package services

// receipt_service_test.go — Phase H slice H.2 contract tests.
//
// Locks three things:
//  1. Document-layer CRUD works: create with lines, read back, update
//     draft, list by company, delete draft, refuse post-state edits.
//  2. Status machine: draft → posted → voided. Non-path transitions
//     (e.g. posting a voided receipt, voiding a draft) are refused.
//  3. **Scope boundary for H.2**: Post and Void have zero side effects
//     on inventory (no movements, no cost layers, no balances, no
//     lots, no serial units) and no side effects on GL (no journal
//     entries, no journal lines). This prevents accidental H.3 slip
//     — the moment anyone adds a ReceiveStockFromReceipt call inside
//     PostReceipt, these assertions break in CI.

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"gobooks/internal/models"
)

// testReceiptDocDB spins an in-memory DB with the full Phase H.2
// footprint plus the inventory / GL tables used by the boundary
// checks. The inventory tables are NOT expected to be written by
// H.2 code paths — they are present so that "assert zero rows"
// checks can actually run.
func testReceiptDocDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:rcpt_doc_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Warehouse{},
		&models.Vendor{},
		&models.ProductService{},
		&models.Account{},
		&models.Receipt{},
		&models.ReceiptLine{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.AuditLog{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

type receiptFixture struct {
	CompanyID   uint
	WarehouseID uint
	VendorID    uint
	ItemID      uint
}

func seedReceiptFixture(t *testing.T, db *gorm.DB) receiptFixture {
	t.Helper()
	co := models.Company{Name: "rcpt-doc-co", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	wh := models.Warehouse{CompanyID: co.ID, Name: "Main", Code: "MAIN", IsActive: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}
	v := models.Vendor{CompanyID: co.ID, Name: "Acme", IsActive: true}
	if err := db.Create(&v).Error; err != nil {
		t.Fatalf("seed vendor: %v", err)
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
	return receiptFixture{
		CompanyID:   co.ID,
		WarehouseID: wh.ID,
		VendorID:    v.ID,
		ItemID:      item.ID,
	}
}

// assertNoInventoryOrGLEffect verifies the H.2 boundary: no inventory
// or GL artefact has been written for the given company. Called
// after PostReceipt / VoidReceipt to lock the no-side-effect
// contract at the CI level.
func assertNoInventoryOrGLEffect(t *testing.T, db *gorm.DB, companyID uint) {
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
			t.Fatalf("H.2 boundary violated: %s has %d row(s) for company %d; H.2 must not produce inventory or GL artefacts",
				c.table, n, companyID)
		}
	}
}

func buildSimpleCreateInput(fx receiptFixture) CreateReceiptInput {
	return CreateReceiptInput{
		CompanyID:     fx.CompanyID,
		ReceiptNumber: "RCPT-001",
		VendorID:      &fx.VendorID,
		WarehouseID:   fx.WarehouseID,
		ReceiptDate:   time.Now().UTC(),
		Memo:          "smoke",
		Reference:     "PACK-9001",
		Lines: []CreateReceiptLineInput{
			{
				SortOrder:        1,
				ProductServiceID: fx.ItemID,
				Description:      "Widget carton",
				Qty:              decimal.NewFromInt(10),
				Unit:             "ea",
				UnitCost:         decimal.NewFromFloat(5.00),
			},
		},
		Actor: "admin@test",
	}
}

// ── Create / Read ────────────────────────────────────────────────────────────

func TestCreateReceipt_PersistsHeaderAndLines(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)

	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.ID == 0 {
		t.Fatalf("expected non-zero ID")
	}
	if out.Status != models.ReceiptStatusDraft {
		t.Fatalf("status: got %q want draft", out.Status)
	}
	if len(out.Lines) != 1 {
		t.Fatalf("lines: got %d want 1", len(out.Lines))
	}
	if out.Lines[0].Qty.Cmp(decimal.NewFromInt(10)) != 0 {
		t.Fatalf("line qty: got %s want 10", out.Lines[0].Qty)
	}
	if !out.Lines[0].UnitCost.Equal(decimal.NewFromFloat(5.00)) {
		t.Fatalf("line unit_cost: got %s want 5.00", out.Lines[0].UnitCost)
	}

	// Round-trip via GetReceipt.
	got, err := GetReceipt(db, fx.CompanyID, out.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ReceiptNumber != "RCPT-001" || got.Reference != "PACK-9001" {
		t.Fatalf("round-trip fields: %+v", got)
	}
}

func TestCreateReceipt_RejectsMissingWarehouse(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	in := buildSimpleCreateInput(fx)
	in.WarehouseID = 0
	if _, err := CreateReceipt(db, in); err != ErrInboundReceiptWarehouseRequired {
		t.Fatalf("got %v want ErrInboundReceiptWarehouseRequired", err)
	}
}

func TestGetReceipt_CompanyScopedNotFound(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Look up with a different company id → must 404, not leak.
	if _, err := GetReceipt(db, fx.CompanyID+999, out.ID); err != ErrInboundReceiptNotFound {
		t.Fatalf("cross-company get: got %v want ErrInboundReceiptNotFound", err)
	}
}

// ── Update ───────────────────────────────────────────────────────────────────

func TestUpdateReceipt_DraftSucceeds(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newMemo := "updated memo"
	newRef := "PACK-9002"
	updated, err := UpdateReceipt(db, fx.CompanyID, out.ID, UpdateReceiptInput{
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

func TestUpdateReceipt_RefusedOnPosted(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("post: %v", err)
	}

	newMemo := "too late"
	_, err = UpdateReceipt(db, fx.CompanyID, out.ID, UpdateReceiptInput{Memo: &newMemo})
	if err == nil {
		t.Fatalf("expected error updating posted receipt")
	}
	// Accept wrap; check sentinel.
	if !isErr(err, ErrInboundReceiptNotDraft) {
		t.Fatalf("got %v want ErrInboundReceiptNotDraft", err)
	}
}

// ── List ─────────────────────────────────────────────────────────────────────

func TestListReceipts_CompanyScoped(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	// Create 2 receipts for fx.CompanyID.
	for i := 0; i < 2; i++ {
		in := buildSimpleCreateInput(fx)
		in.ReceiptNumber = fmt.Sprintf("R%02d", i)
		if _, err := CreateReceipt(db, in); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	// Create a second company + one receipt for it.
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
	if _, err := CreateReceipt(db, CreateReceiptInput{
		CompanyID:     otherCo.ID,
		ReceiptNumber: "X-1",
		WarehouseID:   otherWh.ID,
		ReceiptDate:   time.Now().UTC(),
		Lines: []CreateReceiptLineInput{
			{ProductServiceID: otherItem.ID, Qty: decimal.NewFromInt(1)},
		},
	}); err != nil {
		t.Fatalf("create other: %v", err)
	}

	rows, err := ListReceipts(db, fx.CompanyID, ListReceiptsFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("list: got %d rows want 2 (expected only fx.CompanyID's receipts)", len(rows))
	}
	for _, r := range rows {
		if r.CompanyID != fx.CompanyID {
			t.Fatalf("cross-tenant leak: row %d belongs to company %d", r.ID, r.CompanyID)
		}
	}
}

// ── Post / Void lifecycle ────────────────────────────────────────────────────

func TestPostReceipt_FlipsStatusAndWritesAudit_NoInventoryOrGL(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	posted, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posted.Status != models.ReceiptStatusPosted {
		t.Fatalf("status: got %q want posted", posted.Status)
	}
	if posted.PostedAt == nil {
		t.Fatalf("PostedAt not set")
	}

	// Audit row: exactly one receipt.posted action for this receipt.
	var logs []models.AuditLog
	db.Where("entity_type = ? AND entity_id = ? AND action = ?",
		"receipt", out.ID, "receipt.posted").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("audit: got %d rows want 1 (%+v)", len(logs), logs)
	}
	if logs[0].Actor != "admin@test" {
		t.Fatalf("audit actor: got %q want admin@test", logs[0].Actor)
	}

	// Boundary lock — H.2 must not touch inventory or GL.
	assertNoInventoryOrGLEffect(t, db, fx.CompanyID)
}

func TestPostReceipt_RefusedWhenAlreadyPosted(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("first post: %v", err)
	}
	_, err = PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err == nil {
		t.Fatalf("expected error posting twice")
	}
	if !isErr(err, ErrInboundReceiptAlreadyPosted) {
		t.Fatalf("got %v want ErrInboundReceiptAlreadyPosted", err)
	}
}

func TestVoidReceipt_FlipsStatusAndWritesAudit_NoInventoryOrGL(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("post: %v", err)
	}
	voided, err := VoidReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if voided.Status != models.ReceiptStatusVoided {
		t.Fatalf("status: got %q want voided", voided.Status)
	}
	if voided.VoidedAt == nil {
		t.Fatalf("VoidedAt not set")
	}

	var logs []models.AuditLog
	db.Where("entity_type = ? AND entity_id = ? AND action = ?",
		"receipt", out.ID, "receipt.voided").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("void audit: got %d rows want 1", len(logs))
	}

	// Both post and void happened; boundary must still hold.
	assertNoInventoryOrGLEffect(t, db, fx.CompanyID)
}

func TestVoidReceipt_RefusedOnDraft(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = VoidReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err == nil {
		t.Fatalf("expected void to fail on draft")
	}
	if !isErr(err, ErrInboundReceiptNotPosted) {
		t.Fatalf("got %v want ErrInboundReceiptNotPosted", err)
	}
}

// ── Delete ───────────────────────────────────────────────────────────────────

func TestDeleteReceipt_DraftSucceeds(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := DeleteReceipt(db, fx.CompanyID, out.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := GetReceipt(db, fx.CompanyID, out.ID); err != ErrInboundReceiptNotFound {
		t.Fatalf("after delete: got %v want ErrInboundReceiptNotFound", err)
	}
	// Lines should be gone too.
	var lineCount int64
	db.Model(&models.ReceiptLine{}).Where("receipt_id = ?", out.ID).Count(&lineCount)
	if lineCount != 0 {
		t.Fatalf("lines: got %d want 0 after delete", lineCount)
	}
}

func TestDeleteReceipt_RefusedOnPosted(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("post: %v", err)
	}
	err = DeleteReceipt(db, fx.CompanyID, out.ID)
	if err == nil {
		t.Fatalf("expected delete to fail on posted receipt")
	}
	if !isErr(err, ErrInboundReceiptNotDraft) {
		t.Fatalf("got %v want ErrInboundReceiptNotDraft", err)
	}
}

// ── Dormancy lock: receipt_required is NOT consulted in H.2 ──────────────────

// H.2 must be rail-agnostic. Even on a company with receipt_required=
// true, Receipt Post/Void leaves inventory and GL untouched. Doubles
// as a forward-compatibility test for the H.4 decoupling slice: when
// H.4 lands, receipt_required=true will START producing inventory
// truth by wiring H.3's consumer; until then, the flag is inert here.
func TestReceipt_Lifecycle_DoesNotConsultReceiptRequired(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	// Force the rail ON for this company.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip rail: %v", err)
	}

	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("post: %v", err)
	}
	// Still no inventory or GL effect — H.2 ignores the flag.
	assertNoInventoryOrGLEffect(t, db, fx.CompanyID)
}

// ── Cross-company scope guards ───────────────────────────────────────────────

// seedOtherCompanyVendor returns a vendor that belongs to a company
// different from fx.CompanyID. Used by the cross-company rejection
// tests to construct an input that would produce a tenancy leak if
// validateReceiptHeaderScope were absent.
func seedOtherCompanyVendor(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "other-co", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed other company: %v", err)
	}
	v := models.Vendor{CompanyID: co.ID, Name: "OtherVendor", IsActive: true}
	if err := db.Create(&v).Error; err != nil {
		t.Fatalf("seed other vendor: %v", err)
	}
	return v.ID
}

func seedOtherCompanyWarehouse(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "other-co-wh", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed other company: %v", err)
	}
	wh := models.Warehouse{CompanyID: co.ID, Name: "OtherWH", Code: "OWH", IsActive: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed other warehouse: %v", err)
	}
	return wh.ID
}

func seedOtherCompanyItem(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "other-co-item", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed other company: %v", err)
	}
	item := models.ProductService{
		CompanyID: co.ID, Name: "OtherItem",
		Type: models.ProductServiceTypeInventory, IsActive: true,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed other item: %v", err)
	}
	return item.ID
}

func TestCreateReceipt_RejectsCrossCompanyVendor(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	foreignVendorID := seedOtherCompanyVendor(t, db)

	in := buildSimpleCreateInput(fx)
	in.VendorID = &foreignVendorID
	_, err := CreateReceipt(db, in)
	if !isErr(err, ErrInboundReceiptCrossCompanyReference) {
		t.Fatalf("got %v want ErrInboundReceiptCrossCompanyReference", err)
	}
}

func TestCreateReceipt_RejectsCrossCompanyWarehouse(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	foreignWhID := seedOtherCompanyWarehouse(t, db)

	in := buildSimpleCreateInput(fx)
	in.WarehouseID = foreignWhID
	_, err := CreateReceipt(db, in)
	if !isErr(err, ErrInboundReceiptCrossCompanyReference) {
		t.Fatalf("got %v want ErrInboundReceiptCrossCompanyReference", err)
	}
}

func TestCreateReceipt_RejectsCrossCompanyProductOnLine(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	foreignItemID := seedOtherCompanyItem(t, db)

	in := buildSimpleCreateInput(fx)
	in.Lines[0].ProductServiceID = foreignItemID
	_, err := CreateReceipt(db, in)
	if !isErr(err, ErrInboundReceiptCrossCompanyReference) {
		t.Fatalf("got %v want ErrInboundReceiptCrossCompanyReference", err)
	}
}

// seedOtherCompanyPO creates a purchase order in a different company
// and returns its ID + one of its line IDs, so the cross-company
// checks for PO header and PO-line references can both be exercised.
func seedOtherCompanyPO(t *testing.T, db *gorm.DB) (poID, poLineID uint) {
	t.Helper()
	co := models.Company{Name: "other-co-po", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed other company: %v", err)
	}
	vendor := models.Vendor{CompanyID: co.ID, Name: "POVendor", IsActive: true}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatalf("seed PO vendor: %v", err)
	}
	po := models.PurchaseOrder{
		CompanyID: co.ID, VendorID: vendor.ID, PODate: time.Now().UTC(),
		Status: models.POStatusDraft,
	}
	if err := db.Create(&po).Error; err != nil {
		t.Fatalf("seed PO: %v", err)
	}
	poLine := models.PurchaseOrderLine{
		CompanyID: co.ID, PurchaseOrderID: po.ID, SortOrder: 1,
		Description: "x", Qty: decimal.NewFromInt(1),
	}
	if err := db.Create(&poLine).Error; err != nil {
		t.Fatalf("seed PO line: %v", err)
	}
	return po.ID, poLine.ID
}

func TestCreateReceipt_RejectsCrossCompanyPurchaseOrder(t *testing.T) {
	db := testReceiptDocDB(t)
	// PO model pulls in extra tables via its FKs — migrate them too.
	if err := db.AutoMigrate(&models.PurchaseOrder{}, &models.PurchaseOrderLine{}); err != nil {
		t.Fatalf("migrate PO: %v", err)
	}
	fx := seedReceiptFixture(t, db)
	foreignPO, _ := seedOtherCompanyPO(t, db)

	in := buildSimpleCreateInput(fx)
	in.PurchaseOrderID = &foreignPO
	_, err := CreateReceipt(db, in)
	if !isErr(err, ErrInboundReceiptCrossCompanyReference) {
		t.Fatalf("got %v want ErrInboundReceiptCrossCompanyReference", err)
	}
}

func TestCreateReceipt_RejectsCrossCompanyPOLine(t *testing.T) {
	db := testReceiptDocDB(t)
	if err := db.AutoMigrate(&models.PurchaseOrder{}, &models.PurchaseOrderLine{}); err != nil {
		t.Fatalf("migrate PO: %v", err)
	}
	fx := seedReceiptFixture(t, db)
	_, foreignPOLine := seedOtherCompanyPO(t, db)

	in := buildSimpleCreateInput(fx)
	in.Lines[0].PurchaseOrderLineID = &foreignPOLine
	_, err := CreateReceipt(db, in)
	if !isErr(err, ErrInboundReceiptCrossCompanyReference) {
		t.Fatalf("got %v want ErrInboundReceiptCrossCompanyReference", err)
	}
}

func TestUpdateReceipt_RejectsCrossCompanyVendor(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	foreignVendorID := seedOtherCompanyVendor(t, db)
	_, err = UpdateReceipt(db, fx.CompanyID, out.ID, UpdateReceiptInput{
		VendorID: &foreignVendorID,
	})
	if !isErr(err, ErrInboundReceiptCrossCompanyReference) {
		t.Fatalf("got %v want ErrInboundReceiptCrossCompanyReference", err)
	}
}

// ── Row-lock sanity (Postgres-only; documented no-op on SQLite) ──────────────

// The in-memory SQLite test DB is single-writer, so FOR UPDATE is a
// no-op there and a concurrent-flip race cannot be meaningfully
// reproduced. The production contract — `applyLockForUpdate` emits
// `SELECT ... FOR UPDATE` on PostgreSQL inside the PostReceipt /
// VoidReceipt transactions — is verified by code inspection
// (loadReceiptForUpdate applies the clause) and by the same pattern
// used across the codebase (see customer_credit_service.go /
// gateway_dispute_service.go which skip their race tests on SQLite
// for the identical reason).
//
// This sentinel test explicitly records that the behaviour is locked
// at the code level; removing `applyLockForUpdate` from
// loadReceiptForUpdate will silently pass SQLite tests but regress
// production concurrency safety. Phrased as a skip so the intent is
// visible in test output.
func TestPostReceipt_ConcurrencyLock_DocumentedOnSQLiteSkip(t *testing.T) {
	t.Skip("SELECT ... FOR UPDATE on receipts is applied by loadReceiptForUpdate via applyLockForUpdate; SQLite is single-writer so cannot be race-tested here. Verified on PostgreSQL by code inspection and by the same idiom used in customer_credit_service.")
}

// ── helper ───────────────────────────────────────────────────────────────────

func isErr(got, want error) bool {
	if got == nil || want == nil {
		return got == want
	}
	// errors.Is used via simple unwrap walk to avoid importing errors
	// pkg into every test file twice (already imported above via
	// fmt.Errorf usage in production code).
	type wrappedErr interface{ Unwrap() error }
	for cur := got; cur != nil; {
		if cur == want {
			return true
		}
		if w, ok := cur.(wrappedErr); ok {
			cur = w.Unwrap()
			continue
		}
		return false
	}
	return false
}
