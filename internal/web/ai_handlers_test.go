package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/ai"
	"balanciz/internal/models"
)

func aiAssistTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:ai_assist_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.User{},
		&models.CompanyMembership{},
		&models.AIConnectionSettings{},
		&models.Customer{},
		&models.Invoice{},
		&models.InvoiceLine{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedAIAssistUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()

	user := &models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("ai_%d@test.com", time.Now().UnixNano()),
		PasswordHash: "x",
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func seedAIAssistInvoice(t *testing.T, db *gorm.DB, companyID uint, number string) *models.Invoice {
	t.Helper()

	customer := models.Customer{CompanyID: companyID, Name: "AI Customer", Email: "ai@example.com"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}

	invoice := &models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         number,
		CustomerID:            customer.ID,
		InvoiceDate:           time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusDraft,
		Amount:                decimal.RequireFromString("125.00"),
		Subtotal:              decimal.RequireFromString("125.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("125.00"),
		BalanceDueBase:        decimal.RequireFromString("125.00"),
		CustomerNameSnapshot:  customer.Name,
		CustomerEmailSnapshot: customer.Email,
	}
	if err := db.Create(invoice).Error; err != nil {
		t.Fatal(err)
	}

	line := models.InvoiceLine{
		CompanyID:   companyID,
		InvoiceID:   invoice.ID,
		Description: "Monthly bookkeeping",
		Qty:         decimal.NewFromInt(1),
		UnitPrice:   decimal.RequireFromString("125.00"),
		LineNet:     decimal.RequireFromString("125.00"),
		LineTax:     decimal.Zero,
		LineTotal:   decimal.RequireFromString("125.00"),
		SortOrder:   1,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	return invoice
}

func aiAssistApp(server *Server, user *models.User, companyID uint) *fiber.App {
	membership := &models.CompanyMembership{UserID: user.ID, CompanyID: companyID, Role: "owner", IsActive: true}
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, user)
		c.Locals(LocalsActiveCompanyID, companyID)
		c.Locals(LocalsCompanyMembership, membership)
		return c.Next()
	})
	app.Post("/api/ai/invoice-memo-assist", server.handleAIMemoAssist)
	app.Post("/api/ai/invoice-email-assist", server.handleAIEmailAssist)
	return app
}

func postJSONRequest(t *testing.T, app *fiber.App, path string, body any) *http.Response {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeAIResponse(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func captureLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return &buf, func() {
		slog.SetDefault(prev)
	}
}

func seedAIAssistEnabledConfig(t *testing.T, db *gorm.DB, companyID uint, apiBaseURL string) {
	t.Helper()

	row := models.AIConnectionSettings{
		CompanyID:  companyID,
		Provider:   models.AIProviderOpenAICompatible,
		APIBaseURL: apiBaseURL,
		APIKey:     "test-key",
		ModelName:  "gpt-test",
		Enabled:    true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

func aiSuggestionServer(t *testing.T, suggestion string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("expected Authorization header, got %q", got)
		}

		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": suggestion},
			}},
		})
	}))
}

func TestAIMemoAssist_CrossCompanyIsolation(t *testing.T) {
	db := aiAssistTestDB(t)
	server := &Server{DB: db, AIAssist: ai.New(db)}

	ownerCompanyID := seedCompany(t, db, "AI Owner Co")
	otherCompanyID := seedCompany(t, db, "AI Other Co")
	invoice := seedAIAssistInvoice(t, db, ownerCompanyID, "INV-AI-MEMO-001")
	user := seedAIAssistUser(t, db)
	if err := db.Create(&models.CompanyMembership{UserID: user.ID, CompanyID: otherCompanyID, Role: "owner", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	app := aiAssistApp(server, user, otherCompanyID)
	resp := postJSONRequest(t, app, "/api/ai/invoice-memo-assist", map[string]any{
		"invoice_id": invoice.ID,
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-company memo assist, got %d", resp.StatusCode)
	}
}

func TestAIEmailAssist_CrossCompanyIsolation(t *testing.T) {
	db := aiAssistTestDB(t)
	server := &Server{DB: db, AIAssist: ai.New(db)}

	ownerCompanyID := seedCompany(t, db, "AI Email Owner Co")
	otherCompanyID := seedCompany(t, db, "AI Email Other Co")
	invoice := seedAIAssistInvoice(t, db, ownerCompanyID, "INV-AI-EMAIL-001")
	user := seedAIAssistUser(t, db)
	if err := db.Create(&models.CompanyMembership{UserID: user.ID, CompanyID: otherCompanyID, Role: "owner", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	app := aiAssistApp(server, user, otherCompanyID)
	resp := postJSONRequest(t, app, "/api/ai/invoice-email-assist", map[string]any{
		"invoice_id": invoice.ID,
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-company email assist, got %d", resp.StatusCode)
	}
}

func TestAIMemoAssist_Success(t *testing.T) {
	db := aiAssistTestDB(t)
	companyID := seedCompany(t, db, "AI Memo Success Co")
	invoice := seedAIAssistInvoice(t, db, companyID, "INV-AI-MEMO-OK-001")
	user := seedAIAssistUser(t, db)

	api := aiSuggestionServer(t, "Friendly memo suggestion")
	defer api.Close()
	seedAIAssistEnabledConfig(t, db, companyID, api.URL)

	server := &Server{DB: db, AIAssist: ai.New(db)}
	app := aiAssistApp(server, user, companyID)
	logs, restoreLogs := captureLogs(t)
	defer restoreLogs()

	resp := postJSONRequest(t, app, "/api/ai/invoice-memo-assist", map[string]any{
		"invoice_id": invoice.ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for memo assist success, got %d", resp.StatusCode)
	}
	body := decodeAIResponse(t, resp)
	if got := body["suggestion"]; got != "Friendly memo suggestion" {
		t.Fatalf("expected suggestion in memo assist response, got %#v", got)
	}
	if !strings.Contains(logs.String(), "ai.memo_assist.succeeded") {
		t.Fatalf("expected memo assist success log, got %q", logs.String())
	}
}

func TestAIMemoAssist_Disabled(t *testing.T) {
	db := aiAssistTestDB(t)
	companyID := seedCompany(t, db, "AI Memo Disabled Co")
	invoice := seedAIAssistInvoice(t, db, companyID, "INV-AI-MEMO-DIS-001")
	user := seedAIAssistUser(t, db)

	server := &Server{DB: db, AIAssist: ai.New(db)}
	app := aiAssistApp(server, user, companyID)
	logs, restoreLogs := captureLogs(t)
	defer restoreLogs()

	resp := postJSONRequest(t, app, "/api/ai/invoice-memo-assist", map[string]any{
		"invoice_id": invoice.ID,
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for memo assist disabled branch, got %d", resp.StatusCode)
	}
	body := decodeAIResponse(t, resp)
	if got := body["error"]; got != "AI is not configured for this company" {
		t.Fatalf("expected disabled memo assist error, got %#v", got)
	}
	if !strings.Contains(logs.String(), "ai.memo_assist.disabled") {
		t.Fatalf("expected memo assist disabled log, got %q", logs.String())
	}
}

func TestAIEmailAssist_Success(t *testing.T) {
	db := aiAssistTestDB(t)
	companyID := seedCompany(t, db, "AI Email Success Co")
	invoice := seedAIAssistInvoice(t, db, companyID, "INV-AI-EMAIL-OK-001")
	user := seedAIAssistUser(t, db)

	api := aiSuggestionServer(t, "Email draft suggestion")
	defer api.Close()
	seedAIAssistEnabledConfig(t, db, companyID, api.URL)

	server := &Server{DB: db, AIAssist: ai.New(db)}
	app := aiAssistApp(server, user, companyID)
	logs, restoreLogs := captureLogs(t)
	defer restoreLogs()

	resp := postJSONRequest(t, app, "/api/ai/invoice-email-assist", map[string]any{
		"invoice_id": invoice.ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for email assist success, got %d", resp.StatusCode)
	}
	body := decodeAIResponse(t, resp)
	if got := body["suggestion"]; got != "Email draft suggestion" {
		t.Fatalf("expected suggestion in email assist response, got %#v", got)
	}
	if !strings.Contains(logs.String(), "ai.email_assist.succeeded") {
		t.Fatalf("expected email assist success log, got %q", logs.String())
	}
}

func TestAIEmailAssist_Disabled(t *testing.T) {
	db := aiAssistTestDB(t)
	companyID := seedCompany(t, db, "AI Email Disabled Co")
	invoice := seedAIAssistInvoice(t, db, companyID, "INV-AI-EMAIL-DIS-001")
	user := seedAIAssistUser(t, db)

	server := &Server{DB: db, AIAssist: ai.New(db)}
	app := aiAssistApp(server, user, companyID)
	logs, restoreLogs := captureLogs(t)
	defer restoreLogs()

	resp := postJSONRequest(t, app, "/api/ai/invoice-email-assist", map[string]any{
		"invoice_id": invoice.ID,
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for email assist disabled branch, got %d", resp.StatusCode)
	}
	body := decodeAIResponse(t, resp)
	if got := body["error"]; got != "AI is not configured for this company" {
		t.Fatalf("expected disabled email assist error, got %#v", got)
	}
	if !strings.Contains(logs.String(), "ai.email_assist.disabled") {
		t.Fatalf("expected email assist disabled log, got %q", logs.String())
	}
}
