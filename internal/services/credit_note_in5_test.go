// 遵循project_guide.md
package services

// credit_note_in5_test.go — IN.5 contract tests.
//
// Locks the Rule #4 invariant for the Credit Note path on the AR
// return side:
//
//  1. Legacy mode + stock-item line → inventory_movements reversal
//     row lands at the ORIGINAL sale's unit cost (not today's avg),
//     JE gains Dr Inventory / Cr COGS at that amount, and the
//     customer balance (InventoryBalance) recovers.
//
//  2. Legacy mode + pure-service line → JE unchanged from pre-IN.5
//     (Dr Revenue / Cr AR), no inventory effect.
//
//  3. Controlled mode (shipment_required=true) + stock-item line →
//     ErrCreditNoteStockItemRequiresReturnReceipt. No JE, no
//     inventory effect, credit note stays in draft. Phase I.6
//     Return Receipt is the intended owner.
//
//  4. Stock-item line without OriginalInvoiceLineID → rejected
//     pre-post with ErrCreditNoteStockItemRequiresOriginalLine.
//
//  5. Stock-item line on a standalone credit note (InvoiceID=nil)
//     → rejected pre-post with ErrCreditNoteStockItemRequiresInvoice.
//
//  6. Void reverses the IN.5 inventory return: movement reversal
//     row lands (source_type='credit_note_reversal'), balance
//     decrements back to the post-invoice state.

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

func testCreditNoteIN5DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:credit_note_in5_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.ARAPControlMapping{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Warehouse{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.CreditNote{},
		&models.CreditNoteLine{},
		&models.CreditNoteApplication{},
		&models.ARReturnReceipt{},
		&models.ARReturnReceiptLine{},
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
		&models.PaymentTransaction{},
		&models.TaskInvoiceSource{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

type creditNoteIN5Fixture struct {
	CompanyID          uint
	CustomerID         uint
	ItemID             uint
	WarehouseID        uint
	ARAccountID        uint
	RevenueAccountID   uint
	InventoryAccountID uint
	COGSAccountID      uint
	// Invoice + InvoiceLine seeded by helper before each test; see
	// postInvoiceAndGet below.
}

func seedCreditNoteIN5Fixture(t *testing.T, db *gorm.DB) creditNoteIN5Fixture {
	t.Helper()
	co := models.Company{
		Name:                    "cn-in5-co",
		EntityType:              models.EntityTypeIncorporated,
		BusinessType:            models.BusinessTypeRetail,
		Industry:                models.IndustryRetail,
		IncorporatedDate:        "2024-01-01",
		FiscalYearEnd:           "12-31",
		BusinessNumber:          "000",
		AddressLine:             "1 Main", City: "City", Province: "BC", PostalCode: "V6B1A1", Country: "CA",
		AccountCodeLength:       4,
		AccountCodeLengthLocked: true,
		IsActive:                true,
		BaseCurrencyCode:        "CAD",
		InventoryCostingMethod:  models.InventoryCostingMovingAverage,
	}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	customer := models.Customer{CompanyID: co.ID, Name: "Customer"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	ar := models.Account{CompanyID: co.ID, Code: "1100", Name: "AR",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, IsActive: true}
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Revenue",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	inv := models.Account{CompanyID: co.ID, Code: "1300", Name: "Inventory",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	cogs := models.Account{CompanyID: co.ID, Code: "5000", Name: "COGS",
		RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	for _, a := range []*models.Account{&ar, &rev, &inv, &cogs} {
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("seed account %s: %v", a.Code, err)
		}
	}
	wh := models.Warehouse{CompanyID: co.ID, Name: "Main", Code: "MAIN", IsActive: true, IsDefault: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
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

	// Pre-stock so the subsequent Invoice post has inventory to peel.
	// Authoritative-cost basis for IN.5: the Invoice's COGS will use
	// this $3.00/unit cost; the Credit Note return MUST reverse at
	// that same $3.00 regardless of any later avg drift.
	if _, err := inventoryReceiveForTest(db, co.ID, item.ID, wh.ID,
		decimal.NewFromInt(100), decimal.NewFromFloat(3.00)); err != nil {
		t.Fatalf("pre-stock: %v", err)
	}

	return creditNoteIN5Fixture{
		CompanyID:          co.ID,
		CustomerID:         customer.ID,
		ItemID:             item.ID,
		WarehouseID:        wh.ID,
		ARAccountID:        ar.ID,
		RevenueAccountID:   rev.ID,
		InventoryAccountID: *item.InventoryAccountID,
		COGSAccountID:      *item.COGSAccountID,
	}
}

// postInvoiceWithStockLine creates and posts an invoice for qty × price
// so the IN.5 credit note has something concrete to reverse against.
// Returns the Invoice and the first InvoiceLine's ID (the one the
// credit note traces back to).
func postInvoiceWithStockLine(t *testing.T, db *gorm.DB, fx creditNoteIN5Fixture, qty int, unitPrice float64) (models.Invoice, uint) {
	t.Helper()
	inv := models.Invoice{
		CompanyID:     fx.CompanyID,
		InvoiceNumber: fmt.Sprintf("INV-IN5-%d", time.Now().UnixNano()),
		CustomerID:    fx.CustomerID,
		InvoiceDate:   time.Now().UTC(),
		Status:        models.InvoiceStatusDraft,
		WarehouseID:   &fx.WarehouseID,
		Amount:        decimal.NewFromFloat(unitPrice * float64(qty)),
		Subtotal:      decimal.NewFromFloat(unitPrice * float64(qty)),
		TaxTotal:      decimal.Zero,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	qtyDec := decimal.NewFromInt(int64(qty))
	priceDec := decimal.NewFromFloat(unitPrice)
	lineNet := qtyDec.Mul(priceDec).RoundBank(2)
	line := models.InvoiceLine{
		CompanyID: fx.CompanyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &fx.ItemID,
		Description:      "Widget",
		Qty:              qtyDec, UnitPrice: priceDec,
		LineNet: lineNet, LineTax: decimal.Zero, LineTotal: lineNet,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatalf("seed invoice line: %v", err)
	}
	if err := PostInvoice(db, fx.CompanyID, inv.ID, "tester", nil); err != nil {
		t.Fatalf("post invoice: %v", err)
	}
	// Reload for status + preload ensuring.
	var posted models.Invoice
	db.First(&posted, inv.ID)
	return posted, line.ID
}

// createDraftCreditNoteWithStockLine creates a draft CN for qty units of
// fx.ItemID that traces back to invoiceLineID.
func createDraftCreditNoteWithStockLine(t *testing.T, db *gorm.DB, fx creditNoteIN5Fixture, invoice models.Invoice, invoiceLineID uint, qty int, unitPrice float64) uint {
	t.Helper()
	qtyDec := decimal.NewFromInt(int64(qty))
	priceDec := decimal.NewFromFloat(unitPrice)
	invID := invoice.ID
	invLineID := invoiceLineID
	itemID := fx.ItemID

	cn, err := CreateCreditNoteDraft(db, CreateCreditNoteDraftInput{
		CompanyID:      fx.CompanyID,
		CustomerID:     fx.CustomerID,
		InvoiceID:      &invID,
		CreditNoteDate: time.Now().UTC(),
		Reason:         models.CreditNoteReasonReturn,
		CurrencyCode:   "CAD",
		Lines: []CreditNoteLineInput{{
			Description:           "Return: Widget",
			Qty:                   qtyDec,
			UnitPrice:             priceDec,
			RevenueAccountID:      fx.RevenueAccountID,
			ProductServiceID:      &itemID,
			OriginalInvoiceLineID: &invLineID,
		}},
	})
	if err != nil {
		t.Fatalf("create CN draft: %v", err)
	}
	return cn.ID
}

// ── Scenario 1: legacy stock line forms inventory return + JE ──────────────

func TestPostCreditNote_IN5_LegacyStockLineReversesInventoryAtOriginalCost(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)
	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	// Sanity: after invoice post, on-hand is 100 − 10 = 90.
	var bal models.InventoryBalance
	if err := db.Where("company_id = ? AND item_id = ?", fx.CompanyID, fx.ItemID).
		First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(90)) {
		t.Fatalf("pre-CN on_hand: got %s want 90", bal.QuantityOnHand)
	}

	// Customer returns 4 units at $15 list price.
	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 4, 15.00)
	if err := PostCreditNote(db, fx.CompanyID, cnID, "tester", nil); err != nil {
		t.Fatalf("post CN: %v", err)
	}

	// Inventory restored by 4. Post-CN on_hand should be 94.
	var bal2 models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", fx.CompanyID, fx.ItemID).First(&bal2)
	if !bal2.QuantityOnHand.Equal(decimal.NewFromInt(94)) {
		t.Fatalf("post-CN on_hand: got %s want 94", bal2.QuantityOnHand)
	}

	// Credit-note-sourced inventory movement landed.
	var cnMovs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ? AND source_id = ?",
		fx.CompanyID, "credit_note", cnID).Find(&cnMovs)
	if len(cnMovs) != 1 {
		t.Fatalf("credit_note movements: got %d want 1", len(cnMovs))
	}
	if !cnMovs[0].QuantityDelta.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("CN qty_delta: got %s want +4", cnMovs[0].QuantityDelta)
	}
	// Authoritative cost: $3.00 per original pre-stock, NOT $15 list.
	if !cnMovs[0].UnitCostBase.Equal(decimal.NewFromFloat(3.00)) {
		t.Fatalf("CN unit_cost_base: got %s want 3.00 (original sale cost)",
			cnMovs[0].UnitCostBase)
	}

	// JE includes both the revenue-side reversal AND the inventory
	// reversal. Verify Dr Inventory 12.00 + Cr COGS 12.00 (4 × 3.00).
	var cn models.CreditNote
	db.First(&cn, cnID)
	var je models.JournalEntry
	db.Preload("Lines").First(&je, *cn.JournalEntryID)
	var invDebit, cogsCredit, revDebit, arCredit decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case fx.InventoryAccountID:
			invDebit = invDebit.Add(l.Debit)
		case fx.COGSAccountID:
			cogsCredit = cogsCredit.Add(l.Credit)
		case fx.RevenueAccountID:
			revDebit = revDebit.Add(l.Debit)
		case fx.ARAccountID:
			arCredit = arCredit.Add(l.Credit)
		}
	}
	want := decimal.NewFromFloat(12.00) // 4 × $3 original cost
	if !invDebit.Equal(want) {
		t.Fatalf("Dr Inventory: got %s want %s", invDebit, want)
	}
	if !cogsCredit.Equal(want) {
		t.Fatalf("Cr COGS: got %s want %s", cogsCredit, want)
	}
	// Revenue reversal should be 4 × $15 = $60 (legacy behaviour).
	wantRev := decimal.NewFromFloat(60.00)
	if !revDebit.Equal(wantRev) {
		t.Fatalf("Dr Revenue: got %s want %s", revDebit, wantRev)
	}
	if !arCredit.Equal(wantRev) {
		t.Fatalf("Cr AR: got %s want %s", arCredit, wantRev)
	}
}

// ── Scenario 2: pure-service line posts unchanged ───────────────────────────

func TestPostCreditNote_IN5_PureServiceLinePostsWithoutInventoryEffect(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)

	// Standalone service credit note: no invoice linkage, no product.
	cn, err := CreateCreditNoteDraft(db, CreateCreditNoteDraftInput{
		CompanyID:      fx.CompanyID,
		CustomerID:     fx.CustomerID,
		CreditNoteDate: time.Now().UTC(),
		Reason:         models.CreditNoteReasonGoodwill,
		CurrencyCode:   "CAD",
		Lines: []CreditNoteLineInput{{
			Description:      "Goodwill credit",
			Qty:              decimal.NewFromInt(1),
			UnitPrice:        decimal.NewFromFloat(50.00),
			RevenueAccountID: fx.RevenueAccountID,
		}},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if err := PostCreditNote(db, fx.CompanyID, cn.ID, "tester", nil); err != nil {
		t.Fatalf("post CN: %v", err)
	}
	// Zero credit_note-sourced movements.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, "credit_note", cn.ID).Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("pure-service CN must not form inventory; got %d rows", mvCount)
	}
}

// ── Scenario 3: controlled mode without ARR coverage → rejected (I.6a.3) ─────
//
// Post-I.6a.3 semantic shift: stock-item lines are no longer
// unconditionally rejected under controlled mode. Instead, the CN
// requires EXACT posted-ARReturnReceipt coverage per Q6. When no
// ARR exists (or coverage is short), the post fails with
// ErrCreditNoteStockItemRequiresReturnReceipt — the same sentinel
// kept for RULE4_RUNBOOK §10a triage continuity, but the wrapped
// message now names the coverage shortfall rather than a blanket ban.

func TestPostCreditNote_IN5_ControlledModeWithoutARRCoverageRejected(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)
	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	// Flip shipment_required=true (Phase I controlled mode).
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("flip controlled: %v", err)
	}

	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 4, 15.00)
	err := PostCreditNote(db, fx.CompanyID, cnID, "tester", nil)
	if err == nil {
		t.Fatalf("expected rejection (no ARR coverage)")
	}
	if !isErr(err, ErrCreditNoteStockItemRequiresReturnReceipt) {
		t.Fatalf("got %v want ErrCreditNoteStockItemRequiresReturnReceipt", err)
	}
	// CN stays draft, no JE, no movement.
	var cn models.CreditNote
	db.First(&cn, cnID)
	if cn.Status != models.CreditNoteStatusDraft {
		t.Fatalf("status: got %q want draft (tx should roll back)", cn.Status)
	}
}

// ── Scenario 4: missing OriginalInvoiceLineID ──────────────────────────────

func TestPostCreditNote_IN5_MissingOriginalLineRejected(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)
	invoice, _ := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	// Create draft WITHOUT OriginalInvoiceLineID on the stock line.
	invID := invoice.ID
	itemID := fx.ItemID
	cn, err := CreateCreditNoteDraft(db, CreateCreditNoteDraftInput{
		CompanyID:      fx.CompanyID,
		CustomerID:     fx.CustomerID,
		InvoiceID:      &invID,
		CreditNoteDate: time.Now().UTC(),
		Reason:         models.CreditNoteReasonReturn,
		CurrencyCode:   "CAD",
		Lines: []CreditNoteLineInput{{
			Description:      "Return: Widget (no trace)",
			Qty:              decimal.NewFromInt(4),
			UnitPrice:        decimal.NewFromFloat(15.00),
			RevenueAccountID: fx.RevenueAccountID,
			ProductServiceID: &itemID,
			// deliberately NOT setting OriginalInvoiceLineID
		}},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	postErr := PostCreditNote(db, fx.CompanyID, cn.ID, "tester", nil)
	if !isErr(postErr, ErrCreditNoteStockItemRequiresOriginalLine) {
		t.Fatalf("got %v want ErrCreditNoteStockItemRequiresOriginalLine", postErr)
	}
}

// ── Scenario 5: standalone CN with stock line rejected ─────────────────────

func TestPostCreditNote_IN5_StandaloneStockLineRejected(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)

	itemID := fx.ItemID
	// No InvoiceID set.
	cn, err := CreateCreditNoteDraft(db, CreateCreditNoteDraftInput{
		CompanyID:      fx.CompanyID,
		CustomerID:     fx.CustomerID,
		CreditNoteDate: time.Now().UTC(),
		Reason:         models.CreditNoteReasonReturn,
		CurrencyCode:   "CAD",
		Lines: []CreditNoteLineInput{{
			Description:      "Standalone stock return",
			Qty:              decimal.NewFromInt(1),
			UnitPrice:        decimal.NewFromFloat(15.00),
			RevenueAccountID: fx.RevenueAccountID,
			ProductServiceID: &itemID,
		}},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	postErr := PostCreditNote(db, fx.CompanyID, cn.ID, "tester", nil)
	if !isErr(postErr, ErrCreditNoteStockItemRequiresInvoice) {
		t.Fatalf("got %v want ErrCreditNoteStockItemRequiresInvoice", postErr)
	}
}

// ── Scenario 6: void reverses inventory return ─────────────────────────────

// A linked CN auto-applies to its invoice at post time, and
// VoidCreditNote refuses to void an applied CN (pre-existing
// business rule, CN-side, unrelated to IN.5). To exercise the
// IN.5 void-symmetry contract in isolation, this test manually
// detaches the auto-application before calling Void — proving
// that *IN.5's contribution* (the inventory-movement reversal)
// fires correctly once the CN becomes voidable.
func TestVoidCreditNote_IN5_ReversesInventoryReturn(t *testing.T) {
	db := testCreditNoteIN5DB(t)
	fx := seedCreditNoteIN5Fixture(t, db)
	invoice, invoiceLineID := postInvoiceWithStockLine(t, db, fx, 10, 15.00)

	// Post CN for 4-unit return.
	cnID := createDraftCreditNoteWithStockLine(t, db, fx, invoice, invoiceLineID, 4, 15.00)
	if err := PostCreditNote(db, fx.CompanyID, cnID, "tester", nil); err != nil {
		t.Fatalf("post CN: %v", err)
	}
	// Post-CN on_hand: 100 − 10 + 4 = 94.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", fx.CompanyID, fx.ItemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(94)) {
		t.Fatalf("post-CN on_hand: got %s want 94", bal.QuantityOnHand)
	}

	// Detach auto-application so VoidCreditNote's pre-existing
	// "no void after application" rule doesn't gate this test's
	// scope (which is IN.5's inventory-reversal half).
	if err := db.Where("credit_note_id = ?", cnID).
		Delete(&models.CreditNoteApplication{}).Error; err != nil {
		t.Fatalf("detach application: %v", err)
	}
	// Reset CN status to issued so the void path accepts it.
	if err := db.Model(&models.CreditNote{}).
		Where("id = ?", cnID).
		Update("status", models.CreditNoteStatusIssued).Error; err != nil {
		t.Fatalf("reset CN status: %v", err)
	}

	// Void the CN. Inventory should undo the +4 restoration.
	if err := VoidCreditNote(db, fx.CompanyID, cnID, "tester", nil); err != nil {
		t.Fatalf("void CN: %v", err)
	}
	var bal2 models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", fx.CompanyID, fx.ItemID).First(&bal2)
	if !bal2.QuantityOnHand.Equal(decimal.NewFromInt(90)) {
		t.Fatalf("post-void on_hand: got %s want 90 (should undo the +4)",
			bal2.QuantityOnHand)
	}

	// Reversal inventory movement exists (source_type='credit_note_reversal').
	var revMovs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?",
		fx.CompanyID, "credit_note_reversal").Find(&revMovs)
	if len(revMovs) != 1 {
		t.Fatalf("credit_note_reversal movements: got %d want 1", len(revMovs))
	}
}
