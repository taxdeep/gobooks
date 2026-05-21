package services

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

func TestGenerateMonthlyTaskReportRejectsInvalidPeriod(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task Report Bounds Co")

	tests := []struct {
		name      string
		companyID uint
		year      int
		month     int
		want      error
	}{
		{name: "missing company", companyID: 0, year: 2026, month: 4, want: ErrTaskReportCompanyRequired},
		{name: "year too early", companyID: companyID, year: 0, month: 4, want: ErrTaskReportYearInvalid},
		{name: "month zero", companyID: companyID, year: 2026, month: 0, want: ErrTaskReportMonthInvalid},
		{name: "month thirteen", companyID: companyID, year: 2026, month: 13, want: ErrTaskReportMonthInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := GenerateMonthlyTaskReport(db, tt.companyID, tt.year, tt.month); !errors.Is(err, tt.want) {
				t.Fatalf("GenerateMonthlyTaskReport error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestListTasksByMonthRejectsInvalidPeriod(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task List Bounds Co")

	if _, err := ListTasksByMonth(db, companyID, 2026, 13); !errors.Is(err, ErrTaskReportMonthInvalid) {
		t.Fatalf("ListTasksByMonth error = %v, want %v", err, ErrTaskReportMonthInvalid)
	}
}

func TestListTasksByMonthUsesExclusiveNextMonthBoundary(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task Month Boundary Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")

	aprilStart := baseTaskInput(companyID, customerID)
	aprilStart.Title = "April start"
	aprilStart.TaskDate = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	createTaskForTest(t, db, aprilStart)

	aprilEnd := baseTaskInput(companyID, customerID)
	aprilEnd.Title = "April end"
	aprilEnd.TaskDate = time.Date(2026, 4, 30, 23, 59, 59, int(500*time.Millisecond), time.UTC)
	createTaskForTest(t, db, aprilEnd)

	mayStart := baseTaskInput(companyID, customerID)
	mayStart.Title = "May start"
	mayStart.TaskDate = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	createTaskForTest(t, db, mayStart)

	tasks, err := ListTasksByMonth(db, companyID, 2026, 4)
	if err != nil {
		t.Fatalf("ListTasksByMonth: %v", err)
	}

	if len(tasks) != 2 {
		t.Fatalf("expected 2 April tasks, got %d", len(tasks))
	}
	for _, task := range tasks {
		if task.Title == "May start" {
			t.Fatal("did not expect May start task in April result")
		}
	}
}

func TestGenerateMonthlyTaskReportDoesNotJoinCrossCompanyCustomerNames(t *testing.T) {
	db := taskServiceDB(t)
	companyA := seedTaskServiceCompany(t, db, "Report Co A")
	companyB := seedTaskServiceCompany(t, db, "Report Co B")
	crossCompanyCustomerID := seedTaskServiceCustomer(t, db, companyB, "Other Company Customer")

	task := models.Task{
		CompanyID:    companyA,
		CustomerID:   crossCompanyCustomerID,
		Title:        "Corrupted customer reference",
		TaskDate:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Quantity:     decimal.NewFromInt(1),
		UnitType:     models.TaskUnitTypeHour,
		Rate:         decimal.NewFromInt(100),
		CurrencyCode: "CAD",
		IsBillable:   true,
		Status:       models.TaskStatusCompleted,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("seed corrupted task: %v", err)
	}

	summary, err := GenerateMonthlyTaskReport(db, companyA, 2026, 4)
	if err != nil {
		t.Fatalf("GenerateMonthlyTaskReport: %v", err)
	}
	if summary.TotalTasks != 1 {
		t.Fatalf("expected 1 task, got %d", summary.TotalTasks)
	}
	for _, customerSummary := range summary.ByCustomer {
		if customerSummary.CustomerName == "Other Company Customer" {
			t.Fatal("cross-company customer name leaked into task report")
		}
	}
}

func TestGenerateMonthlyTaskReportUsesTaskLinesWhenPresent(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Report Multi-Line Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")
	accountID := seedTaskRevenueAccount(t, db, companyID, "4000")
	firstItemID := seedTaskServiceItem(t, db, companyID, accountID, "Discovery", models.ProductServiceTypeService, true)
	secondItemID := seedTaskServiceItem(t, db, companyID, accountID, "Build", models.ProductServiceTypeService, true)

	input := baseTaskInput(companyID, customerID)
	input.Title = "Multi-line report task"
	input.TaskDate = time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	input.Lines = []TaskLineInput{
		{
			ProductServiceID: &firstItemID,
			Description:      "Discovery",
			Quantity:         decimal.RequireFromString("2.00"),
			Rate:             decimal.RequireFromString("100.00"),
		},
		{
			ProductServiceID: &secondItemID,
			Description:      "Build",
			Quantity:         decimal.RequireFromString("3.00"),
			Rate:             decimal.RequireFromString("50.00"),
		},
	}
	createTaskForTest(t, db, input)

	summary, err := GenerateMonthlyTaskReport(db, companyID, 2026, 4)
	if err != nil {
		t.Fatalf("GenerateMonthlyTaskReport: %v", err)
	}
	if !summary.TotalQuantity.Equal(decimal.RequireFromString("5.00")) {
		t.Fatalf("expected task-line quantity 5.00, got %s", summary.TotalQuantity)
	}
	if !summary.TotalBillableAmount.Equal(decimal.RequireFromString("350.00")) {
		t.Fatalf("expected task-line amount 350.00, got %s", summary.TotalBillableAmount)
	}
	if len(summary.ByCustomer) != 1 {
		t.Fatalf("expected one customer summary, got %+v", summary.ByCustomer)
	}
	for _, customerSummary := range summary.ByCustomer {
		if !customerSummary.Quantity.Equal(decimal.RequireFromString("5.00")) || !customerSummary.Amount.Equal(decimal.RequireFromString("350.00")) {
			t.Fatalf("unexpected customer summary: %+v", customerSummary)
		}
	}
}
