// 遵循project_guide.md
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gobooks/internal/services"
	"gorm.io/gorm"
)

func TestTaskPagesHappyPath(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Web Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	customerID := seedValidationCustomer(t, db, companyID, "Task Customer")
	app := testRouteApp(t, db)

	newResp := performRequest(t, app, "/tasks/new", rawToken)
	if newResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, newResp.StatusCode)
	}
	newBody := readResponseBody(t, newResp)
	if !strings.Contains(newBody, "New Task") {
		t.Fatalf("expected new task page, got %q", newBody)
	}

	csrf := newCSRFToken(t)
	createForm := url.Values{
		"customer_id":   {fmt.Sprintf("%d", customerID)},
		"title":         {"April consulting"},
		"task_date":     {"2026-04-03"},
		"quantity":      {"2.50"},
		"unit_type":     {models.TaskUnitTypeHour},
		"rate":          {"150.00"},
		"currency_code": {"CAD"},
		"is_billable":   {"1"},
		"notes":         {"Initial task notes"},
	}
	createForm.Set(CSRFFormField, csrf)
	createResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/tasks",
		[]byte(createForm.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if createResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, createResp.StatusCode)
	}
	location := createResp.Header.Get("Location")
	if !strings.Contains(location, "/tasks/") || !strings.Contains(location, "created=1") {
		t.Fatalf("expected task detail redirect, got %q", location)
	}

	var created models.Task
	if err := db.Where("company_id = ? AND title = ?", companyID, "April consulting").First(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.Status != models.TaskStatusOpen {
		t.Fatalf("expected open status, got %q", created.Status)
	}

	detailResp := performRequest(t, app, location, rawToken)
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, detailResp.StatusCode)
	}
	detailBody := readResponseBody(t, detailResp)
	if !strings.Contains(detailBody, "April consulting") || !strings.Contains(detailBody, "Task Customer") {
		t.Fatalf("expected task detail content, got %q", detailBody)
	}
	if !strings.Contains(detailBody, "Not invoiced yet") {
		t.Fatalf("expected invoice linkage placeholder, got %q", detailBody)
	}

	listResp := performRequest(t, app, "/tasks?status=open", rawToken)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, listResp.StatusCode)
	}
	listBody := readResponseBody(t, listResp)
	if !strings.Contains(listBody, "April consulting") || !strings.Contains(listBody, ">Tasks<") {
		t.Fatalf("expected task list with nav entry, got %q", listBody)
	}

	editResp := performRequest(t, app, fmt.Sprintf("/tasks/%d/edit", created.ID), rawToken)
	if editResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, editResp.StatusCode)
	}
	editBody := readResponseBody(t, editResp)
	if !strings.Contains(editBody, "Edit Task") {
		t.Fatalf("expected edit form, got %q", editBody)
	}

	csrf = newCSRFToken(t)
	completeForm := url.Values{}
	completeForm.Set(CSRFFormField, csrf)
	completeResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		fmt.Sprintf("/tasks/%d/complete", created.ID),
		[]byte(completeForm.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if completeResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, completeResp.StatusCode)
	}
	completeLocation := completeResp.Header.Get("Location")
	if want := fmt.Sprintf("/tasks/%d?completed=1", created.ID); completeLocation != want {
		t.Fatalf("expected redirect to %q, got %q", want, completeLocation)
	}
	if err := db.First(&created, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if created.Status != models.TaskStatusCompleted {
		t.Fatalf("expected completed status, got %q", created.Status)
	}

	completedEditResp := performRequest(t, app, fmt.Sprintf("/tasks/%d/edit", created.ID), rawToken)
	if completedEditResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, completedEditResp.StatusCode)
	}
	completedEditBody := readResponseBody(t, completedEditResp)
	if !strings.Contains(completedEditBody, "Completed tasks keep billing snapshot fields locked") {
		t.Fatalf("expected completed edit lock banner, got %q", completedEditBody)
	}

	csrf = newCSRFToken(t)
	cancelForm := url.Values{}
	cancelForm.Set(CSRFFormField, csrf)
	cancelResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		fmt.Sprintf("/tasks/%d/cancel", created.ID),
		[]byte(cancelForm.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if cancelResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, cancelResp.StatusCode)
	}
	cancelLocation := cancelResp.Header.Get("Location")
	if want := fmt.Sprintf("/tasks/%d?cancelled=1", created.ID); cancelLocation != want {
		t.Fatalf("expected redirect to %q, got %q", want, cancelLocation)
	}
	if err := db.First(&created, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if created.Status != models.TaskStatusCancelled {
		t.Fatalf("expected cancelled status, got %q", created.Status)
	}

	cancelledEditResp := performRequest(t, app, fmt.Sprintf("/tasks/%d/edit", created.ID), rawToken)
	if cancelledEditResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, cancelledEditResp.StatusCode)
	}
	if !strings.Contains(cancelledEditResp.Header.Get("Location"), fmt.Sprintf("/tasks/%d?error=", created.ID)) {
		t.Fatalf("expected edit redirect with error, got %q", cancelledEditResp.Header.Get("Location"))
	}
}

func TestTaskDetailShowsLinkedExpensesBillLinesAndSummary(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Detail Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "Task Customer")
	vendorID := seedVendor(t, db, companyID, "Vendor A")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	taskID := seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusCompleted, "Task detail")

	exp1 := models.Expense{
		CompanyID:          companyID,
		TaskID:             &taskID,
		BillableCustomerID: &customerID,
		IsBillable:         true,
		ReinvoiceStatus:    models.ReinvoiceStatusUninvoiced,
		ExpenseDate:        time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		Description:        "Billable hotel",
		Amount:             decimal.RequireFromString("40.00"),
		CurrencyCode:       "CAD",
		VendorID:           &vendorID,
		ExpenseAccountID:   &expenseAccountID,
	}
	exp2 := models.Expense{
		CompanyID:          companyID,
		TaskID:             &taskID,
		BillableCustomerID: &customerID,
		IsBillable:         false,
		ExpenseDate:        time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Description:        "Internal meals",
		Amount:             decimal.RequireFromString("15.00"),
		CurrencyCode:       "CAD",
		ExpenseAccountID:   &expenseAccountID,
	}
	if err := db.Create(&exp1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&exp2).Error; err != nil {
		t.Fatal(err)
	}

	bill := models.Bill{
		CompanyID:           companyID,
		BillNumber:          "BILL-TASK-001",
		VendorID:            vendorID,
		BillDate:            time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		Status:              models.BillStatusDraft,
		CurrencyCode:        "CAD",
		PaymentTermSnapshot: models.PaymentTermSnapshot{TermCode: "DOC"},
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}
	customerCopy := customerID
	billLine1 := models.BillLine{
		CompanyID:          companyID,
		BillID:             bill.ID,
		SortOrder:          1,
		Description:        "Billable supplies",
		Qty:                decimal.NewFromInt(1),
		UnitPrice:          decimal.RequireFromString("30.00"),
		LineNet:            decimal.RequireFromString("30.00"),
		LineTax:            decimal.Zero,
		LineTotal:          decimal.RequireFromString("30.00"),
		ExpenseAccountID:   &expenseAccountID,
		TaskID:             &taskID,
		BillableCustomerID: &customerCopy,
		IsBillable:         true,
		ReinvoiceStatus:    models.ReinvoiceStatusUninvoiced,
	}
	billLine2 := models.BillLine{
		CompanyID:          companyID,
		BillID:             bill.ID,
		SortOrder:          2,
		Description:        "Non-billable supplies",
		Qty:                decimal.NewFromInt(1),
		UnitPrice:          decimal.RequireFromString("10.00"),
		LineNet:            decimal.RequireFromString("10.00"),
		LineTax:            decimal.Zero,
		LineTotal:          decimal.RequireFromString("10.00"),
		ExpenseAccountID:   &expenseAccountID,
		TaskID:             &taskID,
		BillableCustomerID: &customerCopy,
		IsBillable:         false,
	}
	if err := db.Create(&billLine1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&billLine2).Error; err != nil {
		t.Fatal(err)
	}

	app := testRouteApp(t, db)
	resp := performRequest(t, app, fmt.Sprintf("/tasks/%d", taskID), rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	for _, want := range []string{
		"Billable hotel",
		"Billable supplies",
		"Billable Expense Amount",
		"Non-billable Expense Cost",
		"70.00 CAD",
		"25.00 CAD",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected task detail to contain %q, got %q", want, body)
		}
	}
}

func TestTaskBillableWorkPageAndGenerateDraftHappyPath(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Draft Web Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	customerID := seedValidationCustomer(t, db, companyID, "Task Customer")
	otherCustomerID := seedValidationCustomer(t, db, companyID, "Other Customer")
	vendorID := seedVendor(t, db, companyID, "Draft Vendor")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	_ = seedValidationAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	_ = seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	if err := services.EnsureSystemTaskItems(db, companyID); err != nil {
		t.Fatal(err)
	}

	completedTaskID := seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusCompleted, "Completed implementation")
	_ = seedTaskForWeb(t, db, companyID, customerID, models.TaskStatusOpen, "Open task should stay hidden")
	_ = seedTaskForWeb(t, db, companyID, otherCustomerID, models.TaskStatusCompleted, "Other customer task")

	expense, err := services.CreateExpense(db, services.ExpenseInput{
		CompanyID:        companyID,
		TaskID:           &completedTaskID,
		IsBillable:       true,
		ExpenseDate:      time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Description:      "Client travel",
		Amount:           decimal.RequireFromString("45.00"),
		CurrencyCode:     "CAD",
		VendorID:         &vendorID,
		ExpenseAccountID: &expenseAccountID,
	})
	if err != nil {
		t.Fatal(err)
	}

	billLine := seedBillableBillLineForTaskWeb(t, db, companyID, vendorID, expenseAccountID, completedTaskID, "Contractor pass-through", "30.00")

	app := testRouteApp(t, db)

	pageResp := performRequest(t, app, fmt.Sprintf("/tasks/billable-work?customer_id=%d", customerID), rawToken)
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, pageResp.StatusCode)
	}
	pageBody := readResponseBody(t, pageResp)
	for _, want := range []string{
		"Billable Work",
		"Completed implementation",
		"Client travel",
		"Contractor pass-through",
		"Generate Invoice Draft",
	} {
		if !strings.Contains(pageBody, want) {
			t.Fatalf("expected billable work page to contain %q, got %q", want, pageBody)
		}
	}
	if strings.Contains(pageBody, "Open task should stay hidden") || strings.Contains(pageBody, "Other customer task") {
		t.Fatalf("expected ineligible tasks to be hidden, got %q", pageBody)
	}

	csrf := newCSRFToken(t)
	form := url.Values{}
	form.Set("customer_id", fmt.Sprintf("%d", customerID))
	form.Add("task_id", fmt.Sprintf("%d", completedTaskID))
	form.Add("expense_id", fmt.Sprintf("%d", expense.ID))
	form.Add("bill_line_id", fmt.Sprintf("%d", billLine.ID))
	form.Set(CSRFFormField, csrf)
	generateResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/tasks/billable-work/generate",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if generateResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, generateResp.StatusCode)
	}
	location := generateResp.Header.Get("Location")
	if !strings.Contains(location, "/invoices/") || !strings.Contains(location, "/edit?saved=1&locked=1") {
		t.Fatalf("expected invoice editor redirect, got %q", location)
	}

	var invoice models.Invoice
	if err := db.Preload("Lines").Where("company_id = ? AND customer_id = ?", companyID, customerID).Order("id desc").First(&invoice).Error; err != nil {
		t.Fatal(err)
	}
	if invoice.Status != models.InvoiceStatusDraft || len(invoice.Lines) != 3 {
		t.Fatalf("unexpected generated invoice: %+v with %d lines", invoice, len(invoice.Lines))
	}

	var task models.Task
	if err := db.First(&task, completedTaskID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != models.TaskStatusInvoiced {
		t.Fatalf("expected task to move to invoiced, got %q", task.Status)
	}
}

func TestTaskBillableWorkGenerateShowsErrorWhenNothingSelected(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Draft Error Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	customerID := seedValidationCustomer(t, db, companyID, "Task Customer")
	app := testRouteApp(t, db)

	csrf := newCSRFToken(t)
	form := url.Values{}
	form.Set("customer_id", fmt.Sprintf("%d", customerID))
	form.Set(CSRFFormField, csrf)
	resp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/tasks/billable-work/generate",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, services.ErrBillableWorkSelectionRequired.Error()) {
		t.Fatalf("expected selection error banner, got %q", body)
	}
}

func seedBillableBillLineForTaskWeb(t *testing.T, db *gorm.DB, companyID, vendorID, expenseAccountID, taskID uint, description, amount string) models.BillLine {
	t.Helper()

	bill := models.Bill{
		CompanyID:           companyID,
		BillNumber:          fmt.Sprintf("BILL-WEB-%d", time.Now().UnixNano()),
		VendorID:            vendorID,
		BillDate:            time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		Status:              models.BillStatusDraft,
		PaymentTermSnapshot: models.PaymentTermSnapshot{TermCode: "DOC"},
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}

	linkage, err := services.NormalizeTaskCostLinkage(db, services.TaskCostLinkageInput{
		CompanyID:  companyID,
		TaskID:     &taskID,
		IsBillable: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	line := models.BillLine{
		CompanyID:          companyID,
		BillID:             bill.ID,
		SortOrder:          1,
		Description:        description,
		Qty:                decimal.NewFromInt(1),
		UnitPrice:          decimal.RequireFromString(amount),
		LineNet:            decimal.RequireFromString(amount),
		LineTax:            decimal.Zero,
		LineTotal:          decimal.RequireFromString(amount),
		ExpenseAccountID:   &expenseAccountID,
		TaskID:             linkage.TaskID,
		BillableCustomerID: linkage.BillableCustomerID,
		IsBillable:         linkage.IsBillable,
		ReinvoiceStatus:    linkage.ReinvoiceStatus,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return line
}
