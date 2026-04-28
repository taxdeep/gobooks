// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func taskServiceDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:task_service_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.ProductService{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Task{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedTaskServiceCompany(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()

	row := models.Company{
		Name:                    name,
		EntityType:              models.EntityTypeIncorporated,
		BusinessType:            models.BusinessTypeRetail,
		Industry:                models.IndustryRetail,
		IncorporatedDate:        "2024-01-01",
		FiscalYearEnd:           "12-31",
		BusinessNumber:          "123456789",
		AddressLine:             "123 Main",
		City:                    "Vancouver",
		Province:                "BC",
		PostalCode:              "V6B1A1",
		Country:                 "CA",
		AccountCodeLength:       4,
		AccountCodeLengthLocked: true,
		IsActive:                true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func seedTaskServiceCustomer(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()

	row := models.Customer{CompanyID: companyID, Name: name}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func baseTaskInput(companyID, customerID uint) TaskInput {
	return TaskInput{
		CompanyID:    companyID,
		CustomerID:   customerID,
		Title:        "April consulting",
		TaskDate:     time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC),
		Quantity:     decimal.RequireFromString("2.50"),
		UnitType:     models.TaskUnitTypeHour,
		Rate:         decimal.RequireFromString("150.00"),
		CurrencyCode: "USD",
		IsBillable:   true,
		Notes:        "Initial notes",
	}
}

func createTaskForTest(t *testing.T, db *gorm.DB, in TaskInput) *models.Task {
	t.Helper()
	task, err := CreateTask(db, in)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func TestCreateTaskRequiresCustomer(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task Service Co")

	input := baseTaskInput(companyID, 0)
	_, err := CreateTask(db, input)
	if !errors.Is(err, ErrTaskCustomerRequired) {
		t.Fatalf("expected ErrTaskCustomerRequired, got %v", err)
	}
}

func TestCreateTaskStoresSnapshotFields(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task Snapshot Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")

	task, err := CreateTask(db, baseTaskInput(companyID, customerID))
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if task.CustomerID != customerID {
		t.Fatalf("expected customer_id %d, got %d", customerID, task.CustomerID)
	}
	if task.Status != models.TaskStatusOpen {
		t.Fatalf("expected status %q, got %q", models.TaskStatusOpen, task.Status)
	}
	if task.UnitType != models.TaskUnitTypeHour {
		t.Fatalf("expected unit_type %q, got %q", models.TaskUnitTypeHour, task.UnitType)
	}
	if task.CurrencyCode != "USD" {
		t.Fatalf("expected currency USD, got %q", task.CurrencyCode)
	}
	if !task.Quantity.Equal(decimal.RequireFromString("2.50")) {
		t.Fatalf("expected quantity 2.50, got %s", task.Quantity)
	}
	if !task.Rate.Equal(decimal.RequireFromString("150.00")) {
		t.Fatalf("expected rate 150.00, got %s", task.Rate)
	}
	if !task.BillableAmount().Equal(decimal.RequireFromString("375.00")) {
		t.Fatalf("expected billable amount 375.00, got %s", task.BillableAmount())
	}
}

func TestUpdateTaskOpenUpdatesCoreFields(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task Update Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")
	task := createTaskForTest(t, db, baseTaskInput(companyID, customerID))

	updated, err := UpdateTask(db, companyID, task.ID, TaskInput{
		CompanyID:    companyID,
		CustomerID:   customerID,
		Title:        "Updated task",
		TaskDate:     time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		Quantity:     decimal.RequireFromString("3.00"),
		UnitType:     models.TaskUnitTypeDay,
		Rate:         decimal.RequireFromString("200.00"),
		CurrencyCode: "CAD",
		IsBillable:   false,
		Notes:        "Updated notes",
	})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	if updated.Title != "Updated task" {
		t.Fatalf("expected updated title, got %q", updated.Title)
	}
	if updated.UnitType != models.TaskUnitTypeDay {
		t.Fatalf("expected updated unit type, got %q", updated.UnitType)
	}
	if updated.CurrencyCode != "CAD" {
		t.Fatalf("expected updated currency, got %q", updated.CurrencyCode)
	}
	if updated.IsBillable {
		t.Fatal("expected task to become non-billable")
	}
	if !updated.Quantity.Equal(decimal.RequireFromString("3.00")) {
		t.Fatalf("expected quantity 3.00, got %s", updated.Quantity)
	}
	if !updated.Rate.Equal(decimal.RequireFromString("200.00")) {
		t.Fatalf("expected rate 200.00, got %s", updated.Rate)
	}
}

func TestCompleteTaskOpenToCompleted(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task Complete Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")
	task := createTaskForTest(t, db, baseTaskInput(companyID, customerID))

	completed, err := CompleteTask(db, companyID, task.ID)
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if completed.Status != models.TaskStatusCompleted {
		t.Fatalf("expected completed status, got %q", completed.Status)
	}
}

func TestCancelTaskOpenToCancelled(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task Cancel Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")
	task := createTaskForTest(t, db, baseTaskInput(companyID, customerID))

	cancelled, err := CancelTask(db, companyID, task.ID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if cancelled.Status != models.TaskStatusCancelled {
		t.Fatalf("expected cancelled status, got %q", cancelled.Status)
	}
}

func TestTaskEditingRespectsCompletedCancelledAndInvoicedRules(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task Rules Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")

	completed := createTaskForTest(t, db, baseTaskInput(companyID, customerID))
	if _, err := CompleteTask(db, companyID, completed.ID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if _, err := UpdateTask(db, companyID, completed.ID, TaskInput{
		CompanyID:    companyID,
		CustomerID:   customerID,
		Title:        completed.Title,
		TaskDate:     completed.TaskDate,
		Quantity:     completed.Quantity.Add(decimal.NewFromInt(1)),
		UnitType:     completed.UnitType,
		Rate:         completed.Rate,
		CurrencyCode: completed.CurrencyCode,
		IsBillable:   completed.IsBillable,
		Notes:        "changed",
	}); !errors.Is(err, ErrTaskCompletedReadOnly) {
		t.Fatalf("expected ErrTaskCompletedReadOnly, got %v", err)
	}

	cancelled := createTaskForTest(t, db, baseTaskInput(companyID, customerID))
	if _, err := CancelTask(db, companyID, cancelled.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if _, err := UpdateTask(db, companyID, cancelled.ID, baseTaskInput(companyID, customerID)); !errors.Is(err, ErrTaskCancelledReadOnly) {
		t.Fatalf("expected ErrTaskCancelledReadOnly, got %v", err)
	}

	invoiced := createTaskForTest(t, db, baseTaskInput(companyID, customerID))
	if err := db.Model(&models.Task{}).Where("id = ?", invoiced.ID).Update("status", models.TaskStatusInvoiced).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateTask(db, companyID, invoiced.ID, baseTaskInput(companyID, customerID)); !errors.Is(err, ErrTaskInvoicedReadOnly) {
		t.Fatalf("expected ErrTaskInvoicedReadOnly, got %v", err)
	}
}

func TestListTasksFilters(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Task List Co")
	customerA := seedTaskServiceCustomer(t, db, companyID, "Acme")
	customerB := seedTaskServiceCustomer(t, db, companyID, "Beta")

	openA := baseTaskInput(companyID, customerA)
	openA.Title = "Open A"
	openA.TaskDate = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	createTaskForTest(t, db, openA)

	completedB := baseTaskInput(companyID, customerB)
	completedB.Title = "Completed B"
	completedB.TaskDate = time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	taskB := createTaskForTest(t, db, completedB)
	if _, err := CompleteTask(db, companyID, taskB.ID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	status := models.TaskStatusCompleted
	from := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	tasks, err := ListTasks(db, TaskListFilter{
		CompanyID:  companyID,
		CustomerID: &customerB,
		Status:     &status,
		From:       &from,
		To:         &to,
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 filtered task, got %d", len(tasks))
	}
	if tasks[0].Title != "Completed B" {
		t.Fatalf("expected Completed B, got %q", tasks[0].Title)
	}
}

// ── service item helpers ──────────────────────────────────────────────────────

func seedTaskRevenueAccount(t *testing.T, db *gorm.DB, companyID uint, code string) uint {
	t.Helper()
	acct := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              "Revenue " + code,
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailServiceRevenue,
		IsActive:          true,
	}
	if err := db.Create(&acct).Error; err != nil {
		t.Fatal(err)
	}
	return acct.ID
}

func seedTaskServiceItem(t *testing.T, db *gorm.DB, companyID, accountID uint, name string, psType models.ProductServiceType, active bool) uint {
	t.Helper()
	item := models.ProductService{
		CompanyID:        companyID,
		Name:             name,
		Type:             psType,
		RevenueAccountID: accountID,
		IsActive:         true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if !active {
		if err := db.Model(&item).Update("is_active", false).Error; err != nil {
			t.Fatal(err)
		}
	}
	return item.ID
}

// ── service item tests ────────────────────────────────────────────────────────

func TestCreateTask_WithServiceItem_Saves(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "SvcItem Save Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")
	acctID := seedTaskRevenueAccount(t, db, companyID, "4000")
	itemID := seedTaskServiceItem(t, db, companyID, acctID, "Consulting", models.ProductServiceTypeService, true)

	in := baseTaskInput(companyID, customerID)
	in.ProductServiceID = &itemID
	task, err := CreateTask(db, in)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ProductServiceID == nil || *task.ProductServiceID != itemID {
		t.Fatalf("expected ProductServiceID %d, got %v", itemID, task.ProductServiceID)
	}

	// Verify GetTaskByID preloads it.
	fetched, err := GetTaskByID(db, companyID, task.ID)
	if err != nil {
		t.Fatalf("GetTaskByID: %v", err)
	}
	if fetched.ProductService == nil {
		t.Fatal("expected ProductService preloaded, got nil")
	}
	if fetched.ProductService.Name != "Consulting" {
		t.Fatalf("expected name %q, got %q", "Consulting", fetched.ProductService.Name)
	}
}

func TestCreateTask_CrossCompanyServiceItemRejected(t *testing.T) {
	db := taskServiceDB(t)
	companyA := seedTaskServiceCompany(t, db, "Cross Co A")
	companyB := seedTaskServiceCompany(t, db, "Cross Co B")
	customerID := seedTaskServiceCustomer(t, db, companyA, "Acme")
	acctID := seedTaskRevenueAccount(t, db, companyB, "4000")
	itemID := seedTaskServiceItem(t, db, companyB, acctID, "B's Service", models.ProductServiceTypeService, true)

	in := baseTaskInput(companyA, customerID)
	in.ProductServiceID = &itemID
	_, err := CreateTask(db, in)
	if !errors.Is(err, ErrTaskServiceItemInvalid) {
		t.Fatalf("expected ErrTaskServiceItemInvalid for cross-company item, got %v", err)
	}
}

func TestCreateTask_NonServiceTypeItemRejected(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "NonSvc Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")
	acctID := seedTaskRevenueAccount(t, db, companyID, "4000")
	invItemID := seedTaskServiceItem(t, db, companyID, acctID, "Widget", models.ProductServiceTypeInventory, true)

	in := baseTaskInput(companyID, customerID)
	in.ProductServiceID = &invItemID
	_, err := CreateTask(db, in)
	if !errors.Is(err, ErrTaskServiceItemInvalid) {
		t.Fatalf("expected ErrTaskServiceItemInvalid for non-service item, got %v", err)
	}
}

func TestCreateTask_InactiveServiceItemRejected(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Inactive Svc Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")
	acctID := seedTaskRevenueAccount(t, db, companyID, "4000")
	itemID := seedTaskServiceItem(t, db, companyID, acctID, "Old Svc", models.ProductServiceTypeService, false) // inactive

	in := baseTaskInput(companyID, customerID)
	in.ProductServiceID = &itemID
	_, err := CreateTask(db, in)
	if !errors.Is(err, ErrTaskServiceItemInvalid) {
		t.Fatalf("expected ErrTaskServiceItemInvalid for inactive item, got %v", err)
	}
}

func TestEditTask_ServiceItemEchoed(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "SvcItem Echo Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")
	acctID := seedTaskRevenueAccount(t, db, companyID, "4000")
	itemID := seedTaskServiceItem(t, db, companyID, acctID, "Design", models.ProductServiceTypeService, true)

	in := baseTaskInput(companyID, customerID)
	in.ProductServiceID = &itemID
	task := createTaskForTest(t, db, in)

	// Update with a different (nil) service item — should clear it.
	updated, err := UpdateTask(db, companyID, task.ID, TaskInput{
		CompanyID:    companyID,
		CustomerID:   customerID,
		Title:        task.Title,
		TaskDate:     task.TaskDate,
		Quantity:     task.Quantity,
		UnitType:     task.UnitType,
		Rate:         task.Rate,
		CurrencyCode: task.CurrencyCode,
		IsBillable:   task.IsBillable,
		Notes:        task.Notes,
		// ProductServiceID intentionally nil
	})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updated.ProductServiceID != nil {
		t.Fatalf("expected ProductServiceID cleared, got %v", updated.ProductServiceID)
	}

	// Re-assign the item.
	updated2, err := UpdateTask(db, companyID, task.ID, TaskInput{
		CompanyID:        companyID,
		CustomerID:       customerID,
		Title:            task.Title,
		TaskDate:         task.TaskDate,
		Quantity:         task.Quantity,
		UnitType:         task.UnitType,
		Rate:             task.Rate,
		CurrencyCode:     task.CurrencyCode,
		IsBillable:       task.IsBillable,
		Notes:            task.Notes,
		ProductServiceID: &itemID,
	})
	if err != nil {
		t.Fatalf("UpdateTask re-assign: %v", err)
	}
	if updated2.ProductServiceID == nil || *updated2.ProductServiceID != itemID {
		t.Fatalf("expected ProductServiceID %d after re-assign, got %v", itemID, updated2.ProductServiceID)
	}
}

func TestCreateTask_DateStoredAsYYYYMMDD(t *testing.T) {
	db := taskServiceDB(t)
	companyID := seedTaskServiceCompany(t, db, "Date Format Co")
	customerID := seedTaskServiceCustomer(t, db, companyID, "Acme")

	in := baseTaskInput(companyID, customerID)
	in.TaskDate = time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)
	task, err := CreateTask(db, in)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Reload from DB to verify stored format.
	var row models.Task
	if err := db.First(&row, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.TaskDate.Format("2006-01-02") != "2026-04-09" {
		t.Fatalf("expected date 2026-04-09, got %s", row.TaskDate.Format("2006-01-02"))
	}
}
