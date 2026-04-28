// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Test DB setup ─────────────────────────────────────────────────────────────

func taskSystemItemsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:task_system_items_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Customer{},
		&models.Vendor{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Task{},
		&models.Expense{},
		&models.TaskInvoiceSource{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func tsCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	row := models.Company{
		Name:                    "Test Co",
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

func tsRevenueAccount(t *testing.T, db *gorm.DB, companyID uint) {
	t.Helper()
	row := models.Account{
		CompanyID:         companyID,
		Code:              "4000",
		Name:              "Service Revenue",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailServiceRevenue,
		IsActive:          true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestEnsureSystemTaskItems_CreatesItems verifies that EnsureSystemTaskItems
// creates TASK_LABOR and TASK_REIM for a fresh company.
func TestEnsureSystemTaskItems_CreatesItems(t *testing.T) {
	db := taskSystemItemsDB(t)
	companyID := tsCompany(t, db)
	tsRevenueAccount(t, db, companyID)

	if err := EnsureSystemTaskItems(db, companyID); err != nil {
		t.Fatalf("EnsureSystemTaskItems: unexpected error: %v", err)
	}

	// Both system items must exist.
	for _, code := range []string{"TASK_LABOR", "TASK_REIM"} {
		item, err := LookupSystemTaskItem(db, companyID, code)
		if err != nil {
			t.Fatalf("LookupSystemTaskItem(%s): %v", code, err)
		}
		if !item.IsSystem {
			t.Errorf("%s: expected is_system=true, got false", code)
		}
		if item.SystemCode == nil || *item.SystemCode != code {
			t.Errorf("%s: unexpected system_code %v", code, item.SystemCode)
		}
		if !item.IsActive {
			t.Errorf("%s: expected is_active=true, got false", code)
		}
	}

	// TASK_LABOR must be service type.
	labor, _ := LookupSystemTaskItem(db, companyID, "TASK_LABOR")
	if labor.Type != models.ProductServiceTypeService {
		t.Errorf("TASK_LABOR: expected type=service, got %s", labor.Type)
	}

	// TASK_REIM must be non_inventory type.
	reim, _ := LookupSystemTaskItem(db, companyID, "TASK_REIM")
	if reim.Type != models.ProductServiceTypeNonInventory {
		t.Errorf("TASK_REIM: expected type=non_inventory, got %s", reim.Type)
	}
}

// TestEnsureSystemTaskItems_Idempotent verifies that running EnsureSystemTaskItems
// twice does not create duplicate rows and does not return an error.
func TestEnsureSystemTaskItems_Idempotent(t *testing.T) {
	db := taskSystemItemsDB(t)
	companyID := tsCompany(t, db)
	tsRevenueAccount(t, db, companyID)

	if err := EnsureSystemTaskItems(db, companyID); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Second run must succeed with no duplicates.
	if err := EnsureSystemTaskItems(db, companyID); err != nil {
		t.Fatalf("second run: %v", err)
	}

	var count int64
	db.Model(&models.ProductService{}).
		Where("company_id = ? AND is_system = true", companyID).
		Count(&count)
	if count != 2 {
		t.Errorf("expected exactly 2 system items after two runs, got %d", count)
	}
}

// TestEnsureSystemTaskItems_PerCompanyScoped verifies that system items are
// company-scoped: two companies each get their own independent copies.
func TestEnsureSystemTaskItems_PerCompanyScoped(t *testing.T) {
	db := taskSystemItemsDB(t)

	companyA := tsCompany(t, db)
	companyB := tsCompany(t, db)

	tsRevenueAccount(t, db, companyA)
	// Seed a revenue account for company B independently.
	if err := db.Create(&models.Account{
		CompanyID:         companyB,
		Code:              "4000",
		Name:              "Service Revenue",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailServiceRevenue,
		IsActive:          true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := EnsureSystemTaskItems(db, companyA); err != nil {
		t.Fatalf("company A: %v", err)
	}
	if err := EnsureSystemTaskItems(db, companyB); err != nil {
		t.Fatalf("company B: %v", err)
	}

	// Each company has exactly 2 system items.
	for _, cid := range []uint{companyA, companyB} {
		var count int64
		db.Model(&models.ProductService{}).
			Where("company_id = ? AND is_system = true", cid).
			Count(&count)
		if count != 2 {
			t.Errorf("company %d: expected 2 system items, got %d", cid, count)
		}
	}

	// Total across all companies: 4 rows, no cross-contamination.
	var total int64
	db.Model(&models.ProductService{}).Where("is_system = true").Count(&total)
	if total != 4 {
		t.Errorf("expected 4 total system items across 2 companies, got %d", total)
	}
}

// TestEnsureSystemTaskItems_NoRevenueAccount verifies that the function returns
// an error when the company has no revenue accounts at all.
func TestEnsureSystemTaskItems_NoRevenueAccount(t *testing.T) {
	db := taskSystemItemsDB(t)
	companyID := tsCompany(t, db)
	// Deliberately do NOT create any revenue account.

	err := EnsureSystemTaskItems(db, companyID)
	if err == nil {
		t.Fatal("expected error when no revenue account exists, got nil")
	}
}

// TestTaskModel_BillableAmount verifies the Task.BillableAmount() helper.
func TestTaskModel_BillableAmount(t *testing.T) {
	task := models.Task{
		Quantity: decimal.RequireFromString("2.5"),
		Rate:     decimal.RequireFromString("100.00"),
	}
	got := task.BillableAmount()
	want := decimal.RequireFromString("250.00")
	if !got.Equal(want) {
		t.Errorf("BillableAmount: got %s, want %s", got, want)
	}
}

// TestBillLineTaskFieldZeroValues is a compile-time regression guard:
// ensures the new BillLine fields have the expected zero values when a
// BillLine is constructed without explicitly setting these fields.
func TestBillLineTaskFieldZeroValues(t *testing.T) {
	line := models.BillLine{}
	if line.TaskID != nil {
		t.Error("BillLine.TaskID: expected nil zero value")
	}
	if line.IsBillable {
		t.Error("BillLine.IsBillable: expected false zero value")
	}
	if line.ReinvoiceStatus != models.ReinvoiceStatusNone {
		t.Errorf("BillLine.ReinvoiceStatus: expected %q, got %q",
			models.ReinvoiceStatusNone, line.ReinvoiceStatus)
	}
	if !line.MarkupPercent.IsZero() {
		t.Errorf("BillLine.MarkupPercent: expected 0, got %s", line.MarkupPercent)
	}
}

// TestExpenseModelFieldZeroValues guards the Expense model zero values.
func TestExpenseModelFieldZeroValues(t *testing.T) {
	exp := models.Expense{}
	if exp.TaskID != nil {
		t.Error("Expense.TaskID: expected nil zero value")
	}
	if exp.IsBillable {
		t.Error("Expense.IsBillable: expected false zero value")
	}
	if exp.ReinvoiceStatus != models.ReinvoiceStatusNone {
		t.Errorf("Expense.ReinvoiceStatus: expected %q, got %q",
			models.ReinvoiceStatusNone, exp.ReinvoiceStatus)
	}
	if !exp.MarkupPercent.IsZero() {
		t.Errorf("Expense.MarkupPercent: expected 0, got %s", exp.MarkupPercent)
	}
}

// TestTaskStatusConstants ensures the status string values are stable.
func TestTaskStatusConstants(t *testing.T) {
	cases := map[models.TaskStatus]string{
		models.TaskStatusOpen:      "open",
		models.TaskStatusCompleted: "completed",
		models.TaskStatusInvoiced:  "invoiced",
		models.TaskStatusCancelled: "cancelled",
	}
	for status, want := range cases {
		if string(status) != want {
			t.Errorf("TaskStatus %v: expected string %q, got %q", status, want, string(status))
		}
	}
}

// TestReinvoiceStatusConstants ensures the reinvoice status string values are stable.
func TestReinvoiceStatusConstants(t *testing.T) {
	cases := map[models.ReinvoiceStatus]string{
		models.ReinvoiceStatusNone:      "",
		models.ReinvoiceStatusUninvoiced: "uninvoiced",
		models.ReinvoiceStatusInvoiced:  "invoiced",
		models.ReinvoiceStatusExcluded:  "excluded",
	}
	for status, want := range cases {
		if string(status) != want {
			t.Errorf("ReinvoiceStatus %v: expected string %q, got %q", status, want, string(status))
		}
	}
}
