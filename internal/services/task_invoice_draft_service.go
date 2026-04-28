package services

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

var (
	ErrBillableWorkCustomerRequired  = errors.New("customer is required")
	ErrBillableWorkSelectionRequired = errors.New("select at least one billable work source")
	ErrBillableWorkCurrencyMismatch  = errors.New("all selected billable work must use the same currency in this version")
	ErrBillableWorkSourceAlreadyUsed = errors.New("one or more selected sources are already linked to an active invoice")
	ErrBillableWorkTaskNotReady      = errors.New("only completed billable tasks can be invoiced")
	ErrBillableWorkExpenseNotReady   = errors.New("only task-linked billable uninvoiced expenses can be invoiced")
	ErrBillableWorkBillLineNotReady  = errors.New("only task-linked billable uninvoiced bill lines can be invoiced")
	ErrBillableWorkCustomerMismatch  = errors.New("all selected sources must belong to the selected customer")
	ErrBillableWorkSystemItemMissing = errors.New("required task billing system items are missing or inactive")
	ErrBillLineNotFound              = errors.New("bill line not found")
)

type BillableWorkCandidates struct {
	Customer  models.Customer
	Tasks     []models.Task
	Expenses  []models.Expense
	BillLines []models.BillLine
}

type GenerateInvoiceDraftInput struct {
	CompanyID   uint
	CustomerID  uint
	TaskIDs     []uint
	ExpenseIDs  []uint
	BillLineIDs []uint
	Actor       string
	UserID      *uuid.UUID
}

type GenerateInvoiceDraftResult struct {
	InvoiceID     uint
	InvoiceNumber string
	LineCount     int
}

type draftTaskSource struct {
	Task     models.Task
	Line     models.InvoiceLine
	Amount   decimal.Decimal
	Currency string
}

type draftExpenseSource struct {
	Expense  models.Expense
	Line     models.InvoiceLine
	Amount   decimal.Decimal
	Currency string
}

type draftBillLineSource struct {
	BillLine models.BillLine
	Line     models.InvoiceLine
	Amount   decimal.Decimal
	Currency string
}

func ListBillableWorkCandidates(db *gorm.DB, companyID, customerID uint) (*BillableWorkCandidates, error) {
	if customerID == 0 {
		return nil, ErrBillableWorkCustomerRequired
	}

	var customer models.Customer
	if err := db.Where("id = ? AND company_id = ?", customerID, companyID).First(&customer).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillableWorkCustomerRequired
		}
		return nil, err
	}

	out := &BillableWorkCandidates{Customer: customer}

	if err := db.
		Preload("Customer").
		Where("company_id = ? AND customer_id = ? AND status = ? AND is_billable = ? AND invoice_id IS NULL",
			companyID, customerID, models.TaskStatusCompleted, true).
		Order("task_date desc, id desc").
		Find(&out.Tasks).Error; err != nil {
		return nil, err
	}

	if err := db.
		Preload("Task.Customer").
		Preload("Vendor").
		Preload("ExpenseAccount").
		Where(`company_id = ? AND task_id IS NOT NULL AND is_billable = ? AND reinvoice_status = ? AND billable_customer_id = ? AND invoice_id IS NULL`,
			companyID, true, models.ReinvoiceStatusUninvoiced, customerID).
		Order("expense_date desc, id desc").
		Find(&out.Expenses).Error; err != nil {
		return nil, err
	}

	if err := db.
		Preload("Task.Customer").
		Preload("Bill").
		Preload("Bill.Vendor").
		Preload("ExpenseAccount").
		Where(`company_id = ? AND task_id IS NOT NULL AND is_billable = ? AND reinvoice_status = ? AND billable_customer_id = ? AND invoice_id IS NULL`,
			companyID, true, models.ReinvoiceStatusUninvoiced, customerID).
		Order("bill_id desc, sort_order asc, id desc").
		Find(&out.BillLines).Error; err != nil {
		return nil, err
	}

	return out, nil
}

func GenerateInvoiceDraft(db *gorm.DB, in GenerateInvoiceDraftInput) (*GenerateInvoiceDraftResult, error) {
	if in.CompanyID == 0 || in.CustomerID == 0 {
		return nil, ErrBillableWorkCustomerRequired
	}
	taskIDs := dedupeUintIDs(in.TaskIDs)
	expenseIDs := dedupeUintIDs(in.ExpenseIDs)
	billLineIDs := dedupeUintIDs(in.BillLineIDs)
	if len(taskIDs) == 0 && len(expenseIDs) == 0 && len(billLineIDs) == 0 {
		return nil, ErrBillableWorkSelectionRequired
	}
	if strings.TrimSpace(in.Actor) == "" {
		in.Actor = "system"
	}

	var result GenerateInvoiceDraftResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var company models.Company
		if err := tx.Select("id", "base_currency_code").Where("id = ?", in.CompanyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}

		var customer models.Customer
		if err := tx.Where("id = ? AND company_id = ?", in.CustomerID, in.CompanyID).First(&customer).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillableWorkCustomerRequired
			}
			return fmt.Errorf("load customer: %w", err)
		}

		taskItem, err := LookupSystemTaskItem(tx, in.CompanyID, "TASK_LABOR")
		if err != nil {
			return fmt.Errorf("%w: TASK_LABOR", ErrBillableWorkSystemItemMissing)
		}
		reimItem, err := LookupSystemTaskItem(tx, in.CompanyID, "TASK_REIM")
		if err != nil {
			return fmt.Errorf("%w: TASK_REIM", ErrBillableWorkSystemItemMissing)
		}

		tasks, err := loadDraftableTasks(tx, in.CompanyID, in.CustomerID, taskIDs)
		if err != nil {
			return err
		}
		expenses, err := loadDraftableExpenses(tx, in.CompanyID, in.CustomerID, expenseIDs)
		if err != nil {
			return err
		}
		billLines, err := loadDraftableBillLines(tx, in.CompanyID, in.CustomerID, billLineIDs)
		if err != nil {
			return err
		}

		taskDrafts, err := buildTaskDraftSources(tx, in.CompanyID, taskItem, tasks)
		if err != nil {
			return err
		}
		expenseDrafts, err := buildExpenseDraftSources(tx, in.CompanyID, reimItem, expenses)
		if err != nil {
			return err
		}
		billLineDrafts, err := buildBillLineDraftSources(tx, in.CompanyID, reimItem, billLines)
		if err != nil {
			return err
		}

		invoiceCurrencyCode, err := determineDraftCurrency(company.BaseCurrencyCode, taskDrafts, expenseDrafts, billLineDrafts)
		if err != nil {
			return err
		}

		exchangeRate := decimal.NewFromInt(1)
		if invoiceCurrencyCode != "" {
			// Match the existing invoice editor semantics: foreign-currency drafts
			// store 0 so posting can auto-lookup the rate later unless the user
			// overrides it before posting.
			exchangeRate = decimal.Zero
		}

		invoiceNumber, err := SuggestNextInvoiceNumber(tx, in.CompanyID)
		if err != nil {
			return fmt.Errorf("suggest invoice number: %w", err)
		}
		if err := ensureInvoiceNumberAvailable(tx, in.CompanyID, invoiceNumber); err != nil {
			return err
		}

		termSnapshot, dueDate, err := resolveDraftInvoiceTerms(tx, in.CompanyID, customer, time.Now().UTC())
		if err != nil {
			return err
		}

		subtotal := decimal.Zero
		taxTotal := decimal.Zero
		lineCount := len(taskDrafts) + len(expenseDrafts) + len(billLineDrafts)
		lines := make([]models.InvoiceLine, 0, lineCount)
		for _, src := range taskDrafts {
			lines = append(lines, src.Line)
			subtotal = subtotal.Add(src.Line.LineNet)
			taxTotal = taxTotal.Add(src.Line.LineTax)
		}
		for _, src := range expenseDrafts {
			lines = append(lines, src.Line)
			subtotal = subtotal.Add(src.Line.LineNet)
			taxTotal = taxTotal.Add(src.Line.LineTax)
		}
		for _, src := range billLineDrafts {
			lines = append(lines, src.Line)
			subtotal = subtotal.Add(src.Line.LineNet)
			taxTotal = taxTotal.Add(src.Line.LineTax)
		}
		amount := subtotal.Add(taxTotal)

		invoice := models.Invoice{
			CompanyID:               in.CompanyID,
			InvoiceNumber:           invoiceNumber,
			CustomerID:              customer.ID,
			InvoiceDate:             time.Now().UTC(),
			PaymentTermSnapshot:     termSnapshot,
			DueDate:                 dueDate,
			Status:                  models.InvoiceStatusDraft,
			Subtotal:                subtotal,
			TaxTotal:                taxTotal,
			Amount:                  amount,
			BalanceDue:              amount,
			Memo:                    "Generated from billable work",
			CurrencyCode:            invoiceCurrencyCode,
			ExchangeRate:            exchangeRate,
			CustomerNameSnapshot:    customer.Name,
			CustomerEmailSnapshot:   customer.Email,
			CustomerAddressSnapshot: customer.FormattedAddress(),
		}
		if err := tx.Create(&invoice).Error; err != nil {
			return fmt.Errorf("create invoice draft: %w", err)
		}

		sortOrder := uint(1)
		for i := range taskDrafts {
			taskDrafts[i].Line.CompanyID = in.CompanyID
			taskDrafts[i].Line.InvoiceID = invoice.ID
			taskDrafts[i].Line.SortOrder = sortOrder
			sortOrder++
			if err := tx.Create(&taskDrafts[i].Line).Error; err != nil {
				return fmt.Errorf("create task invoice line: %w", err)
			}
			if err := attachTaskSourceToInvoice(tx, in.CompanyID, invoice.ID, taskDrafts[i]); err != nil {
				return err
			}
		}
		for i := range expenseDrafts {
			expenseDrafts[i].Line.CompanyID = in.CompanyID
			expenseDrafts[i].Line.InvoiceID = invoice.ID
			expenseDrafts[i].Line.SortOrder = sortOrder
			sortOrder++
			if err := tx.Create(&expenseDrafts[i].Line).Error; err != nil {
				return fmt.Errorf("create expense invoice line: %w", err)
			}
			if err := attachExpenseSourceToInvoice(tx, in.CompanyID, invoice.ID, expenseDrafts[i]); err != nil {
				return err
			}
		}
		for i := range billLineDrafts {
			billLineDrafts[i].Line.CompanyID = in.CompanyID
			billLineDrafts[i].Line.InvoiceID = invoice.ID
			billLineDrafts[i].Line.SortOrder = sortOrder
			sortOrder++
			if err := tx.Create(&billLineDrafts[i].Line).Error; err != nil {
				return fmt.Errorf("create bill-line invoice line: %w", err)
			}
			if err := attachBillLineSourceToInvoice(tx, in.CompanyID, invoice.ID, billLineDrafts[i]); err != nil {
				return err
			}
		}

		if err := BumpInvoiceNextNumberAfterCreate(tx, in.CompanyID); err != nil {
			return fmt.Errorf("bump invoice next number: %w", err)
		}

		if err := WriteAuditLogWithContextDetails(tx, "invoice.created", "invoice", invoice.ID, in.Actor,
			map[string]any{"company_id": in.CompanyID},
			&in.CompanyID, in.UserID, nil,
			map[string]any{
				"invoice_number": invoice.InvoiceNumber,
				"customer_id":    invoice.CustomerID,
				"total":          invoice.Amount.StringFixed(2),
				"line_count":     lineCount,
				"source":         "task_billable_work",
			},
		); err != nil {
			return err
		}

		result = GenerateInvoiceDraftResult{
			InvoiceID:     invoice.ID,
			InvoiceNumber: invoice.InvoiceNumber,
			LineCount:     lineCount,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func loadDraftableTasks(tx *gorm.DB, companyID, customerID uint, ids []uint) (map[uint]models.Task, error) {
	out := map[uint]models.Task{}
	if len(ids) == 0 {
		return out, nil
	}
	var tasks []models.Task
	if err := applyLockForUpdate(tx.
		Preload("Customer").
		Preload("ProductService").
		Where("company_id = ? AND id IN ?", companyID, ids)).
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) != len(ids) {
		return nil, ErrTaskNotFound
	}
	active, err := loadActiveSourceSet(tx, companyID, models.TaskInvoiceSourceTask, ids)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.CustomerID != customerID {
			return nil, ErrBillableWorkCustomerMismatch
		}
		if task.Status != models.TaskStatusCompleted || !task.IsBillable {
			return nil, ErrBillableWorkTaskNotReady
		}
		if task.InvoiceID != nil || task.InvoiceLineID != nil || active[task.ID] {
			return nil, ErrBillableWorkSourceAlreadyUsed
		}
		out[task.ID] = task
	}
	return out, nil
}

func loadDraftableExpenses(tx *gorm.DB, companyID, customerID uint, ids []uint) (map[uint]models.Expense, error) {
	out := map[uint]models.Expense{}
	if len(ids) == 0 {
		return out, nil
	}
	var expenses []models.Expense
	if err := applyLockForUpdate(tx.
		Preload("Task.Customer").
		Preload("Vendor").
		Preload("ExpenseAccount").
		Where("company_id = ? AND id IN ?", companyID, ids)).
		Find(&expenses).Error; err != nil {
		return nil, err
	}
	if len(expenses) != len(ids) {
		return nil, ErrExpenseNotFound
	}
	active, err := loadActiveSourceSet(tx, companyID, models.TaskInvoiceSourceExpense, ids)
	if err != nil {
		return nil, err
	}
	for _, expense := range expenses {
		if expense.TaskID == nil || !expense.IsBillable || expense.ReinvoiceStatus != models.ReinvoiceStatusUninvoiced {
			return nil, ErrBillableWorkExpenseNotReady
		}
		if expense.BillableCustomerID == nil || *expense.BillableCustomerID != customerID {
			return nil, ErrBillableWorkCustomerMismatch
		}
		if expense.InvoiceID != nil || expense.InvoiceLineID != nil || active[expense.ID] {
			return nil, ErrBillableWorkSourceAlreadyUsed
		}
		out[expense.ID] = expense
	}
	return out, nil
}

func loadDraftableBillLines(tx *gorm.DB, companyID, customerID uint, ids []uint) (map[uint]models.BillLine, error) {
	out := map[uint]models.BillLine{}
	if len(ids) == 0 {
		return out, nil
	}
	var lines []models.BillLine
	if err := applyLockForUpdate(tx.
		Preload("Bill").
		Preload("Task.Customer").
		Preload("ExpenseAccount").
		Where("company_id = ? AND id IN ?", companyID, ids)).
		Find(&lines).Error; err != nil {
		return nil, err
	}
	if len(lines) != len(ids) {
		return nil, ErrBillLineNotFound
	}
	active, err := loadActiveSourceSet(tx, companyID, models.TaskInvoiceSourceBillLine, ids)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if line.TaskID == nil || !line.IsBillable || line.ReinvoiceStatus != models.ReinvoiceStatusUninvoiced {
			return nil, ErrBillableWorkBillLineNotReady
		}
		if line.BillableCustomerID == nil || *line.BillableCustomerID != customerID {
			return nil, ErrBillableWorkCustomerMismatch
		}
		if line.InvoiceID != nil || line.InvoiceLineID != nil || active[line.ID] {
			return nil, ErrBillableWorkSourceAlreadyUsed
		}
		out[line.ID] = line
	}
	return out, nil
}

func buildTaskDraftSources(tx *gorm.DB, companyID uint, item *models.ProductService, sourceMap map[uint]models.Task) ([]draftTaskSource, error) {
	ids := sortedMapKeys(sourceMap)
	out := make([]draftTaskSource, 0, len(ids))
	for _, id := range ids {
		task := sourceMap[id]
		// Use the task's own service item if one was linked at task-creation time.
		// The item must still be active at draft-generation time — if it was
		// deactivated since then, we fall back to the TASK_LABOR system item so
		// the draft can still be generated (the editor can correct it before issuing).
		lineItem := item
		if task.ProductService != nil && task.ProductService.IsActive &&
			task.ProductService.CompanyID == companyID &&
			task.ProductService.Type == models.ProductServiceTypeService {
			lineItem = task.ProductService
		}
		line, amount, currency, err := buildDraftInvoiceLine(tx, companyID, lineItem, strings.TrimSpace(task.Title), task.Quantity, task.Rate, task.CurrencyCode)
		if err != nil {
			return nil, err
		}
		out = append(out, draftTaskSource{
			Task:     task,
			Line:     line,
			Amount:   amount,
			Currency: currency,
		})
	}
	return out, nil
}

func buildExpenseDraftSources(tx *gorm.DB, companyID uint, item *models.ProductService, sourceMap map[uint]models.Expense) ([]draftExpenseSource, error) {
	ids := sortedMapKeys(sourceMap)
	out := make([]draftExpenseSource, 0, len(ids))
	for _, id := range ids {
		expense := sourceMap[id]
		line, amount, currency, err := buildDraftInvoiceLine(tx, companyID, item, strings.TrimSpace(expense.Description), decimal.NewFromInt(1), expense.Amount, expense.CurrencyCode)
		if err != nil {
			return nil, err
		}
		out = append(out, draftExpenseSource{
			Expense:  expense,
			Line:     line,
			Amount:   amount,
			Currency: currency,
		})
	}
	return out, nil
}

func buildBillLineDraftSources(tx *gorm.DB, companyID uint, item *models.ProductService, sourceMap map[uint]models.BillLine) ([]draftBillLineSource, error) {
	ids := sortedMapKeys(sourceMap)
	out := make([]draftBillLineSource, 0, len(ids))
	for _, id := range ids {
		lineSrc := sourceMap[id]
		currency := ""
		if lineSrc.Bill != nil {
			currency = lineSrc.Bill.CurrencyCode
		}
		line, amount, normalizedCurrency, err := buildDraftInvoiceLine(tx, companyID, item, strings.TrimSpace(lineSrc.Description), decimal.NewFromInt(1), lineSrc.LineNet, currency)
		if err != nil {
			return nil, err
		}
		out = append(out, draftBillLineSource{
			BillLine: lineSrc,
			Line:     line,
			Amount:   amount,
			Currency: normalizedCurrency,
		})
	}
	return out, nil
}

func buildDraftInvoiceLine(tx *gorm.DB, companyID uint, item *models.ProductService, description string, qty, unitPrice decimal.Decimal, currency string) (models.InvoiceLine, decimal.Decimal, string, error) {
	lineNet := qty.Mul(unitPrice).RoundBank(2)
	lineTax := decimal.Zero
	// Only carry a tax code that is valid for sales invoices: same company, active,
	// and scope is sales or both. Purchase-only / inactive / cross-company tax codes
	// are stripped silently — the draft editor can correct them before issuing.
	var lineTaxCodeID *uint
	if item.DefaultTaxCodeID != nil {
		var taxCode models.TaxCode
		if err := tx.Where("id = ? AND company_id = ? AND is_active = true AND scope != ?",
			*item.DefaultTaxCodeID, companyID, models.TaxScopePurchase).
			First(&taxCode).Error; err == nil {
			taxResults := CalculateTax(lineNet, taxCode)
			lineTax = SumTaxResults(taxResults)
			lineTaxCodeID = item.DefaultTaxCodeID
		}
		// If not found (purchase-only / inactive / cross-company): leave lineTaxCodeID nil.
	}
	psID := item.ID
	return models.InvoiceLine{
		ProductServiceID: &psID,
		Description:      description,
		Qty:              qty,
		UnitPrice:        unitPrice,
		TaxCodeID:        lineTaxCodeID,
		LineNet:          lineNet,
		LineTax:          lineTax,
		LineTotal:        lineNet.Add(lineTax),
	}, lineNet, strings.ToUpper(strings.TrimSpace(currency)), nil
}

func determineDraftCurrency(baseCurrency string, tasks []draftTaskSource, expenses []draftExpenseSource, billLines []draftBillLineSource) (string, error) {
	base := strings.ToUpper(strings.TrimSpace(baseCurrency))
	if base == "" {
		base = "CAD"
	}
	seen := ""
	setCurrency := func(code string) error {
		code = strings.ToUpper(strings.TrimSpace(code))
		if code == "" {
			code = base
		}
		if seen == "" {
			seen = code
			return nil
		}
		if seen != code {
			return ErrBillableWorkCurrencyMismatch
		}
		return nil
	}
	for _, src := range tasks {
		if err := setCurrency(src.Currency); err != nil {
			return "", err
		}
	}
	for _, src := range expenses {
		if err := setCurrency(src.Currency); err != nil {
			return "", err
		}
	}
	for _, src := range billLines {
		if err := setCurrency(src.Currency); err != nil {
			return "", err
		}
	}
	if seen == base {
		return "", nil
	}
	return seen, nil
}

func resolveDraftInvoiceTerms(db *gorm.DB, companyID uint, customer models.Customer, invoiceDate time.Time) (models.PaymentTermSnapshot, *time.Time, error) {
	termCode := strings.TrimSpace(customer.DefaultPaymentTermCode)
	var term models.PaymentTerm
	if termCode != "" {
		err := db.Where("company_id = ? AND code = ? AND is_active = true", companyID, termCode).First(&term).Error
		if err == nil {
			snapshot := models.BuildSnapshot(term)
			return snapshot, models.ComputeDueDate(invoiceDate, term.NetDays), nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return models.PaymentTermSnapshot{}, nil, fmt.Errorf("load customer payment term: %w", err)
		}
	}
	if err := db.Where("company_id = ? AND is_default = ? AND is_active = true", companyID, true).First(&term).Error; err == nil {
		snapshot := models.BuildSnapshot(term)
		return snapshot, models.ComputeDueDate(invoiceDate, term.NetDays), nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.PaymentTermSnapshot{}, nil, fmt.Errorf("load default payment term: %w", err)
	}
	return models.PaymentTermSnapshot{}, nil, nil
}

func ensureInvoiceNumberAvailable(db *gorm.DB, companyID uint, invoiceNumber string) error {
	var count int64
	if err := db.Model(&models.Invoice{}).
		Where("company_id = ? AND LOWER(invoice_number) = LOWER(?) AND status <> ?", companyID, invoiceNumber, models.InvoiceStatusVoided).
		Count(&count).Error; err != nil {
		return fmt.Errorf("validate invoice number: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("invoice number already exists for this company (case-insensitive)")
	}
	return nil
}

func attachTaskSourceToInvoice(tx *gorm.DB, companyID, invoiceID uint, src draftTaskSource) error {
	invoiceIDCopy := invoiceID
	invoiceLineID := src.Line.ID
	if err := tx.Create(&models.TaskInvoiceSource{
		CompanyID:      companyID,
		InvoiceID:      &invoiceIDCopy,
		InvoiceLineID:  &invoiceLineID,
		SourceType:     models.TaskInvoiceSourceTask,
		SourceID:       src.Task.ID,
		AmountSnapshot: src.Amount,
	}).Error; err != nil {
		return fmt.Errorf("write task invoice source bridge: %w", err)
	}
	return tx.Model(&models.Task{}).
		Where("id = ? AND company_id = ?", src.Task.ID, companyID).
		Updates(map[string]any{
			"status":          string(models.TaskStatusInvoiced),
			"invoice_id":      invoiceID,
			"invoice_line_id": src.Line.ID,
		}).Error
}

func attachExpenseSourceToInvoice(tx *gorm.DB, companyID, invoiceID uint, src draftExpenseSource) error {
	invoiceIDCopy := invoiceID
	invoiceLineID := src.Line.ID
	if err := tx.Create(&models.TaskInvoiceSource{
		CompanyID:      companyID,
		InvoiceID:      &invoiceIDCopy,
		InvoiceLineID:  &invoiceLineID,
		SourceType:     models.TaskInvoiceSourceExpense,
		SourceID:       src.Expense.ID,
		AmountSnapshot: src.Amount,
	}).Error; err != nil {
		return fmt.Errorf("write expense invoice source bridge: %w", err)
	}
	return tx.Model(&models.Expense{}).
		Where("id = ? AND company_id = ?", src.Expense.ID, companyID).
		Updates(map[string]any{
			"reinvoice_status": string(models.ReinvoiceStatusInvoiced),
			"invoice_id":       invoiceID,
			"invoice_line_id":  src.Line.ID,
		}).Error
}

func attachBillLineSourceToInvoice(tx *gorm.DB, companyID, invoiceID uint, src draftBillLineSource) error {
	invoiceIDCopy := invoiceID
	invoiceLineID := src.Line.ID
	if err := tx.Create(&models.TaskInvoiceSource{
		CompanyID:      companyID,
		InvoiceID:      &invoiceIDCopy,
		InvoiceLineID:  &invoiceLineID,
		SourceType:     models.TaskInvoiceSourceBillLine,
		SourceID:       src.BillLine.ID,
		AmountSnapshot: src.Amount,
	}).Error; err != nil {
		return fmt.Errorf("write bill line invoice source bridge: %w", err)
	}
	return tx.Model(&models.BillLine{}).
		Where("id = ? AND company_id = ?", src.BillLine.ID, companyID).
		Updates(map[string]any{
			"reinvoice_status": string(models.ReinvoiceStatusInvoiced),
			"invoice_id":       invoiceID,
			"invoice_line_id":  src.Line.ID,
		}).Error
}

func loadActiveSourceSet(tx *gorm.DB, companyID uint, sourceType models.TaskInvoiceSourceType, ids []uint) (map[uint]bool, error) {
	out := map[uint]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []models.TaskInvoiceSource
	if err := tx.Select("source_id").
		Where("company_id = ? AND source_type = ? AND source_id IN ? AND voided_at IS NULL", companyID, sourceType, ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.SourceID] = true
	}
	return out, nil
}

func dedupeUintIDs(ids []uint) []uint {
	if len(ids) == 0 {
		return nil
	}
	out := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func sortedMapKeys[T any](m map[uint]T) []uint {
	keys := make([]uint, 0, len(m))
	for id := range m {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	return keys
}
