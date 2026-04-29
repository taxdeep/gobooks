// 遵循project_guide.md
package services

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

const SalesOverviewCustomerLimit = 14

type SalesOverview struct {
	BaseCurrency       string
	DataUpdatedAt      time.Time
	CustomerBalances   []SalesOverviewCustomerBalance
	CashFlowMonths     []SalesOverviewCashFlowMonth
	IncomeSeries       []SalesOverviewIncomePoint
	IncomeThisMonth    decimal.Decimal
	IncomePreviousYear decimal.Decimal
	IncomeDelta        decimal.Decimal
	IncomeSeriesMax    decimal.Decimal
	CurrentMonthLabel  string
	PreviousMonthLabel string
}

type SalesOverviewCustomerBalance struct {
	CustomerID   uint
	CustomerName string
	Balance      decimal.Decimal
}

type SalesOverviewCashFlowMonth struct {
	Key       string
	Label     string
	Kind      string // actual | current | forecast
	Received  decimal.Decimal
	Projected decimal.Decimal
	Total     decimal.Decimal
}

type SalesOverviewIncomePoint struct {
	Key    string
	Label  string
	Amount decimal.Decimal
}

func BuildSalesOverview(db *gorm.DB, companyID uint, now time.Time) (SalesOverview, error) {
	if companyID == 0 {
		return SalesOverview{}, nil
	}
	now = now.UTC()
	currentMonth := monthStart(now)

	var company models.Company
	if err := db.Select("id", "base_currency_code").Where("id = ?", companyID).First(&company).Error; err != nil {
		return SalesOverview{}, err
	}
	baseCurrency := company.BaseCurrencyCode
	if baseCurrency == "" {
		baseCurrency = "CAD"
	}

	customerBalances, err := buildSalesOverviewCustomerBalances(db, companyID)
	if err != nil {
		return SalesOverview{}, err
	}
	cashFlow, err := buildSalesOverviewCashFlow(db, companyID, currentMonth)
	if err != nil {
		return SalesOverview{}, err
	}
	incomeSeries, thisMonth, previousYear, err := buildSalesOverviewIncome(db, companyID, currentMonth)
	if err != nil {
		return SalesOverview{}, err
	}

	maxIncome := decimal.Zero
	for _, point := range incomeSeries {
		if point.Amount.GreaterThan(maxIncome) {
			maxIncome = point.Amount
		}
	}

	return SalesOverview{
		BaseCurrency:       baseCurrency,
		DataUpdatedAt:      now,
		CustomerBalances:   customerBalances,
		CashFlowMonths:     cashFlow,
		IncomeSeries:       incomeSeries,
		IncomeThisMonth:    thisMonth,
		IncomePreviousYear: previousYear,
		IncomeDelta:        thisMonth.Sub(previousYear),
		IncomeSeriesMax:    maxIncome,
		CurrentMonthLabel:  currentMonth.Format("Jan 2006"),
		PreviousMonthLabel: currentMonth.AddDate(-1, 0, 0).Format("Jan 2006"),
	}, nil
}

func buildSalesOverviewCustomerBalances(db *gorm.DB, companyID uint) ([]SalesOverviewCustomerBalance, error) {
	var customers []models.Customer
	if err := db.Where("company_id = ? AND is_active = ?", companyID, true).
		Order("name asc").
		Find(&customers).Error; err != nil {
		return nil, err
	}

	var invoices []models.Invoice
	if err := db.Select("customer_id", "balance_due", "balance_due_base").
		Where("company_id = ? AND status NOT IN ? AND balance_due > 0",
			companyID,
			[]models.InvoiceStatus{models.InvoiceStatusDraft, models.InvoiceStatusVoided, models.InvoiceStatusPaid}).
		Find(&invoices).Error; err != nil {
		return nil, err
	}

	balances := make(map[uint]decimal.Decimal, len(customers))
	for _, inv := range invoices {
		balances[inv.CustomerID] = balances[inv.CustomerID].Add(salesOverviewBaseAmount(inv.BalanceDue, inv.BalanceDueBase))
	}

	rows := make([]SalesOverviewCustomerBalance, 0, len(customers))
	for _, customer := range customers {
		rows = append(rows, SalesOverviewCustomerBalance{
			CustomerID:   customer.ID,
			CustomerName: customer.Name,
			Balance:      balances[customer.ID],
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Balance.Equal(rows[j].Balance) {
			return rows[i].CustomerName < rows[j].CustomerName
		}
		return rows[i].Balance.GreaterThan(rows[j].Balance)
	})
	if len(rows) > SalesOverviewCustomerLimit {
		rows = rows[:SalesOverviewCustomerLimit]
	}
	return rows, nil
}

func buildSalesOverviewCashFlow(db *gorm.DB, companyID uint, currentMonth time.Time) ([]SalesOverviewCashFlowMonth, error) {
	start := currentMonth.AddDate(0, -10, 0)
	end := currentMonth.AddDate(0, 4, 0)

	months := make([]SalesOverviewCashFlowMonth, 0, 14)
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
		months = append(months, SalesOverviewCashFlowMonth{
			Key:   key,
			Label: m.Format("Jan 2006"),
			Kind:  kind,
		})
	}

	var receipts []models.CustomerReceipt
	if err := db.Select("receipt_date", "amount", "amount_base").
		Where("company_id = ? AND status IN ? AND receipt_date >= ? AND receipt_date < ?",
			companyID,
			[]models.CustomerReceiptStatus{
				models.CustomerReceiptStatusConfirmed,
				models.CustomerReceiptStatusPartiallyApplied,
				models.CustomerReceiptStatusFullyApplied,
			},
			start, currentMonth.AddDate(0, 1, 0)).
		Find(&receipts).Error; err != nil {
		return nil, err
	}
	for _, receipt := range receipts {
		if i, ok := index[monthKey(receipt.ReceiptDate)]; ok {
			months[i].Received = months[i].Received.Add(salesOverviewBaseAmount(receipt.Amount, receipt.AmountBase))
		}
	}

	var invoices []models.Invoice
	if err := db.Select("invoice_date", "due_date", "net_days_snapshot", "balance_due", "balance_due_base", "status").
		Where("company_id = ? AND status NOT IN ? AND balance_due > 0",
			companyID,
			[]models.InvoiceStatus{models.InvoiceStatusDraft, models.InvoiceStatusVoided, models.InvoiceStatusPaid}).
		Find(&invoices).Error; err != nil {
		return nil, err
	}
	forecastStart := currentMonth.AddDate(0, 1, 0)
	for _, inv := range invoices {
		due := salesOverviewInvoiceDueDate(inv)
		if due == nil {
			continue
		}
		dueMonth := monthStart(*due)
		if dueMonth.Before(forecastStart) || !dueMonth.Before(end) {
			continue
		}
		if i, ok := index[monthKey(dueMonth)]; ok {
			months[i].Projected = months[i].Projected.Add(salesOverviewBaseAmount(inv.BalanceDue, inv.BalanceDueBase))
		}
	}

	for i := range months {
		months[i].Total = months[i].Received.Add(months[i].Projected)
	}
	return months, nil
}

func buildSalesOverviewIncome(db *gorm.DB, companyID uint, currentMonth time.Time) ([]SalesOverviewIncomePoint, decimal.Decimal, decimal.Decimal, error) {
	seriesStart := currentMonth.AddDate(0, -11, 0)
	seriesEnd := currentMonth.AddDate(0, 1, 0)
	prevStart := currentMonth.AddDate(-1, 0, 0)
	prevEnd := prevStart.AddDate(0, 1, 0)
	queryStart := seriesStart
	if prevStart.Before(queryStart) {
		queryStart = prevStart
	}

	var invoices []models.Invoice
	if err := db.Select("invoice_date", "amount", "amount_base", "status").
		Where("company_id = ? AND status NOT IN ? AND invoice_date >= ? AND invoice_date < ?",
			companyID,
			[]models.InvoiceStatus{models.InvoiceStatusDraft, models.InvoiceStatusVoided},
			queryStart, seriesEnd).
		Find(&invoices).Error; err != nil {
		return nil, decimal.Zero, decimal.Zero, err
	}

	seriesTotals := make(map[string]decimal.Decimal, 12)
	previousYear := decimal.Zero
	for _, inv := range invoices {
		amount := salesOverviewBaseAmount(inv.Amount, inv.AmountBase)
		invMonth := monthStart(inv.InvoiceDate)
		if !invMonth.Before(seriesStart) && invMonth.Before(seriesEnd) {
			key := monthKey(invMonth)
			seriesTotals[key] = seriesTotals[key].Add(amount)
		}
		if !inv.InvoiceDate.Before(prevStart) && inv.InvoiceDate.Before(prevEnd) {
			previousYear = previousYear.Add(amount)
		}
	}

	points := make([]SalesOverviewIncomePoint, 0, 12)
	for m := seriesStart; m.Before(seriesEnd); m = m.AddDate(0, 1, 0) {
		key := monthKey(m)
		points = append(points, SalesOverviewIncomePoint{
			Key:    key,
			Label:  m.Format("Jan"),
			Amount: seriesTotals[key],
		})
	}
	thisMonth := seriesTotals[monthKey(currentMonth)]
	return points, thisMonth, previousYear, nil
}

func salesOverviewInvoiceDueDate(inv models.Invoice) *time.Time {
	if inv.DueDate != nil {
		return inv.DueDate
	}
	return models.ComputeDueDate(inv.InvoiceDate, inv.NetDaysSnapshot)
}

func salesOverviewBaseAmount(amount, base decimal.Decimal) decimal.Decimal {
	if base.GreaterThan(decimal.Zero) {
		return base
	}
	return amount
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func monthKey(t time.Time) string {
	return monthStart(t).Format("2006-01")
}
