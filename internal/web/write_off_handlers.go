// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// ── List ──────────────────────────────────────────────────────────────────────

func (s *Server) handleWriteOffList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customers, _ := s.customersForCompany(companyID)
	filterStatus := strings.TrimSpace(c.Query("status"))
	filterCustomer := strings.TrimSpace(c.Query("customer_id"))

	var customerID uint
	if filterCustomer != "" {
		if id, err := strconv.ParseUint(filterCustomer, 10, 64); err == nil {
			customerID = uint(id)
		}
	}

	writeOffs, err := services.ListARWriteOffs(s.DB, companyID, filterStatus, customerID)
	if err != nil {
		writeOffs = nil
	}

	return pages.WriteOffs(pages.WriteOffsVM{
		HasCompany:     true,
		WriteOffs:      writeOffs,
		Customers:      customers,
		FilterStatus:   filterStatus,
		FilterCustomer: filterCustomer,
		Created:        c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleWriteOffNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.WriteOffDetailVM{HasCompany: true}
	vm.WriteOff.WriteOffDate = time.Now()
	vm.WriteOff.ExchangeRate = decimal.NewFromInt(1)

	// Pre-fill from source invoice when deep-linked from Invoice detail.
	// The uncollectible amount defaults to the invoice's current outstanding balance.
	if invID := c.QueryInt("invoice_id", 0); invID > 0 {
		var inv models.Invoice
		if err := s.DB.Where("company_id = ? AND id = ?", companyID, uint(invID)).First(&inv).Error; err == nil {
			vm.WriteOff.CustomerID = inv.CustomerID
			iID := inv.ID
			vm.WriteOff.InvoiceID = &iID
			vm.WriteOff.CurrencyCode = inv.CurrencyCode
			vm.WriteOff.Amount = inv.BalanceDue
		}
	}

	s.loadWriteOffFormData(companyID, &vm)
	return pages.WriteOffDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleWriteOffDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/write-offs", fiber.StatusSeeOther)
	}

	wo, err := services.GetARWriteOff(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/write-offs", fiber.StatusSeeOther)
	}

	vm := pages.WriteOffDetailVM{
		HasCompany: true,
		WriteOff:   *wo,
		Saved:      c.Query("saved") == "1",
		Posted:     c.Query("posted") == "1",
		Voided:     c.Query("voided") == "1",
		Reversed:   c.Query("reversed") == "1",
	}
	s.loadWriteOffFormData(companyID, &vm)
	return pages.WriteOffDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleWriteOffSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	writeOffIDStr := strings.TrimSpace(c.FormValue("write_off_id"))
	var writeOffID uint
	if writeOffIDStr != "" {
		if id, err := strconv.ParseUint(writeOffIDStr, 10, 64); err == nil {
			writeOffID = uint(id)
		}
	}

	in, err := parseWriteOffInput(c)
	if err != nil {
		vm := pages.WriteOffDetailVM{HasCompany: true, FormError: err.Error()}
		if writeOffID > 0 {
			if wo, e := services.GetARWriteOff(s.DB, companyID, writeOffID); e == nil {
				vm.WriteOff = *wo
			}
		}
		s.loadWriteOffFormData(companyID, &vm)
		return pages.WriteOffDetail(vm).Render(c.Context(), c)
	}

	if writeOffID == 0 {
		wo, err := services.CreateARWriteOff(s.DB, companyID, in)
		if err != nil {
			vm := pages.WriteOffDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadWriteOffFormData(companyID, &vm)
			return pages.WriteOffDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/write-offs/"+strconv.FormatUint(uint64(wo.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdateARWriteOff(s.DB, companyID, writeOffID, in)
	if err != nil {
		vm := pages.WriteOffDetailVM{HasCompany: true, FormError: err.Error()}
		if wo, e := services.GetARWriteOff(s.DB, companyID, writeOffID); e == nil {
			vm.WriteOff = *wo
		}
		s.loadWriteOffFormData(companyID, &vm)
		return pages.WriteOffDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/write-offs/"+strconv.FormatUint(uint64(writeOffID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Post ──────────────────────────────────────────────────────────────────────

func (s *Server) handleWriteOffPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/write-offs", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if postErr := services.PostARWriteOff(s.DB, companyID, id, actor, actorID); postErr != nil {
		wo, _ := services.GetARWriteOff(s.DB, companyID, id)
		vm := pages.WriteOffDetailVM{HasCompany: true, FormError: postErr.Error()}
		if wo != nil {
			vm.WriteOff = *wo
		}
		s.loadWriteOffFormData(companyID, &vm)
		return pages.WriteOffDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/write-offs/"+strconv.FormatUint(uint64(id), 10)+"?posted=1", fiber.StatusSeeOther)
}

// ── Void ──────────────────────────────────────────────────────────────────────

func (s *Server) handleWriteOffVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/write-offs", fiber.StatusSeeOther)
	}

	if voidErr := services.VoidARWriteOff(s.DB, companyID, id); voidErr != nil {
		wo, _ := services.GetARWriteOff(s.DB, companyID, id)
		vm := pages.WriteOffDetailVM{HasCompany: true, FormError: voidErr.Error()}
		if wo != nil {
			vm.WriteOff = *wo
		}
		s.loadWriteOffFormData(companyID, &vm)
		return pages.WriteOffDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/write-offs/"+strconv.FormatUint(uint64(id), 10)+"?voided=1", fiber.StatusSeeOther)
}

// ── Reverse ───────────────────────────────────────────────────────────────────

func (s *Server) handleWriteOffReverse(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/write-offs", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if revErr := services.ReverseARWriteOff(s.DB, companyID, id, actor, actorID); revErr != nil {
		wo, _ := services.GetARWriteOff(s.DB, companyID, id)
		vm := pages.WriteOffDetailVM{HasCompany: true, FormError: revErr.Error()}
		if wo != nil {
			vm.WriteOff = *wo
		}
		s.loadWriteOffFormData(companyID, &vm)
		return pages.WriteOffDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/write-offs/"+strconv.FormatUint(uint64(id), 10)+"?reversed=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadWriteOffFormData(companyID uint, vm *pages.WriteOffDetailVM) {
	vm.Customers, _ = s.customersForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("code asc").Find(&vm.Accounts)
	if vm.WriteOff.CustomerID > 0 {
		s.DB.Where("company_id = ? AND customer_id = ? AND balance_due > 0 AND status IN ?",
			companyID, vm.WriteOff.CustomerID,
			[]models.InvoiceStatus{models.InvoiceStatusSent, models.InvoiceStatusPartiallyPaid}).
			Order("invoice_date desc").Find(&vm.Invoices)
	}
}

func parseWriteOffInput(c *fiber.Ctx) (services.ARWriteOffInput, error) {
	customerIDStr := strings.TrimSpace(c.FormValue("customer_id"))
	customerID, _ := strconv.ParseUint(customerIDStr, 10, 64)

	dateStr := strings.TrimSpace(c.FormValue("write_off_date"))
	var writeOffDate time.Time
	if dateStr != "" {
		writeOffDate, _ = time.Parse("2006-01-02", dateStr)
	}

	var invoiceID *uint
	if invStr := strings.TrimSpace(c.FormValue("invoice_id")); invStr != "" {
		if id, err := strconv.ParseUint(invStr, 10, 64); err == nil {
			v := uint(id)
			invoiceID = &v
		}
	}

	var arAccountID *uint
	if arStr := strings.TrimSpace(c.FormValue("ar_account_id")); arStr != "" {
		if id, err := strconv.ParseUint(arStr, 10, 64); err == nil {
			v := uint(id)
			arAccountID = &v
		}
	}

	var expenseAccountID *uint
	if expStr := strings.TrimSpace(c.FormValue("expense_account_id")); expStr != "" {
		if id, err := strconv.ParseUint(expStr, 10, 64); err == nil {
			v := uint(id)
			expenseAccountID = &v
		}
	}

	rateStr := strings.TrimSpace(c.FormValue("exchange_rate"))
	rate, _ := decimal.NewFromString(rateStr)
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	amountStr := strings.TrimSpace(c.FormValue("amount"))
	amount, _ := decimal.NewFromString(amountStr)

	return services.ARWriteOffInput{
		CustomerID:       uint(customerID),
		InvoiceID:        invoiceID,
		ARAccountID:      arAccountID,
		ExpenseAccountID: expenseAccountID,
		WriteOffDate:     writeOffDate,
		CurrencyCode:     strings.TrimSpace(c.FormValue("currency_code")),
		ExchangeRate:     rate,
		Amount:           amount,
		Reason:           strings.TrimSpace(c.FormValue("reason")),
		Memo:             strings.TrimSpace(c.FormValue("memo")),
	}, nil
}
