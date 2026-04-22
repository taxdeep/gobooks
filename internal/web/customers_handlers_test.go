package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gobooks/internal/services"
)

func TestCustomersPageShowsBillableSummary(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Customer Summary Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "Summary Customer")
	vendorID := seedVendor(t, db, companyID, "Summary Vendor")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)

	taskID := seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusCompleted, "Summary task")
	if _, err := services.CreateExpense(db, services.ExpenseInput{
		CompanyID:        companyID,
		TaskID:           &taskID,
		IsBillable:       true,
		ExpenseDate:      time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Description:      "Summary expense",
		Amount:           decimal.RequireFromString("15.00"),
		CurrencyCode:     "CAD",
		VendorID:         &vendorID,
		ExpenseAccountID: &expenseAccountID,
	}); err != nil {
		t.Fatal(err)
	}
	_ = seedBillableBillLineForTaskWeb(t, db, companyID, vendorID, expenseAccountID, taskID, "Summary bill line", "25.00")

	app := testRouteApp(t, db)
	resp := performRequest(t, app, "/customers", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		"Unbilled labor",
		"Unbilled expenses",
		"Total unbilled",
		"125.00 CAD",
		"40.00 CAD",
		"165.00 CAD",
		fmt.Sprintf("/customers/%d", customerID),
		fmt.Sprintf("/tasks/billable-work/report?customer_id=%d", customerID),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected customers page to contain %q, got %q", want, body)
		}
	}
}

func TestCustomerDetailPageHappyPath(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Customer Workspace Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "Workspace Customer")
	otherCustomerID := seedValidationCustomer(t, db, companyID, "Other Customer")
	vendorID := seedVendor(t, db, companyID, "Workspace Vendor")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)

	if err := db.Create(&models.PaymentTerm{
		CompanyID:   companyID,
		Code:        "N30",
		Description: "Net 30",
		IsDefault:   true,
		IsActive:    true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Customer{}).
		Where("id = ?", customerID).
		Updates(map[string]any{
			"email":                     "workspace@example.com",
			"default_payment_term_code": "N30",
			"addr_street1":              "123 Main St",
			"addr_city":                 "Vancouver",
			"addr_province":             "BC",
			"addr_country":              "CA",
		}).Error; err != nil {
		t.Fatal(err)
	}

	taskID := seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusCompleted, "Workspace task")
	if _, err := services.CreateExpense(db, services.ExpenseInput{
		CompanyID:        companyID,
		TaskID:           &taskID,
		IsBillable:       true,
		ExpenseDate:      time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Description:      "Workspace expense",
		Amount:           decimal.RequireFromString("15.00"),
		CurrencyCode:     "CAD",
		VendorID:         &vendorID,
		ExpenseAccountID: &expenseAccountID,
	}); err != nil {
		t.Fatal(err)
	}
	_ = seedBillableBillLineForTaskWeb(t, db, companyID, vendorID, expenseAccountID, taskID, "Workspace bill line", "25.00")
	_ = seedTaskForWeb(t, db, companyID, otherCustomerID, models.TaskStatusCompleted, "Other task")

	for _, inv := range []models.Invoice{
		{
			CompanyID:     companyID,
			InvoiceNumber: "INV-CUST-001",
			CustomerID:    customerID,
			InvoiceDate:   time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
			Status:        models.InvoiceStatusIssued,
			Amount:        decimal.RequireFromString("250.00"),
			BalanceDue:    decimal.RequireFromString("250.00"),
			CurrencyCode:  "CAD",
			DueDate:       datePtrWeb(t, "2026-04-01"),
		},
		{
			CompanyID:     companyID,
			InvoiceNumber: "INV-CUST-002",
			CustomerID:    customerID,
			InvoiceDate:   time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			Status:        models.InvoiceStatusPaid,
			Amount:        decimal.RequireFromString("125.00"),
			BalanceDue:    decimal.Zero,
			CurrencyCode:  "CAD",
		},
		{
			CompanyID:     companyID,
			InvoiceNumber: "INV-CUST-003",
			CustomerID:    customerID,
			InvoiceDate:   time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC),
			Status:        models.InvoiceStatusPartiallyPaid,
			Amount:        decimal.RequireFromString("80.00"),
			BalanceDue:    decimal.RequireFromString("30.00"),
			CurrencyCode:  "CAD",
			// Due date is kept in the future (rolling) so the
			// "exactly 1 overdue invoice" assertion below stays
			// stable as wall-clock time advances. Pre-IN.9 this
			// was a fixed "2026-04-20" literal that rotted on
			// 2026-04-21 when today's-date crossed it.
			DueDate: futureDueDateWeb(1),
		},
		{
			CompanyID:     companyID,
			InvoiceNumber: "INV-OTHER-001",
			CustomerID:    otherCustomerID,
			InvoiceDate:   time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC),
			Status:        models.InvoiceStatusDraft,
			Amount:        decimal.RequireFromString("999.00"),
			BalanceDue:    decimal.RequireFromString("999.00"),
			CurrencyCode:  "CAD",
		},
	} {
		invoice := inv
		if err := db.Create(&invoice).Error; err != nil {
			t.Fatal(err)
		}
	}

	app := testRouteApp(t, db)
	resp := performRequest(t, app, fmt.Sprintf("/customers/%d", customerID), rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		"Customer Workspace",
		"Workspace Customer",
		"workspace@example.com",
		"N30 — Net 30",
		"Unbilled Labor",
		"Unbilled Expenses",
		"Total Unbilled",
		"Outstanding AR",
		"Outstanding Invoices",
		"Overdue Invoices",
		"125.00 CAD",
		"40.00 CAD",
		"165.00 CAD",
		"280.00 CAD",
		">2<",
		">1<",
		fmt.Sprintf("/tasks/billable-work/report?customer_id=%d", customerID),
		fmt.Sprintf("/tasks?customer_id=%d", customerID),
		fmt.Sprintf("/invoices?customer_id=%d", customerID),
		"INV-CUST-002",
		"INV-CUST-003",
		"Paid",
		"Partially Paid",
		"Unpaid",
		"50.00 CAD",
		"30.00 CAD",
		"250.00 CAD",
		"2026-04-06",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected customer detail to contain %q, got %q", want, body)
		}
	}
	if strings.Contains(body, "INV-OTHER-001") {
		t.Fatalf("expected other customer invoice to stay hidden, got %q", body)
	}
}

func TestInvoicesListFiltersByCustomerID(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Invoice Customer Filter Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerA := seedValidationCustomer(t, db, companyID, "Invoice Customer A")
	customerB := seedValidationCustomer(t, db, companyID, "Invoice Customer B")

	for _, inv := range []models.Invoice{
		{
			CompanyID:     companyID,
			InvoiceNumber: "INV-FILTER-A",
			CustomerID:    customerA,
			InvoiceDate:   time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
			Status:        models.InvoiceStatusPartiallyPaid,
			Amount:        decimal.RequireFromString("100.00"),
			BalanceDue:    decimal.RequireFromString("40.00"),
			CurrencyCode:  "CAD",
		},
		{
			CompanyID:     companyID,
			InvoiceNumber: "INV-FILTER-B",
			CustomerID:    customerB,
			InvoiceDate:   time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
			Status:        models.InvoiceStatusIssued,
			Amount:        decimal.RequireFromString("200.00"),
			BalanceDue:    decimal.RequireFromString("200.00"),
			CurrencyCode:  "CAD",
		},
	} {
		invoice := inv
		if err := db.Create(&invoice).Error; err != nil {
			t.Fatal(err)
		}
	}

	app := testRouteApp(t, db)
	resp := performRequest(t, app, fmt.Sprintf("/invoices?customer_id=%d", customerA), rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "INV-FILTER-A") {
		t.Fatalf("expected filtered invoice list to include target customer invoice, got %q", body)
	}
	if !strings.Contains(body, "Partially Paid") || !strings.Contains(body, "40.00 CAD") {
		t.Fatalf("expected filtered invoice list to show payment visibility for target invoice, got %q", body)
	}
	if strings.Contains(body, "INV-FILTER-B") {
		t.Fatalf("expected filtered invoice list to exclude other customer invoice, got %q", body)
	}
}

func datePtrWeb(t *testing.T, value string) *time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

// futureDueDateWeb returns a *time.Time `monthsOut` months from
// today at UTC midnight. Used by tests that need a guaranteed-
// future due date for "not-yet-overdue" invoice fixtures, so the
// test's overdue-count assertions stay stable as wall-clock time
// advances.
func futureDueDateWeb(monthsOut int) *time.Time {
	today := time.Now().UTC()
	d := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, monthsOut, 0)
	return &d
}
