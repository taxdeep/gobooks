package services

import (
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// CurrencyTotal is an operational visibility total in one document currency.
// It intentionally does not perform FX normalization or accounting translation.
type CurrencyTotal struct {
	CurrencyCode string
	Amount       decimal.Decimal
}

// UnbilledWorkReport is the read-only visibility model for currently draftable
// billable work. It mirrors the existing Draft Generator source rules without
// performing any mutations.
type UnbilledWorkReport struct {
	Tasks                 []models.Task
	Expenses              []models.Expense
	BillLines             []models.BillLine
	TaskLaborTotals       []CurrencyTotal
	BillableExpenseTotals []CurrencyTotal
	TotalUnbilledTotals   []CurrencyTotal
}

// CustomerBillableSummary is a minimal per-customer rollup of current
// draftable work. Totals are grouped by document currency for safe display.
type CustomerBillableSummary struct {
	CustomerID              uint
	UnbilledTaskLabor       []CurrencyTotal
	UnbilledBillableExpense []CurrencyTotal
	TotalUnbilled           []CurrencyTotal
	LastBillableWorkDate    *time.Time
}

// TaskInvoiceTraceRow is one historical or active task_invoice_sources record
// formatted for UI trace pages.
type TaskInvoiceTraceRow struct {
	SourceType     models.TaskInvoiceSourceType
	SourceID       uint
	SourceLabel    string
	InvoiceID      *uint
	InvoiceLineID  *uint
	InvoiceNumber  string
	InvoiceStatus  models.InvoiceStatus
	Payment        InvoicePaymentVisibility
	AmountSnapshot decimal.Decimal
	CurrencyCode   string
	CreatedAt      time.Time
	IsActive       bool
}

// TaskBillingTrace explains how a task and its linked billable costs have been
// invoiced over time. task_invoice_sources is the audit truth; task/expense/
// bill-line cache fields are only used as quick lookup.
type TaskBillingTrace struct {
	StateLabel            string
	StateDetail           string
	CurrentTaskInvoiceID  *uint
	CurrentTaskInvoiceNum string
	CurrentTaskStatus     models.InvoiceStatus
	CurrentTaskPayment    InvoicePaymentVisibility
	HasAnyActiveLinkage   bool
	HasHistoricalLinkage  bool
	TaskHistory           []TaskInvoiceTraceRow
	ExpenseHistory        []TaskInvoiceTraceRow
	BillLineHistory       []TaskInvoiceTraceRow
}

func ListUnbilledWork(db *gorm.DB, companyID uint, customerID *uint) (*UnbilledWorkReport, error) {
	baseCurrency, err := companyBaseCurrencyCode(db, companyID)
	if err != nil {
		return nil, err
	}
	tasks, err := listUnbilledTasks(db, companyID, customerID)
	if err != nil {
		return nil, err
	}
	expenses, err := listUnbilledExpenses(db, companyID, customerID)
	if err != nil {
		return nil, err
	}
	billLines, err := listUnbilledBillLines(db, companyID, customerID)
	if err != nil {
		return nil, err
	}

	report := &UnbilledWorkReport{
		Tasks:     tasks,
		Expenses:  expenses,
		BillLines: billLines,
	}
	for _, task := range tasks {
		currency := normalizeVisibilityCurrency(task.CurrencyCode, baseCurrency)
		report.TaskLaborTotals = addCurrencyTotal(report.TaskLaborTotals, currency, task.BillableAmount())
		report.TotalUnbilledTotals = addCurrencyTotal(report.TotalUnbilledTotals, currency, task.BillableAmount())
	}
	for _, exp := range expenses {
		currency := normalizeVisibilityCurrency(exp.CurrencyCode, baseCurrency)
		report.BillableExpenseTotals = addCurrencyTotal(report.BillableExpenseTotals, currency, exp.Amount)
		report.TotalUnbilledTotals = addCurrencyTotal(report.TotalUnbilledTotals, currency, exp.Amount)
	}
	for _, line := range billLines {
		currency := billLineCurrencyCode(line, baseCurrency)
		report.BillableExpenseTotals = addCurrencyTotal(report.BillableExpenseTotals, currency, line.LineNet)
		report.TotalUnbilledTotals = addCurrencyTotal(report.TotalUnbilledTotals, currency, line.LineNet)
	}
	return report, nil
}

func ListCustomerBillableSummaries(db *gorm.DB, companyID uint) (map[uint]CustomerBillableSummary, error) {
	baseCurrency, err := companyBaseCurrencyCode(db, companyID)
	if err != nil {
		return nil, err
	}
	tasks, err := listUnbilledTasks(db, companyID, nil)
	if err != nil {
		return nil, err
	}
	expenses, err := listUnbilledExpenses(db, companyID, nil)
	if err != nil {
		return nil, err
	}
	billLines, err := listUnbilledBillLines(db, companyID, nil)
	if err != nil {
		return nil, err
	}

	out := map[uint]CustomerBillableSummary{}
	for _, task := range tasks {
		summary := out[task.CustomerID]
		summary.CustomerID = task.CustomerID
		currency := normalizeVisibilityCurrency(task.CurrencyCode, baseCurrency)
		summary.UnbilledTaskLabor = addCurrencyTotal(summary.UnbilledTaskLabor, currency, task.BillableAmount())
		summary.TotalUnbilled = addCurrencyTotal(summary.TotalUnbilled, currency, task.BillableAmount())
		summary.LastBillableWorkDate = latestDate(summary.LastBillableWorkDate, task.TaskDate)
		out[task.CustomerID] = summary
	}
	for _, exp := range expenses {
		customerID := expenseCustomerID(exp)
		if customerID == 0 {
			continue
		}
		summary := out[customerID]
		summary.CustomerID = customerID
		currency := normalizeVisibilityCurrency(exp.CurrencyCode, baseCurrency)
		summary.UnbilledBillableExpense = addCurrencyTotal(summary.UnbilledBillableExpense, currency, exp.Amount)
		summary.TotalUnbilled = addCurrencyTotal(summary.TotalUnbilled, currency, exp.Amount)
		summary.LastBillableWorkDate = latestDate(summary.LastBillableWorkDate, exp.ExpenseDate)
		out[customerID] = summary
	}
	for _, line := range billLines {
		customerID := billLineCustomerID(line)
		if customerID == 0 {
			continue
		}
		summary := out[customerID]
		summary.CustomerID = customerID
		currency := billLineCurrencyCode(line, baseCurrency)
		summary.UnbilledBillableExpense = addCurrencyTotal(summary.UnbilledBillableExpense, currency, line.LineNet)
		summary.TotalUnbilled = addCurrencyTotal(summary.TotalUnbilled, currency, line.LineNet)
		if line.Bill != nil {
			summary.LastBillableWorkDate = latestDate(summary.LastBillableWorkDate, line.Bill.BillDate)
		}
		out[customerID] = summary
	}
	return out, nil
}

func GetTaskBillingTrace(db *gorm.DB, companyID, taskID uint) (*TaskBillingTrace, error) {
	baseCurrency, err := companyBaseCurrencyCode(db, companyID)
	if err != nil {
		return nil, err
	}
	task, err := GetTaskByID(db, companyID, taskID)
	if err != nil {
		return nil, err
	}

	var expenses []models.Expense
	if err := db.
		Preload("Invoice").
		Where("company_id = ? AND task_id = ?", companyID, taskID).
		Order("expense_date desc, id desc").
		Find(&expenses).Error; err != nil {
		return nil, err
	}

	var billLines []models.BillLine
	if err := db.
		Preload("Bill").
		Preload("Invoice").
		Where("company_id = ? AND task_id = ?", companyID, taskID).
		Order("bill_id desc, sort_order asc, id desc").
		Find(&billLines).Error; err != nil {
		return nil, err
	}

	trace := &TaskBillingTrace{}

	taskRows, err := listTaskInvoiceTraceRows(db, companyID, models.TaskInvoiceSourceTask, []uint{taskID}, map[uint]string{taskID: task.Title}, map[uint]string{taskID: normalizeVisibilityCurrency(task.CurrencyCode, baseCurrency)})
	if err != nil {
		return nil, err
	}
	trace.TaskHistory = taskRows

	expenseIDs := make([]uint, 0, len(expenses))
	expenseLabels := make(map[uint]string, len(expenses))
	expenseCurrencies := make(map[uint]string, len(expenses))
	for _, exp := range expenses {
		expenseIDs = append(expenseIDs, exp.ID)
		expenseLabels[exp.ID] = exp.Description
		expenseCurrencies[exp.ID] = normalizeVisibilityCurrency(exp.CurrencyCode, baseCurrency)
	}
	if len(expenseIDs) > 0 {
		rows, err := listTaskInvoiceTraceRows(db, companyID, models.TaskInvoiceSourceExpense, expenseIDs, expenseLabels, expenseCurrencies)
		if err != nil {
			return nil, err
		}
		trace.ExpenseHistory = rows
	}

	billLineIDs := make([]uint, 0, len(billLines))
	billLineLabels := make(map[uint]string, len(billLines))
	billLineCurrencies := make(map[uint]string, len(billLines))
	for _, line := range billLines {
		billLineIDs = append(billLineIDs, line.ID)
		billLineLabels[line.ID] = line.Description
		billLineCurrencies[line.ID] = billLineCurrencyCode(line, baseCurrency)
	}
	if len(billLineIDs) > 0 {
		rows, err := listTaskInvoiceTraceRows(db, companyID, models.TaskInvoiceSourceBillLine, billLineIDs, billLineLabels, billLineCurrencies)
		if err != nil {
			return nil, err
		}
		trace.BillLineHistory = rows
	}

	taskActive := firstActiveTraceRow(trace.TaskHistory)
	anyActive := taskActive != nil || firstActiveTraceRow(trace.ExpenseHistory) != nil || firstActiveTraceRow(trace.BillLineHistory) != nil
	trace.HasAnyActiveLinkage = anyActive
	trace.HasHistoricalLinkage = hasHistoricalTraceRows(trace.TaskHistory) || hasHistoricalTraceRows(trace.ExpenseHistory) || hasHistoricalTraceRows(trace.BillLineHistory)
	if taskActive != nil {
		trace.CurrentTaskInvoiceID = taskActive.InvoiceID
		trace.CurrentTaskInvoiceNum = taskActive.InvoiceNumber
		trace.CurrentTaskStatus = taskActive.InvoiceStatus
		trace.CurrentTaskPayment = taskActive.Payment
	}

	switch {
	case taskActive != nil && taskActive.InvoiceStatus == models.InvoiceStatusDraft:
		trace.StateLabel = "In draft invoice"
		trace.StateDetail = "This task's labor line is currently included in a draft invoice."
	case taskActive != nil:
		trace.StateLabel = "Invoiced"
		trace.StateDetail = "This task's labor line is currently linked to an invoice."
	case anyActive:
		trace.StateLabel = "Linked costs invoiced"
		trace.StateDetail = "This task has active billable cost linkages even though the task labor itself is not currently linked."
	case trace.HasHistoricalLinkage && task.Status == models.TaskStatusCompleted && task.IsBillable:
		trace.StateLabel = "Previously invoiced"
		trace.StateDetail = "This task has historical invoice linkage and is currently available for a new draft."
	case task.Status == models.TaskStatusCompleted && task.IsBillable:
		trace.StateLabel = "Ready to invoice"
		trace.StateDetail = "Completed billable work can be included in a new invoice draft."
	case task.Status == models.TaskStatusCompleted && !task.IsBillable:
		trace.StateLabel = "Completed non-billable"
		trace.StateDetail = "This task is completed but marked non-billable."
	case task.Status == models.TaskStatusOpen:
		trace.StateLabel = "Open"
		trace.StateDetail = "Open tasks do not enter the billable draft list yet."
	case task.Status == models.TaskStatusCancelled:
		trace.StateLabel = "Cancelled"
		trace.StateDetail = "Cancelled tasks do not participate in billing."
	case task.Status == models.TaskStatusInvoiced:
		trace.StateLabel = "Invoiced"
		trace.StateDetail = "This task is currently marked invoiced."
	default:
		trace.StateLabel = string(task.Status)
	}

	return trace, nil
}

func listUnbilledTasks(db *gorm.DB, companyID uint, customerID *uint) ([]models.Task, error) {
	q := db.
		Preload("Customer").
		Where(`company_id = ? AND status = ? AND is_billable = ? AND invoice_id IS NULL AND invoice_line_id IS NULL`,
			companyID, models.TaskStatusCompleted, true)
	if customerID != nil && *customerID > 0 {
		q = q.Where("customer_id = ?", *customerID)
	}

	var tasks []models.Task
	if err := q.Order("task_date desc, id desc").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return filterTasksWithoutActiveLinkage(db, companyID, tasks)
}

func listUnbilledExpenses(db *gorm.DB, companyID uint, customerID *uint) ([]models.Expense, error) {
	q := db.
		Preload("Task.Customer").
		Preload("BillableCustomer").
		Where(`company_id = ? AND task_id IS NOT NULL AND is_billable = ? AND reinvoice_status = ? AND invoice_id IS NULL AND invoice_line_id IS NULL`,
			companyID, true, models.ReinvoiceStatusUninvoiced)
	if customerID != nil && *customerID > 0 {
		q = q.Where("billable_customer_id = ?", *customerID)
	}

	var expenses []models.Expense
	if err := q.Order("expense_date desc, id desc").Find(&expenses).Error; err != nil {
		return nil, err
	}
	return filterExpensesWithoutActiveLinkage(db, companyID, expenses)
}

func listUnbilledBillLines(db *gorm.DB, companyID uint, customerID *uint) ([]models.BillLine, error) {
	q := db.
		Preload("Task.Customer").
		Preload("Bill").
		Preload("BillableCustomer").
		Where(`company_id = ? AND task_id IS NOT NULL AND is_billable = ? AND reinvoice_status = ? AND invoice_id IS NULL AND invoice_line_id IS NULL`,
			companyID, true, models.ReinvoiceStatusUninvoiced)
	if customerID != nil && *customerID > 0 {
		q = q.Where("billable_customer_id = ?", *customerID)
	}

	var lines []models.BillLine
	if err := q.Order("bill_id desc, sort_order asc, id desc").Find(&lines).Error; err != nil {
		return nil, err
	}
	return filterBillLinesWithoutActiveLinkage(db, companyID, lines)
}

func filterTasksWithoutActiveLinkage(db *gorm.DB, companyID uint, tasks []models.Task) ([]models.Task, error) {
	if len(tasks) == 0 {
		return tasks, nil
	}
	ids := make([]uint, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	active, err := loadActiveSourceSet(db, companyID, models.TaskInvoiceSourceTask, ids)
	if err != nil {
		return nil, err
	}
	out := make([]models.Task, 0, len(tasks))
	for _, task := range tasks {
		if !active[task.ID] {
			out = append(out, task)
		}
	}
	return out, nil
}

func filterExpensesWithoutActiveLinkage(db *gorm.DB, companyID uint, expenses []models.Expense) ([]models.Expense, error) {
	if len(expenses) == 0 {
		return expenses, nil
	}
	ids := make([]uint, 0, len(expenses))
	for _, exp := range expenses {
		ids = append(ids, exp.ID)
	}
	active, err := loadActiveSourceSet(db, companyID, models.TaskInvoiceSourceExpense, ids)
	if err != nil {
		return nil, err
	}
	out := make([]models.Expense, 0, len(expenses))
	for _, exp := range expenses {
		if !active[exp.ID] {
			out = append(out, exp)
		}
	}
	return out, nil
}

func filterBillLinesWithoutActiveLinkage(db *gorm.DB, companyID uint, lines []models.BillLine) ([]models.BillLine, error) {
	if len(lines) == 0 {
		return lines, nil
	}
	ids := make([]uint, 0, len(lines))
	for _, line := range lines {
		ids = append(ids, line.ID)
	}
	active, err := loadActiveSourceSet(db, companyID, models.TaskInvoiceSourceBillLine, ids)
	if err != nil {
		return nil, err
	}
	out := make([]models.BillLine, 0, len(lines))
	for _, line := range lines {
		if !active[line.ID] {
			out = append(out, line)
		}
	}
	return out, nil
}

func listTaskInvoiceTraceRows(db *gorm.DB, companyID uint, sourceType models.TaskInvoiceSourceType, sourceIDs []uint, labels map[uint]string, currencies map[uint]string) ([]TaskInvoiceTraceRow, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}

	var bridges []models.TaskInvoiceSource
	if err := db.
		Where("company_id = ? AND source_type = ? AND source_id IN ?", companyID, sourceType, dedupeUintIDs(sourceIDs)).
		Order("created_at desc, id desc").
		Find(&bridges).Error; err != nil {
		return nil, err
	}
	if len(bridges) == 0 {
		return nil, nil
	}

	invoiceIDs := make([]uint, 0, len(bridges))
	for _, bridge := range bridges {
		if bridge.InvoiceID != nil && *bridge.InvoiceID > 0 {
			invoiceIDs = append(invoiceIDs, *bridge.InvoiceID)
		}
	}
	invoiceMap := map[uint]models.Invoice{}
	if len(invoiceIDs) > 0 {
		var invoices []models.Invoice
		if err := db.Select("id", "invoice_number", "status", "amount", "balance_due", "currency_code").
			Where("company_id = ? AND id IN ?", companyID, dedupeUintIDs(invoiceIDs)).
			Find(&invoices).Error; err != nil {
			return nil, err
		}
		for _, inv := range invoices {
			invoiceMap[inv.ID] = inv
		}
	}

	rows := make([]TaskInvoiceTraceRow, 0, len(bridges))
	for _, bridge := range bridges {
		row := TaskInvoiceTraceRow{
			SourceType:     bridge.SourceType,
			SourceID:       bridge.SourceID,
			SourceLabel:    labels[bridge.SourceID],
			InvoiceID:      bridge.InvoiceID,
			InvoiceLineID:  bridge.InvoiceLineID,
			AmountSnapshot: bridge.AmountSnapshot,
			CurrencyCode:   currencies[bridge.SourceID],
			CreatedAt:      bridge.CreatedAt,
			IsActive:       bridge.VoidedAt == nil,
		}
		if bridge.InvoiceID != nil {
			if invoice, ok := invoiceMap[*bridge.InvoiceID]; ok {
				row.InvoiceNumber = invoice.InvoiceNumber
				row.InvoiceStatus = invoice.Status
				row.Payment = BuildInvoicePaymentVisibility(invoice)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func firstActiveTraceRow(rows []TaskInvoiceTraceRow) *TaskInvoiceTraceRow {
	for i := range rows {
		if rows[i].IsActive {
			return &rows[i]
		}
	}
	return nil
}

func hasHistoricalTraceRows(rows []TaskInvoiceTraceRow) bool {
	for _, row := range rows {
		if !row.IsActive {
			return true
		}
	}
	return false
}

func addCurrencyTotal(totals []CurrencyTotal, currency string, amount decimal.Decimal) []CurrencyTotal {
	if amount.IsZero() {
		return totals
	}
	for i := range totals {
		if totals[i].CurrencyCode == currency {
			totals[i].Amount = totals[i].Amount.Add(amount)
			return totals
		}
	}
	totals = append(totals, CurrencyTotal{CurrencyCode: currency, Amount: amount})
	sort.SliceStable(totals, func(i, j int) bool {
		return totals[i].CurrencyCode < totals[j].CurrencyCode
	})
	return totals
}

func normalizeVisibilityCurrency(currency string, baseCurrency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		return strings.ToUpper(strings.TrimSpace(baseCurrency))
	}
	return currency
}

func expenseCustomerID(exp models.Expense) uint {
	if exp.BillableCustomerID != nil && *exp.BillableCustomerID > 0 {
		return *exp.BillableCustomerID
	}
	if exp.Task != nil {
		return exp.Task.CustomerID
	}
	return 0
}

func billLineCustomerID(line models.BillLine) uint {
	if line.BillableCustomerID != nil && *line.BillableCustomerID > 0 {
		return *line.BillableCustomerID
	}
	if line.Task != nil {
		return line.Task.CustomerID
	}
	return 0
}

func billLineCurrencyCode(line models.BillLine, baseCurrency string) string {
	if line.Bill != nil {
		return normalizeVisibilityCurrency(line.Bill.CurrencyCode, baseCurrency)
	}
	return normalizeVisibilityCurrency("", baseCurrency)
}

func latestDate(current *time.Time, candidate time.Time) *time.Time {
	if candidate.IsZero() {
		return current
	}
	candidate = candidate.UTC()
	if current == nil || candidate.After(*current) {
		copy := candidate
		return &copy
	}
	return current
}

func MergeCurrencyTotals(parts ...[]CurrencyTotal) []CurrencyTotal {
	out := make([]CurrencyTotal, 0)
	for _, set := range parts {
		for _, row := range set {
			out = addCurrencyTotal(out, row.CurrencyCode, row.Amount)
		}
	}
	return out
}

func HasAnyUnbilledWork(report *UnbilledWorkReport) bool {
	if report == nil {
		return false
	}
	return len(report.Tasks) > 0 || len(report.Expenses) > 0 || len(report.BillLines) > 0
}

func CustomerSummaryOrZero(summaries map[uint]CustomerBillableSummary, customerID uint) CustomerBillableSummary {
	if summaries == nil {
		return CustomerBillableSummary{CustomerID: customerID}
	}
	if summary, ok := summaries[customerID]; ok {
		return summary
	}
	return CustomerBillableSummary{CustomerID: customerID}
}

func companyBaseCurrencyCode(db *gorm.DB, companyID uint) (string, error) {
	var company models.Company
	if err := db.Select("id", "base_currency_code").Where("id = ?", companyID).First(&company).Error; err != nil {
		return "", err
	}
	return company.BaseCurrencyCode, nil
}
