// 遵循project_guide.md
package services

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func TestBuildExpenseOverviewAggregatesPaymentsForecastAndExpenses(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:expense_overview?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Vendor{},
		&models.Bill{},
		&models.Expense{},
		&models.JournalEntry{},
		&models.SettlementAllocation{},
	); err != nil {
		t.Fatal(err)
	}

	company := models.Company{Name: "AP Overview Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	vendorA := models.Vendor{CompanyID: company.ID, Name: "A Vendor", IsActive: true}
	vendorB := models.Vendor{CompanyID: company.ID, Name: "B Vendor", IsActive: true}
	if err := db.Create(&vendorA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&vendorB).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	currentMonth := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	nextMonthDue := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	previousYearMonth := time.Date(2025, 4, 8, 0, 0, 0, 0, time.UTC)

	bills := []models.Bill{
		{
			CompanyID: company.ID, VendorID: vendorA.ID, BillNumber: "BILL-CUR",
			BillDate: currentMonth.AddDate(0, 0, 4), Status: models.BillStatusPosted,
			Amount: decimal.NewFromInt(600), AmountBase: decimal.NewFromInt(600),
			BalanceDue: decimal.NewFromInt(450), BalanceDueBase: decimal.NewFromInt(450),
		},
		{
			CompanyID: company.ID, VendorID: vendorB.ID, BillNumber: "BILL-FUTURE",
			BillDate: currentMonth.AddDate(0, 0, 20), DueDate: &nextMonthDue, Status: models.BillStatusPartiallyPaid,
			Amount: decimal.NewFromInt(300), AmountBase: decimal.NewFromInt(300),
			BalanceDue: decimal.NewFromInt(300), BalanceDueBase: decimal.NewFromInt(300),
		},
		{
			CompanyID: company.ID, VendorID: vendorA.ID, BillNumber: "BILL-PREV",
			BillDate: previousYearMonth, Status: models.BillStatusPaid,
			Amount: decimal.NewFromInt(125), AmountBase: decimal.NewFromInt(125),
			BalanceDue: decimal.Zero, BalanceDueBase: decimal.Zero,
		},
	}
	if err := db.Create(&bills).Error; err != nil {
		t.Fatal(err)
	}

	je := models.JournalEntry{
		CompanyID: company.ID,
		EntryDate: currentMonth.AddDate(0, 0, 12),
		Status:    models.JournalEntryStatusPosted,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}
	settlement := models.SettlementAllocation{
		CompanyID:          company.ID,
		JournalEntryID:     je.ID,
		DocumentType:       models.SettlementDocBill,
		DocumentID:         bills[0].ID,
		AmountApplied:      decimal.NewFromInt(150),
		ARAPBaseReleased:   decimal.NewFromInt(150),
		BankBaseAmount:     decimal.NewFromInt(150),
		RealizedFXGainLoss: decimal.Zero,
		SettlementRate:     decimal.NewFromInt(1),
	}
	if err := db.Create(&settlement).Error; err != nil {
		t.Fatal(err)
	}

	expense := models.Expense{
		CompanyID:        company.ID,
		ExpenseNumber:    "EXP-CUR",
		ExpenseDate:      currentMonth.AddDate(0, 0, 15),
		Status:           models.ExpenseStatusPosted,
		Amount:           decimal.NewFromInt(80),
		CurrencyCode:     "CAD",
		Description:      "Card charge",
		PaymentMethod:    models.PaymentMethodCreditCard,
		PaymentReference: "CC-1",
	}
	if err := db.Create(&expense).Error; err != nil {
		t.Fatal(err)
	}

	overview, err := BuildExpenseOverview(db, company.ID, now)
	if err != nil {
		t.Fatal(err)
	}

	if overview.BaseCurrency != "CAD" {
		t.Fatalf("BaseCurrency = %q, want CAD", overview.BaseCurrency)
	}
	if len(overview.VendorBalances) < 2 {
		t.Fatalf("expected vendor balance rows, got %d", len(overview.VendorBalances))
	}
	if overview.VendorBalances[0].VendorName != "A Vendor" || !overview.VendorBalances[0].Balance.Equal(decimal.NewFromInt(450)) {
		t.Fatalf("first vendor balance = %+v, want A Vendor 450", overview.VendorBalances[0])
	}

	april := findExpenseOverviewMonth(t, overview.CashOutMonths, "2026-04")
	if !april.PaidBills.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("April paid bills = %s, want 150", april.PaidBills)
	}
	if !april.PaidExpenses.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("April paid expenses = %s, want 80", april.PaidExpenses)
	}
	may := findExpenseOverviewMonth(t, overview.CashOutMonths, "2026-05")
	if !may.Projected.Equal(decimal.NewFromInt(300)) {
		t.Fatalf("May projected = %s, want 300", may.Projected)
	}
	if !overview.ExpenseThisMonth.Equal(decimal.NewFromInt(980)) {
		t.Fatalf("ExpenseThisMonth = %s, want 980", overview.ExpenseThisMonth)
	}
	if !overview.ExpensePreviousYear.Equal(decimal.NewFromInt(125)) {
		t.Fatalf("ExpensePreviousYear = %s, want 125", overview.ExpensePreviousYear)
	}
}

func findExpenseOverviewMonth(t *testing.T, months []ExpenseOverviewCashOutMonth, key string) ExpenseOverviewCashOutMonth {
	t.Helper()
	for _, month := range months {
		if month.Key == key {
			return month
		}
	}
	t.Fatalf("month %s not found in %+v", key, months)
	return ExpenseOverviewCashOutMonth{}
}
