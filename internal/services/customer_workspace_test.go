package services

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

func TestGetCustomerWorkspace_UsesSummaryTruthAndRecentInvoices(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	paymentTerm := models.PaymentTerm{
		CompanyID:   fixture.companyID,
		Code:        "N30",
		Description: "Net 30",
		IsDefault:   true,
		IsActive:    true,
	}
	if err := db.Create(&paymentTerm).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Customer{}).
		Where("id = ?", fixture.customerID).
		Updates(map[string]any{
			"default_payment_term_code": paymentTerm.Code,
			"email":                     "billing@example.com",
			"addr_street1":              "123 Main St",
			"addr_city":                 "Vancouver",
			"addr_province":             "BC",
			"addr_country":              "CA",
		}).Error; err != nil {
		t.Fatal(err)
	}

	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Workspace task", true)
	_ = seedDraftExpense(t, db, fixture, &task.ID, true, "Workspace expense", "15.00")
	_ = seedDraftBillLine(t, db, fixture, &task.ID, true, "Workspace line", "25.00")
	_ = seedDraftTask(t, db, fixture.companyID, fixture.otherCustomerID, models.TaskStatusCompleted, "Other customer task", true)

	invoiceA := models.Invoice{
		CompanyID:     fixture.companyID,
		InvoiceNumber: "INV-CUST-001",
		CustomerID:    fixture.customerID,
		InvoiceDate:   time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusIssued,
		Amount:        decimal.RequireFromString("250.00"),
		BalanceDue:    decimal.RequireFromString("250.00"),
		CurrencyCode:  "CAD",
		DueDate:       datePtr(t, "2026-04-01"),
	}
	invoiceB := models.Invoice{
		CompanyID:     fixture.companyID,
		InvoiceNumber: "INV-CUST-002",
		CustomerID:    fixture.customerID,
		InvoiceDate:   time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusPaid,
		Amount:        decimal.RequireFromString("125.00"),
		BalanceDue:    decimal.Zero,
		CurrencyCode:  "CAD",
	}
	invoiceC := models.Invoice{
		CompanyID:     fixture.companyID,
		InvoiceNumber: "INV-CUST-003",
		CustomerID:    fixture.customerID,
		InvoiceDate:   time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusPartiallyPaid,
		Amount:        decimal.RequireFromString("80.00"),
		BalanceDue:    decimal.RequireFromString("30.00"),
		CurrencyCode:  "CAD",
		// Due date rolling-future so the "exactly 1 overdue invoice"
		// assertion below stays stable as wall-clock time advances.
		// Pre-IN.9 this was a fixed "2026-04-20" literal.
		DueDate: futureDueDate(1),
	}
	otherInvoice := models.Invoice{
		CompanyID:     fixture.companyID,
		InvoiceNumber: "INV-OTHER-001",
		CustomerID:    fixture.otherCustomerID,
		InvoiceDate:   time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusDraft,
		Amount:        decimal.RequireFromString("999.00"),
		BalanceDue:    decimal.RequireFromString("999.00"),
		CurrencyCode:  "CAD",
	}
	for _, inv := range []*models.Invoice{&invoiceA, &invoiceB, &invoiceC, &otherInvoice} {
		if err := db.Create(inv).Error; err != nil {
			t.Fatal(err)
		}
	}

	workspace, err := GetCustomerWorkspace(db, fixture.companyID, fixture.customerID)
	if err != nil {
		t.Fatalf("GetCustomerWorkspace failed: %v", err)
	}

	if workspace.Customer.ID != fixture.customerID {
		t.Fatalf("expected customer %d, got %d", fixture.customerID, workspace.Customer.ID)
	}
	if workspace.DefaultPaymentTermLabel != paymentTerm.DropdownLabel() {
		t.Fatalf("expected payment term label %q, got %q", paymentTerm.DropdownLabel(), workspace.DefaultPaymentTermLabel)
	}
	if got := currencyAmount(workspace.BillableSummary.UnbilledTaskLabor, "CAD"); !got.Equal(decimal.RequireFromString("300.00")) {
		t.Fatalf("expected labor total 300.00 CAD, got %s", got)
	}
	if got := currencyAmount(workspace.BillableSummary.UnbilledBillableExpense, "CAD"); !got.Equal(decimal.RequireFromString("40.00")) {
		t.Fatalf("expected expense total 40.00 CAD, got %s", got)
	}
	if got := currencyAmount(workspace.BillableSummary.TotalUnbilled, "CAD"); !got.Equal(decimal.RequireFromString("340.00")) {
		t.Fatalf("expected total unbilled 340.00 CAD, got %s", got)
	}
	if workspace.BillableSummary.LastBillableWorkDate == nil || !sameCalendarDate(*workspace.BillableSummary.LastBillableWorkDate, mustDate(t, "2026-04-06")) {
		t.Fatalf("expected last billable work date 2026-04-06, got %+v", workspace.BillableSummary.LastBillableWorkDate)
	}
	if got := currencyAmount(workspace.ARSummary.OutstandingTotals, "CAD"); !got.Equal(decimal.RequireFromString("280.00")) {
		t.Fatalf("expected outstanding AR 280.00 CAD, got %s", got)
	}
	if workspace.ARSummary.OutstandingInvoiceCount != 2 {
		t.Fatalf("expected 2 outstanding invoices, got %d", workspace.ARSummary.OutstandingInvoiceCount)
	}
	if workspace.ARSummary.OverdueInvoiceCount != 1 {
		t.Fatalf("expected 1 overdue invoice, got %d", workspace.ARSummary.OverdueInvoiceCount)
	}
	if len(workspace.OutstandingInvoices) != 2 {
		t.Fatalf("expected 2 outstanding invoices in list, got %d", len(workspace.OutstandingInvoices))
	}
	if workspace.OutstandingInvoices[0].InvoiceNumber != "INV-CUST-003" || workspace.OutstandingInvoices[1].InvoiceNumber != "INV-CUST-001" {
		t.Fatalf("unexpected outstanding invoice order: %+v", workspace.OutstandingInvoices)
	}
	if got := BuildInvoicePaymentVisibility(workspace.OutstandingInvoices[0]); got.State != InvoicePaymentStatePartiallyPaid || !got.PaidAmount.Equal(decimal.RequireFromString("50.00")) {
		t.Fatalf("expected partial payment visibility on outstanding invoice, got %+v", got)
	}
	if got := BuildInvoicePaymentVisibility(workspace.OutstandingInvoices[1]); got.State != InvoicePaymentStateUnpaid || !got.BalanceDue.Equal(decimal.RequireFromString("250.00")) {
		t.Fatalf("expected unpaid visibility on overdue invoice, got %+v", got)
	}
	if len(workspace.RecentInvoices) != 3 {
		t.Fatalf("expected 3 recent invoices, got %d", len(workspace.RecentInvoices))
	}
	if workspace.MostRecentInvoice == nil || workspace.MostRecentInvoice.InvoiceNumber != "INV-CUST-002" {
		t.Fatalf("expected most recent invoice INV-CUST-002, got %+v", workspace.MostRecentInvoice)
	}
	if got := BuildInvoicePaymentVisibility(*workspace.MostRecentInvoice); got.State != InvoicePaymentStatePaid || !got.PaidAmount.Equal(decimal.RequireFromString("125.00")) {
		t.Fatalf("expected paid visibility on most recent invoice, got %+v", got)
	}
	for _, inv := range workspace.RecentInvoices {
		if inv.CustomerID != fixture.customerID {
			t.Fatalf("expected customer-scoped invoice results, got customer %d", inv.CustomerID)
		}
	}
}

func TestGetCustomerWorkspace_NotFound(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	_, err := GetCustomerWorkspace(db, fixture.companyID, 999999)
	if !errors.Is(err, ErrCustomerWorkspaceNotFound) {
		t.Fatalf("expected ErrCustomerWorkspaceNotFound, got %v", err)
	}
}

func datePtr(t *testing.T, value string) *time.Time {
	t.Helper()
	d := mustDate(t, value)
	return &d
}

// futureDueDate returns a *time.Time `monthsOut` months from today
// at UTC midnight. Used by tests that need a guaranteed-future due
// date for "not-yet-overdue" invoice fixtures so the test's
// overdue-count assertions stay stable as wall-clock time advances.
func futureDueDate(monthsOut int) *time.Time {
	today := time.Now().UTC()
	d := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, monthsOut, 0)
	return &d
}
