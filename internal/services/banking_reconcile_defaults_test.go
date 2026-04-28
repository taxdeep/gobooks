// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

func testReconcileDefaultsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:recdefaults_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Reconciliation{},
		&models.ReconciliationDraft{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

func seedRecDefCompany(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	co := models.Company{Name: name, IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	return co.ID
}

func seedBankAccount(t *testing.T, db *gorm.DB, companyID uint, code string) uint {
	t.Helper()
	acc := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              "Bank " + code,
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	return acc.ID
}

// 1. In-progress draft → highest priority, restores all fields.
func TestComputeReconcileDefaults_Draft(t *testing.T) {
	db := testReconcileDefaultsDB(t)
	coID := seedRecDefCompany(t, db, "Draft Co")
	acctID := seedBankAccount(t, db, coID, "1000")

	// Seed a draft.
	if err := UpsertReconcileDraft(db, coID, acctID, "2026-04-30", "5000.00", `["1","2"]`); err != nil {
		t.Fatalf("UpsertReconcileDraft: %v", err)
	}

	d, err := ComputeReconcileDefaults(db, coID, acctID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != ReconcileDefaultsDraft {
		t.Fatalf("expected Draft source, got %d", d.Source)
	}
	if d.StatementDate != "2026-04-30" {
		t.Errorf("StatementDate: want 2026-04-30, got %q", d.StatementDate)
	}
	if d.EndingBalance != "5000.00" {
		t.Errorf("EndingBalance: want 5000.00, got %q", d.EndingBalance)
	}
	if d.SelectedLineIDs != `["1","2"]` {
		t.Errorf("SelectedLineIDs: want [\"1\",\"2\"], got %q", d.SelectedLineIDs)
	}
}

// 2. No draft, prior completed reconciliation → infer next month-end; ending balance left empty.
func TestComputeReconcileDefaults_Inferred(t *testing.T) {
	db := testReconcileDefaultsDB(t)
	coID := seedRecDefCompany(t, db, "Infer Co")
	acctID := seedBankAccount(t, db, coID, "1001")

	// Seed a completed reconciliation with statement_date = 2026-03-31.
	lastDate := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	rec := models.Reconciliation{
		CompanyID:      coID,
		AccountID:      acctID,
		StatementDate:  lastDate,
		EndingBalance:  decimal.RequireFromString("3000.00"),
		ClearedBalance: decimal.RequireFromString("3000.00"),
	}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatal(err)
	}

	d, err := ComputeReconcileDefaults(db, coID, acctID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != ReconcileDefaultsInferred {
		t.Fatalf("expected Inferred source, got %d", d.Source)
	}
	// Next month-end after 2026-03-31 → 2026-04-30.
	if d.StatementDate != "2026-04-30" {
		t.Errorf("StatementDate: want 2026-04-30, got %q", d.StatementDate)
	}
	if d.EndingBalance != "" {
		t.Errorf("EndingBalance should be empty (user must enter), got %q", d.EndingBalance)
	}
}

// 2b. Prior reconciliation date is not month-end — next month-end is still the last day of next month.
func TestComputeReconcileDefaults_Inferred_MidMonth(t *testing.T) {
	db := testReconcileDefaultsDB(t)
	coID := seedRecDefCompany(t, db, "MidMonth Co")
	acctID := seedBankAccount(t, db, coID, "1002")

	lastDate := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	rec := models.Reconciliation{
		CompanyID:      coID,
		AccountID:      acctID,
		StatementDate:  lastDate,
		EndingBalance:  decimal.RequireFromString("100.00"),
		ClearedBalance: decimal.RequireFromString("100.00"),
	}
	db.Create(&rec)

	d, err := ComputeReconcileDefaults(db, coID, acctID)
	if err != nil {
		t.Fatal(err)
	}
	// Next month-end after 2026-01-15 → 2026-02-28.
	if d.StatementDate != "2026-02-28" {
		t.Errorf("StatementDate: want 2026-02-28, got %q", d.StatementDate)
	}
}

// 2c. December → wraps to Jan 31 next year.
func TestComputeReconcileDefaults_Inferred_DecemberWrap(t *testing.T) {
	db := testReconcileDefaultsDB(t)
	coID := seedRecDefCompany(t, db, "DecWrap Co")
	acctID := seedBankAccount(t, db, coID, "1003")

	lastDate := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	rec := models.Reconciliation{
		CompanyID:      coID,
		AccountID:      acctID,
		StatementDate:  lastDate,
		EndingBalance:  decimal.RequireFromString("200.00"),
		ClearedBalance: decimal.RequireFromString("200.00"),
	}
	db.Create(&rec)

	d, err := ComputeReconcileDefaults(db, coID, acctID)
	if err != nil {
		t.Fatal(err)
	}
	// Next month-end after 2025-12-31 → 2026-01-31.
	if d.StatementDate != "2026-01-31" {
		t.Errorf("StatementDate: want 2026-01-31, got %q", d.StatementDate)
	}
}

// 3. First reconciliation — blank; no date, no balance.
func TestComputeReconcileDefaults_Blank(t *testing.T) {
	db := testReconcileDefaultsDB(t)
	coID := seedRecDefCompany(t, db, "Blank Co")
	acctID := seedBankAccount(t, db, coID, "1004")

	d, err := ComputeReconcileDefaults(db, coID, acctID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != ReconcileDefaultsBlank {
		t.Fatalf("expected Blank source, got %d", d.Source)
	}
	if d.StatementDate != "" || d.EndingBalance != "" {
		t.Errorf("Blank should produce no defaults, got date=%q balance=%q", d.StatementDate, d.EndingBalance)
	}
}

// 4. Draft takes priority over a completed reconciliation for the same account.
func TestComputeReconcileDefaults_DraftBeatsCompleted(t *testing.T) {
	db := testReconcileDefaultsDB(t)
	coID := seedRecDefCompany(t, db, "Priority Co")
	acctID := seedBankAccount(t, db, coID, "1005")

	// Completed reconciliation exists.
	rec := models.Reconciliation{
		CompanyID:      coID,
		AccountID:      acctID,
		StatementDate:  time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		EndingBalance:  decimal.RequireFromString("1000.00"),
		ClearedBalance: decimal.RequireFromString("1000.00"),
	}
	db.Create(&rec)

	// Draft also exists with a different date.
	UpsertReconcileDraft(db, coID, acctID, "2026-04-15", "1200.00", "[]")

	d, err := ComputeReconcileDefaults(db, coID, acctID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != ReconcileDefaultsDraft {
		t.Fatalf("draft should win over completed reconciliation, got source %d", d.Source)
	}
	if d.StatementDate != "2026-04-15" {
		t.Errorf("want draft date 2026-04-15, got %q", d.StatementDate)
	}
}

// 5. Different bank accounts are strictly isolated.
func TestComputeReconcileDefaults_AccountIsolation(t *testing.T) {
	db := testReconcileDefaultsDB(t)
	coID := seedRecDefCompany(t, db, "Isolation Co")
	acctA := seedBankAccount(t, db, coID, "1100")
	acctB := seedBankAccount(t, db, coID, "1200")

	// Draft only for account A.
	UpsertReconcileDraft(db, coID, acctA, "2026-04-30", "999.00", "[]")

	// Completed reconciliation only for account B.
	rec := models.Reconciliation{
		CompanyID:      coID,
		AccountID:      acctB,
		StatementDate:  time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		EndingBalance:  decimal.RequireFromString("500.00"),
		ClearedBalance: decimal.RequireFromString("500.00"),
	}
	db.Create(&rec)

	// Account A: should see draft.
	dA, err := ComputeReconcileDefaults(db, coID, acctA)
	if err != nil {
		t.Fatal(err)
	}
	if dA.Source != ReconcileDefaultsDraft {
		t.Errorf("account A: expected Draft source, got %d", dA.Source)
	}

	// Account B: should see inferred (no draft), not account A's draft.
	dB, err := ComputeReconcileDefaults(db, coID, acctB)
	if err != nil {
		t.Fatal(err)
	}
	if dB.Source != ReconcileDefaultsInferred {
		t.Errorf("account B: expected Inferred source, got %d", dB.Source)
	}
	if dB.StatementDate != "2026-04-30" {
		t.Errorf("account B: want 2026-04-30, got %q", dB.StatementDate)
	}
}

// Unit test for the nextMonthEnd helper directly.
func TestNextMonthEnd(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"2026-03-31", "2026-04-30"},
		{"2026-01-15", "2026-02-28"},
		{"2025-12-31", "2026-01-31"},
		{"2024-01-31", "2024-02-29"}, // 2024 is a leap year
		{"2025-01-31", "2025-02-28"}, // 2025 is not a leap year
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			in, _ := time.Parse("2006-01-02", tc.input)
			got := nextMonthEnd(in).Format("2006-01-02")
			if got != tc.want {
				t.Errorf("nextMonthEnd(%s) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}
