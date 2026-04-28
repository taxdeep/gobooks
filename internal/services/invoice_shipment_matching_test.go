// 遵循project_guide.md
package services

// invoice_shipment_matching_test.go — Phase I slice I.5 tests.
//
// Locks four contracts:
//
//  1. Happy path (flag=on, matched line): a posted Invoice with a
//     non-nil shipment_line_id closes the corresponding open
//     waiting_for_invoice row. Status → 'closed', resolved_*
//     fields populated.
//
//  2. Unmatched line (flag=on, no shipment_line_id): an Invoice
//     line without ShipmentLineID posts cleanly; no WFI row is
//     touched. Non-stock / service lines run through AR + Revenue
//     only, as in I.4.
//
//  3. Cross-company rejection: an Invoice line referencing a
//     ShipmentLine owned by another company is refused with
//     ErrInvoiceShipmentLineCrossCompany. No WFI row is closed,
//     no JE is persisted (tx rolls back).
//
//  4. Void reopens: voiding an Invoice that had closed a WFI row
//     flips that row back to 'open', clearing resolved_*. The
//     shipment's goods are again "shipped but not invoiced".

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

// testI5DB is the full fixture: shipment + invoice + WFI + all the
// satellite tables invoice_void touches (PaymentTransaction,
// SettlementAllocation, CreditNote*, TaskInvoiceSource).
func testI5DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:i5_%s?mode=memory&cache=shared", t.Name())
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
		&models.Shipment{},
		&models.ShipmentLine{},
		&models.WaitingForInvoiceItem{},
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

type i5Fixture struct {
	CompanyID          uint
	CustomerID         uint
	ItemID             uint
	WarehouseID        uint
	ARAccountID        uint
	RevenueAccountID   uint
	InventoryAccountID uint
	COGSAccountID      uint
}

func seedI5Fixture(t *testing.T, db *gorm.DB) i5Fixture {
	t.Helper()
	co := models.Company{
		Name:                    "i5-co",
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
		ShipmentRequired:        true, // I.5 operates under flag=on
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
	wh := models.Warehouse{CompanyID: co.ID, Name: "Main", Code: "MAIN", IsActive: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}
	item := models.ProductService{CompanyID: co.ID, Name: "Widget",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID,
		InventoryAccountID: &inv.ID, COGSAccountID: &cogs.ID, IsActive: true}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}
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
		IdempotencyKey: fmt.Sprintf("i5_test_seed:%d", co.ID),
	}); err != nil {
		t.Fatalf("pre-stock: %v", err)
	}
	return i5Fixture{
		CompanyID: co.ID, CustomerID: customer.ID, ItemID: item.ID,
		WarehouseID:        wh.ID,
		ARAccountID:        ar.ID,
		RevenueAccountID:   rev.ID,
		InventoryAccountID: *item.InventoryAccountID,
		COGSAccountID:      *item.COGSAccountID,
	}
}

// createAndPostShipmentForI5 creates a shipment with one stock-item
// line of qty=7, posts it (flag=on → forms COGS + WFI), and returns
// the ShipmentLine ID for the invoice to match against.
func createAndPostShipmentForI5(t *testing.T, db *gorm.DB, fx i5Fixture) uint {
	t.Helper()
	out, err := CreateShipment(db, CreateShipmentInput{
		CompanyID:      fx.CompanyID,
		ShipmentNumber: "SHIP-I5",
		CustomerID:     &fx.CustomerID,
		WarehouseID:    fx.WarehouseID,
		ShipDate:       time.Now().UTC(),
		Lines: []CreateShipmentLineInput{
			{SortOrder: 1, ProductServiceID: fx.ItemID, Qty: decimal.NewFromInt(7), Unit: "ea"},
		},
	})
	if err != nil {
		t.Fatalf("create shipment: %v", err)
	}
	if _, err := PostShipment(db, fx.CompanyID, out.ID, "admin@test", nil); err != nil {
		t.Fatalf("post shipment: %v", err)
	}
	// Reload with lines so caller can pick up line ID.
	var reloaded models.Shipment
	db.Preload("Lines").First(&reloaded, out.ID)
	if len(reloaded.Lines) != 1 {
		t.Fatalf("expected 1 shipment line, got %d", len(reloaded.Lines))
	}
	return reloaded.Lines[0].ID
}

func seedI5Invoice(t *testing.T, db *gorm.DB, fx i5Fixture, qty decimal.Decimal, unitPrice decimal.Decimal, shipmentLineID *uint) uint {
	t.Helper()
	lineNet := qty.Mul(unitPrice).RoundBank(2)
	invoice := models.Invoice{
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
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	line := models.InvoiceLine{
		CompanyID:        fx.CompanyID,
		InvoiceID:        invoice.ID,
		SortOrder:        1,
		ProductServiceID: &fx.ItemID,
		Description:      "Widget",
		Qty:              qty,
		UnitPrice:        unitPrice,
		LineNet:          lineNet,
		LineTax:          decimal.Zero,
		LineTotal:        lineNet,
		ShipmentLineID:   shipmentLineID,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatalf("seed invoice line: %v", err)
	}
	return invoice.ID
}

// ── Happy path: matched line closes WFI ─────────────────────────────────────

func TestPostInvoice_I5_MatchedLineClosesWFI(t *testing.T) {
	db := testI5DB(t)
	fx := seedI5Fixture(t, db)
	shipLineID := createAndPostShipmentForI5(t, db, fx)

	// WFI should be open after shipment post.
	var wfi models.WaitingForInvoiceItem
	if err := db.Where("company_id = ? AND shipment_line_id = ?",
		fx.CompanyID, shipLineID).First(&wfi).Error; err != nil {
		t.Fatalf("load WFI: %v", err)
	}
	if wfi.Status != models.WaitingForInvoiceStatusOpen {
		t.Fatalf("pre-invoice WFI status: got %q want open", wfi.Status)
	}

	invID := seedI5Invoice(t, db, fx,
		decimal.NewFromInt(7), decimal.NewFromFloat(10.00), &shipLineID)
	if err := PostInvoice(db, fx.CompanyID, invID, "tester", nil); err != nil {
		t.Fatalf("post invoice: %v", err)
	}

	// WFI closed with resolution fields populated.
	db.First(&wfi, wfi.ID)
	if wfi.Status != models.WaitingForInvoiceStatusClosed {
		t.Fatalf("post-invoice WFI status: got %q want closed", wfi.Status)
	}
	if wfi.ResolvedInvoiceID == nil || *wfi.ResolvedInvoiceID != invID {
		t.Fatalf("WFI resolved_invoice_id: got %v want %d", wfi.ResolvedInvoiceID, invID)
	}
	if wfi.ResolvedInvoiceLineID == nil || *wfi.ResolvedInvoiceLineID == 0 {
		t.Fatalf("WFI resolved_invoice_line_id not set")
	}
	if wfi.ResolvedAt == nil {
		t.Fatalf("WFI resolved_at not set")
	}
}

// ── Unmatched line posts cleanly without touching WFI ───────────────────────

func TestPostInvoice_I5_UnmatchedLine_WFIUntouched(t *testing.T) {
	db := testI5DB(t)
	fx := seedI5Fixture(t, db)
	shipLineID := createAndPostShipmentForI5(t, db, fx)

	// Invoice line with ShipmentLineID=nil — legit scenario: the
	// Invoice includes a fee line that was never shipped.
	invID := seedI5Invoice(t, db, fx,
		decimal.NewFromInt(1), decimal.NewFromFloat(25.00), nil)
	if err := PostInvoice(db, fx.CompanyID, invID, "tester", nil); err != nil {
		t.Fatalf("post invoice: %v", err)
	}

	// WFI still open — no match occurred.
	var wfi models.WaitingForInvoiceItem
	if err := db.Where("company_id = ? AND shipment_line_id = ?",
		fx.CompanyID, shipLineID).First(&wfi).Error; err != nil {
		t.Fatalf("load WFI: %v", err)
	}
	if wfi.Status != models.WaitingForInvoiceStatusOpen {
		t.Fatalf("WFI status after unmatched invoice: got %q want open", wfi.Status)
	}
}

// ── Cross-company: ShipmentLine from another company rejected ───────────────

func TestPostInvoice_I5_CrossCompanyShipmentLine_Rejected(t *testing.T) {
	db := testI5DB(t)
	fxA := seedI5Fixture(t, db)

	// Seed a second company and post a shipment for it, capturing its
	// shipment_line_id. Note: second company's seed re-uses the same
	// seed function which reuses the "i5-co" name — fine, the GORM
	// row is distinct by ID. We only need its ShipmentLine.
	var fxB i5Fixture
	{
		co := models.Company{
			Name:                    "i5-co-B",
			EntityType:              models.EntityTypeIncorporated,
			BusinessType:            models.BusinessTypeRetail,
			Industry:                models.IndustryRetail,
			IncorporatedDate:        "2024-01-01",
			FiscalYearEnd:           "12-31",
			BusinessNumber:          "001",
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
			ShipmentRequired:        true,
		}
		if err := db.Create(&co).Error; err != nil {
			t.Fatalf("seed co-B: %v", err)
		}
		customer := models.Customer{CompanyID: co.ID, Name: "Customer B"}
		db.Create(&customer)
		rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev",
			RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
		inv := models.Account{CompanyID: co.ID, Code: "1300", Name: "Inv",
			RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
		cogs := models.Account{CompanyID: co.ID, Code: "5000", Name: "COGS",
			RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
		for _, a := range []*models.Account{&rev, &inv, &cogs} {
			db.Create(a)
		}
		wh := models.Warehouse{CompanyID: co.ID, Name: "B-MAIN", Code: "BMAIN", IsActive: true}
		db.Create(&wh)
		itemB := models.ProductService{CompanyID: co.ID, Name: "Widget B",
			Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID,
			InventoryAccountID: &inv.ID, COGSAccountID: &cogs.ID, IsActive: true}
		itemB.ApplyTypeDefaults()
		db.Create(&itemB)
		if _, err := inventory.ReceiveStock(db, inventory.ReceiveStockInput{
			CompanyID: co.ID, ItemID: itemB.ID, WarehouseID: wh.ID,
			Quantity: decimal.NewFromInt(50), MovementDate: time.Now().UTC(),
			UnitCost: decimal.NewFromFloat(4.00), ExchangeRate: decimal.NewFromInt(1),
			SourceType: "test_seed", IdempotencyKey: fmt.Sprintf("i5_cc_seed:%d", co.ID),
		}); err != nil {
			t.Fatalf("pre-stock B: %v", err)
		}
		fxB = i5Fixture{CompanyID: co.ID, CustomerID: customer.ID, ItemID: itemB.ID, WarehouseID: wh.ID}
	}
	shipLineB := createAndPostShipmentForI5(t, db, fxB)

	// Now seed an invoice for company A with shipment_line_id pointing
	// at the company-B shipment line.
	invID := seedI5Invoice(t, db, fxA,
		decimal.NewFromInt(7), decimal.NewFromFloat(10.00), &shipLineB)
	err := PostInvoice(db, fxA.CompanyID, invID, "tester", nil)
	if err == nil {
		t.Fatalf("expected cross-company rejection")
	}
	if !isErr(err, ErrInvoiceShipmentLineCrossCompany) {
		t.Fatalf("got %v want ErrInvoiceShipmentLineCrossCompany", err)
	}

	// Tx rolled back: invoice still draft.
	var invRow models.Invoice
	db.First(&invRow, invID)
	if invRow.Status != models.InvoiceStatusDraft {
		t.Fatalf("status: got %q want draft (tx should roll back)", invRow.Status)
	}
	// Company B's WFI still open.
	var wfiB models.WaitingForInvoiceItem
	db.Where("shipment_line_id = ?", shipLineB).First(&wfiB)
	if wfiB.Status != models.WaitingForInvoiceStatusOpen {
		t.Fatalf("co-B WFI after rejection: got %q want open", wfiB.Status)
	}
}

// ── Flag-off with shipment_line_id is rejected ──────────────────────────────

func TestPostInvoice_I5_ShipmentLineUnderFlagOff_Rejected(t *testing.T) {
	db := testI5DB(t)
	fx := seedI5Fixture(t, db)
	shipLineID := createAndPostShipmentForI5(t, db, fx)

	// Flip flag OFF after shipment post.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("shipment_required", false).Error; err != nil {
		t.Fatalf("flip flag off: %v", err)
	}

	invID := seedI5Invoice(t, db, fx,
		decimal.NewFromInt(7), decimal.NewFromFloat(10.00), &shipLineID)
	err := PostInvoice(db, fx.CompanyID, invID, "tester", nil)
	if err == nil {
		t.Fatalf("expected rejection under flag=off")
	}
	if !isErr(err, ErrInvoiceShipmentLineInFlagOffContext) {
		t.Fatalf("got %v want ErrInvoiceShipmentLineInFlagOffContext", err)
	}
}

// ── Void reopens WFI ────────────────────────────────────────────────────────

func TestVoidInvoice_I5_ReopensWFI(t *testing.T) {
	db := testI5DB(t)
	fx := seedI5Fixture(t, db)
	shipLineID := createAndPostShipmentForI5(t, db, fx)

	invID := seedI5Invoice(t, db, fx,
		decimal.NewFromInt(7), decimal.NewFromFloat(10.00), &shipLineID)
	if err := PostInvoice(db, fx.CompanyID, invID, "tester", nil); err != nil {
		t.Fatalf("post: %v", err)
	}

	// Confirm WFI closed by the invoice.
	var wfi models.WaitingForInvoiceItem
	db.Where("shipment_line_id = ?", shipLineID).First(&wfi)
	if wfi.Status != models.WaitingForInvoiceStatusClosed {
		t.Fatalf("pre-void WFI status: got %q want closed", wfi.Status)
	}

	if err := VoidInvoice(db, fx.CompanyID, invID, "tester", nil); err != nil {
		t.Fatalf("void: %v", err)
	}

	// WFI reopened; resolved_* cleared. Use a FRESH struct because
	// GORM's First does not zero fields already populated from an
	// earlier Scan when the DB value is NULL.
	var reopened models.WaitingForInvoiceItem
	db.First(&reopened, wfi.ID)
	if reopened.Status != models.WaitingForInvoiceStatusOpen {
		t.Fatalf("post-void WFI status: got %q want open", reopened.Status)
	}
	if reopened.ResolvedInvoiceID != nil {
		t.Fatalf("WFI resolved_invoice_id not cleared: %v", *reopened.ResolvedInvoiceID)
	}
	if reopened.ResolvedInvoiceLineID != nil {
		t.Fatalf("WFI resolved_invoice_line_id not cleared: %v", *reopened.ResolvedInvoiceLineID)
	}
	if reopened.ResolvedAt != nil {
		t.Fatalf("WFI resolved_at not cleared")
	}
}

// ── Double-invoice attempt: second invoice for same shipment line rejected ──

func TestPostInvoice_I5_DoubleMatchOnSameShipmentLine_Rejected(t *testing.T) {
	db := testI5DB(t)
	fx := seedI5Fixture(t, db)
	shipLineID := createAndPostShipmentForI5(t, db, fx)

	inv1ID := seedI5Invoice(t, db, fx,
		decimal.NewFromInt(7), decimal.NewFromFloat(10.00), &shipLineID)
	if err := PostInvoice(db, fx.CompanyID, inv1ID, "tester", nil); err != nil {
		t.Fatalf("post inv1: %v", err)
	}

	// Second invoice attempts to match the same shipment line — WFI
	// already closed, so there is no open row to close.
	inv2ID := seedI5Invoice(t, db, fx,
		decimal.NewFromInt(7), decimal.NewFromFloat(10.00), &shipLineID)
	err := PostInvoice(db, fx.CompanyID, inv2ID, "tester", nil)
	if err == nil {
		t.Fatalf("expected rejection on duplicate match")
	}
	if !isErr(err, ErrWaitingForInvoiceNotFound) {
		t.Fatalf("got %v want ErrWaitingForInvoiceNotFound", err)
	}
}
