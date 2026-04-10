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

// ── SmartPicker integration tests ─────────────────────────────────────────────

// TestExpenseNew_SmartPickerAttrs verifies that GET /expenses/new renders the
// SmartPicker with the correct data-* attributes for the expense account field,
// and that the no-JS fallback select is also present.
func TestExpenseNew_SmartPickerAttrs(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP New Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, "/expenses/new", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	for _, want := range []string{
		`data-entity="account"`,
		`data-context="expense_form_category"`,
		`data-required="true"`,
		`data-field-name="expense_account_id"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing SmartPicker attr %q in new form", want)
		}
	}
	// No-JS fallback select must be present in the HTML structure.
	if !strings.Contains(body, `<select`) {
		t.Error("fallback select must be present for no-JS users")
	}
	if !strings.Contains(body, `name="expense_account_id"`) {
		t.Error("fallback select must have name attribute for no-JS submission")
	}
}

// TestExpenseEdit_SmartPickerRehydration verifies that the edit page correctly
// populates data-value and data-selected-label for the SmartPicker, enabling
// the visible input to show the account name rather than a raw ID.
func TestExpenseEdit_SmartPickerRehydration(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Edit Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	accID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	expenseID := seedExpenseForSP(t, db, companyID, accID)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, fmt.Sprintf("/expenses/%d/edit", expenseID), rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	if !strings.Contains(body, fmt.Sprintf(`data-value="%d"`, accID)) {
		t.Errorf("missing data-value=%d in edit page HTML", accID)
	}
	if !strings.Contains(body, `data-selected-label="Office Supplies"`) {
		t.Error("missing data-selected-label in edit page HTML")
	}
	// Raw ID must not appear as a visible text node.
	if strings.Contains(body, fmt.Sprintf(">%d<", accID)) {
		t.Errorf("raw account ID %d must not appear as text content", accID)
	}
}

// TestExpenseEdit_InvalidAccountRehydration verifies that when a previously
// saved expense account is no longer valid (inactive), the edit page clears
// the SmartPicker value and shows the "no longer available" error.
func TestExpenseEdit_InvalidAccountRehydration(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Invalid Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	// Create account active, save expense, then deactivate account.
	accID := seedSPAccount(t, db, companyID, "6100", "Retired Account", models.RootExpense, true)
	expenseID := seedExpenseForSP(t, db, companyID, accID)
	if err := db.Model(&models.Account{}).Where("id = ?", accID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	app := testRouteApp(t, db)

	resp := performRequest(t, app, fmt.Sprintf("/expenses/%d/edit", expenseID), rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	if strings.Contains(body, fmt.Sprintf(`data-value="%d"`, accID)) {
		t.Error("data-value must be cleared for invalid account")
	}
	if !strings.Contains(body, `data-value=""`) {
		t.Error("data-value must be empty string for invalid account")
	}
	if !strings.Contains(body, "Previously selected expense account is no longer available") {
		t.Error("missing 'no longer available' error message")
	}
}

// TestExpense_SaveRejectsInactiveAccount verifies that POST /expenses with an
// inactive expense account ID is rejected by the backend.
func TestExpense_SaveRejectsInactiveAccount(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Inactive Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	accID := seedSPAccount(t, db, companyID, "6100", "Retired", models.RootExpense, false)
	app := testRouteApp(t, db)

	csrf := newCSRFToken(t)
	form := url.Values{
		"expense_date":       {"2026-04-10"},
		"description":        {"Test"},
		"amount":             {"10.00"},
		"currency_code":      {"CAD"},
		"expense_account_id": {fmt.Sprintf("%d", accID)},
	}
	form.Set(CSRFFormField, csrf)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	// Must re-render form (200) with error, not redirect.
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("inactive account must not be accepted")
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "expense account") && !strings.Contains(body, "account") {
		t.Errorf("expected account error message, got body snippet: %.200s", body)
	}
	// SmartPicker must render data-value="" — illegal ID must not leak into re-render.
	if !strings.Contains(body, `data-value=""`) {
		t.Error("data-value must be empty when account is rejected (inactive)")
	}
	if strings.Contains(body, fmt.Sprintf(`data-value="%d"`, accID)) {
		t.Errorf("illegal inactive account ID %d must not appear in data-value", accID)
	}
}

// TestExpense_SaveRejectsCrossCompanyAccount verifies that a cross-company account
// ID is rejected — company isolation must not be bypassable via POST.
func TestExpense_SaveRejectsCrossCompanyAccount(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Own Co2")
	otherID := seedCompany(t, db, "SP Other Co2")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	otherAccID := seedSPAccount(t, db, otherID, "6100", "Other Co Account", models.RootExpense, true)
	app := testRouteApp(t, db)

	csrf := newCSRFToken(t)
	form := url.Values{
		"expense_date":       {"2026-04-10"},
		"description":        {"Test"},
		"amount":             {"10.00"},
		"currency_code":      {"CAD"},
		"expense_account_id": {fmt.Sprintf("%d", otherAccID)},
	}
	form.Set(CSRFFormField, csrf)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("cross-company account must not be accepted")
	}
	body := readResponseBody(t, resp)
	// SmartPicker must render data-value="" — cross-company ID must not leak into re-render.
	if !strings.Contains(body, `data-value=""`) {
		t.Error("data-value must be empty when account is rejected (cross-company)")
	}
	if strings.Contains(body, fmt.Sprintf(`data-value="%d"`, otherAccID)) {
		t.Errorf("cross-company account ID %d must not appear in data-value", otherAccID)
	}
}

// TestExpense_SaveRejectsNonExpenseAccount verifies that a revenue account ID
// is rejected — only expense-type accounts are valid.
func TestExpense_SaveRejectsNonExpenseAccount(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Revenue Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	revAccID := seedSPAccount(t, db, companyID, "4100", "Sales Revenue", models.RootRevenue, true)
	app := testRouteApp(t, db)

	csrf := newCSRFToken(t)
	form := url.Values{
		"expense_date":       {"2026-04-10"},
		"description":        {"Test"},
		"amount":             {"10.00"},
		"currency_code":      {"CAD"},
		"expense_account_id": {fmt.Sprintf("%d", revAccID)},
	}
	form.Set(CSRFFormField, csrf)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("non-expense account must not be accepted")
	}
	body := readResponseBody(t, resp)
	// SmartPicker must render data-value="" — non-expense account ID must not leak into re-render.
	if !strings.Contains(body, `data-value=""`) {
		t.Error("data-value must be empty when account is rejected (non-expense type)")
	}
	if strings.Contains(body, fmt.Sprintf(`data-value="%d"`, revAccID)) {
		t.Errorf("non-expense account ID %d must not appear in data-value", revAccID)
	}
}

// TestExpense_ErrorRerenderPreservesSmartPickerState verifies that when a POST
// fails due to a non-account field error, the SmartPicker retains the previously
// submitted account ID and label.
func TestExpense_ErrorRerenderPreservesSmartPickerState(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Rerender Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	accID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	app := testRouteApp(t, db)

	csrf := newCSRFToken(t)
	form := url.Values{
		"expense_date":       {"2026-04-10"},
		"description":        {""},           // empty — will trigger validation error
		"amount":             {"25.00"},
		"currency_code":      {"CAD"},
		"expense_account_id": {fmt.Sprintf("%d", accID)},
	}
	form.Set(CSRFFormField, csrf)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("form with empty description should not redirect")
	}
	body := readResponseBody(t, resp)

	// SmartPicker must retain the submitted valid account.
	if !strings.Contains(body, fmt.Sprintf(`data-value="%d"`, accID)) {
		t.Errorf("data-value must be preserved on error re-render, accID=%d", accID)
	}
	if !strings.Contains(body, `data-selected-label="Office Supplies"`) {
		t.Error("data-selected-label must be preserved on error re-render")
	}
}

// TestExpense_FallbackSelectInHTML verifies that the no-JS fallback select is
// present in the HTML with a name attribute, proving the no-JS path is viable.
func TestExpense_FallbackSelectInHTML(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Fallback Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, "/expenses/new", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	// The fallback select must appear with name="expense_account_id" in the HTML.
	if !strings.Contains(body, `name="expense_account_id"`) {
		t.Error("fallback select must have name attribute for no-JS form submission")
	}
	// At least one option must be present for the user to choose from.
	if !strings.Contains(body, "<option value=") {
		t.Error("fallback select must contain options")
	}
}

// seedExpenseForSP creates a minimal expense linked to an account for SmartPicker tests.
func seedExpenseForSP(t *testing.T, db *gorm.DB, companyID, accID uint) uint {
	t.Helper()
	exp := models.Expense{
		CompanyID:        companyID,
		ExpenseDate:      time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Description:      "SP test expense",
		Amount:           decimal.RequireFromString("50.00"),
		CurrencyCode:     "CAD",
		ExpenseAccountID: &accID,
	}
	if err := db.Create(&exp).Error; err != nil {
		t.Fatal(err)
	}
	return exp.ID
}
