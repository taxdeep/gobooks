// 遵循project_guide.md
package web

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// handleBills renders the bills list page.
func (s *Server) handleBills(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vendors, err := s.vendorsForCompany(companyID)
	if err != nil {
		return pages.Bills(pages.BillsVM{
			HasCompany: true,
			FormError:  "Could not load vendors.",
		}).Render(c.Context(), c)
	}

	filterQ := strings.TrimSpace(c.Query("q"))
	filterVendorID := strings.TrimSpace(c.Query("vendor_id"))
	filterFrom := strings.TrimSpace(c.Query("from"))
	filterTo := strings.TrimSpace(c.Query("to"))

	qry := s.DB.Preload("Vendor").Model(&models.Bill{}).Where("company_id = ?", companyID)
	if filterQ != "" {
		qry = qry.Where("LOWER(bill_number) LIKE LOWER(?)", "%"+filterQ+"%")
	}
	if filterVendorID != "" {
		if id, err := services.ParseUint(filterVendorID); err == nil && id > 0 {
			qry = qry.Where("vendor_id = ?", uint(id))
		}
	}
	if filterFrom != "" {
		if d, err := time.Parse("2006-01-02", filterFrom); err == nil {
			qry = qry.Where("bill_date >= ?", d)
		}
	}
	if filterTo != "" {
		if d, err := time.Parse("2006-01-02", filterTo); err == nil {
			qry = qry.Where("bill_date < ?", d.AddDate(0, 0, 1))
		}
	}

	var bills []models.Bill
	if err := qry.Order("bill_date desc, id desc").Find(&bills).Error; err != nil {
		return pages.Bills(pages.BillsVM{
			HasCompany: true,
			FormError:  "Could not load bills.",
		}).Render(c.Context(), c)
	}

	formError := ""
	if c.Query("voiderror") == "1" {
		formError = "Could not void bill. Check that it is posted and has no other dependencies."
	}

	return pages.Bills(pages.BillsVM{
		HasCompany:     true,
		Vendors:        vendors,
		Bills:          bills,
		Posted:         c.Query("posted") == "1",
		Saved:          c.Query("saved") == "1",
		Voided:         c.Query("voided") == "1",
		FormError:      formError,
		FilterQ:        filterQ,
		FilterVendorID: filterVendorID,
		FilterFrom:     filterFrom,
		FilterTo:       filterTo,
	}).Render(c.Context(), c)
}

// handleBillNew renders the blank bill editor.
func (s *Server) handleBillNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	nextNo := "BILL001"
	var latest models.Bill
	if err := s.DB.Where("company_id = ?", companyID).Order("id desc").First(&latest).Error; err == nil {
		nextNo = services.NextDocumentNumber(latest.BillNumber, "BILL001")
	}

	today := time.Now().Format("2006-01-02")
	vm := pages.BillEditorVM{
		HasCompany: true,
		IsEdit:     false,
		BillNumber: nextNo,
		BillDate:   today,
	}

	if err := s.loadBillEditorDropdowns(companyID, &vm); err != nil {
		vm.FormError = "Could not load dropdown data."
	}

	// Pre-select default payment term and compute default due date.
	for _, pt := range vm.PaymentTerms {
		if pt.IsDefault {
			vm.TermCode = pt.Code
			due := models.ComputeDueDate(time.Now(), pt.NetDays)
			if due != nil {
				vm.DueDate = due.Format("2006-01-02")
			}
			break
		}
	}

	return pages.BillEditor(vm).Render(c.Context(), c)
}

// handleBillEdit renders the editor pre-filled with an existing draft bill.
func (s *Server) handleBillEdit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.Params("id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/bills", fiber.StatusSeeOther)
	}
	billID := uint(id64)

	var bill models.Bill
	if err := s.DB.Preload("Lines").
		Where("id = ? AND company_id = ?", billID, companyID).
		First(&bill).Error; err != nil {
		return c.Redirect("/bills", fiber.StatusSeeOther)
	}
	if bill.Status != models.BillStatusDraft {
		return c.Redirect("/bills", fiber.StatusSeeOther)
	}

	vm := pages.BillEditorVM{
		HasCompany:   true,
		IsEdit:       true,
		EditingID:    billID,
		ReviewLocked: c.Query("locked") == "1",
		BillNumber:   bill.BillNumber,
		VendorID:     strconv.FormatUint(uint64(bill.VendorID), 10),
		BillDate:     bill.BillDate.Format("2006-01-02"),
		TermCode:     bill.TermCode,
		Memo:         bill.Memo,
		FormError:    strings.TrimSpace(c.Query("error")),
		Saved:        c.Query("saved") == "1",
		CurrencyCode: bill.CurrencyCode,
		ExchangeRate: displayDocumentExchangeRate(bill.CurrencyCode, bill.ExchangeRate),
	}
	if CanFromCtx(c, ActionBillUpdate) {
		vm.SubmitPath = fmt.Sprintf("/bills/%d/post", billID)
	}
	if bill.DueDate != nil {
		vm.DueDate = bill.DueDate.Format("2006-01-02")
	}

	for _, l := range bill.Lines {
		vm.Lines = append(vm.Lines, pages.BillLineFormRow{
			ExpenseAccountID: optUintStr(l.ExpenseAccountID),
			Description:      l.Description,
			Amount:           l.LineNet.StringFixed(2),
			TaxCodeID:        optUintStr(l.TaxCodeID),
			TaskID:           optUintStr(l.TaskID),
			IsBillable:       l.IsBillable,
			LineNet:          l.LineNet.StringFixed(2),
			LineTax:          l.LineTax.StringFixed(2),
			LineTotal:        l.LineTotal.StringFixed(2),
		})
	}

	if err := s.loadBillEditorDropdowns(companyID, &vm); err != nil {
		vm.FormError = "Could not load dropdown data."
	}
	vm.InitialLinesJSON = buildBillInitialLinesJSON(vm.Lines)
	return pages.BillEditor(vm).Render(c.Context(), c)
}

// handleBillSaveDraft creates or updates a draft bill with line items.
func (s *Server) handleBillSaveDraft(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// ── Parse header ──────────────────────────────────────────────────────────
	billIDRaw := strings.TrimSpace(c.FormValue("bill_id"))
	billNo := strings.TrimSpace(c.FormValue("bill_number"))
	vendorRaw := strings.TrimSpace(c.FormValue("vendor_id"))
	dateRaw := strings.TrimSpace(c.FormValue("bill_date"))
	termsRaw := strings.TrimSpace(c.FormValue("terms"))
	dueDateRaw := strings.TrimSpace(c.FormValue("due_date"))
	memo := strings.TrimSpace(c.FormValue("memo"))
	currencyCodeRaw := strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
	exchangeRateRaw := strings.TrimSpace(c.FormValue("exchange_rate"))
	lineCountRaw := strings.TrimSpace(c.FormValue("line_count"))

	isEdit := billIDRaw != "" && billIDRaw != "0"
	var editingID uint
	if isEdit {
		id64, err := strconv.ParseUint(billIDRaw, 10, 64)
		if err != nil || id64 == 0 {
			return c.Redirect("/bills", fiber.StatusSeeOther)
		}
		editingID = uint(id64)
	}

	vm := pages.BillEditorVM{
		HasCompany:   true,
		IsEdit:       isEdit,
		EditingID:    editingID,
		BillNumber:   billNo,
		VendorID:     vendorRaw,
		BillDate:     dateRaw,
		TermCode:     termsRaw,
		DueDate:      dueDateRaw,
		Memo:         memo,
		CurrencyCode: currencyCodeRaw,
		ExchangeRate: exchangeRateRaw,
	}
	if isEdit && CanFromCtx(c, ActionBillUpdate) {
		vm.SubmitPath = fmt.Sprintf("/bills/%d/post", editingID)
	}
	_ = s.loadBillEditorDropdowns(companyID, &vm)

	// ── Validate header ───────────────────────────────────────────────────────
	if billNo != "" {
		if err := services.ValidateDocumentNumber(billNo); err != nil {
			vm.BillNumberError = err.Error()
		}
	}
	vendorID, vendorErr := services.ParseUint(vendorRaw)
	if vendorErr != nil || vendorID == 0 {
		vm.VendorError = "Vendor is required."
	}
	billDate, dateErr := time.Parse("2006-01-02", dateRaw)
	if dateErr != nil {
		vm.DateError = "Bill Date is required."
	}
	currencySelection, currencyErr, exchangeRateErr := normalizeDocumentCurrencySelection(
		vm.MultiCurrencyEnabled,
		vm.BaseCurrencyCode,
		vm.CompanyCurrencies,
		currencyCodeRaw,
		exchangeRateRaw,
	)
	vm.CurrencyError = currencyErr
	vm.ExchangeRateError = exchangeRateErr
	if vm.CurrencyError == "" {
		vm.CurrencyCode = currencySelection.CurrencyCode
	}
	if vm.ExchangeRateError == "" {
		vm.ExchangeRate = displayDocumentExchangeRate(currencySelection.CurrencyCode, currencySelection.ExchangeRate)
	}
	// Look up the selected payment term from the master table.
	var selectedTerm *models.PaymentTerm
	if termsRaw != "" {
		var pt models.PaymentTerm
		if err := s.DB.Where("company_id = ? AND code = ?", companyID, termsRaw).
			First(&pt).Error; err == nil {
			selectedTerm = &pt
		}
	}

	// ── Parse lines ───────────────────────────────────────────────────────────
	lineCount, _ := strconv.Atoi(lineCountRaw)
	if lineCount < 1 {
		lineCount = 0
	}

	type parsedBillLine struct {
		ExpenseAccountID   *uint
		Description        string
		Amount             decimal.Decimal
		TaxCodeID          *uint
		TaskID             *uint
		BillableCustomerID *uint
		IsBillable         bool
		ReinvoiceStatus    models.ReinvoiceStatus
	}

	var parsedLines []parsedBillLine
	var lineFormRows []pages.BillLineFormRow

	for i := 0; i < lineCount; i++ {
		key := func(field string) string { return fmt.Sprintf("%s[%d]", field, i) }
		accIDRaw := strings.TrimSpace(c.FormValue(key("line_expense_account_id")))
		desc := strings.TrimSpace(c.FormValue(key("line_description")))
		amtRaw := strings.TrimSpace(c.FormValue(key("line_amount")))
		tcIDRaw := strings.TrimSpace(c.FormValue(key("line_tax_code_id")))
		taskIDRaw := strings.TrimSpace(c.FormValue(key("line_task_id")))
		isBillable := c.FormValue(key("line_is_billable")) == "1"

		if isBillPlaceholderLine(desc, amtRaw, accIDRaw, tcIDRaw, taskIDRaw, isBillable) {
			continue
		}

		row := pages.BillLineFormRow{
			ExpenseAccountID: accIDRaw,
			Description:      desc,
			Amount:           amtRaw,
			TaxCodeID:        tcIDRaw,
			TaskID:           taskIDRaw,
			IsBillable:       isBillable,
		}

		amt, aErr := decimal.NewFromString(amtRaw)
		if aErr != nil || amt.IsNegative() {
			amt = decimal.Zero
		}
		if desc == "" {
			row.Error = "Description is required."
		}
		lineFormRows = append(lineFormRows, row)

		pl := parsedBillLine{Description: desc, Amount: amt}
		if id64, err := strconv.ParseUint(accIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.ExpenseAccountID = &id
		}
		if id64, err := strconv.ParseUint(tcIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.TaxCodeID = &id
		}
		if id64, err := strconv.ParseUint(taskIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.TaskID = &id
		}
		pl.IsBillable = isBillable
		parsedLines = append(parsedLines, pl)
	}

	accountNameByID := make(map[uint]string, len(vm.Accounts))
	for _, acc := range vm.Accounts {
		accountNameByID[acc.ID] = strings.TrimSpace(acc.Name)
	}
	for i := range parsedLines {
		if strings.TrimSpace(parsedLines[i].Description) != "" || parsedLines[i].ExpenseAccountID == nil {
			continue
		}
		if name := accountNameByID[*parsedLines[i].ExpenseAccountID]; name != "" {
			parsedLines[i].Description = name
			lineFormRows[i].Description = name
			lineFormRows[i].Error = ""
		}
	}

	for i := range parsedLines {
		linkage, err := services.NormalizeTaskCostLinkage(s.DB, services.TaskCostLinkageInput{
			CompanyID:  companyID,
			TaskID:     parsedLines[i].TaskID,
			IsBillable: parsedLines[i].IsBillable,
		})
		if err != nil {
			lineFormRows[i].Error = err.Error()
			continue
		}
		parsedLines[i].TaskID = linkage.TaskID
		parsedLines[i].BillableCustomerID = linkage.BillableCustomerID
		parsedLines[i].IsBillable = linkage.IsBillable
		parsedLines[i].ReinvoiceStatus = linkage.ReinvoiceStatus
	}

	vm.Lines = lineFormRows
	vm.InitialLinesJSON = buildBillInitialLinesJSON(lineFormRows)

	// ── Validation ────────────────────────────────────────────────────────────
	hasLineErr := false
	for _, r := range lineFormRows {
		if r.Error != "" {
			hasLineErr = true
			break
		}
	}
	if len(parsedLines) == 0 {
		vm.LinesError = "At least one line item is required."
	}
	if hasLineErr && vm.LinesError == "" {
		vm.LinesError = "Complete or remove any incomplete line items before saving."
	}

	if vm.BillNumberError != "" || vm.VendorError != "" || vm.DateError != "" ||
		vm.CurrencyError != "" || vm.ExchangeRateError != "" ||
		vm.LinesError != "" || hasLineErr {
		return pages.BillEditor(vm).Render(c.Context(), c)
	}

	// Verify vendor belongs to company.
	var venCount int64
	if err := s.DB.Model(&models.Vendor{}).
		Where("id = ? AND company_id = ?", uint(vendorID), companyID).
		Count(&venCount).Error; err != nil {
		vm.FormError = "Could not validate vendor."
		return pages.BillEditor(vm).Render(c.Context(), c)
	}
	if venCount == 0 {
		vm.VendorError = "Vendor is not valid for this company."
		return pages.BillEditor(vm).Render(c.Context(), c)
	}

	// Verify tax codes belong to company.
	for i, pl := range parsedLines {
		if pl.TaxCodeID == nil {
			continue
		}
		var tcCount int64
		if err := s.DB.Model(&models.TaxCode{}).
			Where("id = ? AND company_id = ? AND is_active = true", *pl.TaxCodeID, companyID).
			Count(&tcCount).Error; err != nil {
			vm.FormError = fmt.Sprintf("Could not validate line %d tax code.", i+1)
			return pages.BillEditor(vm).Render(c.Context(), c)
		}
		if tcCount == 0 {
			vm.FormError = fmt.Sprintf("Line %d has an invalid tax code.", i+1)
			return pages.BillEditor(vm).Render(c.Context(), c)
		}
	}

	// Duplicate bill number check (skip when empty — empty bill numbers are allowed).
	if billNo != "" {
		var dupCount int64
		dupQuery := s.DB.Model(&models.Bill{}).
			Where("company_id = ? AND LOWER(bill_number) = LOWER(?)", companyID, billNo)
		if isEdit {
			dupQuery = dupQuery.Where("id <> ?", editingID)
		}
		if err := dupQuery.Count(&dupCount).Error; err != nil {
			vm.FormError = "Could not validate bill number."
			return pages.BillEditor(vm).Render(c.Context(), c)
		}
		if dupCount > 0 {
			vm.BillNumberError = "Bill number already exists for this company (case-insensitive)."
			return pages.BillEditor(vm).Render(c.Context(), c)
		}
	}

	// ── Parse tax adjustments (user-edited per-code amounts) ─────────────────
	taxAdjCountRaw := strings.TrimSpace(c.FormValue("tax_adj_count"))
	taxAdjCount, _ := strconv.Atoi(taxAdjCountRaw)
	taxAdjMap := map[uint]decimal.Decimal{} // taxCodeID → user-supplied amount
	for i := 0; i < taxAdjCount; i++ {
		idRaw := strings.TrimSpace(c.FormValue(fmt.Sprintf("tax_adj_id[%d]", i)))
		amtRaw := strings.TrimSpace(c.FormValue(fmt.Sprintf("tax_adj_amount[%d]", i)))
		tcID64, err := strconv.ParseUint(idRaw, 10, 64)
		if err != nil || tcID64 == 0 {
			continue
		}
		amt, err := decimal.NewFromString(amtRaw)
		if err != nil || amt.IsNegative() {
			continue
		}
		taxAdjMap[uint(tcID64)] = amt.RoundBank(2)
	}

	// ── Compute line amounts ──────────────────────────────────────────────────
	taxCodeCache := map[uint]*models.TaxCode{}
	for _, pl := range parsedLines {
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
			First(&tc).Error; err == nil {
			taxCodeCache[tcID] = &tc
		}
	}

	type computedBillLine struct {
		parsedBillLine
		LineNet   decimal.Decimal
		LineTax   decimal.Decimal
		LineTotal decimal.Decimal
	}
	var computed []computedBillLine
	subtotal := decimal.Zero

	// First pass: compute line nets and unadjusted taxes; track per-code calculated totals.
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
				results := services.CalculateTax(lineNet, *tc)
				lineTax = services.SumTaxResults(results)
			}
		}
		subtotal = subtotal.Add(lineNet)
		idx := len(computed)
		computed = append(computed, computedBillLine{
			parsedBillLine: pl,
			LineNet:        lineNet,
			LineTax:        lineTax,
			LineTotal:      lineNet.Add(lineTax),
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

	// Second pass: if the user adjusted a tax code total, redistribute proportionally.
	taxTotal := decimal.Zero
	for codeID, cd := range codeData {
		adj, hasAdj := taxAdjMap[codeID]
		if !hasAdj || adj.Equal(cd.calcTotal) {
			taxTotal = taxTotal.Add(cd.calcTotal)
			continue
		}
		if cd.calcTotal.IsZero() {
			each := adj.Div(decimal.NewFromInt(int64(len(cd.indices)))).RoundBank(2)
			remainder := adj
			for i, li := range cd.indices {
				lineTax := each
				if i == len(cd.indices)-1 {
					lineTax = remainder
				}
				computed[li].LineTax = lineTax
				computed[li].LineTotal = computed[li].LineNet.Add(lineTax)
				remainder = remainder.Sub(lineTax)
			}
		} else {
			remaining := adj
			for i, li := range cd.indices {
				var lineTax decimal.Decimal
				if i == len(cd.indices)-1 {
					lineTax = remaining
				} else {
					lineTax = computed[li].LineTax.Mul(adj).Div(cd.calcTotal).RoundBank(2)
				}
				computed[li].LineTax = lineTax
				computed[li].LineTotal = computed[li].LineNet.Add(lineTax)
				remaining = remaining.Sub(lineTax)
			}
		}
		taxTotal = taxTotal.Add(adj)
	}

	grandTotal := subtotal.Add(taxTotal)

	// ── Compute due date ──────────────────────────────────────────────────────
	var dueDate *time.Time
	if selectedTerm != nil && selectedTerm.NetDays > 0 {
		dueDate = models.ComputeDueDate(billDate, selectedTerm.NetDays)
	} else if dueDateRaw != "" {
		if d, err := time.Parse("2006-01-02", dueDateRaw); err == nil {
			dueDate = &d
		}
	}

	// ── DB transaction ────────────────────────────────────────────────────────
	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}

	var savedBillID uint
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var bill models.Bill

		if isEdit {
			if err := tx.Where("id = ? AND company_id = ?", editingID, companyID).First(&bill).Error; err != nil {
				return fmt.Errorf("bill not found")
			}
			if bill.Status != models.BillStatusDraft {
				return fmt.Errorf("only draft bills can be edited")
			}
			bill.BillNumber = billNo
			bill.VendorID = uint(vendorID)
			bill.BillDate = billDate
			if selectedTerm != nil {
				bill.PaymentTermSnapshot = models.BuildSnapshot(*selectedTerm)
			} else {
				bill.PaymentTermSnapshot = models.PaymentTermSnapshot{TermCode: termsRaw}
			}
			bill.DueDate = dueDate
			bill.Memo = memo
			bill.CurrencyCode = currencySelection.CurrencyCode
			bill.ExchangeRate = currencySelection.ExchangeRate
			bill.Subtotal = subtotal
			bill.TaxTotal = taxTotal
			bill.Amount = grandTotal
			if err := tx.Save(&bill).Error; err != nil {
				return err
			}
			if err := tx.Where("bill_id = ?", bill.ID).Delete(&models.BillLine{}).Error; err != nil {
				return err
			}
		} else {
			var billSnap models.PaymentTermSnapshot
			if selectedTerm != nil {
				billSnap = models.BuildSnapshot(*selectedTerm)
			} else {
				billSnap = models.PaymentTermSnapshot{TermCode: termsRaw}
			}
			bill = models.Bill{
				CompanyID:           companyID,
				BillNumber:          billNo,
				VendorID:            uint(vendorID),
				BillDate:            billDate,
				PaymentTermSnapshot: billSnap,
				DueDate:             dueDate,
				Status:              models.BillStatusDraft,
				Memo:                memo,
				CurrencyCode:        currencySelection.CurrencyCode,
				ExchangeRate:        currencySelection.ExchangeRate,
				Subtotal:            subtotal,
				TaxTotal:            taxTotal,
				Amount:              grandTotal,
			}
			if err := tx.Create(&bill).Error; err != nil {
				return err
			}
		}

		for i, cl := range computed {
			line := models.BillLine{
				CompanyID:          companyID,
				BillID:             bill.ID,
				SortOrder:          uint(i + 1),
				Description:        cl.Description,
				Qty:                decimal.NewFromInt(1),
				UnitPrice:          cl.Amount,
				LineNet:            cl.LineNet,
				LineTax:            cl.LineTax,
				LineTotal:          cl.LineTotal,
				ExpenseAccountID:   cl.ExpenseAccountID,
				TaxCodeID:          cl.TaxCodeID,
				TaskID:             cl.TaskID,
				BillableCustomerID: cl.BillableCustomerID,
				IsBillable:         cl.IsBillable,
				ReinvoiceStatus:    cl.ReinvoiceStatus,
			}
			if err := tx.Create(&line).Error; err != nil {
				return err
			}
		}

		action := "bill.created"
		if isEdit {
			action = "bill.updated"
		}
		savedBillID = bill.ID
		return services.WriteAuditLogWithContextDetails(tx, action, "bill", bill.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, &uid, nil,
			map[string]any{
				"bill_number": bill.BillNumber,
				"vendor_id":   bill.VendorID,
				"total":       bill.Amount.StringFixed(2),
				"line_count":  len(computed),
			},
		)
	})
	if err != nil {
		vm.FormError = billSaveErrorMessage(err)
		return pages.BillEditor(vm).Render(c.Context(), c)
	}

	return redirectTo(c, fmt.Sprintf("/bills/%d/edit?saved=1&locked=1", savedBillID))
}

// handleBillPost submits a saved draft bill and posts it to accounting.
func (s *Server) handleBillPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/bills", "company context required")
	}

	billID, err := parseBillID(c)
	if err != nil {
		return redirectErr(c, "/bills", "invalid bill ID")
	}

	user := UserFromCtx(c)
	actor := "system"
	var uid *uuid.UUID
	if user != nil {
		u := user.ID
		uid = &u
		if user.Email != "" {
			actor = user.Email
		}
	}

	if err := services.PostBill(s.DB, companyID, billID, actor, uid); err != nil {
		return redirectErr(c, fmt.Sprintf("/bills/%d/edit?locked=1", billID), "Could not submit bill.")
	}

	return redirectTo(c, "/bills?posted=1")
}

func (s *Server) billsForCompany(companyID uint) ([]models.Bill, error) {
	var bills []models.Bill
	err := s.DB.Preload("Vendor").Where("company_id = ?", companyID).Order("bill_date desc, id desc").Find(&bills).Error
	return bills, err
}

// loadBillEditorDropdowns fills vendors, accounts, taxCodes, paymentTerms + JSON blobs on vm.
// Also loads multi-currency settings when the company has it enabled.
func (s *Server) loadBillEditorDropdowns(companyID uint, vm *pages.BillEditorVM) error {
	if err := s.DB.Where("company_id = ?", companyID).Order("name asc").
		Find(&vm.Vendors).Error; err != nil {
		return err
	}
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("code asc").
		Find(&vm.Accounts).Error; err != nil {
		return err
	}
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").
		Find(&vm.TaxCodes).Error; err != nil {
		return err
	}
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").
		Find(&vm.PaymentTerms).Error; err != nil {
		return err
	}
	selectableTasks, err := services.ListSelectableTasks(s.DB, companyID)
	if err != nil {
		return err
	}
	vm.SelectableTasks = selectableTasks
	vm.AccountsJSON = buildBillAccountsJSON(vm.Accounts)
	vm.TaxCodesJSON = buildTaxCodesJSON(vm.TaxCodes)
	vm.TasksJSON = buildBillTasksJSON(vm.SelectableTasks)
	vm.PaymentTermsJSON = buildPaymentTermsJSON(vm.PaymentTerms)
	vm.VendorsTermsJSON = buildVendorsTermsJSON(vm.Vendors)

	// Multi-currency: load company settings and enabled currencies.
	var company models.Company
	if err := s.DB.Select("id", "base_currency_code", "multi_currency_enabled").
		First(&company, companyID).Error; err == nil {
		vm.MultiCurrencyEnabled = company.MultiCurrencyEnabled
		vm.BaseCurrencyCode = company.BaseCurrencyCode
		if company.MultiCurrencyEnabled {
			ccs, _ := services.ListCompanyCurrencies(s.DB, companyID)
			vm.CompanyCurrencies = ccs
		}
	}
	return nil
}

type billAccountJSONItem struct {
	ID   uint   `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func buildBillAccountsJSON(accounts []models.Account) string {
	items := make([]billAccountJSONItem, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, billAccountJSONItem{ID: a.ID, Code: a.Code, Name: a.Name})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// buildVendorsTermsJSON returns a JSON object mapping vendor ID → DefaultPaymentTermCode.
func buildVendorsTermsJSON(vendors []models.Vendor) string {
	m := make(map[string]string, len(vendors))
	for _, v := range vendors {
		if v.DefaultPaymentTermCode != "" {
			m[strconv.FormatUint(uint64(v.ID), 10)] = v.DefaultPaymentTermCode
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// buildBillInitialLinesJSON serialises BillLineFormRow slice for Alpine's data-initial-lines.
func buildBillInitialLinesJSON(rows []pages.BillLineFormRow) string {
	type alpineLine struct {
		ExpenseAccountID string `json:"expense_account_id"`
		Description      string `json:"description"`
		Amount           string `json:"amount"`
		TaxCodeID        string `json:"tax_code_id"`
		TaskID           string `json:"task_id"`
		IsBillable       bool   `json:"is_billable"`
		LineNet          string `json:"line_net"`
		LineTax          string `json:"line_tax"`
		Error            string `json:"error"`
	}
	items := make([]alpineLine, 0, len(rows))
	for _, r := range rows {
		net := r.LineNet
		if net == "" {
			net = "0.00"
		}
		tax := r.LineTax
		if tax == "" {
			tax = "0.00"
		}
		items = append(items, alpineLine{
			ExpenseAccountID: r.ExpenseAccountID,
			Description:      r.Description,
			Amount:           r.Amount,
			TaxCodeID:        r.TaxCodeID,
			TaskID:           r.TaskID,
			IsBillable:       r.IsBillable,
			LineNet:          net,
			LineTax:          tax,
			Error:            r.Error,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

type billTaskJSONItem struct {
	ID           uint   `json:"id"`
	Title        string `json:"title"`
	CustomerName string `json:"customer_name"`
	Status       string `json:"status"`
}

func buildBillTasksJSON(tasks []models.Task) string {
	items := make([]billTaskJSONItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, billTaskJSONItem{
			ID:           task.ID,
			Title:        task.Title,
			CustomerName: task.Customer.Name,
			Status:       string(task.Status),
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func parseBillID(c *fiber.Ctx) (uint, error) {
	idStr := c.Params("id")
	id64, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint(id64), nil
}

func isBillPlaceholderLine(desc, amountRaw, expenseAccountIDRaw, taxCodeIDRaw, taskIDRaw string, isBillable bool) bool {
	if desc != "" || expenseAccountIDRaw != "" || taxCodeIDRaw != "" || taskIDRaw != "" || isBillable {
		return false
	}

	if amountRaw == "" {
		return true
	}

	amt, err := decimal.NewFromString(amountRaw)
	if err != nil {
		return false
	}
	return amt.IsZero()
}

// handleBillVoid voids a posted bill and creates a reversal JE.
// POST /bills/:id/void
func (s *Server) handleBillVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.Params("id"))
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/bills", fiber.StatusSeeOther)
	}
	billID := uint(id64)

	user := UserFromCtx(c)
	var userID *uuid.UUID
	actor := "system"
	if user != nil {
		uid := user.ID
		userID = &uid
		if user.Email != "" {
			actor = user.Email
		}
	}

	if err := services.VoidBill(s.DB, companyID, billID, actor, userID); err != nil {
		return c.Redirect("/bills?voiderror=1", fiber.StatusSeeOther)
	}

	return c.Redirect("/bills?voided=1", fiber.StatusSeeOther)
}
