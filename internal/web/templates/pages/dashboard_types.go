// 遵循project_guide.md
package pages

// MoneyVM is a formatted money value with a sign hint for coloring.
type MoneyVM struct {
	Value      string
	IsPositive bool
}

type ProfitLossVM struct {
	Revenue   MoneyVM
	Expenses  MoneyVM
	NetIncome MoneyVM
}

type ExpenseLineVM struct {
	Account string
	Amount  MoneyVM
}

type ExpensesVM struct {
	Total    MoneyVM
	TopLines []ExpenseLineVM
}

type RevenueTrendPointVM struct {
	Label   string
	Revenue MoneyVM
}

type BankAccountVM struct {
	Code string
	Name string
}

type DashboardVM struct {
	HasCompany bool
	RangeLabel string
	ReportFrom string
	ReportTo   string

	PnL          ProfitLossVM
	Expenses     ExpensesVM
	RevenueTrend []RevenueTrendPointVM
	BankAccounts []BankAccountVM
}
