// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func testCustomerDepositsAccountDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:cust_deposits_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedCustomerDepositsCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{
		Name:              "Deposits Test Co",
		EntityType:        models.EntityTypeIncorporated,
		BusinessType:      models.BusinessTypeRetail,
		Industry:          models.IndustryRetail,
		IncorporatedDate:  "2024-01-01",
		FiscalYearEnd:     "12-31",
		BusinessNumber:    "111222333",
		AccountCodeLength: 4,
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// TestEnsureCustomerDepositsAccount_CreatesOnFirstCall locks the happy path:
// first call creates a system-generated liability account with the right
// classification + system_key, returning its ID.
func TestEnsureCustomerDepositsAccount_CreatesOnFirstCall(t *testing.T) {
	db := testCustomerDepositsAccountDB(t)
	companyID := seedCustomerDepositsCompany(t, db)

	id, err := EnsureCustomerDepositsAccount(db, companyID)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero account ID")
	}

	var acc models.Account
	if err := db.First(&acc, id).Error; err != nil {
		t.Fatal(err)
	}
	if acc.Name != "Customer Deposits" {
		t.Errorf("name = %q, want %q", acc.Name, "Customer Deposits")
	}
	if acc.RootAccountType != models.RootLiability {
		t.Errorf("root = %q, want liability", acc.RootAccountType)
	}
	if acc.DetailAccountType != models.DetailOtherCurrentLiability {
		t.Errorf("detail = %q, want other_current_liability", acc.DetailAccountType)
	}
	if !acc.IsSystemGenerated {
		t.Error("expected IsSystemGenerated=true")
	}
	if acc.SystemKey == nil || *acc.SystemKey != "customer_deposits" {
		t.Errorf("system_key = %+v, want customer_deposits", acc.SystemKey)
	}
	if !acc.IsActive {
		t.Error("expected IsActive=true")
	}
}

// TestEnsureCustomerDepositsAccount_IdempotentOnSecondCall locks the
// idempotency contract: a second call returns the same ID without creating
// a duplicate account — the overpayment path calls this per transaction, so
// double-creation would mean multiple liability accounts per company.
func TestEnsureCustomerDepositsAccount_IdempotentOnSecondCall(t *testing.T) {
	db := testCustomerDepositsAccountDB(t)
	companyID := seedCustomerDepositsCompany(t, db)

	first, err := EnsureCustomerDepositsAccount(db, companyID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureCustomerDepositsAccount(db, companyID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("expected same ID on second call; got %d then %d", first, second)
	}

	var count int64
	db.Model(&models.Account{}).
		Where("company_id = ? AND system_key = ?", companyID, "customer_deposits").
		Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 customer_deposits account, got %d", count)
	}
}

// TestEnsureCustomerDepositsAccount_ScopedPerCompany confirms two different
// companies each get their own account (multi-tenant safety).
func TestEnsureCustomerDepositsAccount_ScopedPerCompany(t *testing.T) {
	db := testCustomerDepositsAccountDB(t)
	a := seedCustomerDepositsCompany(t, db)
	b := seedCustomerDepositsCompany(t, db)

	aid, err := EnsureCustomerDepositsAccount(db, a)
	if err != nil {
		t.Fatal(err)
	}
	bid, err := EnsureCustomerDepositsAccount(db, b)
	if err != nil {
		t.Fatal(err)
	}
	if aid == bid {
		t.Fatalf("expected distinct accounts per company; both returned %d", aid)
	}
}
