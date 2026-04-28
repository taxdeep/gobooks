// 遵循project_guide.md
package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testInvoicePaymentDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:invpay_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Customer{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.InvoiceHostedLink{},
		&models.PaymentGatewayAccount{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
		&models.HostedPaymentAttempt{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

type invPaySetup struct {
	companyID  uint
	customerID uint
	invoiceID  uint
	gatewayID  uint
}

func setupInvPay(t *testing.T, db *gorm.DB) invPaySetup {
	t.Helper()
	co := models.Company{Name: "InvPay Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Cust", AddrStreet1: "1 St"}
	db.Create(&cust)

	inv := models.Invoice{
		CompanyID: co.ID, InvoiceNumber: "INV-PAY-1",
		CustomerID: cust.ID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
		CurrencyCode: "CAD", CustomerNameSnapshot: "Cust",
	}
	db.Create(&inv)

	gw := models.PaymentGatewayAccount{
		CompanyID: co.ID, ProviderType: models.ProviderStripe,
		DisplayName: "Stripe Prod", AuthStatus: "connected", IsActive: true,
	}
	db.Create(&gw)

	return invPaySetup{companyID: co.ID, customerID: cust.ID, invoiceID: inv.ID, gatewayID: gw.ID}
}

// ── Create payment request from invoice ──────────────────────────────────────

func TestCreatePaymentRequestForInvoice_OK(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	req, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err != nil {
		t.Fatalf("CreatePaymentRequestForInvoice: %v", err)
	}
	if req.InvoiceID == nil || *req.InvoiceID != s.invoiceID {
		t.Error("Invoice ID not linked")
	}
	if !req.Amount.Equal(decimal.NewFromInt(500)) {
		t.Errorf("Expected amount 500 (from BalanceDue), got %s", req.Amount)
	}
	if req.CurrencyCode != "CAD" {
		t.Errorf("Expected currency CAD, got %s", req.CurrencyCode)
	}
	if req.Status != models.PaymentRequestPending {
		t.Errorf("Expected status pending, got %s", req.Status)
	}

	var saved models.PaymentRequest
	if err := db.First(&saved, req.ID).Error; err != nil {
		t.Fatalf("reload request: %v", err)
	}
	if saved.Status != models.PaymentRequestPending {
		t.Errorf("Expected persisted status pending, got %s", saved.Status)
	}
}

func TestCreatePaymentRequestForInvoice_DefaultAmountFromBalanceDue(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	// Update balance due to 300 (simulating partial payment).
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Update("balance_due", "300")

	req, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !req.Amount.Equal(decimal.NewFromInt(300)) {
		t.Errorf("Expected amount 300 (from updated BalanceDue), got %s", req.Amount)
	}
}

func TestCreatePaymentRequestForInvoice_DuplicateBlocked(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	// First request.
	CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})

	// Second should fail.
	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err == nil {
		t.Fatal("Expected duplicate error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Expected 'already exists' error, got: %v", err)
	}
}

func TestCreatePaymentRequestForInvoice_CrossCompanyBlocked(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: otherCo.ID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err == nil {
		t.Fatal("Expected cross-company error")
	}
}

func TestCreatePaymentRequestForInvoice_WrongGatewayBlocked(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)
	otherGW := models.PaymentGatewayAccount{CompanyID: otherCo.ID, ProviderType: models.ProviderPayPal, DisplayName: "PP", AuthStatus: "ok", IsActive: true}
	db.Create(&otherGW)

	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: otherGW.ID,
	})
	if err == nil {
		t.Fatal("Expected wrong gateway error")
	}
}

func TestCreatePaymentRequest_DoesNotChangeInvoiceStatus(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})

	// Invoice should still be issued, not paid.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("Expected invoice status issued, got %s — payment request should NOT change invoice status", inv.Status)
	}
}

// ── Invoice status guards ────────────────────────────────────────────────────

func TestCreatePaymentRequest_PaidInvoice_Blocked(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	// Mark invoice as paid.
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Update("status", string(models.InvoiceStatusPaid))

	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err == nil {
		t.Fatal("Expected error for paid invoice")
	}
	if !strings.Contains(err.Error(), "payable") {
		t.Errorf("Expected payable-state error, got: %v", err)
	}
}

func TestCreatePaymentRequest_VoidedInvoice_Blocked(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Update("status", string(models.InvoiceStatusVoided))

	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err == nil {
		t.Fatal("Expected error for voided invoice")
	}
}

func TestCreatePaymentRequest_DraftInvoice_Blocked(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Update("status", string(models.InvoiceStatusDraft))

	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err == nil {
		t.Fatal("Expected error for draft invoice")
	}
}

func TestIsInvoicePayable(t *testing.T) {
	payable := []models.InvoiceStatus{
		models.InvoiceStatusIssued, models.InvoiceStatusSent,
		models.InvoiceStatusOverdue, models.InvoiceStatusPartiallyPaid,
	}
	for _, s := range payable {
		if !IsInvoicePayable(s) {
			t.Errorf("%s should be payable", s)
		}
	}

	notPayable := []models.InvoiceStatus{
		models.InvoiceStatusDraft, models.InvoiceStatusPaid, models.InvoiceStatusVoided,
	}
	for _, s := range notPayable {
		if IsInvoicePayable(s) {
			t.Errorf("%s should NOT be payable", s)
		}
	}
}

// ── External txn ref duplicate guard ─────────────────────────────────────────

func TestValidateExternalTxnRefUnique_EmptyAllowed(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	err := ValidateExternalTxnRefUnique(db, s.companyID, s.gatewayID, "")
	if err != nil {
		t.Error("Empty ref should always be allowed")
	}
}

func TestValidateExternalTxnRefUnique_DuplicateBlocked(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	CreatePaymentTransaction(db, &models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gatewayID,
		TransactionType: models.TxnTypeCharge, Amount: decimal.NewFromInt(100),
		ExternalTxnRef: "ch_unique_123", RawPayload: datatypes.JSON("{}"),
	})

	err := ValidateExternalTxnRefUnique(db, s.companyID, s.gatewayID, "ch_unique_123")
	if err == nil {
		t.Fatal("Expected duplicate ref error")
	}
}

// ── List payment requests for invoice ────────────────────────────────────────

func TestListPaymentRequestsForInvoice(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})

	reqs, err := ListPaymentRequestsForInvoice(db, s.companyID, s.invoiceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("Expected 1 request, got %d", len(reqs))
	}
	if reqs[0].GatewayAccount.DisplayName != "Stripe Prod" {
		t.Error("Gateway account not preloaded")
	}
}

// ── Blocker 2: sequential partial collection ─────────────────────────────────

// TestCreatePaymentRequest_AfterPartialPaymentApplied_SecondRequestAllowed verifies
// that after a partial payment is applied (invoice → partially_paid), the first
// PaymentRequest is transitioned to paid (consumed), and a second request for the
// remaining balance can be created without hitting the duplicate-active-request guard.
func TestCreatePaymentRequest_AfterPartialPaymentApplied_SecondRequestAllowed(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db) // invoice.Amount = 500

	// First request for 500.
	req1, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err != nil {
		t.Fatalf("first request: %v", err)
	}

	// Simulate partial payment applied: invoice → partially_paid (balance=300).
	// We directly update the invoice and PaymentRequest to simulate what
	// ApplyPaymentTransactionToInvoice does (marking req as paid).
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Updates(map[string]any{
		"status": string(models.InvoiceStatusPartiallyPaid), "balance_due": "300",
	})
	db.Model(&models.PaymentRequest{}).Where("id = ?", req1.ID).Update("status", models.PaymentRequestPaid)

	// Second request for remaining 300 — must NOT be blocked.
	req2, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err != nil {
		t.Fatalf("second request after partial payment: %v", err)
	}
	if !req2.Amount.Equal(decimal.NewFromInt(300)) {
		t.Errorf("second request amount: want 300, got %s", req2.Amount)
	}
}

// TestCreatePaymentRequest_AfterTwoAppliedPartials_ThirdRequestAllowed verifies
// that previously CONSUMED partial collections do not block the final request.
//
// Scenario: invoice.Amount=500, two prior provider-confirmed collections of 200
// each have already been applied (BalanceDue=100). A third request for the final
// 100 must still be allowed. This guards against incorrectly comparing TOTAL
// succeeded collections (400) to the current BalanceDue (100).
func TestCreatePaymentRequest_AfterTwoAppliedPartials_ThirdRequestAllowed(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db) // invoice.Amount = 500

	// Simulate two prior applied partial payments: invoice now has 100 remaining.
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Updates(map[string]any{
		"status":      string(models.InvoiceStatusPartiallyPaid),
		"balance_due": "100",
	})

	// Provider-confirmed collections for the two already-applied partials.
	for i := 0; i < 2; i++ {
		db.Create(&models.HostedPaymentAttempt{
			CompanyID:        s.companyID,
			InvoiceID:        s.invoiceID,
			HostedLinkID:     1,
			GatewayAccountID: s.gatewayID,
			ProviderType:     models.ProviderStripe,
			Amount:           decimal.NewFromInt(200),
			CurrencyCode:     "CAD",
			Status:           models.HostedPaymentAttemptPaymentSucceeded,
		})
	}

	req, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err != nil {
		t.Fatalf("third request after two applied partials: %v", err)
	}
	if !req.Amount.Equal(decimal.NewFromInt(100)) {
		t.Errorf("third request amount: want 100, got %s", req.Amount)
	}
}

// TestCreatePaymentRequest_Amount_SubtractsUnconsumedCollection verifies that
// when a payment_succeeded attempt exists but hasn't been applied yet, the new
// request amount is BalanceDue - sum(unapplied verified), not raw BalanceDue.
func TestCreatePaymentRequest_Amount_SubtractsUnconsumedCollection(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db) // invoice.Amount=500, balance=500

	// Simulate a $200 payment_succeeded attempt (not yet applied).
	db.Create(&models.HostedPaymentAttempt{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, HostedLinkID: 1,
		GatewayAccountID: s.gatewayID, ProviderType: models.ProviderStripe,
		Amount: decimal.NewFromInt(200), CurrencyCode: "CAD",
		Status: models.HostedPaymentAttemptPaymentSucceeded,
	})

	// Invoice still shows balance=500 (not yet applied).
	req, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err != nil {
		t.Fatalf("create request with unconsumed collection: %v", err)
	}
	// Amount should be 500 - 200 = 300, not 500.
	if !req.Amount.Equal(decimal.NewFromInt(300)) {
		t.Errorf("request amount: want 300 (balance minus unapplied), got %s", req.Amount)
	}
}

// TestCreatePaymentRequest_DuplicateGuard_StillBlocksTrueDuplicates verifies
// that loosening the partial-payment guard does NOT allow true duplicates
// (two requests when no payment has been made or consumed).
func TestCreatePaymentRequest_DuplicateGuard_StillBlocksTrueDuplicates(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	// First request — no payment made.
	CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})

	// Second request — first is still active (not consumed). Must be blocked.
	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err == nil {
		t.Fatal("expected duplicate-active-request block, got nil")
	}
}

// ── Batch 10.1: verified-collection block in CreatePaymentRequestForInvoice ──

// TestCreatePaymentRequestForInvoice_BlocksAfterVerifiedCollection verifies that
// creating a payment request fails with ErrVerifiedGatewayCollectionExists when a
// payment_succeeded HostedPaymentAttempt already exists for the invoice.
func TestCreatePaymentRequestForInvoice_BlocksAfterVerifiedCollection(t *testing.T) {
	db := testInvoicePaymentDB(t)
	s := setupInvPay(t, db)

	// Seed a payment_succeeded attempt for this invoice.
	db.Create(&models.HostedPaymentAttempt{
		CompanyID:        s.companyID,
		InvoiceID:        s.invoiceID,
		HostedLinkID:     1,
		GatewayAccountID: s.gatewayID,
		ProviderType:     models.ProviderStripe,
		Amount:           decimal.NewFromInt(500),
		CurrencyCode:     "CAD",
		Status:           models.HostedPaymentAttemptPaymentSucceeded,
	})

	_, err := CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gatewayID,
	})
	if err == nil {
		t.Fatal("expected error when verified collection exists, got nil")
	}
	if !strings.Contains(err.Error(), "already been confirmed") {
		t.Errorf("expected error to mention confirmed payment; got: %v", err)
	}
}
