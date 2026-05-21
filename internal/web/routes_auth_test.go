package web

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/config"
	"balanciz/internal/models"
	"balanciz/internal/services"
)

func testRouteDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_routes_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.Company{},
		&models.Session{},
		&models.CompanyMembership{},
		&models.CompanyInvitation{},
		&models.Account{},
		&models.AuditLog{},
		&models.COATemplate{},
		&models.COATemplateAccount{},
		&models.SystemSetting{},
		// Task module: EnsureSystemTaskItems is called during company setup.
		&models.TaxCode{},
		&models.Customer{},
		&models.Vendor{},
		&models.NumberingSetting{},
		&models.PaymentTerm{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Bill{},
		&models.BillLine{},
		&models.ProductService{},
		&models.Task{},
		&models.TaskLine{},
		&models.Expense{},
		&models.ExpenseLine{},
		&models.TaskInvoiceSource{},
		&models.UserPreference{},
		&models.UserCompanyPermission{},
		&models.CompanyFeature{},
		&models.AccountingBook{},
		&models.UserPlan{},
		// Customer + Vendor detail pages read from these for their
		// Transactions tabs (ListSalesTransactions / ListPurchaseTransactions
		// touch every AR/AP document family) and sidebar summaries
		// (credits / refunds / shipping / allowed currencies). Missing
		// tables caused the query-layer fail-fast to nil-out the whole
		// Transactions feed when running against the sqlite test DB.
		&models.Quote{},
		&models.SalesOrder{},
		&models.CustomerReceipt{},
		&models.CreditNote{},
		&models.ARReturn{},
		&models.ARRefund{},
		&models.CustomerCredit{},
		&models.CustomerAllowedCurrency{},
		&models.CustomerShippingAddress{},
		&models.PurchaseOrder{},
		&models.VendorCreditNote{},
		&models.VendorRefund{},
		&models.SmartPickerUsage{},
		&models.SmartPickerEvent{},
		&models.SmartPickerUsageStat{},
		&models.SmartPickerPairStat{},
		&models.SmartPickerRecentQuery{},
		&models.SmartPickerLearningProfile{},
		&models.SmartPickerRankingHint{},
		&models.SmartPickerAliasSuggestion{},
		&models.AIJobRun{},
		&models.AIRequestLog{},
		&models.SmartPickerDecisionTrace{},
		&models.ReportUsageEvent{},
		&models.ReportUsageStat{},
		&models.DashboardUserWidget{},
		&models.DashboardWidgetSuggestion{},
		&models.ActionCenterTask{},
		&models.ActionCenterTaskEvent{},
		// Customer Deposit — AR liability document for overpayments
		// and manual prepayments. Successor to CustomerCredit.
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func testRouteApp(t *testing.T, db *gorm.DB) *fiber.App {
	t.Helper()
	return NewServer(config.Config{
		Env:  "test",
		Addr: ":0",
	}, db)
}

func TestOperationalProbeRoutesArePublic(t *testing.T) {
	db := testRouteDB(t)
	app := testRouteApp(t, db)

	for _, path := range []string{"/healthz", "/readyz", "/version"} {
		resp := performRequest(t, app, path, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: expected %d, got %d", path, http.StatusOK, resp.StatusCode)
		}
	}
}

func seedCompany(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	company := models.Company{
		Name:                    name,
		EntityType:              models.EntityTypeIncorporated,
		BusinessType:            models.BusinessTypeRetail,
		Industry:                models.IndustryRetail,
		IncorporatedDate:        "2024-01-01",
		FiscalYearEnd:           "12-31",
		BusinessNumber:          "123456789",
		AddressLine:             "123 Main",
		City:                    "Vancouver",
		Province:                "BC",
		PostalCode:              "V6B1A1",
		Country:                 "CA",
		AccountCodeLength:       4,
		AccountCodeLengthLocked: true,
	}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	return company.ID
}

func seedUserSession(t *testing.T, db *gorm.DB, activeCompanyID *uint) (models.User, string) {
	t.Helper()

	user := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("%s@example.com", t.Name()),
		PasswordHash: "not-used-in-route-tests",
		DisplayName:  "Route Test",
		IsActive:     true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	rawToken, tokenHash, err := NewOpaqueSessionToken()
	if err != nil {
		t.Fatal(err)
	}

	session := models.Session{
		ID:              uuid.New(),
		TokenHash:       tokenHash,
		UserID:          user.ID,
		ActiveCompanyID: activeCompanyID,
		ExpiresAt:       time.Now().UTC().Add(24 * time.Hour),
		CreatedAt:       time.Now().UTC(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	return user, rawToken
}

func seedMembership(t *testing.T, db *gorm.DB, userID uuid.UUID, companyID uint) {
	t.Helper()

	membership := models.CompanyMembership{
		ID:        uuid.New(),
		UserID:    userID,
		CompanyID: companyID,
		Role:      models.CompanyRoleAdmin,
		IsActive:  true,
	}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatal(err)
	}
	ensureTestFeature(t, db, companyID, models.FeatureKeyTask)
}

func seedMembershipWithRole(t *testing.T, db *gorm.DB, userID uuid.UUID, companyID uint, role models.CompanyRole) {
	t.Helper()
	membership := models.CompanyMembership{
		ID:        uuid.New(),
		UserID:    userID,
		CompanyID: companyID,
		Role:      role,
		IsActive:  true,
	}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatal(err)
	}
	ensureTestFeature(t, db, companyID, models.FeatureKeyTask)
}

func ensureTestFeature(t *testing.T, db *gorm.DB, companyID uint, feature models.FeatureKey) {
	t.Helper()

	row := models.CompanyFeature{
		CompanyID:  companyID,
		FeatureKey: feature,
		Status:     models.FeatureStatusEnabled,
		Maturity:   models.FeatureMaturityBeta,
	}
	if err := db.Where("company_id = ? AND feature_key = ?", companyID, feature).FirstOrCreate(&row).Error; err != nil {
		t.Fatal(err)
	}
}

func performRequest(t *testing.T, app *fiber.App, path string, rawToken string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if rawToken != "" {
		req.AddCookie(&http.Cookie{
			Name:  SessionCookieName,
			Value: rawToken,
			Path:  "/",
		})
	}

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func performFormRequest(t *testing.T, app *fiber.App, method string, path string, form url.Values, rawToken string) *http.Response {
	t.Helper()

	var body []byte
	if form != nil {
		body = []byte(form.Encode())
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if rawToken != "" {
		req.AddCookie(&http.Cookie{
			Name:  SessionCookieName,
			Value: rawToken,
			Path:  "/",
		})
	}

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestProtectedRoutesRedirectToLoginWhenUnauthenticated(t *testing.T) {
	db := testRouteDB(t)
	seedCompany(t, db, "Acme")
	app := testRouteApp(t, db)

	tests := []struct {
		name string
		path string
	}{
		{name: "dashboard", path: "/"},
		{name: "report", path: "/reports/trial-balance"},
		{name: "audit log", path: "/settings/audit-log"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := performRequest(t, app, tc.path, "")
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
			}
			if got := resp.Header.Get("Location"); got != "/login" {
				t.Fatalf("expected redirect to /login, got %q", got)
			}
		})
	}
}

func TestProtectedRoutesRedirectToSelectCompanyWhenSessionHasNoResolvableActiveCompany(t *testing.T) {
	db := testRouteDB(t)
	companyA := seedCompany(t, db, "Acme")
	companyB := seedCompany(t, db, "Beta")
	user, rawToken := seedUserSession(t, db, nil)
	seedMembership(t, db, user.ID, companyA)
	seedMembership(t, db, user.ID, companyB)

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/reports/trial-balance", rawToken)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/select-company" {
		t.Fatalf("expected redirect to /select-company, got %q", got)
	}
}

func TestProtectedRoutesRedirectToSelectCompanyWhenSessionActiveCompanyIsStale(t *testing.T) {
	db := testRouteDB(t)
	companyA := seedCompany(t, db, "Acme")
	companyB := seedCompany(t, db, "Beta")
	staleCompanyID := uint(9999)
	user, rawToken := seedUserSession(t, db, &staleCompanyID)
	seedMembership(t, db, user.ID, companyA)
	seedMembership(t, db, user.ID, companyB)

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/settings/audit-log", rawToken)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/select-company" {
		t.Fatalf("expected redirect to /select-company, got %q", got)
	}
}

func TestTaskRoutesRequireTaskFeature(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Feature Gate Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	if err := db.Create(&models.CompanyMembership{
		ID:        uuid.New(),
		UserID:    user.ID,
		CompanyID: companyID,
		Role:      models.CompanyRoleAdmin,
		IsActive:  true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/tasks", rawToken)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected disabled task module to return %d, got %d", http.StatusNotFound, resp.StatusCode)
	}
}

func TestPermissionedDashboardLinksMatchBackendGuards(t *testing.T) {
	tests := []struct {
		name       string
		role       models.CompanyRole
		path       string
		wantStatus int
	}{
		{name: "ap cannot open sales overview", role: models.CompanyRoleAP, path: "/sales-overview", wantStatus: http.StatusForbidden},
		{name: "ap cannot open sales transactions", role: models.CompanyRoleAP, path: "/sales-transactions", wantStatus: http.StatusForbidden},
		{name: "ap cannot open sales transactions api", role: models.CompanyRoleAP, path: "/api/sales-transactions", wantStatus: http.StatusForbidden},
		{name: "ap cannot open customer deposits", role: models.CompanyRoleAP, path: "/deposits", wantStatus: http.StatusForbidden},
		{name: "ap cannot open customer receipts", role: models.CompanyRoleAP, path: "/receipts", wantStatus: http.StatusForbidden},
		{name: "ap cannot open customer returns", role: models.CompanyRoleAP, path: "/returns", wantStatus: http.StatusForbidden},
		{name: "ap cannot open customer refunds", role: models.CompanyRoleAP, path: "/refunds", wantStatus: http.StatusForbidden},
		{name: "ap cannot open write offs", role: models.CompanyRoleAP, path: "/write-offs", wantStatus: http.StatusForbidden},
		{name: "ap cannot open customer statement", role: models.CompanyRoleAP, path: "/customer-statement", wantStatus: http.StatusForbidden},
		{name: "ap can open ap aging", role: models.CompanyRoleAP, path: "/ap-aging", wantStatus: http.StatusOK},
		{name: "viewer cannot open vendor prepayments", role: models.CompanyRoleViewer, path: "/vendor-prepayments", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open vendor refunds", role: models.CompanyRoleViewer, path: "/vendor-refunds", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open inventory stock", role: models.CompanyRoleViewer, path: "/inventory/stock", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open inventory transfers", role: models.CompanyRoleViewer, path: "/inventory/transfers", wantStatus: http.StatusForbidden},
		{name: "viewer cannot download shipment pdf", role: models.CompanyRoleViewer, path: "/shipments/1/pdf-v2", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open warehouses", role: models.CompanyRoleViewer, path: "/warehouses", wantStatus: http.StatusForbidden},
		{name: "viewer can open template settings read page", role: models.CompanyRoleViewer, path: "/settings/invoice-templates/manage", wantStatus: http.StatusOK},
		{name: "viewer cannot open payment gateway settings", role: models.CompanyRoleViewer, path: "/settings/payment-gateways", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open payment gateway transactions", role: models.CompanyRoleViewer, path: "/settings/payment-gateways/transactions", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open channel settings", role: models.CompanyRoleViewer, path: "/settings/channels", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open ar ap control settings", role: models.CompanyRoleViewer, path: "/settings/ar-ap-control", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open ai connect settings", role: models.CompanyRoleViewer, path: "/settings/ai-connect", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open notification settings", role: models.CompanyRoleViewer, path: "/settings/company/notifications", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open security settings", role: models.CompanyRoleViewer, path: "/settings/company/security", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open pdf template settings", role: models.CompanyRoleViewer, path: "/settings/templates", wantStatus: http.StatusForbidden},
		{name: "viewer cannot open pdf templates direct route", role: models.CompanyRoleViewer, path: "/pdf-templates", wantStatus: http.StatusForbidden},
		{name: "ap cannot open payment gateway settings", role: models.CompanyRoleAP, path: "/settings/payment-gateways", wantStatus: http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := testRouteDB(t)
			companyID := seedCompany(t, db, "Permission Guard Co")
			user, rawToken := seedUserSession(t, db, &companyID)
			seedMembershipWithRole(t, db, user.ID, companyID, tc.role)
			app := testRouteApp(t, db)

			resp := performRequest(t, app, tc.path, rawToken)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("%s: expected %d, got %d", tc.path, tc.wantStatus, resp.StatusCode)
			}
		})
	}
}

func TestCompanyFeaturesPageEnablesTaskModule(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Feature Enable Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	if err := db.Create(&models.CompanyMembership{
		ID:        uuid.New(),
		UserID:    user.ID,
		CompanyID: companyID,
		Role:      models.CompanyRoleOwner,
		IsActive:  true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	app := testRouteApp(t, db)

	pageResp := performRequest(t, app, "/settings/company/features", rawToken)
	defer pageResp.Body.Close()
	pageBody, _ := io.ReadAll(pageResp.Body)
	body := string(pageBody)
	if !strings.Contains(body, "Before you enable Task:") {
		t.Fatalf("expected task-specific enable copy, got %q", body)
	}
	if !strings.Contains(body, "Task adds work tracking") {
		t.Fatalf("expected task-specific feature warning copy")
	}

	form := url.Values{}
	form.Set("feature_key", string(models.FeatureKeyTask))
	form.Set("reason_code", string(models.ReasonCodeTrialPilot))
	form.Set("typed_confirmation", "ENABLE TASK")
	csrf := newCSRFToken(t)
	form.Set(CSRFFormField, csrf)
	req := httptest.NewRequest(http.MethodPost, "/settings/company/features/enable", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"})
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"})
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect after enabling task, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); !strings.Contains(got, "next=members") {
		t.Fatalf("expected enable redirect to include member-permission next step, got %q", got)
	}

	enabled, err := services.IsCompanyFeatureEnabled(db, companyID, models.FeatureKeyTask)
	if err != nil {
		t.Fatalf("is task enabled: %v", err)
	}
	if !enabled {
		t.Fatalf("expected task feature enabled")
	}

	followResp := performRequest(t, app, resp.Header.Get("Location"), rawToken)
	defer followResp.Body.Close()
	followBody, _ := io.ReadAll(followResp.Body)
	follow := string(followBody)
	if !strings.Contains(follow, "Manage member permissions") {
		t.Fatalf("expected post-enable CTA to manage member permissions")
	}
	if !strings.Contains(follow, "Task access is controlled in Members") {
		t.Fatalf("expected enabled task card to show permissions hint")
	}
}

func TestLegacySetupPostRedirectsToLoginWhenUnauthenticated(t *testing.T) {
	db := testRouteDB(t)
	_, _ = seedUserSession(t, db, nil)
	app := testRouteApp(t, db)

	resp := performFormRequest(t, app, http.MethodPost, "/setup", nil, "")

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/login" {
		t.Fatalf("expected redirect to /login, got %q", got)
	}
}

func TestLegacySetupCreatesOwnerMembershipAndActivatesSessionCompany(t *testing.T) {
	db := testRouteDB(t)
	if err := services.SeedDefaultCOATemplate(db); err != nil {
		t.Fatal(err)
	}
	user, rawToken := seedUserSession(t, db, nil)
	app := testRouteApp(t, db)

	form := url.Values{}
	form.Set("company_name", "Acme Setup Co")
	form.Set("entity_type", string(models.EntityTypeIncorporated))
	form.Set("address_line", "123 Main")
	form.Set("city", "Vancouver")
	form.Set("province", "BC")
	form.Set("postal_code", "V6B1A1")
	form.Set("country", "CA")
	form.Set("business_number", "123456789")
	form.Set("industry", string(models.IndustryRetail))
	form.Set("incorporated_date", "2024-01-01")
	form.Set("fiscal_year_end", "12-31")
	form.Set("account_code_length", "4")

	resp := performFormRequest(t, app, http.MethodPost, "/setup", form, rawToken)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/" {
		t.Fatalf("expected redirect to /, got %q", got)
	}

	var company models.Company
	if err := db.Where("name = ?", "Acme Setup Co").First(&company).Error; err != nil {
		t.Fatal(err)
	}

	var membership models.CompanyMembership
	if err := db.Where("user_id = ? AND company_id = ?", user.ID, company.ID).First(&membership).Error; err != nil {
		t.Fatal(err)
	}
	if membership.Role != models.CompanyRoleOwner {
		t.Fatalf("expected owner membership, got %s", membership.Role)
	}
	if !membership.IsActive {
		t.Fatal("expected membership to be active")
	}

	var session models.Session
	if err := db.Where("user_id = ?", user.ID).First(&session).Error; err != nil {
		t.Fatal(err)
	}
	if session.ActiveCompanyID == nil || *session.ActiveCompanyID != company.ID {
		t.Fatalf("expected active_company_id=%d, got %+v", company.ID, session.ActiveCompanyID)
	}

	var items []models.ProductService
	if err := db.Where("company_id = ? AND system_code IN (?, ?)", company.ID, "TASK_LABOR", "TASK_REIM").
		Order("system_code asc").
		Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 seeded system items, got %d", len(items))
	}
	for _, item := range items {
		if !item.IsSystem {
			t.Fatalf("expected %s to be marked is_system", item.Name)
		}
		if item.SystemCode == nil || (*item.SystemCode != "TASK_LABOR" && *item.SystemCode != "TASK_REIM") {
			t.Fatalf("unexpected system_code %+v", item.SystemCode)
		}
	}
}
