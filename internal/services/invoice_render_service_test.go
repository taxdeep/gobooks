// 遵循project_guide.md
package services

// invoice_render_service_test.go — Tests for the invoice render pipeline.
//
// Coverage:
//   TestBuildRenderData_VMAccuracy              — monetary fields match invoice truth, no recalculation
//   TestBuildRenderData_CurrencyFallsBackToBase — company base currency used when invoice.CurrencyCode is blank
//   TestBuildRenderData_ExplicitCurrency        — invoice.CurrencyCode overrides company base
//   TestBuildRenderData_ShowTaxSummaryTrue       — ShowTaxSummary renders tax column
//   TestBuildRenderData_ShowTaxSummaryFalse      — ShowTaxSummary=false hides tax column
//   TestBuildRenderData_ShowLogoFalse            — ShowLogo=false excludes logo even when path set
//   TestBuildRenderData_ShowCompanyAddressFalse  — ShowCompanyAddress=false hides company address
//   TestBuildRenderData_ShowNotesFalse           — ShowNotes=false hides memo/notes section
//   TestBuildRenderData_CompanyIsolation         — cross-company invoice returns error
//   TestResolveRenderTemplate_PinnedActive       — pinned active template is returned
//   TestResolveRenderTemplate_PinnedInactive     — inactive pinned template falls back to company default
//   TestResolveRenderTemplate_NoTemplate         — no template → system fallback classic
//   TestResolveRenderTemplate_DefaultTemplate    — company active default template is returned
//   TestRenderInvoiceForPrint_HasPrintScript     — print page contains window.print() trigger

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func testRenderDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:render_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.AuditLog{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.PaymentTerm{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.InvoiceTemplate{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedRenderCompany(t *testing.T, db *gorm.DB, baseCurrency string) *models.Company {
	t.Helper()
	co := models.Company{Name: "Render Co", BaseCurrencyCode: baseCurrency, IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	return &co
}

func seedRenderCustomer(t *testing.T, db *gorm.DB, companyID uint) *models.Customer {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer", Email: "test@example.com"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return &c
}

func seedRenderInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint) *models.Invoice {
	t.Helper()
	inv := models.Invoice{
		CompanyID:            companyID,
		CustomerID:           customerID,
		InvoiceNumber:        "INV-TEST-001",
		InvoiceDate:          time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:               models.InvoiceStatusIssued,
		Amount:               decimal.RequireFromString("210.00"),
		Subtotal:             decimal.RequireFromString("200.00"),
		TaxTotal:             decimal.RequireFromString("10.00"),
		BalanceDue:           decimal.RequireFromString("210.00"),
		CustomerNameSnapshot: "Test Customer",
		CustomerAddressSnapshot: "123 Main St\nVancouver, BC V1A 1A1",
		Lines: []models.InvoiceLine{
			{
				CompanyID:   companyID,
				Description: "Service A",
				Qty:         decimal.NewFromInt(2),
				UnitPrice:   decimal.RequireFromString("100.00"),
				LineNet:     decimal.RequireFromString("200.00"),
				LineTax:     decimal.RequireFromString("10.00"),
				LineTotal:   decimal.RequireFromString("210.00"),
			},
		},
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return &inv
}

func seedRenderTemplate(t *testing.T, db *gorm.DB, companyID uint, style string, isDefault, isActive bool) *models.InvoiceTemplate {
	t.Helper()
	cfg := models.DefaultTemplateConfig(style)
	cfgJSON, _ := json.Marshal(cfg)
	tmpl := models.InvoiceTemplate{
		CompanyID:  companyID,
		Name:       fmt.Sprintf("%s template", style),
		ConfigJSON: cfgJSON,
		IsDefault:  isDefault,
		IsActive:   true, // create active first (GORM zero-value bool issue)
	}
	if err := db.Create(&tmpl).Error; err != nil {
		t.Fatal(err)
	}
	if !isActive {
		if err := db.Model(&tmpl).Update("is_active", false).Error; err != nil {
			t.Fatal(err)
		}
		tmpl.IsActive = false
	}
	return &tmpl
}

// ── VM Accuracy ───────────────────────────────────────────────────────────────

func TestBuildRenderData_VMAccuracy(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)

	data, err := BuildInvoiceRenderData(db, co.ID, inv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !data.Amount.Equal(decimal.RequireFromString("210.00")) {
		t.Errorf("Amount: want 210.00, got %s", data.Amount)
	}
	if !data.Subtotal.Equal(decimal.RequireFromString("200.00")) {
		t.Errorf("Subtotal: want 200.00, got %s", data.Subtotal)
	}
	if !data.TaxTotal.Equal(decimal.RequireFromString("10.00")) {
		t.Errorf("TaxTotal: want 10.00, got %s", data.TaxTotal)
	}
	if !data.BalanceDue.Equal(decimal.RequireFromString("210.00")) {
		t.Errorf("BalanceDue: want 210.00, got %s", data.BalanceDue)
	}
	if data.InvoiceNumber != "INV-TEST-001" {
		t.Errorf("InvoiceNumber: want INV-TEST-001, got %s", data.InvoiceNumber)
	}
	if data.CustomerName != "Test Customer" {
		t.Errorf("CustomerName: want Test Customer, got %s", data.CustomerName)
	}
}

func TestBuildRenderData_CurrencyFallsBackToBase(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	// inv.CurrencyCode is blank (base currency)

	data, err := BuildInvoiceRenderData(db, co.ID, inv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Currency != "CAD" {
		t.Errorf("Currency: want CAD (company base), got %s", data.Currency)
	}
}

func TestBuildRenderData_ExplicitCurrency(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	inv.CurrencyCode = "USD"

	data, err := BuildInvoiceRenderData(db, co.ID, inv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Currency != "USD" {
		t.Errorf("Currency: want USD, got %s", data.Currency)
	}
}

// ── Company Isolation ─────────────────────────────────────────────────────────

func TestBuildRenderData_CompanyIsolation(t *testing.T) {
	db := testRenderDB(t)
	co1 := seedRenderCompany(t, db, "CAD")
	co2 := seedRenderCompany(t, db, "USD")
	cu := seedRenderCustomer(t, db, co1.ID)
	inv := seedRenderInvoice(t, db, co1.ID, cu.ID)

	// Try to render co1's invoice as co2 — must fail.
	_, err := BuildInvoiceRenderData(db, co2.ID, inv)
	if err == nil {
		t.Fatal("expected error for cross-company render, got nil")
	}
}

// ── Section Visibility Toggles ────────────────────────────────────────────────

func cfgWithToggles(showTax, showLogo, showCompanyAddr, showNotes bool) models.TemplateConfig {
	cfg := models.DefaultTemplateConfig("classic")
	cfg.ShowTaxSummary = showTax
	cfg.ShowLogo = showLogo
	cfg.ShowCompanyAddress = showCompanyAddr
	cfg.ShowNotes = showNotes
	return cfg
}

func buildRenderDataWithConfig(t *testing.T, db *gorm.DB, co *models.Company, cu *models.Customer, inv *models.Invoice, cfg models.TemplateConfig) *InvoiceRenderData {
	t.Helper()
	cfgJSON, _ := json.Marshal(cfg)
	tmpl := models.InvoiceTemplate{
		CompanyID:  co.ID,
		Name:       "toggle-test",
		ConfigJSON: cfgJSON,
		IsDefault:  false,
		IsActive:   true,
	}
	if err := db.Create(&tmpl).Error; err != nil {
		t.Fatal(err)
	}
	inv.TemplateID = &tmpl.ID

	data, err := BuildInvoiceRenderData(db, co.ID, inv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return data
}

func TestBuildRenderData_ShowTaxSummaryTrue(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	cfg := cfgWithToggles(true, false, false, false)

	data := buildRenderDataWithConfig(t, db, co, cu, inv, cfg)
	html := RenderInvoiceToHTML(*data)

	if !strings.Contains(html, "Tax") {
		t.Error("ShowTaxSummary=true: expected tax column in HTML")
	}
}

func TestBuildRenderData_ShowTaxSummaryFalse(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	cfg := cfgWithToggles(false, false, false, false)

	data := buildRenderDataWithConfig(t, db, co, cu, inv, cfg)
	html := RenderInvoiceToHTML(*data)

	// Tax column header should not appear when ShowTaxSummary=false.
	// The summary tax row also should not appear.
	if strings.Contains(html, "<th class=\"numeric\">Tax</th>") {
		t.Error("ShowTaxSummary=false: tax column header should not appear")
	}
}

func TestBuildRenderData_ShowNotesFalse(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	inv.Memo = "Important notes here"
	cfg := cfgWithToggles(false, false, false, false) // ShowNotes=false

	data := buildRenderDataWithConfig(t, db, co, cu, inv, cfg)
	html := RenderInvoiceToHTML(*data)

	if strings.Contains(html, "Important notes here") {
		t.Error("ShowNotes=false: memo should not appear in HTML")
	}
}

func TestBuildRenderData_ShowCompanyAddressFalse(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	co.AddressLine = "999 Corp Ave"
	db.Save(co)
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	cfg := cfgWithToggles(false, false, false, false) // ShowCompanyAddress=false

	data := buildRenderDataWithConfig(t, db, co, cu, inv, cfg)
	html := RenderInvoiceToHTML(*data)

	if strings.Contains(html, "999 Corp Ave") {
		t.Error("ShowCompanyAddress=false: company address should not appear")
	}
}

func TestBuildRenderData_ShowLogoFalse(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	co.LogoPath = "/tmp/fake-logo.png" // won't exist, but ShowLogo=false should skip the load
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	cfg := cfgWithToggles(false, false, false, false) // ShowLogo=false

	data := buildRenderDataWithConfig(t, db, co, cu, inv, cfg)

	// LogoImageBase64 should be empty because ShowLogo=false.
	if data.LogoImageBase64 != "" {
		t.Error("ShowLogo=false: LogoImageBase64 should be empty")
	}
}

// ── Template Resolution Chain ─────────────────────────────────────────────────

func TestResolveRenderTemplate_PinnedActive(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)

	// Pinned modern template (active).
	pinnedTmpl := seedRenderTemplate(t, db, co.ID, "modern", false, true)
	inv.TemplateID = &pinnedTmpl.ID

	cfg := resolveRenderTemplate(db, inv, co.ID)
	if cfg.TemplateStyle != "modern" {
		t.Errorf("expected modern template, got %s", cfg.TemplateStyle)
	}
}

func TestResolveRenderTemplate_PinnedInactive(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)

	// Pinned modern template but INACTIVE — should fall back.
	pinnedTmpl := seedRenderTemplate(t, db, co.ID, "modern", false, false) // is_active=false
	inv.TemplateID = &pinnedTmpl.ID

	// Company default: classic.
	seedRenderTemplate(t, db, co.ID, "classic", true, true)

	cfg := resolveRenderTemplate(db, inv, co.ID)
	// Must fall back to company default (classic), not the inactive pinned one.
	if cfg.TemplateStyle != "classic" {
		t.Errorf("inactive pinned template should fall back to company default, got %s", cfg.TemplateStyle)
	}
}

func TestResolveRenderTemplate_NoTemplate(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	inv.TemplateID = nil

	// No templates at all for this company → system fallback.
	cfg := resolveRenderTemplate(db, inv, co.ID)
	if cfg.TemplateStyle != "classic" {
		t.Errorf("system fallback should be classic, got %s", cfg.TemplateStyle)
	}
}

func TestResolveRenderTemplate_DefaultTemplate(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	inv.TemplateID = nil

	// Company active default: modern.
	seedRenderTemplate(t, db, co.ID, "modern", true, true)

	cfg := resolveRenderTemplate(db, inv, co.ID)
	if cfg.TemplateStyle != "modern" {
		t.Errorf("company default should be modern, got %s", cfg.TemplateStyle)
	}
}

// ── Print render ──────────────────────────────────────────────────────────────

func TestRenderInvoiceForPrint_HasPrintScript(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)

	data, err := BuildInvoiceRenderData(db, co.ID, inv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	html := RenderInvoiceForPrint(*data)

	if !strings.Contains(html, "window.print()") {
		t.Error("print HTML should contain window.print() trigger")
	}
	// Must still contain the invoice content.
	if !strings.Contains(html, "INV-TEST-001") {
		t.Error("print HTML should contain invoice number")
	}
}

// ── Multiline address rendering ───────────────────────────────────────────────

func TestBuildRenderData_MultilineCustomerAddress(t *testing.T) {
	db := testRenderDB(t)
	co := seedRenderCompany(t, db, "CAD")
	cu := seedRenderCustomer(t, db, co.ID)
	inv := seedRenderInvoice(t, db, co.ID, cu.ID)
	// inv.CustomerAddressSnapshot is "123 Main St\nVancouver, BC V1A 1A1"

	data, err := BuildInvoiceRenderData(db, co.ID, inv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	html := RenderInvoiceToHTML(*data)

	// Both lines must appear as separate elements, not joined.
	if !strings.Contains(html, "123 Main St") {
		t.Error("address line 1 not found in HTML")
	}
	if !strings.Contains(html, "Vancouver, BC V1A 1A1") {
		t.Error("address line 2 not found in HTML")
	}
	// The raw newline must not appear verbatim in the HTML.
	if strings.Contains(html, "123 Main St\nVancouver") {
		t.Error("raw newline should be split into separate HTML elements, not left as \\n")
	}
}
