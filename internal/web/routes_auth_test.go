package web

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"gobooks/internal/config"
	"gobooks/internal/models"
	"gobooks/internal/services"
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
		&models.Expense{},
		&models.TaskInvoiceSource{},
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
