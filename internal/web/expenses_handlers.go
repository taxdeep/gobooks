package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// rehydrateVendorLabel uses the VendorProvider to look up the human-readable
// label for the given vendor ID. Returns "" if the ID is empty or the vendor
// is not found within the company scope.
func (s *Server) rehydrateVendorLabel(companyID uint, idStr string) string {
	if idStr == "" {
		return ""
	}
	p, ok := defaultSmartPickerRegistry.get("vendor")
	if !ok {
		return ""
	}
	item, err := p.GetByID(s.DB, SmartPickerContext{CompanyID: companyID, Context: "expense_form_vendor"}, idStr)
	if err != nil || item == nil {
		return ""
	}
	return item.Primary
}

// rehydratePaymentAccountLabel uses the PaymentAccountProvider to look up the
// human-readable label for the given payment account ID. Returns "" if the ID
// is empty or the account no longer satisfies the payment-account guards.
func (s *Server) rehydratePaymentAccountLabel(companyID uint, idStr string) string {
	if idStr == "" {
		return ""
	}
	p, ok := defaultSmartPickerRegistry.get("payment_account")
	if !ok {
		return ""
	}
	item, err := p.GetByID(s.DB, SmartPickerContext{CompanyID: companyID, Context: "expense_form_payment"}, idStr)
	if err != nil || item == nil {
		return ""
	}
	return item.Primary
}

func (s *Server) handleExpenses(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	expenses, err := services.ListExpenses(s.DB, services.ExpenseListFilter{CompanyID: companyID})
	if err != nil {
		return pages.Expenses(pages.ExpenseListVM{
			HasCompany: true,
			FormError:  "Could not load expenses.",
		}).Render(c.Context(), c)
	}

	return pages.Expenses(pages.ExpenseListVM{
		HasCompany: true,
		FormError:  strings.TrimSpace(c.Query("error")),
		Created:    c.Query("created") == "1",
		Updated:    c.Query("updated") == "1",
		CanCreate:  CanFromCtx(c, ActionBillCreate),
		CanUpdate:  CanFromCtx(c, ActionBillUpdate),
		Expenses:   expenses,
	}).Render(c.Context(), c)
}

func (s *Server) handleExpenseNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm, err := s.newExpenseFormVM(companyID)
	if err != nil {
		return pages.ExpenseForm(pages.ExpenseFormVM{
			HasCompany: true,
			FormError:  "Could not load expense form.",
		}).Render(c.Context(), c)
	}
	return pages.ExpenseForm(vm).Render(c.Context(), c)
}

func (s *Server) handleExpenseCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm, input, hasErr := s.buildExpenseFormVMFromRequest(c, companyID, nil)
	if hasErr {
		return pages.ExpenseForm(vm).Render(c.Context(), c)
	}

	expense, err := services.CreateExpense(s.DB, input)
	if err != nil {
		s.applyExpenseServiceError(&vm, err)
		return pages.ExpenseForm(vm).Render(c.Context(), c)
	}
	_ = expense
	return redirectTo(c, "/expenses?created=1")
}

func (s *Server) handleExpenseEdit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	expenseID, err := parseExpenseID(c)
	if err != nil {
		return redirectErr(c, "/expenses", "invalid expense ID")
	}

	expense, err := services.GetExpenseByID(s.DB, companyID, expenseID)
	if err != nil {
		return redirectErr(c, "/expenses", err.Error())
	}

	vm, err := s.expenseFormVMFromExpense(companyID, expense)
	if err != nil {
		return redirectErr(c, "/expenses", "could not load expense form")
	}
	vm.FormError = strings.TrimSpace(c.Query("error"))
	return pages.ExpenseForm(vm).Render(c.Context(), c)
}

func (s *Server) handleExpenseUpdate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	expenseID, err := parseExpenseID(c)
	if err != nil {
		return redirectErr(c, "/expenses", "invalid expense ID")
	}

	existing, err := services.GetExpenseByID(s.DB, companyID, expenseID)
	if err != nil {
		return redirectErr(c, "/expenses", err.Error())
	}
	vm, input, hasErr := s.buildExpenseFormVMFromRequest(c, companyID, existing)
	vm.IsEdit = true
	vm.EditingID = expenseID
	if hasErr {
		return pages.ExpenseForm(vm).Render(c.Context(), c)
	}

	expense, err := services.UpdateExpense(s.DB, companyID, expenseID, input)
	if err != nil {
		s.applyExpenseServiceError(&vm, err)
		return pages.ExpenseForm(vm).Render(c.Context(), c)
	}
	_ = expense
	return redirectTo(c, "/expenses?updated=1")
}

func parseExpenseID(c *fiber.Ctx) (uint, error) {
	idRaw := strings.TrimSpace(c.Params("id"))
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return 0, fmt.Errorf("invalid expense ID")
	}
	return uint(id64), nil
}

func (s *Server) newExpenseFormVM(companyID uint) (pages.ExpenseFormVM, error) {
	vm := pages.ExpenseFormVM{
		HasCompany:  true,
		ExpenseDate: time.Now().Format("2006-01-02"),
	}
	if err := s.loadExpenseFormContext(companyID, &vm); err != nil {
		return vm, err
	}
	if vm.CurrencyCode == "" {
		vm.CurrencyCode = vm.BaseCurrencyCode
	}
	// Seed two blank line rows for the new expense form.
	vm.Lines = []pages.ExpenseLineFormVM{
		{Amount: "0.00"},
		{Amount: "0.00"},
	}
	return vm, nil
}

func (s *Server) expenseFormVMFromExpense(companyID uint, exp *models.Expense) (pages.ExpenseFormVM, error) {
	vm := pages.ExpenseFormVM{
		HasCompany:       true,
		IsEdit:           true,
		EditingID:        exp.ID,
		ExpenseNumber:    exp.ExpenseNumber,
		ExpenseDate:      exp.ExpenseDate.Format("2006-01-02"),
		CurrencyCode:     exp.CurrencyCode,
		VendorID:         optUintStr(exp.VendorID),
		Notes:            exp.Notes,
		PaymentMethod:    string(exp.PaymentMethod),
		PaymentReference: exp.PaymentReference,
	}

	// Rehydrate vendor label for SmartPicker.
	vm.VendorLabel = s.rehydrateVendorLabel(companyID, vm.VendorID)

	// Rehydrate payment account for SmartPicker.
	if exp.PaymentAccountID != nil {
		idStr := fmt.Sprintf("%d", *exp.PaymentAccountID)
		label := s.rehydratePaymentAccountLabel(companyID, idStr)
		if label != "" {
			vm.PaymentAccountID = idStr
			vm.PaymentAccountLabel = label
		} else {
			vm.PaymentAccountID = ""
			vm.PaymentAccountLabel = ""
			vm.PaymentAccountError = "Previously selected payment account is no longer available. Please choose a new one."
		}
	}

	if err := s.loadExpenseFormContext(companyID, &vm); err != nil {
		return vm, err
	}

	// Rehydrate line items from the expense's Lines slice (preloaded by GetExpenseByID).
	if len(exp.Lines) > 0 {
		vm.Lines = make([]pages.ExpenseLineFormVM, 0, len(exp.Lines))
		for _, l := range exp.Lines {
			lineVM := pages.ExpenseLineFormVM{
				Description: l.Description,
				Amount:      l.Amount.StringFixed(2),
				LineTax:     l.LineTax.StringFixed(2),
				LineTotal:   l.LineTotal.StringFixed(2),
				IsBillable:  l.IsBillable,
			}
			if l.ExpenseAccountID != nil {
				lineVM.ExpenseAccountID = fmt.Sprintf("%d", *l.ExpenseAccountID)
			}
			if l.ProductServiceID != nil {
				lineVM.ProductServiceID = fmt.Sprintf("%d", *l.ProductServiceID)
			}
			if l.TaxCodeID != nil {
				lineVM.TaxCodeID = fmt.Sprintf("%d", *l.TaxCodeID)
			}
			if l.TaskID != nil {
				lineVM.TaskID = fmt.Sprintf("%d", *l.TaskID)
			}
			vm.Lines = append(vm.Lines, lineVM)
		}
	} else {
		// Fallback: single line from header fields (pre-migration data, no tax).
		lineVM := pages.ExpenseLineFormVM{
			Description: exp.Description,
			Amount:      exp.Amount.StringFixed(2),
			LineTax:     "0.00",
			LineTotal:   exp.Amount.StringFixed(2),
			IsBillable:  exp.IsBillable,
		}
		if exp.ExpenseAccountID != nil {
			lineVM.ExpenseAccountID = fmt.Sprintf("%d", *exp.ExpenseAccountID)
		}
		if exp.TaskID != nil {
			lineVM.TaskID = fmt.Sprintf("%d", *exp.TaskID)
		}
		vm.Lines = []pages.ExpenseLineFormVM{lineVM}
	}

	return vm, nil
}

func (s *Server) buildExpenseFormVMFromRequest(c *fiber.Ctx, companyID uint, existing *models.Expense) (pages.ExpenseFormVM, services.ExpenseInput, bool) {
	vm := pages.ExpenseFormVM{HasCompany: true}
	if existing != nil {
		vm.IsEdit = true
		vm.EditingID = existing.ID
	}
	_ = s.loadExpenseFormContext(companyID, &vm)

	vm.ExpenseDate = strings.TrimSpace(c.FormValue("expense_date"))
	vm.CurrencyCode = strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
	vm.VendorID = strings.TrimSpace(c.FormValue("vendor_id"))
	vm.PaymentAccountID = strings.TrimSpace(c.FormValue("payment_account_id"))
	vm.PaymentMethod = strings.TrimSpace(c.FormValue("payment_method"))
	vm.PaymentReference = strings.TrimSpace(c.FormValue("payment_reference"))
	vm.Notes = strings.TrimSpace(c.FormValue("notes"))

	if vm.CurrencyCode == "" {
		vm.CurrencyCode = vm.BaseCurrencyCode
	}

	// Rehydrate vendor label for error re-render.
	vm.VendorLabel = s.rehydrateVendorLabel(companyID, vm.VendorID)

	// Rehydrate payment account SmartPicker label for error re-render.
	vm.PaymentAccountLabel = s.rehydratePaymentAccountLabel(companyID, vm.PaymentAccountID)

	// ── Parse line items ─────────────────────────────────────────────────────
	lineCountRaw := strings.TrimSpace(c.FormValue("line_count"))
	lineCount, _ := strconv.Atoi(lineCountRaw)
	if lineCount < 0 || lineCount > 50 {
		lineCount = 0
	}

	type parsedLine struct {
		ExpenseAccountID *uint
		ProductServiceID *uint
		Description      string
		Amount           decimal.Decimal // pre-tax net
		TaxCodeIDRaw     string
		TaxCodeID        *uint
		TaskID           *uint
		IsBillable       bool
	}
	var parsedLines []parsedLine
	var lineVMs []pages.ExpenseLineFormVM

	for i := 0; i < lineCount; i++ {
		key := func(field string) string { return fmt.Sprintf("%s[%d]", field, i) }
		accIDRaw := strings.TrimSpace(c.FormValue(key("line_expense_account_id")))
		psIDRaw := strings.TrimSpace(c.FormValue(key("line_product_service_id")))
		desc := strings.TrimSpace(c.FormValue(key("line_description")))
		amtRaw := strings.TrimSpace(c.FormValue(key("line_amount")))
		tcIDRaw := strings.TrimSpace(c.FormValue(key("line_tax_code_id")))
		taskIDRaw := strings.TrimSpace(c.FormValue(key("line_task_id")))
		isBillable := c.FormValue(key("line_is_billable")) == "1"

		amt, aErr := decimal.NewFromString(amtRaw)
		if aErr != nil || amt.IsNegative() {
			amt = decimal.Zero
		}

		// Skip fully blank placeholder rows (no account, no product,
		// no description, zero amount, no task).
		if accIDRaw == "" && psIDRaw == "" && desc == "" && taskIDRaw == "" && tcIDRaw == "" &&
			(amtRaw == "" || amtRaw == "0.00" || amtRaw == "0") {
			continue
		}

		lVM := pages.ExpenseLineFormVM{
			ExpenseAccountID: accIDRaw,
			ProductServiceID: psIDRaw,
			Description:      desc,
			Amount:           amt.StringFixed(2),
			TaxCodeID:        tcIDRaw,
			TaskID:           taskIDRaw,
			IsBillable:       isBillable,
		}

		pl := parsedLine{Description: desc, Amount: amt, TaxCodeIDRaw: tcIDRaw, IsBillable: isBillable}
		if id64, err := strconv.ParseUint(accIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.ExpenseAccountID = &id
		}
		if id64, err := strconv.ParseUint(psIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.ProductServiceID = &id
		}
		if id64, err := strconv.ParseUint(tcIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.TaxCodeID = &id
		}
		if id64, err := strconv.ParseUint(taskIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.TaskID = &id
		}

		parsedLines = append(parsedLines, pl)
		lineVMs = append(lineVMs, lVM)
	}

	// Ensure at least the submitted rows are visible on error re-render.
	if len(lineVMs) == 0 {
		lineVMs = []pages.ExpenseLineFormVM{{Amount: "0.00"}, {Amount: "0.00"}}
	}
	vm.Lines = lineVMs

	// ── Validate and load tax codes ───────────────────────────────────────────
	taxCodeCache := map[uint]*models.TaxCode{}
	for i, pl := range parsedLines {
		if pl.TaxCodeID == nil {
			continue
		}
		tcID := *pl.TaxCodeID
		if _, ok := taxCodeCache[tcID]; ok {
			continue
		}
		var tc models.TaxCode
		if err := s.DB.
			Where("id = ? AND company_id = ? AND is_active = true", tcID, companyID).
			First(&tc).Error; err != nil {
			vm.FormError = fmt.Sprintf("Line %d has an invalid tax code.", i+1)
			return vm, services.ExpenseInput{}, true
		}
		taxCodeCache[tcID] = &tc
	}

	// ── Parse tax adjustments (user-edited per-code overrides) ────────────────
	taxAdjCountRaw := strings.TrimSpace(c.FormValue("tax_adj_count"))
	taxAdjCount, _ := strconv.Atoi(taxAdjCountRaw)
	taxAdjMap := map[uint]decimal.Decimal{}
	for i := 0; i < taxAdjCount; i++ {
		idRaw := strings.TrimSpace(c.FormValue(fmt.Sprintf("tax_adj_id[%d]", i)))
		adjAmtRaw := strings.TrimSpace(c.FormValue(fmt.Sprintf("tax_adj_amount[%d]", i)))
		tcID64, err := strconv.ParseUint(idRaw, 10, 64)
		if err != nil || tcID64 == 0 {
			continue
		}
		adjAmt, err := decimal.NewFromString(adjAmtRaw)
		if err != nil {
			continue
		}
		taxAdjMap[uint(tcID64)] = adjAmt.RoundBank(2)
	}

	// ── Compute per-line tax amounts (two-pass, mirrors bill handler) ─────────
	type computedLine struct {
		parsedLine
		LineNet   decimal.Decimal
		LineTax   decimal.Decimal
		LineTotal decimal.Decimal
	}
	var computed []computedLine

	type perCodeData struct {
		calcTotal decimal.Decimal
		indices   []int
	}
	codeData := map[uint]*perCodeData{}

	for _, pl := range parsedLines {
		lineNet := pl.Amount.RoundBank(2)
		var lineTax decimal.Decimal
		if pl.TaxCodeID != nil {
			if tc, ok := taxCodeCache[*pl.TaxCodeID]; ok {
				lineTax = lineNet.Mul(tc.Rate).RoundBank(2)
			}
		}
		idx := len(computed)
		computed = append(computed, computedLine{
			parsedLine: pl,
			LineNet:    lineNet,
			LineTax:    lineTax,
			LineTotal:  lineNet.Add(lineTax),
		})
		if pl.TaxCodeID != nil {
			cd := codeData[*pl.TaxCodeID]
			if cd == nil {
				cd = &perCodeData{}
				codeData[*pl.TaxCodeID] = cd
			}
			cd.calcTotal = cd.calcTotal.Add(lineTax)
			cd.indices = append(cd.indices, idx)
		}
	}

	// Second pass: apply user tax adjustments proportionally.
	for codeID, cd := range codeData {
		adj, hasAdj := taxAdjMap[codeID]
		if !hasAdj || adj.Equal(cd.calcTotal) {
			continue
		}
		if cd.calcTotal.IsZero() {
			each := adj.Div(decimal.NewFromInt(int64(len(cd.indices)))).RoundBank(2)
			remainder := adj
			for i, li := range cd.indices {
				tax := each
				if i == len(cd.indices)-1 {
					tax = remainder
				}
				computed[li].LineTax = tax
				computed[li].LineTotal = computed[li].LineNet.Add(tax)
				remainder = remainder.Sub(tax)
			}
		} else {
			remaining := adj
			for i, li := range cd.indices {
				var tax decimal.Decimal
				if i == len(cd.indices)-1 {
					tax = remaining
				} else {
					tax = computed[li].LineTax.Mul(adj).Div(cd.calcTotal).RoundBank(2)
				}
				computed[li].LineTax = tax
				computed[li].LineTotal = computed[li].LineNet.Add(tax)
				remaining = remaining.Sub(tax)
			}
		}
	}

	// Backfill computed tax into lineVMs for error re-render.
	for i, cl := range computed {
		if i < len(vm.Lines) {
			vm.Lines[i].LineTax = cl.LineTax.StringFixed(2)
			vm.Lines[i].LineTotal = cl.LineTotal.StringFixed(2)
		}
	}

	// ── Build service input ───────────────────────────────────────────────────
	var input services.ExpenseInput
	input.CompanyID = companyID
	input.CurrencyCode = vm.CurrencyCode
	input.Notes = vm.Notes

	for _, cl := range computed {
		input.Lines = append(input.Lines, services.ExpenseLineInput{
			Description:      cl.Description,
			Amount:           cl.LineNet,
			ExpenseAccountID: cl.ExpenseAccountID,
			ProductServiceID: cl.ProductServiceID,
			TaxCodeID:        cl.TaxCodeID,
			LineTax:          cl.LineTax,
			LineTotal:        cl.LineTotal,
			TaskID:           cl.TaskID,
			IsBillable:       cl.IsBillable,
		})
	}

	var hasErr bool

	// Payment account eligibility guard.
	if vm.PaymentAccountID != "" && vm.PaymentAccountLabel == "" {
		vm.PaymentAccountError = "The selected payment account is not available or is not a valid payment source (must be a bank, credit card, or petty-cash account)."
		vm.PaymentAccountID = ""
		hasErr = true
	}

	if d, err := time.Parse("2006-01-02", vm.ExpenseDate); err == nil {
		input.ExpenseDate = d
	} else {
		vm.ExpenseDateError = "Expense date is required."
		hasErr = true
	}

	if vm.CurrencyCode == "" {
		vm.CurrencyError = "Currency is required."
		hasErr = true
	} else if !containsString(vm.CurrencyOptions, vm.CurrencyCode) {
		vm.CurrencyError = "Currency is not enabled for this company."
		hasErr = true
	}

	// Lines must be present and have positive amounts.
	if len(parsedLines) == 0 {
		vm.FormError = "At least one expense line with a positive amount is required."
		hasErr = true
	} else {
		allZero := true
		for _, pl := range parsedLines {
			if pl.Amount.GreaterThan(decimal.Zero) {
				allZero = false
				break
			}
		}
		if allZero {
			vm.AmountError = "At least one line must have an amount greater than zero."
			hasErr = true
		}
	}

	if id64, err := services.ParseUint(vm.VendorID); err == nil && id64 > 0 {
		id := uint(id64)
		input.VendorID = &id
	}
	if id64, err := services.ParseUint(vm.PaymentAccountID); err == nil && id64 > 0 {
		id := uint(id64)
		input.PaymentAccountID = &id
	}
	input.PaymentMethod = models.PaymentMethod(vm.PaymentMethod)
	input.PaymentReference = vm.PaymentReference
	return vm, input, hasErr
}

func (s *Server) loadExpenseFormContext(companyID uint, vm *pages.ExpenseFormVM) error {
	// Vendor uses SmartPicker (on-demand search); expense accounts and tasks are
	// pre-loaded as JSON for the Alpine component.
	selectableTasks, err := services.ListSelectableTasks(s.DB, companyID)
	if err != nil {
		return err
	}
	vm.SelectableTasksJSON = buildExpenseTasksJSON(selectableTasks)

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

	// Pre-load expense accounts for the line-item category <select>.
	var expAccounts []models.Account
	if err := s.DB.
		Where("company_id = ? AND root_account_type = ? AND is_active = true", companyID, models.RootExpense).
		Order("code ASC").
		Find(&expAccounts).Error; err != nil {
		return err
	}
	vm.ExpenseAccountsJSON = buildExpenseAccountsJSON(expAccounts)

	// Pre-load purchase-scope tax codes for the per-line tax <select>.
	var taxCodes []models.TaxCode
	if err := s.DB.
		Where("company_id = ? AND is_active = true AND scope IN ?", companyID,
			[]models.TaxScope{models.TaxScopePurchase, models.TaxScopeBoth}).
		Order("name ASC").
		Find(&taxCodes).Error; err != nil {
		return err
	}
	vm.TaxCodesJSON = buildExpenseTaxCodesJSON(taxCodes)

	// Pre-load active catalog products for the per-line item picker.
	// Matches PO detail's catalog loader: active rows only, ordered
	// by name. Both stock and service items are listed — the picker
	// displays the kind inline so operators choose deliberately.
	var products []models.ProductService
	if err := s.DB.
		Where("company_id = ? AND is_active = true", companyID).
		Order("name ASC").
		Find(&products).Error; err != nil {
		return err
	}
	vm.ProductsJSON = buildExpenseProductsJSON(products)

	return nil
}

type expenseProductJSONItem struct {
	ID   uint   `json:"id"`
	SKU  string `json:"sku"`
	Name string `json:"name"`
	Kind string `json:"kind"` // "stock" | "service"
}

func buildExpenseProductsJSON(products []models.ProductService) string {
	items := make([]expenseProductJSONItem, 0, len(products))
	for _, p := range products {
		kind := "service"
		if p.IsStockItem {
			kind = "stock"
		}
		items = append(items, expenseProductJSONItem{
			ID:   p.ID,
			SKU:  p.SKU,
			Name: p.Name,
			Kind: kind,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

type expenseAccountJSONItem struct {
	ID   uint   `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func buildExpenseAccountsJSON(accounts []models.Account) string {
	items := make([]expenseAccountJSONItem, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, expenseAccountJSONItem{ID: a.ID, Code: a.Code, Name: a.Name})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

type expenseTaxCodeJSONItem struct {
	ID   uint   `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Rate string `json:"rate"` // fraction string, e.g. "0.050000"
}

func buildExpenseTaxCodesJSON(codes []models.TaxCode) string {
	items := make([]expenseTaxCodeJSONItem, 0, len(codes))
	for _, tc := range codes {
		items = append(items, expenseTaxCodeJSONItem{
			ID:   tc.ID,
			Code: tc.Code,
			Name: tc.Name,
			Rate: tc.Rate.String(),
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

type expenseTaskJSONItem struct {
	ID           uint   `json:"id"`
	Title        string `json:"title"`
	CustomerName string `json:"customer_name"`
}

func buildExpenseTasksJSON(tasks []models.Task) string {
	items := make([]expenseTaskJSONItem, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, expenseTaskJSONItem{
			ID:           t.ID,
			Title:        t.Title,
			CustomerName: t.Customer.Name,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func (s *Server) applyExpenseServiceError(vm *pages.ExpenseFormVM, err error) {
	switch {
	case errors.Is(err, services.ErrExpenseDateRequired):
		vm.ExpenseDateError = err.Error()
	case errors.Is(err, services.ErrExpenseDescriptionRequired):
		vm.DescriptionError = err.Error()
	case errors.Is(err, services.ErrExpenseAmountInvalid), errors.Is(err, services.ErrExpenseLinesRequired):
		vm.AmountError = err.Error()
	case errors.Is(err, services.ErrExpenseCurrencyRequired):
		vm.CurrencyError = err.Error()
	case errors.Is(err, services.ErrExpenseAccountRequired), errors.Is(err, services.ErrExpenseAccountInvalid),
		errors.Is(err, services.ErrExpenseLineAccountRequired), errors.Is(err, services.ErrExpenseLineAccountInvalid):
		vm.ExpenseAccountError = err.Error()
	case errors.Is(err, services.ErrExpenseVendorInvalid):
		vm.VendorError = err.Error()
	case errors.Is(err, services.ErrTaskLinkageTaskNotFound), errors.Is(err, services.ErrTaskLinkageTaskStatusInvalid),
		errors.Is(err, services.ErrTaskBillableCustomerMismatch):
		vm.FormError = err.Error()
	case errors.Is(err, services.ErrExpensePaymentAccountInvalid):
		vm.PaymentAccountError = err.Error()
	case errors.Is(err, services.ErrExpensePaymentMethodRequired), errors.Is(err, services.ErrExpensePaymentMethodInvalid):
		vm.PaymentMethodError = err.Error()
	default:
		vm.FormError = err.Error()
	}
}
