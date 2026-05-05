// 遵循project_guide.md
package services

// sales_order_invoice_tracking_test.go — SO↔Invoice tracking
// chain contract tests. Locks:
//
//  1. **Match by ProductServiceID + FIFO-remaining.** An invoice
//     with 2 lines for the same product → both get
//     sales_order_line_id pointing at the matching SO line (if it
//     has enough remaining qty).
//
//  2. **Partial apply → status = partially_invoiced + InvoicedQty /
//     InvoicedAmount incremented.**
//
//  3. **Full apply across multiple invoices → status =
//     fully_invoiced.**
//
//  4. **Reverse (void) rolls status back.** partially_invoiced →
//     confirmed when last invoice is voided.
//
//  5. **Standalone invoice (no SalesOrderID) is a no-op.** Neither
//     matching nor apply / reverse should touch anything.

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func soTrackingDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.SalesOrder{},
		&models.SalesOrderLine{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Quote{},
		&models.QuoteLine{},
		&models.CreditNote{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type soTrackingFixture struct {
	CompanyID  uint
	CustomerID uint
	ProductID1 uint
	ProductID2 uint
}

func seedSOTrackingFixture(t *testing.T, db *gorm.DB) soTrackingFixture {
	t.Helper()
	co := models.Company{Name: "SO Track Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	cust := models.Customer{CompanyID: co.ID, Name: "Tracking Cust"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	if err := db.Create(&rev).Error; err != nil {
		t.Fatal(err)
	}
	p1 := models.ProductService{CompanyID: co.ID, Name: "Widget",
		Type: models.ProductServiceTypeService, RevenueAccountID: rev.ID, IsActive: true}
	p1.ApplyTypeDefaults()
	if err := db.Create(&p1).Error; err != nil {
		t.Fatal(err)
	}
	p2 := models.ProductService{CompanyID: co.ID, Name: "Gizmo",
		Type: models.ProductServiceTypeService, RevenueAccountID: rev.ID, IsActive: true}
	p2.ApplyTypeDefaults()
	if err := db.Create(&p2).Error; err != nil {
		t.Fatal(err)
	}
	return soTrackingFixture{
		CompanyID: co.ID, CustomerID: cust.ID,
		ProductID1: p1.ID, ProductID2: p2.ID,
	}
}

// seedSOWithTwoLines creates a confirmed SO with two lines:
//   - Widget × 10 @ $50 = $500
//   - Gizmo × 5 @ $20 = $100
//
// Returns (soID, widgetLineID, gizmoLineID).
func seedSOWithTwoLines(t *testing.T, db *gorm.DB, fx soTrackingFixture) (uint, uint, uint) {
	t.Helper()
	widget := fx.ProductID1
	gizmo := fx.ProductID2
	so, err := CreateSalesOrder(db, fx.CompanyID, SalesOrderInput{
		CustomerID:   fx.CustomerID,
		OrderDate:    time.Now().UTC(),
		CurrencyCode: "CAD",
		Lines: []SalesOrderLineInput{
			{ProductServiceID: &widget, Quantity: decimal.NewFromInt(10),
				UnitPrice: decimal.NewFromInt(50)},
			{ProductServiceID: &gizmo, Quantity: decimal.NewFromInt(5),
				UnitPrice: decimal.NewFromInt(20)},
		},
	})
	if err != nil {
		t.Fatalf("CreateSalesOrder: %v", err)
	}
	if err := ConfirmSalesOrder(db, fx.CompanyID, so.ID); err != nil {
		t.Fatalf("ConfirmSalesOrder: %v", err)
	}
	var lines []models.SalesOrderLine
	if err := db.Where("sales_order_id = ?", so.ID).Order("sort_order asc").
		Find(&lines).Error; err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 SO lines, got %d", len(lines))
	}
	return so.ID, lines[0].ID, lines[1].ID
}

// seedDraftInvoice creates a draft invoice with the given SO link
// and lines. Lines carry no `sales_order_line_id` yet — the caller
// runs MatchInvoiceLinesToSalesOrder separately.
type invLineSpec struct {
	ProductID uint
	Qty       float64
	UnitPrice float64
}

func seedDraftInvoice(t *testing.T, db *gorm.DB, fx soTrackingFixture,
	soID uint, specs []invLineSpec) uint {
	t.Helper()
	var soPtr *uint
	if soID != 0 {
		soPtr = &soID
	}
	inv := models.Invoice{
		CompanyID:     fx.CompanyID,
		InvoiceNumber: "INV-SOT-001",
		CustomerID:    fx.CustomerID,
		InvoiceDate:   time.Now().UTC(),
		Status:        models.InvoiceStatusDraft,
		CurrencyCode:  "CAD",
		ExchangeRate:  decimal.NewFromInt(1),
		SalesOrderID:  soPtr,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	for i, sp := range specs {
		psID := sp.ProductID
		line := models.InvoiceLine{
			CompanyID:        fx.CompanyID,
			InvoiceID:        inv.ID,
			SortOrder:        uint(i + 1),
			ProductServiceID: &psID,
			Qty:              decimal.NewFromFloat(sp.Qty),
			UnitPrice:        decimal.NewFromFloat(sp.UnitPrice),
			LineNet:          decimal.NewFromFloat(sp.Qty * sp.UnitPrice).Round(2),
			LineTotal:        decimal.NewFromFloat(sp.Qty * sp.UnitPrice).Round(2),
		}
		if err := db.Create(&line).Error; err != nil {
			t.Fatalf("create invoice line: %v", err)
		}
	}
	return inv.ID
}

// ── Scenario 1: Match by PS + FIFO ──────────────────────────────────────────

func TestMatchInvoiceLinesToSalesOrder_HappyMatch(t *testing.T) {
	db := soTrackingDB(t)
	fx := seedSOTrackingFixture(t, db)
	soID, widgetSOLineID, gizmoSOLineID := seedSOWithTwoLines(t, db, fx)

	// Invoice for 4 widgets + 2 gizmos.
	invID := seedDraftInvoice(t, db, fx, soID, []invLineSpec{
		{ProductID: fx.ProductID1, Qty: 4, UnitPrice: 50},
		{ProductID: fx.ProductID2, Qty: 2, UnitPrice: 20},
	})

	if err := MatchInvoiceLinesToSalesOrder(db, fx.CompanyID, invID); err != nil {
		t.Fatalf("match: %v", err)
	}

	var lines []models.InvoiceLine
	db.Where("invoice_id = ?", invID).Order("sort_order asc").Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("lines: got %d want 2", len(lines))
	}
	if lines[0].SalesOrderLineID == nil || *lines[0].SalesOrderLineID != widgetSOLineID {
		t.Fatalf("widget line match: got %v want %d", lines[0].SalesOrderLineID, widgetSOLineID)
	}
	if lines[1].SalesOrderLineID == nil || *lines[1].SalesOrderLineID != gizmoSOLineID {
		t.Fatalf("gizmo line match: got %v want %d", lines[1].SalesOrderLineID, gizmoSOLineID)
	}
}

// ── Scenario 2: Partial apply → partially_invoiced ──────────────────────────

func TestApplyInvoicePostToSalesOrder_Partial(t *testing.T) {
	db := soTrackingDB(t)
	fx := seedSOTrackingFixture(t, db)
	soID, widgetSOLineID, _ := seedSOWithTwoLines(t, db, fx)

	invID := seedDraftInvoice(t, db, fx, soID, []invLineSpec{
		{ProductID: fx.ProductID1, Qty: 3, UnitPrice: 50}, // 3 of 10
	})
	if err := MatchInvoiceLinesToSalesOrder(db, fx.CompanyID, invID); err != nil {
		t.Fatalf("match: %v", err)
	}

	var inv models.Invoice
	db.Preload("Lines").First(&inv, invID)
	if err := ApplyInvoicePostToSalesOrder(db, inv); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var so models.SalesOrder
	db.First(&so, soID)
	if so.Status != models.SalesOrderStatusPartiallyInvoiced {
		t.Fatalf("status: got %s want partially_invoiced", so.Status)
	}
	if !so.InvoicedAmount.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("invoiced_amount: got %s want 150.00 (3 × $50)", so.InvoicedAmount)
	}
	var widgetLine models.SalesOrderLine
	db.First(&widgetLine, widgetSOLineID)
	if !widgetLine.InvoicedQty.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("widget InvoicedQty: got %s want 3", widgetLine.InvoicedQty)
	}
}

// ── Scenario 3: Two invoices summing to full → fully_invoiced ───────────────

func TestApplyInvoicePostToSalesOrder_FullyInvoiced_TwoInvoices(t *testing.T) {
	db := soTrackingDB(t)
	fx := seedSOTrackingFixture(t, db)
	soID, _, _ := seedSOWithTwoLines(t, db, fx)

	// Invoice 1: 10 widgets + 2 gizmos.
	inv1 := seedDraftInvoice(t, db, fx, soID, []invLineSpec{
		{ProductID: fx.ProductID1, Qty: 10, UnitPrice: 50},
		{ProductID: fx.ProductID2, Qty: 2, UnitPrice: 20},
	})
	if err := MatchInvoiceLinesToSalesOrder(db, fx.CompanyID, inv1); err != nil {
		t.Fatalf("match 1: %v", err)
	}
	var inv1Full models.Invoice
	db.Preload("Lines").First(&inv1Full, inv1)
	if err := ApplyInvoicePostToSalesOrder(db, inv1Full); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	var so1 models.SalesOrder
	db.First(&so1, soID)
	if so1.Status != models.SalesOrderStatusPartiallyInvoiced {
		t.Fatalf("mid-state status: got %s want partially_invoiced", so1.Status)
	}

	// Invoice 2: remaining 3 gizmos.
	inv2 := seedDraftInvoice(t, db, fx, soID, []invLineSpec{
		{ProductID: fx.ProductID2, Qty: 3, UnitPrice: 20},
	})
	// Invoice 2 needs a unique invoice number — override.
	db.Model(&models.Invoice{}).Where("id = ?", inv2).
		Update("invoice_number", "INV-SOT-002")
	if err := MatchInvoiceLinesToSalesOrder(db, fx.CompanyID, inv2); err != nil {
		t.Fatalf("match 2: %v", err)
	}
	var inv2Full models.Invoice
	db.Preload("Lines").First(&inv2Full, inv2)
	if err := ApplyInvoicePostToSalesOrder(db, inv2Full); err != nil {
		t.Fatalf("apply 2: %v", err)
	}

	var so2 models.SalesOrder
	db.First(&so2, soID)
	if so2.Status != models.SalesOrderStatusFullyInvoiced {
		t.Fatalf("final status: got %s want fully_invoiced", so2.Status)
	}
	// InvoicedAmount should equal SO total: 10×$50 + 5×$20 = $600.
	// Across the two invoices: inv1 = 500 + 40 = 540; inv2 = 60.
	if !so2.InvoicedAmount.Equal(decimal.NewFromInt(600)) {
		t.Fatalf("invoiced_amount: got %s want 600.00 (full SO total)",
			so2.InvoicedAmount)
	}
}

// ── Scenario 4: Reverse → rolls status back ─────────────────────────────────

func TestApplyInvoicePostToSalesOrder_FullyInvoiced_IgnoresDefaultBlankLine(t *testing.T) {
	db := soTrackingDB(t)
	fx := seedSOTrackingFixture(t, db)
	soID, _, _ := seedSOWithTwoLines(t, db, fx)

	if err := db.Where("sales_order_id = ? AND product_service_id = ?", soID, fx.ProductID2).
		Delete(&models.SalesOrderLine{}).Error; err != nil {
		t.Fatalf("remove second substantive line: %v", err)
	}
	blank := models.SalesOrderLine{
		SalesOrderID: soID,
		Quantity:     decimal.NewFromInt(1),
		UnitPrice:    decimal.Zero,
		LineNet:      decimal.Zero,
		TaxAmount:    decimal.Zero,
		LineTotal:    decimal.Zero,
		SortOrder:    99,
	}
	if err := db.Create(&blank).Error; err != nil {
		t.Fatalf("create default blank line: %v", err)
	}

	invID := seedDraftInvoice(t, db, fx, soID, []invLineSpec{
		{ProductID: fx.ProductID1, Qty: 10, UnitPrice: 50},
	})
	if err := MatchInvoiceLinesToSalesOrder(db, fx.CompanyID, invID); err != nil {
		t.Fatalf("match: %v", err)
	}
	var inv models.Invoice
	db.Preload("Lines").First(&inv, invID)
	if err := ApplyInvoicePostToSalesOrder(db, inv); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var so models.SalesOrder
	db.First(&so, soID)
	if so.Status != models.SalesOrderStatusFullyInvoiced {
		t.Fatalf("status: got %s want fully_invoiced", so.Status)
	}
}

func TestGetSalesOrderNormalizesHistoricalBlankLineStatus(t *testing.T) {
	db := soTrackingDB(t)
	fx := seedSOTrackingFixture(t, db)
	soID, widgetLineID, _ := seedSOWithTwoLines(t, db, fx)

	if err := db.Where("sales_order_id = ? AND product_service_id = ?", soID, fx.ProductID2).
		Delete(&models.SalesOrderLine{}).Error; err != nil {
		t.Fatalf("remove second substantive line: %v", err)
	}
	if err := db.Create(&models.SalesOrderLine{
		SalesOrderID: soID,
		Quantity:     decimal.NewFromInt(1),
		SortOrder:    99,
	}).Error; err != nil {
		t.Fatalf("create historical blank line: %v", err)
	}
	if err := db.Model(&models.SalesOrderLine{}).Where("id = ?", widgetLineID).
		Update("invoiced_qty", decimal.NewFromInt(10)).Error; err != nil {
		t.Fatalf("mark widget invoiced: %v", err)
	}
	if err := db.Model(&models.SalesOrder{}).Where("id = ?", soID).
		Updates(map[string]any{
			"status":          string(models.SalesOrderStatusPartiallyInvoiced),
			"invoiced_amount": decimal.NewFromInt(500),
		}).Error; err != nil {
		t.Fatalf("seed partial status: %v", err)
	}

	so, err := GetSalesOrder(db, fx.CompanyID, soID)
	if err != nil {
		t.Fatalf("get sales order: %v", err)
	}
	if so.Status != models.SalesOrderStatusFullyInvoiced {
		t.Fatalf("status: got %s want fully_invoiced", so.Status)
	}
}

func TestReverseInvoicePostOnSalesOrder_RollsBackStatus(t *testing.T) {
	db := soTrackingDB(t)
	fx := seedSOTrackingFixture(t, db)
	soID, _, _ := seedSOWithTwoLines(t, db, fx)

	invID := seedDraftInvoice(t, db, fx, soID, []invLineSpec{
		{ProductID: fx.ProductID1, Qty: 4, UnitPrice: 50},
	})
	if err := MatchInvoiceLinesToSalesOrder(db, fx.CompanyID, invID); err != nil {
		t.Fatalf("match: %v", err)
	}
	var inv models.Invoice
	db.Preload("Lines").First(&inv, invID)
	if err := ApplyInvoicePostToSalesOrder(db, inv); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Sanity: now partially_invoiced.
	var soMid models.SalesOrder
	db.First(&soMid, soID)
	if soMid.Status != models.SalesOrderStatusPartiallyInvoiced {
		t.Fatalf("pre-reverse status: got %s want partially_invoiced", soMid.Status)
	}

	// Reverse.
	if err := ReverseInvoicePostOnSalesOrder(db, inv); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	var soEnd models.SalesOrder
	db.First(&soEnd, soID)
	if soEnd.Status != models.SalesOrderStatusConfirmed {
		t.Fatalf("post-reverse status: got %s want confirmed (back to baseline)", soEnd.Status)
	}
	if !soEnd.InvoicedAmount.IsZero() {
		t.Fatalf("post-reverse invoiced_amount: got %s want 0", soEnd.InvoicedAmount)
	}
}

// ── Scenario 5: Standalone invoice is a no-op ───────────────────────────────

func TestSOTracking_StandaloneInvoice_NoOp(t *testing.T) {
	db := soTrackingDB(t)
	fx := seedSOTrackingFixture(t, db)

	invID := seedDraftInvoice(t, db, fx, 0 /* no SO */, []invLineSpec{
		{ProductID: fx.ProductID1, Qty: 1, UnitPrice: 100},
	})
	if err := MatchInvoiceLinesToSalesOrder(db, fx.CompanyID, invID); err != nil {
		t.Fatalf("match should be no-op: %v", err)
	}
	var inv models.Invoice
	db.Preload("Lines").First(&inv, invID)

	// Apply + Reverse on a standalone invoice should be no-ops
	// (not errors).
	if err := ApplyInvoicePostToSalesOrder(db, inv); err != nil {
		t.Fatalf("apply no-op: %v", err)
	}
	if err := ReverseInvoicePostOnSalesOrder(db, inv); err != nil {
		t.Fatalf("reverse no-op: %v", err)
	}

	// Line's sales_order_line_id remains nil.
	var lines []models.InvoiceLine
	db.Where("invoice_id = ?", invID).Find(&lines)
	if lines[0].SalesOrderLineID != nil {
		t.Fatalf("standalone invoice line should not have SO-line link")
	}
}
