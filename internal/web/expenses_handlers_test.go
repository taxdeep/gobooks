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

	"balanciz/internal/models"
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
		"expense_date":               {"2026-04-04"},
		"currency_code":              {"CAD"},
		"vendor_id":                  {fmt.Sprintf("%d", vendorID)},
		"notes":                      {"Linked to task"},
		"line_count":                 {"1"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", expenseAccountID)},
		"line_description[0]":        {"Client materials"},
		"line_amount[0]":             {"45.00"},
		"line_task_id[0]":            {fmt.Sprintf("%d", openTaskID)},
		"line_is_billable[0]":        {"1"},
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
	if err := db.Where("company_id = ? AND description LIKE ?", companyID, "%Client materials%").First(&linked).Error; err != nil {
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
		"expense_date":               {"2026-04-05"},
		"currency_code":              {"CAD"},
		"is_billable":                {"1"},
		"line_count":                 {"1"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", expenseAccountID)},
		"line_description[0]":        {"Office snacks"},
		"line_amount[0]":             {"12.50"},
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
	if err := db.Where("company_id = ? AND description LIKE ?", companyID, "%Office snacks%").First(&ordinary).Error; err != nil {
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
// SmartPicker with the correct data-* attributes for vendor, expense account,
// and payment account fields.
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
		// Vendor SmartPicker
		`data-entity="vendor"`,
		`data-context="expense_form_vendor"`,
		`data-field-name="vendor_id"`,
		// Payment account SmartPicker
		`data-entity="payment_account"`,
		`data-context="expense_form_payment"`,
		`data-field-name="payment_account_id"`,
		// Expense accounts pre-loaded as JSON for the line-item category <select>
		`data-expense-accounts=`,
		// Line-item section present
		`line_count`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing attr %q in new expense form", want)
		}
	}
	// Native selects (task, payment method, currency) must still be present.
	if !strings.Contains(body, `<select`) {
		t.Error("native selects (task, payment method) must be present")
	}
	// expense_account_id must NOT appear as a top-level SmartPicker;
	// it is now a line-item category <select> managed by Alpine.
	for _, absent := range []string{
		`data-context="expense_form_category"`,
		`<select name="expense_account_id"`,
	} {
		if strings.Contains(body, absent) {
			t.Errorf("expense account SmartPicker attr %q must not appear; category is now a line-level select", absent)
		}
	}
}

// TestExpenseEdit_SmartPickerRehydration verifies that the edit page correctly
// populates the account ID in data-initial-lines JSON so the line-item category
// <select> is pre-selected on the edit page.
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

	// Account ID must appear in data-initial-lines JSON for Alpine to pre-select it.
	// Templ HTML-escapes attribute values: " → &#34;
	escapedID := fmt.Sprintf("&#34;expense_account_id&#34;:&#34;%d&#34;", accID)
	if !strings.Contains(body, escapedID) {
		t.Errorf("account ID %d must appear (HTML-escaped) in data-initial-lines attr", accID)
	}
	// The vendor SmartPicker must still be present.
	if !strings.Contains(body, `data-entity="vendor"`) {
		t.Error("vendor SmartPicker must still be present on edit page")
	}
}

// TestExpenseEdit_InvalidAccountRehydration verifies that when a previously
// saved expense account is inactive, the edit page still loads (200) and the
// inactive account does NOT appear in data-expense-accounts (the line-item
// category select only shows active accounts). Validation error surfaces at POST.
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

	// The inactive account must NOT appear in data-expense-accounts (only active accounts are pre-loaded).
	// We check by looking for the account name; code-based checks may false-positive if the account code
	// appears elsewhere (e.g. in the initial-lines JSON which echoes the raw DB ID).
	if strings.Contains(body, `"Retired Account"`) {
		t.Error("inactive account name must not appear in data-expense-accounts JSON")
	}
	// Form must still load without a server error.
	if strings.Contains(body, "Internal Server Error") || strings.Contains(body, "500") {
		t.Error("edit page must not crash when account is inactive")
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
		"expense_date":               {"2026-04-10"},
		"currency_code":              {"CAD"},
		"line_count":                 {"1"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", accID)},
		"line_description[0]":        {"Test"},
		"line_amount[0]":             {"10.00"},
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
	if !strings.Contains(body, "expense account") && !strings.Contains(body, "account") && !strings.Contains(body, "not valid") {
		t.Errorf("expected account error message, got body snippet: %.200s", body)
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
		"expense_date":               {"2026-04-10"},
		"currency_code":              {"CAD"},
		"line_count":                 {"1"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", otherAccID)},
		"line_description[0]":        {"Test"},
		"line_amount[0]":             {"10.00"},
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
		"expense_date":               {"2026-04-10"},
		"currency_code":              {"CAD"},
		"line_count":                 {"1"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", revAccID)},
		"line_description[0]":        {"Test"},
		"line_amount[0]":             {"10.00"},
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
}

// TestExpense_ErrorRerenderPreservesLineState verifies that when a POST fails
// (missing expense date), the submitted line account ID is preserved in
// data-initial-lines so the user does not lose their selection.
func TestExpense_ErrorRerenderPreservesLineState(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Rerender Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	accID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	app := testRouteApp(t, db)

	csrf := newCSRFToken(t)
	form := url.Values{
		"expense_date":               {""}, // empty — will trigger validation error
		"currency_code":              {"CAD"},
		"line_count":                 {"1"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", accID)},
		"line_description[0]":        {"Office supplies"},
		"line_amount[0]":             {"25.00"},
	}
	form.Set(CSRFFormField, csrf)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("form with missing date must not redirect")
	}
	body := readResponseBody(t, resp)

	// Line account ID must be preserved in data-initial-lines on error re-render.
	// Templ HTML-escapes attribute values: " → &#34;
	escapedID := fmt.Sprintf("&#34;expense_account_id&#34;:&#34;%d&#34;", accID)
	if !strings.Contains(body, escapedID) {
		t.Errorf("line account ID %d must be preserved in data-initial-lines on error re-render", accID)
	}
	// Date error must be shown.
	if !strings.Contains(body, "date") && !strings.Contains(body, "required") {
		t.Error("expected date error on re-render")
	}
}

// TestExpense_SmartPickerOnlyInputSurface verifies that each SmartPicker-controlled
// field (vendor, expense account, payment account) has exactly one visible input
// surface in the HTML — the SmartPicker's text input rendered by Alpine. No duplicate
// fallback selects or legacy mirror fields should be present for these fields.
func TestExpense_SmartPickerOnlyInputSurface(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "SP Single Surface Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	app := testRouteApp(t, db)

	resp := performRequest(t, app, "/expenses/new", rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)

	// No-JS fallback <select> elements for SmartPicker-controlled fields must be absent.
	// vendor and payment_account use SmartPicker; expense_account is a line-item select
	// managed by Alpine (no static name attribute).
	for _, fallbackSelect := range []string{
		`<select name="payment_account_id"`,
		`<select name="vendor_id"`,
		`<select name="expense_account_id"`,
	} {
		if strings.Contains(body, fallbackSelect) {
			t.Errorf("no-JS fallback select must be removed: %s found in HTML", fallbackSelect)
		}
	}

	// Per-line task options are loaded via data-tasks (Alpine JSON); verify the
	// attribute is present on the form element.
	if !strings.Contains(body, `data-tasks=`) {
		t.Error("expected data-tasks attribute for per-line task Alpine binding")
	}
}

// ── Payment Account eligibility tests ────────────────────────────────────────
// These tests verify that the backend enforces the payment-source account
// contract: only DetailBank, DetailCreditCard, and DetailOtherCurrentAsset
// accounts are accepted. Any other account type must be rejected at the
// service layer regardless of whether it belongs to the same company.

func expensePaymentForm(t *testing.T, expAccID, payAccID uint) url.Values {
	t.Helper()
	csrf := newCSRFToken(t)
	form := url.Values{
		"expense_date":               {"2026-04-10"},
		"currency_code":              {"CAD"},
		"payment_account_id":         {fmt.Sprintf("%d", payAccID)},
		"payment_method":             {"wire"},
		"line_count":                 {"1"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", expAccID)},
		"line_description[0]":        {"Payment account test"},
		"line_amount[0]":             {"20.00"},
	}
	form.Set(CSRFFormField, csrf)
	return form
}

func TestExpensePaymentAccount_BankAccepted(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "PA Bank Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	expAccID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	bankID := seedValidationAccount(t, db, companyID, "1010", models.RootAsset, models.DetailBank)
	app := testRouteApp(t, db)

	form := expensePaymentForm(t, expAccID, bankID)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: form.Get(CSRFFormField), Path: "/"},
	)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bank account must be accepted as payment source, got %d", resp.StatusCode)
	}
}

func TestExpensePaymentAccount_CreditCardAccepted(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "PA CC Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	expAccID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	ccID := seedValidationAccount(t, db, companyID, "2100", models.RootLiability, models.DetailCreditCard)
	app := testRouteApp(t, db)

	form := expensePaymentForm(t, expAccID, ccID)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: form.Get(CSRFFormField), Path: "/"},
	)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("credit card account must be accepted as payment source, got %d", resp.StatusCode)
	}
}

func TestExpensePaymentAccount_PettyCashAccepted(t *testing.T) {
	// DetailOtherCurrentAsset is the model's representation of petty cash /
	// liquid current assets ("cash" in the product requirement).
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "PA Cash Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	expAccID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	cashID := seedValidationAccount(t, db, companyID, "1050", models.RootAsset, models.DetailOtherCurrentAsset)
	app := testRouteApp(t, db)

	form := expensePaymentForm(t, expAccID, cashID)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: form.Get(CSRFFormField), Path: "/"},
	)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("petty-cash (other_current_asset) account must be accepted as payment source, got %d", resp.StatusCode)
	}
}

func TestExpensePaymentAccount_NonEligibleRejected(t *testing.T) {
	// An expense account (operating_expense) is a valid company-scoped account but
	// must NOT be accepted as a payment source — backend must reject it.
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "PA Ineligible Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	expAccID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	// Use a revenue account as the (ineligible) payment account.
	revenueID := seedValidationAccount(t, db, companyID, "4100", models.RootRevenue, models.DetailOperatingRevenue)
	app := testRouteApp(t, db)

	form := expensePaymentForm(t, expAccID, revenueID)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: form.Get(CSRFFormField), Path: "/"},
	)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("non-eligible account type (revenue) must not be accepted as payment source")
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "payment account") && !strings.Contains(body, "not valid") {
		t.Errorf("expected payment account error message, got body snippet: %.300s", body)
	}
}

func TestExpensePaymentAccount_APAccountRejected(t *testing.T) {
	// An A/P account (accounts_payable) would be semantically wrong as a payment
	// source; backend must reject it.
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "PA AP Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)
	expAccID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	apID := seedValidationAccount(t, db, companyID, "2000", models.RootLiability, models.DetailAccountsPayable)
	app := testRouteApp(t, db)

	form := expensePaymentForm(t, expAccID, apID)
	resp := performSecurityRequest(t, app, http.MethodPost, "/expenses",
		[]byte(form.Encode()), "application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: form.Get(CSRFFormField), Path: "/"},
	)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("A/P account must not be accepted as payment source")
	}
}

// seedExpenseForSP creates a minimal expense with one ExpenseLine for handler tests.
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
	line := models.ExpenseLine{
		ExpenseID:        exp.ID,
		LineOrder:        0,
		Description:      "SP test expense",
		Amount:           decimal.RequireFromString("50.00"),
		ExpenseAccountID: &accID,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return exp.ID
}
