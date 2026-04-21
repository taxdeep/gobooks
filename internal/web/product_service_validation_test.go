// 遵循project_guide.md
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── shared test setup ─────────────────────────────────────────────────────────

func testProductServiceValidationDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_ps_validation_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.User{},
		&models.CompanyMembership{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.ItemComponent{},
		&models.AuditLog{},
		&models.Warehouse{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedPSUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()

	u := &models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("%s@example.com", t.Name()),
		PasswordHash: "not-used",
		DisplayName:  "PS Validation Test",
		IsActive:     true,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func productServiceValidationApp(server *Server, user *models.User, companyID uint) *fiber.App {
	app := fiber.New()
	membership := &models.CompanyMembership{Role: models.CompanyRoleAdmin}
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, user)
		c.Locals(LocalsActiveCompanyID, companyID)
		c.Locals(LocalsCompanyMembership, membership)
		return c.Next()
	})
	app.Post("/products-services", server.handleProductServiceCreate)
	app.Post("/products-services/update", server.handleProductServiceUpdate)
	app.Post("/products-services/inactive", server.handleProductServiceInactive)
	return app
}

// ── Batch 1: company-scope validation on create/update ────────────────────────

// TestProductServiceCreate_RejectsCrossCompanyRevenueAccount verifies that the
// create handler refuses a revenue_account_id that belongs to a different company.
// This is the server-side guard; the UI only offers company-owned accounts in the
// dropdown, but we must not trust raw POST values.
func TestProductServiceCreate_RejectsCrossCompanyRevenueAccount(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	_ = seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	revenueBID := seedValidationAccount(t, db, companyB, "4000", models.RootRevenue, models.DetailServiceRevenue)

	app := productServiceValidationApp(server, user, companyA)

	// POST with company-B's revenue account while authenticated as company-A.
	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":               {"Consulting"},
		"type":               {string(models.ProductServiceTypeService)},
		"structure_type":     {string(models.ItemStructureSingle)},
		"default_price":      {"100.00"},
		"revenue_account_id": {fmt.Sprintf("%d", revenueBID)},
	}, "")

	// Must re-render the form with an error (200), not redirect on success.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected cross-company revenue account error in body, got %q", body)
	}

	// No item must have been created.
	var count int64
	db.Model(&models.ProductService{}).Where("company_id = ?", companyA).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 items for company A, got %d", count)
	}
}

// TestProductServiceCreate_RejectsCrossCompanyTaxCode verifies that a default_tax_code_id
// belonging to a different company is rejected at create time.
func TestProductServiceCreate_RejectsCrossCompanyTaxCode(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityBID := seedValidationAccount(t, db, companyB, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	taxBID := seedValidationTaxCode(t, db, companyB, liabilityBID, "GST-B")

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":                 {"Consulting"},
		"type":                 {string(models.ProductServiceTypeService)},
		"structure_type":       {string(models.ItemStructureSingle)},
		"default_price":        {"100.00"},
		"revenue_account_id":   {fmt.Sprintf("%d", revenueAID)},
		"default_tax_code_id":  {fmt.Sprintf("%d", taxBID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected cross-company tax code error in body, got %q", body)
	}

	var count int64
	db.Model(&models.ProductService{}).Where("company_id = ?", companyA).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 items for company A, got %d", count)
	}
}

// TestProductServiceUpdate_RejectsCrossCompanyRevenueAccount verifies the same
// company-scope guard applies on update.
func TestProductServiceUpdate_RejectsCrossCompanyRevenueAccount(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	revenueBID := seedValidationAccount(t, db, companyB, "4000", models.RootRevenue, models.DetailServiceRevenue)

	item := models.ProductService{
		CompanyID:        companyA,
		Name:             "Consulting",
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revenueAID,
		IsActive:         true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services/update", url.Values{
		"item_id":            {fmt.Sprintf("%d", item.ID)},
		"name":               {"Consulting"},
		"type":               {string(models.ProductServiceTypeService)},
		"structure_type":     {string(models.ItemStructureSingle)},
		"default_price":      {"100.00"},
		"revenue_account_id": {fmt.Sprintf("%d", revenueBID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected cross-company revenue account error in body, got %q", body)
	}

	// Revenue account must not have changed.
	var reloaded models.ProductService
	if err := db.First(&reloaded, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.RevenueAccountID != revenueAID {
		t.Fatalf("expected revenue account to remain %d, got %d", revenueAID, reloaded.RevenueAccountID)
	}
}

// TestProductServiceCreate_ValidCompanyScopeSucceeds verifies the happy path:
// a correctly-scoped create with a company-owned revenue account succeeds.
func TestProductServiceCreate_ValidCompanyScopeSucceeds(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityAID := seedValidationAccount(t, db, companyA, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	taxAID := seedValidationTaxCode(t, db, companyA, liabilityAID, "GST-A")

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":                {"Web Design"},
		"type":                {string(models.ProductServiceTypeService)},
		"structure_type":      {string(models.ItemStructureSingle)},
		"default_price":       {"250.00"},
		"revenue_account_id":  {fmt.Sprintf("%d", revenueAID)},
		"default_tax_code_id": {fmt.Sprintf("%d", taxAID)},
	}, "")

	// Successful create redirects.
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on success, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); !strings.HasPrefix(got, "/products-services") {
		t.Fatalf("expected redirect to /products-services, got %q", got)
	}

	var item models.ProductService
	if err := db.Where("company_id = ? AND name = ?", companyA, "Web Design").First(&item).Error; err != nil {
		t.Fatalf("expected item to exist: %v", err)
	}
	if item.RevenueAccountID != revenueAID {
		t.Fatalf("expected revenue account %d, got %d", revenueAID, item.RevenueAccountID)
	}
	if item.DefaultTaxCodeID == nil || *item.DefaultTaxCodeID != taxAID {
		t.Fatalf("expected tax code %d, got %v", taxAID, item.DefaultTaxCodeID)
	}
}

// ── Batch 2: invoice line ↔ product defaults ──────────────────────────────────

// TestBuildProductsJSON_IncludesDescription verifies that buildProductsJSON
// serialises the item's Description field so that the invoice editor can
// prefer it over the item Name when auto-filling lines.
func TestBuildProductsJSON_IncludesDescription(t *testing.T) {
	products := []models.ProductService{
		{
			ID:          1,
			Name:        "Web Design",
			Description: "Custom website design and development",
			DefaultPrice: mustDecimal(t, "250.00"),
		},
		{
			ID:          2,
			Name:        "Hosting",
			Description: "", // empty: JS should fall back to name
		},
	}

	got := buildProductsJSON(products)

	if !strings.Contains(got, `"description":"Custom website design and development"`) {
		t.Fatalf("expected description in JSON, got %q", got)
	}
	if !strings.Contains(got, `"name":"Web Design"`) {
		t.Fatalf("expected name in JSON, got %q", got)
	}
	// Empty description must be present (not omitted) so the JS receives it.
	if !strings.Contains(got, `"description":""`) {
		t.Fatalf("expected empty description field to be present in JSON, got %q", got)
	}
}

// ── Batch 2: inactive item protection at draft save ───────────────────────────

// TestValidateInvoiceDraftReferences_RejectsInactiveProductService confirms
// that an invoice draft cannot be saved with an inactive product/service on a
// line. The check is at validateInvoiceDraftReferences level (before DB write).
func TestValidateInvoiceDraftReferences_RejectsInactiveProductService(t *testing.T) {
	db := testInvoiceEditorValidationDB(t)
	server := &Server{DB: db}

	companyID := seedValidationCompany(t, db, "Inactive Item Co")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)

	// Create an item then mark it inactive.
	inactiveItemID := seedValidationProduct(t, db, companyID, revenueID, "Archived Service")
	if err := db.Model(&models.ProductService{}).Where("id = ?", inactiveItemID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	lines := []parsedInvoiceLine{{ProductServiceID: &inactiveItemID}}
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")
	err := server.validateInvoiceDraftReferences(companyID, customerID, lines)
	if err == nil {
		t.Fatal("expected error for inactive product/service, got nil")
	}
	if !strings.Contains(err.Error(), "invalid product/service") {
		t.Fatalf("expected 'invalid product/service' in error, got %q", err.Error())
	}
}

// TestValidateInvoiceDraftReferences_AllowsNilProductServiceID confirms that
// free-form lines (no product/service) are still allowed at draft-save time.
// The posting engine requires an item, but drafts remain permissive.
func TestValidateInvoiceDraftReferences_AllowsNilProductServiceID(t *testing.T) {
	db := testInvoiceEditorValidationDB(t)
	server := &Server{DB: db}

	companyID := seedValidationCompany(t, db, "Free-form Line Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")

	// A line with no product/service (nil) must pass draft validation.
	lines := []parsedInvoiceLine{{ProductServiceID: nil, Description: "Custom work"}}
	if err := server.validateInvoiceDraftReferences(companyID, customerID, lines); err != nil {
		t.Fatalf("expected nil error for free-form line, got %v", err)
	}
}

// ── Batch 1 P1 leftovers ──────────────────────────────────────────────────────

// TestProductServiceUpdate_RejectsCrossCompanyTaxCode verifies that the update
// handler refuses a default_tax_code_id belonging to a different company, and
// that the DB record is not mutated.
func TestProductServiceUpdate_RejectsCrossCompanyTaxCode(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityAID := seedValidationAccount(t, db, companyA, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	taxAID := seedValidationTaxCode(t, db, companyA, liabilityAID, "GST-A")
	liabilityBID := seedValidationAccount(t, db, companyB, "2100", models.RootLiability, models.DetailSalesTaxPayable)
	taxBID := seedValidationTaxCode(t, db, companyB, liabilityBID, "GST-B")

	// Existing item using company-A's tax code.
	item := models.ProductService{
		CompanyID:        companyA,
		Name:             "Consulting",
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revenueAID,
		DefaultTaxCodeID: &taxAID,
		IsActive:         true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	app := productServiceValidationApp(server, user, companyA)

	// POST update with company-B's tax code while authenticated as company-A.
	resp := performFormRequest(t, app, http.MethodPost, "/products-services/update", url.Values{
		"item_id":             {fmt.Sprintf("%d", item.ID)},
		"name":                {"Consulting"},
		"type":                {string(models.ProductServiceTypeService)},
		"structure_type":      {string(models.ItemStructureSingle)},
		"default_price":       {"100.00"},
		"revenue_account_id":  {fmt.Sprintf("%d", revenueAID)},
		"default_tax_code_id": {fmt.Sprintf("%d", taxBID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected cross-company tax code error in body, got %q", body)
	}

	// DefaultTaxCodeID must not have changed.
	var reloaded models.ProductService
	if err := db.First(&reloaded, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.DefaultTaxCodeID == nil || *reloaded.DefaultTaxCodeID != taxAID {
		t.Fatalf("expected tax code to remain %d, got %v", taxAID, reloaded.DefaultTaxCodeID)
	}
}

// ── Batch 1 P2 leftovers ──────────────────────────────────────────────────────

// TestProductServiceCreate_RejectsCrossCompanyCOGSAccount verifies that the
// create handler refuses a cogs_account_id belonging to a different company.
func TestProductServiceCreate_RejectsCrossCompanyCOGSAccount(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	cogsAID := seedValidationAccount(t, db, companyA, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	cogsBID := seedValidationAccount(t, db, companyB, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	invAID := seedValidationAccount(t, db, companyA, "1300", models.RootAsset, models.DetailInventory)
	_ = cogsAID

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":                 {"Widget"},
		"type":                 {string(models.ProductServiceTypeInventory)},
		"structure_type":       {string(models.ItemStructureSingle)},
		"default_price":        {"50.00"},
		"revenue_account_id":   {fmt.Sprintf("%d", revenueAID)},
		"cogs_account_id":      {fmt.Sprintf("%d", cogsBID)},
		"inventory_account_id": {fmt.Sprintf("%d", invAID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected cross-company COGS account error in body, got %q", body)
	}

	var count int64
	db.Model(&models.ProductService{}).Where("company_id = ?", companyA).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 items created, got %d", count)
	}
}

// TestProductServiceUpdate_RejectsCrossCompanyCOGSAccount verifies the same
// company-scope guard on the update path for the COGS account.
func TestProductServiceUpdate_RejectsCrossCompanyCOGSAccount(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	cogsAID := seedValidationAccount(t, db, companyA, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	cogsBID := seedValidationAccount(t, db, companyB, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	invAID := seedValidationAccount(t, db, companyA, "1300", models.RootAsset, models.DetailInventory)

	item := models.ProductService{
		CompanyID:          companyA,
		Name:               "Widget",
		Type:               models.ProductServiceTypeInventory,
		RevenueAccountID:   revenueAID,
		COGSAccountID:      &cogsAID,
		InventoryAccountID: &invAID,
		IsActive:           true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services/update", url.Values{
		"item_id":              {fmt.Sprintf("%d", item.ID)},
		"name":                 {"Widget"},
		"type":                 {string(models.ProductServiceTypeInventory)},
		"structure_type":       {string(models.ItemStructureSingle)},
		"default_price":        {"50.00"},
		"revenue_account_id":   {fmt.Sprintf("%d", revenueAID)},
		"cogs_account_id":      {fmt.Sprintf("%d", cogsBID)},
		"inventory_account_id": {fmt.Sprintf("%d", invAID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected cross-company COGS account error in body, got %q", body)
	}

	// COGS account must not have changed.
	var reloaded models.ProductService
	if err := db.First(&reloaded, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.COGSAccountID == nil || *reloaded.COGSAccountID != cogsAID {
		t.Fatalf("expected COGS account to remain %d, got %v", cogsAID, reloaded.COGSAccountID)
	}
}

// ── Account type correctness tests (P1 fix) ───────────────────────────────────
//
// These tests verify that the backend rejects a same-company account that has
// the wrong account type, even when it passes the company-scope check.
// This closes the forged-POST gap where the UI's dropdown filter is bypassed.

// TestProductServiceCreate_RejectsWrongTypeCOGSAccount verifies that a COGS
// account submitted with a non-cost-of-sales root type is rejected at create time.
func TestProductServiceCreate_RejectsWrongTypeCOGSAccount(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	// A revenue-type account submitted as COGS — same company, wrong type.
	wrongTypeCOGS := seedValidationAccount(t, db, companyA, "4100", models.RootRevenue, models.DetailServiceRevenue)
	invAID := seedValidationAccount(t, db, companyA, "1300", models.RootAsset, models.DetailInventory)

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":                 {"Widget"},
		"type":                 {string(models.ProductServiceTypeInventory)},
		"structure_type":       {string(models.ItemStructureSingle)},
		"default_price":        {"50.00"},
		"revenue_account_id":   {fmt.Sprintf("%d", revenueAID)},
		"cogs_account_id":      {fmt.Sprintf("%d", wrongTypeCOGS)},
		"inventory_account_id": {fmt.Sprintf("%d", invAID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected wrong-type COGS error in body, got %q", body)
	}

	var count int64
	db.Model(&models.ProductService{}).Where("company_id = ?", companyA).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 items created, got %d", count)
	}
}

// TestProductServiceUpdate_RejectsWrongTypeCOGSAccount verifies the same
// account-type guard on the update path.
func TestProductServiceUpdate_RejectsWrongTypeCOGSAccount(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	cogsAID := seedValidationAccount(t, db, companyA, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	invAID := seedValidationAccount(t, db, companyA, "1300", models.RootAsset, models.DetailInventory)
	// Same-company account with wrong type used as forged COGS replacement.
	wrongTypeCOGS := seedValidationAccount(t, db, companyA, "4100", models.RootRevenue, models.DetailServiceRevenue)

	item := models.ProductService{
		CompanyID:          companyA,
		Name:               "Widget",
		Type:               models.ProductServiceTypeInventory,
		RevenueAccountID:   revenueAID,
		COGSAccountID:      &cogsAID,
		InventoryAccountID: &invAID,
		IsActive:           true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services/update", url.Values{
		"item_id":              {fmt.Sprintf("%d", item.ID)},
		"name":                 {"Widget"},
		"type":                 {string(models.ProductServiceTypeInventory)},
		"structure_type":       {string(models.ItemStructureSingle)},
		"default_price":        {"50.00"},
		"revenue_account_id":   {fmt.Sprintf("%d", revenueAID)},
		"cogs_account_id":      {fmt.Sprintf("%d", wrongTypeCOGS)},
		"inventory_account_id": {fmt.Sprintf("%d", invAID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected wrong-type COGS error in body, got %q", body)
	}

	var reloaded models.ProductService
	if err := db.First(&reloaded, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.COGSAccountID == nil || *reloaded.COGSAccountID != cogsAID {
		t.Fatalf("expected COGS account to remain %d, got %v", cogsAID, reloaded.COGSAccountID)
	}
}

// TestProductServiceCreate_RejectsWrongTypeInventoryAccount verifies that an
// inventory asset account with an incorrect detail type is rejected at create time.
func TestProductServiceCreate_RejectsWrongTypeInventoryAccount(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	cogsAID := seedValidationAccount(t, db, companyA, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	// A liability account submitted as inventory asset — same company, wrong detail type.
	wrongTypeInv := seedValidationAccount(t, db, companyA, "2100", models.RootLiability, models.DetailSalesTaxPayable)

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":                 {"Widget"},
		"type":                 {string(models.ProductServiceTypeInventory)},
		"structure_type":       {string(models.ItemStructureSingle)},
		"default_price":        {"50.00"},
		"revenue_account_id":   {fmt.Sprintf("%d", revenueAID)},
		"cogs_account_id":      {fmt.Sprintf("%d", cogsAID)},
		"inventory_account_id": {fmt.Sprintf("%d", wrongTypeInv)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected wrong-type inventory error in body, got %q", body)
	}

	var count int64
	db.Model(&models.ProductService{}).Where("company_id = ?", companyA).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 items created, got %d", count)
	}
}

// TestProductServiceUpdate_RejectsWrongTypeInventoryAccount verifies the same
// detail-type guard for inventory asset accounts on the update path.
func TestProductServiceUpdate_RejectsWrongTypeInventoryAccount(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	revenueAID := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	cogsAID := seedValidationAccount(t, db, companyA, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	invAID := seedValidationAccount(t, db, companyA, "1300", models.RootAsset, models.DetailInventory)
	// A revenue account submitted as inventory asset — same company, wrong detail type.
	wrongTypeInv := seedValidationAccount(t, db, companyA, "4100", models.RootRevenue, models.DetailServiceRevenue)

	item := models.ProductService{
		CompanyID:          companyA,
		Name:               "Widget",
		Type:               models.ProductServiceTypeInventory,
		RevenueAccountID:   revenueAID,
		COGSAccountID:      &cogsAID,
		InventoryAccountID: &invAID,
		IsActive:           true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	app := productServiceValidationApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services/update", url.Values{
		"item_id":              {fmt.Sprintf("%d", item.ID)},
		"name":                 {"Widget"},
		"type":                 {string(models.ProductServiceTypeInventory)},
		"structure_type":       {string(models.ItemStructureSingle)},
		"default_price":        {"50.00"},
		"revenue_account_id":   {fmt.Sprintf("%d", revenueAID)},
		"cogs_account_id":      {fmt.Sprintf("%d", cogsAID)},
		"inventory_account_id": {fmt.Sprintf("%d", wrongTypeInv)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company") {
		t.Fatalf("expected wrong-type inventory error in body, got %q", body)
	}

	var reloaded models.ProductService
	if err := db.First(&reloaded, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.InventoryAccountID == nil || *reloaded.InventoryAccountID != invAID {
		t.Fatalf("expected inventory account to remain %d, got %v", invAID, reloaded.InventoryAccountID)
	}
}
