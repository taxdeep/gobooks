// 遵循project_guide.md
package services

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// stock_qty_guard_test.go — locks the IsStockItem integer rule (S1 → S4)
// across the four AR/AP doc services that received the guard in S4.3:
// Quote, PurchaseOrder, CreditNote, VendorCreditNote.  Sales-order
// coverage lives in sales_order_stockqty_test.go.

func stockGuardDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Quote{},
		&models.QuoteLine{},
		&models.PurchaseOrder{},
		&models.PurchaseOrderLine{},
		&models.CreditNote{},
		&models.CreditNoteLine{},
		&models.VendorCreditNote{},
		&models.VendorCreditNoteLine{},
		&models.NumberingSetting{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type stockGuardFixture struct {
	CompanyID    uint
	CustomerID   uint
	VendorID     uint
	StockItemID  uint
	RevenueAccID uint
}

func seedStockGuardFixture(t *testing.T, db *gorm.DB) stockGuardFixture {
	t.Helper()
	co := models.Company{
		Name: "SGuard Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4,
	}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	cust := models.Customer{CompanyID: co.ID, Name: "Cust"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	vend := models.Vendor{CompanyID: co.ID, Name: "Vend"}
	if err := db.Create(&vend).Error; err != nil {
		t.Fatal(err)
	}
	rev := models.Account{
		CompanyID: co.ID, Code: "4000", Name: "Rev",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true,
	}
	if err := db.Create(&rev).Error; err != nil {
		t.Fatal(err)
	}
	stock := models.ProductService{
		CompanyID: co.ID, Name: "Watermelon",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true,
	}
	stock.ApplyTypeDefaults()
	if err := db.Create(&stock).Error; err != nil {
		t.Fatal(err)
	}
	return stockGuardFixture{
		CompanyID:    co.ID,
		CustomerID:   cust.ID,
		VendorID:     vend.ID,
		StockItemID:  stock.ID,
		RevenueAccID: rev.ID,
	}
}

// ── Quote ────────────────────────────────────────────────────────────────────

func TestCreateQuote_RejectsFractionalStockQty(t *testing.T) {
	db := stockGuardDB(t)
	f := seedStockGuardFixture(t, db)

	_, err := CreateQuote(db, f.CompanyID, QuoteInput{
		CustomerID: f.CustomerID,
		QuoteDate:  time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Lines: []QuoteLineInput{{
			ProductServiceID: &f.StockItemID,
			Description:      "Watermelon",
			Quantity:         decimal.RequireFromString("8.5"),
			UnitPrice:        decimal.NewFromInt(10),
		}},
	})
	if err == nil {
		t.Fatal("expected fractional stock-qty rejection, got nil")
	}
	if !strings.Contains(err.Error(), "whole-unit") {
		t.Errorf("error = %v, want whole-unit guidance", err)
	}
}

// ── Purchase Order ───────────────────────────────────────────────────────────

func TestCreatePurchaseOrder_RejectsFractionalStockQty(t *testing.T) {
	db := stockGuardDB(t)
	f := seedStockGuardFixture(t, db)

	_, err := CreatePurchaseOrder(db, f.CompanyID, POInput{
		VendorID:     f.VendorID,
		PODate:       time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		CurrencyCode: "CAD",
		Lines: []POLineInput{{
			ProductServiceID: &f.StockItemID,
			Description:      "Watermelon",
			Qty:              decimal.RequireFromString("8.5"),
			UnitPrice:        decimal.NewFromInt(10),
		}},
	})
	if err == nil {
		t.Fatal("expected fractional stock-qty rejection on PO, got nil")
	}
	if !strings.Contains(err.Error(), "whole-unit") {
		t.Errorf("PO error = %v, want whole-unit guidance", err)
	}
}

// ── Credit Note (AR) ─────────────────────────────────────────────────────────

func TestCreateCreditNoteDraft_RejectsFractionalStockQty(t *testing.T) {
	db := stockGuardDB(t)
	f := seedStockGuardFixture(t, db)

	_, err := CreateCreditNoteDraft(db, CreateCreditNoteDraftInput{
		CompanyID:      f.CompanyID,
		CustomerID:     f.CustomerID,
		CreditNoteDate: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Lines: []CreditNoteLineInput{{
			Description:      "Watermelon return",
			Qty:              decimal.RequireFromString("8.5"),
			UnitPrice:        decimal.NewFromInt(10),
			RevenueAccountID: f.RevenueAccID,
			ProductServiceID: &f.StockItemID,
		}},
	})
	if err == nil {
		t.Fatal("expected fractional stock-qty rejection on CN, got nil")
	}
	if !strings.Contains(err.Error(), "whole-unit") {
		t.Errorf("CN error = %v, want whole-unit guidance", err)
	}
}

// ── Row-friendly variant for handler-side editors ───────────────────────────

// TestStockItemQtyRowError covers the user-facing wrapper used by Invoice +
// Bill editor handlers: returns a short message (no "line N" prefix) when
// the row should be flagged inline, "" otherwise.
func TestStockItemQtyRowError(t *testing.T) {
	db := stockGuardDB(t)
	f := seedStockGuardFixture(t, db)

	// Service item — fractional qty is OK, returns "".
	svc := models.ProductService{
		CompanyID: f.CompanyID, Name: "Consulting",
		Type: models.ProductServiceTypeService, IsActive: true,
	}
	svc.ApplyTypeDefaults()
	if err := db.Create(&svc).Error; err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		psID   *uint
		qty    string
		want   string // "" = ok; non-empty = expected message substring
	}{
		{"nil product (free-text line) → ok", nil, "1.5", ""},
		{"service item fractional → ok", &svc.ID, "1.5", ""},
		{"stock item whole → ok", &f.StockItemID, "8", ""},
		{"stock item fractional → row error", &f.StockItemID, "8.5", "whole-unit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StockItemQtyRowError(db, f.CompanyID, tc.psID, decimal.RequireFromString(tc.qty))
			if tc.want == "" {
				if got != "" {
					t.Errorf("expected ok, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("expected message containing %q, got %q", tc.want, got)
			}
		})
	}
}

// ── Vendor Credit Note (AP) ──────────────────────────────────────────────────

func TestCreateVendorCreditNote_RejectsFractionalStockQty(t *testing.T) {
	db := stockGuardDB(t)
	f := seedStockGuardFixture(t, db)

	_, err := CreateVendorCreditNote(db, f.CompanyID, VendorCreditNoteInput{
		VendorID:       f.VendorID,
		CreditNoteDate: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		CurrencyCode:   "CAD",
		Lines: []VendorCreditNoteLineInput{{
			ProductServiceID: &f.StockItemID,
			Description:      "Watermelon return",
			Qty:              decimal.RequireFromString("8.5"),
			UnitPrice:        decimal.NewFromInt(10),
		}},
	})
	if err == nil {
		t.Fatal("expected fractional stock-qty rejection on VCN, got nil")
	}
	if !strings.Contains(err.Error(), "whole-unit") {
		t.Errorf("VCN error = %v, want whole-unit guidance", err)
	}
}
