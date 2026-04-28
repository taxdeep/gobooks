package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

func TestTaskFormCustomerSmartPickerIntegration(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Task Customer Picker Co")
	otherCompanyID := seedCompany(t, db, "Task Customer Picker Other Co")
	user, rawToken := seedUserSession(t, db, &companyID)
	seedMembership(t, db, user.ID, companyID)

	validCustomerID := seedValidationCustomer(t, db, companyID, "Picker Customer")
	crossCompanyCustomerID := seedValidationCustomer(t, db, otherCompanyID, "Other Picker Customer")

	app := testRouteApp(t, db)

	newResp := performRequest(t, app, "/tasks/new", rawToken)
	if newResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, newResp.StatusCode)
	}
	newBody := readResponseBody(t, newResp)
	for _, want := range []string{
		`data-entity="customer"`,
		`data-context="task_form_customer"`,
		`data-field-name="customer_id"`,
		`name="customer_id"`,
	} {
		if !strings.Contains(newBody, want) {
			t.Fatalf("expected new task customer SmartPicker HTML to contain %q, got %q", want, newBody)
		}
	}
	if strings.Contains(newBody, `<input type="hidden" name="customer_id"`) {
		t.Fatalf("interactive customer SmartPicker hidden input must not have a static customer_id name, got %q", newBody)
	}

	postTask := func(title string, customerID uint) *http.Response {
		t.Helper()
		csrf := newCSRFToken(t)
		form := url.Values{
			"customer_id":   {fmt.Sprintf("%d", customerID)},
			"title":         {title},
			"task_date":     {"2026-04-10"},
			"quantity":      {"1.00"},
			"unit_type":     {models.TaskUnitTypeHour},
			"rate":          {"110.00"},
			"currency_code": {"CAD"},
			"is_billable":   {"1"},
		}
		form.Set(CSRFFormField, csrf)
		return performSecurityRequest(
			t,
			app,
			http.MethodPost,
			"/tasks",
			[]byte(form.Encode()),
			"application/x-www-form-urlencoded",
			&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
			&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
		)
	}

	createResp := postTask("Customer picker task", validCustomerID)
	if createResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, createResp.StatusCode)
	}
	var created models.Task
	if err := db.Where("company_id = ? AND title = ?", companyID, "Customer picker task").First(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.CustomerID != validCustomerID {
		t.Fatalf("expected customer %d, got %d", validCustomerID, created.CustomerID)
	}

	editResp := performRequest(t, app, fmt.Sprintf("/tasks/%d/edit", created.ID), rawToken)
	if editResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, editResp.StatusCode)
	}
	editBody := readResponseBody(t, editResp)
	if !strings.Contains(editBody, fmt.Sprintf(`data-value="%d"`, validCustomerID)) {
		t.Fatalf("expected edit page data-value to rehydrate customer %d, got %q", validCustomerID, editBody)
	}
	if !strings.Contains(editBody, `data-selected-label="Picker Customer"`) {
		t.Fatalf("expected edit page data-selected-label to rehydrate customer, got %q", editBody)
	}

	crossResp := postTask("Reject cross-company customer", crossCompanyCustomerID)
	if crossResp.StatusCode != http.StatusOK {
		t.Fatalf("expected form re-render %d, got %d", http.StatusOK, crossResp.StatusCode)
	}
	crossBody := readResponseBody(t, crossResp)
	if !strings.Contains(crossBody, services.ErrTaskCustomerInvalid.Error()) {
		t.Fatalf("expected customer validation error, got %q", crossBody)
	}
	if !strings.Contains(crossBody, `data-value=""`) {
		t.Fatalf("expected rejected customer to clear SmartPicker data-value, got %q", crossBody)
	}
	if strings.Contains(crossBody, fmt.Sprintf(`data-value="%d"`, crossCompanyCustomerID)) {
		t.Fatalf("illegal customer ID %d must not be retained in data-value, got %q", crossCompanyCustomerID, crossBody)
	}

	csrf := newCSRFToken(t)
	badTitleForm := url.Values{
		"customer_id":   {fmt.Sprintf("%d", validCustomerID)},
		"title":         {""},
		"task_date":     {"2026-04-10"},
		"quantity":      {"1.00"},
		"unit_type":     {models.TaskUnitTypeHour},
		"rate":          {"110.00"},
		"currency_code": {"CAD"},
		"is_billable":   {"1"},
	}
	badTitleForm.Set(CSRFFormField, csrf)
	badTitleResp := performSecurityRequest(
		t,
		app,
		http.MethodPost,
		"/tasks",
		[]byte(badTitleForm.Encode()),
		"application/x-www-form-urlencoded",
		&http.Cookie{Name: SessionCookieName, Value: rawToken, Path: "/"},
		&http.Cookie{Name: CSRFCookieName, Value: csrf, Path: "/"},
	)
	if badTitleResp.StatusCode != http.StatusOK {
		t.Fatalf("expected other-field error re-render %d, got %d", http.StatusOK, badTitleResp.StatusCode)
	}
	badTitleBody := readResponseBody(t, badTitleResp)
	if !strings.Contains(badTitleBody, fmt.Sprintf(`data-value="%d"`, validCustomerID)) {
		t.Fatalf("valid customer data-value must be preserved on other-field error, got %q", badTitleBody)
	}
	if !strings.Contains(badTitleBody, `data-selected-label="Picker Customer"`) {
		t.Fatalf("valid customer label must be preserved on other-field error, got %q", badTitleBody)
	}
}
