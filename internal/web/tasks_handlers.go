// 遵循project_guide.md
package web

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleTasks(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customers, err := s.customersForCompany(companyID)
	if err != nil {
		return pages.Tasks(pages.TasksVM{
			HasCompany: true,
			FormError:  "Could not load customers.",
		}).Render(c.Context(), c)
	}

	filterCustomerID := strings.TrimSpace(c.Query("customer_id"))
	filterStatus := strings.TrimSpace(c.Query("status"))
	filterFrom := strings.TrimSpace(c.Query("from"))
	filterTo := strings.TrimSpace(c.Query("to"))

	filter := services.TaskListFilter{CompanyID: companyID}
	if filterCustomerID != "" {
		if id64, err := services.ParseUint(filterCustomerID); err == nil && id64 > 0 {
			id := uint(id64)
			filter.CustomerID = &id
		}
	}
	if filterStatus != "" {
		status := models.TaskStatus(filterStatus)
		for _, allowed := range models.AllTaskStatuses() {
			if allowed == status {
				filter.Status = &status
				break
			}
		}
	}
	if filterFrom != "" {
		if d, err := time.Parse("2006-01-02", filterFrom); err == nil {
			filter.From = &d
		}
	}
	if filterTo != "" {
		if d, err := time.Parse("2006-01-02", filterTo); err == nil {
			filter.To = &d
		}
	}

	tasks, err := services.ListTasks(s.DB, filter)
	if err != nil {
		return pages.Tasks(pages.TasksVM{
			HasCompany: true,
			FormError:  "Could not load tasks.",
		}).Render(c.Context(), c)
	}

	vm := pages.TasksVM{
		HasCompany:       true,
		FormError:        strings.TrimSpace(c.Query("error")),
		Created:          c.Query("created") == "1",
		Updated:          c.Query("updated") == "1",
		Completed:        c.Query("completed") == "1",
		Cancelled:        c.Query("cancelled") == "1",
		CanCreate:        CanFromCtx(c, ActionInvoiceCreate),
		CanUpdate:        CanFromCtx(c, ActionInvoiceUpdate),
		Customers:        customers,
		Tasks:            tasks,
		FilterCustomerID: filterCustomerID,
		FilterStatus:     filterStatus,
		FilterFrom:       filterFrom,
		FilterTo:         filterTo,
	}
	return pages.Tasks(vm).Render(c.Context(), c)
}

func (s *Server) handleTaskBillableWork(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm, err := s.buildBillableWorkVM(companyID, strings.TrimSpace(c.Query("customer_id")), nil, nil, nil)
	if err != nil {
		vm.FormError = "Could not load billable work."
	}
	return pages.TaskBillableWork(vm).Render(c.Context(), c)
}

func (s *Server) handleTaskBillableWorkReport(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customers, err := s.customersForCompany(companyID)
	if err != nil {
		return pages.TaskBillableWorkReport(pages.BillableWorkReportVM{
			HasCompany: true,
			FormError:  "Could not load customers.",
		}).Render(c.Context(), c)
	}

	selectedCustomerID := strings.TrimSpace(c.Query("customer_id"))
	var filterCustomerID *uint
	if selectedCustomerID != "" {
		if id64, err := services.ParseUint(selectedCustomerID); err == nil && id64 > 0 {
			id := uint(id64)
			filterCustomerID = &id
		} else {
			selectedCustomerID = ""
		}
	}

	report, err := services.ListUnbilledWork(s.DB, companyID, filterCustomerID)
	if err != nil {
		return pages.TaskBillableWorkReport(pages.BillableWorkReportVM{
			HasCompany: true,
			FormError:  "Could not load billable work visibility.",
			Customers:  customers,
		}).Render(c.Context(), c)
	}

	summaryMap, err := services.ListCustomerBillableSummaries(s.DB, companyID)
	if err != nil {
		return pages.TaskBillableWorkReport(pages.BillableWorkReportVM{
			HasCompany: true,
			FormError:  "Could not load customer billable summaries.",
			Customers:  customers,
		}).Render(c.Context(), c)
	}

	vm := pages.BillableWorkReportVM{
		HasCompany:             true,
		Customers:              customers,
		SelectedCustomerID:     selectedCustomerID,
		Tasks:                  report.Tasks,
		Expenses:               report.Expenses,
		BillLines:              report.BillLines,
		TaskLaborTotals:        report.TaskLaborTotals,
		BillableExpenseTotals:  report.BillableExpenseTotals,
		TotalUnbilledTotals:    report.TotalUnbilledTotals,
		CustomerSummaries:      summaryMap,
	}
	return pages.TaskBillableWorkReport(vm).Render(c.Context(), c)
}

func (s *Server) handleTaskGenerateInvoiceDraft(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customerIDRaw := strings.TrimSpace(c.FormValue("customer_id"))
	taskIDs := parseRepeatedUintFormValues(c, "task_id")
	expenseIDs := parseRepeatedUintFormValues(c, "expense_id")
	billLineIDs := parseRepeatedUintFormValues(c, "bill_line_id")

	vm, _ := s.buildBillableWorkVM(companyID, customerIDRaw, stringifyUintIDs(taskIDs), stringifyUintIDs(expenseIDs), stringifyUintIDs(billLineIDs))

	customerID64, err := services.ParseUint(customerIDRaw)
	if err != nil || customerID64 == 0 {
		vm.FormError = services.ErrBillableWorkCustomerRequired.Error()
		return pages.TaskBillableWork(vm).Render(c.Context(), c)
	}

	user := UserFromCtx(c)
	actor := "system"
	var userID *uuid.UUID
	if user != nil {
		actor = user.Email
		if strings.TrimSpace(actor) == "" {
			actor = "user"
		}
		uid := user.ID
		userID = &uid
	}

	result, err := services.GenerateInvoiceDraft(s.DB, services.GenerateInvoiceDraftInput{
		CompanyID:   companyID,
		CustomerID:  uint(customerID64),
		TaskIDs:     taskIDs,
		ExpenseIDs:  expenseIDs,
		BillLineIDs: billLineIDs,
		Actor:       actor,
		UserID:      userID,
	})
	if err != nil {
		vm.FormError = err.Error()
		return pages.TaskBillableWork(vm).Render(c.Context(), c)
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d/edit?saved=1&locked=1", result.InvoiceID))
}

func (s *Server) handleTaskNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm, err := s.newTaskFormVM(companyID)
	if err != nil {
		return pages.TaskForm(pages.TaskFormVM{
			HasCompany: true,
			FormError:  "Could not load task form.",
		}).Render(c.Context(), c)
	}
	return pages.TaskForm(vm).Render(c.Context(), c)
}

func (s *Server) handleTaskCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm, input, hasErr := s.buildTaskFormVMFromRequest(c, companyID, nil)
	if hasErr {
		return pages.TaskForm(vm).Render(c.Context(), c)
	}

	task, err := services.CreateTask(s.DB, input)
	if err != nil {
		s.applyTaskServiceError(&vm, err)
		return pages.TaskForm(vm).Render(c.Context(), c)
	}

	return redirectTo(c, fmt.Sprintf("/tasks/%d?created=1", task.ID))
}

func (s *Server) handleTaskDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	taskID, err := parseTaskID(c)
	if err != nil {
		return redirectErr(c, "/tasks", "invalid task ID")
	}

	task, err := services.GetTaskByID(s.DB, companyID, taskID)
	if err != nil {
		return redirectErr(c, "/tasks", err.Error())
	}
	summary, err := services.GetTaskCostSummary(s.DB, companyID, taskID)
	if err != nil {
		return redirectErr(c, "/tasks", err.Error())
	}
	trace, err := services.GetTaskBillingTrace(s.DB, companyID, taskID)
	if err != nil {
		return redirectErr(c, "/tasks", err.Error())
	}

	canUpdate := CanFromCtx(c, ActionInvoiceUpdate)
	vm := pages.TaskDetailVM{
		HasCompany:             true,
		Task:                   *task,
		LinkedExpenses:         summary.Expenses,
		LinkedBillLines:        summary.BillLines,
		BillableExpenseAmount:  summary.BillableExpenseAmount,
		NonBillableExpenseCost: summary.NonBillableExpenseCost,
		BillingTrace:           *trace,
		FormError:              strings.TrimSpace(c.Query("error")),
		Created:                c.Query("created") == "1",
		Updated:                c.Query("updated") == "1",
		Completed:              c.Query("completed") == "1",
		Cancelled:              c.Query("cancelled") == "1",
		CanEdit:                canUpdate && taskEditableInUI(task.Status),
		CanComplete:            canUpdate && task.Status == models.TaskStatusOpen,
		CanCancel:              canUpdate && (task.Status == models.TaskStatusOpen || task.Status == models.TaskStatusCompleted),
	}
	return pages.TaskDetail(vm).Render(c.Context(), c)
}

func (s *Server) handleTaskEdit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	taskID, err := parseTaskID(c)
	if err != nil {
		return redirectErr(c, "/tasks", "invalid task ID")
	}

	task, err := services.GetTaskByID(s.DB, companyID, taskID)
	if err != nil {
		return redirectErr(c, "/tasks", err.Error())
	}
	if task.Status == models.TaskStatusCancelled || task.Status == models.TaskStatusInvoiced {
		return redirectErr(c, fmt.Sprintf("/tasks/%d", taskID), taskEditError(task.Status).Error())
	}

	vm, err := s.taskFormVMFromTask(companyID, task)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/tasks/%d", taskID), "could not load task form")
	}
	vm.FormError = strings.TrimSpace(c.Query("error"))
	return pages.TaskForm(vm).Render(c.Context(), c)
}

func (s *Server) handleTaskUpdate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	taskID, err := parseTaskID(c)
	if err != nil {
		return redirectErr(c, "/tasks", "invalid task ID")
	}

	task, err := services.GetTaskByID(s.DB, companyID, taskID)
	if err != nil {
		return redirectErr(c, "/tasks", err.Error())
	}
	if task.Status == models.TaskStatusCancelled || task.Status == models.TaskStatusInvoiced {
		return redirectErr(c, fmt.Sprintf("/tasks/%d", taskID), taskEditError(task.Status).Error())
	}

	vm, input, hasErr := s.buildTaskFormVMFromRequest(c, companyID, task)
	vm.IsEdit = true
	vm.EditingID = taskID
	vm.Status = task.Status
	vm.ReadOnlyCore = task.Status == models.TaskStatusCompleted
	vm.CanCancel = task.Status == models.TaskStatusOpen || task.Status == models.TaskStatusCompleted
	if hasErr {
		return pages.TaskForm(vm).Render(c.Context(), c)
	}

	updated, err := services.UpdateTask(s.DB, companyID, taskID, input)
	if err != nil {
		if errors.Is(err, services.ErrTaskCancelledReadOnly) || errors.Is(err, services.ErrTaskInvoicedReadOnly) {
			return redirectErr(c, fmt.Sprintf("/tasks/%d", taskID), err.Error())
		}
		s.applyTaskServiceError(&vm, err)
		return pages.TaskForm(vm).Render(c.Context(), c)
	}

	return redirectTo(c, fmt.Sprintf("/tasks/%d?updated=1", updated.ID))
}

func (s *Server) handleTaskComplete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/tasks", "company context required")
	}
	taskID, err := parseTaskID(c)
	if err != nil {
		return redirectErr(c, "/tasks", "invalid task ID")
	}
	task, err := services.CompleteTask(s.DB, companyID, taskID)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/tasks/%d", taskID), err.Error())
	}
	return redirectTo(c, fmt.Sprintf("/tasks/%d?completed=1", task.ID))
}

func (s *Server) handleTaskCancel(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/tasks", "company context required")
	}
	taskID, err := parseTaskID(c)
	if err != nil {
		return redirectErr(c, "/tasks", "invalid task ID")
	}
	task, err := services.CancelTask(s.DB, companyID, taskID)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/tasks/%d", taskID), err.Error())
	}
	return redirectTo(c, fmt.Sprintf("/tasks/%d?cancelled=1", task.ID))
}

func parseTaskID(c *fiber.Ctx) (uint, error) {
	idRaw := strings.TrimSpace(c.Params("id"))
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return 0, fmt.Errorf("invalid task ID")
	}
	return uint(id64), nil
}

func parseRepeatedUintFormValues(c *fiber.Ctx, key string) []uint {
	var out []uint
	c.Request().PostArgs().VisitAll(func(k, v []byte) {
		if string(k) != key {
			return
		}
		id64, err := services.ParseUint(strings.TrimSpace(string(v)))
		if err != nil || id64 == 0 {
			return
		}
		out = append(out, uint(id64))
	})
	return out
}

func stringifyUintIDs(ids []uint) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatUint(uint64(id), 10))
	}
	return out
}

func (s *Server) newTaskFormVM(companyID uint) (pages.TaskFormVM, error) {
	vm := pages.TaskFormVM{
		HasCompany: true,
		TaskDate:   time.Now().Format("2006-01-02"),
		Quantity:   "1",
		UnitType:   models.TaskUnitTypeHour,
		Rate:       "0.00",
		IsBillable: true,
	}
	if err := s.loadTaskFormContext(companyID, &vm); err != nil {
		return vm, err
	}
	if vm.CurrencyCode == "" {
		vm.CurrencyCode = vm.BaseCurrencyCode
	}
	return vm, nil
}

func (s *Server) taskFormVMFromTask(companyID uint, task *models.Task) (pages.TaskFormVM, error) {
	vm := pages.TaskFormVM{
		HasCompany:   true,
		IsEdit:       true,
		EditingID:    task.ID,
		Status:       task.Status,
		ReadOnlyCore: task.Status == models.TaskStatusCompleted,
		CanCancel:    task.Status == models.TaskStatusOpen || task.Status == models.TaskStatusCompleted,
		CustomerID:   strconv.FormatUint(uint64(task.CustomerID), 10),
		Title:        task.Title,
		TaskDate:     task.TaskDate.Format("2006-01-02"),
		Quantity:     task.Quantity.String(),
		UnitType:     task.UnitType,
		Rate:         task.Rate.StringFixed(2),
		CurrencyCode: task.CurrencyCode,
		IsBillable:   task.IsBillable,
		Notes:        task.Notes,
	}
	if err := s.loadTaskFormContext(companyID, &vm); err != nil {
		return vm, err
	}
	return vm, nil
}

func (s *Server) buildTaskFormVMFromRequest(c *fiber.Ctx, companyID uint, existing *models.Task) (pages.TaskFormVM, services.TaskInput, bool) {
	vm := pages.TaskFormVM{HasCompany: true}
	if existing != nil {
		vm.IsEdit = true
		vm.EditingID = existing.ID
		vm.Status = existing.Status
		vm.ReadOnlyCore = existing.Status == models.TaskStatusCompleted
		vm.CanCancel = existing.Status == models.TaskStatusOpen || existing.Status == models.TaskStatusCompleted
	}
	_ = s.loadTaskFormContext(companyID, &vm)

	vm.CustomerID = strings.TrimSpace(c.FormValue("customer_id"))
	vm.Title = strings.TrimSpace(c.FormValue("title"))
	vm.TaskDate = strings.TrimSpace(c.FormValue("task_date"))
	vm.Quantity = strings.TrimSpace(c.FormValue("quantity"))
	vm.UnitType = strings.TrimSpace(c.FormValue("unit_type"))
	vm.Rate = strings.TrimSpace(c.FormValue("rate"))
	vm.CurrencyCode = strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
	vm.IsBillable = c.FormValue("is_billable") == "1"
	vm.Notes = strings.TrimSpace(c.FormValue("notes"))

	if vm.Quantity == "" {
		vm.Quantity = "1"
	}
	if vm.UnitType == "" && existing != nil {
		vm.UnitType = existing.UnitType
	}
	if vm.Rate == "" {
		vm.Rate = "0.00"
	}
	if vm.CurrencyCode == "" {
		vm.CurrencyCode = vm.BaseCurrencyCode
	}

	var input services.TaskInput
	input.CompanyID = companyID
	input.Title = vm.Title
	input.UnitType = vm.UnitType
	input.CurrencyCode = vm.CurrencyCode
	input.IsBillable = vm.IsBillable
	input.Notes = vm.Notes

	var hasErr bool
	if id64, err := services.ParseUint(vm.CustomerID); err == nil && id64 > 0 {
		input.CustomerID = uint(id64)
	} else {
		vm.CustomerError = "Customer is required."
		hasErr = true
	}

	if vm.Title == "" {
		vm.TitleError = "Title is required."
		hasErr = true
	}

	if d, err := time.Parse("2006-01-02", vm.TaskDate); err == nil {
		input.TaskDate = d
	} else {
		vm.TaskDateError = "Task date is required."
		hasErr = true
	}

	qty, err := decimal.NewFromString(vm.Quantity)
	if err != nil || qty.IsNegative() {
		vm.QuantityError = "Quantity must be zero or greater."
		hasErr = true
	} else {
		input.Quantity = qty
	}

	if vm.UnitType == "" {
		vm.UnitTypeError = "Unit type is required."
		hasErr = true
	} else if !models.IsValidTaskUnitType(vm.UnitType) {
		vm.UnitTypeError = "Unit type is invalid."
		hasErr = true
	}

	rate, err := decimal.NewFromString(vm.Rate)
	if err != nil || rate.IsNegative() {
		vm.RateError = "Rate must be zero or greater."
		hasErr = true
	} else {
		input.Rate = rate
	}

	if vm.CurrencyCode == "" {
		vm.CurrencyError = "Currency is required."
		hasErr = true
	} else if !containsString(vm.CurrencyOptions, vm.CurrencyCode) {
		vm.CurrencyError = "Currency is not enabled for this company."
		hasErr = true
	}

	return vm, input, hasErr
}

func (s *Server) loadTaskFormContext(companyID uint, vm *pages.TaskFormVM) error {
	customers, err := s.customersForCompany(companyID)
	if err != nil {
		return err
	}
	vm.Customers = customers

	var company models.Company
	if err := s.DB.Select("id", "base_currency_code", "multi_currency_enabled").First(&company, companyID).Error; err != nil {
		return err
	}
	vm.BaseCurrencyCode = company.BaseCurrencyCode
	vm.MultiCurrency = company.MultiCurrencyEnabled
	vm.CurrencyOptions = []string{company.BaseCurrencyCode}
	if company.MultiCurrencyEnabled {
		ccs, err := services.ListCompanyCurrencies(s.DB, companyID)
		if err != nil {
			return err
		}
		for _, cc := range ccs {
			if !cc.IsActive {
				continue
			}
			code := strings.ToUpper(strings.TrimSpace(cc.CurrencyCode))
			if code == "" || containsString(vm.CurrencyOptions, code) {
				continue
			}
			vm.CurrencyOptions = append(vm.CurrencyOptions, code)
		}
	}
	return nil
}

func (s *Server) buildBillableWorkVM(companyID uint, customerIDRaw string, selectedTaskIDs, selectedExpenseIDs, selectedBillLineIDs []string) (pages.BillableWorkVM, error) {
	customers, err := s.customersForCompany(companyID)
	if err != nil {
		return pages.BillableWorkVM{HasCompany: true}, err
	}

	vm := pages.BillableWorkVM{
		HasCompany:          true,
		CanGenerate:         true,
		Customers:           customers,
		SelectedCustomerID:  customerIDRaw,
		SelectedTaskIDs:     selectedTaskIDs,
		SelectedExpenseIDs:  selectedExpenseIDs,
		SelectedBillLineIDs: selectedBillLineIDs,
	}

	if customerIDRaw == "" {
		return vm, nil
	}
	customerID64, err := services.ParseUint(customerIDRaw)
	if err != nil || customerID64 == 0 {
		return vm, nil
	}
	candidates, err := services.ListBillableWorkCandidates(s.DB, companyID, uint(customerID64))
	if err != nil {
		return vm, err
	}
	vm.Tasks = candidates.Tasks
	vm.Expenses = candidates.Expenses
	vm.BillLines = candidates.BillLines
	return vm, nil
}

func (s *Server) applyTaskServiceError(vm *pages.TaskFormVM, err error) {
	switch {
	case errors.Is(err, services.ErrTaskCustomerRequired), errors.Is(err, services.ErrTaskCustomerInvalid):
		vm.CustomerError = err.Error()
	case errors.Is(err, services.ErrTaskTitleRequired):
		vm.TitleError = err.Error()
	case errors.Is(err, services.ErrTaskDateRequired):
		vm.TaskDateError = err.Error()
	case errors.Is(err, services.ErrTaskQuantityNegative):
		vm.QuantityError = err.Error()
	case errors.Is(err, services.ErrTaskUnitTypeRequired), errors.Is(err, services.ErrTaskUnitTypeInvalid):
		vm.UnitTypeError = err.Error()
	case errors.Is(err, services.ErrTaskRateNegative):
		vm.RateError = err.Error()
	case errors.Is(err, services.ErrTaskCurrencyRequired):
		vm.CurrencyError = err.Error()
	default:
		vm.FormError = err.Error()
	}
}

func taskEditableInUI(status models.TaskStatus) bool {
	switch status {
	case models.TaskStatusOpen, models.TaskStatusCompleted:
		return true
	default:
		return false
	}
}

func taskEditError(status models.TaskStatus) error {
	switch status {
	case models.TaskStatusCancelled:
		return services.ErrTaskCancelledReadOnly
	case models.TaskStatusInvoiced:
		return services.ErrTaskInvoicedReadOnly
	default:
		return services.ErrTaskNotFound
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
