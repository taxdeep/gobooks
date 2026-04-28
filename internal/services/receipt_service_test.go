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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// osReadDir / osReadFile are local aliases so the inventory import-
// guard test (TestInventoryPackage_DoesNotImportAccountingPackages)
// can walk production files without bringing extra stdlib calls into
// the public surface of this test file.
var (
	osReadDir  = os.ReadDir
	osReadFile = os.ReadFile
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
		&models.InventoryLayerConsumption{},
		&models.InventoryTrackingConsumption{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
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

// ── H.3 flag-off regression lock (byte-identical to H.2) ─────────────────────

// Under `receipt_required=false` (the default), PostReceipt remains a
// pure document-layer status flip. This locks the H.3 exit condition
// "Under receipt_required=false, Phase G behavior is byte-identical"
// for the Receipt side: legacy companies continue with Bill-forms-
// inventory and Receipt produces no parallel inventory / GL effect.
func TestPostReceipt_FlagOff_ByteIdenticalToH2_NoInventoryOrGL(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	// Default is flag=false; make it explicit.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", false).Error; err != nil {
		t.Fatalf("set flag off: %v", err)
	}
	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	posted, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posted.JournalEntryID != nil {
		t.Fatalf("flag=false post must not link a JE; got %d", *posted.JournalEntryID)
	}
	assertNoInventoryOrGLEffect(t, db, fx.CompanyID)
}

// ── H.3 flag-on happy path: the 3-layer chain materialises ───────────────────

// seedGRIRAccount creates a liability account and configures it as
// the company's GR/IR clearing account, satisfying the precondition
// for PostReceipt under flag=true.
func seedGRIRAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	acct := models.Account{
		CompanyID:         companyID,
		Code:              "2100",
		Name:              "GR/IR Clearing",
		RootAccountType:   models.RootLiability,
		DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive:          true,
	}
	if err := db.Create(&acct).Error; err != nil {
		t.Fatalf("seed GR/IR account: %v", err)
	}
	if err := db.Model(&models.Company{}).
		Where("id = ?", companyID).
		Update("gr_ir_clearing_account_id", acct.ID).Error; err != nil {
		t.Fatalf("wire GR/IR: %v", err)
	}
	return acct.ID
}

// seedInventoryAccountOnItem configures the product's InventoryAccountID
// so the Dr Inventory side of the GR/IR journal is postable.
func seedInventoryAccountOnItem(t *testing.T, db *gorm.DB, companyID, itemID uint) uint {
	t.Helper()
	acct := models.Account{
		CompanyID:         companyID,
		Code:              "1300",
		Name:              "Inventory Asset",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailInventory,
		IsActive:          true,
	}
	if err := db.Create(&acct).Error; err != nil {
		t.Fatalf("seed inventory account: %v", err)
	}
	if err := db.Model(&models.ProductService{}).
		Where("id = ?", itemID).
		Update("inventory_account_id", acct.ID).Error; err != nil {
		t.Fatalf("wire inventory account on item: %v", err)
	}
	return acct.ID
}

// flagOnFixture primes a company fully for PostReceipt flag=true: rail
// flipped ON, GR/IR account configured, InventoryAccountID set on the
// item. Returns the three relevant account IDs for assertions.
type flagOnAccountSetup struct {
	InventoryAccountID uint
	GRIRAccountID      uint
}

func seedFlagOnFixture(t *testing.T, db *gorm.DB, fx receiptFixture) flagOnAccountSetup {
	t.Helper()
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip receipt_required: %v", err)
	}
	invID := seedInventoryAccountOnItem(t, db, fx.CompanyID, fx.ItemID)
	grirID := seedGRIRAccount(t, db, fx.CompanyID)
	return flagOnAccountSetup{InventoryAccountID: invID, GRIRAccountID: grirID}
}

func TestPostReceipt_FlagOn_ProducesReceiveTruthAndGRIRJournal(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	accts := seedFlagOnFixture(t, db, fx)

	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	posted, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// (1) Layer one: document — status flipped + JE linked.
	if posted.Status != models.ReceiptStatusPosted {
		t.Fatalf("status: got %q", posted.Status)
	}
	if posted.JournalEntryID == nil {
		t.Fatalf("JE not linked; flag=true with stock lines must link JE")
	}

	// (2) Layer two: receive truth — one inventory_movements row per
	// stock-item line, source_type='receipt', source_id=receipt.ID.
	var mvs []models.InventoryMovement
	if err := db.Where("company_id = ? AND source_type = ? AND source_id = ?",
		fx.CompanyID, "receipt", out.ID).Find(&mvs).Error; err != nil {
		t.Fatalf("load movements: %v", err)
	}
	if len(mvs) != 1 {
		t.Fatalf("movements: got %d want 1 (one per stock line)", len(mvs))
	}
	if !mvs[0].QuantityDelta.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("qty_delta: got %s want 10", mvs[0].QuantityDelta)
	}

	// (3) Layer three: inventory effect — balance updated by the
	// inventory module's internal machinery.
	var bal models.InventoryBalance
	if err := db.Where("company_id = ? AND item_id = ?",
		fx.CompanyID, fx.ItemID).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("on_hand: got %s want 10", bal.QuantityOnHand)
	}

	// (4) Parallel: JE posted, balanced. Dr Inventory (per line), Cr GR/IR (total).
	var je models.JournalEntry
	if err := db.Preload("Lines").
		Where("id = ?", *posted.JournalEntryID).
		First(&je).Error; err != nil {
		t.Fatalf("load JE: %v", err)
	}
	if je.SourceType != models.LedgerSourceReceipt || je.SourceID != out.ID {
		t.Fatalf("JE source linkage: got %s/%d want receipt/%d", je.SourceType, je.SourceID, out.ID)
	}
	var invDebit, grirCredit decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case accts.InventoryAccountID:
			invDebit = invDebit.Add(l.Debit)
		case accts.GRIRAccountID:
			grirCredit = grirCredit.Add(l.Credit)
		}
	}
	wantValue := decimal.NewFromFloat(50.00) // qty 10 × unit_cost 5.00
	if !invDebit.Equal(wantValue) {
		t.Fatalf("inventory debit: got %s want %s", invDebit, wantValue)
	}
	if !grirCredit.Equal(wantValue) {
		t.Fatalf("GR/IR credit: got %s want %s", grirCredit, wantValue)
	}
}

func TestPostReceipt_FlagOn_ButGRIRNotConfigured_Rejected(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	// Flag ON, inventory account set, but NO GR/IR configured.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip flag: %v", err)
	}
	seedInventoryAccountOnItem(t, db, fx.CompanyID, fx.ItemID)

	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if !isErr(err, ErrGRIRAccountNotConfigured) {
		t.Fatalf("got %v want ErrGRIRAccountNotConfigured", err)
	}
	// Receipt must remain in draft — tx rolled back.
	var stillDraft models.Receipt
	db.First(&stillDraft, out.ID)
	if stillDraft.Status != models.ReceiptStatusDraft {
		t.Fatalf("status: got %q want draft (tx should roll back)", stillDraft.Status)
	}
}

func TestPostReceipt_FlagOn_InventoryAccountMissing_Rejected(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	// Flag ON, GR/IR configured, but item has no inventory_account_id.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip flag: %v", err)
	}
	seedGRIRAccount(t, db, fx.CompanyID)

	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if !isErr(err, ErrInboundReceiptInventoryAccountMissing) {
		t.Fatalf("got %v want ErrInboundReceiptInventoryAccountMissing", err)
	}
}

func TestPostReceipt_FlagOn_ServiceOnlyReceipt_NoJEBooked(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	accts := seedFlagOnFixture(t, db, fx)
	_ = accts

	// Create a service (non-stock) item and use it on the receipt
	// instead of the default stock item.
	svc := models.ProductService{
		CompanyID: fx.CompanyID, Name: "Consulting",
		Type: models.ProductServiceTypeService, IsActive: true,
	}
	svc.ApplyTypeDefaults()
	if err := db.Create(&svc).Error; err != nil {
		t.Fatalf("seed service: %v", err)
	}
	in := buildSimpleCreateInput(fx)
	in.Lines[0].ProductServiceID = svc.ID
	in.Lines[0].Description = "Delivery labour"
	out, err := CreateReceipt(db, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	posted, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	// Non-stock lines → no movements, no JE booked.
	if posted.JournalEntryID != nil {
		t.Fatalf("service-only receipt must not link JE; got %d", *posted.JournalEntryID)
	}
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ?", fx.CompanyID, "receipt").
		Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("service-only receipt produced %d movements want 0", mvCount)
	}
}

func TestVoidReceipt_FlagOn_ReversesJournalAndMovements(t *testing.T) {
	db := testReceiptDocDB(t)
	fx := seedReceiptFixture(t, db)
	seedFlagOnFixture(t, db, fx)

	out, err := CreateReceipt(db, buildSimpleCreateInput(fx))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	posted, err := PostReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	origJEID := *posted.JournalEntryID

	voided, err := VoidReceipt(db, fx.CompanyID, out.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if voided.Status != models.ReceiptStatusVoided {
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

	// Reversal JE exists, ReversedFromID -> orig.
	var revJEs []models.JournalEntry
	db.Where("reversed_from_id = ?", origJEID).Find(&revJEs)
	if len(revJEs) != 1 {
		t.Fatalf("reversal JEs: got %d want 1", len(revJEs))
	}

	// Reversal inventory movement exists.
	var revMvs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?",
		fx.CompanyID, "receipt_reversal").Find(&revMvs)
	if len(revMvs) != 1 {
		t.Fatalf("reversal movements: got %d want 1", len(revMvs))
	}

	// Balance back to zero.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?",
		fx.CompanyID, fx.ItemID).First(&bal)
	if !bal.QuantityOnHand.IsZero() {
		t.Fatalf("balance after void: got %s want 0", bal.QuantityOnHand)
	}
}

// ── Hard Rule #3: GL-agnostic inventory package ──────────────────────────────

// Locks H.0 Hard Rule #3 at the compile-boundary level. The inventory
// package must remain production-import-free of accounting,
// chart-of-accounts, and journal-entry packages. The rule is stated
// at the semantic level; this test maps the rule to concrete
// production-file import inspection. If the project ever renames or
// relocates those packages, update the forbidden-prefix list, not
// the rule itself.
func TestInventoryPackage_DoesNotImportAccountingPackages(t *testing.T) {
	const invDir = "inventory"
	entries, err := osReadDir(invDir)
	if err != nil {
		t.Fatalf("read inventory package dir: %v", err)
	}
	forbidden := []string{
		"balanciz/internal/services/accounts",
		"balanciz/internal/accounting",
		"balanciz/internal/ledger",
	}
	// Permitted within inventory: models (for InventoryMovement,
	// InventoryBalance, InventoryCostLayer shapes), decimal, gorm.
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Skip test files — H.0 rule is for production code.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := invDir + "/" + e.Name()
		data, err := osReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, bad := range forbidden {
			if strings.Contains(string(data), `"`+bad+`"`) {
				t.Fatalf("%s imports forbidden package %s — violates Phase H Hard Rule #3 (INVENTORY_MODULE_API.md §Phase H)", path, bad)
			}
		}
	}
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
