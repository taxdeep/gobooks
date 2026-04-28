package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

func reconciliationDetailTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_recon_detail_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
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
		&models.GatewayPayout{},
		&models.GatewayPayoutComponent{},
		&models.BankEntry{},
		&models.PayoutReconciliation{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func reconciliationDetailApp(server *Server, companyID uint) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsActiveCompanyID, companyID)
		return c.Next()
	})
	app.Get("/settings/payment-gateways/reconciliation-exceptions/:id", server.handleReconciliationExceptionDetail)
	return app
}

func TestHandleReconciliationExceptionDetail_ShowsLinkedSummariesHooksAndAttempts(t *testing.T) {
	db := reconciliationDetailTestDB(t)
	server := &Server{DB: db}
	app := reconciliationDetailApp(server, 1)

	payout := models.GatewayPayout{
		CompanyID:        1,
		GatewayAccountID: 1,
		ProviderPayoutID: "po_test_001",
		PayoutDate:       time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		CurrencyCode:     "CAD",
		GrossAmount:      decimal.RequireFromString("120.00"),
		FeeAmount:        decimal.RequireFromString("10.00"),
		NetAmount:        decimal.RequireFromString("110.00"),
		BankAccountID:    5,
	}
	if err := db.Create(&payout).Error; err != nil {
		t.Fatal(err)
	}

	entry := models.BankEntry{
		CompanyID:     1,
		BankAccountID: 5,
		EntryDate:     time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		Amount:        decimal.RequireFromString("110.00"),
		CurrencyCode:  "CAD",
		Description:   "Stripe deposit",
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}

	ex := models.ReconciliationException{
		CompanyID:       1,
		ExceptionType:   models.ExceptionAmountMismatch,
		Status:          models.ExceptionStatusOpen,
		GatewayPayoutID: &payout.ID,
		BankEntryID:     &entry.ID,
		DedupKey:        "recon-detail-test",
		Summary:         "Match attempt failed: payout net amount does not match bank entry amount",
		CreatedByActor:  "tester@example.com",
	}
	if err := db.Create(&ex).Error; err != nil {
		t.Fatal(err)
	}

	attempt := models.ReconciliationResolutionAttempt{
		CompanyID:                 1,
		ReconciliationExceptionID: ex.ID,
		HookType:                  models.HookTypeRetryMatch,
		Status:                    models.AttemptStatusRejected,
		Summary:                   "Retry match failed",
		Detail:                    "bank entry already used elsewhere",
		Actor:                     "tester@example.com",
	}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}

	resp := performRequest(t, app, fmt.Sprintf("/settings/payment-gateways/reconciliation-exceptions/%d", ex.ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	for _, want := range []string{
		"Gateway Payout",
		"po_test_001",
		"Expected Deposit",
		"110.00",
		"Bank Entry",
		"Stripe deposit",
		"Resolution Actions",
		"Retry Match",
		"Add / Edit Payout Components",
		"Recent Resolution Attempts",
		"Retry match failed",
		"bank entry already used elsewhere",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected response body to contain %q, got %q", want, body)
		}
	}
}

func TestHandleReconciliationExceptionDetail_MissingLinkedObjectsDegradesSafely(t *testing.T) {
	db := reconciliationDetailTestDB(t)
	server := &Server{DB: db}
	app := reconciliationDetailApp(server, 1)

	payoutID := uint(999)
	bankEntryID := uint(888)
	ex := models.ReconciliationException{
		CompanyID:       1,
		ExceptionType:   models.ExceptionUnknownComponentPattern,
		Status:          models.ExceptionStatusReviewed,
		GatewayPayoutID: &payoutID,
		BankEntryID:     &bankEntryID,
		DedupKey:        "recon-detail-missing-linked",
		Summary:         "Linked records were archived before investigation",
		CreatedByActor:  "tester@example.com",
	}
	if err := db.Create(&ex).Error; err != nil {
		t.Fatal(err)
	}

	resp := performRequest(t, app, fmt.Sprintf("/settings/payment-gateways/reconciliation-exceptions/%d", ex.ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	for _, want := range []string{
		"Payout #999",
		"Linked payout record unavailable.",
		"Entry #888",
		"Linked bank entry record unavailable.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected response body to contain %q, got %q", want, body)
		}
	}
}
