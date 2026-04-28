package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

func paymentReverseDetailTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_pr_detail_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.PaymentTransaction{},
		&models.PaymentAllocation{},
		&models.PaymentReverseAllocation{},
		&models.PaymentReverseException{},
		&models.PaymentReverseResolutionAttempt{},
		&models.Invoice{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func paymentReverseDetailApp(server *Server, companyID uint) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsActiveCompanyID, companyID)
		return c.Next()
	})
	app.Get("/settings/payment-gateways/reverse-exceptions/:id", server.handlePaymentReverseExceptionDetail)
	return app
}

func prDetailPtrUint(v uint) *uint { return &v }

func seedPRDetailTxn(t *testing.T, db *gorm.DB, companyID uint, txnType models.PaymentTransactionType, amount string) *models.PaymentTransaction {
	t.Helper()
	txn := &models.PaymentTransaction{
		CompanyID:        companyID,
		GatewayAccountID: 1,
		TransactionType:  txnType,
		Amount:           decimal.RequireFromString(amount),
		CurrencyCode:     "USD",
		Status:           "completed",
		RawPayload:       datatypes.JSON([]byte("{}")),
	}
	if err := db.Create(txn).Error; err != nil {
		t.Fatal(err)
	}
	return txn
}

func seedPRDetailInvoice(t *testing.T, db *gorm.DB, companyID uint, number string) *models.Invoice {
	t.Helper()
	inv := &models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: number,
		CustomerID:    1,
		Amount:        decimal.RequireFromString("100.00"),
		BalanceDue:    decimal.RequireFromString("100.00"),
		Status:        models.InvoiceStatusIssued,
		InvoiceDate:   time.Now().UTC(),
	}
	if err := db.Create(inv).Error; err != nil {
		t.Fatal(err)
	}
	return inv
}

func seedPRDetailForwardAlloc(t *testing.T, db *gorm.DB, companyID, txnID, invoiceID uint, amount string) *models.PaymentAllocation {
	t.Helper()
	alloc := &models.PaymentAllocation{
		CompanyID:            companyID,
		PaymentTransactionID: txnID,
		InvoiceID:            invoiceID,
		AllocatedAmount:      decimal.RequireFromString(amount),
	}
	if err := db.Create(alloc).Error; err != nil {
		t.Fatal(err)
	}
	return alloc
}

func seedPRDetailReverseAlloc(t *testing.T, db *gorm.DB, companyID, revTxnID, origTxnID, payAllocID, invoiceID uint, amount string) *models.PaymentReverseAllocation {
	t.Helper()
	row := &models.PaymentReverseAllocation{
		CompanyID:           companyID,
		ReverseTxnID:        revTxnID,
		OriginalTxnID:       origTxnID,
		PaymentAllocationID: payAllocID,
		InvoiceID:           invoiceID,
		Amount:              decimal.RequireFromString(amount),
		ReverseType:         models.ReverseAllocRefund,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func seedPRDetailException(
	t *testing.T,
	db *gorm.DB,
	companyID uint,
	exType models.PaymentReverseExceptionType,
	reverseTxnID, originalTxnID *uint,
) *models.PaymentReverseException {
	t.Helper()
	ex := &models.PaymentReverseException{
		CompanyID:      companyID,
		ExceptionType:  exType,
		Status:         models.PRExceptionStatusOpen,
		ReverseTxnID:   reverseTxnID,
		OriginalTxnID:  originalTxnID,
		DedupKey:       fmt.Sprintf("web-pr-detail-%d-%d", companyID, time.Now().UnixNano()),
		Summary:        "test reverse exception",
		CreatedByActor: "tester@example.com",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatal(err)
	}
	return ex
}

func TestHandlePaymentReverseExceptionDetail_ShowsRealNavigationLinksAndRollup(t *testing.T) {
	db := paymentReverseDetailTestDB(t)
	server := &Server{DB: db}
	app := paymentReverseDetailApp(server, 1)

	origTxn := seedPRDetailTxn(t, db, 1, models.TxnTypeCharge, "100.00")
	revTxn := seedPRDetailTxn(t, db, 1, models.TxnTypeRefund, "40.00")
	inv1 := seedPRDetailInvoice(t, db, 1, "INV-001")
	inv2 := seedPRDetailInvoice(t, db, 1, "INV-002")
	alloc1 := seedPRDetailForwardAlloc(t, db, 1, origTxn.ID, inv1.ID, "60.00")
	alloc2 := seedPRDetailForwardAlloc(t, db, 1, origTxn.ID, inv2.ID, "40.00")
	seedPRDetailReverseAlloc(t, db, 1, revTxn.ID, origTxn.ID, alloc1.ID, inv1.ID, "24.00")
	seedPRDetailReverseAlloc(t, db, 1, revTxn.ID, origTxn.ID, alloc2.ID, inv2.ID, "16.00")
	ex := seedPRDetailException(t, db, 1, models.PRExceptionOverCreditBoundary, prDetailPtrUint(revTxn.ID), prDetailPtrUint(origTxn.ID))
	if err := db.Create(&models.PaymentReverseResolutionAttempt{
		CompanyID:                 1,
		PaymentReverseExceptionID: ex.ID,
		HookType:                  models.PRHookRetryCheck,
		Status:                    models.PRAttemptRejected,
		Summary:                   "attempt rejected for test",
		Actor:                     "tester@example.com",
		CreatedAt:                 time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed reverse attempt: %v", err)
	}

	resp := performRequest(t, app, fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d", ex.ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	for _, want := range []string{
		"Allocation Summary",
		"Recommended Next Step",
		"Resolution Hooks",
		"Recent Resolution Attempts",
		"attempt rejected for test",
		fmt.Sprintf("/settings/payment-gateways/transactions#txn-%d", revTxn.ID),
		fmt.Sprintf("/settings/payment-gateways/transactions#txn-%d", origTxn.ID),
		fmt.Sprintf("/invoices/%d", inv1.ID),
		fmt.Sprintf("/invoices/%d", inv2.ID),
		"INV-001",
		"INV-002",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected response body to contain %q, got %q", want, body)
		}
	}
}

func TestHandlePaymentReverseExceptionDetail_MissingInvoiceRecordDegradesSafely(t *testing.T) {
	db := paymentReverseDetailTestDB(t)
	server := &Server{DB: db}
	app := paymentReverseDetailApp(server, 1)

	origTxn := seedPRDetailTxn(t, db, 1, models.TxnTypeCharge, "75.00")
	revTxn := seedPRDetailTxn(t, db, 1, models.TxnTypeRefund, "20.00")
	missingInvoiceID := uint(999)
	seedPRDetailForwardAlloc(t, db, 1, origTxn.ID, missingInvoiceID, "75.00")
	ex := seedPRDetailException(t, db, 1, models.PRExceptionRequiresManualSplit, prDetailPtrUint(revTxn.ID), prDetailPtrUint(origTxn.ID))

	resp := performRequest(t, app, fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d", ex.ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	if !strings.Contains(body, "#999") {
		t.Fatalf("expected missing invoice fallback in body, got %q", body)
	}
	if strings.Contains(body, "/invoices/999") {
		t.Fatalf("expected missing invoice to render without clickable link, got %q", body)
	}
}

// ── Batch 27: Handler-level POST integration tests ────────────────────────────
//
// These tests exercise the full handler path — routing, parameter parsing,
// service call, error translation, and redirect feedback — using an in-process
// Fiber app with an in-memory SQLite DB.
//
// Coverage:
//   D1. Hook POST happy path → redirect with hook_actioned=1
//   D2. Duplicate hook POST → redirect with hook_error and rejected attempt
//   D3. Navigation hook POST → silently redirects to detail (no error)
//   D4. Hook POST on terminal exception → redirect with hook_error
//   D5. Review POST happy path → redirect with actioned=1
//   D6. Review POST on already-terminal → redirect with error
//   D7. Dismiss POST with note → redirect with actioned=1
//   D8. Dismiss POST without note → redirect with error
//   D9. Resolve POST happy path → redirect with actioned=1
//   D10. Resolve POST on already-resolved → redirect with error

func paymentReversePostApp(server *Server, companyID uint) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsActiveCompanyID, companyID)
		return c.Next()
	})
	app.Get("/settings/payment-gateways/reverse-exceptions/:id", server.handlePaymentReverseExceptionDetail)
	app.Post("/settings/payment-gateways/reverse-exceptions/:id/hooks/:hookType", server.handlePaymentReverseExceptionHook)
	app.Post("/settings/payment-gateways/reverse-exceptions/:id/review", server.handlePaymentReverseExceptionReview)
	app.Post("/settings/payment-gateways/reverse-exceptions/:id/dismiss", server.handlePaymentReverseExceptionDismiss)
	app.Post("/settings/payment-gateways/reverse-exceptions/:id/resolve", server.handlePaymentReverseExceptionResolve)
	return app
}

func prPostTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:web_pr_post_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.PaymentTransaction{},
		&models.PaymentAllocation{},
		&models.PaymentReverseAllocation{},
		&models.PaymentReverseException{},
		&models.PaymentReverseResolutionAttempt{},
		&models.Invoice{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedEligibleHookScenario builds a scenario where retry_safe_reverse_check is
// available and the validator (ValidateRefundReverseAllocatable) will pass.
func seedEligibleHookScenario(t *testing.T, db *gorm.DB, companyID uint) *models.PaymentReverseException {
	t.Helper()
	orig := &models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: 1,
		TransactionType: models.TxnTypeCharge,
		Amount:          decimal.RequireFromString("1000.00"),
		CurrencyCode:    "USD", Status: "completed",
		RawPayload: datatypes.JSON([]byte("{}")),
	}
	if err := db.Create(orig).Error; err != nil {
		t.Fatal(err)
	}
	db.Create(&models.PaymentAllocation{
		CompanyID: companyID, PaymentTransactionID: orig.ID,
		InvoiceID: 1, AllocatedAmount: decimal.RequireFromString("1000.00"),
	})

	jeID := uint(99)
	rev := &models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: 1,
		TransactionType: models.TxnTypeRefund,
		Amount:          decimal.RequireFromString("500.00"),
		CurrencyCode:    "USD", Status: "completed",
		RawPayload:           datatypes.JSON([]byte("{}")),
		PostedJournalEntryID: &jeID,
	}
	if err := db.Create(rev).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(rev).Update("original_transaction_id", orig.ID)

	ex := &models.PaymentReverseException{
		CompanyID: companyID, ExceptionType: models.PRExceptionAmountExceedsStrategy,
		Status:       models.PRExceptionStatusOpen,
		ReverseTxnID: &rev.ID, OriginalTxnID: &orig.ID,
		DedupKey:       fmt.Sprintf("handler-post-test-%d", time.Now().UnixNano()),
		Summary:        "hook handler test exception",
		CreatedByActor: "system",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatal(err)
	}
	return ex
}

// D1. Hook POST happy path
func TestHandlePRHookPost_HappyPathRedirectsWithActioned(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	ex := seedEligibleHookScenario(t, db, 1)
	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/hooks/retry_safe_reverse_check", ex.ID)

	resp := performFormRequest(t, app, http.MethodPost, path, nil, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "hook_actioned=1") {
		t.Errorf("want hook_actioned=1 in redirect, got %q", loc)
	}

	// Verify attempt was recorded.
	var count int64
	db.Model(&models.PaymentReverseResolutionAttempt{}).
		Where("payment_reverse_exception_id = ? AND company_id = ?", ex.ID, 1).
		Count(&count)
	if count == 0 {
		t.Error("want at least 1 attempt recorded after hook POST")
	}
}

// D2. Duplicate hook POST redirects with error and records a rejected attempt.
func TestHandlePRHookPost_DuplicateRedirectsWithErrorAndRecordsRejected(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	ex := seedEligibleHookScenario(t, db, 1)
	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/hooks/retry_safe_reverse_check", ex.ID)

	first := performFormRequest(t, app, http.MethodPost, path, nil, "")
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("first POST: want 303 redirect, got %d", first.StatusCode)
	}
	if loc := first.Header.Get("Location"); !strings.Contains(loc, "hook_actioned=1") {
		t.Fatalf("first POST: want hook_actioned=1, got %q", loc)
	}

	second := performFormRequest(t, app, http.MethodPost, path, nil, "")
	if second.StatusCode != http.StatusSeeOther {
		t.Fatalf("second POST: want 303 redirect, got %d", second.StatusCode)
	}
	if loc := second.Header.Get("Location"); !strings.Contains(loc, "hook_error") {
		t.Fatalf("second POST: want hook_error duplicate feedback, got %q", loc)
	}

	var attempts []models.PaymentReverseResolutionAttempt
	if err := db.Where("company_id = ? AND payment_reverse_exception_id = ?", 1, ex.ID).
		Order("id ASC").
		Find(&attempts).Error; err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	var succeeded, rejected int
	for _, attempt := range attempts {
		switch attempt.Status {
		case models.PRAttemptSucceeded:
			succeeded++
		case models.PRAttemptRejected:
			rejected++
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("want one succeeded and one rejected attempt, got succeeded=%d rejected=%d total=%d",
			succeeded, rejected, len(attempts))
	}
}

// D3. Navigation hook POST redirects to detail without error.
func TestHandlePRHookPost_NavigationHookRedirectsToDetailSilently(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	ex := seedEligibleHookScenario(t, db, 1)
	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/hooks/open_reverse_transaction", ex.ID)

	resp := performFormRequest(t, app, http.MethodPost, path, nil, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "hook_error") {
		t.Errorf("want no hook_error for navigation hook redirect, got %q", loc)
	}
	if strings.Contains(loc, "hook_actioned") {
		t.Errorf("want no hook_actioned for navigation hook redirect, got %q", loc)
	}
	// Must redirect to the detail page base path.
	expected := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d", ex.ID)
	if !strings.Contains(loc, expected) {
		t.Errorf("want redirect to detail page %q, got %q", expected, loc)
	}
}

// D3. Hook POST on terminal exception → redirect with hook_error.
func TestHandlePRHookPost_TerminalExceptionRedirectsWithError(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	// Create a dismissed (terminal) exception.
	ex := &models.PaymentReverseException{
		CompanyID: 1, ExceptionType: models.PRExceptionAmountExceedsStrategy,
		Status:         models.PRExceptionStatusDismissed,
		DedupKey:       fmt.Sprintf("handler-terminal-%d", time.Now().UnixNano()),
		Summary:        "terminal exception",
		CreatedByActor: "system",
	}
	db.Create(ex)

	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/hooks/retry_safe_reverse_check", ex.ID)
	resp := performFormRequest(t, app, http.MethodPost, path, nil, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "hook_error") {
		t.Errorf("want hook_error in redirect for terminal exception, got %q", loc)
	}
}

// D4. Review POST happy path → redirect with actioned=1.
func TestHandlePRReview_HappyPathRedirectsWithActioned(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	ex := seedEligibleHookScenario(t, db, 1)
	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/review", ex.ID)

	resp := performFormRequest(t, app, http.MethodPost, path, nil, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "actioned=1") {
		t.Errorf("want actioned=1 in redirect, got %q", loc)
	}

	// Verify status changed in DB.
	var updated models.PaymentReverseException
	db.Where("id = ? AND company_id = ?", ex.ID, 1).First(&updated)
	if updated.Status != models.PRExceptionStatusReviewed {
		t.Errorf("want status=reviewed in DB, got %s", updated.Status)
	}
}

// D5. Review POST on already-terminal → redirect with error.
func TestHandlePRReview_TerminalExceptionRedirectsWithError(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	// Seed as resolved (terminal).
	ex := &models.PaymentReverseException{
		CompanyID: 1, ExceptionType: models.PRExceptionAmountExceedsStrategy,
		Status:         models.PRExceptionStatusResolved,
		DedupKey:       fmt.Sprintf("handler-resolved-%d", time.Now().UnixNano()),
		Summary:        "resolved exception",
		CreatedByActor: "system",
	}
	db.Create(ex)

	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/review", ex.ID)
	resp := performFormRequest(t, app, http.MethodPost, path, nil, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("want error query param in redirect for terminal review, got %q", loc)
	}
}

// D6. Dismiss POST with note → redirect with actioned=1.
func TestHandlePRDismiss_WithNoteRedirectsWithActioned(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	ex := seedEligibleHookScenario(t, db, 1)
	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/dismiss", ex.ID)

	form := url.Values{"note": {"This exception is being dismissed as expected."}}
	resp := performFormRequest(t, app, http.MethodPost, path, form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "actioned=1") {
		t.Errorf("want actioned=1 in redirect, got %q", loc)
	}

	var updated models.PaymentReverseException
	db.Where("id = ? AND company_id = ?", ex.ID, 1).First(&updated)
	if updated.Status != models.PRExceptionStatusDismissed {
		t.Errorf("want status=dismissed in DB, got %s", updated.Status)
	}
}

// D7. Dismiss POST without note → redirect with error (note is required).
func TestHandlePRDismiss_WithoutNoteRedirectsWithError(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	ex := seedEligibleHookScenario(t, db, 1)
	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/dismiss", ex.ID)

	// POST without note field.
	resp := performFormRequest(t, app, http.MethodPost, path, nil, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("want error query param in redirect for missing note, got %q", loc)
	}

	// Status must not change.
	var unchanged models.PaymentReverseException
	db.Where("id = ? AND company_id = ?", ex.ID, 1).First(&unchanged)
	if unchanged.Status != models.PRExceptionStatusOpen {
		t.Errorf("want status still open after failed dismiss, got %s", unchanged.Status)
	}
}

// D8. Resolve POST happy path → redirect with actioned=1.
func TestHandlePRResolve_WithNoteRedirectsWithActioned(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	ex := seedEligibleHookScenario(t, db, 1)
	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/resolve", ex.ID)

	form := url.Values{"note": {"Resolved after manual review."}}
	resp := performFormRequest(t, app, http.MethodPost, path, form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "actioned=1") {
		t.Errorf("want actioned=1 in redirect, got %q", loc)
	}

	var updated models.PaymentReverseException
	db.Where("id = ? AND company_id = ?", ex.ID, 1).First(&updated)
	if updated.Status != models.PRExceptionStatusResolved {
		t.Errorf("want status=resolved in DB, got %s", updated.Status)
	}
}

// D9. Resolve POST on already-resolved → redirect with error (terminal guard).
func TestHandlePRResolve_AlreadyResolvedRedirectsWithError(t *testing.T) {
	db := prPostTestDB(t)
	server := &Server{DB: db}
	app := paymentReversePostApp(server, 1)

	ex := &models.PaymentReverseException{
		CompanyID: 1, ExceptionType: models.PRExceptionAmountExceedsStrategy,
		Status:         models.PRExceptionStatusResolved,
		DedupKey:       fmt.Sprintf("handler-resolve-terminal-%d", time.Now().UnixNano()),
		Summary:        "already resolved",
		CreatedByActor: "system",
	}
	db.Create(ex)

	path := fmt.Sprintf("/settings/payment-gateways/reverse-exceptions/%d/resolve", ex.ID)
	form := url.Values{"note": {"Attempt to re-resolve."}}
	resp := performFormRequest(t, app, http.MethodPost, path, form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("want error query param for double-resolve, got %q", loc)
	}
}
