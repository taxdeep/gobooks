// 遵循project_guide.md
package services

// invoice_post_shipment_required_test.go — Phase I slice I.4 tests.
//
// Locks two contracts:
//
//  1. Flag-off regression: when shipment_required=false, PostInvoice
//     continues to form COGS and produce inventory movements exactly
//     as in the pre-Phase-I codepath. Byte-identical legacy behavior
//     is the I.4 exit condition for all existing companies.
//
//  2. Flag-on decoupling: when shipment_required=true, PostInvoice
//     books AR + Revenue (+ Tax) only. No CreateSaleMovements call,
//     no BuildCOGSFragments call — the JE has zero COGS lines and
//     zero inventory_movements rows attributable to this invoice.
//     The Shipment already did all of that at ship time (I.3); doing
//     it again here would double-consume inventory and double-book
//     COGS.
//
// Void under flag=true is implicitly covered because
// ReverseSaleMovements is safe on zero-movement invoices (no-op
// loop). An explicit test is included so any future refactor that
// adds a mandatory movement-exists check trips the assertion.

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
	"balanciz/internal/services/inventory"
)

// testInvoicePostI4DB spins an in-memory DB with the full footprint
// PostInvoice / VoidInvoice needs, including inventory tables so the
// flag-off case can materialise cost layers and the flag-on case can
// assert their absence.
func testInvoicePostI4DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:i4_%s?mode=memory&cache=shared", t.Name())
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
		&models.CreditNote{},
		&models.CreditNoteApplication{},
		&models.SettlementAllocation{},
		&models.TaskInvoiceSource{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type i4Fixture struct {
	CompanyID          uint
	CustomerID         uint
	ItemID             uint
	WarehouseID        uint
	ARAccountID        uint
	RevenueAccountID   uint
	InventoryAccountID uint
	COGSAccountID      uint
}

func seedI4Fixture(t *testing.T, db *gorm.DB) i4Fixture {
	t.Helper()
	co := models.Company{
		Name:                    "i4-co",
		EntityType:              models.EntityTypeIncorporated,
		BusinessType:            models.BusinessTypeRetail,
		Industry:                models.IndustryRetail,
		IncorporatedDate:        "2024-01-01",
		FiscalYearEnd:           "12-31",
		BusinessNumber:          "000",
		AddressLine:             "1 Main",
		City:                    "City",
		Province:                "BC",
		PostalCode:              "V6B1A1",
		Country:                 "CA",
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
	ar := models.Account{
		CompanyID: co.ID, Code: "1100", Name: "AR",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable,
		IsActive: true,
	}
	rev := models.Account{
		CompanyID: co.ID, Code: "4000", Name: "Revenue",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue",
		IsActive: true,
	}
	inv := models.Account{
		CompanyID: co.ID, Code: "1300", Name: "Inventory",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory,
		IsActive: true,
	}
	cogs := models.Account{
		CompanyID: co.ID, Code: "5000", Name: "COGS",
		RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold,
		IsActive: true,
	}
	for _, a := range []*models.Account{&ar, &rev, &inv, &cogs} {
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("seed account %s: %v", a.Code, err)
		}
	}
	wh := models.Warehouse{CompanyID: co.ID, Name: "Main", Code: "MAIN", IsActive: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}
	item := models.ProductService{
		CompanyID: co.ID, Name: "Widget",
		Type:               models.ProductServiceTypeInventory,
		RevenueAccountID:   rev.ID,
		InventoryAccountID: &inv.ID,
		COGSAccountID:      &cogs.ID,
		IsActive:           true,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}

	// Pre-stock 100 units at $3.00 via inventory.ReceiveStock.
	if _, err := inventory.ReceiveStock(db, inventory.ReceiveStockInput{
		CompanyID:      co.ID,
		ItemID:         item.ID,
		WarehouseID:    wh.ID,
		Quantity:       decimal.NewFromInt(100),
		MovementDate:   time.Now().UTC(),
		UnitCost:       decimal.NewFromFloat(3.00),
		ExchangeRate:   decimal.NewFromInt(1),
		SourceType:     "test_seed",
		SourceID:       0,
		IdempotencyKey: fmt.Sprintf("i4_test_seed:%d", co.ID),
	}); err != nil {
		t.Fatalf("pre-stock: %v", err)
	}

	return i4Fixture{
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

func seedI4Invoice(t *testing.T, db *gorm.DB, fx i4Fixture, qty decimal.Decimal, unitPrice decimal.Decimal) uint {
	t.Helper()
	lineNet := qty.Mul(unitPrice).RoundBank(2)
	inv := models.Invoice{
		CompanyID:     fx.CompanyID,
		InvoiceNumber: fmt.Sprintf("INV-%d", time.Now().UnixNano()),
		CustomerID:    fx.CustomerID,
		InvoiceDate:   time.Now().UTC(),
		Status:        models.InvoiceStatusDraft,
		WarehouseID:   &fx.WarehouseID,
		Amount:        lineNet,
		Subtotal:      lineNet,
		TaxTotal:      decimal.Zero,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	line := models.InvoiceLine{
		CompanyID:        fx.CompanyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: &fx.ItemID,
		Description:      "Widget",
		Qty:              qty,
		UnitPrice:        unitPrice,
		LineNet:          lineNet,
		LineTax:          decimal.Zero,
		LineTotal:        lineNet,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatalf("seed invoice line: %v", err)
	}
	return inv.ID
}

// ── Flag-off regression: byte-identical COGS path ───────────────────────────

func TestPostInvoice_FlagOff_ProducesCOGSAndInventoryMovement(t *testing.T) {
	db := testInvoicePostI4DB(t)
	fx := seedI4Fixture(t, db)
	// Explicit default: flag off. Confirm the legacy Invoice-forms-COGS
	// path still runs end-to-end.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", false).Error; err != nil {
		t.Fatalf("set flag off: %v", err)
	}
	invID := seedI4Invoice(t, db, fx,
		decimal.NewFromInt(5), decimal.NewFromFloat(10.00))

	if err := PostInvoice(db, fx.CompanyID, invID, "tester", nil); err != nil {
		t.Fatalf("post: %v", err)
	}

	// JE has 4 sides expected: AR debit, Revenue credit, COGS debit,
	// Inventory credit. Tax is zero so no tax line.
	var je models.JournalEntry
	db.Where("source_type = ? AND source_id = ?", models.LedgerSourceInvoice, invID).
		Preload("Lines").First(&je)
	var cogsDebit, invCredit decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case fx.COGSAccountID:
			cogsDebit = cogsDebit.Add(l.Debit)
		case fx.InventoryAccountID:
			invCredit = invCredit.Add(l.Credit)
		}
	}
	wantCOGS := decimal.NewFromFloat(15.00) // 5 @ $3 moving-avg
	if !cogsDebit.Equal(wantCOGS) {
		t.Fatalf("COGS debit under flag=off: got %s want %s", cogsDebit, wantCOGS)
	}
	if !invCredit.Equal(wantCOGS) {
		t.Fatalf("Inventory credit under flag=off: got %s want %s", invCredit, wantCOGS)
	}

	// One inventory movement attributed to the invoice.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, "invoice", invID).
		Count(&mvCount)
	if mvCount != 1 {
		t.Fatalf("invoice movements under flag=off: got %d want 1", mvCount)
	}
}

// ── Flag-on decoupling: AR + Revenue only ───────────────────────────────────

func TestPostInvoice_FlagOn_NoCOGSNoMovement(t *testing.T) {
	db := testInvoicePostI4DB(t)
	fx := seedI4Fixture(t, db)
	// Flip rail ON.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("set flag on: %v", err)
	}
	invID := seedI4Invoice(t, db, fx,
		decimal.NewFromInt(5), decimal.NewFromFloat(10.00))

	if err := PostInvoice(db, fx.CompanyID, invID, "tester", nil); err != nil {
		t.Fatalf("post: %v", err)
	}

	// JE exists — AR + Revenue only.
	var je models.JournalEntry
	db.Where("source_type = ? AND source_id = ?", models.LedgerSourceInvoice, invID).
		Preload("Lines").First(&je)
	var arDebit, revenueCredit, cogsDebit, invCredit decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case fx.ARAccountID:
			arDebit = arDebit.Add(l.Debit)
		case fx.RevenueAccountID:
			revenueCredit = revenueCredit.Add(l.Credit)
		case fx.COGSAccountID:
			cogsDebit = cogsDebit.Add(l.Debit)
		case fx.InventoryAccountID:
			invCredit = invCredit.Add(l.Credit)
		}
	}
	want := decimal.NewFromFloat(50.00) // 5 @ $10
	if !arDebit.Equal(want) {
		t.Fatalf("AR debit: got %s want %s", arDebit, want)
	}
	if !revenueCredit.Equal(want) {
		t.Fatalf("Revenue credit: got %s want %s", revenueCredit, want)
	}
	// I.4 invariant: no COGS, no Inventory-asset credit.
	if !cogsDebit.IsZero() {
		t.Fatalf("flag=on must NOT book COGS; got %s", cogsDebit)
	}
	if !invCredit.IsZero() {
		t.Fatalf("flag=on must NOT credit Inventory; got %s", invCredit)
	}

	// No inventory movement attributed to the invoice.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, "invoice", invID).
		Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("flag=on invoice must NOT produce movements; got %d", mvCount)
	}

	// Pre-stock untouched: 100 on hand still.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", fx.CompanyID, fx.ItemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("on_hand under flag=on: got %s want 100 (pre-stock untouched)", bal.QuantityOnHand)
	}
}

// ── Flag-on void: no movement reversal needed ───────────────────────────────

func TestVoidInvoice_FlagOn_NoMovementReversal(t *testing.T) {
	db := testInvoicePostI4DB(t)
	fx := seedI4Fixture(t, db)
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", true).Error; err != nil {
		t.Fatalf("set flag on: %v", err)
	}
	invID := seedI4Invoice(t, db, fx,
		decimal.NewFromInt(5), decimal.NewFromFloat(10.00))

	if err := PostInvoice(db, fx.CompanyID, invID, "tester", nil); err != nil {
		t.Fatalf("post: %v", err)
	}
	if err := VoidInvoice(db, fx.CompanyID, invID, "tester", nil); err != nil {
		t.Fatalf("void: %v", err)
	}

	// No reversal inventory movement since none existed to reverse.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type IN (?, ?)",
			fx.CompanyID, "invoice", "invoice_reversal").
		Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("flag=on void must NOT produce invoice-movement rows; got %d", mvCount)
	}

	// Invoice status flipped to voided.
	var inv models.Invoice
	db.First(&inv, invID)
	if inv.Status != models.InvoiceStatusVoided {
		t.Fatalf("status: got %q want voided", inv.Status)
	}
}
