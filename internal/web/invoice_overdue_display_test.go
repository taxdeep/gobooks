package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

func TestInvoiceOverdueDisplayUsesComputedStatus(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Invoice Overdue Display Co")
	app := errorFeedbackApp(server, user, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "Overdue Customer")
	yesterday := time.Now().AddDate(0, 0, -1)

	inv := models.Invoice{
		CompanyID:     companyID,
		CustomerID:    customerID,
		InvoiceNumber: "INV-OVERDUE-001",
		InvoiceDate:   time.Now().AddDate(0, 0, -10).UTC(),
		DueDate:       &yesterday,
		Status:        models.InvoiceStatusIssued,
		CurrencyCode:  "USD",
		Amount:        decimal.RequireFromString("150.00"),
		BalanceDue:    decimal.RequireFromString("150.00"),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	listResp := performRequest(t, app, "/invoices?status=overdue", "")
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, listResp.StatusCode)
	}
	listBody := readResponseBody(t, listResp)
	if !strings.Contains(listBody, "INV-OVERDUE-001") {
		t.Fatalf("expected overdue invoice in overdue filter, got %q", listBody)
	}
	if !strings.Contains(listBody, "Overdue") {
		t.Fatalf("expected overdue badge in list page, got %q", listBody)
	}

	issuedResp := performRequest(t, app, "/invoices?status=issued", "")
	if issuedResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, issuedResp.StatusCode)
	}
	issuedBody := readResponseBody(t, issuedResp)
	if strings.Contains(issuedBody, "INV-OVERDUE-001") {
		t.Fatalf("expected computed-overdue invoice to be excluded from issued filter, got %q", issuedBody)
	}

	detailResp := performRequest(t, app, fmt.Sprintf("/invoices/%d", inv.ID), "")
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, detailResp.StatusCode)
	}
	detailBody := readResponseBody(t, detailResp)
	if !strings.Contains(detailBody, "Overdue") {
		t.Fatalf("expected overdue badge on detail page, got %q", detailBody)
	}
}
