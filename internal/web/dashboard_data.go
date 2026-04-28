// 遵循project_guide.md
package web

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

type dashboardOverviewResponse struct {
	RangeLabel   string                        `json:"range_label"`
	GeneratedAt  string                        `json:"generated_at"`
	KPIs         []dashboardKPIResponse        `json:"kpis"`
	RevenueTrend []dashboardTrendResponse      `json:"revenue_trend"`
	Expenses     dashboardExpensesResponse     `json:"expenses"`
	BankAccounts []dashboardBankResponse       `json:"bank_accounts"`
	Tasks        []dashboardTaskResponse       `json:"tasks"`
	Suggestions  []dashboardSuggestionResponse `json:"suggestions"`
	Widgets      []dashboardWidgetResponse     `json:"widgets"`
}

type dashboardKPIResponse struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Value      string `json:"value"`
	IsPositive bool   `json:"is_positive"`
	Hint       string `json:"hint"`
	Tone       string `json:"tone"`
	Href       string `json:"href,omitempty"`
}

type dashboardTrendResponse struct {
	Label      string `json:"label"`
	Revenue    string `json:"revenue"`
	IsPositive bool   `json:"is_positive"`
}

type dashboardExpensesResponse struct {
	Total    dashboardMoneyResponse     `json:"total"`
	TopLines []dashboardExpenseResponse `json:"top_lines"`
}

type dashboardMoneyResponse struct {
	Value      string `json:"value"`
	IsPositive bool   `json:"is_positive"`
}

type dashboardExpenseResponse struct {
	Account string                 `json:"account"`
	Amount  dashboardMoneyResponse `json:"amount"`
}

type dashboardBankResponse struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type dashboardTaskResponse struct {
	ID          string         `json:"id"`
	TaskType    string         `json:"task_type"`
	Title       string         `json:"title"`
	Reason      string         `json:"reason"`
	Priority    string         `json:"priority"`
	Status      string         `json:"status"`
	ActionURL   string         `json:"action_url"`
	DueDate     string         `json:"due_date,omitempty"`
	Evidence    map[string]any `json:"evidence,omitempty"`
	AIGenerated bool           `json:"ai_generated"`
}

type dashboardSuggestionResponse struct {
	ID         string         `json:"id"`
	WidgetKey  string         `json:"widget_key"`
	Title      string         `json:"title"`
	Reason     string         `json:"reason"`
	Status     string         `json:"status"`
	Source     string         `json:"source"`
	Confidence string         `json:"confidence"`
	Evidence   map[string]any `json:"evidence,omitempty"`
}

type dashboardWidgetResponse struct {
	ID        string         `json:"id"`
	WidgetKey string         `json:"widget_key"`
	Title     string         `json:"title"`
	Source    string         `json:"source"`
	Position  *int           `json:"position,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
}

func buildDashboardVM(db *gorm.DB, companyID uint) pages.DashboardVM {
	now := time.Now()
	fromDate := now.AddDate(0, 0, -30)
	toDate := now

	toMoneyVM := func(d decimal.Decimal) pages.MoneyVM {
		return pages.MoneyVM{
			Value:      pages.Money(d),
			IsPositive: d.GreaterThanOrEqual(decimal.Zero),
		}
	}

	vm := pages.DashboardVM{
		HasCompany:   true,
		RangeLabel:   "Last 30 days",
		ReportFrom:   fromDate.Format("2006-01-02"),
		ReportTo:     toDate.Format("2006-01-02"),
		RevenueTrend: []pages.RevenueTrendPointVM{},
	}

	if report, err := services.IncomeStatementReport(db, companyID, fromDate, toDate); err == nil {
		vm.PnL.Revenue = toMoneyVM(report.TotalRevenue)
		vm.PnL.Expenses = toMoneyVM(report.TotalExpenses)
		vm.PnL.NetIncome = toMoneyVM(report.NetIncome)
		vm.Expenses.Total = vm.PnL.Expenses

		top := report.Expenses
		if len(top) > 6 {
			top = top[:6]
		}
		vm.Expenses.TopLines = make([]pages.ExpenseLineVM, 0, len(top))
		for _, line := range top {
			vm.Expenses.TopLines = append(vm.Expenses.TopLines, pages.ExpenseLineVM{
				Account: line.Name,
				Amount:  toMoneyVM(line.Amount),
			})
		}
	}

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	for i := 2; i >= 0; i-- {
		ms := monthStart.AddDate(0, -i, 0)
		me := ms.AddDate(0, 1, -1)
		report, err := services.IncomeStatementReport(db, companyID, ms, me)
		if err != nil {
			continue
		}
		vm.RevenueTrend = append(vm.RevenueTrend, pages.RevenueTrendPointVM{
			Label: ms.Format("2006-01"),
			Revenue: pages.MoneyVM{
				Value:      pages.Money(report.TotalRevenue),
				IsPositive: report.TotalRevenue.GreaterThanOrEqual(decimal.Zero),
			},
		})
	}

	var assetAccounts []models.Account
	if err := db.Where("company_id = ? AND root_account_type = ?", companyID, models.RootAsset).Order("code asc").Limit(50).Find(&assetAccounts).Error; err == nil {
		bankAccounts := make([]models.Account, 0, len(assetAccounts))
		for _, account := range assetAccounts {
			if account.DetailAccountType == models.DetailBank || strings.Contains(strings.ToLower(account.Name), "bank") {
				bankAccounts = append(bankAccounts, account)
			}
		}
		if len(bankAccounts) == 0 {
			bankAccounts = assetAccounts
		}
		if len(bankAccounts) > 5 {
			bankAccounts = bankAccounts[:5]
		}
		vm.BankAccounts = make([]pages.BankAccountVM, 0, len(bankAccounts))
		for _, account := range bankAccounts {
			vm.BankAccounts = append(vm.BankAccounts, pages.BankAccountVM{
				Code: account.Code,
				Name: account.Name,
			})
		}
	}

	return vm
}

func buildDashboardOverview(db *gorm.DB, companyID uint, userID *uuid.UUID) (dashboardOverviewResponse, error) {
	vm := buildDashboardVM(db, companyID)

	tasks, err := dashboardOpenTasks(db, companyID, userID)
	if err != nil {
		return dashboardOverviewResponse{}, err
	}
	suggestions, err := dashboardPendingSuggestions(db, companyID, userID)
	if err != nil {
		return dashboardOverviewResponse{}, err
	}
	widgets, err := dashboardActiveWidgets(db, companyID, userID)
	if err != nil {
		return dashboardOverviewResponse{}, err
	}

	return dashboardOverviewResponse{
		RangeLabel:   vm.RangeLabel,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		KPIs:         dashboardKPIResponses(vm, tasks, suggestions),
		RevenueTrend: dashboardTrendResponses(vm.RevenueTrend),
		Expenses:     dashboardExpenses(vm.Expenses),
		BankAccounts: dashboardBanks(vm.BankAccounts),
		Tasks:        dashboardTasks(tasks),
		Suggestions:  dashboardSuggestions(suggestions),
		Widgets:      dashboardWidgets(widgets),
	}, nil
}

func dashboardOpenTasks(db *gorm.DB, companyID uint, userID *uuid.UUID) ([]models.ActionCenterTask, error) {
	statuses := []string{models.ActionTaskStatusOpen, models.ActionTaskStatusInProgress}
	q := db.Where("company_id = ? AND status IN ?", companyID, statuses)
	if userID != nil {
		q = q.Where("(assigned_user_id IS NULL OR assigned_user_id = ?)", *userID)
	} else {
		q = q.Where("assigned_user_id IS NULL")
	}
	var rows []models.ActionCenterTask
	err := q.Order("CASE priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 ELSE 1 END DESC, due_date ASC, created_at DESC").Limit(20).Find(&rows).Error
	return rows, err
}

func dashboardPendingSuggestions(db *gorm.DB, companyID uint, userID *uuid.UUID) ([]models.DashboardWidgetSuggestion, error) {
	q := db.Where("company_id = ? AND status = ?", companyID, models.DashboardSuggestionPending)
	if userID != nil {
		q = q.Where("(user_id IS NULL OR user_id = ?)", *userID)
	} else {
		q = q.Where("user_id IS NULL")
	}
	var rows []models.DashboardWidgetSuggestion
	err := q.Order("created_at DESC").Limit(20).Find(&rows).Error
	return rows, err
}

func dashboardActiveWidgets(db *gorm.DB, companyID uint, userID *uuid.UUID) ([]models.DashboardUserWidget, error) {
	q := db.Where("company_id = ? AND active = ?", companyID, true)
	if userID != nil {
		q = q.Where("(user_id IS NULL OR user_id = ?)", *userID)
	} else {
		q = q.Where("user_id IS NULL")
	}
	var rows []models.DashboardUserWidget
	err := q.Order("COALESCE(position, 999999) ASC, created_at ASC").Limit(50).Find(&rows).Error
	return rows, err
}

func dashboardKPIResponses(vm pages.DashboardVM, tasks []models.ActionCenterTask, suggestions []models.DashboardWidgetSuggestion) []dashboardKPIResponse {
	overdueCount := 0
	billsDueCount := 0
	for _, task := range tasks {
		switch task.TaskType {
		case "invoices_overdue":
			overdueCount++
		case "bills_due_soon":
			billsDueCount++
		}
	}
	return []dashboardKPIResponse{
		{Key: "revenue", Label: "Revenue", Value: vm.PnL.Revenue.Value, IsPositive: vm.PnL.Revenue.IsPositive, Hint: "last 30d", Tone: "success", Href: dashboardIncomeStatementURL(vm.ReportFrom, vm.ReportTo, "revenue")},
		{Key: "expenses", Label: "Expenses", Value: vm.PnL.Expenses.Value, IsPositive: vm.PnL.Expenses.IsPositive, Hint: "last 30d", Tone: "danger", Href: dashboardIncomeStatementURL(vm.ReportFrom, vm.ReportTo, "expenses")},
		{Key: "net_income", Label: "Net Income", Value: vm.PnL.NetIncome.Value, IsPositive: vm.PnL.NetIncome.IsPositive, Hint: "last 30d", Tone: "primary", Href: dashboardIncomeStatementURL(vm.ReportFrom, vm.ReportTo, "net-income")},
		{Key: "attention", Label: "Attention", Value: decimal.NewFromInt(int64(len(tasks) + len(suggestions))).String(), IsPositive: len(tasks)+len(suggestions) == 0, Hint: "tasks + suggestions", Tone: "warning"},
		{Key: "overdue_invoices", Label: "Overdue invoices", Value: decimal.NewFromInt(int64(overdueCount)).String(), IsPositive: overdueCount == 0, Hint: "AR aging", Tone: "danger", Href: "/reports/ar-aging"},
		{Key: "bills_due", Label: "Bills due", Value: decimal.NewFromInt(int64(billsDueCount)).String(), IsPositive: billsDueCount == 0, Hint: "AP aging", Tone: "warning", Href: "/ap-aging"},
	}
}

func dashboardIncomeStatementURL(fromDate, toDate, anchor string) string {
	href := "/reports/income-statement?from=" + fromDate + "&to=" + toDate
	if anchor != "" {
		href += "#" + anchor
	}
	return href
}

func dashboardTrendResponses(points []pages.RevenueTrendPointVM) []dashboardTrendResponse {
	out := make([]dashboardTrendResponse, 0, len(points))
	for _, point := range points {
		out = append(out, dashboardTrendResponse{Label: point.Label, Revenue: point.Revenue.Value, IsPositive: point.Revenue.IsPositive})
	}
	return out
}

func dashboardExpenses(expenses pages.ExpensesVM) dashboardExpensesResponse {
	lines := make([]dashboardExpenseResponse, 0, len(expenses.TopLines))
	for _, line := range expenses.TopLines {
		lines = append(lines, dashboardExpenseResponse{
			Account: line.Account,
			Amount:  dashboardMoneyResponse{Value: line.Amount.Value, IsPositive: line.Amount.IsPositive},
		})
	}
	return dashboardExpensesResponse{
		Total:    dashboardMoneyResponse{Value: expenses.Total.Value, IsPositive: expenses.Total.IsPositive},
		TopLines: lines,
	}
}

func dashboardBanks(accounts []pages.BankAccountVM) []dashboardBankResponse {
	out := make([]dashboardBankResponse, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, dashboardBankResponse{Code: account.Code, Name: account.Name})
	}
	return out
}

func dashboardTasks(tasks []models.ActionCenterTask) []dashboardTaskResponse {
	out := make([]dashboardTaskResponse, 0, len(tasks))
	for _, task := range tasks {
		dueDate := ""
		if task.DueDate != nil {
			dueDate = task.DueDate.Format("2006-01-02")
		}
		out = append(out, dashboardTaskResponse{
			ID:          task.ID.String(),
			TaskType:    task.TaskType,
			Title:       task.Title,
			Reason:      task.Reason,
			Priority:    task.Priority,
			Status:      task.Status,
			ActionURL:   task.ActionURL,
			DueDate:     dueDate,
			Evidence:    jsonObject(task.EvidenceJSON),
			AIGenerated: task.AIGenerated,
		})
	}
	return out
}

func dashboardSuggestions(suggestions []models.DashboardWidgetSuggestion) []dashboardSuggestionResponse {
	out := make([]dashboardSuggestionResponse, 0, len(suggestions))
	for _, suggestion := range suggestions {
		out = append(out, dashboardSuggestionResponse{
			ID:         suggestion.ID.String(),
			WidgetKey:  suggestion.WidgetKey,
			Title:      suggestion.Title,
			Reason:     suggestion.Reason,
			Status:     suggestion.Status,
			Source:     suggestion.Source,
			Confidence: suggestion.Confidence.StringFixed(2),
			Evidence:   jsonObject(suggestion.EvidenceJSON),
		})
	}
	return out
}

func dashboardWidgets(widgets []models.DashboardUserWidget) []dashboardWidgetResponse {
	out := make([]dashboardWidgetResponse, 0, len(widgets))
	for _, widget := range widgets {
		out = append(out, dashboardWidgetResponse{
			ID:        widget.ID.String(),
			WidgetKey: widget.WidgetKey,
			Title:     widget.Title,
			Source:    widget.Source,
			Position:  widget.Position,
			Config:    jsonObject(widget.ConfigJSON),
		})
	}
	return out
}

func jsonObject(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
