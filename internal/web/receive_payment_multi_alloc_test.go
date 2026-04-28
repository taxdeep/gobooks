// 遵循project_guide.md
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// seedOpenInvoiceForCustomer creates a fresh 100.00 open invoice for an
// existing customer. Used when a test needs multiple open invoices
// attached to the *same* customer (the shared
// seedReportCacheOpenInvoice helper creates its own customer per call,
// which is the wrong shape for batch-payment tests).
func seedOpenInvoiceForCustomer(t *testing.T, db *gorm.DB, companyID, customerID uint, number string) *models.Invoice {
	t.Helper()
	inv := &models.Invoice{
		CompanyID:            companyID,
		InvoiceNumber:        number,
		CustomerID:           customerID,
		InvoiceDate:          time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		DueDate:              ptrTime(time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)),
		Status:               models.InvoiceStatusIssued,
		Amount:               decimal.RequireFromString("100.00"),
		AmountBase:           decimal.RequireFromString("100.00"),
		Subtotal:             decimal.RequireFromString("100.00"),
		TaxTotal:             decimal.Zero,
		BalanceDue:           decimal.RequireFromString("100.00"),
		BalanceDueBase:       decimal.RequireFromString("100.00"),
		CustomerNameSnapshot: "Batch Payment Customer",
	}
	if err := db.Create(inv).Error; err != nil {
		t.Fatal(err)
	}
	return inv
}

// TestReceivePayment_MultiAllocation locks the behaviour the user
// asked for: one POST to /banking/receive-payment can carry N
// (invoice_id, amount) pairs via the allocation_invoice_id[] +
// allocation_amount[] parallel form fields, and each allocation is
// recorded against its respective invoice.
//
// Also covers two adjacent contracts:
//   - Partial payment per row (amount < balance due) succeeds.
//   - Legacy single-invoice_id + amount form still works (deep-link
//     from invoice detail page).
func TestReceivePayment_MultiAllocation(t *testing.T) {
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(&models.PaymentReceipt{}, &models.SettlementAllocation{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, ReportCache: NewReportAcceleration()}
	t.Cleanup(server.ReportCache.plCache.Close)
	t.Cleanup(server.ReportCache.arCache.Close)

	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Multi Alloc Co")
	bankAccountID := seedValidationAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)

	customerID := seedValidationCustomer(t, db, companyID, "Batch Payment Customer")
	inv1 := seedOpenInvoiceForCustomer(t, db, companyID, customerID, "INV-MA-001")
	inv2 := seedOpenInvoiceForCustomer(t, db, companyID, customerID, "INV-MA-002")

	app := reportCacheLifecycleApp(server, user, companyID)

	form := url.Values{
		"customer_id":     {fmt.Sprintf("%d", customerID)},
		"payment_method":  {string(models.PaymentMethodCheck)},
		"entry_date":      {"2026-04-30"},
		"bank_account_id": {fmt.Sprintf("%d", bankAccountID)},
		// Two parallel allocation entries. Partial pay on inv1 (40 of
		// balance), full pay on inv2. Server computes total = 140.
		"allocation_invoice_id": {
			fmt.Sprintf("%d", inv1.ID),
			fmt.Sprintf("%d", inv2.ID),
		},
		"allocation_amount": {
			"40.00",
			"100.00",
		},
		"memo": {"Batch payment — INV 001 partial, INV 002 full"},
	}
	resp := performFormRequest(t, app, http.MethodPost, "/banking/receive-payment", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("multi-alloc POST: expected 303, got %d", resp.StatusCode)
	}

	// Both invoices must have received allocations.
	var reloaded1, reloaded2 models.Invoice
	if err := db.First(&reloaded1, inv1.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&reloaded2, inv2.ID).Error; err != nil {
		t.Fatal(err)
	}
	// inv1: balance was 100, paid 40 → balance_due now 60, status
	// partially_paid. inv2: balance was 100, paid 100 → paid.
	wantInv1Balance := decimal.RequireFromString("60.00")
	if !reloaded1.BalanceDue.Equal(wantInv1Balance) {
		t.Errorf("inv1.BalanceDue = %s, want %s", reloaded1.BalanceDue, wantInv1Balance)
	}
	if reloaded1.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("inv1.Status = %q, want %q", reloaded1.Status, models.InvoiceStatusPartiallyPaid)
	}
	if !reloaded2.BalanceDue.IsZero() {
		t.Errorf("inv2.BalanceDue = %s, want 0", reloaded2.BalanceDue)
	}
	if reloaded2.Status != models.InvoiceStatusPaid {
		t.Errorf("inv2.Status = %q, want %q", reloaded2.Status, models.InvoiceStatusPaid)
	}

	// Legacy single-invoice form still works — asserts backward compat.
	inv3 := seedOpenInvoiceForCustomer(t, db, companyID, customerID, "INV-MA-003")
	legacyForm := url.Values{
		"customer_id":     {fmt.Sprintf("%d", customerID)},
		"payment_method":  {string(models.PaymentMethodCheck)},
		"entry_date":      {"2026-04-30"},
		"bank_account_id": {fmt.Sprintf("%d", bankAccountID)},
		"invoice_id":      {fmt.Sprintf("%d", inv3.ID)},
		"amount":          {"100.00"},
	}
	resp2 := performFormRequest(t, app, http.MethodPost, "/banking/receive-payment", legacyForm, "")
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("legacy single-invoice POST: expected 303, got %d", resp2.StatusCode)
	}
	var reloaded3 models.Invoice
	if err := db.First(&reloaded3, inv3.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded3.Status != models.InvoiceStatusPaid {
		t.Errorf("legacy path: inv3.Status = %q, want paid", reloaded3.Status)
	}
}

// Overpayment behaviour moved to CustomerDeposit path — see the service-layer
// test in internal/services/receive_payment_deposit_test.go. HTTP-layer
// coverage will return once the handler + form accept the new_deposit_amount
// field (UI slice 3).
