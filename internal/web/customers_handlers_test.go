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
		fmt.Sprintf("/tasks/billable-work/report?customer_id=%d", customerID),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected customers page to contain %q, got %q", want, body)
		}
	}
}
