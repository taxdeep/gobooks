package web

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"gobooks/internal/config"
	"gobooks/internal/models"
)

func testSecurityDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_security_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.Company{},
		&models.Session{},
		&models.CompanyMembership{},
		&models.CompanySecuritySettings{},
		&models.SystemSecuritySettings{},
		&models.SecurityEvent{},
		&models.SysadminUser{},
		&models.SysadminSession{},
		&models.SystemSetting{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func testSecurityApp(t *testing.T, db *gorm.DB) *fiber.App {
	t.Helper()
	return NewServer(config.Config{
		Env:  "test",
		Addr: ":0",
	}, db)
}

func seedBusinessSessionWithRole(t *testing.T, db *gorm.DB, role models.CompanyRole, active bool) (string, uint) {
	t.Helper()

	suffix := uuid.NewString()
	company := models.Company{
		Name:                    fmt.Sprintf("%s Co %s", t.Name(), suffix),
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
		IsActive:                true,
	}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	if !active {
		if err := db.Model(&company).Update("is_active", false).Error; err != nil {
			t.Fatal(err)
		}
		company.IsActive = false
	}

	user := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("%s-%s@example.com", t.Name(), suffix),
		PasswordHash: "not-used",
		DisplayName:  "Security Test",
		IsActive:     true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	membership := models.CompanyMembership{
		ID:        uuid.New(),
		UserID:    user.ID,
		CompanyID: company.ID,
		Role:      role,
		IsActive:  true,
	}
	if err := db.Create(&membership).Error; err != nil {
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
		ActiveCompanyID: &company.ID,
		ExpiresAt:       time.Now().UTC().Add(24 * time.Hour),
		CreatedAt:       time.Now().UTC(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	return rawToken, company.ID
}

func seedBusinessLoginUser(t *testing.T, db *gorm.DB, role models.CompanyRole, email, password string) uint {
	t.Helper()

	company := models.Company{
		Name:                    fmt.Sprintf("%s Login Co %s", t.Name(), uuid.NewString()),
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
		IsActive:                true,
	}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	user := models.User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: string(hash),
		DisplayName:  "Login Test",
		IsActive:     true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	membership := models.CompanyMembership{
		ID:        uuid.New(),
		UserID:    user.ID,
		CompanyID: company.ID,
		Role:      role,
		IsActive:  true,
	}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatal(err)
	}

	return company.ID
}

func seedSysadminLoginUser(t *testing.T, db *gorm.DB, email, password string) uint {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	user := models.SysadminUser{
		Email:        email,
		PasswordHash: string(hash),
		IsActive:     true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func newCSRFToken(t *testing.T) string {
	t.Helper()

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func performSecurityRequest(t *testing.T, app *fiber.App, method string, target string, body []byte, contentType string, cookies ...*http.Cookie) *http.Response {
	t.Helper()

	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		req.AddCookie(cookie)
	}

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestViewerNonGetRequestsAreForbidden(t *testing.T) {
	db := testSecurityDB(t)
	app := testSecurityApp(t, db)
	rawToken, _ := seedBusinessSessionWithRole(t, db, models.CompanyRoleViewer, true)
	csrf := newCSRFToken(t)

	form := url.Values{}
	form.Set("query", "office supplies")
	form.Set(CSRFFormField, csrf)

	resp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/accounts/suggestions",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, resp.StatusCode)
	}
}

func TestInactiveCompanyBlocksWritesBeforeHandler(t *testing.T) {
	db := testSecurityDB(t)
	app := testSecurityApp(t, db)
	rawToken, _ := seedBusinessSessionWithRole(t, db, models.CompanyRoleAdmin, false)
	csrf := newCSRFToken(t)

	form := url.Values{}
	form.Set("query", "office supplies")
	form.Set(CSRFFormField, csrf)

	resp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/accounts/suggestions",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, resp.StatusCode)
	}
}

func TestMaintenanceModeBlocksBusinessRoutesButNotAdminLogin(t *testing.T) {
	db := testSecurityDB(t)
	if err := db.Create(&models.SystemSetting{
		Key:       "maintenance_mode",
		Value:     "true",
		UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	app := testSecurityApp(t, db)

	businessResp := performSecurityRequest(t, app, http.MethodGet, "/login", nil, "", nil)
	if businessResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d", http.StatusServiceUnavailable, businessResp.StatusCode)
	}

	adminResp := performSecurityRequest(t, app, http.MethodGet, "/admin/login", nil, "", nil)
	if adminResp.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("expected admin login to bypass maintenance mode")
	}
}

func TestBusinessSessionCannotAccessSysadminRoutes(t *testing.T) {
	db := testSecurityDB(t)
	app := testSecurityApp(t, db)
	rawToken, _ := seedBusinessSessionWithRole(t, db, models.CompanyRoleAdmin, true)

	resp := performSecurityRequest(
		t,
		app,
		http.MethodGet,
		"/admin/dashboard",
		nil,
		"",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
	)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/admin/login" {
		t.Fatalf("expected redirect to /admin/login, got %q", got)
	}
}

func TestCSRFMiddlewareRejectsMissingTokenAndAllowsMatchingToken(t *testing.T) {
	db := testSecurityDB(t)
	app := testSecurityApp(t, db)
	rawToken, _ := seedBusinessSessionWithRole(t, db, models.CompanyRoleAdmin, true)

	missingTokenResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/logout",
		nil,
		"",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
	)
	if missingTokenResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, missingTokenResp.StatusCode)
	}

	rawToken2, _ := seedBusinessSessionWithRole(t, db, models.CompanyRoleAdmin, true)
	csrf := newCSRFToken(t)
	form := url.Values{}
	form.Set(CSRFFormField, csrf)

	matchingTokenResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/logout",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken2, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if matchingTokenResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, matchingTokenResp.StatusCode)
	}
	if got := matchingTokenResp.Header.Get("Location"); got != "/login" {
		t.Fatalf("expected redirect to /login, got %q", got)
	}
}

func TestAPRoleCannotAccessReceivePaymentForm(t *testing.T) {
	db := testSecurityDB(t)
	app := testSecurityApp(t, db)
	rawToken, _ := seedBusinessSessionWithRole(t, db, models.CompanyRoleAP, true)

	resp := performSecurityRequest(
		t,
		app,
		http.MethodGet,
		"/banking/receive-payment",
		nil,
		"",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
	)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, resp.StatusCode)
	}
}

func TestViewerCannotAccessBillCreationForm(t *testing.T) {
	db := testSecurityDB(t)
	app := testSecurityApp(t, db)
	rawToken, _ := seedBusinessSessionWithRole(t, db, models.CompanyRoleViewer, true)

	resp := performSecurityRequest(
		t,
		app,
		http.MethodGet,
		"/bills/new",
		nil,
		"",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
	)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, resp.StatusCode)
	}
}

func TestAPRoleCannotRunReconcileAutoMatch(t *testing.T) {
	db := testSecurityDB(t)
	app := testSecurityApp(t, db)
	rawToken, _ := seedBusinessSessionWithRole(t, db, models.CompanyRoleAP, true)
	csrf := newCSRFToken(t)

	form := url.Values{}
	form.Set("account_id", "1")
	form.Set("statement_date", "2026-03-30")
	form.Set("ending_balance", "0.00")
	form.Set(CSRFFormField, csrf)

	resp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/banking/reconcile/auto-match",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, resp.StatusCode)
	}
}

func TestBusinessLoginWritesSecurityEvent(t *testing.T) {
	db := testSecurityDB(t)
	companyID := seedBusinessLoginUser(t, db, models.CompanyRoleBookkeeper, "bookkeeper@example.com", "supersecret")
	app := testSecurityApp(t, db)
	csrf := newCSRFToken(t)

	form := url.Values{}
	form.Set("email", "bookkeeper@example.com")
	form.Set("password", "supersecret")
	form.Set(CSRFFormField, csrf)

	resp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/login",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}

	var events []models.SecurityEvent
	if err := db.Order("id asc").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 security event, got %d", len(events))
	}
	if events[0].EventType != "login.success" {
		t.Fatalf("expected login.success event, got %s", events[0].EventType)
	}
	if events[0].CompanyID == nil || *events[0].CompanyID != companyID {
		t.Fatalf("expected company_id=%d, got %+v", companyID, events[0].CompanyID)
	}
}

func TestAdminLoginWritesSecurityEvent(t *testing.T) {
	db := testSecurityDB(t)
	adminID := seedSysadminLoginUser(t, db, "admin@example.com", "supersecret")
	app := testSecurityApp(t, db)
	csrf := newCSRFToken(t)

	form := url.Values{}
	form.Set("email", "admin@example.com")
	form.Set("password", "supersecret")
	form.Set(CSRFFormField, csrf)

	resp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/admin/login",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}

	var events []models.SecurityEvent
	if err := db.Order("id asc").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 security event, got %d", len(events))
	}
	if events[0].EventType != "login.success" {
		t.Fatalf("expected login.success event, got %s", events[0].EventType)
	}
	if events[0].CompanyID != nil {
		t.Fatalf("expected admin login event without company, got %+v", events[0].CompanyID)
	}
	if events[0].UserID == nil || *events[0].UserID != fmt.Sprintf("%d", adminID) {
		t.Fatalf("expected user_id=%d, got %+v", adminID, events[0].UserID)
	}
}

func TestLoginPostsRequireCSRFTokens(t *testing.T) {
	db := testSecurityDB(t)
	app := testSecurityApp(t, db)

	businessForm := url.Values{}
	businessForm.Set("email", "bookkeeper@example.com")
	businessForm.Set("password", "supersecret")

	businessResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/login",
		[]byte(businessForm.Encode()),
		"application/x-www-form-urlencoded",
	)
	if businessResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, businessResp.StatusCode)
	}

	adminForm := url.Values{}
	adminForm.Set("email", "admin@example.com")
	adminForm.Set("password", "supersecret")

	adminResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/admin/login",
		[]byte(adminForm.Encode()),
		"application/x-www-form-urlencoded",
	)
	if adminResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, adminResp.StatusCode)
	}
}
