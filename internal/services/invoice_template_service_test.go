// 遵循project_guide.md
package services

// invoice_template_service_test.go — Tests for invoice template service functions.
//
// Coverage:
//   TestSetDefaultInvoiceTemplate_FirstDefault          — set default when none exists
//   TestSetDefaultInvoiceTemplate_SwapsDefault          — atomically replaces existing default
//   TestSetDefaultInvoiceTemplate_AlreadyDefault        — no-op when already default
//   TestSetDefaultInvoiceTemplate_WrongCompany          — rejects cross-company ID
//   TestCreateInvoiceTemplate_DefaultEnforcedAtService  — create second default returns error
//   TestResolveTemplateEmailConfig_UsesInvoiceTemplate  — resolveTemplateEmailConfig loads pinned template
//   TestResolveTemplateEmailConfig_FallsBackToDefault   — resolveTemplateEmailConfig uses company default
//   TestResolveTemplateEmailConfig_NoTemplate           — returns empty config without error

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func testTemplateServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:tmpl_svc_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.AuditLog{},
		&models.InvoiceTemplate{},
		&models.Customer{},
		&models.Account{},
		&models.Invoice{},
		&models.InvoiceLine{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedTemplateCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	co := models.Company{Name: "Template Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	return co.ID
}

func seedTemplate(t *testing.T, db *gorm.DB, companyID uint, name string, isDefault bool) *models.InvoiceTemplate {
	t.Helper()
	cfg := models.DefaultTemplateConfig("classic")
	cfgJSON, _ := json.Marshal(cfg)
	tmpl, err := CreateInvoiceTemplate(db, companyID, name, "", cfgJSON, isDefault)
	if err != nil {
		t.Fatalf("seedTemplate %q: %v", name, err)
	}
	return tmpl
}

// ── SetDefaultInvoiceTemplate ─────────────────────────────────────────────────

func TestSetDefaultInvoiceTemplate_FirstDefault(t *testing.T) {
	db := testTemplateServiceDB(t)
	companyID := seedTemplateCompany(t, db)
	tmpl := seedTemplate(t, db, companyID, "Classic", false)

	result, err := SetDefaultInvoiceTemplate(db, companyID, tmpl.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsDefault {
		t.Error("returned template should have IsDefault=true")
	}

	// Verify in DB.
	var reloaded models.InvoiceTemplate
	db.First(&reloaded, tmpl.ID)
	if !reloaded.IsDefault {
		t.Error("template in DB should have IsDefault=true")
	}
}

func TestSetDefaultInvoiceTemplate_SwapsDefault(t *testing.T) {
	db := testTemplateServiceDB(t)
	companyID := seedTemplateCompany(t, db)

	old := seedTemplate(t, db, companyID, "Old Default", true)
	newTmpl := seedTemplate(t, db, companyID, "New Default", false)

	_, err := SetDefaultInvoiceTemplate(db, companyID, newTmpl.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Old default must be cleared.
	var reloadedOld models.InvoiceTemplate
	db.First(&reloadedOld, old.ID)
	if reloadedOld.IsDefault {
		t.Error("old default should have IsDefault=false after swap")
	}

	// New template must be default.
	var reloadedNew models.InvoiceTemplate
	db.First(&reloadedNew, newTmpl.ID)
	if !reloadedNew.IsDefault {
		t.Error("new template should have IsDefault=true after swap")
	}

	// Exactly one default must exist.
	var count int64
	db.Model(&models.InvoiceTemplate{}).Where("company_id = ? AND is_default = true", companyID).Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 default, got %d", count)
	}
}

func TestSetDefaultInvoiceTemplate_AlreadyDefault(t *testing.T) {
	db := testTemplateServiceDB(t)
	companyID := seedTemplateCompany(t, db)
	tmpl := seedTemplate(t, db, companyID, "Already Default", true)

	// Should not error — no-op.
	result, err := SetDefaultInvoiceTemplate(db, companyID, tmpl.ID)
	if err != nil {
		t.Fatalf("unexpected error on already-default: %v", err)
	}
	if !result.IsDefault {
		t.Error("template should still be default")
	}
}

func TestSetDefaultInvoiceTemplate_WrongCompany(t *testing.T) {
	db := testTemplateServiceDB(t)
	co1 := seedTemplateCompany(t, db)
	co2ID := func() uint {
		co := models.Company{Name: "Other Co", BaseCurrencyCode: "USD", IsActive: true}
		db.Create(&co)
		return co.ID
	}()

	tmpl := seedTemplate(t, db, co1, "Co1 Template", false)

	// Try to set default using wrong company ID.
	_, err := SetDefaultInvoiceTemplate(db, co2ID, tmpl.ID)
	if err == nil {
		t.Fatal("expected error for cross-company set-default, got nil")
	}
}

// ── CreateInvoiceTemplate default enforcement ─────────────────────────────────

func TestCreateInvoiceTemplate_DefaultEnforcedAtService(t *testing.T) {
	db := testTemplateServiceDB(t)
	companyID := seedTemplateCompany(t, db)

	seedTemplate(t, db, companyID, "First Default", true)

	// Creating a second default must fail at the service layer.
	cfg := models.DefaultTemplateConfig("classic")
	cfgJSON, _ := json.Marshal(cfg)
	_, err := CreateInvoiceTemplate(db, companyID, "Second Default", "", cfgJSON, true)
	if err == nil {
		t.Fatal("expected error creating second default template, got nil")
	}
}

// ── resolveTemplateEmailConfig ────────────────────────────────────────────────

func TestResolveTemplateEmailConfig_UsesInvoiceTemplate(t *testing.T) {
	db := testTemplateServiceDB(t)
	companyID := seedTemplateCompany(t, db)

	cfg := models.DefaultTemplateConfig("classic")
	cfg.EmailDefaultSubject = "Custom Subject {{InvoiceNumber}}"
	cfg.EmailDefaultBody = "Custom Body {{CustomerName}}"
	cfgJSON, _ := json.Marshal(cfg)

	tmpl, _ := CreateInvoiceTemplate(db, companyID, "Custom", "", cfgJSON, false)

	// Build a minimal invoice with TemplateID pinned.
	inv := &models.Invoice{
		CompanyID:            companyID,
		TemplateID:           &tmpl.ID,
		CustomerNameSnapshot: "Jane",
		InvoiceNumber:        "INV-001",
		Amount:               decimal.RequireFromString("100"),
		BalanceDue:           decimal.RequireFromString("100"),
	}

	resolved, err := resolveTemplateEmailConfig(db, inv, companyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.EmailDefaultSubject != "Custom Subject {{InvoiceNumber}}" {
		t.Errorf("EmailDefaultSubject: want custom, got %q", resolved.EmailDefaultSubject)
	}
	if resolved.EmailDefaultBody != "Custom Body {{CustomerName}}" {
		t.Errorf("EmailDefaultBody: want custom, got %q", resolved.EmailDefaultBody)
	}
}

func TestResolveTemplateEmailConfig_FallsBackToDefault(t *testing.T) {
	db := testTemplateServiceDB(t)
	companyID := seedTemplateCompany(t, db)

	cfg := models.DefaultTemplateConfig("modern")
	cfg.EmailDefaultSubject = "Company Default Subject"
	cfgJSON, _ := json.Marshal(cfg)
	CreateInvoiceTemplate(db, companyID, "Default", "", cfgJSON, true)

	// Invoice with no pinned template.
	inv := &models.Invoice{
		CompanyID:     companyID,
		TemplateID:    nil,
		InvoiceNumber: "INV-002",
	}

	resolved, err := resolveTemplateEmailConfig(db, inv, companyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.EmailDefaultSubject != "Company Default Subject" {
		t.Errorf("should fall back to company default, got %q", resolved.EmailDefaultSubject)
	}
}

func TestResolveTemplateEmailConfig_NoTemplate(t *testing.T) {
	db := testTemplateServiceDB(t)
	companyID := seedTemplateCompany(t, db)

	// No templates at all for this company.
	inv := &models.Invoice{
		CompanyID:     companyID,
		TemplateID:    nil,
		InvoiceNumber: "INV-003",
	}

	resolved, err := resolveTemplateEmailConfig(db, inv, companyID)
	if err != nil {
		t.Fatalf("should return empty config without error, got: %v", err)
	}
	// Empty config: all fields are zero values.
	if resolved.EmailDefaultSubject != "" {
		t.Errorf("expected empty subject, got %q", resolved.EmailDefaultSubject)
	}
}
