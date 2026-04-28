package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

func workspaceHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_workspace_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.ReconciliationException{},
		&models.ReconciliationResolutionAttempt{},
		&models.PaymentReverseException{},
		&models.PaymentReverseResolutionAttempt{},
		&models.PaymentTransaction{},
		&models.PaymentAllocation{},
		&models.PayoutReconciliation{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func workspaceHandlerApp(server *Server, companyID uint) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsActiveCompanyID, companyID)
		return c.Next()
	})
	app.Get("/settings/payment-gateways/investigation", server.handleInvestigationWorkspace)
	return app
}

func seedWorkspaceHandlerReconException(t *testing.T, db *gorm.DB, companyID uint, idx int) *models.ReconciliationException {
	t.Helper()
	ex := &models.ReconciliationException{
		CompanyID:      companyID,
		ExceptionType:  models.ExceptionAmountMismatch,
		Status:         models.ExceptionStatusOpen,
		DedupKey:       fmt.Sprintf("workspace-handler-%d-%03d", companyID, idx),
		Summary:        fmt.Sprintf("workspace row %03d", idx),
		CreatedByActor: "tester@example.com",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatal(err)
	}
	return ex
}

func TestHandleInvestigationWorkspace_ClampsOutOfRangePageToLastPage(t *testing.T) {
	db := workspaceHandlerTestDB(t)
	server := &Server{DB: db}
	app := workspaceHandlerApp(server, 1)

	for i := 1; i <= 51; i++ {
		seedWorkspaceHandlerReconException(t, db, 1, i)
	}

	resp := performRequest(t, app, "/settings/payment-gateways/investigation?page=3", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	if !strings.Contains(body, "Page 2 of 2") {
		t.Fatalf("expected clamped page indicator, got %q", body)
	}
	if strings.Contains(body, "Page 3 of 2") {
		t.Fatalf("expected impossible page indicator to be absent, got %q", body)
	}
	if !strings.Contains(body, "workspace row 001") {
		t.Fatalf("expected last page to show the final real row, got %q", body)
	}
	if !strings.Contains(body, "51 total exception(s) matching filter") {
		t.Fatalf("expected total-count summary in body, got %q", body)
	}
}

// seedWorkspaceHandlerPRException creates a minimal PaymentReverseException for
// handler-level workspace tests.
func seedWorkspaceHandlerPRException(t *testing.T, db *gorm.DB, companyID uint, idx int) *models.PaymentReverseException {
	t.Helper()
	ex := &models.PaymentReverseException{
		CompanyID:      companyID,
		ExceptionType:  models.PRExceptionReverseAllocationAmbiguous,
		Status:         models.PRExceptionStatusOpen,
		DedupKey:       fmt.Sprintf("workspace-handler-pr-%d-%03d", companyID, idx),
		Summary:        fmt.Sprintf("pr workspace row %03d", idx),
		CreatedByActor: "tester@example.com",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatal(err)
	}
	return ex
}

// TestHandleInvestigationWorkspace_CrossDomainAccurateTotal verifies that when
// both exception domains are active the handler reports an accurate SQL-derived
// total, not a capped/in-memory count.
func TestHandleInvestigationWorkspace_CrossDomainAccurateTotal(t *testing.T) {
	db := workspaceHandlerTestDB(t)
	server := &Server{DB: db}
	app := workspaceHandlerApp(server, 1)

	// 51 recon + 10 PR = 61 total — previously the capped cross-domain merge
	// would report len(merged) which is ≤ 2×500, but here we verify the count
	// in the rendered response is the true sum.
	for i := 1; i <= 51; i++ {
		seedWorkspaceHandlerReconException(t, db, 1, i)
	}
	for i := 1; i <= 10; i++ {
		seedWorkspaceHandlerPRException(t, db, 1, i)
	}

	resp := performRequest(t, app, "/settings/payment-gateways/investigation?page=1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	if !strings.Contains(body, "61 total exception(s) matching filter") {
		t.Fatalf("expected accurate cross-domain total 61 in body, got:\n%s", body)
	}
}

// TestHandleInvestigationWorkspace_HasMoreNotPresentOnLastPage verifies that
// the rendered response does not expose a "next page" when the current page is
// the last one (i.e. total rows ≤ pageSize).
func TestHandleInvestigationWorkspace_HasMoreNotPresentOnLastPage(t *testing.T) {
	db := workspaceHandlerTestDB(t)
	server := &Server{DB: db}
	app := workspaceHandlerApp(server, 1)

	// 3 exceptions — single page, HasMore must be false.
	for i := 1; i <= 3; i++ {
		seedWorkspaceHandlerReconException(t, db, 1, i)
	}

	resp := performRequest(t, app, "/settings/payment-gateways/investigation?page=1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	if !strings.Contains(body, "3 total exception(s) matching filter") {
		t.Fatalf("expected total 3 in body, got:\n%s", body)
	}
	// Single page: the pagination nav (Page X of Y) must not appear — the
	// template only renders it when TotalPages > 1.
	if strings.Contains(body, "Page 1 of 2") || strings.Contains(body, "Page 2 of") {
		t.Fatalf("unexpected multi-page indicator for 3-row result: %q", body)
	}
}

func TestHandleInvestigationWorkspace_RendersDismissedBucket(t *testing.T) {
	db := workspaceHandlerTestDB(t)
	server := &Server{DB: db}
	app := workspaceHandlerApp(server, 1)

	for i := 1; i <= 2; i++ {
		ex := seedWorkspaceHandlerReconException(t, db, 1, i)
		if err := db.Model(ex).Update("status", models.ExceptionStatusDismissed).Error; err != nil {
			t.Fatal(err)
		}
	}
	pr := seedWorkspaceHandlerPRException(t, db, 1, 1)
	if err := db.Model(pr).Update("status", models.PRExceptionStatusDismissed).Error; err != nil {
		t.Fatal(err)
	}

	resp := performRequest(t, app, "/settings/payment-gateways/investigation?page=1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	if !strings.Contains(body, ">3</div><div class=\"mt-1 text-small font-medium\">Dismissed</div>") {
		t.Fatalf("expected rendered dismissed bucket count, got:\n%s", body)
	}
}

func TestHandleInvestigationWorkspace_CursorNextLinkContinuesRows(t *testing.T) {
	db := workspaceHandlerTestDB(t)
	server := &Server{DB: db}
	app := workspaceHandlerApp(server, 1)

	for i := 1; i <= 55; i++ {
		seedWorkspaceHandlerReconException(t, db, 1, i)
	}

	resp := performRequest(t, app, "/settings/payment-gateways/investigation?page=1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "workspace row 055") {
		t.Fatalf("expected first page to contain newest row, got:\n%s", body)
	}
	if strings.Contains(body, "workspace row 005") {
		t.Fatalf("first page should not contain second-page row 005, got:\n%s", body)
	}

	m := regexp.MustCompile(`cursor=([^"&]+)`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatalf("expected next link with cursor, got:\n%s", body)
	}
	cursor, err := url.QueryUnescape(m[1])
	if err != nil {
		t.Fatalf("unescape cursor: %v", err)
	}

	resp = performRequest(t, app, fmt.Sprintf("/settings/payment-gateways/investigation?page=2&cursor=%s", url.QueryEscape(cursor)), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body = readResponseBody(t, resp)

	if !strings.Contains(body, "Page 2 of 2") {
		t.Fatalf("expected cursor page indicator, got:\n%s", body)
	}
	if !strings.Contains(body, "55 total exception(s) matching filter") {
		t.Fatalf("expected stable full total on cursor page, got:\n%s", body)
	}
	if strings.Contains(body, "workspace row 055") {
		t.Fatalf("cursor page should not repeat first-page row 055, got:\n%s", body)
	}
	if !strings.Contains(body, "workspace row 005") {
		t.Fatalf("cursor page should contain next-page row 005, got:\n%s", body)
	}
}
