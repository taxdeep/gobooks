// 遵循project_guide.md
package services

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

const ExpenseOverviewVendorLimit = 14

type ExpenseOverview struct {
	BaseCurrency        string
	DataUpdatedAt       time.Time
	VendorBalances      []ExpenseOverviewVendorBalance
	CashOutMonths       []ExpenseOverviewCashOutMonth
	ExpenseSeries       []ExpenseOverviewPoint
	ExpenseThisMonth    decimal.Decimal
	ExpensePreviousYear decimal.Decimal
	ExpenseDelta        decimal.Decimal
	ExpenseSeriesMax    decimal.Decimal
	CurrentMonthLabel   string
	PreviousMonthLabel  string
}

type ExpenseOverviewVendorBalance struct {
	VendorID   uint
	VendorName string
	Balance    decimal.Decimal
}

type ExpenseOverviewCashOutMonth struct {
	Key          string
	Label        string
	Kind         string // actual | current | forecast
	PaidBills    decimal.Decimal
	PaidExpenses decimal.Decimal
	Projected    decimal.Decimal
	Total        decimal.Decimal
}

type ExpenseOverviewPoint struct {
	Key    string
	Label  string
	Amount decimal.Decimal
}

func BuildExpenseOverview(db *gorm.DB, companyID uint, now time.Time) (ExpenseOverview, error) {
	if companyID == 0 {
		return ExpenseOverview{}, nil
	}
	now = now.UTC()
	currentMonth := monthStart(now)

	var company models.Company
	if err := db.Select("id", "base_currency_code").Where("id = ?", companyID).First(&company).Error; err != nil {
		return ExpenseOverview{}, err
	}
	baseCurrency := company.BaseCurrencyCode
	if baseCurrency == "" {
		baseCurrency = "CAD"
	}

	vendorBalances, err := buildExpenseOverviewVendorBalances(db, companyID)
	if err != nil {
		return ExpenseOverview{}, err
	}
	cashOut, err := buildExpenseOverviewCashOut(db, companyID, currentMonth)
	if err != nil {
		return ExpenseOverview{}, err
	}
	expenseSeries, thisMonth, previousYear, err := buildExpenseOverviewExpenseSeries(db, companyID, currentMonth)
	if err != nil {
		return ExpenseOverview{}, err
	}

	maxExpense := decimal.Zero
	for _, point := range expenseSeries {
		if point.Amount.GreaterThan(maxExpense) {
			maxExpense = point.Amount
		}
	}

	return ExpenseOverview{
		BaseCurrency:        baseCurrency,
		DataUpdatedAt:       now,
		VendorBalances:      vendorBalances,
		CashOutMonths:       cashOut,
		ExpenseSeries:       expenseSeries,
		ExpenseThisMonth:    thisMonth,
		ExpensePreviousYear: previousYear,
		ExpenseDelta:        thisMonth.Sub(previousYear),
		ExpenseSeriesMax:    maxExpense,
		CurrentMonthLabel:   currentMonth.Format("Jan 2006"),
		PreviousMonthLabel:  currentMonth.AddDate(-1, 0, 0).Format("Jan 2006"),
	}, nil
}

func buildExpenseOverviewVendorBalances(db *gorm.DB, companyID uint) ([]ExpenseOverviewVendorBalance, error) {
	var vendors []models.Vendor
	if err := db.Where("company_id = ? AND is_active = ?", companyID, true).
		Order("name asc").
		Find(&vendors).Error; err != nil {
		return nil, err
	}

	var bills []models.Bill
	if err := db.Select("vendor_id", "balance_due", "balance_due_base").
		Where("company_id = ? AND status IN ? AND balance_due > 0",
			companyID,
			[]models.BillStatus{models.BillStatusPosted, models.BillStatusPartiallyPaid}).
		Find(&bills).Error; err != nil {
		return nil, err
	}

	balances := make(map[uint]decimal.Decimal, len(vendors))
	for _, bill := range bills {
		balances[bill.VendorID] = balances[bill.VendorID].Add(expenseOverviewBaseAmount(bill.BalanceDue, bill.BalanceDueBase))
	}

	rows := make([]ExpenseOverviewVendorBalance, 0, len(vendors))
	for _, vendor := range vendors {
		rows = append(rows, ExpenseOverviewVendorBalance{
			VendorID:   vendor.ID,
			VendorName: vendor.Name,
			Balance:    balances[vendor.ID],
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Balance.Equal(rows[j].Balance) {
			return rows[i].VendorName < rows[j].VendorName
		}
		return rows[i].Balance.GreaterThan(rows[j].Balance)
	})
	if len(rows) > ExpenseOverviewVendorLimit {
		rows = rows[:ExpenseOverviewVendorLimit]
	}
	return rows, nil
}

func buildExpenseOverviewCashOut(db *gorm.DB, companyID uint, currentMonth time.Time) ([]ExpenseOverviewCashOutMonth, error) {
	start := currentMonth.AddDate(0, -10, 0)
	end := currentMonth.AddDate(0, 4, 0)

	months := make([]ExpenseOverviewCashOutMonth, 0, 14)
	index := make(map[string]int, 14)
	for m := start; m.Before(end); m = m.AddDate(0, 1, 0) {
		kind := "actual"
		if m.Equal(currentMonth) {
			kind = "current"
		} else if m.After(currentMonth) {
			kind = "forecast"
		}
		key := monthKey(m)
		index[key] = len(months)
		months = append(months, ExpenseOverviewCashOutMonth{
			Key:   key,
			Label: m.Format("Jan 2006"),
			Kind:  kind,
		})
	}

	var settlements []struct {
		EntryDate      time.Time
		BankBaseAmount decimal.Decimal
	}
	if err := db.Table("settlement_allocations AS sa").
		Select("je.entry_date, sa.bank_base_amount").
		Joins("JOIN journal_entries AS je ON je.id = sa.journal_entry_id").
		Where("sa.company_id = ? AND sa.document_type = ? AND je.status = ? AND je.entry_date >= ? AND je.entry_date < ?",
			companyID,
			models.SettlementDocBill,
			models.JournalEntryStatusPosted,
			start, currentMonth.AddDate(0, 1, 0)).
		Scan(&settlements).Error; err != nil {
		return nil, err
	}
	for _, settlement := range settlements {
		if i, ok := index[monthKey(settlement.EntryDate)]; ok {
			months[i].PaidBills = months[i].PaidBills.Add(settlement.BankBaseAmount)
		}
	}

	var expenses []models.Expense
	if err := db.Select("expense_date", "amount").
		Where("company_id = ? AND status = ? AND expense_date >= ? AND expense_date < ?",
			companyID,
			models.ExpenseStatusPosted,
			start, currentMonth.AddDate(0, 1, 0)).
		Find(&expenses).Error; err != nil {
		return nil, err
	}
	for _, expense := range expenses {
		if i, ok := index[monthKey(expense.ExpenseDate)]; ok {
			months[i].PaidExpenses = months[i].PaidExpenses.Add(expense.Amount)
		}
	}

	var bills []models.Bill
	if err := db.Select("bill_date", "due_date", "net_days_snapshot", "balance_due", "balance_due_base").
		Where("company_id = ? AND status IN ? AND balance_due > 0",
			companyID,
			[]models.BillStatus{models.BillStatusPosted, models.BillStatusPartiallyPaid}).
		Find(&bills).Error; err != nil {
		return nil, err
	}
	forecastStart := currentMonth.AddDate(0, 1, 0)
	for _, bill := range bills {
		due := expenseOverviewBillDueDate(bill)
		if due == nil {
			continue
		}
		dueMonth := monthStart(*due)
		if dueMonth.Before(forecastStart) || !dueMonth.Before(end) {
			continue
		}
		if i, ok := index[monthKey(dueMonth)]; ok {
			months[i].Projected = months[i].Projected.Add(expenseOverviewBaseAmount(bill.BalanceDue, bill.BalanceDueBase))
		}
	}

	for i := range months {
		months[i].Total = months[i].PaidBills.Add(months[i].PaidExpenses).Add(months[i].Projected)
	}
	return months, nil
}

func buildExpenseOverviewExpenseSeries(db *gorm.DB, companyID uint, currentMonth time.Time) ([]ExpenseOverviewPoint, decimal.Decimal, decimal.Decimal, error) {
	seriesStart := currentMonth.AddDate(0, -11, 0)
	seriesEnd := currentMonth.AddDate(0, 1, 0)
	prevStart := currentMonth.AddDate(-1, 0, 0)
	prevEnd := prevStart.AddDate(0, 1, 0)
	queryStart := seriesStart
	if prevStart.Before(queryStart) {
		queryStart = prevStart
	}

	seriesTotals := make(map[string]decimal.Decimal, 12)
	previousYear := decimal.Zero

	var bills []models.Bill
	if err := db.Select("bill_date", "amount", "amount_base").
		Where("company_id = ? AND status NOT IN ? AND bill_date >= ? AND bill_date < ?",
			companyID,
			[]models.BillStatus{models.BillStatusDraft, models.BillStatusVoided},
			queryStart, seriesEnd).
		Find(&bills).Error; err != nil {
		return nil, decimal.Zero, decimal.Zero, err
	}
	for _, bill := range bills {
		amount := expenseOverviewBaseAmount(bill.Amount, bill.AmountBase)
		billMonth := monthStart(bill.BillDate)
		if !billMonth.Before(seriesStart) && billMonth.Before(seriesEnd) {
			key := monthKey(billMonth)
			seriesTotals[key] = seriesTotals[key].Add(amount)
		}
		if !bill.BillDate.Before(prevStart) && bill.BillDate.Before(prevEnd) {
			previousYear = previousYear.Add(amount)
		}
	}

	var expenses []models.Expense
	if err := db.Select("expense_date", "amount").
		Where("company_id = ? AND status = ? AND expense_date >= ? AND expense_date < ?",
			companyID,
			models.ExpenseStatusPosted,
			queryStart, seriesEnd).
		Find(&expenses).Error; err != nil {
		return nil, decimal.Zero, decimal.Zero, err
	}
	for _, expense := range expenses {
		amount := expense.Amount
		expenseMonth := monthStart(expense.ExpenseDate)
		if !expenseMonth.Before(seriesStart) && expenseMonth.Before(seriesEnd) {
			key := monthKey(expenseMonth)
			seriesTotals[key] = seriesTotals[key].Add(amount)
		}
		if !expense.ExpenseDate.Before(prevStart) && expense.ExpenseDate.Before(prevEnd) {
			previousYear = previousYear.Add(amount)
		}
	}

	points := make([]ExpenseOverviewPoint, 0, 12)
	for m := seriesStart; m.Before(seriesEnd); m = m.AddDate(0, 1, 0) {
		key := monthKey(m)
		points = append(points, ExpenseOverviewPoint{
			Key:    key,
			Label:  m.Format("Jan"),
			Amount: seriesTotals[key],
		})
	}
	thisMonth := seriesTotals[monthKey(currentMonth)]
	return points, thisMonth, previousYear, nil
}

func expenseOverviewBillDueDate(bill models.Bill) *time.Time {
	if bill.DueDate != nil {
		return bill.DueDate
	}
	return models.ComputeDueDate(bill.BillDate, bill.NetDaysSnapshot)
}

func expenseOverviewBaseAmount(amount, base decimal.Decimal) decimal.Decimal {
	if base.GreaterThan(decimal.Zero) {
		return base
	}
	return amount
}
