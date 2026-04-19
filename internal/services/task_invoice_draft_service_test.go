package services

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

func testTaskInvoiceDraftDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:task_invoice_draft_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.NumberingSetting{},
		&models.PaymentTerm{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.PaymentTransaction{},
		&models.SettlementAllocation{},
		&models.CreditNoteApplication{}, // required by VoidInvoice credit-application reversal
		&models.APCreditApplication{},   // required by VoidBill credit-application reversal
		&models.PaymentReceipt{},
		&models.AuditLog{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.Bill{},
		&models.BillLine{},
		&models.Task{},
		&models.Expense{},
		&models.ExpenseLine{},
		&models.TaskInvoiceSource{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type taskDraftFixture struct {
	companyID        uint
	customerID       uint
	otherCustomerID  uint
	vendorID         uint
	expenseAccountID uint
	taskLaborItemID  uint
	taskReimItemID   uint
}

func seedTaskDraftFixture(t *testing.T, db *gorm.DB) taskDraftFixture {
	t.Helper()

	company := models.Company{Name: "Task Draft Co", FiscalYearEnd: "12-31", BaseCurrencyCode: "CAD"}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	customerID := seedCustomerForInvoice(t, db, company.ID)
	otherCustomer := models.Customer{CompanyID: company.ID, Name: "Other Customer"}
	if err := db.Create(&otherCustomer).Error; err != nil {
		t.Fatal(err)
	}
	vendor := models.Vendor{CompanyID: company.ID, Name: "Vendor A"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}

	_ = seedAccountForInvoice(t, db, company.ID, "1100", "Accounts Receivable", models.RootAsset, models.DetailAccountsReceivable)
	_ = seedAccountForInvoice(t, db, company.ID, "4000", "Service Revenue", models.RootRevenue, models.DetailServiceRevenue)
	expenseAccountID := seedAccountForInvoice(t, db, company.ID, "6100", "Office Expense", models.RootExpense, models.DetailOfficeExpense)

	if err := EnsureSystemTaskItems(db, company.ID); err != nil {
		t.Fatalf("seed system items: %v", err)
	}
	taskItem, err := LookupSystemTaskItem(db, company.ID, "TASK_LABOR")
	if err != nil {
		t.Fatalf("lookup task labor item: %v", err)
	}
	reimItem, err := LookupSystemTaskItem(db, company.ID, "TASK_REIM")
	if err != nil {
		t.Fatalf("lookup task reim item: %v", err)
	}

	return taskDraftFixture{
		companyID:        company.ID,
		customerID:       customerID,
		otherCustomerID:  otherCustomer.ID,
		vendorID:         vendor.ID,
		expenseAccountID: expenseAccountID,
		taskLaborItemID:  taskItem.ID,
		taskReimItemID:   reimItem.ID,
	}
}

func seedDraftTask(t *testing.T, db *gorm.DB, companyID, customerID uint, status models.TaskStatus, title string, billable bool) models.Task {
	t.Helper()
	task := models.Task{
		CompanyID:    companyID,
		CustomerID:   customerID,
		Title:        title,
		TaskDate:     time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		Quantity:     decimal.RequireFromString("2.00"),
		UnitType:     models.TaskUnitTypeHour,
		Rate:         decimal.RequireFromString("150.00"),
		CurrencyCode: "CAD",
		IsBillable:   billable,
		Status:       status,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if !billable {
		if err := db.Model(&task).Update("is_billable", false).Error; err != nil {
			t.Fatal(err)
		}
		task.IsBillable = false
	}
	return task
}

func seedDraftProductServiceItem(t *testing.T, db *gorm.DB, companyID uint, accountCode, name string, itemType models.ProductServiceType) uint {
	t.Helper()

	revenueAccountID := seedAccountForInvoice(t, db, companyID, accountCode, name+" Revenue", models.RootRevenue, models.DetailServiceRevenue)
	item := models.ProductService{
		CompanyID:        companyID,
		Name:             name,
		Type:             itemType,
		RevenueAccountID: revenueAccountID,
		IsActive:         true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return item.ID
}

func loadSingleDraftLine(t *testing.T, db *gorm.DB, invoiceID uint) models.InvoiceLine {
	t.Helper()

	var invoice models.Invoice
	if err := db.Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc") }).
		First(&invoice, invoiceID).Error; err != nil {
		t.Fatal(err)
	}
	if len(invoice.Lines) != 1 {
		t.Fatalf("expected one invoice line, got %d", len(invoice.Lines))
	}
	return invoice.Lines[0]
}

func seedDraftExpense(t *testing.T, db *gorm.DB, fixture taskDraftFixture, taskID *uint, billable bool, description string, amount string) models.Expense {
	t.Helper()
	expense, err := CreateExpense(db, ExpenseInput{
		CompanyID:        fixture.companyID,
		TaskID:           taskID,
		IsBillable:       billable,
		ExpenseDate:      time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		Description:      description,
		Amount:           decimal.RequireFromString(amount),
		CurrencyCode:     "CAD",
		VendorID:         &fixture.vendorID,
		ExpenseAccountID: &fixture.expenseAccountID,
	})
	if err != nil {
		t.Fatalf("create expense: %v", err)
	}
	return *expense
}

func seedDraftBillLine(t *testing.T, db *gorm.DB, fixture taskDraftFixture, taskID *uint, billable bool, description string, amount string) models.BillLine {
	t.Helper()

	bill := models.Bill{
		CompanyID:           fixture.companyID,
		BillNumber:          fmt.Sprintf("BILL-%d", time.Now().UnixNano()),
		VendorID:            fixture.vendorID,
		BillDate:            time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		Status:              models.BillStatusDraft,
		PaymentTermSnapshot: models.PaymentTermSnapshot{TermCode: "DOC"},
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}

	linkage, err := NormalizeTaskCostLinkage(db, TaskCostLinkageInput{
		CompanyID:  fixture.companyID,
		TaskID:     taskID,
		IsBillable: billable,
	})
	if err != nil {
		t.Fatalf("normalize bill-line linkage: %v", err)
	}

	line := models.BillLine{
		CompanyID:          fixture.companyID,
		BillID:             bill.ID,
		SortOrder:          1,
		Description:        description,
		Qty:                decimal.NewFromInt(1),
		UnitPrice:          decimal.RequireFromString(amount),
		LineNet:            decimal.RequireFromString(amount),
		LineTax:            decimal.Zero,
		LineTotal:          decimal.RequireFromString(amount),
		ExpenseAccountID:   &fixture.expenseAccountID,
		TaskID:             linkage.TaskID,
		BillableCustomerID: linkage.BillableCustomerID,
		IsBillable:         linkage.IsBillable,
		ReinvoiceStatus:    linkage.ReinvoiceStatus,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Preload("Bill").First(&line, line.ID).Error; err != nil {
		t.Fatal(err)
	}
	return line
}

func TestGenerateInvoiceDraft_HappyPathWithMixedSources(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Implementation work", true)
	expense := seedDraftExpense(t, db, fixture, &task.ID, true, "Client travel", "45.00")
	billLine := seedDraftBillLine(t, db, fixture, &task.ID, true, "Contractor pass-through", "30.00")

	result, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:   fixture.companyID,
		CustomerID:  fixture.customerID,
		TaskIDs:     []uint{task.ID},
		ExpenseIDs:  []uint{expense.ID},
		BillLineIDs: []uint{billLine.ID},
		Actor:       "tester",
	})
	if err != nil {
		t.Fatalf("GenerateInvoiceDraft failed: %v", err)
	}
	if result.InvoiceID == 0 || result.LineCount != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}

	var invoice models.Invoice
	if err := db.Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc") }).First(&invoice, result.InvoiceID).Error; err != nil {
		t.Fatal(err)
	}
	if invoice.Status != models.InvoiceStatusDraft {
		t.Fatalf("expected draft invoice, got %q", invoice.Status)
	}
	if invoice.CurrencyCode != "" {
		t.Fatalf("expected base-currency draft to keep blank currency code, got %q", invoice.CurrencyCode)
	}
	if !invoice.ExchangeRate.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("expected base-currency exchange rate 1, got %s", invoice.ExchangeRate)
	}
	if len(invoice.Lines) != 3 {
		t.Fatalf("expected 3 invoice lines, got %d", len(invoice.Lines))
	}

	taskLine := invoice.Lines[0]
	if taskLine.ProductServiceID == nil || *taskLine.ProductServiceID != fixture.taskLaborItemID {
		t.Fatalf("expected TASK_LABOR item %d, got %+v", fixture.taskLaborItemID, taskLine.ProductServiceID)
	}
	if taskLine.Description != task.Title || !taskLine.Qty.Equal(task.Quantity) || !taskLine.UnitPrice.Equal(task.Rate) {
		t.Fatalf("unexpected task line mapping: %+v", taskLine)
	}

	expenseLine := invoice.Lines[1]
	if expenseLine.ProductServiceID == nil || *expenseLine.ProductServiceID != fixture.taskReimItemID {
		t.Fatalf("expected TASK_REIM item %d, got %+v", fixture.taskReimItemID, expenseLine.ProductServiceID)
	}
	if expenseLine.Description != expense.Description || !expenseLine.Qty.Equal(decimal.NewFromInt(1)) || !expenseLine.UnitPrice.Equal(expense.Amount) {
		t.Fatalf("unexpected expense line mapping: %+v", expenseLine)
	}

	billLineInvoice := invoice.Lines[2]
	if billLineInvoice.ProductServiceID == nil || *billLineInvoice.ProductServiceID != fixture.taskReimItemID {
		t.Fatalf("expected TASK_REIM item on bill line, got %+v", billLineInvoice.ProductServiceID)
	}
	if billLineInvoice.Description != billLine.Description || !billLineInvoice.Qty.Equal(decimal.NewFromInt(1)) || !billLineInvoice.UnitPrice.Equal(billLine.LineNet) {
		t.Fatalf("unexpected bill-line mapping: %+v", billLineInvoice)
	}

	var bridges []models.TaskInvoiceSource
	if err := db.Order("id asc").Find(&bridges).Error; err != nil {
		t.Fatal(err)
	}
	if len(bridges) != 3 {
		t.Fatalf("expected 3 task invoice bridges, got %d", len(bridges))
	}
	for _, bridge := range bridges {
		if bridge.VoidedAt != nil {
			t.Fatalf("expected active bridge, got voided %+v", bridge)
		}
		if bridge.InvoiceID == nil || *bridge.InvoiceID != invoice.ID {
			t.Fatalf("expected invoice ref %d, got %+v", invoice.ID, bridge.InvoiceID)
		}
		if bridge.InvoiceLineID == nil {
			t.Fatalf("expected invoice line ref, got %+v", bridge)
		}
	}

	var reloadedTask models.Task
	if err := db.First(&reloadedTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloadedTask.Status != models.TaskStatusInvoiced || reloadedTask.InvoiceID == nil || *reloadedTask.InvoiceID != invoice.ID {
		t.Fatalf("unexpected task linkage after draft: %+v", reloadedTask)
	}

	var reloadedExpense models.Expense
	if err := db.First(&reloadedExpense, expense.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloadedExpense.ReinvoiceStatus != models.ReinvoiceStatusInvoiced || reloadedExpense.InvoiceID == nil || *reloadedExpense.InvoiceID != invoice.ID {
		t.Fatalf("unexpected expense linkage after draft: %+v", reloadedExpense)
	}

	var reloadedBillLine models.BillLine
	if err := db.First(&reloadedBillLine, billLine.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloadedBillLine.ReinvoiceStatus != models.ReinvoiceStatusInvoiced || reloadedBillLine.InvoiceID == nil || *reloadedBillLine.InvoiceID != invoice.ID {
		t.Fatalf("unexpected bill-line linkage after draft: %+v", reloadedBillLine)
	}
}

func TestGenerateInvoiceDraft_UsesTaskServiceItemAndRejectsNonServiceAtDraftTime(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	customServiceID := seedDraftProductServiceItem(t, db, fixture.companyID, "4100", "Implementation Service", models.ProductServiceTypeService)
	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Custom service task", true)
	if err := db.Model(&task).Update("product_service_id", customServiceID).Error; err != nil {
		t.Fatal(err)
	}

	result, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{task.ID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatalf("GenerateInvoiceDraft custom service failed: %v", err)
	}
	line := loadSingleDraftLine(t, db, result.InvoiceID)
	if line.ProductServiceID == nil || *line.ProductServiceID != customServiceID {
		t.Fatalf("expected custom service item %d, got %+v", customServiceID, line.ProductServiceID)
	}
	if *line.ProductServiceID == fixture.taskLaborItemID {
		t.Fatalf("expected custom service item, got TASK_LABOR %d", fixture.taskLaborItemID)
	}

	staleServiceID := seedDraftProductServiceItem(t, db, fixture.companyID, "4101", "Later Non-Service", models.ProductServiceTypeService)
	staleTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Stale service task", true)
	if err := db.Model(&staleTask).Update("product_service_id", staleServiceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ProductService{}).
		Where("id = ?", staleServiceID).
		Update("type", models.ProductServiceTypeNonInventory).Error; err != nil {
		t.Fatal(err)
	}

	staleResult, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{staleTask.ID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatalf("GenerateInvoiceDraft stale service failed: %v", err)
	}
	staleLine := loadSingleDraftLine(t, db, staleResult.InvoiceID)
	if staleLine.ProductServiceID == nil || *staleLine.ProductServiceID != fixture.taskLaborItemID {
		t.Fatalf("expected fallback TASK_LABOR item %d, got %+v", fixture.taskLaborItemID, staleLine.ProductServiceID)
	}
	if *staleLine.ProductServiceID == staleServiceID {
		t.Fatalf("expected non-service task item %d to be rejected at draft time", staleServiceID)
	}
}

func TestGenerateInvoiceDraft_RejectsInvalidStatesAndMismatches(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	openTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusOpen, "Open task", true)
	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{openTask.ID},
		Actor:      "tester",
	}); !errors.Is(err, ErrBillableWorkTaskNotReady) {
		t.Fatalf("expected ErrBillableWorkTaskNotReady, got %v", err)
	}

	nonBillableTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Non-billable", false)
	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{nonBillableTask.ID},
		Actor:      "tester",
	}); !errors.Is(err, ErrBillableWorkTaskNotReady) {
		t.Fatalf("expected ErrBillableWorkTaskNotReady for non-billable task, got %v", err)
	}

	readyTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Ready task", true)
	plainExpense := seedDraftExpense(t, db, fixture, nil, true, "Plain expense", "20.00")
	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		ExpenseIDs: []uint{plainExpense.ID},
		Actor:      "tester",
	}); !errors.Is(err, ErrBillableWorkExpenseNotReady) {
		t.Fatalf("expected ErrBillableWorkExpenseNotReady, got %v", err)
	}

	nonBillableBillLine := seedDraftBillLine(t, db, fixture, &readyTask.ID, false, "Non-billable bill line", "18.00")
	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:   fixture.companyID,
		CustomerID:  fixture.customerID,
		BillLineIDs: []uint{nonBillableBillLine.ID},
		Actor:       "tester",
	}); !errors.Is(err, ErrBillableWorkBillLineNotReady) {
		t.Fatalf("expected ErrBillableWorkBillLineNotReady, got %v", err)
	}

	otherCustomerTask := seedDraftTask(t, db, fixture.companyID, fixture.otherCustomerID, models.TaskStatusCompleted, "Other customer task", true)
	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{otherCustomerTask.ID},
		Actor:      "tester",
	}); !errors.Is(err, ErrBillableWorkCustomerMismatch) {
		t.Fatalf("expected ErrBillableWorkCustomerMismatch, got %v", err)
	}

	otherCompany := seedTaskDraftFixture(t, db)
	foreignTask := seedDraftTask(t, db, otherCompany.companyID, otherCompany.customerID, models.TaskStatusCompleted, "Other company task", true)
	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{foreignTask.ID},
		Actor:      "tester",
	}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound for cross-company source, got %v", err)
	}
}

func TestLoadDraftableBillLines_UsesNamedNotFoundError(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)
	otherFixture := seedTaskDraftFixture(t, db)
	otherTask := seedDraftTask(t, db, otherFixture.companyID, otherFixture.customerID, models.TaskStatusCompleted, "Other company task", true)
	otherLine := seedDraftBillLine(t, db, otherFixture, &otherTask.ID, true, "Cross-company bill line", "18.00")

	_, err := loadDraftableBillLines(db, fixture.companyID, fixture.customerID, []uint{otherLine.ID})
	if !errors.Is(err, ErrBillLineNotFound) {
		t.Fatalf("expected ErrBillLineNotFound, got %v", err)
	}
}

func TestHasActiveTaskInvoiceSources_IgnoresHistoricalRows(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	invoice := models.Invoice{
		CompanyID:               fixture.companyID,
		InvoiceNumber:           "INV-TASK-ACTIVE",
		CustomerID:              fixture.customerID,
		InvoiceDate:             time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		PaymentTermSnapshot:     models.PaymentTermSnapshot{TermCode: "DOC"},
		Status:                  models.InvoiceStatusDraft,
		CustomerNameSnapshot:    "Customer",
		CustomerEmailSnapshot:   "",
		CustomerAddressSnapshot: "",
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}
	line := models.InvoiceLine{
		CompanyID:   fixture.companyID,
		InvoiceID:   invoice.ID,
		SortOrder:   1,
		Description: "Task source line",
		Qty:         decimal.NewFromInt(1),
		UnitPrice:   decimal.RequireFromString("10.00"),
		LineNet:     decimal.RequireFromString("10.00"),
		LineTax:     decimal.Zero,
		LineTotal:   decimal.RequireFromString("10.00"),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	bridge := models.TaskInvoiceSource{
		CompanyID:      fixture.companyID,
		InvoiceID:      &invoice.ID,
		InvoiceLineID:  &line.ID,
		SourceType:     models.TaskInvoiceSourceTask,
		SourceID:       999,
		AmountSnapshot: decimal.RequireFromString("10.00"),
	}
	if err := db.Create(&bridge).Error; err != nil {
		t.Fatal(err)
	}

	hasActive, err := HasActiveTaskInvoiceSources(db, fixture.companyID, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasActive {
		t.Fatal("expected active task invoice sources to be detected")
	}

	now := time.Now().UTC()
	if err := db.Model(&models.TaskInvoiceSource{}).Where("id = ?", bridge.ID).Update("voided_at", now).Error; err != nil {
		t.Fatal(err)
	}

	hasActive, err = HasActiveTaskInvoiceSources(db, fixture.companyID, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasActive {
		t.Fatal("expected historical bridge rows to be ignored")
	}
}

func TestGenerateInvoiceDraft_BlocksDuplicateActiveLinkage(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)
	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Duplicated task", true)

	bridge := models.TaskInvoiceSource{
		CompanyID:      fixture.companyID,
		SourceType:     models.TaskInvoiceSourceTask,
		SourceID:       task.ID,
		AmountSnapshot: decimal.RequireFromString("300.00"),
	}
	if err := db.Create(&bridge).Error; err != nil {
		t.Fatalf("seed active bridge: %v", err)
	}

	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{task.ID},
		Actor:      "tester",
	}); !errors.Is(err, ErrBillableWorkSourceAlreadyUsed) {
		t.Fatalf("expected ErrBillableWorkSourceAlreadyUsed, got %v", err)
	}
}

func TestGenerateInvoiceDraft_CurrencyRules(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	fxTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "USD task", true)
	if err := db.Model(&models.Task{}).Where("id = ?", fxTask.ID).Updates(map[string]any{
		"currency_code": "USD",
		"rate":          decimal.RequireFromString("100.00"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{fxTask.ID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatalf("expected foreign-currency draft to succeed, got %v", err)
	}
	var fxInvoice models.Invoice
	if err := db.First(&fxInvoice, result.InvoiceID).Error; err != nil {
		t.Fatal(err)
	}
	if fxInvoice.CurrencyCode != "USD" {
		t.Fatalf("expected USD draft currency, got %q", fxInvoice.CurrencyCode)
	}
	if !fxInvoice.ExchangeRate.IsZero() {
		t.Fatalf("expected foreign-currency draft exchange rate 0 for auto-lookup, got %s", fxInvoice.ExchangeRate)
	}

	mixedTask := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "CAD task", true)
	mixedExpense := seedDraftExpense(t, db, fixture, &mixedTask.ID, true, "USD expense", "25.00")
	if err := db.Model(&models.Expense{}).Where("id = ?", mixedExpense.ID).Update("currency_code", "USD").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{mixedTask.ID},
		ExpenseIDs: []uint{mixedExpense.ID},
		Actor:      "tester",
	}); !errors.Is(err, ErrBillableWorkCurrencyMismatch) {
		t.Fatalf("expected ErrBillableWorkCurrencyMismatch, got %v", err)
	}
}

func TestGenerateInvoiceDraft_DeleteDraftReleasesSourcesAndAllowsRegeneration(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)
	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Delete release task", true)
	expense := seedDraftExpense(t, db, fixture, &task.ID, true, "Delete release expense", "35.00")
	billLine := seedDraftBillLine(t, db, fixture, &task.ID, true, "Delete release bill line", "25.00")

	result, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:   fixture.companyID,
		CustomerID:  fixture.customerID,
		TaskIDs:     []uint{task.ID},
		ExpenseIDs:  []uint{expense.ID},
		BillLineIDs: []uint{billLine.ID},
		Actor:       "tester",
	})
	if err != nil {
		t.Fatalf("generate draft: %v", err)
	}

	if err := DeleteInvoice(db, fixture.companyID, result.InvoiceID, "tester", nil); err != nil {
		t.Fatalf("DeleteInvoice failed: %v", err)
	}

	var invoiceCount int64
	if err := db.Model(&models.Invoice{}).Where("id = ?", result.InvoiceID).Count(&invoiceCount).Error; err != nil {
		t.Fatal(err)
	}
	if invoiceCount != 0 {
		t.Fatalf("expected draft invoice deleted, count=%d", invoiceCount)
	}

	assertReleasedTaskCostSources(t, db, fixture.companyID, task.ID, expense.ID, billLine.ID, true)

	var bridges []models.TaskInvoiceSource
	if err := db.Order("id asc").Find(&bridges).Error; err != nil {
		t.Fatal(err)
	}
	if len(bridges) != 3 {
		t.Fatalf("expected 3 preserved bridge rows, got %d", len(bridges))
	}
	for _, bridge := range bridges {
		if bridge.VoidedAt == nil {
			t.Fatalf("expected historical bridge after delete, got %+v", bridge)
		}
		if bridge.InvoiceID != nil || bridge.InvoiceLineID != nil {
			t.Fatalf("expected deleted-draft bridge refs cleared, got %+v", bridge)
		}
	}

	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:   fixture.companyID,
		CustomerID:  fixture.customerID,
		TaskIDs:     []uint{task.ID},
		ExpenseIDs:  []uint{expense.ID},
		BillLineIDs: []uint{billLine.ID},
		Actor:       "tester",
	}); err != nil {
		t.Fatalf("expected regenerated draft to succeed after delete release, got %v", err)
	}
}

func TestGenerateInvoiceDraft_VoidInvoiceRollsBackSourcesAndAllowsRegeneration(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)
	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Void release task", true)
	expense := seedDraftExpense(t, db, fixture, &task.ID, true, "Void release expense", "40.00")
	billLine := seedDraftBillLine(t, db, fixture, &task.ID, true, "Void release bill line", "22.00")

	result, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:   fixture.companyID,
		CustomerID:  fixture.customerID,
		TaskIDs:     []uint{task.ID},
		ExpenseIDs:  []uint{expense.ID},
		BillLineIDs: []uint{billLine.ID},
		Actor:       "tester",
	})
	if err != nil {
		t.Fatalf("generate draft: %v", err)
	}

	if _, err := IssueInvoice(db, fixture.companyID, result.InvoiceID); err != nil {
		t.Fatalf("IssueInvoice failed: %v", err)
	}
	if err := VoidInvoice(db, fixture.companyID, result.InvoiceID, "tester", nil); err != nil {
		t.Fatalf("VoidInvoice failed: %v", err)
	}

	var invoice models.Invoice
	if err := db.First(&invoice, result.InvoiceID).Error; err != nil {
		t.Fatal(err)
	}
	if invoice.Status != models.InvoiceStatusVoided {
		t.Fatalf("expected voided invoice, got %q", invoice.Status)
	}

	assertReleasedTaskCostSources(t, db, fixture.companyID, task.ID, expense.ID, billLine.ID, false)

	var bridges []models.TaskInvoiceSource
	if err := db.Order("id asc").Find(&bridges).Error; err != nil {
		t.Fatal(err)
	}
	if len(bridges) != 3 {
		t.Fatalf("expected 3 preserved bridge rows, got %d", len(bridges))
	}
	for _, bridge := range bridges {
		if bridge.VoidedAt == nil {
			t.Fatalf("expected bridge to be historical after void, got %+v", bridge)
		}
		if bridge.InvoiceID == nil || bridge.InvoiceLineID == nil {
			t.Fatalf("expected historical void bridge refs retained, got %+v", bridge)
		}
	}

	if _, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:   fixture.companyID,
		CustomerID:  fixture.customerID,
		TaskIDs:     []uint{task.ID},
		ExpenseIDs:  []uint{expense.ID},
		BillLineIDs: []uint{billLine.ID},
		Actor:       "tester",
	}); err != nil {
		t.Fatalf("expected regenerated draft to succeed after void rollback, got %v", err)
	}
}

// ── Tax scope strip tests ─────────────────────────────────────────────────────

// TestTaskDraft_PurchaseOnlyTax_StrippedFromLine verifies that when a system task
// item has a purchase-only DefaultTaxCodeID, GenerateInvoiceDraft strips it:
// the resulting invoice line gets TaxCodeID=nil and LineTax=0.
func TestTaskDraft_PurchaseOnlyTax_StrippedFromLine(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	// Create a purchase-only tax code.
	taxAcct := models.Account{
		CompanyID: fixture.companyID, Code: "2310", Name: "Tax Payable",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailSalesTaxPayable, IsActive: true,
	}
	if err := db.Create(&taxAcct).Error; err != nil {
		t.Fatal(err)
	}
	purTax := models.TaxCode{
		CompanyID:         fixture.companyID,
		Name:              "GST Purchase",
		Code:              "GST-P",
		TaxType:           "taxable",
		Rate:              decimal.NewFromFloat(0.05),
		Scope:             models.TaxScopePurchase,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      decimal.Zero,
		SalesTaxAccountID: taxAcct.ID,
		IsActive:          true,
	}
	if err := db.Create(&purTax).Error; err != nil {
		t.Fatal(err)
	}

	// Assign purchase-only tax as default on the TASK_LABOR system item.
	if err := db.Model(&models.ProductService{}).
		Where("id = ?", fixture.taskLaborItemID).
		Update("default_tax_code_id", purTax.ID).Error; err != nil {
		t.Fatal(err)
	}

	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Tax strip task", true)
	result, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{task.ID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatalf("generate draft: %v", err)
	}

	var lines []models.InvoiceLine
	if err := db.Where("invoice_id = ?", result.InvoiceID).Find(&lines).Error; err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Fatal("expected at least one invoice line")
	}
	// The task labor line must have no tax code (stripped).
	for _, l := range lines {
		if l.ProductServiceID != nil && *l.ProductServiceID == fixture.taskLaborItemID {
			if l.TaxCodeID != nil {
				t.Errorf("TaxCodeID should be nil (stripped), got %v", *l.TaxCodeID)
			}
			if !l.LineTax.IsZero() {
				t.Errorf("LineTax should be 0 (stripped), got %s", l.LineTax.String())
			}
			return
		}
	}
	t.Error("no line found for TASK_LABOR item")
}

// TestTaskDraft_InactiveTax_StrippedFromLine verifies that when a system task item
// has an inactive DefaultTaxCodeID, GenerateInvoiceDraft strips it.
func TestTaskDraft_InactiveTax_StrippedFromLine(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	taxAcct := models.Account{
		CompanyID: fixture.companyID, Code: "2311", Name: "Tax Payable 2",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailSalesTaxPayable, IsActive: true,
	}
	if err := db.Create(&taxAcct).Error; err != nil {
		t.Fatal(err)
	}
	inactiveTax := models.TaxCode{
		CompanyID:         fixture.companyID,
		Name:              "Old GST",
		Code:              "GST-OLD",
		TaxType:           "taxable",
		Rate:              decimal.NewFromFloat(0.05),
		Scope:             models.TaxScopeSales,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      decimal.Zero,
		SalesTaxAccountID: taxAcct.ID,
		IsActive:          true,
	}
	if err := db.Create(&inactiveTax).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&inactiveTax).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Model(&models.ProductService{}).
		Where("id = ?", fixture.taskLaborItemID).
		Update("default_tax_code_id", inactiveTax.ID).Error; err != nil {
		t.Fatal(err)
	}

	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Inactive tax strip task", true)
	result, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{task.ID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatalf("generate draft: %v", err)
	}

	var lines []models.InvoiceLine
	db.Where("invoice_id = ?", result.InvoiceID).Find(&lines)
	for _, l := range lines {
		if l.ProductServiceID != nil && *l.ProductServiceID == fixture.taskLaborItemID {
			if l.TaxCodeID != nil {
				t.Errorf("TaxCodeID should be nil (stripped), got %v", *l.TaxCodeID)
			}
			if !l.LineTax.IsZero() {
				t.Errorf("LineTax should be 0 (stripped), got %s", l.LineTax.String())
			}
			return
		}
	}
	t.Error("no line found for TASK_LABOR item")
}

// TestTaskDraft_ValidSalesTax_CarriedThrough verifies that a valid sales-scoped
// active tax code on the system task item is carried to the generated invoice line.
func TestTaskDraft_ValidSalesTax_CarriedThrough(t *testing.T) {
	db := testTaskInvoiceDraftDB(t)
	fixture := seedTaskDraftFixture(t, db)

	taxAcct := models.Account{
		CompanyID: fixture.companyID, Code: "2312", Name: "Tax Payable 3",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailSalesTaxPayable, IsActive: true,
	}
	if err := db.Create(&taxAcct).Error; err != nil {
		t.Fatal(err)
	}
	salesTax := models.TaxCode{
		CompanyID:         fixture.companyID,
		Name:              "GST Sales",
		Code:              "GST-S",
		TaxType:           "taxable",
		Rate:              decimal.NewFromFloat(0.05),
		Scope:             models.TaxScopeSales,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      decimal.Zero,
		SalesTaxAccountID: taxAcct.ID,
		IsActive:          true,
	}
	if err := db.Create(&salesTax).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Model(&models.ProductService{}).
		Where("id = ?", fixture.taskLaborItemID).
		Update("default_tax_code_id", salesTax.ID).Error; err != nil {
		t.Fatal(err)
	}

	// 2h × $150/h = $300 net; 5% = $15 tax
	task := seedDraftTask(t, db, fixture.companyID, fixture.customerID, models.TaskStatusCompleted, "Sales tax task", true)
	result, err := GenerateInvoiceDraft(db, GenerateInvoiceDraftInput{
		CompanyID:  fixture.companyID,
		CustomerID: fixture.customerID,
		TaskIDs:    []uint{task.ID},
		Actor:      "tester",
	})
	if err != nil {
		t.Fatalf("generate draft: %v", err)
	}

	var lines []models.InvoiceLine
	db.Where("invoice_id = ?", result.InvoiceID).Find(&lines)
	for _, l := range lines {
		if l.ProductServiceID != nil && *l.ProductServiceID == fixture.taskLaborItemID {
			if l.TaxCodeID == nil {
				t.Error("TaxCodeID should be set for valid sales tax code")
			}
			expected := decimal.NewFromFloat(15.00)
			if !l.LineTax.Equal(expected) {
				t.Errorf("LineTax: want %s, got %s", expected, l.LineTax)
			}
			return
		}
	}
	t.Error("no line found for TASK_LABOR item")
}

func assertReleasedTaskCostSources(t *testing.T, db *gorm.DB, companyID, taskID, expenseID, billLineID uint, expectClearedBridgeRefs bool) {
	t.Helper()

	var task models.Task
	if err := db.Where("company_id = ? AND id = ?", companyID, taskID).First(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != models.TaskStatusCompleted || task.InvoiceID != nil || task.InvoiceLineID != nil {
		t.Fatalf("expected task released back to completed with nil cache, got %+v", task)
	}

	var expense models.Expense
	if err := db.Where("company_id = ? AND id = ?", companyID, expenseID).First(&expense).Error; err != nil {
		t.Fatal(err)
	}
	if expense.ReinvoiceStatus != models.ReinvoiceStatusUninvoiced || expense.InvoiceID != nil || expense.InvoiceLineID != nil {
		t.Fatalf("expected expense released back to uninvoiced with nil cache, got %+v", expense)
	}

	var billLine models.BillLine
	if err := db.Where("company_id = ? AND id = ?", companyID, billLineID).First(&billLine).Error; err != nil {
		t.Fatal(err)
	}
	if billLine.ReinvoiceStatus != models.ReinvoiceStatusUninvoiced || billLine.InvoiceID != nil || billLine.InvoiceLineID != nil {
		t.Fatalf("expected bill line released back to uninvoiced with nil cache, got %+v", billLine)
	}

	var activeCount int64
	if err := db.Model(&models.TaskInvoiceSource{}).Where("company_id = ? AND voided_at IS NULL", companyID).Count(&activeCount).Error; err != nil {
		t.Fatal(err)
	}
	if activeCount != 0 {
		t.Fatalf("expected no active bridge rows after release, got %d", activeCount)
	}

	if expectClearedBridgeRefs {
		var count int64
		if err := db.Model(&models.TaskInvoiceSource{}).
			Where("company_id = ? AND (invoice_id IS NOT NULL OR invoice_line_id IS NOT NULL)", companyID).
			Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected all bridge refs cleared after draft delete, got %d rows", count)
		}
	}
}
