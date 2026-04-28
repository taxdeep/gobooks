// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/searchprojection/producers"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// ── List ─────────────────────────────────────────────────────────────────────

func (s *Server) handleReceipts(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	filterStatus := strings.TrimSpace(c.Query("status"))
	filterCustomer := strings.TrimSpace(c.Query("customer_id"))
	filterFromStr := strings.TrimSpace(c.Query("from"))
	filterToStr := strings.TrimSpace(c.Query("to"))

	var customerID uint
	if filterCustomer != "" {
		if id, err := strconv.ParseUint(filterCustomer, 10, 64); err == nil {
			customerID = uint(id)
		}
	}

	dateFrom, dateTo := parseListDateRange(filterFromStr, filterToStr)

	receipts, err := services.ListCustomerReceipts(s.DB, companyID, services.CustomerReceiptListFilter{
		Status:     filterStatus,
		CustomerID: customerID,
		DateFrom:   dateFrom,
		DateTo:     dateTo,
	})
	if err != nil {
		receipts = nil
	}

	return pages.Receipts(pages.ReceiptsVM{
		HasCompany:          true,
		Receipts:            receipts,
		FilterStatus:        filterStatus,
		FilterCustomer:      filterCustomer,
		FilterCustomerLabel: lookupCustomerName(s.DB, companyID, customerID),
		FilterDateFrom:      filterFromStr,
		FilterDateTo:        filterToStr,
		Created:             c.Query("created") == "1",
		Saved:               c.Query("saved") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleReceiptNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.ReceiptDetailVM{HasCompany: true}
	vm.Receipt.ReceiptDate = time.Now()
	s.loadReceiptFormData(companyID, &vm)
	return pages.ReceiptDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleReceiptDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/receipts", fiber.StatusSeeOther)
	}

	rcpt, err := services.GetCustomerReceipt(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/receipts", fiber.StatusSeeOther)
	}

	apps, _ := services.ListReceiptApplications(s.DB, companyID, id)

	vm := pages.ReceiptDetailVM{
		HasCompany:   true,
		Receipt:      *rcpt,
		Applications: apps,
		Saved:        c.Query("saved") == "1",
		Confirmed:    c.Query("confirmed") == "1",
		Reversed:     c.Query("reversed") == "1",
		Voided:       c.Query("voided") == "1",
		Applied:      c.Query("applied") == "1",
		Unapplied:    c.Query("unapplied") == "1",
	}
	s.loadReceiptFormData(companyID, &vm)
	return pages.ReceiptDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleReceiptSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	receiptIDStr := strings.TrimSpace(c.FormValue("receipt_id"))
	var receiptID uint
	if receiptIDStr != "" {
		if id, err := strconv.ParseUint(receiptIDStr, 10, 64); err == nil {
			receiptID = uint(id)
		}
	}

	in, err := parseReceiptInput(c)
	if err != nil {
		vm := pages.ReceiptDetailVM{HasCompany: true, FormError: err.Error()}
		if receiptID > 0 {
			if rcpt, e := services.GetCustomerReceipt(s.DB, companyID, receiptID); e == nil {
				vm.Receipt = *rcpt
			}
		}
		s.loadReceiptFormData(companyID, &vm)
		return pages.ReceiptDetail(vm).Render(c.Context(), c)
	}

	if receiptID == 0 {
		rcpt, err := services.CreateCustomerReceipt(s.DB, companyID, in)
		if err != nil {
			vm := pages.ReceiptDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadReceiptFormData(companyID, &vm)
			return pages.ReceiptDetail(vm).Render(c.Context(), c)
		}
		_ = producers.ProjectCustomerReceipt(c.Context(), s.DB, s.SearchProjector, companyID, rcpt.ID)
		return c.Redirect("/receipts/"+strconv.FormatUint(uint64(rcpt.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdateCustomerReceipt(s.DB, companyID, receiptID, in)
	if err != nil {
		vm := pages.ReceiptDetailVM{HasCompany: true, FormError: err.Error()}
		if rcpt, e := services.GetCustomerReceipt(s.DB, companyID, receiptID); e == nil {
			vm.Receipt = *rcpt
		}
		s.loadReceiptFormData(companyID, &vm)
		return pages.ReceiptDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectCustomerReceipt(c.Context(), s.DB, s.SearchProjector, companyID, receiptID)
	return c.Redirect("/receipts/"+strconv.FormatUint(uint64(receiptID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Confirm ───────────────────────────────────────────────────────────────────

func (s *Server) handleReceiptConfirm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/receipts", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c) // reuse depositActor helper (email + uuid)
	if confirmErr := services.ConfirmCustomerReceipt(s.DB, companyID, id, actor, actorID); confirmErr != nil {
		rcpt, _ := services.GetCustomerReceipt(s.DB, companyID, id)
		vm := pages.ReceiptDetailVM{HasCompany: true, FormError: confirmErr.Error()}
		if rcpt != nil {
			vm.Receipt = *rcpt
		}
		s.loadReceiptFormData(companyID, &vm)
		return pages.ReceiptDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectCustomerReceipt(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/receipts/"+strconv.FormatUint(uint64(id), 10)+"?confirmed=1", fiber.StatusSeeOther)
}

// ── Apply ─────────────────────────────────────────────────────────────────────

func (s *Server) handleReceiptApply(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/receipts", fiber.StatusSeeOther)
	}

	invoiceIDStr := strings.TrimSpace(c.FormValue("invoice_id"))
	invoiceID, _ := strconv.ParseUint(invoiceIDStr, 10, 64)
	amountStr := strings.TrimSpace(c.FormValue("amount_applied"))
	amount, _ := decimal.NewFromString(amountStr)

	actor, _ := depositActor(c)
	in := services.ApplyReceiptInput{
		ReceiptID:     id,
		InvoiceID:     uint(invoiceID),
		AmountApplied: amount,
		Actor:         actor,
	}

	if applyErr := services.ApplyReceiptToInvoice(s.DB, companyID, in); applyErr != nil {
		rcpt, _ := services.GetCustomerReceipt(s.DB, companyID, id)
		apps, _ := services.ListReceiptApplications(s.DB, companyID, id)
		vm := pages.ReceiptDetailVM{HasCompany: true, FormError: applyErr.Error(), Applications: apps}
		if rcpt != nil {
			vm.Receipt = *rcpt
		}
		s.loadReceiptFormData(companyID, &vm)
		return pages.ReceiptDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/receipts/"+strconv.FormatUint(uint64(id), 10)+"?applied=1", fiber.StatusSeeOther)
}

// ── Unapply ───────────────────────────────────────────────────────────────────

func (s *Server) handleReceiptUnapply(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/receipts", fiber.StatusSeeOther)
	}

	appIDStr := strings.TrimSpace(c.FormValue("application_id"))
	appID, _ := strconv.ParseUint(appIDStr, 10, 64)
	actor, _ := depositActor(c)

	if unapplyErr := services.UnapplyReceipt(s.DB, companyID, uint(appID), actor); unapplyErr != nil {
		rcpt, _ := services.GetCustomerReceipt(s.DB, companyID, id)
		apps, _ := services.ListReceiptApplications(s.DB, companyID, id)
		vm := pages.ReceiptDetailVM{HasCompany: true, FormError: unapplyErr.Error(), Applications: apps}
		if rcpt != nil {
			vm.Receipt = *rcpt
		}
		s.loadReceiptFormData(companyID, &vm)
		return pages.ReceiptDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/receipts/"+strconv.FormatUint(uint64(id), 10)+"?unapplied=1", fiber.StatusSeeOther)
}

// ── Reverse ───────────────────────────────────────────────────────────────────

func (s *Server) handleReceiptReverse(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/receipts", fiber.StatusSeeOther)
	}

	actor, _ := depositActor(c)
	if reverseErr := services.ReverseCustomerReceipt(s.DB, companyID, id, actor); reverseErr != nil {
		rcpt, _ := services.GetCustomerReceipt(s.DB, companyID, id)
		apps, _ := services.ListReceiptApplications(s.DB, companyID, id)
		vm := pages.ReceiptDetailVM{HasCompany: true, FormError: reverseErr.Error(), Applications: apps}
		if rcpt != nil {
			vm.Receipt = *rcpt
		}
		s.loadReceiptFormData(companyID, &vm)
		return pages.ReceiptDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectCustomerReceipt(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/receipts/"+strconv.FormatUint(uint64(id), 10)+"?reversed=1", fiber.StatusSeeOther)
}

// ── Void ──────────────────────────────────────────────────────────────────────

func (s *Server) handleReceiptVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/receipts", fiber.StatusSeeOther)
	}

	if voidErr := services.VoidCustomerReceipt(s.DB, companyID, id); voidErr != nil {
		rcpt, _ := services.GetCustomerReceipt(s.DB, companyID, id)
		vm := pages.ReceiptDetailVM{HasCompany: true, FormError: voidErr.Error()}
		if rcpt != nil {
			vm.Receipt = *rcpt
		}
		s.loadReceiptFormData(companyID, &vm)
		return pages.ReceiptDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectCustomerReceipt(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/receipts/"+strconv.FormatUint(uint64(id), 10)+"?voided=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadReceiptFormData(companyID uint, vm *pages.ReceiptDetailVM) {
	vm.Customers, _ = s.customersForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("code asc").Find(&vm.Accounts)

	// Load open invoices for this customer (for apply form).
	if vm.Receipt.CustomerID != 0 {
		s.DB.Where("company_id = ? AND customer_id = ? AND status IN ? AND balance_due > 0",
			companyID, vm.Receipt.CustomerID,
			[]models.InvoiceStatus{models.InvoiceStatusSent, models.InvoiceStatusPartiallyPaid},
		).Order("invoice_date asc").Find(&vm.Invoices)
	}
}

func parseReceiptInput(c *fiber.Ctx) (services.CustomerReceiptInput, error) {
	customerIDStr := strings.TrimSpace(c.FormValue("customer_id"))
	if customerIDStr == "" {
		return services.CustomerReceiptInput{}, fiber.NewError(fiber.StatusBadRequest, "customer is required")
	}
	cid, err := strconv.ParseUint(customerIDStr, 10, 64)
	if err != nil || cid == 0 {
		return services.CustomerReceiptInput{}, fiber.NewError(fiber.StatusBadRequest, "invalid customer")
	}

	receiptDateStr := strings.TrimSpace(c.FormValue("receipt_date"))
	receiptDate := time.Now()
	if receiptDateStr != "" {
		if d, e := time.Parse("2006-01-02", receiptDateStr); e == nil {
			receiptDate = d
		}
	}

	amountStr := strings.TrimSpace(c.FormValue("amount"))
	amount, _ := decimal.NewFromString(amountStr)

	in := services.CustomerReceiptInput{
		CustomerID:    uint(cid),
		ReceiptDate:   receiptDate,
		CurrencyCode:  strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code"))),
		Amount:        amount,
		PaymentMethod: models.PaymentMethod(strings.TrimSpace(c.FormValue("payment_method"))),
		Reference:     strings.TrimSpace(c.FormValue("reference")),
		Memo:          strings.TrimSpace(c.FormValue("memo")),
	}

	if bankStr := strings.TrimSpace(c.FormValue("bank_account_id")); bankStr != "" {
		if bid, e := strconv.ParseUint(bankStr, 10, 64); e == nil && bid > 0 {
			uid := uint(bid)
			in.BankAccountID = &uid
		}
	}

	return in, nil
}
