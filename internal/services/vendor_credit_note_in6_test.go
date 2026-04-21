// 遵循project_guide.md
package services

// vendor_credit_note_in6_test.go — IN.6a contract tests.
//
// Locks the Rule #4 invariant for the Vendor Credit Note path on
// the AP return side:
//
//  1. Legacy mode + stock-item line (full-qty return) →
//     inventory_movements reversal row lands at the ORIGINAL bill's
//     unit cost (not today's avg), JE gains Dr Offset / Cr Inventory
//     at that amount, and InventoryBalance decrements.
//
//  2. Legacy mode + pure-service line → JE unchanged from pre-IN.6a
//     (Dr AP / Cr Offset), no inventory effect.
//
//  3. Controlled mode (receipt_required=true) + stock-item line →
//     ErrVendorCreditNoteStockItemRequiresReturnReceipt. No JE, no
//     inventory effect, VCN stays in draft. Future Vendor Return
//     Receipt is the intended owner.
//
//  4. Stock-item line without OriginalBillLineID → rejected
//     pre-post with ErrVendorCreditNoteStockItemRequiresOriginalLine.
//
//  5. Stock-item line on a standalone VCN (BillID=nil) → rejected
//     pre-post with ErrVendorCreditNoteStockItemRequiresBill.
//
//  6. Stock-item line with qty < original bill line qty → rejected
//     with ErrVendorCreditNotePartialReturnNotSupported. The inventory
//     module's outflow verbs don't accept a caller-supplied cost, so
//     partial returns are deferred to a follow-up slice that either
//     extends IssueStock or adds a Vendor Return Receipt path.
//
//  7. Header-only (no lines) VCN → legacy Dr AP / Cr Offset path
//     untouched. Back-compat guarantee for VCNs predating IN.6a.

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
	"gobooks/internal/services/inventory"
)

func testVendorCreditNoteIN6DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:vcn_in6_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Vendor{},
		&models.Account{},
		&models.ARAPControlMapping{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Warehouse{},
		&models.Bill{},
		&models.BillLine{},
		&models.VendorCreditNote{},
		&models.VendorCreditNoteLine{},
		&models.APCreditApplication{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.InventoryLayerConsumption{},
		&models.InventoryTrackingConsumption{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

type vcnIN6Fixture struct {
	CompanyID          uint
	VendorID           uint
	ItemID             uint
	WarehouseID        uint
	APAccountID        uint
	OffsetAccountID    uint
	InventoryAccountID uint
	ExpenseAccountID   uint
}

func seedVCNIN6Fixture(t *testing.T, db *gorm.DB) vcnIN6Fixture {
	t.Helper()
	co := models.Company{
		Name:                   "vcn-in6-co",
		EntityType:             models.EntityTypeIncorporated,
		BusinessType:           models.BusinessTypeRetail,
		Industry:               models.IndustryRetail,
		IncorporatedDate:       "2024-01-01",
		FiscalYearEnd:          "12-31",
		BusinessNumber:         "000",
		AddressLine:            "1 Main", City: "City", Province: "BC", PostalCode: "V6B1A1", Country: "CA",
		AccountCodeLength:       4,
		AccountCodeLengthLocked: true,
		IsActive:                true,
		BaseCurrencyCode:        "CAD",
		InventoryCostingMethod:  models.InventoryCostingMovingAverage,
	}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	vend := models.Vendor{CompanyID: co.ID, Name: "Vendor"}
	if err := db.Create(&vend).Error; err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	ap := models.Account{CompanyID: co.ID, Code: "2100", Name: "AP",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailAccountsPayable, IsActive: true}
	offset := models.Account{CompanyID: co.ID, Code: "5200", Name: "Purchase Returns",
		RootAccountType: models.RootCostOfSales, DetailAccountType: "purchase_returns", IsActive: true}
	inv := models.Account{CompanyID: co.ID, Code: "1300", Name: "Inventory",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	exp := models.Account{CompanyID: co.ID, Code: "6000", Name: "Expense",
		RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	for _, a := range []*models.Account{&ap, &offset, &inv, &exp} {
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("seed account %s: %v", a.Code, err)
		}
	}
	wh := models.Warehouse{CompanyID: co.ID, Name: "Main", Code: "MAIN", IsActive: true, IsDefault: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}

	// Stock item; revenue account isn't used by Bill path but required
	// by validator.
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Revenue",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	if err := db.Create(&rev).Error; err != nil {
		t.Fatalf("seed revenue account: %v", err)
	}
	cogs := models.Account{CompanyID: co.ID, Code: "5000", Name: "COGS",
		RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	if err := db.Create(&cogs).Error; err != nil {
		t.Fatalf("seed cogs account: %v", err)
	}

	item := models.ProductService{
		CompanyID: co.ID, Name: "Widget",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID,
		InventoryAccountID: &inv.ID, COGSAccountID: &cogs.ID, IsActive: true,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}

	return vcnIN6Fixture{
		CompanyID:          co.ID,
		VendorID:           vend.ID,
		ItemID:             item.ID,
		WarehouseID:        wh.ID,
		APAccountID:        ap.ID,
		OffsetAccountID:    offset.ID,
		InventoryAccountID: *item.InventoryAccountID,
		ExpenseAccountID:   exp.ID,
	}
}

// postBillWithStockLine creates + posts a Bill for qty × price so the
// IN.6a VCN test has something concrete to reverse against. Returns
// the posted Bill and the first BillLine's ID.
func postBillWithStockLine(t *testing.T, db *gorm.DB, fx vcnIN6Fixture, qty int, unitPrice float64) (models.Bill, uint) {
	t.Helper()
	qtyDec := decimal.NewFromInt(int64(qty))
	priceDec := decimal.NewFromFloat(unitPrice)
	lineNet := qtyDec.Mul(priceDec).RoundBank(2)

	bill := models.Bill{
		CompanyID:    fx.CompanyID,
		BillNumber:   fmt.Sprintf("BILL-IN6-%d", time.Now().UnixNano()),
		VendorID:     fx.VendorID,
		BillDate:     time.Now().UTC(),
		Status:       models.BillStatusDraft,
		CurrencyCode: "",
		ExchangeRate: decimal.NewFromInt(1),
		Subtotal:     lineNet,
		Amount:       lineNet,
		BalanceDue:   lineNet,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	line := models.BillLine{
		CompanyID:        fx.CompanyID,
		BillID:           bill.ID,
		SortOrder:        1,
		ProductServiceID: &fx.ItemID,
		Description:      "Widget",
		Qty:              qtyDec,
		UnitPrice:        priceDec,
		ExpenseAccountID: &fx.ExpenseAccountID,
		LineNet:          lineNet,
		LineTax:          decimal.Zero,
		LineTotal:        lineNet,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatalf("seed bill line: %v", err)
	}
	if err := PostBill(db, fx.CompanyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("post bill: %v", err)
	}
	var posted models.Bill
	db.First(&posted, bill.ID)
	return posted, line.ID
}

// createDraftVCNWithStockLine creates a draft VCN with a single stock
// line that traces back to billLineID. Returns the VCN ID.
func createDraftVCNWithStockLine(t *testing.T, db *gorm.DB, fx vcnIN6Fixture, bill models.Bill, billLineID uint, qty int, unitPrice float64) uint {
	t.Helper()
	qtyDec := decimal.NewFromInt(int64(qty))
	priceDec := decimal.NewFromFloat(unitPrice)
	billID := bill.ID
	billLine := billLineID
	itemID := fx.ItemID
	apID := fx.APAccountID
	offsetID := fx.OffsetAccountID

	vcn, err := CreateVendorCreditNote(db, fx.CompanyID, VendorCreditNoteInput{
		VendorID:        fx.VendorID,
		BillID:          &billID,
		CreditNoteDate:  time.Now().UTC(),
		CurrencyCode:    "CAD",
		ExchangeRate:    decimal.NewFromInt(1),
		APAccountID:     &apID,
		OffsetAccountID: &offsetID,
		Lines: []VendorCreditNoteLineInput{{
			Description:        "Return: Widget",
			Qty:                qtyDec,
			UnitPrice:          priceDec,
			ProductServiceID:   &itemID,
			OriginalBillLineID: &billLine,
		}},
	})
	if err != nil {
		t.Fatalf("create VCN draft: %v", err)
	}
	return vcn.ID
}

// ── Scenario 1: legacy stock line reverses inventory at original cost ─────────

func TestPostVendorCreditNote_IN6a_LegacyStockLineReversesInventoryAtOriginalCost(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)

	// Sanity: after bill post, on-hand is 10 at $20.
	var bal models.InventoryBalance
	if err := db.Where("company_id = ? AND item_id = ?", fx.CompanyID, fx.ItemID).
		First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("pre-VCN on_hand: got %s want 10", bal.QuantityOnHand)
	}

	// Return full 10 units at $20.
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 10, 20.00)
	if err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil); err != nil {
		t.Fatalf("post VCN: %v", err)
	}

	// Inventory decremented by 10. Post-VCN on_hand should be 0.
	var bal2 models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", fx.CompanyID, fx.ItemID).First(&bal2)
	if !bal2.QuantityOnHand.Equal(decimal.Zero) {
		t.Fatalf("post-VCN on_hand: got %s want 0", bal2.QuantityOnHand)
	}

	// A reversal inventory_movements row landed with source_type='vendor_credit_note'.
	var vcnMovs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ? AND source_id = ?",
		fx.CompanyID, "vendor_credit_note", vcnID).Find(&vcnMovs)
	if len(vcnMovs) != 1 {
		t.Fatalf("vendor_credit_note movements: got %d want 1", len(vcnMovs))
	}
	if !vcnMovs[0].QuantityDelta.Equal(decimal.NewFromInt(-10)) {
		t.Fatalf("VCN qty_delta: got %s want -10", vcnMovs[0].QuantityDelta)
	}
	// Authoritative cost: $20 per original bill line (not today's
	// avg, though in this test WA hasn't drifted).
	if vcnMovs[0].UnitCostBase == nil || !vcnMovs[0].UnitCostBase.Equal(decimal.NewFromFloat(20.00)) {
		got := "<nil>"
		if vcnMovs[0].UnitCostBase != nil {
			got = vcnMovs[0].UnitCostBase.String()
		}
		t.Fatalf("VCN unit_cost_base: got %s want 20.00", got)
	}

	// JE has Dr AP 200 + Cr Inventory 200 (net; Offset nets to zero
	// after aggregation of Cr Offset 200 / Dr Offset 200).
	var vcn models.VendorCreditNote
	db.First(&vcn, vcnID)
	var je models.JournalEntry
	db.Preload("Lines").First(&je, *vcn.JournalEntryID)
	var apDebit, invCredit, offsetNet decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case fx.APAccountID:
			apDebit = apDebit.Add(l.Debit).Sub(l.Credit)
		case fx.InventoryAccountID:
			invCredit = invCredit.Add(l.Credit).Sub(l.Debit)
		case fx.OffsetAccountID:
			offsetNet = offsetNet.Add(l.Debit).Sub(l.Credit)
		}
	}
	want := decimal.NewFromFloat(200.00)
	if !apDebit.Equal(want) {
		t.Fatalf("Dr AP: got %s want %s", apDebit, want)
	}
	if !invCredit.Equal(want) {
		t.Fatalf("Cr Inventory: got %s want %s", invCredit, want)
	}
	if !offsetNet.IsZero() {
		t.Fatalf("Offset should net to zero after aggregation: got %s", offsetNet)
	}
}

// ── Scenario 2: pure-service line posts unchanged ────────────────────────────

func TestPostVendorCreditNote_IN6a_PureServiceLinePostsWithoutInventoryEffect(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)

	// Seed a non-stock item.
	revID := uint(0)
	db.Model(&models.Account{}).Where("company_id = ? AND code = ?", fx.CompanyID, "4000").Select("id").Scan(&revID)
	svcItem := models.ProductService{
		CompanyID: fx.CompanyID, Name: "Consulting",
		Type: models.ProductServiceTypeService, RevenueAccountID: revID, IsActive: true,
	}
	svcItem.ApplyTypeDefaults()
	if err := db.Create(&svcItem).Error; err != nil {
		t.Fatalf("seed svc item: %v", err)
	}

	apID := fx.APAccountID
	offsetID := fx.OffsetAccountID
	itemID := svcItem.ID
	vcn, err := CreateVendorCreditNote(db, fx.CompanyID, VendorCreditNoteInput{
		VendorID:        fx.VendorID,
		CreditNoteDate:  time.Now().UTC(),
		CurrencyCode:    "CAD",
		APAccountID:     &apID,
		OffsetAccountID: &offsetID,
		Lines: []VendorCreditNoteLineInput{{
			Description:      "Consulting adjustment",
			Qty:              decimal.NewFromInt(1),
			UnitPrice:        decimal.NewFromFloat(50.00),
			ProductServiceID: &itemID,
		}},
	})
	if err != nil {
		t.Fatalf("create VCN draft: %v", err)
	}
	if err := PostVendorCreditNote(db, fx.CompanyID, vcn.ID, "tester", nil); err != nil {
		t.Fatalf("post VCN: %v", err)
	}

	// Zero vendor_credit_note-sourced movements.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, "vendor_credit_note", vcn.ID).Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("pure-service VCN must not form inventory; got %d rows", mvCount)
	}

	// JE is the legacy Dr AP 50 / Cr Offset 50.
	var posted models.VendorCreditNote
	db.First(&posted, vcn.ID)
	var je models.JournalEntry
	db.Preload("Lines").First(&je, *posted.JournalEntryID)
	var apDebit, offsetCredit decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case fx.APAccountID:
			apDebit = apDebit.Add(l.Debit)
		case fx.OffsetAccountID:
			offsetCredit = offsetCredit.Add(l.Credit)
		}
	}
	want := decimal.NewFromFloat(50.00)
	if !apDebit.Equal(want) || !offsetCredit.Equal(want) {
		t.Fatalf("Dr AP/Cr Offset: got %s/%s want %s/%s", apDebit, offsetCredit, want, want)
	}
}

// ── Scenario 3: controlled mode rejects stock-item line ──────────────────────

func TestPostVendorCreditNote_IN6a_ControlledModeRejectsStockLine(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)

	// Flip to controlled mode AFTER the bill post (legacy) so we keep
	// the original movement in place for the trace.
	if err := db.Model(&models.Company{}).Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip receipt_required: %v", err)
	}

	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 10, 20.00)

	err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil)
	if err == nil {
		t.Fatal("expected controlled-mode stock-item rejection")
	}
	if !errors.Is(err, ErrVendorCreditNoteStockItemRequiresReturnReceipt) {
		t.Fatalf("want ErrVendorCreditNoteStockItemRequiresReturnReceipt, got %v", err)
	}

	// No side-effects: no JE, no movements.
	var vcn models.VendorCreditNote
	db.First(&vcn, vcnID)
	if vcn.Status != models.VendorCreditNoteStatusDraft {
		t.Errorf("VCN status after reject: got %s want draft", vcn.Status)
	}
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, "vendor_credit_note", vcnID).Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("rejected post must not form inventory; got %d rows", mvCount)
	}
}

// ── Scenario 4: missing OriginalBillLineID rejected ──────────────────────────

func TestPostVendorCreditNote_IN6a_MissingOriginalLineRejected(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, _ := postBillWithStockLine(t, db, fx, 10, 20.00)

	billID := bill.ID
	itemID := fx.ItemID
	apID := fx.APAccountID
	offsetID := fx.OffsetAccountID
	vcn, err := CreateVendorCreditNote(db, fx.CompanyID, VendorCreditNoteInput{
		VendorID:        fx.VendorID,
		BillID:          &billID,
		CreditNoteDate:  time.Now().UTC(),
		CurrencyCode:    "CAD",
		APAccountID:     &apID,
		OffsetAccountID: &offsetID,
		Lines: []VendorCreditNoteLineInput{{
			Description:      "Return without trace",
			Qty:              decimal.NewFromInt(10),
			UnitPrice:        decimal.NewFromFloat(20.00),
			ProductServiceID: &itemID,
			// OriginalBillLineID deliberately nil
		}},
	})
	if err != nil {
		t.Fatalf("create VCN draft: %v", err)
	}

	err = PostVendorCreditNote(db, fx.CompanyID, vcn.ID, "tester", nil)
	if err == nil {
		t.Fatal("expected missing-trace rejection")
	}
	if !errors.Is(err, ErrVendorCreditNoteStockItemRequiresOriginalLine) {
		t.Fatalf("want ErrVendorCreditNoteStockItemRequiresOriginalLine, got %v", err)
	}
}

// ── Scenario 5: standalone stock VCN (no BillID) rejected ────────────────────

func TestPostVendorCreditNote_IN6a_StandaloneStockLineRejected(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)

	apID := fx.APAccountID
	offsetID := fx.OffsetAccountID
	itemID := fx.ItemID
	vcn, err := CreateVendorCreditNote(db, fx.CompanyID, VendorCreditNoteInput{
		VendorID:        fx.VendorID,
		BillID:          nil, // <-- standalone
		CreditNoteDate:  time.Now().UTC(),
		CurrencyCode:    "CAD",
		APAccountID:     &apID,
		OffsetAccountID: &offsetID,
		Lines: []VendorCreditNoteLineInput{{
			Description:      "Orphan stock credit",
			Qty:              decimal.NewFromInt(1),
			UnitPrice:        decimal.NewFromFloat(20.00),
			ProductServiceID: &itemID,
		}},
	})
	if err != nil {
		t.Fatalf("create VCN draft: %v", err)
	}

	err = PostVendorCreditNote(db, fx.CompanyID, vcn.ID, "tester", nil)
	if err == nil {
		t.Fatal("expected standalone-stock rejection")
	}
	if !errors.Is(err, ErrVendorCreditNoteStockItemRequiresBill) {
		t.Fatalf("want ErrVendorCreditNoteStockItemRequiresBill, got %v", err)
	}
}

// ── Scenario 6: partial-qty return rejected ──────────────────────────────────

func TestPostVendorCreditNote_IN6a_PartialReturnRejected(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)
	bill, billLineID := postBillWithStockLine(t, db, fx, 10, 20.00)

	// Try to return only 4 of the 10.
	vcnID := createDraftVCNWithStockLine(t, db, fx, bill, billLineID, 4, 20.00)

	err := PostVendorCreditNote(db, fx.CompanyID, vcnID, "tester", nil)
	if err == nil {
		t.Fatal("expected partial-return rejection")
	}
	if !errors.Is(err, ErrVendorCreditNotePartialReturnNotSupported) {
		t.Fatalf("want ErrVendorCreditNotePartialReturnNotSupported, got %v", err)
	}

	// No side-effects: original bill movement untouched (not yet
	// marked as reversed), no VCN-sourced movements.
	_, err2 := inventory.ReverseMovement(db, inventory.ReverseMovementInput{
		CompanyID:          fx.CompanyID,
		OriginalMovementID: 1, // sanity check: movement 1 must still be reversible
		MovementDate:       time.Now().UTC(),
		SourceType:         "test_probe",
		IdempotencyKey:     "in6a-partial-probe",
	})
	// If original was already reversed by our failed post, this second
	// probe would return ErrReversalAlreadyApplied. We allow other
	// errors (insufficient stock is expected since on_hand is only 10).
	if err2 != nil && errors.Is(err2, inventory.ErrReversalAlreadyApplied) {
		t.Fatal("original bill movement was reversed despite partial-return rejection — tx rollback broken")
	}
}

// ── Scenario 7: header-only VCN (no lines) keeps legacy posting ──────────────

func TestPostVendorCreditNote_IN6a_HeaderOnlyLegacyUnchanged(t *testing.T) {
	db := testVendorCreditNoteIN6DB(t)
	fx := seedVCNIN6Fixture(t, db)

	apID := fx.APAccountID
	offsetID := fx.OffsetAccountID
	vcn, err := CreateVendorCreditNote(db, fx.CompanyID, VendorCreditNoteInput{
		VendorID:        fx.VendorID,
		CreditNoteDate:  time.Now().UTC(),
		CurrencyCode:    "CAD",
		APAccountID:     &apID,
		OffsetAccountID: &offsetID,
		Amount:          decimal.NewFromFloat(75.00),
		// Lines: nil → legacy header-only
	})
	if err != nil {
		t.Fatalf("create header-only VCN: %v", err)
	}
	if err := PostVendorCreditNote(db, fx.CompanyID, vcn.ID, "tester", nil); err != nil {
		t.Fatalf("post header-only VCN: %v", err)
	}

	// Zero inventory movements.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, "vendor_credit_note", vcn.ID).Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("header-only VCN must not form inventory; got %d rows", mvCount)
	}

	// Legacy JE: Dr AP 75 / Cr Offset 75 only.
	var posted models.VendorCreditNote
	db.First(&posted, vcn.ID)
	var je models.JournalEntry
	db.Preload("Lines").First(&je, *posted.JournalEntryID)
	if len(je.Lines) != 2 {
		t.Fatalf("header-only JE lines: got %d want 2", len(je.Lines))
	}
}
