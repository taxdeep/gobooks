// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── List ──────────────────────────────────────────────────────────────────────

func (s *Server) handleReturnList(c *fiber.Ctx) error {
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

	returns, err := services.ListARReturns(s.DB, companyID, filterStatus, customerID)
	if err != nil {
		returns = nil
	}

	return pages.Returns(pages.ReturnsVM{
		HasCompany:     true,
		Returns:        returns,
		Customers:      customers,
		FilterStatus:   filterStatus,
		FilterCustomer: filterCustomer,
		Created:        c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleReturnNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.ReturnDetailVM{HasCompany: true}
	vm.Return.ReturnDate = time.Now()
	s.loadReturnFormData(companyID, &vm)
	return pages.ReturnDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleReturnDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/returns", fiber.StatusSeeOther)
	}

	ret, err := services.GetARReturn(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/returns", fiber.StatusSeeOther)
	}

	vm := pages.ReturnDetailVM{
		HasCompany: true,
		Return:     *ret,
		Saved:      c.Query("saved") == "1",
		Submitted:  c.Query("submitted") == "1",
		Approved:   c.Query("approved") == "1",
		Rejected:   c.Query("rejected") == "1",
		Cancelled:  c.Query("cancelled") == "1",
		Processed:  c.Query("processed") == "1",
	}
	s.loadReturnFormData(companyID, &vm)
	return pages.ReturnDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleReturnCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	returnIDStr := strings.TrimSpace(c.FormValue("return_id"))
	var returnID uint
	if returnIDStr != "" {
		if id, err := strconv.ParseUint(returnIDStr, 10, 64); err == nil {
			returnID = uint(id)
		}
	}

	in, err := parseReturnInput(c)
	if err != nil {
		vm := pages.ReturnDetailVM{HasCompany: true, FormError: err.Error()}
		if returnID > 0 {
			if ret, e := services.GetARReturn(s.DB, companyID, returnID); e == nil {
				vm.Return = *ret
			}
		}
		s.loadReturnFormData(companyID, &vm)
		return pages.ReturnDetail(vm).Render(c.Context(), c)
	}

	if returnID == 0 {
		ret, err := services.CreateARReturn(s.DB, companyID, in)
		if err != nil {
			vm := pages.ReturnDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadReturnFormData(companyID, &vm)
			return pages.ReturnDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/returns/"+strconv.FormatUint(uint64(ret.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdateARReturn(s.DB, companyID, returnID, in)
	if err != nil {
		vm := pages.ReturnDetailVM{HasCompany: true, FormError: err.Error()}
		if ret, e := services.GetARReturn(s.DB, companyID, returnID); e == nil {
			vm.Return = *ret
		}
		s.loadReturnFormData(companyID, &vm)
		return pages.ReturnDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/returns/"+strconv.FormatUint(uint64(returnID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Submit ────────────────────────────────────────────────────────────────────

func (s *Server) handleReturnSubmit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/returns", fiber.StatusSeeOther)
	}

	if err := services.SubmitARReturn(s.DB, companyID, id); err != nil {
		ret, _ := services.GetARReturn(s.DB, companyID, id)
		vm := pages.ReturnDetailVM{HasCompany: true, FormError: err.Error()}
		if ret != nil {
			vm.Return = *ret
		}
		s.loadReturnFormData(companyID, &vm)
		return pages.ReturnDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/returns/"+strconv.FormatUint(uint64(id), 10)+"?submitted=1", fiber.StatusSeeOther)
}

// ── Approve ───────────────────────────────────────────────────────────────────

func (s *Server) handleReturnApprove(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/returns", fiber.StatusSeeOther)
	}

	actor, _ := depositActor(c)
	if err := services.ApproveARReturn(s.DB, companyID, id, actor); err != nil {
		ret, _ := services.GetARReturn(s.DB, companyID, id)
		vm := pages.ReturnDetailVM{HasCompany: true, FormError: err.Error()}
		if ret != nil {
			vm.Return = *ret
		}
		s.loadReturnFormData(companyID, &vm)
		return pages.ReturnDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/returns/"+strconv.FormatUint(uint64(id), 10)+"?approved=1", fiber.StatusSeeOther)
}

// ── Reject ────────────────────────────────────────────────────────────────────

func (s *Server) handleReturnReject(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/returns", fiber.StatusSeeOther)
	}

	if err := services.RejectARReturn(s.DB, companyID, id); err != nil {
		ret, _ := services.GetARReturn(s.DB, companyID, id)
		vm := pages.ReturnDetailVM{HasCompany: true, FormError: err.Error()}
		if ret != nil {
			vm.Return = *ret
		}
		s.loadReturnFormData(companyID, &vm)
		return pages.ReturnDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/returns/"+strconv.FormatUint(uint64(id), 10)+"?rejected=1", fiber.StatusSeeOther)
}

// ── Cancel ────────────────────────────────────────────────────────────────────

func (s *Server) handleReturnCancel(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/returns", fiber.StatusSeeOther)
	}

	if err := services.CancelARReturn(s.DB, companyID, id); err != nil {
		ret, _ := services.GetARReturn(s.DB, companyID, id)
		vm := pages.ReturnDetailVM{HasCompany: true, FormError: err.Error()}
		if ret != nil {
			vm.Return = *ret
		}
		s.loadReturnFormData(companyID, &vm)
		return pages.ReturnDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/returns/"+strconv.FormatUint(uint64(id), 10)+"?cancelled=1", fiber.StatusSeeOther)
}

// ── Mark Processed ────────────────────────────────────────────────────────────

func (s *Server) handleReturnProcess(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/returns", fiber.StatusSeeOther)
	}

	if err := services.MarkReturnProcessed(s.DB, companyID, id); err != nil {
		ret, _ := services.GetARReturn(s.DB, companyID, id)
		vm := pages.ReturnDetailVM{HasCompany: true, FormError: err.Error()}
		if ret != nil {
			vm.Return = *ret
		}
		s.loadReturnFormData(companyID, &vm)
		return pages.ReturnDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/returns/"+strconv.FormatUint(uint64(id), 10)+"?processed=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadReturnFormData(companyID uint, vm *pages.ReturnDetailVM) {
	vm.Customers, _ = s.customersForCompany(companyID)
	if vm.Return.CustomerID != 0 {
		s.DB.Where("company_id = ? AND customer_id = ? AND status IN ?",
			companyID, vm.Return.CustomerID,
			[]models.InvoiceStatus{models.InvoiceStatusSent, models.InvoiceStatusPartiallyPaid, models.InvoiceStatusPaid},
		).Order("invoice_date desc").Find(&vm.Invoices)
	}
}

func parseReturnInput(c *fiber.Ctx) (services.ARReturnInput, error) {
	customerIDStr := strings.TrimSpace(c.FormValue("customer_id"))
	customerID, _ := strconv.ParseUint(customerIDStr, 10, 64)

	invoiceIDStr := strings.TrimSpace(c.FormValue("invoice_id"))
	invoiceID, _ := strconv.ParseUint(invoiceIDStr, 10, 64)

	dateStr := strings.TrimSpace(c.FormValue("return_date"))
	var returnDate time.Time
	if dateStr != "" {
		returnDate, _ = time.Parse("2006-01-02", dateStr)
	}

	amountStr := strings.TrimSpace(c.FormValue("return_amount"))
	amount, _ := decimal.NewFromString(amountStr)

	reason := models.ARReturnReason(strings.TrimSpace(c.FormValue("reason")))
	if reason == "" {
		reason = models.ARReturnReasonOther
	}

	return services.ARReturnInput{
		CustomerID:   uint(customerID),
		InvoiceID:    uint(invoiceID),
		ReturnDate:   returnDate,
		Reason:       reason,
		Description:  strings.TrimSpace(c.FormValue("description")),
		CurrencyCode: strings.TrimSpace(c.FormValue("currency_code")),
		ReturnAmount: amount,
	}, nil
}
