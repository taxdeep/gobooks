package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

func TestExpensePagesSaveTaskLinkageAndKeepOrdinaryPathWorking(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Expense Web Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "Task Customer")
	otherCustomerID := seedValidationCustomer(t, db, companyID, "Other Customer")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	vendorID := seedVendor(t, db, companyID, "Expense Vendor")
	openTaskID := seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusOpen, "Open install")
	seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusCancelled, "Cancelled task")
	seedTaskForWeb(t, db, companyID, otherCustomerID, models.TaskStatusInvoiced, "Invoiced task")

	app := testRouteApp(t, db)

	newResp := performRequest(t, app, "/expenses/new", rawToken)
	if newResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, newResp.StatusCode)
	}
	body := readResponseBody(t, newResp)
	if !strings.Contains(body, "Open install") {
		t.Fatalf("expected selectable open task, got %q", body)
	}
	if strings.Contains(body, "Cancelled task") || strings.Contains(body, "Invoiced task") {
		t.Fatalf("expected cancelled/invoiced tasks to be hidden, got %q", body)
	}

	csrf := newCSRFToken(t)
	form := url.Values{
		"expense_date":       {"2026-04-04"},
		"description":        {"Client materials"},
		"amount":             {"45.00"},
		"currency_code":      {"CAD"},
		"vendor_id":          {fmt.Sprintf("%d", vendorID)},
		"expense_account_id": {fmt.Sprintf("%d", expenseAccountID)},
		"task_id":            {fmt.Sprintf("%d", openTaskID)},
		"is_billable":        {"1"},
		"notes":              {"Linked to task"},
	}
	form.Set(CSRFFormField, csrf)
	resp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/expenses",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/expenses?created=1" {
		t.Fatalf("expected redirect to created list, got %q", got)
	}

	var linked models.Expense
	if err := db.Where("company_id = ? AND description = ?", companyID, "Client materials").First(&linked).Error; err != nil {
		t.Fatal(err)
	}
	if linked.TaskID == nil || *linked.TaskID != openTaskID {
		t.Fatalf("expected task linkage to %d, got %+v", openTaskID, linked.TaskID)
	}
	if linked.BillableCustomerID == nil || *linked.BillableCustomerID != customerID {
		t.Fatalf("expected billable customer %d, got %+v", customerID, linked.BillableCustomerID)
	}
	if linked.ReinvoiceStatus != models.ReinvoiceStatusUninvoiced {
		t.Fatalf("expected uninvoiced status, got %q", linked.ReinvoiceStatus)
	}

	csrf = newCSRFToken(t)
	ordinaryForm := url.Values{
		"expense_date":       {"2026-04-05"},
		"description":        {"Office snacks"},
		"amount":             {"12.50"},
		"currency_code":      {"CAD"},
		"expense_account_id": {fmt.Sprintf("%d", expenseAccountID)},
		"is_billable":        {"1"},
	}
	ordinaryForm.Set(CSRFFormField, csrf)
	ordinaryResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/expenses",
		[]byte(ordinaryForm.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if ordinaryResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, ordinaryResp.StatusCode)
	}

	var ordinary models.Expense
	if err := db.Where("company_id = ? AND description = ?", companyID, "Office snacks").First(&ordinary).Error; err != nil {
		t.Fatal(err)
	}
	if ordinary.TaskID != nil {
		t.Fatalf("expected no task linkage, got %+v", ordinary.TaskID)
	}
	if ordinary.BillableCustomerID != nil {
		t.Fatalf("expected no billable customer, got %+v", ordinary.BillableCustomerID)
	}
	if ordinary.ReinvoiceStatus != models.ReinvoiceStatusNone {
		t.Fatalf("expected empty reinvoice status, got %q", ordinary.ReinvoiceStatus)
	}
}

func seedTaskForWeb(t *testing.T, db *gorm.DB, companyID, customerID uint, status models.TaskStatus, title string) uint {
	t.Helper()
	task := models.Task{
		CompanyID:    companyID,
		CustomerID:   customerID,
		Title:        title,
		TaskDate:     time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		Quantity:     decimal.RequireFromString("1"),
		UnitType:     models.TaskUnitTypeHour,
		Rate:         decimal.RequireFromString("125.00"),
		CurrencyCode: "CAD",
		IsBillable:   true,
		Status:       status,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	return task.ID
}

func seedVendor(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()
	vendor := models.Vendor{CompanyID: companyID, Name: name}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	return vendor.ID
}
