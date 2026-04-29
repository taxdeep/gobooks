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

func TestBuildSalesOverviewAggregatesReceiptsForecastAndIncome(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:sales_overview?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Company{}, &models.Customer{}, &models.Invoice{}, &models.CustomerReceipt{}); err != nil {
		t.Fatal(err)
	}

	company := models.Company{Name: "Overview Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	customerA := models.Customer{CompanyID: company.ID, Name: "A Customer", IsActive: true}
	customerB := models.Customer{CompanyID: company.ID, Name: "B Customer", IsActive: true}
	if err := db.Create(&customerA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&customerB).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	currentMonth := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	nextMonthDue := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	previousYearMonth := time.Date(2025, 4, 10, 0, 0, 0, 0, time.UTC)

	invoices := []models.Invoice{
		{
			CompanyID: company.ID, CustomerID: customerA.ID, InvoiceNumber: "INV-CUR",
			InvoiceDate: currentMonth.AddDate(0, 0, 5), Status: models.InvoiceStatusIssued,
			Amount: decimal.NewFromInt(500), AmountBase: decimal.NewFromInt(500),
			BalanceDue: decimal.NewFromInt(500), BalanceDueBase: decimal.NewFromInt(500),
		},
		{
			CompanyID: company.ID, CustomerID: customerB.ID, InvoiceNumber: "INV-FUTURE",
			InvoiceDate: currentMonth.AddDate(0, 0, 20), DueDate: &nextMonthDue, Status: models.InvoiceStatusSent,
			Amount: decimal.NewFromInt(300), AmountBase: decimal.NewFromInt(300),
			BalanceDue: decimal.NewFromInt(300), BalanceDueBase: decimal.NewFromInt(300),
		},
		{
			CompanyID: company.ID, CustomerID: customerA.ID, InvoiceNumber: "INV-PREV",
			InvoiceDate: previousYearMonth, Status: models.InvoiceStatusIssued,
			Amount: decimal.NewFromInt(125), AmountBase: decimal.NewFromInt(125),
			BalanceDue: decimal.Zero, BalanceDueBase: decimal.Zero,
		},
	}
	if err := db.Create(&invoices).Error; err != nil {
		t.Fatal(err)
	}

	receipt := models.CustomerReceipt{
		CompanyID: company.ID, CustomerID: customerA.ID,
		ReceiptDate: currentMonth.AddDate(0, 0, 12),
		Status:      models.CustomerReceiptStatusConfirmed,
		Amount:      decimal.NewFromInt(250),
		AmountBase:  decimal.NewFromInt(250),
	}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatal(err)
	}

	overview, err := BuildSalesOverview(db, company.ID, now)
	if err != nil {
		t.Fatal(err)
	}

	if overview.BaseCurrency != "CAD" {
		t.Fatalf("BaseCurrency = %q, want CAD", overview.BaseCurrency)
	}
	if len(overview.CustomerBalances) < 2 {
		t.Fatalf("expected customer balance rows, got %d", len(overview.CustomerBalances))
	}
	if overview.CustomerBalances[0].CustomerName != "A Customer" || !overview.CustomerBalances[0].Balance.Equal(decimal.NewFromInt(500)) {
		t.Fatalf("first customer balance = %+v, want A Customer 500", overview.CustomerBalances[0])
	}

	april := findOverviewMonth(t, overview.CashFlowMonths, "2026-04")
	if !april.Received.Equal(decimal.NewFromInt(250)) {
		t.Fatalf("April received = %s, want 250", april.Received)
	}
	may := findOverviewMonth(t, overview.CashFlowMonths, "2026-05")
	if !may.Projected.Equal(decimal.NewFromInt(300)) {
		t.Fatalf("May projected = %s, want 300", may.Projected)
	}
	if !overview.IncomeThisMonth.Equal(decimal.NewFromInt(800)) {
		t.Fatalf("IncomeThisMonth = %s, want 800", overview.IncomeThisMonth)
	}
	if !overview.IncomePreviousYear.Equal(decimal.NewFromInt(125)) {
		t.Fatalf("IncomePreviousYear = %s, want 125", overview.IncomePreviousYear)
	}
}

func findOverviewMonth(t *testing.T, months []SalesOverviewCashFlowMonth, key string) SalesOverviewCashFlowMonth {
	t.Helper()
	for _, month := range months {
		if month.Key == key {
			return month
		}
	}
	t.Fatalf("month %s not found in %+v", key, months)
	return SalesOverviewCashFlowMonth{}
}
