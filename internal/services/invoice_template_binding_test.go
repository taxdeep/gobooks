// 遵循project_guide.md
package services

// invoice_template_binding_test.go — Tests for Batch 3 template binding and
// historical stability rules.
//
// Coverage:
//   TestIssueInvoice_PinsDefaultTemplate         — IssueInvoice pins active default when TemplateID is nil
//   TestIssueInvoice_PreservesExistingBinding    — IssueInvoice does not overwrite explicit TemplateID
//   TestIssueInvoice_NilTemplateWhenNoDefault    — no default template → TemplateID stays nil after issue
//   TestBindTemplateToInvoice_Success            — explicit re-bind on draft invoice
//   TestBindTemplateToInvoice_CrossCompany       — cross-company template rejected
//   TestBindTemplateToInvoice_InactiveTemplate   — inactive template cannot be bound
//   TestBindTemplateToInvoice_NonDraft           — cannot re-bind issued invoice
//   TestDeactivateInvoiceTemplate_Success        — deactivate non-default template
//   TestDeactivateInvoiceTemplate_DefaultBlocked — cannot deactivate the company default
//   TestDeactivateInvoiceTemplate_AlreadyInactive — deactivate is a no-op when already inactive
//   TestResolveTemplateEmailConfig_InactivePinnedFallsBack — inactive pinned → company default used
//   TestResolveTemplateEmailConfig_InactiveDefaultFallsBack — inactive default → empty config
//   TestHistoricalStability_DefaultChangeDoesNotAffectPinnedInvoice — pinned invoice ignores default change
//   TestResolveTemplateIdentity_ReturnsIDAndName — identity resolves correctly
//   TestResolveTemplateIdentity_NoTemplate       — nil ID returned when no template resolves
//   TestEmailLogSnapshot_BodyAndTemplateRecorded — InvoiceEmailLog captures body + template identity

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func testBindingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:binding_%s?mode=memory&cache=shared", t.Name())
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
		&models.ItemComponent{},
		&models.PaymentTerm{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.PaymentReceipt{},
		&models.SettlementAllocation{},
		&models.PaymentTransaction{},
		&models.TaskInvoiceSource{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.InvoiceTemplate{},
		&models.InvoiceEmailLog{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedBindingCompany(t *testing.T, db *gorm.DB) *models.Company {
	t.Helper()
	co := models.Company{Name: "Binding Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	return &co
}

func seedBindingTemplate(t *testing.T, db *gorm.DB, companyID uint, name string, isDefault, isActive bool) *models.InvoiceTemplate {
	t.Helper()
	cfg := models.DefaultTemplateConfig("classic")
	cfgJSON, _ := json.Marshal(cfg)
	tmpl := models.InvoiceTemplate{
		CompanyID:  companyID,
		Name:       name,
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

// seedMinimalIssuableInvoice creates a fully issuable invoice with one product line.
// Sets up AR + revenue accounts and a product for PostInvoice to succeed.
func seedMinimalIssuableInvoice(t *testing.T, db *gorm.DB, companyID uint) *models.Invoice {
	t.Helper()

	// Revenue account (PostInvoice credits this per line via ProductService.RevenueAccount)
	revenueAcct := models.Account{
		CompanyID:         companyID,
		Name:              "Revenue",
		Code:              "4000",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailServiceRevenue,
		IsActive:          true,
	}
	if err := db.Create(&revenueAcct).Error; err != nil {
		t.Fatal(err)
	}

	// AR account (PostInvoice looks up first active DetailAccountsReceivable)
	_ = models.Account{
		CompanyID:         companyID,
		Name:              "Accounts Receivable",
		Code:              "1100",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailAccountsReceivable,
		IsActive:          true,
	}
	var arAcct models.Account
	arAcct.CompanyID = companyID
	arAcct.Name = "Accounts Receivable"
	arAcct.Code = "1100"
	arAcct.RootAccountType = models.RootAsset
	arAcct.DetailAccountType = models.DetailAccountsReceivable
	arAcct.IsActive = true
	if err := db.Create(&arAcct).Error; err != nil {
		t.Fatal(err)
	}

	// Product (must have RevenueAccountID for PostInvoice fragment builder)
	prod := models.ProductService{
		CompanyID:        companyID,
		Name:             "Service A",
		Type:             "service",
		IsActive:         true,
		CanBeSold:        true,
		RevenueAccountID: revenueAcct.ID,
	}
	if err := db.Create(&prod).Error; err != nil {
		t.Fatal(err)
	}

	// Customer
	cust := models.Customer{CompanyID: companyID, Name: "Test Customer", Email: "test@example.com"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}

	inv := models.Invoice{
		CompanyID:               companyID,
		InvoiceNumber:           fmt.Sprintf("INV-%d", time.Now().UnixNano()),
		CustomerID:              cust.ID,
		InvoiceDate:             time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:                  models.InvoiceStatusDraft,
		Amount:                  decimal.RequireFromString("100.00"),
		Subtotal:                decimal.RequireFromString("100.00"),
		TaxTotal:                decimal.Zero,
		BalanceDue:              decimal.RequireFromString("100.00"),
		BalanceDueBase:          decimal.RequireFromString("100.00"),
		CustomerNameSnapshot:    "Test Customer",
		CustomerEmailSnapshot:   "test@example.com",
		CustomerAddressSnapshot: "123 Main St",
		Lines: []models.InvoiceLine{
			{
				CompanyID:        companyID,
				Description:      "Service A",
				Qty:              decimal.NewFromInt(1),
				UnitPrice:        decimal.RequireFromString("100.00"),
				LineNet:          decimal.RequireFromString("100.00"),
				LineTax:          decimal.Zero,
				LineTotal:        decimal.RequireFromString("100.00"),
				ProductServiceID: &prod.ID,
			},
		},
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return &inv
}

// ── IssueInvoice template stabilization ──────────────────────────────────────

func TestIssueInvoice_PinsDefaultTemplate(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)

	// Company has an active default template.
	defaultTmpl := seedBindingTemplate(t, db, co.ID, "Classic Default", true, true)

	inv := seedMinimalIssuableInvoice(t, db, co.ID)
	// Invoice starts unbound.
	if inv.TemplateID != nil {
		t.Fatal("precondition: invoice should start with nil TemplateID")
	}

	issued, err := IssueInvoice(db, co.ID, inv.ID)
	if err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}

	if issued.TemplateID == nil {
		t.Fatal("IssueInvoice should have pinned the company default template")
	}
	if *issued.TemplateID != defaultTmpl.ID {
		t.Errorf("pinned template ID: want %d, got %d", defaultTmpl.ID, *issued.TemplateID)
	}

	// Verify persisted in DB.
	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.TemplateID == nil || *reloaded.TemplateID != defaultTmpl.ID {
		t.Error("TemplateID not persisted to DB after IssueInvoice")
	}
}

func TestIssueInvoice_PreservesExistingBinding(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)

	pinnedTmpl := seedBindingTemplate(t, db, co.ID, "Pinned Classic", false, true)
	seedBindingTemplate(t, db, co.ID, "Company Default Modern", true, true)

	inv := seedMinimalIssuableInvoice(t, db, co.ID)
	// Explicitly bind to the non-default template.
	db.Model(inv).Update("template_id", pinnedTmpl.ID)

	issued, err := IssueInvoice(db, co.ID, inv.ID)
	if err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}

	if issued.TemplateID == nil || *issued.TemplateID != pinnedTmpl.ID {
		t.Errorf("IssueInvoice must not overwrite an explicit TemplateID; want %d, got %v", pinnedTmpl.ID, issued.TemplateID)
	}
}

func TestIssueInvoice_NilTemplateWhenNoDefault(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	// No templates at all for this company.

	inv := seedMinimalIssuableInvoice(t, db, co.ID)

	issued, err := IssueInvoice(db, co.ID, inv.ID)
	if err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}

	// TemplateID should remain nil — system fallback will be used at render time.
	if issued.TemplateID != nil {
		t.Errorf("TemplateID should be nil when no company default exists, got %d", *issued.TemplateID)
	}
}

// ── BindTemplateToInvoice ─────────────────────────────────────────────────────

func TestBindTemplateToInvoice_Success(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	tmpl := seedBindingTemplate(t, db, co.ID, "Modern", false, true)
	inv := seedMinimalIssuableInvoice(t, db, co.ID)

	updated, err := BindTemplateToInvoice(db, co.ID, inv.ID, tmpl.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.TemplateID == nil || *updated.TemplateID != tmpl.ID {
		t.Errorf("TemplateID: want %d, got %v", tmpl.ID, updated.TemplateID)
	}

	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.TemplateID == nil || *reloaded.TemplateID != tmpl.ID {
		t.Error("TemplateID not persisted after BindTemplateToInvoice")
	}
}

func TestBindTemplateToInvoice_CrossCompany(t *testing.T) {
	db := testBindingDB(t)
	co1 := seedBindingCompany(t, db)
	co2 := &models.Company{Name: "Other Co", BaseCurrencyCode: "USD", IsActive: true}
	db.Create(co2)

	tmplCo2 := seedBindingTemplate(t, db, co2.ID, "Co2 Template", false, true)
	inv := seedMinimalIssuableInvoice(t, db, co1.ID)

	// Try to bind co2's template to co1's invoice — must fail.
	_, err := BindTemplateToInvoice(db, co1.ID, inv.ID, tmplCo2.ID)
	if err == nil {
		t.Fatal("expected error for cross-company template bind, got nil")
	}
}

func TestBindTemplateToInvoice_InactiveTemplate(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	inactiveTmpl := seedBindingTemplate(t, db, co.ID, "Inactive", false, false)
	inv := seedMinimalIssuableInvoice(t, db, co.ID)

	_, err := BindTemplateToInvoice(db, co.ID, inv.ID, inactiveTmpl.ID)
	if err == nil {
		t.Fatal("expected error when binding inactive template, got nil")
	}
}

func TestBindTemplateToInvoice_NonDraft(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	tmpl := seedBindingTemplate(t, db, co.ID, "Modern", false, true)
	inv := seedMinimalIssuableInvoice(t, db, co.ID)

	// Force invoice to issued status.
	db.Model(inv).Update("status", string(models.InvoiceStatusIssued))

	_, err := BindTemplateToInvoice(db, co.ID, inv.ID, tmpl.ID)
	if err == nil {
		t.Fatal("expected error when binding template to non-draft invoice, got nil")
	}
}

// ── DeactivateInvoiceTemplate ─────────────────────────────────────────────────

func TestDeactivateInvoiceTemplate_Success(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	tmpl := seedBindingTemplate(t, db, co.ID, "Classic", false, true)

	result, err := DeactivateInvoiceTemplate(db, co.ID, tmpl.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsActive {
		t.Error("template should be inactive after deactivation")
	}

	var reloaded models.InvoiceTemplate
	db.First(&reloaded, tmpl.ID)
	if reloaded.IsActive {
		t.Error("template still active in DB after deactivation")
	}
}

func TestDeactivateInvoiceTemplate_DefaultBlocked(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	defaultTmpl := seedBindingTemplate(t, db, co.ID, "Default", true, true)

	_, err := DeactivateInvoiceTemplate(db, co.ID, defaultTmpl.ID)
	if err == nil {
		t.Fatal("expected error when deactivating the company default, got nil")
	}
}

func TestDeactivateInvoiceTemplate_AlreadyInactive(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	inactiveTmpl := seedBindingTemplate(t, db, co.ID, "Already Inactive", false, false)

	// Should be a no-op, not an error.
	result, err := DeactivateInvoiceTemplate(db, co.ID, inactiveTmpl.ID)
	if err != nil {
		t.Fatalf("unexpected error on already-inactive: %v", err)
	}
	if result.IsActive {
		t.Error("template should still be inactive")
	}
}

// ── resolveTemplateEmailConfig with is_active ─────────────────────────────────

func TestResolveTemplateEmailConfig_InactivePinnedFallsBack(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)

	// Pinned template: inactive.
	inactiveTmpl := seedBindingTemplate(t, db, co.ID, "Inactive Pinned", false, false)

	// Company default: active, with a known EmailDefaultSubject.
	defCfg := models.DefaultTemplateConfig("classic")
	defCfg.EmailDefaultSubject = "From Default Template"
	defCfgJSON, _ := json.Marshal(defCfg)
	defTmpl := models.InvoiceTemplate{
		CompanyID:  co.ID,
		Name:       "Active Default",
		ConfigJSON: defCfgJSON,
		IsDefault:  true,
		IsActive:   true,
	}
	db.Create(&defTmpl)

	inv := &models.Invoice{CompanyID: co.ID, TemplateID: &inactiveTmpl.ID}

	cfg, err := resolveTemplateEmailConfig(db, inv, co.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Inactive pinned → should fall back to company default.
	if cfg.EmailDefaultSubject != "From Default Template" {
		t.Errorf("expected fallback to company default, got subject %q", cfg.EmailDefaultSubject)
	}
}

func TestResolveTemplateEmailConfig_InactiveDefaultFallsBack(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)

	// Company default: inactive.
	seedBindingTemplate(t, db, co.ID, "Inactive Default", true, false)

	inv := &models.Invoice{CompanyID: co.ID, TemplateID: nil}

	cfg, err := resolveTemplateEmailConfig(db, inv, co.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No active template found → empty config (system fallback at send time).
	if cfg.EmailDefaultSubject != "" {
		t.Errorf("expected empty config when no active template, got subject %q", cfg.EmailDefaultSubject)
	}
}

// ── Historical stability ──────────────────────────────────────────────────────

func TestHistoricalStability_DefaultChangeDoesNotAffectPinnedInvoice(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)

	// Invoice was issued with "Classic" template pinned.
	classicTmpl := seedBindingTemplate(t, db, co.ID, "Classic", true, true)
	inv := seedMinimalIssuableInvoice(t, db, co.ID)
	db.Model(inv).Update("template_id", classicTmpl.ID)

	// Later: company switches default to "Modern".
	modernCfg := models.DefaultTemplateConfig("modern")
	modernCfgJSON, _ := json.Marshal(modernCfg)
	modernTmpl := models.InvoiceTemplate{
		CompanyID:  co.ID,
		Name:       "Modern",
		ConfigJSON: modernCfgJSON,
		IsDefault:  false,
		IsActive:   true,
	}
	db.Create(&modernTmpl)
	// Atomically swap default.
	db.Model(&models.InvoiceTemplate{}).Where("company_id = ? AND is_default = true", co.ID).Update("is_default", false)
	db.Model(&modernTmpl).Update("is_default", true)

	// Reload the invoice.
	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)

	// The invoice's TemplateID still points to Classic — it was pinned.
	if reloaded.TemplateID == nil || *reloaded.TemplateID != classicTmpl.ID {
		t.Errorf("invoice should still be pinned to classic template after default change, got %v", reloaded.TemplateID)
	}

	// Render resolution should use the pinned (classic) template, not the new default (modern).
	resolved := resolveRenderTemplate(db, &reloaded, co.ID)
	if resolved.TemplateStyle != "classic" {
		t.Errorf("render should use pinned classic template, got style %q", resolved.TemplateStyle)
	}
}

// ── resolveTemplateIdentity ───────────────────────────────────────────────────

func TestResolveTemplateIdentity_ReturnsIDAndName(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	tmpl := seedBindingTemplate(t, db, co.ID, "My Template", true, true)

	inv := &models.Invoice{CompanyID: co.ID, TemplateID: nil}

	id, name := resolveTemplateIdentity(db, inv, co.ID)
	if id == nil || *id != tmpl.ID {
		t.Errorf("template ID: want %d, got %v", tmpl.ID, id)
	}
	if name != "My Template" {
		t.Errorf("template name: want %q, got %q", "My Template", name)
	}
}

func TestResolveTemplateIdentity_NoTemplate(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)
	// No templates at all.

	inv := &models.Invoice{CompanyID: co.ID, TemplateID: nil}

	id, name := resolveTemplateIdentity(db, inv, co.ID)
	if id != nil {
		t.Errorf("expected nil template ID when no template resolves, got %d", *id)
	}
	if name != "" {
		t.Errorf("expected empty template name, got %q", name)
	}
}

// ── Email log snapshot ────────────────────────────────────────────────────────

func TestEmailLogSnapshot_BodyAndTemplateRecorded(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)

	// Template with known email subject.
	cfg := models.DefaultTemplateConfig("classic")
	cfg.EmailDefaultBody = "Hello {{CustomerName}}, your invoice is {{InvoiceNumber}}."
	cfgJSON, _ := json.Marshal(cfg)
	tmpl := models.InvoiceTemplate{
		CompanyID:  co.ID,
		Name:       "Snapshot Template",
		ConfigJSON: cfgJSON,
		IsDefault:  true,
		IsActive:   true,
	}
	db.Create(&tmpl)

	// Build a minimal InvoiceEmailLog directly to test the model fields.
	// (Full send pipeline requires SMTP which is out of scope for unit tests.)
	tmplID := tmpl.ID
	logEntry := models.InvoiceEmailLog{
		CompanyID:            co.ID,
		InvoiceID:            1,
		ToEmail:              "customer@example.com",
		SendStatus:           models.EmailSendStatusSent,
		Subject:              "Invoice #INV-001",
		BodyResolved:         "Hello Jane, your invoice is INV-001.",
		TemplateIDSnapshot:   &tmplID,
		TemplateNameSnapshot: "Snapshot Template",
		TemplateType:         "invoice",
	}
	if err := db.Create(&logEntry).Error; err != nil {
		t.Fatalf("failed to create email log: %v", err)
	}

	// Reload and verify snapshot fields are persisted.
	var reloaded models.InvoiceEmailLog
	db.First(&reloaded, logEntry.ID)

	if reloaded.BodyResolved != "Hello Jane, your invoice is INV-001." {
		t.Errorf("BodyResolved: want %q, got %q", "Hello Jane, your invoice is INV-001.", reloaded.BodyResolved)
	}
	if reloaded.TemplateIDSnapshot == nil || *reloaded.TemplateIDSnapshot != tmplID {
		t.Errorf("TemplateIDSnapshot: want %d, got %v", tmplID, reloaded.TemplateIDSnapshot)
	}
	if reloaded.TemplateNameSnapshot != "Snapshot Template" {
		t.Errorf("TemplateNameSnapshot: want %q, got %q", "Snapshot Template", reloaded.TemplateNameSnapshot)
	}
}

// ── Deactivated template does not block render ────────────────────────────────

func TestDeactivatedTemplate_RenderFallsBackGracefully(t *testing.T) {
	db := testBindingDB(t)
	co := seedBindingCompany(t, db)

	// Invoice is bound to a template that later gets deactivated.
	tmpl := seedBindingTemplate(t, db, co.ID, "Will Be Deactivated", false, true)
	cust := &models.Customer{CompanyID: co.ID, Name: "Test Cust", Email: "c@example.com"}
	db.Create(cust)

	inv := &models.Invoice{
		CompanyID:            co.ID,
		CustomerID:           cust.ID,
		InvoiceNumber:        "INV-DEC-001",
		InvoiceDate:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Status:               models.InvoiceStatusIssued,
		Amount:               decimal.RequireFromString("100.00"),
		Subtotal:             decimal.RequireFromString("100.00"),
		TaxTotal:             decimal.Zero,
		BalanceDue:           decimal.RequireFromString("100.00"),
		TemplateID:           &tmpl.ID,
		CustomerNameSnapshot: "Test Cust",
	}
	db.Create(inv)

	// Now deactivate the template.
	db.Model(tmpl).Update("is_active", false)

	// resolveRenderTemplate must not panic or error — it should fall back to system default.
	resolved := resolveRenderTemplate(db, inv, co.ID)
	if resolved.TemplateStyle == "" {
		t.Error("fallback should produce a non-empty style (system classic)")
	}
	if resolved.TemplateStyle != "classic" {
		t.Errorf("expected system fallback classic, got %q", resolved.TemplateStyle)
	}
}
