package services

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
)

func TestListUnbilledWork_CurrentSourcesOnly(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	readyTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Ready task", true)
	readyExpense := seedDraftExpense(t, db, fixture, &readyTask.ID, true, "Ready expense", "45.00")
	readyBillLine := seedDraftBillLine(t, db, fixture, &readyTask.ID, true, "Ready bill line", "30.00")

	activeTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Already drafted", true)
	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{activeTask.ID},
		Actor:      "tester",
	}); err != nil {
		t.Fatalf("generate active draft: %v", err)
	}

	releasedTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Released task", true)
	releasedDraft, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{releasedTask.ID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatalf("generate released draft: %v", err)
	}
	if err := DeleteInvoice(db, fixture.companyID, releasedDraft.InvoiceID, "tester", nil); err != nil {
		t.Fatalf("delete released draft: %v", err)
	}

	report, err := ListUnbilledWork(db, fixture.companyID, &fixture.customerID)
	if err != nil {
		t.Fatalf("ListUnbilledWork failed: %v", err)
	}

	if !hasTask(report.Tasks, readyTask.ID) {
		t.Fatalf("expected ready task %d in unbilled report", readyTask.ID)
	}
	if !hasTask(report.Tasks, releasedTask.ID) {
		t.Fatalf("expected released task %d back in unbilled report", releasedTask.ID)
	}
	if hasTask(report.Tasks, activeTask.ID) {
		t.Fatalf("did not expect active drafted task %d in unbilled report", activeTask.ID)
	}
	if !hasExpense(report.Expenses, readyExpense.ID) {
		t.Fatalf("expected ready expense %d in unbilled report", readyExpense.ID)
	}
	if !hasBillLine(report.BillLines, readyBillLine.ID) {
		t.Fatalf("expected ready bill line %d in unbilled report", readyBillLine.ID)
	}
	if got := currencyAmount(report.TaskLaborTotals, "CAD"); !got.Equal(decimal.RequireFromString("600.00")) {
		t.Fatalf("expected task labor total 600.00 CAD, got %s", got)
	}
	if got := currencyAmount(report.BillableExpenseTotals, "CAD"); !got.Equal(decimal.RequireFromString("75.00")) {
		t.Fatalf("expected billable expense total 75.00 CAD, got %s", got)
	}
	if got := currencyAmount(report.TotalUnbilledTotals, "CAD"); !got.Equal(decimal.RequireFromString("675.00")) {
		t.Fatalf("expected total unbilled 675.00 CAD, got %s", got)
	}
}

func TestGetTaskBillingTrace_DistinguishesActiveAndHistorical(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Trace task", true)
	expense := seedDraftExpense(t, db, fixture, &task.ID, true, "Trace expense", "20.00")
	billLine := seedDraftBillLine(t, db, fixture, &task.ID, true, "Trace bill line", "10.00")

	firstDraft, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{task.ID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatalf("generate first draft: %v", err)
	}
	if err := DeleteInvoice(db, fixture.companyID, firstDraft.InvoiceID, "tester", nil); err != nil {
		t.Fatalf("delete first draft: %v", err)
	}

	secondDraft, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:   fixture.companyID,
		CustomerID:  fixture.customerID,
		TaskIDs:     []uint{task.ID},
		ExpenseIDs:  []uint{expense.ID},
		BillLineIDs: []uint{billLine.ID},
		Actor:       "tester",
	})
	if err != nil {
		t.Fatalf("generate second draft: %v", err)
	}

	trace, err := GetTaskBillingTrace(db, fixture.companyID, task.ID)
	if err != nil {
		t.Fatalf("GetTaskBillingTrace failed: %v", err)
	}

	if trace.StateLabel != "In draft invoice" {
		t.Fatalf("expected in-draft state, got %q", trace.StateLabel)
	}
	if !trace.HasAnyActiveLinkage {
		t.Fatalf("expected active linkage")
	}
	if !trace.HasHistoricalLinkage {
		t.Fatalf("expected historical linkage")
	}
	if trace.CurrentTaskInvoiceID == nil || *trace.CurrentTaskInvoiceID != secondDraft.InvoiceID {
		t.Fatalf("expected current task invoice %d, got %+v", secondDraft.InvoiceID, trace.CurrentTaskInvoiceID)
	}
	if len(trace.TaskHistory) != 2 {
		t.Fatalf("expected 2 task history rows, got %d", len(trace.TaskHistory))
	}
	if !trace.TaskHistory[0].IsActive {
		t.Fatalf("expected newest task history row to be active")
	}
	if trace.TaskHistory[1].IsActive {
		t.Fatalf("expected older task history row to be historical")
	}
	if trace.TaskHistory[1].InvoiceID != nil {
		t.Fatalf("expected released draft history to clear invoice reference, got %+v", trace.TaskHistory[1].InvoiceID)
	}
	if len(trace.ExpenseHistory) != 1 || !trace.ExpenseHistory[0].IsActive {
		t.Fatalf("expected one active expense history row, got %+v", trace.ExpenseHistory)
	}
	if len(trace.BillLineHistory) != 1 || !trace.BillLineHistory[0].IsActive {
		t.Fatalf("expected one active bill line history row, got %+v", trace.BillLineHistory)
	}
}

func TestListCustomerBillableSummaries_AggregatesByCustomer(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	customerOneTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Customer one task", true)
	_ = seedDraftExpense(t, db, fixture, &customerOneTask.ID, true, "Customer one expense", "15.00")
	_ = seedDraftBillLine(t, db, fixture, &customerOneTask.ID, true, "Customer one line", "25.00")

	customerTwoTask := seedDraftTask(t, db, fixture.companyID, fixture.otherCustomerID, models.TaskStatusCompleted, "Customer two task", true)
	_ = seedDraftExpense(t, db, fixture, &customerTwoTask.ID, false, "Customer two internal", "9.00")
	_ = seedDraftBillLine(t, db, fixture, &customerTwoTask.ID, true, "Customer two line", "10.00")

	summaries, err := ListCustomerBillableSummaries(db, fixture.companyID)
	if err != nil {
		t.Fatalf("ListCustomerBillableSummaries failed: %v", err)
	}

	one := CustomerSummaryOrZero(summaries, fixture.customerID)
	if got := currencyAmount(one.UnbilledTaskLabor, "CAD"); !got.Equal(decimal.RequireFromString("300.00")) {
		t.Fatalf("expected customer one labor 300.00 CAD, got %s", got)
	}
	if got := currencyAmount(one.UnbilledBillableExpense, "CAD"); !got.Equal(decimal.RequireFromString("40.00")) {
		t.Fatalf("expected customer one expense 40.00 CAD, got %s", got)
	}
	if got := currencyAmount(one.TotalUnbilled, "CAD"); !got.Equal(decimal.RequireFromString("340.00")) {
		t.Fatalf("expected customer one total 340.00 CAD, got %s", got)
	}

	two := CustomerSummaryOrZero(summaries, fixture.otherCustomerID)
	if got := currencyAmount(two.UnbilledTaskLabor, "CAD"); !got.Equal(decimal.RequireFromString("300.00")) {
		t.Fatalf("expected customer two labor 300.00 CAD, got %s", got)
	}
	if got := currencyAmount(two.UnbilledBillableExpense, "CAD"); !got.Equal(decimal.RequireFromString("10.00")) {
		t.Fatalf("expected customer two expense 10.00 CAD, got %s", got)
	}
	if got := currencyAmount(two.TotalUnbilled, "CAD"); !got.Equal(decimal.RequireFromString("310.00")) {
		t.Fatalf("expected customer two total 310.00 CAD, got %s", got)
	}
	if two.LastBillableWorkDate == nil || !sameCalendarDate(*two.LastBillableWorkDate, mustDate(t, "2026-04-06")) {
		t.Fatalf("expected customer two last billable date from bill line, got %+v", two.LastBillableWorkDate)
	}
}

func hasTask(tasks []models.Task, taskID uint) bool {
	for _, task := range tasks {
		if task.ID == taskID {
			return true
		}
	}
	return false
}

func hasExpense(expenses []models.Expense, expenseID uint) bool {
	for _, exp := range expenses {
		if exp.ID == expenseID {
			return true
		}
	}
	return false
}

func hasBillLine(lines []models.BillLine, billLineID uint) bool {
	for _, line := range lines {
		if line.ID == billLineID {
			return true
		}
	}
	return false
}

func currencyAmount(totals []CurrencyTotal, currency string) decimal.Decimal {
	for _, total := range totals {
		if total.CurrencyCode == currency {
			return total.Amount
		}
	}
	return decimal.Zero
}

func mustDate(t *testing.T, value string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func sameCalendarDate(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
