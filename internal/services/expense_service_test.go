package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

func testExpenseServiceDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:expense_service_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.Task{},
		&models.Expense{},
		&models.ExpenseLine{},
		&models.Bill{},
		&models.BillLine{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedExpenseCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	company := models.Company{Name: "Expense Co", FiscalYearEnd: "12-31", BaseCurrencyCode: "CAD"}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	return company.ID
}

func seedExpenseCustomer(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()
	customer := models.Customer{CompanyID: companyID, Name: name}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	return customer.ID
}

func seedExpenseVendor(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()
	vendor := models.Vendor{CompanyID: companyID, Name: name}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	return vendor.ID
}

func seedExpenseAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	account := models.Account{CompanyID: companyID, Code: "6100", Name: "Office Supplies", RootAccountType: models.RootExpense, DetailAccountType: models.DetailOfficeExpense, IsActive: true}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	return account.ID
}

func seedTaskForExpense(t *testing.T, db *gorm.DB, companyID, customerID uint, status models.TaskStatus, title string) uint {
	t.Helper()
	task := models.Task{
		CompanyID:    companyID,
		CustomerID:   customerID,
		Title:        title,
		TaskDate:     time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		Quantity:     decimal.RequireFromString("1"),
		UnitType:     models.TaskUnitTypeHour,
		Rate:         decimal.RequireFromString("100"),
		CurrencyCode: "CAD",
		IsBillable:   true,
		Status:       status,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	return task.ID
}

func TestNormalizeTaskCostLinkage_ValidatesStatusAndCustomer(t *testing.T) {
	db := testExpenseServiceDB(t)
	companyID := seedExpenseCompany(t, db)
	customerA := seedExpenseCustomer(t, db, companyID, "Customer A")
	customerB := seedExpenseCustomer(t, db, companyID, "Customer B")
	openTaskID := seedTaskForExpense(t, db, companyID, customerA, models.TaskStatusOpen, "Open task")
	cancelledTaskID := seedTaskForExpense(t, db, companyID, customerA, models.TaskStatusCancelled, "Cancelled task")
	invoicedTaskID := seedTaskForExpense(t, db, companyID, customerA, models.TaskStatusInvoiced, "Invoiced task")

	linkage, err := NormalizeTaskCostLinkage(db, TaskCostLinkageInput{
		CompanyID:  companyID,
		TaskID:     &openTaskID,
		IsBillable: true,
	})
	if err != nil {
		t.Fatalf("expected open task to validate, got %v", err)
	}
	if linkage.BillableCustomerID == nil || *linkage.BillableCustomerID != customerA {
		t.Fatalf("expected billable customer %d, got %+v", customerA, linkage.BillableCustomerID)
	}
	if linkage.ReinvoiceStatus != models.ReinvoiceStatusUninvoiced {
		t.Fatalf("expected uninvoiced status, got %q", linkage.ReinvoiceStatus)
	}

	if _, err := NormalizeTaskCostLinkage(db, TaskCostLinkageInput{
		CompanyID:          companyID,
		TaskID:             &openTaskID,
		BillableCustomerID: &customerB,
		IsBillable:         true,
	}); err != ErrTaskBillableCustomerMismatch {
		t.Fatalf("expected customer mismatch, got %v", err)
	}

	if _, err := NormalizeTaskCostLinkage(db, TaskCostLinkageInput{
		CompanyID:  companyID,
		TaskID:     &cancelledTaskID,
		IsBillable: true,
	}); err != ErrTaskLinkageTaskStatusInvalid {
		t.Fatalf("expected cancelled task error, got %v", err)
	}
	if _, err := NormalizeTaskCostLinkage(db, TaskCostLinkageInput{
		CompanyID:  companyID,
		TaskID:     &invoicedTaskID,
		IsBillable: true,
	}); err != ErrTaskLinkageTaskStatusInvalid {
		t.Fatalf("expected invoiced task error, got %v", err)
	}
}

func TestCreateExpense_AutoSetsReinvoiceStatusAndLeavesOrdinaryPathUntouched(t *testing.T) {
	db := testExpenseServiceDB(t)
	companyID := seedExpenseCompany(t, db)
	customerID := seedExpenseCustomer(t, db, companyID, "Customer A")
	vendorID := seedExpenseVendor(t, db, companyID, "Vendor A")
	expenseAccountID := seedExpenseAccount(t, db, companyID)
	taskID := seedTaskForExpense(t, db, companyID, customerID, models.TaskStatusCompleted, "Completed task")

	billable, err := CreateExpense(db, ExpenseInput{
		CompanyID:        companyID,
		TaskID:           &taskID,
		IsBillable:       true,
		ExpenseDate:      time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		Description:      "Task-linked hotel",
		Amount:           decimal.RequireFromString("75.00"),
		CurrencyCode:     "CAD",
		VendorID:         &vendorID,
		ExpenseAccountID: &expenseAccountID,
	})
	if err != nil {
		t.Fatalf("create billable expense: %v", err)
	}
	if billable.BillableCustomerID == nil || *billable.BillableCustomerID != customerID {
		t.Fatalf("expected billable customer %d, got %+v", customerID, billable.BillableCustomerID)
	}
	if billable.ReinvoiceStatus != models.ReinvoiceStatusUninvoiced {
		t.Fatalf("expected uninvoiced, got %q", billable.ReinvoiceStatus)
	}

	nonBillable, err := CreateExpense(db, ExpenseInput{
		CompanyID:        companyID,
		TaskID:           &taskID,
		IsBillable:       false,
		ExpenseDate:      time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Description:      "Internal task cost",
		Amount:           decimal.RequireFromString("20.00"),
		CurrencyCode:     "CAD",
		ExpenseAccountID: &expenseAccountID,
	})
	if err != nil {
		t.Fatalf("create non-billable expense: %v", err)
	}
	if nonBillable.ReinvoiceStatus != models.ReinvoiceStatusNone {
		t.Fatalf("expected empty reinvoice status, got %q", nonBillable.ReinvoiceStatus)
	}
	if nonBillable.BillableCustomerID == nil || *nonBillable.BillableCustomerID != customerID {
		t.Fatalf("expected task customer to remain attached, got %+v", nonBillable.BillableCustomerID)
	}

	ordinary, err := CreateExpense(db, ExpenseInput{
		CompanyID:        companyID,
		IsBillable:       true,
		ExpenseDate:      time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		Description:      "Standalone office purchase",
		Amount:           decimal.RequireFromString("15.00"),
		CurrencyCode:     "CAD",
		ExpenseAccountID: &expenseAccountID,
	})
	if err != nil {
		t.Fatalf("create standalone expense: %v", err)
	}
	if ordinary.TaskID != nil {
		t.Fatalf("expected no task linkage, got %+v", ordinary.TaskID)
	}
	if ordinary.BillableCustomerID != nil {
		t.Fatalf("expected no billable customer, got %+v", ordinary.BillableCustomerID)
	}
	if ordinary.ReinvoiceStatus != models.ReinvoiceStatusNone {
		t.Fatalf("expected no reinvoice status, got %q", ordinary.ReinvoiceStatus)
	}
}

func TestListSelectableTasks_OnlyReturnsOpenAndCompleted(t *testing.T) {
	db := testExpenseServiceDB(t)
	companyID := seedExpenseCompany(t, db)
	customerID := seedExpenseCustomer(t, db, companyID, "Customer A")
	seedTaskForExpense(t, db, companyID, customerID, models.TaskStatusOpen, "Open")
	seedTaskForExpense(t, db, companyID, customerID, models.TaskStatusCompleted, "Completed")
	seedTaskForExpense(t, db, companyID, customerID, models.TaskStatusCancelled, "Cancelled")
	seedTaskForExpense(t, db, companyID, customerID, models.TaskStatusInvoiced, "Invoiced")

	tasks, err := ListSelectableTasks(db, companyID)
	if err != nil {
		t.Fatalf("list selectable tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 selectable tasks, got %d", len(tasks))
	}
	for _, task := range tasks {
		if task.Status != models.TaskStatusOpen && task.Status != models.TaskStatusCompleted {
			t.Fatalf("unexpected task status %q returned", task.Status)
		}
	}
}
