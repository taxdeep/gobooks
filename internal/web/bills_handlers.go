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
	"gobooks/internal/searchprojection/producers"
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

	// Optional: pre-fill from a Purchase Order when the user came
	// through the "Create Bill from PO" button on the PO detail page.
	// Pure UX shortcut — no identity FK is persisted on the created
	// Bill today, because the Bill schema has no purchase_order_id
	// column and adding one is a separate, dedicated slice. The
	// operator is free to edit every pre-filled field before saving.
	if fromPOStr := strings.TrimSpace(c.Query("from_po")); fromPOStr != "" {
		if fromPO64, err := strconv.ParseUint(fromPOStr, 10, 64); err == nil && fromPO64 != 0 {
			if s.prefillBillFromPO(companyID, uint(fromPO64), &vm) {
				// Only populate InitialLinesJSON when we actually
				// pre-filled lines; the fresh-form path intentionally
				// leaves it empty so the editor shows its default row.
				vm.InitialLinesJSON = buildBillInitialLinesJSON(vm.Lines)
			}
		}
	}

	return pages.BillEditor(vm).Render(c.Context(), c)
}

// prefillBillFromPO maps a Purchase Order's header + lines onto the
// BillEditor VM so the operator lands on /bills/new with the form
// already populated. Silent on errors — a missing / cross-company /
// cancelled PO simply leaves the form blank rather than flashing an
// error, because the button that sends us here already validates
// visibility against PO state.
func (s *Server) prefillBillFromPO(companyID, poID uint, vm *pages.BillEditorVM) bool {
	po, err := services.GetPurchaseOrder(s.DB, companyID, poID)
	if err != nil || po == nil {
		return false
	}
	if po.CompanyID != companyID {
		return false
	}
	// Header pre-fill.
	vm.VendorID = strconv.FormatUint(uint64(po.VendorID), 10)
	if po.CurrencyCode != "" && !strings.EqualFold(po.CurrencyCode, vm.BaseCurrencyCode) {
		vm.CurrencyCode = po.CurrencyCode
		if po.ExchangeRate.GreaterThan(decimal.Zero) && !po.ExchangeRate.Equal(decimal.NewFromInt(1)) {
			vm.ExchangeRate = po.ExchangeRate.String()
		}
	}
	if strings.TrimSpace(po.Notes) != "" {
		// PO's operator-facing Notes maps to Bill's Memo (the bill
		// model uses Memo, not Notes, for the same concept).
		vm.Memo = po.Notes
	}
	// Line pre-fill: one bill line per PO line, Amount = LineNet
	// (qty × unit_price). Bill form is amount-based; tax is applied
	// at bill time so we deliberately do NOT carry over a TaxCode —
	// the operator picks the vendor's actual tax when billing.
	//
	// ExpenseAccountID (Bill's "Category" column) derivation, in
	// priority order — first non-zero hit wins:
	//   1. The PO line's explicit ExpenseAccountID. PurchaseOrderLine
	//      has the field today (model + preload), but the current PO
	//      editor doesn't expose it as a column, so this branch only
	//      fires for lines whose account was set programmatically /
	//      via API. Future: add Category column to the PO editor.
	//   2. ProductService.InventoryAccountID for stock items. Bill
	//      posting's AdjustBillFragmentsForInventory (legacy flag=off)
	//      / AdjustBillFragmentsForGRIRClearing (Phase H flag=on)
	//      both redirect away from the asset account at post time, so
	//      using it here as the "category" is safe — the routing
	//      lands in the right GL place regardless of the rail state.
	//   3. ProductService.COGSAccountID as a fallback for items that
	//      have no inventory account configured.
	//   4. nil — leave the Category dropdown on "-- None --" so the
	//      operator picks. Service items with no account hint and
	//      free-form lines fall through here.
	rows := make([]pages.BillLineFormRow, 0, len(po.Lines))
	for _, pl := range po.Lines {
		desc := strings.TrimSpace(pl.Description)
		if desc == "" && pl.ProductService != nil {
			desc = pl.ProductService.Name
		}
		amount := pl.LineNet.StringFixed(2)
		if pl.LineNet.IsZero() && pl.Qty.GreaterThan(decimal.Zero) && pl.UnitPrice.GreaterThan(decimal.Zero) {
			amount = pl.Qty.Mul(pl.UnitPrice).RoundBank(2).StringFixed(2)
		}
		expenseAcctID := derivePOLineExpenseAccountID(pl)
		expenseAcctStr := ""
		if expenseAcctID != 0 {
			expenseAcctStr = strconv.FormatUint(uint64(expenseAcctID), 10)
		}
		// Rule #4 / IN.1: carry the PO line's product identity +
		// Qty + UnitPrice through to the new Bill line so stock
		// items actually form inventory on Bill post (legacy flag=off
		// path). Previously we flattened to a single Amount and lost
		// ProductServiceID/Qty/UnitPrice — which is exactly why the
		// post-PO→Bill inventory chain was silently broken.
		psIDStr := ""
		qtyStr := ""
		unitPriceStr := ""
		if pl.ProductServiceID != nil && *pl.ProductServiceID != 0 {
			psIDStr = strconv.FormatUint(uint64(*pl.ProductServiceID), 10)
			qtyStr = pl.Qty.StringFixed(4)
			unitPriceStr = pl.UnitPrice.StringFixed(4)
		}
		rows = append(rows, pages.BillLineFormRow{
			ProductServiceID: psIDStr,
			ExpenseAccountID: expenseAcctStr,
			Description:      desc,
			Qty:              qtyStr,
			UnitPrice:        unitPriceStr,
			Amount:           amount,
		})
	}
	if len(rows) > 0 {
		vm.Lines = rows
	}
	return true
}

// derivePOLineExpenseAccountID picks the best account to pre-populate
// the Bill's "Category" cell from a PO line. Returns 0 when no signal
// is available (operator picks).
//
// See prefillBillFromPO for the priority order rationale. Kept as a
// private helper so future hints (e.g. vendor-default expense, COA
// system_key fallback) can be added in one spot.
func derivePOLineExpenseAccountID(pl models.PurchaseOrderLine) uint {
	if pl.ExpenseAccountID != nil && *pl.ExpenseAccountID != 0 {
		return *pl.ExpenseAccountID
	}
	if pl.ProductService == nil {
		return 0
	}
	if pl.ProductService.InventoryAccountID != nil && *pl.ProductService.InventoryAccountID != 0 {
		return *pl.ProductService.InventoryAccountID
	}
	if pl.ProductService.COGSAccountID != nil && *pl.ProductService.COGSAccountID != 0 {
		return *pl.ProductService.COGSAccountID
	}
	return 0
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
		WarehouseID:  optUintStr(bill.WarehouseID),
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
		// Rule #4 / IN.1: ProductServiceID, Qty and UnitPrice MUST
		// round-trip through the form reload. A prior revision only
		// persisted ExpenseAccountID / Description / Amount — which
		// silently turned every item-aware line into a "— Expense only
		// —" row on reload, dropping the stock-item flag and reducing
		// Qty to the default 1 / UnitPrice to 0. Submit would then
		// either re-save garbage or fail the post with no stack trace
		// visible to the operator.
		vm.Lines = append(vm.Lines, pages.BillLineFormRow{
			ProductServiceID: optUintStr(l.ProductServiceID),
			ExpenseAccountID: optUintStr(l.ExpenseAccountID),
			Description:      l.Description,
			Qty:              l.Qty.String(),
			UnitPrice:        l.UnitPrice.StringFixed(4),
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
	warehouseIDRaw := strings.TrimSpace(c.FormValue("warehouse_id"))
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
		HasCompany:  true,
		IsEdit:      isEdit,
		EditingID:   editingID,
		BillNumber:  billNo,
		VendorID:    vendorRaw,
		BillDate:    dateRaw,
		TermCode:    termsRaw,
		DueDate:     dueDateRaw,
		Memo:        memo,
		WarehouseID: warehouseIDRaw,
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
		ProductServiceID   *uint
		ExpenseAccountID   *uint
		Description        string
		Qty                decimal.Decimal
		Unit               string
		UnitPrice          decimal.Decimal
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
		psIDRaw := strings.TrimSpace(c.FormValue(key("line_product_service_id")))
		accIDRaw := strings.TrimSpace(c.FormValue(key("line_expense_account_id")))
		desc := strings.TrimSpace(c.FormValue(key("line_description")))
		qtyRaw := strings.TrimSpace(c.FormValue(key("line_qty")))
		unitRaw := strings.TrimSpace(c.FormValue(key("line_unit")))
		unitPriceRaw := strings.TrimSpace(c.FormValue(key("line_unit_price")))
		amtRaw := strings.TrimSpace(c.FormValue(key("line_amount")))
		tcIDRaw := strings.TrimSpace(c.FormValue(key("line_tax_code_id")))
		taskIDRaw := strings.TrimSpace(c.FormValue(key("line_task_id")))
		isBillable := c.FormValue(key("line_is_billable")) == "1"

		if isBillPlaceholderLine(desc, amtRaw, accIDRaw, tcIDRaw, taskIDRaw, isBillable) {
			continue
		}

		row := pages.BillLineFormRow{
			ProductServiceID: psIDRaw,
			ExpenseAccountID: accIDRaw,
			Description:      desc,
			Qty:              qtyRaw,
			Unit:             unitRaw,
			UnitPrice:        unitPriceRaw,
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

		pl := parsedBillLine{Description: desc, Amount: amt, Unit: unitRaw}
		if id64, err := strconv.ParseUint(psIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.ProductServiceID = &id
		}
		// Rule #4: when an item is picked, Qty and UnitPrice come from
		// the form; Amount is computed (qty × unit_price). When no
		// item is picked, legacy fallback: Qty=1, UnitPrice=Amount.
		if pl.ProductServiceID != nil {
			qty, qErr := decimal.NewFromString(qtyRaw)
			if qErr != nil || !qty.IsPositive() {
				qty = decimal.NewFromInt(1)
			}
			up, upErr := decimal.NewFromString(unitPriceRaw)
			if upErr != nil || up.IsNegative() {
				up = decimal.Zero
			}
			pl.Qty = qty
			pl.UnitPrice = up
			// Authoritative Amount for the bill JE is qty × unit_price
			// when an item is picked. Overrides whatever was in the
			// hidden/readonly Amount input — operator can't desync by
			// editing Amount independently.
			pl.Amount = qty.Mul(up).RoundBank(2)
		} else {
			pl.Qty = decimal.NewFromInt(1)
			pl.UnitPrice = amt
		}
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
			Where("company_id = ? AND LOWER(bill_number) = LOWER(?) AND status <> ?", companyID, billNo, models.BillStatusVoided)
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

	// Parse optional warehouse selection.
	var billWarehouseID *uint
	if wid64, err := strconv.ParseUint(warehouseIDRaw, 10, 64); err == nil && wid64 > 0 {
		wid := uint(wid64)
		billWarehouseID = &wid
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
			bill.WarehouseID = billWarehouseID
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
				WarehouseID:         billWarehouseID,
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
				ProductServiceID:   cl.ProductServiceID,
				Description:        cl.Description,
				Qty:                cl.Qty,
				UnitPrice:          cl.UnitPrice,
				LineNet:             cl.LineNet,
				LineTax:             cl.LineTax,
				LineTotal:           cl.LineTotal,
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
	_ = producers.ProjectBill(c.Context(), s.DB, s.SearchProjector, companyID, savedBillID)

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
		// Surface the service's underlying message so the operator
		// can see WHY the post failed (missing COGS account on a
		// stock item, insufficient stock, warehouse ambiguity, etc.)
		// A prior revision collapsed everything to the opaque string
		// "Could not submit bill." — operators then had no actionable
		// cue and had to guess. The raw err.Error() is already
		// formatted by PostBill for human consumption.
		return redirectErr(c, fmt.Sprintf("/bills/%d/edit?locked=1", billID),
			"Could not submit bill: "+err.Error())
	}
	s.ReportCache.InvalidateCompany(companyID)
	_ = producers.ProjectBill(c.Context(), s.DB, s.SearchProjector, companyID, billID)

	return redirectTo(c, fmt.Sprintf("/bills/%d", billID))
}

func (s *Server) billsForCompany(companyID uint) ([]models.Bill, error) {
	var bills []models.Bill
	err := s.DB.Preload("Vendor").Where("company_id = ?", companyID).Order("bill_date desc, id desc").Find(&bills).Error
	return bills, err
}

// loadBillEditorDropdowns fills vendors, accounts, taxCodes, paymentTerms + JSON blobs on vm.
// Also loads multi-currency settings when the company has it enabled.
func (s *Server) loadBillEditorDropdowns(companyID uint, vm *pages.BillEditorVM) error {
	// Active vendors only for the picker — deactivated vendors stay on
	// historical bills but can't be selected for new ones.
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").
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
	vm.Warehouses, _ = services.ListWarehouses(s.DB, companyID)

	// Rule #4 / IN.1: product catalog for line-level Item picker. Same
	// filter as PO editor (active items; no type restriction — Q1 says
	// the picker always shows amount-only fallback + whatever items
	// exist; operator can leave Item blank for pure expense lines).
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").
		Find(&vm.Products).Error; err != nil {
		return err
	}

	vm.AccountsJSON = buildBillAccountsJSON(vm.Accounts)
	vm.TaxCodesJSON = buildTaxCodesJSON(vm.TaxCodes)
	vm.TasksJSON = buildBillTasksJSON(vm.SelectableTasks)
	vm.ProductsJSON = buildBillProductsJSON(vm.Products)
	vm.PaymentTermsJSON = buildPaymentTermsJSON(vm.PaymentTerms)
	vm.VendorsTermsJSON = buildVendorsTermsJSON(vm.Vendors)
	vm.WarehousesJSON = buildBillWarehousesJSON(vm.Warehouses)

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
		ProductServiceID string `json:"product_service_id"`
		ExpenseAccountID string `json:"expense_account_id"`
		Description      string `json:"description"`
		Qty              string `json:"qty"`
		Unit             string `json:"unit"`
		UnitPrice        string `json:"unit_price"`
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
		qty := r.Qty
		if qty == "" {
			qty = "1"
		}
		unitPrice := r.UnitPrice
		if unitPrice == "" {
			unitPrice = "0.00"
		}
		items = append(items, alpineLine{
			ProductServiceID: r.ProductServiceID,
			ExpenseAccountID: r.ExpenseAccountID,
			Description:      r.Description,
			Qty:              qty,
			Unit:             r.Unit,
			UnitPrice:        unitPrice,
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

// buildBillProductsJSON serialises the product catalog for the
// line-level Item picker (Rule #4 / IN.1). Shape mirrors the
// existing AccountsJSON pattern: minimal fields, one row per active
// ProductService.
//
// Exposed fields:
//   - id, sku, name — display identity for the picker label
//   - is_stock_item — drives the "· stock" vs "· service" badge on
//     each option, matching the PO editor's labelling convention
//   - inventory_account_id / cogs_account_id — read by the Alpine
//     store to auto-populate the line's Category (ExpenseAccountID)
//     when an item is selected (same derivation chain as
//     derivePOLineExpenseAccountID for PO→Bill conversion)
func buildBillProductsJSON(products []models.ProductService) string {
	type alpineProduct struct {
		ID                 uint   `json:"id"`
		SKU                string `json:"sku"`
		Name               string `json:"name"`
		IsStockItem        bool   `json:"is_stock_item"`
		InventoryAccountID uint   `json:"inventory_account_id"`
		COGSAccountID      uint   `json:"cogs_account_id"`
	}
	items := make([]alpineProduct, 0, len(products))
	for _, p := range products {
		a := alpineProduct{
			ID:          p.ID,
			SKU:         p.SKU,
			Name:        p.Name,
			IsStockItem: p.IsStockItem,
		}
		if p.InventoryAccountID != nil {
			a.InventoryAccountID = *p.InventoryAccountID
		}
		if p.COGSAccountID != nil {
			a.COGSAccountID = *p.COGSAccountID
		}
		items = append(items, a)
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// buildBillWarehousesJSON serialises the warehouse list for the
// type-ahead combobox. The `search` field is a lowercased bag
// (code + name + description + city + country + address) so a
// single substring match against the user query hits any of
// those. `label` is what the input displays after selection.
func buildBillWarehousesJSON(warehouses []models.Warehouse) string {
	type alpineWarehouse struct {
		ID     uint   `json:"id"`
		Code   string `json:"code"`
		Name   string `json:"name"`
		Label  string `json:"label"`
		Addr   string `json:"addr"`
		Search string `json:"search"`
	}
	items := make([]alpineWarehouse, 0, len(warehouses))
	for _, w := range warehouses {
		if !w.IsActive {
			continue
		}
		addrParts := make([]string, 0, 3)
		if w.AddressLine1 != "" {
			addrParts = append(addrParts, w.AddressLine1)
		}
		if w.City != "" {
			addrParts = append(addrParts, w.City)
		}
		if w.Country != "" {
			addrParts = append(addrParts, w.Country)
		}
		addr := strings.Join(addrParts, ", ")
		search := strings.ToLower(strings.Join([]string{
			w.Code, w.Name, w.Description, w.AddressLine1, w.City, w.Country,
		}, " "))
		items = append(items, alpineWarehouse{
			ID:     w.ID,
			Code:   w.Code,
			Name:   w.Name,
			Label:  w.Name + " (" + w.Code + ")",
			Addr:   addr,
			Search: search,
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

// handleBillDetail renders the read-only bill detail page.
// GET /bills/:id
func (s *Server) handleBillDetail(c *fiber.Ctx) error {
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

	var bill models.Bill
	if err := s.DB.
		Preload("Vendor").
		Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc") }).
		Preload("JournalEntry").
		Where("id = ? AND company_id = ?", billID, companyID).
		First(&bill).Error; err != nil {
		return c.Redirect("/bills", fiber.StatusSeeOther)
	}

	// Draft bills redirect to the edit page.
	if bill.Status == models.BillStatusDraft {
		return c.Redirect(fmt.Sprintf("/bills/%d/edit", billID), fiber.StatusSeeOther)
	}

	// Load AP credit applications with VCN info.
	var apApps []models.APCreditApplication
	s.DB.Preload("VendorCreditNote").
		Where("bill_id = ? AND company_id = ?", billID, companyID).
		Order("applied_at asc").Find(&apApps)

	vm := pages.BillDetailVM{
		HasCompany:           true,
		Bill:                 bill,
		APCreditApplications: apApps,
	}
	if bill.JournalEntry != nil {
		vm.JournalNo = bill.JournalEntry.JournalNo
	}
	return pages.BillDetail(vm).Render(c.Context(), c)
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
	s.ReportCache.InvalidateCompany(companyID)
	_ = producers.ProjectBill(c.Context(), s.DB, s.SearchProjector, companyID, billID)

	return c.Redirect("/bills?voided=1", fiber.StatusSeeOther)
}
