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

func (s *Server) handleVendorPrepaymentList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vendors, _ := s.vendorsForCompany(companyID)
	filterStatus := strings.TrimSpace(c.Query("status"))
	filterVendor := strings.TrimSpace(c.Query("vendor_id"))

	var vendorID uint
	if filterVendor != "" {
		if id, err := strconv.ParseUint(filterVendor, 10, 64); err == nil {
			vendorID = uint(id)
		}
	}

	pps, err := services.ListVendorPrepayments(s.DB, companyID, filterStatus, vendorID)
	if err != nil {
		pps = nil
	}

	return pages.VendorPrepayments(pages.VendorPrepaymentsVM{
		HasCompany:   true,
		Prepayments:  pps,
		Vendors:      vendors,
		FilterStatus: filterStatus,
		FilterVendor: filterVendor,
		Created:      c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleVendorPrepaymentNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.VendorPrepaymentDetailVM{HasCompany: true}
	vm.Prepayment.PrepaymentDate = time.Now()
	vm.Prepayment.ExchangeRate = decimal.NewFromInt(1)
	vm.Prepayment.PaymentMethod = models.PaymentMethodOther
	s.loadVPFormData(companyID, &vm)
	return pages.VendorPrepaymentDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorPrepaymentDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-prepayments", fiber.StatusSeeOther)
	}

	pp, err := services.GetVendorPrepayment(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/vendor-prepayments", fiber.StatusSeeOther)
	}

	vm := pages.VendorPrepaymentDetailVM{
		HasCompany: true,
		Prepayment: *pp,
		Saved:      c.Query("saved") == "1",
		Posted:     c.Query("posted") == "1",
		Voided:     c.Query("voided") == "1",
	}
	s.loadVPFormData(companyID, &vm)
	return pages.VendorPrepaymentDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleVendorPrepaymentSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	ppIDStr := strings.TrimSpace(c.FormValue("prepayment_id"))
	var ppID uint
	if ppIDStr != "" {
		if id, err := strconv.ParseUint(ppIDStr, 10, 64); err == nil {
			ppID = uint(id)
		}
	}

	in, err := parseVPInput(c)
	if err != nil {
		vm := pages.VendorPrepaymentDetailVM{HasCompany: true, FormError: err.Error()}
		if ppID > 0 {
			if pp, e := services.GetVendorPrepayment(s.DB, companyID, ppID); e == nil {
				vm.Prepayment = *pp
			}
		}
		s.loadVPFormData(companyID, &vm)
		return pages.VendorPrepaymentDetail(vm).Render(c.Context(), c)
	}

	if ppID == 0 {
		pp, err := services.CreateVendorPrepayment(s.DB, companyID, in)
		if err != nil {
			vm := pages.VendorPrepaymentDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadVPFormData(companyID, &vm)
			return pages.VendorPrepaymentDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/vendor-prepayments/"+strconv.FormatUint(uint64(pp.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdateVendorPrepayment(s.DB, companyID, ppID, in)
	if err != nil {
		vm := pages.VendorPrepaymentDetailVM{HasCompany: true, FormError: err.Error()}
		if pp, e := services.GetVendorPrepayment(s.DB, companyID, ppID); e == nil {
			vm.Prepayment = *pp
		}
		s.loadVPFormData(companyID, &vm)
		return pages.VendorPrepaymentDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-prepayments/"+strconv.FormatUint(uint64(ppID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Post ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorPrepaymentPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-prepayments", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if postErr := services.PostVendorPrepayment(s.DB, companyID, id, actor, actorID); postErr != nil {
		pp, _ := services.GetVendorPrepayment(s.DB, companyID, id)
		vm := pages.VendorPrepaymentDetailVM{HasCompany: true, FormError: postErr.Error()}
		if pp != nil {
			vm.Prepayment = *pp
		}
		s.loadVPFormData(companyID, &vm)
		return pages.VendorPrepaymentDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-prepayments/"+strconv.FormatUint(uint64(id), 10)+"?posted=1", fiber.StatusSeeOther)
}

// ── Void ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorPrepaymentVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-prepayments", fiber.StatusSeeOther)
	}

	if voidErr := services.VoidVendorPrepayment(s.DB, companyID, id); voidErr != nil {
		pp, _ := services.GetVendorPrepayment(s.DB, companyID, id)
		vm := pages.VendorPrepaymentDetailVM{HasCompany: true, FormError: voidErr.Error()}
		if pp != nil {
			vm.Prepayment = *pp
		}
		s.loadVPFormData(companyID, &vm)
		return pages.VendorPrepaymentDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-prepayments/"+strconv.FormatUint(uint64(id), 10)+"?voided=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadVPFormData(companyID uint, vm *pages.VendorPrepaymentDetailVM) {
	vm.Vendors, _ = s.vendorsForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("code asc").Find(&vm.Accounts)
}

func parseVPInput(c *fiber.Ctx) (services.VendorPrepaymentInput, error) {
	vendorIDStr := strings.TrimSpace(c.FormValue("vendor_id"))
	vendorID, _ := strconv.ParseUint(vendorIDStr, 10, 64)

	dateStr := strings.TrimSpace(c.FormValue("prepayment_date"))
	var prepaymentDate time.Time
	if dateStr != "" {
		prepaymentDate, _ = time.Parse("2006-01-02", dateStr)
	}

	var bankAccountID *uint
	if bankStr := strings.TrimSpace(c.FormValue("bank_account_id")); bankStr != "" {
		if id, err := strconv.ParseUint(bankStr, 10, 64); err == nil {
			v := uint(id)
			bankAccountID = &v
		}
	}

	var prepaymentAccountID *uint
	if ppAccStr := strings.TrimSpace(c.FormValue("prepayment_account_id")); ppAccStr != "" {
		if id, err := strconv.ParseUint(ppAccStr, 10, 64); err == nil {
			v := uint(id)
			prepaymentAccountID = &v
		}
	}

	rateStr := strings.TrimSpace(c.FormValue("exchange_rate"))
	rate, _ := decimal.NewFromString(rateStr)
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	amountStr := strings.TrimSpace(c.FormValue("amount"))
	amount, _ := decimal.NewFromString(amountStr)

	pmStr := strings.TrimSpace(c.FormValue("payment_method"))
	pm := models.PaymentMethod(pmStr)
	if pm == "" {
		pm = models.PaymentMethodOther
	}

	return services.VendorPrepaymentInput{
		VendorID:            uint(vendorID),
		PrepaymentDate:      prepaymentDate,
		CurrencyCode:        strings.TrimSpace(c.FormValue("currency_code")),
		ExchangeRate:        rate,
		Amount:              amount,
		BankAccountID:       bankAccountID,
		PrepaymentAccountID: prepaymentAccountID,
		PaymentMethod:       pm,
		Reference:           strings.TrimSpace(c.FormValue("reference")),
		Memo:                strings.TrimSpace(c.FormValue("memo")),
	}, nil
}
