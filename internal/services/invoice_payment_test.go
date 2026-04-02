// 遵循project_guide.md
package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
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
		&models.PaymentGatewayAccount{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
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
	if req.Status != models.PaymentRequestCreated {
		t.Errorf("Expected status created, got %s", req.Status)
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
