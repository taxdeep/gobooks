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

func TestTaskBillableWorkReportPageHappyPath(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Report Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "Task Customer")
	otherCustomerID := seedValidationCustomer(t, db, companyID, "Other Customer")
	vendorID := seedVendor(t, db, companyID, "Report Vendor")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	_ = seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	if err := services.EnsureSystemTaskItems(db, companyID); err != nil {
		t.Fatal(err)
	}

	taskID := seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusCompleted, "Visibility task")
	_ = seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusOpen, "Hidden open task")
	_ = seedTaskForWeb(t, db, companyID, otherCustomerID, models.TaskStatusCompleted, "Other customer completed")
	if _, err := services.CreateExpense(db, services.ExpenseInput{
		CompanyID:        companyID,
		TaskID:           &taskID,
		IsBillable:       true,
		ExpenseDate:      time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Description:      "Visibility expense",
		Amount:           decimal.RequireFromString("45.00"),
		CurrencyCode:     "CAD",
		VendorID:         &vendorID,
		ExpenseAccountID: &expenseAccountID,
	}); err != nil {
		t.Fatal(err)
	}
	_ = seedBillableBillLineForTaskWeb(t, db, companyID, vendorID, expenseAccountID, taskID, "Visibility bill line", "30.00")

	app := testRouteApp(t, db)
	resp := performRequest(t, app, fmt.Sprintf("/tasks/billable-work/report?customer_id=%d", customerID), rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		"Billable Work Report",
		"Visibility task",
		"Visibility expense",
		"Visibility bill line",
		"Unbilled Task Labor",
		"Unbilled Billable Expense",
		"Total Unbilled",
		"125.00 CAD",
		"75.00 CAD",
		"200.00 CAD",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected report page to contain %q, got %q", want, body)
		}
	}
	if strings.Contains(body, "Hidden open task") || strings.Contains(body, "Other customer completed") {
		t.Fatalf("expected ineligible tasks hidden, got %q", body)
	}
}

func TestTaskDetailShowsBillingTraceHistory(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Trace Web Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "Trace Customer")
	vendorID := seedVendor(t, db, companyID, "Trace Vendor")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	_ = seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	if err := services.EnsureSystemTaskItems(db, companyID); err != nil {
		t.Fatal(err)
	}

	taskID := seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusCompleted, "Trace task")
	expense, err := services.CreateExpense(db, services.ExpenseInput{
		CompanyID:        companyID,
		TaskID:           &taskID,
		IsBillable:       true,
		ExpenseDate:      time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Description:      "Trace expense",
		Amount:           decimal.RequireFromString("20.00"),
		CurrencyCode:     "CAD",
		VendorID:         &vendorID,
		ExpenseAccountID: &expenseAccountID,
	})
	if err != nil {
		t.Fatal(err)
	}
	billLine := seedBillableBillLineForTaskWeb(t, db, companyID, vendorID, expenseAccountID, taskID, "Trace bill line", "10.00")

	firstDraft, err := services.GenerateInvoiceDraft(db, services.GenerateInvoiceDraftInput{
		CompanyID:  companyID,
		CustomerID: customerID,
		TaskIDs:    []uint{taskID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.DeleteInvoice(db, companyID, firstDraft.InvoiceID, "tester", nil); err != nil {
		t.Fatal(err)
	}
	secondDraft, err := services.GenerateInvoiceDraft(db, services.GenerateInvoiceDraftInput{
		CompanyID:   companyID,
		CustomerID:  customerID,
		TaskIDs:     []uint{taskID},
		ExpenseIDs:  []uint{expense.ID},
		BillLineIDs: []uint{billLine.ID},
		Actor:       "tester",
	})
	if err != nil {
		t.Fatal(err)
	}

	var invoice models.Invoice
	if err := db.First(&invoice, secondDraft.InvoiceID).Error; err != nil {
		t.Fatal(err)
	}

	app := testRouteApp(t, db)
	resp := performRequest(t, app, fmt.Sprintf("/tasks/%d", taskID), rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		"Task Billing Trace",
		"Task Invoice History",
		"Linked Expense Invoice History",
		"Linked Bill Line Invoice History",
		"Trace expense",
		"Trace bill line",
		"Released draft reference cleared",
		"In draft invoice",
		invoice.InvoiceNumber,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected task detail to contain %q, got %q", want, body)
		}
	}
}
