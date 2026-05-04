// 遵循project_guide.md
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Provider unit tests ───────────────────────────────────────────────────────

func TestExpenseAccountProvider_EntityType(t *testing.T) {
	var p ExpenseAccountProvider
	if got := p.EntityType(); got != "account" {
		t.Fatalf("expected %q, got %q", "account", got)
	}
}

func TestExpenseAccountProvider_Search_ReturnsOnlyExpenseAccounts(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Search Co")

	// Active expense accounts
	expID1 := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	expID2 := seedSPAccount(t, db, companyID, "6200", "Utilities", models.RootExpense, true)
	// Inactive expense account — must not appear
	seedSPAccount(t, db, companyID, "6300", "Retired Expense", models.RootExpense, false)
	// Revenue account — must not appear
	seedSPAccount(t, db, companyID, "4100", "Sales Revenue", models.RootRevenue, true)
	// Different company — must not appear
	otherID := seedCompany(t, db, "Other Co")
	seedSPAccount(t, db, otherID, "6100", "Office Supplies", models.RootExpense, true)

	var p ExpenseAccountProvider
	ctx := SmartPickerContext{CompanyID: companyID, Context: "expense_form_category", Limit: 50}
	result, err := p.Search(db, ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	ids := collectIDs(result.Candidates)
	if !ids[fmt.Sprintf("%d", expID1)] || !ids[fmt.Sprintf("%d", expID2)] {
		t.Fatalf("expected both expense accounts, got %+v", result.Candidates)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Candidates))
	}
}

func TestExpenseAccountProvider_Search_FiltersOnQuery(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Filter Co")

	seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	seedSPAccount(t, db, companyID, "6200", "Utilities", models.RootExpense, true)

	var p ExpenseAccountProvider
	ctx := SmartPickerContext{CompanyID: companyID, Context: "expense_form_category", Limit: 20}

	result, err := p.Search(db, ctx, "office")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("expected 1 match for 'office', got %d: %+v", len(result.Candidates), result.Candidates)
	}
	if result.Candidates[0].Primary != "Office Supplies" {
		t.Fatalf("expected 'Office Supplies', got %q", result.Candidates[0].Primary)
	}
}

func TestExpenseAccountProvider_Search_ItemShape(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Shape Co")
	id := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)

	var p ExpenseAccountProvider
	ctx := SmartPickerContext{CompanyID: companyID, Limit: 20}
	result, err := p.Search(db, ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Candidates))
	}
	item := result.Candidates[0]
	if item.ID != fmt.Sprintf("%d", id) {
		t.Fatalf("expected ID=%d, got %q", id, item.ID)
	}
	if item.Primary != "Office Supplies" {
		t.Fatalf("expected Primary='Office Supplies', got %q", item.Primary)
	}
	if item.Secondary != "6100" {
		t.Fatalf("expected Secondary='6100', got %q", item.Secondary)
	}
}

func TestExpenseAccountProvider_GetByID_HappyPath(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP GetByID Co")
	id := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)

	var p ExpenseAccountProvider
	item, err := p.GetByID(db, SmartPickerContext{CompanyID: companyID, Context: "expense_form_category"}, fmt.Sprintf("%d", id))
	if err != nil {
		t.Fatal(err)
	}
	if item == nil {
		t.Fatal("expected item, got nil")
	}
	if item.Primary != "Office Supplies" || item.Secondary != "6100" {
		t.Fatalf("unexpected item: %+v", item)
	}
}

func TestExpenseAccountProvider_GetByID_CrossCompanyReturnsNil(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP GBI Own Co")
	otherID := seedCompany(t, db, "SP GBI Other Co")
	id := seedSPAccount(t, db, otherID, "6100", "Office Supplies", models.RootExpense, true)

	var p ExpenseAccountProvider
	item, err := p.GetByID(db, SmartPickerContext{CompanyID: companyID, Context: "expense_form_category"}, fmt.Sprintf("%d", id))
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Fatalf("expected nil for cross-company, got %+v", item)
	}
}

func TestExpenseAccountProvider_GetByID_InactiveReturnsNil(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP GBI Inactive Co")
	id := seedSPAccount(t, db, companyID, "6100", "Retired", models.RootExpense, false)

	var p ExpenseAccountProvider
	item, err := p.GetByID(db, SmartPickerContext{CompanyID: companyID, Context: "expense_form_category"}, fmt.Sprintf("%d", id))
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Fatalf("expected nil for inactive account, got %+v", item)
	}
}

func TestExpenseAccountProvider_GetByID_NonExpenseReturnsNil(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP GBI Revenue Co")
	id := seedSPAccount(t, db, companyID, "4100", "Sales Revenue", models.RootRevenue, true)

	var p ExpenseAccountProvider
	item, err := p.GetByID(db, SmartPickerContext{CompanyID: companyID, Context: "expense_form_category"}, fmt.Sprintf("%d", id))
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Fatalf("expected nil for non-expense account, got %+v", item)
	}
}

// ── Registry unit tests ───────────────────────────────────────────────────────

func TestCustomerProvider_SearchAndGetByIDScopesToCompany(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Customer Co")
	otherID := seedCompany(t, db, "SP Customer Other Co")
	alphaID := seedSPCustomer(t, db, companyID, "Alpha Studio", "alpha@example.com")
	seedSPCustomer(t, db, companyID, "Beta Studio", "beta@example.com")
	otherAlphaID := seedSPCustomer(t, db, otherID, "Alpha Studio", "secret@example.com")

	var p CustomerProvider
	ctx := SmartPickerContext{CompanyID: companyID, Context: "invoice_customer", Limit: 20}
	result, err := p.Search(db, ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].ID != fmt.Sprintf("%d", alphaID) {
		t.Fatalf("expected only own alpha customer, got %+v", result.Candidates)
	}
	if result.Candidates[0].Primary != "Alpha Studio" || result.Candidates[0].Secondary != "alpha@example.com" {
		t.Fatalf("unexpected customer item shape: %+v", result.Candidates[0])
	}

	item, err := p.GetByID(db, ctx, fmt.Sprintf("%d", alphaID))
	if err != nil {
		t.Fatal(err)
	}
	if item == nil || item.Primary != "Alpha Studio" {
		t.Fatalf("expected own customer rehydration, got %+v", item)
	}
	item, err = p.GetByID(db, ctx, fmt.Sprintf("%d", otherAlphaID))
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Fatalf("expected nil for cross-company customer, got %+v", item)
	}
}

func TestVendorProvider_SearchAndGetByIDScopesToCompany(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Vendor Co")
	otherID := seedCompany(t, db, "SP Vendor Other Co")
	vendorID := seedSPVendor(t, db, companyID, "North Supplies", "north@example.com", "604-555-0100")
	seedSPVendor(t, db, companyID, "South Supplies", "south@example.com", "")
	otherVendorID := seedSPVendor(t, db, otherID, "North Supplies", "secret@example.com", "")

	var p VendorProvider
	ctx := SmartPickerContext{CompanyID: companyID, Context: "expense_vendor", Limit: 20}
	result, err := p.Search(db, ctx, "north")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].ID != fmt.Sprintf("%d", vendorID) {
		t.Fatalf("expected only own north vendor, got %+v", result.Candidates)
	}
	if result.Candidates[0].Primary != "North Supplies" || result.Candidates[0].Secondary != "north@example.com" {
		t.Fatalf("unexpected vendor item shape: %+v", result.Candidates[0])
	}

	item, err := p.GetByID(db, ctx, fmt.Sprintf("%d", vendorID))
	if err != nil {
		t.Fatal(err)
	}
	if item == nil || item.Primary != "North Supplies" {
		t.Fatalf("expected own vendor rehydration, got %+v", item)
	}
	item, err = p.GetByID(db, ctx, fmt.Sprintf("%d", otherVendorID))
	if err != nil {
		t.Fatal(err)
	}
	if item != nil {
		t.Fatalf("expected nil for cross-company vendor, got %+v", item)
	}
}

func TestProductServiceProvider_TaskContextGuardsSearchAndGetByID(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Product Service Co")
	otherID := seedCompany(t, db, "SP Product Service Other Co")
	revenueID := seedSPAccount(t, db, companyID, "4100", "Service Revenue", models.RootRevenue, true)
	otherRevenueID := seedSPAccount(t, db, otherID, "4100", "Other Revenue", models.RootRevenue, true)

	serviceID := seedSPProductService(t, db, companyID, revenueID, "Implementation Service", "IMPL", models.ProductServiceTypeService, true)
	inactiveID := seedSPProductService(t, db, companyID, revenueID, "Inactive Service", "OLD", models.ProductServiceTypeService, false)
	nonServiceID := seedSPProductService(t, db, companyID, revenueID, "Inventory Widget", "WID", models.ProductServiceTypeInventory, true)
	otherServiceID := seedSPProductService(t, db, otherID, otherRevenueID, "Other Service", "OTHER", models.ProductServiceTypeService, true)

	var p ProductServiceProvider
	ctx := SmartPickerContext{CompanyID: companyID, Context: "task_form_service_item", Limit: 20}
	result, err := p.Search(db, ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := collectIDs(result.Candidates)
	if !ids[fmt.Sprintf("%d", serviceID)] {
		t.Fatalf("expected active service item, got %+v", result.Candidates)
	}
	for _, notWant := range []uint{inactiveID, nonServiceID, otherServiceID} {
		if ids[fmt.Sprintf("%d", notWant)] {
			t.Fatalf("expected task context to exclude item %d, got %+v", notWant, result.Candidates)
		}
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("expected only one active same-company service, got %+v", result.Candidates)
	}

	result, err = p.Search(db, ctx, "impl")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].ID != fmt.Sprintf("%d", serviceID) {
		t.Fatalf("expected query to match service by name/SKU, got %+v", result.Candidates)
	}

	item, err := p.GetByID(db, ctx, fmt.Sprintf("%d", serviceID))
	if err != nil {
		t.Fatal(err)
	}
	if item == nil || item.Primary != "Implementation Service" {
		t.Fatalf("expected service item rehydration, got %+v", item)
	}
	for _, tc := range []struct {
		name string
		id   uint
	}{
		{name: "inactive", id: inactiveID},
		{name: "non-service", id: nonServiceID},
		{name: "cross-company", id: otherServiceID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, err := p.GetByID(db, ctx, fmt.Sprintf("%d", tc.id))
			if err != nil {
				t.Fatal(err)
			}
			if item != nil {
				t.Fatalf("expected nil for %s item, got %+v", tc.name, item)
			}
		})
	}
}

func TestProductServiceProvider_POPayloadIncludesItemAndAccountCodes(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP PO Product Co")
	revenueID := seedSPAccount(t, db, companyID, "4100", "Sales", models.RootRevenue, true)
	inventoryID := seedSPAccount(t, db, companyID, "1300", "Inventory", models.RootAsset, true)
	itemID := seedSPProductService(t, db, companyID, revenueID, "Blue Pen", "PEN-BLUE", models.ProductServiceTypeInventory, true)
	if err := db.Model(&models.ProductService{}).Where("id = ?", itemID).Update("inventory_account_id", inventoryID).Error; err != nil {
		t.Fatal(err)
	}

	var p ProductServiceProvider
	ctx := SmartPickerContext{CompanyID: companyID, Context: "po_line_item", Limit: 20}
	result, err := p.Search(db, ctx, "blue")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("expected one PO item candidate, got %+v", result.Candidates)
	}
	item := result.Candidates[0]
	if item.Primary != "Blue Pen" || item.Payload["item_code"] != "PEN-BLUE" {
		t.Fatalf("expected item name/code payload, got %+v", item)
	}
	if item.Payload["expense_account_id"] != fmt.Sprintf("%d", inventoryID) || item.Payload["account_code"] != "1300" {
		t.Fatalf("expected inventory account payload, got %+v", item.Payload)
	}

	rehydrated, err := p.GetByID(db, ctx, fmt.Sprintf("%d", itemID))
	if err != nil {
		t.Fatal(err)
	}
	if rehydrated == nil || rehydrated.Payload["account_code"] != "1300" {
		t.Fatalf("expected rehydrated account payload, got %+v", rehydrated)
	}
}

func TestSmartPickerRegistry_UnknownEntityReturnsFalse(t *testing.T) {
	_, ok := defaultSmartPickerRegistry.get("nonexistent_entity")
	if ok {
		t.Fatal("expected false for unknown entity")
	}
}

func TestSmartPickerRegistry_AccountEntityFound(t *testing.T) {
	p, ok := defaultSmartPickerRegistry.get("account")
	if !ok {
		t.Fatal("expected 'account' provider to be registered")
	}
	if p.EntityType() != "account" {
		t.Fatalf("expected EntityType='account', got %q", p.EntityType())
	}
}

// ── Handler integration tests ─────────────────────────────────────────────────

func TestSmartPickerRegistry_BatchDProvidersFound(t *testing.T) {
	for _, entity := range []string{"customer", "vendor", "product_service"} {
		p, ok := defaultSmartPickerRegistry.get(entity)
		if !ok {
			t.Fatalf("expected %q provider to be registered", entity)
		}
		if p.EntityType() != entity {
			t.Fatalf("expected EntityType=%q, got %q", entity, p.EntityType())
		}
	}
}

func TestSmartPickerHandler_RequiresAuth(t *testing.T) {
	db := testRouteDB(t)
	app := testRouteApp(t, db)

	req, _ := http.NewRequest(http.MethodGet, "/api/smart-picker/search?entity=account", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	// No session → redirected to /login
	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected non-200 without auth")
	}
}

func TestSmartPickerHandler_MissingEntityParam(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Handler Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, "/api/smart-picker/search", rawToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing entity, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "entity param required") {
		t.Fatalf("expected error message, got %q", body)
	}
}

func TestSmartPickerHandler_UnknownEntity(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Handler Co2")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, "/api/smart-picker/search?entity=widget", rawToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown entity, got %d", resp.StatusCode)
	}
}

func TestSmartPickerHandler_AccountSearchReturnsJSON(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Handler Co3")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	seedSPAccount(t, db, companyID, "6200", "Utilities", models.RootExpense, true)
	// Inactive — should not appear
	seedSPAccount(t, db, companyID, "6300", "Retired", models.RootExpense, false)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, "/api/smart-picker/search?entity=account&context=expense_form_category", rawToken)
	if resp.StatusCode != http.StatusOK {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result SmartPickerResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("expected 2 items, got %d: %+v", len(result.Candidates), result.Candidates)
	}
	if result.Candidates[0].Secondary == "" {
		t.Fatal("expected Secondary (code) to be populated")
	}
	if !result.RequiresBackendValidation {
		t.Fatal("expected requires_backend_validation to stay true in search response")
	}
}

func TestSmartPickerHandler_EchoesClientRequestID(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Handler Request Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	app := testRouteApp(t, db)

	const requestID = "sp-client-req-001"
	resp := performRequest(t, app, "/api/smart-picker/search?entity=account&context=expense_form_category&request_id="+requestID, rawToken)
	if resp.StatusCode != http.StatusOK {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result SmartPickerResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.RequestID != requestID {
		t.Fatalf("expected request_id %q, got %q", requestID, result.RequestID)
	}
}

func TestSmartPickerHandler_AccountSearchQuery(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Handler Co4")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	seedSPAccount(t, db, companyID, "6200", "Utilities", models.RootExpense, true)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, "/api/smart-picker/search?entity=account&q=util", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result SmartPickerResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Primary != "Utilities" {
		t.Fatalf("expected 1 match for 'util', got %+v", result.Candidates)
	}
}

func TestSmartPickerHandler_CrossCompanyIsolation(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Own Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	// Account belongs to another company
	otherID := seedCompany(t, db, "SP Other Co")
	seedSPAccount(t, db, otherID, "6100", "Secret Expense", models.RootExpense, true)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, "/api/smart-picker/search?entity=account", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result SmartPickerResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("expected 0 items (cross-company isolation), got %d: %+v", len(result.Candidates), result.Candidates)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// seedSPAccount creates an account for SmartPicker tests.
// SQLite boolean zero-value workaround: create with IsActive=true, then update to false if needed.
func seedSPAccount(t *testing.T, db *gorm.DB, companyID uint, code, name string, root models.RootAccountType, active bool) uint {
	t.Helper()

	detail := detailForRoot(root)
	acc := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              name,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	if !active {
		if err := db.Model(&acc).Update("is_active", false).Error; err != nil {
			t.Fatal(err)
		}
	}
	return acc.ID
}

func seedSPCustomer(t *testing.T, db *gorm.DB, companyID uint, name, email string) uint {
	t.Helper()

	customer := models.Customer{
		CompanyID: companyID,
		Name:      name,
		Email:     email,
	}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	return customer.ID
}

func seedSPVendor(t *testing.T, db *gorm.DB, companyID uint, name, email, phone string) uint {
	t.Helper()

	vendor := models.Vendor{
		CompanyID: companyID,
		Name:      name,
		Email:     email,
		Phone:     phone,
	}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	return vendor.ID
}

func seedSPProductService(t *testing.T, db *gorm.DB, companyID, revenueAccountID uint, name, sku string, itemType models.ProductServiceType, active bool) uint {
	t.Helper()

	item := models.ProductService{
		CompanyID:         companyID,
		Name:              name,
		SKU:               sku,
		Type:              itemType,
		RevenueAccountID:  revenueAccountID,
		IsActive:          true,
		ItemStructureType: models.ItemStructureSingle,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if !active {
		if err := db.Model(&item).Update("is_active", false).Error; err != nil {
			t.Fatal(err)
		}
	}
	return item.ID
}

// detailForRoot picks a representative detail type for a given root.
func detailForRoot(root models.RootAccountType) models.DetailAccountType {
	switch root {
	case models.RootExpense:
		return models.DetailOperatingExpense
	case models.RootRevenue:
		return models.DetailServiceRevenue
	case models.RootAsset:
		return models.DetailOtherAsset
	default:
		return models.DetailOperatingExpense
	}
}

// collectIDs converts a slice of SmartPickerItems to a set of IDs for easy membership checks.
func collectIDs(items []SmartPickerItem) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it.ID] = true
	}
	return m
}
