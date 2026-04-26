// 遵循project_guide.md
package web

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/searchprojection/producers"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

type parsedInvoiceLine struct {
	ProductServiceID *uint
	Description      string
	Qty              decimal.Decimal
	UnitPrice        decimal.Decimal
	TaxCodeID        *uint
}

// handleInvoiceDetail renders the read-only invoice detail page.
func (s *Server) handleInvoiceDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.Params("id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}

	var inv models.Invoice
	err := s.DB.
		Preload("Customer").
		Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Preload("JournalEntry").
		Preload("SalesOrder").
		Where("id = ? AND company_id = ?", uint(id64), companyID).
		First(&inv).Error
	if err != nil {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}

	// Load send defaults for non-draft, non-voided invoices.
	// Uses the same resolution pipeline as SendInvoiceByEmail so the modal matches
	// exactly what would be sent.
	var sendDefaults *services.InvoiceSendDefaults
	if inv.Status != models.InvoiceStatusDraft && inv.Status != models.InvoiceStatusVoided {
		sendDefaults, _ = services.GetInvoiceSendDefaults(s.DB, companyID, uint(id64))
	}

	// Load email history (all attempts, newest first).
	emailHistory, _ := services.GetInvoiceEmailHistory(s.DB, companyID, inv.ID)

	// Load company templates for draft bind-template action.
	var templates []models.InvoiceTemplate
	if inv.Status == models.InvoiceStatusDraft {
		templates, _ = services.ListInvoiceTemplates(s.DB, companyID)
	}

	// Load credit note applications for this invoice.
	var cnApplications []models.CreditNoteApplication
	if inv.Status != models.InvoiceStatusDraft {
		s.DB.Preload("Invoice").
			Where("invoice_id = ? AND company_id = ?", inv.ID, companyID).
			Order("applied_at asc").Find(&cnApplications)
	}

	// Load payment requests for this invoice.
	paymentReqs, _ := services.ListPaymentRequestsForInvoice(s.DB, companyID, inv.ID)

	// Load gateway accounts for "Request Payment" form.
	gatewayAccts, _ := services.ListGatewayAccounts(s.DB, companyID)

	// Load active hosted share link for non-draft, non-voided invoices.
	// Nil when no active link exists or when the invoice is draft/voided.
	var activeLink *models.InvoiceHostedLink
	if inv.Status != models.InvoiceStatusDraft && inv.Status != models.InvoiceStatusVoided {
		if link, err := services.GetActiveHostedLink(s.DB, companyID, inv.ID); err == nil {
			activeLink = link
		}
	}

	// GatewayPaymentStatus: surface the latest hosted attempt status for the
	// operator-facing detail page so they can see if a webhook-confirmed payment
	// is waiting to be applied.
	// GatewaySettlementStatus/Reason: Batch 12 — persisted outcome of the last
	// auto-settle or manual retry so operators can act without reading logs.
	var gatewayPaymentStatus, gatewaySettlementStatus, gatewaySettlementReason string
	if latestAttempt := services.LatestAttemptForInvoice(s.DB, inv.ID, companyID); latestAttempt != nil {
		gatewayPaymentStatus = string(latestAttempt.Status)
		gatewaySettlementStatus = latestAttempt.SettlementStatus
		gatewaySettlementReason = latestAttempt.SettlementReason
	}

	vm := pages.InvoiceDetailVM{
		HasCompany:         true,
		Invoice:            inv,
		ActiveLink:         activeLink,
		NewShareURL:        strings.TrimSpace(c.Query("newlink")),
		SendDefaults:       sendDefaults,
		EmailHistory:       emailHistory,
		Templates:          templates,
		JustVoided:         c.Query("voided") == "1",
		JustIssued:         c.Query("issued") == "1",
		JustSent:           c.Query("sent") == "1",
		JustPaid:           c.Query("paid") == "1" || c.Query("received") == "1",
		JustTemplateBound:  c.Query("tmplbound") == "1",
		FormError:          strings.TrimSpace(c.Query("error")),
		VoidError:          c.Query("voiderror"),
		EmailError:         c.Query("emailerror"),
		PaymentRequests:    paymentReqs,
		GatewayAccounts:    gatewayAccts,
		JustPaymentCreated: c.Query("paymentcreated") == "1",
		IsChannelOrigin:      inv.ChannelOrderID != nil,
		PDFAvailable:         services.PDFGeneratorAvailable(),
		GatewayPaymentStatus:    gatewayPaymentStatus,
		GatewaySettlementStatus: gatewaySettlementStatus,
		GatewaySettlementReason: gatewaySettlementReason,
		JustSettled:             c.Query("settled") == "1",
		CreditNoteApplications:  cnApplications,
	}
	if inv.JournalEntry != nil {
		vm.JournalNo = inv.JournalEntry.JournalNo
	}

	return pages.InvoiceDetail(vm).Render(c.Context(), c)
}

// handleInvoiceNew renders the blank invoice editor.
//
// Supports an optional `?sales_order_id=X` query param — when
// present, pre-fills customer, currency, memo, and line rows from
// a Confirmed SalesOrder. Remaining-quantity semantics: each
// pre-filled line's qty is `Quantity - InvoicedQty`. Lines fully
// invoiced already (remaining <= 0) are skipped. This is a UI
// shortcut only — the operator still reviews + saves the draft
// like any other invoice. The post path does NOT yet link back
// to update SO.InvoicedAmount / line.InvoicedQty; that tracking
// is a follow-on slice.
func (s *Server) handleInvoiceNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	nextNo, err := services.SuggestNextInvoiceNumber(s.DB, companyID)
	if err != nil {
		nextNo = "IN001"
	}

	today := time.Now().Format("2006-01-02")
	vm := pages.InvoiceEditorVM{
		HasCompany:    true,
		IsEdit:        false,
		InvoiceNumber: nextNo,
		InvoiceDate:   today,
	}

	if err := s.loadEditorDropdowns(companyID, &vm); err != nil {
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

	// SO → Invoice shortcut. Pre-fills customer + currency + memo +
	// line rows from a Confirmed SalesOrder's remaining qty. Bad /
	// missing / wrong-company sales_order_id is ignored silently —
	// the operator just sees the blank editor instead.
	if soIDStr := strings.TrimSpace(c.Query("sales_order_id")); soIDStr != "" {
		if soID, parseErr := strconv.ParseUint(soIDStr, 10, 64); parseErr == nil && soID > 0 {
			s.prefillInvoiceFromSalesOrder(companyID, uint(soID), &vm)
		}
	}

	vm.InitialLinesJSON = buildInitialLinesJSON(vm.Lines)
	return pages.InvoiceEditor(vm).Render(c.Context(), c)
}

// prefillInvoiceFromSalesOrder loads the given SalesOrder (scoped
// to company) and, if it's a Confirmed order with remaining
// uninvoiced qty on at least one line, sets vm.CustomerID,
// vm.CurrencyCode, vm.Memo, and vm.Lines from it. Silent no-op on
// any error or on a Draft / Cancelled / fully-invoiced SO — the
// shortcut is a convenience, not a required flow.
func (s *Server) prefillInvoiceFromSalesOrder(companyID, soID uint, vm *pages.InvoiceEditorVM) {
	var so models.SalesOrder
	if err := s.DB.Preload("Lines").
		Where("id = ? AND company_id = ?", soID, companyID).
		First(&so).Error; err != nil {
		return
	}
	if so.Status != models.SalesOrderStatusConfirmed {
		// Don't pre-fill from Draft (operator still editing) or
		// Cancelled (nothing to invoice).
		return
	}

	vm.CustomerID = strconv.FormatUint(uint64(so.CustomerID), 10)
	vm.CurrencyCode = so.CurrencyCode
	vm.SalesOrderID = so.ID
	vm.SalesOrderNumber = so.OrderNumber
	vm.CustomerPONumber = so.CustomerPONumber
	if so.Memo != "" {
		vm.Memo = so.Memo
	}
	s.loadCustomerContactInto(companyID, so.CustomerID, vm)

	for _, l := range so.Lines {
		remaining := l.Quantity.Sub(l.InvoicedQty)
		if !remaining.IsPositive() {
			continue
		}
		psLabel := ""
		if l.ProductService != nil {
			psLabel = l.ProductService.Name
		}
		vm.Lines = append(vm.Lines, pages.InvoiceLineFormRow{
			ProductServiceID:    optUintStr(l.ProductServiceID),
			ProductServiceLabel: psLabel,
			Description:         l.Description,
			Qty:                 remaining.String(),
			UnitPrice:           l.UnitPrice.StringFixed(4),
			TaxCodeID:           optUintStr(l.TaxCodeID),
		})
	}
}

// handleInvoiceEdit renders the editor pre-filled with an existing draft invoice.
func (s *Server) handleInvoiceEdit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.Params("id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}
	invoiceID := uint(id64)

	var inv models.Invoice
	if err := s.DB.Preload("Lines").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}
	if inv.Status != models.InvoiceStatusDraft {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}

	taskGeneratedReadOnly, err := services.HasActiveTaskInvoiceSources(s.DB, companyID, invoiceID)
	if err != nil {
		return redirectErr(c, "/invoices", "could not load invoice editor")
	}

	vm := pages.InvoiceEditorVM{
		HasCompany:            true,
		IsEdit:                true,
		EditingID:             invoiceID,
		ReviewLocked:          c.Query("locked") == "1",
		TaskGeneratedReadOnly: taskGeneratedReadOnly,
		InvoiceNumber:         inv.InvoiceNumber,
		CustomerID:            strconv.FormatUint(uint64(inv.CustomerID), 10),
		InvoiceDate:           inv.InvoiceDate.Format("2006-01-02"),
		TermCode:              inv.TermCode,
		Memo:                  inv.Memo,
		CustomerPONumber:      inv.CustomerPONumber,
		CustomerEmail:         inv.CustomerEmailSnapshot,
		BillTo:                inv.CustomerAddressSnapshot,
		ShipTo:                inv.ShipToSnapshot,
		ShipToLabel:           inv.ShipToLabel,
		WarehouseID:           optUintStr(inv.WarehouseID),
		FormError:             strings.TrimSpace(c.Query("error")),
		Saved:                 c.Query("saved") == "1",
		CurrencyCode:          inv.CurrencyCode,
		ExchangeRate:          displayDocumentExchangeRate(inv.CurrencyCode, inv.ExchangeRate),
	}
	// SO↔Invoice tracking: preserve the sales_order_id link on
	// re-render so form re-save keeps the SO-sourced provenance.
	// Looks up the SO's OrderNumber for the "from SO" hint strip
	// on the template.
	if inv.SalesOrderID != nil && *inv.SalesOrderID != 0 {
		vm.SalesOrderID = *inv.SalesOrderID
		var so models.SalesOrder
		if err := s.DB.Select("id", "order_number").
			Where("id = ? AND company_id = ?", *inv.SalesOrderID, companyID).
			First(&so).Error; err == nil {
			vm.SalesOrderNumber = so.OrderNumber
		}
	}
	if CanFromCtx(c, ActionInvoiceApprove) {
		vm.SubmitPath = fmt.Sprintf("/invoices/%d/issue", invoiceID)
	}
	if taskGeneratedReadOnly {
		vm.ReviewLocked = true
		vm.SaveTaskDraftPath = fmt.Sprintf("/invoices/%d/save-task-draft", invoiceID)
		if CanFromCtx(c, ActionInvoiceDelete) {
			vm.DeletePath = fmt.Sprintf("/invoices/%d/delete", invoiceID)
		}
	}
	if inv.DueDate != nil {
		vm.DueDate = inv.DueDate.Format("2006-01-02")
	}

	// Load the customer's shipping-address catalogue so the ship-to dropdown
	// shows current named options. Snapshot fields above are already loaded,
	// so loadCustomerContactInto's empty-skip guards leave them alone.
	s.loadCustomerContactInto(companyID, inv.CustomerID, &vm)

	// Build line form rows from existing lines.
	for _, l := range inv.Lines {
		psLabel := ""
		if l.ProductService != nil {
			psLabel = l.ProductService.Name
		}
		vm.Lines = append(vm.Lines, pages.InvoiceLineFormRow{
			LineID:              strconv.FormatUint(uint64(l.ID), 10),
			ProductServiceID:    optUintStr(l.ProductServiceID),
			ProductServiceLabel: psLabel,
			Description:         l.Description,
			Qty:                 l.Qty.String(),
			UnitPrice:           l.UnitPrice.StringFixed(4),
			TaxCodeID:           optUintStr(l.TaxCodeID),
			LineNet:             l.LineNet.StringFixed(2),
			LineTax:             l.LineTax.StringFixed(2),
			LineTotal:           l.LineTotal.StringFixed(2),
		})
	}

	if err := s.loadEditorDropdowns(companyID, &vm); err != nil {
		vm.FormError = "Could not load dropdown data."
	}
	vm.InitialLinesJSON = buildInitialLinesJSON(vm.Lines)
	return pages.InvoiceEditor(vm).Render(c.Context(), c)
}

// handleInvoiceSaveDraft creates or updates a draft invoice with line items.
func (s *Server) handleInvoiceSaveDraft(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// ── Parse header ─────────────────────────────────────────────────────────
	invoiceIDRaw := strings.TrimSpace(c.FormValue("invoice_id"))
	invoiceNo := strings.TrimSpace(c.FormValue("invoice_number"))
	customerRaw := strings.TrimSpace(c.FormValue("customer_id"))
	dateRaw := strings.TrimSpace(c.FormValue("invoice_date"))
	termsRaw := strings.TrimSpace(c.FormValue("terms"))
	dueDateRaw := strings.TrimSpace(c.FormValue("due_date"))
	memoRaw := c.FormValue("memo")
	memo := services.SanitizeMemoHTML(memoRaw)
	customerPONumber := strings.TrimSpace(c.FormValue("customer_po_number"))
	customerEmailOverride := strings.TrimSpace(c.FormValue("customer_email"))
	billToOverride := strings.TrimSpace(c.FormValue("bill_to"))
	// Phase 4: editor merges email into the bill-to textarea (line 1 for
	// readability). Strip it back out before snapshot so the address block
	// doesn't end up duplicated when the renderer also prints customer.email.
	if customerEmailOverride != "" {
		lines := strings.SplitN(billToOverride, "\n", 2)
		if len(lines) > 0 && strings.TrimSpace(lines[0]) == customerEmailOverride {
			if len(lines) > 1 {
				billToOverride = strings.TrimSpace(lines[1])
			} else {
				billToOverride = ""
			}
		}
	}
	shipToSnapshot := strings.TrimSpace(c.FormValue("ship_to"))
	shipToLabel := strings.TrimSpace(c.FormValue("ship_to_label"))
	// update_customer_contact=1 means "also write the email override back to
	// the Customer record" (so future invoices default to it). Set by the
	// Invoice editor's "Update customer record?" dialog. Bill-to / ship-to
	// are snapshot-only regardless — address updates happen on the customer
	// page where the structured fields can be edited directly.
	updateCustomerContact := strings.TrimSpace(c.FormValue("update_customer_contact")) == "1"
	currencyCodeRaw := strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
	exchangeRateRaw := strings.TrimSpace(c.FormValue("exchange_rate"))
	warehouseIDRaw := strings.TrimSpace(c.FormValue("warehouse_id"))
	lineCountRaw := strings.TrimSpace(c.FormValue("line_count"))
	taxAdjCountRaw := strings.TrimSpace(c.FormValue("tax_adj_count"))
	// SO↔Invoice tracking: if the form carries a sales_order_id
	// (set by the "Create Invoice" shortcut on SO detail), persist
	// it on the invoice header + server-side match lines to SO
	// lines at insertion time. NULL / "0" / bad value = standalone
	// invoice (no tracking). Validation of same-company lives in
	// the helper called after insertion.
	var invoiceSalesOrderID *uint
	if raw := strings.TrimSpace(c.FormValue("sales_order_id")); raw != "" {
		if v, err := strconv.ParseUint(raw, 10, 64); err == nil && v > 0 {
			u := uint(v)
			invoiceSalesOrderID = &u
		}
	}

	isEdit := invoiceIDRaw != "" && invoiceIDRaw != "0"
	var editingID uint
	if isEdit {
		id64, err := strconv.ParseUint(invoiceIDRaw, 10, 64)
		if err != nil || id64 == 0 {
			return c.Redirect("/invoices", fiber.StatusSeeOther)
		}
		editingID = uint(id64)
	}

	vm := pages.InvoiceEditorVM{
		HasCompany:       true,
		IsEdit:           isEdit,
		EditingID:        editingID,
		InvoiceNumber:    invoiceNo,
		CustomerID:       customerRaw,
		InvoiceDate:      dateRaw,
		TermCode:         termsRaw,
		DueDate:          dueDateRaw,
		Memo:             memo,
		CustomerPONumber: customerPONumber,
		CustomerEmail:    customerEmailOverride,
		BillTo:           billToOverride,
		ShipTo:           shipToSnapshot,
		ShipToLabel:      shipToLabel,
		WarehouseID:      warehouseIDRaw,
		CurrencyCode:     currencyCodeRaw,
		ExchangeRate:     exchangeRateRaw,
	}
	if isEdit && CanFromCtx(c, ActionInvoiceApprove) {
		vm.SubmitPath = fmt.Sprintf("/invoices/%d/issue", editingID)
	}
	_ = s.loadEditorDropdowns(companyID, &vm)
	if isEdit {
		taskGeneratedReadOnly, err := services.HasActiveTaskInvoiceSources(s.DB, companyID, editingID)
		if err != nil {
			vm.FormError = "Could not verify draft edit permissions."
			return pages.InvoiceEditor(vm).Render(c.Context(), c)
		}
		if taskGeneratedReadOnly {
			return redirectErr(c, fmt.Sprintf("/invoices/%d/edit", editingID), services.ErrTaskGeneratedDraftReadOnly.Error())
		}
	}

	// ── Validate header ───────────────────────────────────────────────────────
	if invoiceNo == "" {
		vm.InvoiceNumberError = "Invoice Number is required."
	} else if err := services.ValidateDocumentNumber(invoiceNo); err != nil {
		vm.InvoiceNumberError = err.Error()
	}
	custID, custErr := services.ParseUint(customerRaw)
	if custErr != nil || custID == 0 {
		vm.CustomerError = "Customer is required."
	}
	invoiceDate, dateErr := time.Parse("2006-01-02", dateRaw)
	if dateErr != nil {
		vm.DateError = "Invoice Date is required."
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
	var parsedLines []parsedInvoiceLine
	var lineFormRows []pages.InvoiceLineFormRow

	for i := 0; i < lineCount; i++ {
		key := func(field string) string { return fmt.Sprintf("%s[%d]", field, i) }
		desc := strings.TrimSpace(c.FormValue(key("line_description")))
		qtyRaw := strings.TrimSpace(c.FormValue(key("line_qty")))
		priceRaw := strings.TrimSpace(c.FormValue(key("line_unit_price")))
		psIDRaw := strings.TrimSpace(c.FormValue(key("line_product_service_id")))
		tcIDRaw := strings.TrimSpace(c.FormValue(key("line_tax_code_id")))

		if isInvoicePlaceholderLine(desc, qtyRaw, priceRaw, psIDRaw, tcIDRaw) {
			continue
		}

		row := pages.InvoiceLineFormRow{
			ProductServiceID: psIDRaw,
			Description:      desc,
			Qty:              qtyRaw,
			UnitPrice:        priceRaw,
			TaxCodeID:        tcIDRaw,
		}

		qty, qErr := decimal.NewFromString(qtyRaw)
		if qErr != nil || qty.IsZero() || qty.IsNegative() {
			qty = decimal.NewFromInt(1)
		}
		price, pErr := decimal.NewFromString(priceRaw)
		if pErr != nil {
			price = decimal.Zero
		}
		if desc == "" {
			row.Error = "Description is required."
		}

		pl := parsedInvoiceLine{Description: desc, Qty: qty, UnitPrice: price}
		if id64, err := strconv.ParseUint(psIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.ProductServiceID = &id
		}
		if id64, err := strconv.ParseUint(tcIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			pl.TaxCodeID = &id
		}

		// Stock-item integer rule (S1 / S4 batch follow-up). Surface as a
		// row-level error so the user sees it inline next to the offending
		// qty input rather than as a banner. Only applied when the row's
		// description check hasn't already failed — avoid stacking errors.
		if row.Error == "" {
			if msg := services.StockItemQtyRowError(s.DB, companyID, pl.ProductServiceID, qty); msg != "" {
				row.Error = msg
			}
		}

		lineFormRows = append(lineFormRows, row)
		parsedLines = append(parsedLines, pl)
	}

	vm.Lines = lineFormRows
	vm.InitialLinesJSON = buildInitialLinesJSON(lineFormRows)

	// ── Line-level validation ─────────────────────────────────────────────────
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

	if vm.InvoiceNumberError != "" || vm.CustomerError != "" || vm.DateError != "" ||
		vm.CurrencyError != "" || vm.ExchangeRateError != "" ||
		vm.LinesError != "" || hasLineErr {
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	if err := s.validateInvoiceDraftReferences(companyID, uint(custID), parsedLines); err != nil {
		vm.FormError = err.Error()
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	// ── Compute line amounts ──────────────────────────────────────────────────
	// Load only the tax codes referenced by lines (with components for tax calc).
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

	// ── Read tax adjustments (user-edited totals per tax code) ───────────────
	// tax_adj_count + tax_adj_id[i] + tax_adj_amount[i]
	taxAdjCount, _ := strconv.Atoi(taxAdjCountRaw)
	taxAdjMap := map[uint]decimal.Decimal{} // codeID → user-provided total tax
	for i := 0; i < taxAdjCount; i++ {
		key := func(f string) string { return fmt.Sprintf("%s[%d]", f, i) }
		idRaw2 := strings.TrimSpace(c.FormValue(key("tax_adj_id")))
		amtRaw := strings.TrimSpace(c.FormValue(key("tax_adj_amount")))
		if idRaw2 == "" || amtRaw == "" {
			continue
		}
		codeID64, err := strconv.ParseUint(idRaw2, 10, 64)
		if err != nil || codeID64 == 0 {
			continue
		}
		amt, err := decimal.NewFromString(amtRaw)
		if err != nil || amt.IsNegative() {
			continue
		}
		taxAdjMap[uint(codeID64)] = amt.RoundBank(2)
	}

	type computedLine struct {
		parsedInvoiceLine
		LineNet    decimal.Decimal
		LineTax    decimal.Decimal
		LineTotal  decimal.Decimal
		TaxResults []services.TaxLineResult
	}
	var computed []computedLine
	subtotal := decimal.Zero

	// First pass: compute line nets and unadjusted taxes; track per-code calculated totals.
	type perCodeData struct {
		calcTotal decimal.Decimal
		indices   []int
	}
	codeData := map[uint]*perCodeData{}

	for _, pl := range parsedLines {
		lineNet := pl.Qty.Mul(pl.UnitPrice).RoundBank(2)
		var lineTax decimal.Decimal
		var taxResults []services.TaxLineResult
		if pl.TaxCodeID != nil {
			if tc, ok := taxCodeCache[*pl.TaxCodeID]; ok {
				taxResults = services.CalculateTax(lineNet, *tc)
				lineTax = services.SumTaxResults(taxResults)
			}
		}
		subtotal = subtotal.Add(lineNet)
		idx := len(computed)
		computed = append(computed, computedLine{
			parsedInvoiceLine: pl,
			LineNet:           lineNet,
			LineTax:           lineTax,
			LineTotal:         lineNet.Add(lineTax),
			TaxResults:        taxResults,
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
			// No override — use calculated values.
			taxTotal = taxTotal.Add(cd.calcTotal)
			continue
		}
		// Redistribute override amount across lines using this code.
		if cd.calcTotal.IsZero() {
			// Split evenly when no base to proportion against.
			each := adj.Div(decimal.NewFromInt(int64(len(cd.indices)))).RoundBank(2)
			remainder := adj
			for i, li := range cd.indices {
				t := each
				if i == len(cd.indices)-1 {
					t = remainder // absorb rounding
				}
				computed[li].LineTax = t
				computed[li].LineTotal = computed[li].LineNet.Add(t)
				remainder = remainder.Sub(t)
			}
		} else {
			// Proportional scaling.
			remaining := adj
			for i, li := range cd.indices {
				var t decimal.Decimal
				if i == len(cd.indices)-1 {
					t = remaining
				} else {
					t = computed[li].LineTax.Mul(adj).Div(cd.calcTotal).RoundBank(2)
				}
				computed[li].LineTax = t
				computed[li].LineTotal = computed[li].LineNet.Add(t)
				remaining = remaining.Sub(t)
			}
		}
		taxTotal = taxTotal.Add(adj)
	}
	grandTotal := subtotal.Add(taxTotal)

	// ── Compute due date ──────────────────────────────────────────────────────
	var dueDate *time.Time
	if selectedTerm != nil && selectedTerm.NetDays > 0 {
		dueDate = models.ComputeDueDate(invoiceDate, selectedTerm.NetDays)
	} else if dueDateRaw != "" {
		if d, err := time.Parse("2006-01-02", dueDateRaw); err == nil {
			dueDate = &d
		}
	}

	// ── Duplicate number check (new invoices only) ────────────────────────────
	var dupCount int64
	dupQuery := s.DB.Model(&models.Invoice{}).
		Where("company_id = ? AND LOWER(invoice_number) = LOWER(?) AND status <> ?", companyID, invoiceNo, models.InvoiceStatusVoided)
	if isEdit {
		dupQuery = dupQuery.Where("id <> ?", editingID)
	}
	if err := dupQuery.Count(&dupCount).Error; err != nil {
		vm.FormError = "Could not validate invoice number."
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}
	if dupCount > 0 {
		vm.InvoiceNumberError = "Invoice number already exists for this company (case-insensitive)."
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	// ── DB transaction ────────────────────────────────────────────────────────
	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}

	// ── Load customer for snapshots ──────────────────────────────────────────
	var customer models.Customer
	if err := s.DB.Where("id = ? AND company_id = ?", uint(custID), companyID).
		First(&customer).Error; err != nil {
		vm.FormError = "Customer not found."
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}

	// "Update customer record?" dialog path. When the operator typed a
	// different email and picked "Update customer", rewrite Customer.Email
	// here so the snapshot (computed below) and the live record stay in sync
	// and future invoices default to the new value. Only applied when the
	// override is non-empty and actually differs from the current record.
	if updateCustomerContact && customerEmailOverride != "" && customerEmailOverride != customer.Email {
		if err := s.DB.Model(&customer).Update("email", customerEmailOverride).Error; err == nil {
			customer.Email = customerEmailOverride
		}
	}

	// Parse optional warehouse selection.
	var invWarehouseID *uint
	if wid64, err := strconv.ParseUint(warehouseIDRaw, 10, 64); err == nil && wid64 > 0 {
		wid := uint(wid64)
		invWarehouseID = &wid
	}

	var savedInvoiceID uint
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var inv models.Invoice

		if isEdit {
			if err := tx.Where("id = ? AND company_id = ?", editingID, companyID).First(&inv).Error; err != nil {
				return fmt.Errorf("invoice not found")
			}
			if inv.Status != models.InvoiceStatusDraft {
				return fmt.Errorf("only draft invoices can be edited")
			}
			taskGeneratedReadOnly, err := services.HasActiveTaskInvoiceSources(tx, companyID, editingID)
			if err != nil {
				return fmt.Errorf("check task invoice sources: %w", err)
			}
			if taskGeneratedReadOnly {
				return services.ErrTaskGeneratedDraftReadOnly
			}
			inv.InvoiceNumber = invoiceNo
			inv.CustomerID = uint(custID)
			inv.InvoiceDate = invoiceDate
			if selectedTerm != nil {
				inv.PaymentTermSnapshot = models.BuildSnapshot(*selectedTerm)
			} else {
				inv.PaymentTermSnapshot = models.PaymentTermSnapshot{TermCode: termsRaw}
			}
			inv.DueDate = dueDate
			inv.Memo = memo
			inv.WarehouseID = invWarehouseID
			inv.CurrencyCode = currencySelection.CurrencyCode
			inv.ExchangeRate = currencySelection.ExchangeRate
			inv.Subtotal = subtotal
			inv.TaxTotal = taxTotal
			inv.Amount = grandTotal
			inv.BalanceDue = grandTotal
			inv.CustomerNameSnapshot = customer.Name
			// Snapshot fields honour editor overrides when provided; empty
			// override falls back to the live Customer values so pre-Phase-2
			// drafts re-saved without the new form inputs keep their
			// existing behaviour.
			inv.CustomerEmailSnapshot = customerSnapshotOrDefault(customerEmailOverride, customer.Email)
			inv.CustomerAddressSnapshot = customerSnapshotOrDefault(billToOverride, customer.FormattedAddress())
			inv.ShipToSnapshot = shipToSnapshot
			inv.ShipToLabel = shipToLabel
			inv.CustomerPONumber = customerPONumber
			// SO tracking: on re-save of a draft, re-apply the
			// sales_order_id carried on the form. Operator can edit
			// the hidden field out or leave it — we persist whatever
			// arrives. Null means they deliberately detached the SO
			// link. (Pre-filled drafts carry it forward on re-save.)
			inv.SalesOrderID = invoiceSalesOrderID
			if err := tx.Save(&inv).Error; err != nil {
				return err
			}
			// Delete existing lines and re-insert.
			if err := tx.Where("invoice_id = ?", inv.ID).Delete(&models.InvoiceLine{}).Error; err != nil {
				return err
			}
		} else {
			var snap models.PaymentTermSnapshot
			if selectedTerm != nil {
				snap = models.BuildSnapshot(*selectedTerm)
			} else {
				snap = models.PaymentTermSnapshot{TermCode: termsRaw}
			}
			inv = models.Invoice{
				CompanyID:               companyID,
				InvoiceNumber:           invoiceNo,
				CustomerID:              uint(custID),
				InvoiceDate:             invoiceDate,
				PaymentTermSnapshot:     snap,
				DueDate:                 dueDate,
				Status:                  models.InvoiceStatusDraft,
				Memo:                    memo,
				WarehouseID:             invWarehouseID,
				CurrencyCode:            currencySelection.CurrencyCode,
				ExchangeRate:            currencySelection.ExchangeRate,
				Subtotal:                subtotal,
				TaxTotal:                taxTotal,
				Amount:                  grandTotal,
				BalanceDue:              grandTotal,
				CustomerNameSnapshot:    customer.Name,
				CustomerEmailSnapshot:   customerSnapshotOrDefault(customerEmailOverride, customer.Email),
				CustomerAddressSnapshot: customerSnapshotOrDefault(billToOverride, customer.FormattedAddress()),
				ShipToSnapshot:          shipToSnapshot,
				ShipToLabel:             shipToLabel,
				CustomerPONumber:        customerPONumber,
				SalesOrderID:            invoiceSalesOrderID,
			}
			// Auto-assign company active default template on new invoice creation.
			// Best-effort: if no default template exists the invoice starts unbound (nil),
			// which is valid — the render pipeline falls back gracefully.
			var defaultTmpl models.InvoiceTemplate
			if err := tx.Where("company_id = ? AND is_default = true AND is_active = true", companyID).
				First(&defaultTmpl).Error; err == nil {
				inv.TemplateID = &defaultTmpl.ID
			}
			if err := tx.Create(&inv).Error; err != nil {
				return err
			}
			// Only advance the counter if the user kept the system-suggested number.
			// If the user entered a custom number, leave the counter unchanged so the
			// suggestion remains valid for the next invoice.
			if suggestedNo, sErr := services.SuggestNextInvoiceNumber(tx, companyID); sErr == nil {
				if strings.EqualFold(strings.TrimSpace(invoiceNo), strings.TrimSpace(suggestedNo)) {
					if err := services.BumpInvoiceNextNumberAfterCreate(tx, companyID); err != nil {
						return err
					}
				}
			}
		}

		// Insert lines.
		for i, cl := range computed {
			// UOM snapshot (Phase U2 — 2026-04-25). Resolves the line's
			// UOM defaults from the linked product (Sell side for invoices)
			// and computes Qty × factor for the inventory module to read
			// without re-multiplying.
			uom := services.SnapshotLineUOM(tx, companyID, cl.ProductServiceID, services.LineUOMSell, cl.Qty, "", decimal.Zero)
			line := models.InvoiceLine{
				CompanyID:        companyID,
				InvoiceID:        inv.ID,
				SortOrder:        uint(i + 1),
				Description:      cl.Description,
				Qty:              cl.Qty,
				UnitPrice:        cl.UnitPrice,
				LineUOM:          uom.LineUOM,
				LineUOMFactor:    uom.LineUOMFactor,
				QtyInStockUOM:    uom.QtyInStockUOM,
				LineNet:          cl.LineNet,
				LineTax:          cl.LineTax,
				LineTotal:        cl.LineTotal,
				ProductServiceID: cl.ProductServiceID,
				TaxCodeID:        cl.TaxCodeID,
			}
			if err := tx.Create(&line).Error; err != nil {
				return err
			}
		}

		// SO tracking — if the invoice carries a SalesOrderID,
		// match each invoice line to a SalesOrderLine by
		// (ProductServiceID + FIFO-remaining) and persist
		// `sales_order_line_id` on the matched rows. Idempotent
		// on re-save: MatchInvoiceLinesToSalesOrder clears stale
		// links first. No-op when SalesOrderID is nil.
		if err := services.MatchInvoiceLinesToSalesOrder(tx, companyID, inv.ID); err != nil {
			return err
		}

		action := "invoice.created"
		if isEdit {
			action = "invoice.updated"
		}
		savedInvoiceID = inv.ID
		return services.WriteAuditLogWithContextDetails(tx, action, "invoice", inv.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, &uid, nil,
			map[string]any{
				"invoice_number": inv.InvoiceNumber,
				"customer_id":    inv.CustomerID,
				"total":          inv.Amount.StringFixed(2),
				"line_count":     len(computed),
			},
		)
	})
	if err != nil {
		vm.FormError = invoiceSaveErrorMessage(err)
		return pages.InvoiceEditor(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectInvoice(c.Context(), s.DB, s.SearchProjector, companyID, savedInvoiceID)

	return redirectTo(c, fmt.Sprintf("/invoices/%d/edit?saved=1&locked=1", savedInvoiceID))
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *Server) validateInvoiceDraftReferences(companyID, customerID uint, lines []parsedInvoiceLine) error {
	var customerCount int64
	if err := s.DB.Model(&models.Customer{}).
		Where("id = ? AND company_id = ?", customerID, companyID).
		Count(&customerCount).Error; err != nil {
		return fmt.Errorf("could not validate customer")
	}
	if customerCount == 0 {
		return fmt.Errorf("customer is not valid for this company")
	}

	for i, line := range lines {
		if line.ProductServiceID != nil {
			var productCount int64
			if err := s.DB.Model(&models.ProductService{}).
				Where("id = ? AND company_id = ? AND is_active = true", *line.ProductServiceID, companyID).
				Count(&productCount).Error; err != nil {
				return fmt.Errorf("could not validate line %d product/service", i+1)
			}
			if productCount == 0 {
				return fmt.Errorf("line %d has an invalid product/service for this company", i+1)
			}
		}
		if line.TaxCodeID != nil {
			var taxCodeCount int64
			// Reject purchase-only codes on sales invoices — they are not valid for sales.
			if err := s.DB.Model(&models.TaxCode{}).
				Where("id = ? AND company_id = ? AND is_active = true AND scope != ?",
					*line.TaxCodeID, companyID, models.TaxScopePurchase).
				Count(&taxCodeCount).Error; err != nil {
				return fmt.Errorf("could not validate line %d tax code", i+1)
			}
			if taxCodeCount == 0 {
				return fmt.Errorf("line %d has an invalid tax code for this company", i+1)
			}
		}
	}

	return nil
}

// optUintStr converts *uint to string; empty string if nil.
func optUintStr(p *uint) string {
	if p == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*p), 10)
}

// customerSnapshotOrDefault returns override if non-empty, otherwise fallback.
// Used at Invoice save time: empty override means "no editor override; copy
// the live Customer value into the snapshot" (pre-Phase-2 behaviour).
func customerSnapshotOrDefault(override, fallback string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	return fallback
}

// loadCustomerContactInto pre-fills the Invoice editor's contact block (email,
// bill-to, ship-to + named-shipping-address dropdown) from the live Customer
// record + customer_shipping_addresses table. Best-effort: missing customer or
// query errors leave the corresponding fields untouched.
//
// Skips fields the operator already set on the VM (e.g. when re-rendering after
// a validation error) so user input isn't clobbered. Always rebuilds the
// dropdown options though, since those depend on the customer record only.
func (s *Server) loadCustomerContactInto(companyID, customerID uint, vm *pages.InvoiceEditorVM) {
	if customerID == 0 {
		return
	}
	var c models.Customer
	if err := s.DB.Where("id = ? AND company_id = ?", customerID, companyID).First(&c).Error; err != nil {
		return
	}
	if vm.CustomerEmail == "" {
		vm.CustomerEmail = c.Email
	}
	if vm.BillTo == "" {
		vm.BillTo = c.FormattedAddress()
	}
	var shipAddrs []models.CustomerShippingAddress
	if err := s.DB.Where("customer_id = ?", customerID).
		Order("is_default DESC, id ASC").
		Find(&shipAddrs).Error; err == nil && len(shipAddrs) > 0 {
		opts := make([]pages.ShippingAddressOption, 0, len(shipAddrs))
		for _, a := range shipAddrs {
			opts = append(opts, pages.ShippingAddressOption{
				Label: a.Label, Address: a.FormattedAddress(), IsDefault: a.IsDefault,
			})
		}
		vm.AvailableShippingAddresses = opts
		if vm.ShipTo == "" && len(opts) > 0 {
			vm.ShipTo = opts[0].Address
			vm.ShipToLabel = opts[0].Label
		}
	}
}

// loadEditorDropdowns fills customers, products, taxCodes, paymentTerms + JSON blobs on vm.
// Also loads multi-currency settings when the company has it enabled.
func (s *Server) loadEditorDropdowns(companyID uint, vm *pages.InvoiceEditorVM) error {
	// Active customers only — deactivated customers stay on historical
	// invoices but aren't offered as a choice for new ones.
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").
		Find(&vm.Customers).Error; err != nil {
		return err
	}
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").
		Find(&vm.Products).Error; err != nil {
		return err
	}
	// Only expose sales/both tax codes — purchase-only codes are not valid on sales invoices.
	if err := s.DB.Where("company_id = ? AND is_active = true AND scope != ?",
		companyID, models.TaxScopePurchase).Order("name asc").
		Find(&vm.TaxCodes).Error; err != nil {
		return err
	}
	if err := s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").
		Find(&vm.PaymentTerms).Error; err != nil {
		return err
	}
	vm.Warehouses, _ = services.ListWarehouses(s.DB, companyID)
	vm.ProductsJSON = buildProductsJSON(vm.Products)
	vm.TaxCodesJSON = buildTaxCodesJSON(vm.TaxCodes)
	vm.PaymentTermsJSON = buildPaymentTermsJSON(vm.PaymentTerms)
	vm.CustomersTermsJSON = buildCustomersTermsJSON(vm.Customers)

	// Multi-currency: load company settings and enabled currencies.
	var company models.Company
	if err := s.DB.Select("id", "base_currency_code", "multi_currency_enabled").
		First(&company, companyID).Error; err == nil {
		vm.MultiCurrencyEnabled = company.MultiCurrencyEnabled
		vm.BaseCurrencyCode = company.BaseCurrencyCode
		if company.MultiCurrencyEnabled {
			ccs, _ := services.ListCompanyCurrencies(s.DB, companyID)
			vm.CompanyCurrencies = ccs
			// Build the currency list for the Quick Create Customer drawer.
			// Always include the base currency first, then any foreign currencies.
			codes := make([]string, 0, 1+len(ccs))
			codes = append(codes, company.BaseCurrencyCode)
			for _, cc := range ccs {
				codes = append(codes, cc.CurrencyCode)
			}
			if b, err := json.Marshal(codes); err == nil {
				vm.QuickCreateCurrenciesJSON = string(b)
			}
		}
	}
	return nil
}

type productJSONItem struct {
	ID               uint   `json:"id"`
	Name             string `json:"name"`
	// Description is the item's own description text (may be empty).
	// onProductChange uses this as the auto-fill value for the line description;
	// it falls back to Name when Description is blank.
	Description      string `json:"description"`
	DefaultPrice     string `json:"default_price"`
	DefaultTaxCodeID *uint  `json:"default_tax_code_id"`
}

type taxCodeJSONItem struct {
	ID   uint   `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Rate string `json:"rate"` // stored fraction, e.g. "0.050000" for 5%
}

func buildProductsJSON(products []models.ProductService) string {
	items := make([]productJSONItem, 0, len(products))
	for _, p := range products {
		items = append(items, productJSONItem{
			ID:               p.ID,
			Name:             p.Name,
			Description:      p.Description,
			DefaultPrice:     p.DefaultPrice.StringFixed(2),
			DefaultTaxCodeID: p.DefaultTaxCodeID,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func buildTaxCodesJSON(codes []models.TaxCode) string {
	items := make([]taxCodeJSONItem, 0, len(codes))
	for _, tc := range codes {
		items = append(items, taxCodeJSONItem{
			ID:   tc.ID,
			Code: tc.Code,
			Name: tc.Name,
			Rate: tc.Rate.String(), // fraction: "0.05" for 5%
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

type paymentTermJSONItem struct {
	Code    string `json:"code"`
	NetDays int    `json:"netDays"`
}

func buildPaymentTermsJSON(terms []models.PaymentTerm) string {
	items := make([]paymentTermJSONItem, 0, len(terms))
	for _, pt := range terms {
		items = append(items, paymentTermJSONItem{Code: pt.Code, NetDays: pt.NetDays})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// buildCustomersTermsJSON returns a JSON object mapping customer ID → DefaultPaymentTermCode.
func buildCustomersTermsJSON(customers []models.Customer) string {
	m := make(map[string]string, len(customers))
	for _, c := range customers {
		if c.DefaultPaymentTermCode != "" {
			m[strconv.FormatUint(uint64(c.ID), 10)] = c.DefaultPaymentTermCode
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// buildInitialLinesJSON serialises InvoiceLineFormRow slice for Alpine's data-initial-lines.
func buildInitialLinesJSON(rows []pages.InvoiceLineFormRow) string {
	type alpineLine struct {
		// InvoiceLineID is the DB primary key, non-empty for existing lines.
		// Alpine uses it as line.invoice_line_id; the task-draft save handler
		// submits it as line_invoice_line_id[i] to match locked lines.
		InvoiceLineID       string `json:"invoice_line_id"`
		ProductServiceID    string `json:"product_service_id"`
		ProductServiceLabel string `json:"product_service_label"`
		Description         string `json:"description"`
		Qty                 string `json:"qty"`
		UnitPrice           string `json:"unit_price"`
		TaxCodeID           string `json:"tax_code_id"`
		LineNet          string `json:"line_net"`
		// SavedLineTax carries the server-stored tax amount for this line.
		// Used by init() to detect user tax overrides that differ from the
		// rate-based calculation, so the review page displays the correct value.
		SavedLineTax string `json:"saved_line_tax"`
		Error        string `json:"error"`
	}
	items := make([]alpineLine, 0, len(rows))
	for _, r := range rows {
		net := r.LineNet
		if net == "" {
			net = "0.00"
		}
		items = append(items, alpineLine{
			InvoiceLineID:       r.LineID,
			ProductServiceID:    r.ProductServiceID,
			ProductServiceLabel: r.ProductServiceLabel,
			Description:         r.Description,
			Qty:                 r.Qty,
			UnitPrice:           r.UnitPrice,
			TaxCodeID:           r.TaxCodeID,
			LineNet:             net,
			SavedLineTax:        r.LineTax,
			Error:               r.Error,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// handleInvoiceSaveTaskDraft handles POST /invoices/:id/save-task-draft.
//
// Only task-generated draft invoices may use this endpoint. It accepts a
// limited set of changes that are permitted on task-generated drafts:
//   - Invoice Memo (free-text header field)
//   - Tax code per existing line (identified by line_invoice_line_id[i])
//   - Tax adjustment overrides
//   - New ad-hoc lines added beyond the task-generated set
//
// All locked header fields (invoice number, customer, date, terms, currency,
// exchange rate) and locked line fields (description, qty, unit price) are
// loaded from the database and never taken from the form — so no hidden-input
// spoofing can change them.
func (s *Server) handleInvoiceSaveTaskDraft(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.Params("id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}
	invoiceID := uint(id64)

	// Verify the invoice is a task-generated draft belonging to this company.
	var inv models.Invoice
	if err := s.DB.Preload("Lines").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return c.Redirect("/invoices", fiber.StatusSeeOther)
	}
	if inv.Status != models.InvoiceStatusDraft {
		return redirectErr(c, "/invoices", "only draft invoices can be edited")
	}
	isTaskGenerated, err := services.HasActiveTaskInvoiceSources(s.DB, companyID, invoiceID)
	if err != nil || !isTaskGenerated {
		return redirectErr(c, fmt.Sprintf("/invoices/%d/edit", invoiceID), "invoice is not a task-generated draft")
	}

	// ── Parse allowed fields ──────────────────────────────────────────────────
	memo := strings.TrimSpace(c.FormValue("memo"))
	lineCountRaw := strings.TrimSpace(c.FormValue("line_count"))
	taxAdjCountRaw := strings.TrimSpace(c.FormValue("tax_adj_count"))
	lineCount, _ := strconv.Atoi(lineCountRaw)
	if lineCount < 0 {
		lineCount = 0
	}

	// ── Build a map of existing lines by their DB id for fast lookup ──────────
	existingByID := make(map[uint]*models.InvoiceLine, len(inv.Lines))
	for i := range inv.Lines {
		existingByID[inv.Lines[i].ID] = &inv.Lines[i]
	}

	// ── Parse line_is_locked + line_invoice_line_id + line_tax_code_id ────────
	// For locked lines we update only TaxCodeID on the existing DB row.
	// For new (unlocked) lines we insert a fresh InvoiceLine.
	type newLine struct {
		ProductServiceID *uint
		Description      string
		Qty              decimal.Decimal
		UnitPrice        decimal.Decimal
		TaxCodeID        *uint
	}
	var updatedTaxCodes []struct {
		line      *models.InvoiceLine
		taxCodeID *uint
	}
	var newLines []newLine

	for i := 0; i < lineCount; i++ {
		key := func(field string) string { return fmt.Sprintf("%s[%d]", field, i) }
		isLocked := strings.TrimSpace(c.FormValue(key("line_is_locked"))) == "1"
		taxCodeRaw := strings.TrimSpace(c.FormValue(key("line_tax_code_id")))

		var tcID *uint
		if taxCodeRaw != "" {
			id64, err := strconv.ParseUint(taxCodeRaw, 10, 64)
			if err == nil && id64 > 0 {
				id := uint(id64)
				tcID = &id
			}
		}

		if isLocked {
			lineIDRaw := strings.TrimSpace(c.FormValue(key("line_invoice_line_id")))
			lineID64, err := strconv.ParseUint(lineIDRaw, 10, 64)
			if err != nil || lineID64 == 0 {
				continue
			}
			existing, ok := existingByID[uint(lineID64)]
			if !ok {
				continue // not a line belonging to this invoice
			}
			updatedTaxCodes = append(updatedTaxCodes, struct {
				line      *models.InvoiceLine
				taxCodeID *uint
			}{existing, tcID})
		} else {
			// New user-added line — parse full fields.
			desc := strings.TrimSpace(c.FormValue(key("line_description")))
			qtyRaw := strings.TrimSpace(c.FormValue(key("line_qty")))
			priceRaw := strings.TrimSpace(c.FormValue(key("line_unit_price")))
			psIDRaw := strings.TrimSpace(c.FormValue(key("line_product_service_id")))

			if isInvoicePlaceholderLine(desc, qtyRaw, priceRaw, psIDRaw, taxCodeRaw) {
				continue
			}
			if desc == "" {
				continue
			}

			qty, qErr := decimal.NewFromString(qtyRaw)
			if qErr != nil || qty.IsZero() || qty.IsNegative() {
				qty = decimal.NewFromInt(1)
			}
			price, pErr := decimal.NewFromString(priceRaw)
			if pErr != nil {
				price = decimal.Zero
			}

			var psID *uint
			if psIDRaw != "" {
				id64, err := strconv.ParseUint(psIDRaw, 10, 64)
				if err == nil && id64 > 0 {
					id := uint(id64)
					psID = &id
				}
			}

			newLines = append(newLines, newLine{
				ProductServiceID: psID,
				Description:      desc,
				Qty:              qty,
				UnitPrice:        price,
				TaxCodeID:        tcID,
			})
		}
	}

	// ── Parse tax adjustments ─────────────────────────────────────────────────
	taxAdjCount, _ := strconv.Atoi(taxAdjCountRaw)
	taxAdjMap := map[uint]decimal.Decimal{}
	for i := 0; i < taxAdjCount; i++ {
		key := func(f string) string { return fmt.Sprintf("%s[%d]", f, i) }
		idRaw := strings.TrimSpace(c.FormValue(key("tax_adj_id")))
		amtRaw := strings.TrimSpace(c.FormValue(key("tax_adj_amount")))
		if idRaw == "" || amtRaw == "" {
			continue
		}
		codeID64, err := strconv.ParseUint(idRaw, 10, 64)
		if err != nil || codeID64 == 0 {
			continue
		}
		amt, err := decimal.NewFromString(amtRaw)
		if err != nil || amt.IsNegative() {
			continue
		}
		taxAdjMap[uint(codeID64)] = amt.RoundBank(2)
	}

	// ── Load tax codes needed for recalculation ───────────────────────────────
	taxCodeCache := map[uint]*models.TaxCode{}
	loadTaxCode := func(id *uint) {
		if id == nil {
			return
		}
		if _, ok := taxCodeCache[*id]; ok {
			return
		}
		var tc models.TaxCode
		if err := s.DB.
			Where("id = ? AND company_id = ? AND is_active = true AND scope != ?",
				*id, companyID, models.TaxScopePurchase).
			First(&tc).Error; err == nil {
			taxCodeCache[*id] = &tc
		}
	}

	// Pre-load existing-line tax codes (may have been changed in form).
	for _, u := range updatedTaxCodes {
		loadTaxCode(u.taxCodeID)
	}
	for i := range inv.Lines {
		// Also load current DB tax code in case it wasn't changed.
		loadTaxCode(inv.Lines[i].TaxCodeID)
	}
	for _, nl := range newLines {
		loadTaxCode(nl.TaxCodeID)
	}

	// ── DB transaction ────────────────────────────────────────────────────────
	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}

	err = s.DB.Transaction(func(tx *gorm.DB) error {
		// Apply updated tax codes to locked lines.
		for _, u := range updatedTaxCodes {
			if err := tx.Model(u.line).Update("tax_code_id", u.taxCodeID).Error; err != nil {
				return fmt.Errorf("update line tax code: %w", err)
			}
			u.line.TaxCodeID = u.taxCodeID
		}

		// Insert new user-added lines.
		nextSort := uint(len(inv.Lines) + 1)
		for _, nl := range newLines {
			lineNet := nl.Qty.Mul(nl.UnitPrice).RoundBank(2)
			var lineTax decimal.Decimal
			if nl.TaxCodeID != nil {
				if tc, ok := taxCodeCache[*nl.TaxCodeID]; ok {
					results := services.CalculateTax(lineNet, *tc)
					lineTax = services.SumTaxResults(results)
				}
			}
			uom := services.SnapshotLineUOM(tx, companyID, nl.ProductServiceID, services.LineUOMSell, nl.Qty, "", decimal.Zero)
			newL := models.InvoiceLine{
				CompanyID:        companyID,
				InvoiceID:        inv.ID,
				SortOrder:        nextSort,
				Description:      nl.Description,
				Qty:              nl.Qty,
				UnitPrice:        nl.UnitPrice,
				LineUOM:          uom.LineUOM,
				LineUOMFactor:    uom.LineUOMFactor,
				QtyInStockUOM:    uom.QtyInStockUOM,
				LineNet:          lineNet,
				LineTax:          lineTax,
				LineTotal:        lineNet.Add(lineTax),
				ProductServiceID: nl.ProductServiceID,
				TaxCodeID:        nl.TaxCodeID,
			}
			if err := tx.Create(&newL).Error; err != nil {
				return fmt.Errorf("insert new line: %w", err)
			}
			inv.Lines = append(inv.Lines, newL)
			nextSort++
		}

		// Recalculate invoice totals across all lines (existing + new).
		// Build per-code calculated totals from all lines.
		type perCodeData struct {
			calcTotal decimal.Decimal
			lineIdxs  []int
		}
		allLines := inv.Lines
		codeData := map[uint]*perCodeData{}
		subtotal := decimal.Zero
		type lineComputed struct {
			net decimal.Decimal
			tax decimal.Decimal
		}
		lineCmp := make([]lineComputed, len(allLines))

		for i, l := range allLines {
			net := l.Qty.Mul(l.UnitPrice).RoundBank(2)
			subtotal = subtotal.Add(net)
			var tax decimal.Decimal
			if l.TaxCodeID != nil {
				if tc, ok := taxCodeCache[*l.TaxCodeID]; ok {
					results := services.CalculateTax(net, *tc)
					tax = services.SumTaxResults(results)
					cd := codeData[*l.TaxCodeID]
					if cd == nil {
						cd = &perCodeData{}
						codeData[*l.TaxCodeID] = cd
					}
					cd.calcTotal = cd.calcTotal.Add(tax)
					cd.lineIdxs = append(cd.lineIdxs, i)
				}
			}
			lineCmp[i] = lineComputed{net: net, tax: tax}
		}

		// Apply tax adjustments (user overrides).
		taxTotal := decimal.Zero
		for codeID, cd := range codeData {
			adj, hasAdj := taxAdjMap[codeID]
			if !hasAdj || adj.Equal(cd.calcTotal) {
				taxTotal = taxTotal.Add(cd.calcTotal)
				continue
			}
			// Distribute override proportionally — same logic as main save handler.
			if cd.calcTotal.IsZero() {
				each := adj.Div(decimal.NewFromInt(int64(len(cd.lineIdxs)))).RoundBank(2)
				remaining := adj
				for j, li := range cd.lineIdxs {
					t := each
					if j == len(cd.lineIdxs)-1 {
						t = remaining
					}
					lineCmp[li].tax = t
					remaining = remaining.Sub(t)
				}
			} else {
				remaining := adj
				for j, li := range cd.lineIdxs {
					var t decimal.Decimal
					if j == len(cd.lineIdxs)-1 {
						t = remaining
					} else {
						t = lineCmp[li].tax.Mul(adj).Div(cd.calcTotal).RoundBank(2)
					}
					lineCmp[li].tax = t
					remaining = remaining.Sub(t)
				}
			}
			taxTotal = taxTotal.Add(adj)
		}

		grandTotal := subtotal.Add(taxTotal)

		// Update the invoice header.
		if err := tx.Model(&inv).Updates(map[string]any{
			"memo":        memo,
			"subtotal":    subtotal,
			"tax_total":   taxTotal,
			"amount":      grandTotal,
			"balance_due": grandTotal,
		}).Error; err != nil {
			return fmt.Errorf("update invoice: %w", err)
		}

		return services.WriteAuditLogWithContextDetails(tx, "invoice.task_draft_saved", "invoice", inv.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, &uid, nil,
			map[string]any{
				"invoice_id": inv.ID,
				"new_lines":  len(newLines),
			},
		)
	})
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d/edit", invoiceID), "could not save changes: "+err.Error())
	}
	_ = producers.ProjectInvoice(c.Context(), s.DB, s.SearchProjector, companyID, invoiceID)

	return redirectTo(c, fmt.Sprintf("/invoices/%d/edit?saved=1", invoiceID))
}

func isInvoicePlaceholderLine(desc, qtyRaw, priceRaw, productServiceIDRaw, taxCodeIDRaw string) bool {
	if desc != "" || productServiceIDRaw != "" || taxCodeIDRaw != "" {
		return false
	}

	qtyBlankOrDefault := qtyRaw == ""
	if !qtyBlankOrDefault {
		if qty, err := decimal.NewFromString(qtyRaw); err == nil {
			qtyBlankOrDefault = qty.Equal(decimal.NewFromInt(1))
		}
	}

	priceBlankOrZero := priceRaw == ""
	if !priceBlankOrZero {
		if price, err := decimal.NewFromString(priceRaw); err == nil {
			priceBlankOrZero = price.IsZero()
		}
	}

	return qtyBlankOrDefault && priceBlankOrZero
}
