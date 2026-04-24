// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/searchprojection/producers"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── List ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorReturnList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	filterStatus := strings.TrimSpace(c.Query("status"))
	filterVendor := strings.TrimSpace(c.Query("vendor_id"))
	filterFromStr := strings.TrimSpace(c.Query("from"))
	filterToStr := strings.TrimSpace(c.Query("to"))

	var vendorID uint
	if filterVendor != "" {
		if id, err := strconv.ParseUint(filterVendor, 10, 64); err == nil {
			vendorID = uint(id)
		}
	}

	dateFrom, dateTo := parseListDateRange(filterFromStr, filterToStr)

	vrs, err := services.ListVendorReturns(s.DB, companyID, services.VendorReturnListFilter{
		Status:   filterStatus,
		VendorID: vendorID,
		DateFrom: dateFrom,
		DateTo:   dateTo,
	})
	if err != nil {
		vrs = nil
	}

	return pages.VendorReturns(pages.VendorReturnsVM{
		HasCompany:        true,
		Returns:           vrs,
		FilterStatus:      filterStatus,
		FilterVendor:      filterVendor,
		FilterVendorLabel: lookupVendorName(s.DB, companyID, vendorID),
		FilterDateFrom:    filterFromStr,
		FilterDateTo:      filterToStr,
		Created:           c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleVendorReturnNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.VendorReturnDetailVM{HasCompany: true}
	vm.Return.ReturnDate = time.Now()

	// Pre-fill vendor + source bill when deep-linked from Bill detail.
	if billID := c.QueryInt("bill_id", 0); billID > 0 {
		var bill models.Bill
		if err := s.DB.Where("company_id = ? AND id = ?", companyID, uint(billID)).First(&bill).Error; err == nil {
			vm.Return.VendorID = bill.VendorID
			bID := bill.ID
			vm.Return.BillID = &bID
			vm.Return.CurrencyCode = bill.CurrencyCode
		}
	}

	s.loadVRFormData(companyID, &vm)
	return pages.VendorReturnDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorReturnDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-returns", fiber.StatusSeeOther)
	}

	vr, err := services.GetVendorReturn(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/vendor-returns", fiber.StatusSeeOther)
	}

	vm := pages.VendorReturnDetailVM{
		HasCompany: true,
		Return:     *vr,
		Saved:      c.Query("saved") == "1",
		Submitted:  c.Query("submitted") == "1",
		Approved:   c.Query("approved") == "1",
		Cancelled:  c.Query("cancelled") == "1",
		Processed:  c.Query("processed") == "1",
	}
	s.loadVRFormData(companyID, &vm)
	return pages.VendorReturnDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleVendorReturnSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vrIDStr := strings.TrimSpace(c.FormValue("return_id"))
	var vrID uint
	if vrIDStr != "" {
		if id, err := strconv.ParseUint(vrIDStr, 10, 64); err == nil {
			vrID = uint(id)
		}
	}

	in := parseVRInput(c)

	if vrID == 0 {
		vr, err := services.CreateVendorReturn(s.DB, companyID, in)
		_ = producers.ProjectVendorReturn(c.Context(), s.DB, s.SearchProjector, companyID, vr.ID)
		if err != nil {
			vm := pages.VendorReturnDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadVRFormData(companyID, &vm)
			return pages.VendorReturnDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/vendor-returns/"+strconv.FormatUint(uint64(vr.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err := services.UpdateVendorReturn(s.DB, companyID, vrID, in)
	_ = producers.ProjectVendorReturn(c.Context(), s.DB, s.SearchProjector, companyID, vrID)
	if err != nil {
		vm := pages.VendorReturnDetailVM{HasCompany: true, FormError: err.Error()}
		if vr, e := services.GetVendorReturn(s.DB, companyID, vrID); e == nil {
			vm.Return = *vr
		}
		s.loadVRFormData(companyID, &vm)
		return pages.VendorReturnDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-returns/"+strconv.FormatUint(uint64(vrID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Submit ────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorReturnSubmit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-returns", fiber.StatusSeeOther)
	}
	if submitErr := services.SubmitVendorReturn(s.DB, companyID, id); submitErr != nil {
		vr, _ := services.GetVendorReturn(s.DB, companyID, id)
		vm := pages.VendorReturnDetailVM{HasCompany: true, FormError: submitErr.Error()}
		if vr != nil {
			vm.Return = *vr
		}
		s.loadVRFormData(companyID, &vm)
		return pages.VendorReturnDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectVendorReturn(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/vendor-returns/"+strconv.FormatUint(uint64(id), 10)+"?submitted=1", fiber.StatusSeeOther)
}

// ── Approve ───────────────────────────────────────────────────────────────────

func (s *Server) handleVendorReturnApprove(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-returns", fiber.StatusSeeOther)
	}
	if approveErr := services.ApproveVendorReturn(s.DB, companyID, id); approveErr != nil {
		vr, _ := services.GetVendorReturn(s.DB, companyID, id)
		vm := pages.VendorReturnDetailVM{HasCompany: true, FormError: approveErr.Error()}
		if vr != nil {
			vm.Return = *vr
		}
		s.loadVRFormData(companyID, &vm)
		return pages.VendorReturnDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectVendorReturn(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/vendor-returns/"+strconv.FormatUint(uint64(id), 10)+"?approved=1", fiber.StatusSeeOther)
}

// ── Cancel ────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorReturnCancel(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-returns", fiber.StatusSeeOther)
	}
	if cancelErr := services.CancelVendorReturn(s.DB, companyID, id); cancelErr != nil {
		vr, _ := services.GetVendorReturn(s.DB, companyID, id)
		vm := pages.VendorReturnDetailVM{HasCompany: true, FormError: cancelErr.Error()}
		if vr != nil {
			vm.Return = *vr
		}
		s.loadVRFormData(companyID, &vm)
		return pages.VendorReturnDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectVendorReturn(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/vendor-returns/"+strconv.FormatUint(uint64(id), 10)+"?cancelled=1", fiber.StatusSeeOther)
}

// ── Process ───────────────────────────────────────────────────────────────────

func (s *Server) handleVendorReturnProcess(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-returns", fiber.StatusSeeOther)
	}
	if procErr := services.ProcessVendorReturn(s.DB, companyID, id); procErr != nil {
		vr, _ := services.GetVendorReturn(s.DB, companyID, id)
		vm := pages.VendorReturnDetailVM{HasCompany: true, FormError: procErr.Error()}
		if vr != nil {
			vm.Return = *vr
		}
		s.loadVRFormData(companyID, &vm)
		return pages.VendorReturnDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectVendorReturn(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/vendor-returns/"+strconv.FormatUint(uint64(id), 10)+"?processed=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadVRFormData(companyID uint, vm *pages.VendorReturnDetailVM) {
	vm.Vendors, _ = s.vendorsForCompany(companyID)
	if vm.Return.VendorID > 0 {
		s.DB.Where("company_id = ? AND vendor_id = ? AND balance_due > 0", companyID, vm.Return.VendorID).
			Order("bill_date desc").Find(&vm.Bills)
	}
}

func parseVRInput(c *fiber.Ctx) services.VendorReturnInput {
	vendorIDStr := strings.TrimSpace(c.FormValue("vendor_id"))
	vendorID, _ := strconv.ParseUint(vendorIDStr, 10, 64)

	dateStr := strings.TrimSpace(c.FormValue("return_date"))
	var returnDate time.Time
	if dateStr != "" {
		returnDate, _ = time.Parse("2006-01-02", dateStr)
	}

	var billID *uint
	if billStr := strings.TrimSpace(c.FormValue("bill_id")); billStr != "" {
		if id, err := strconv.ParseUint(billStr, 10, 64); err == nil {
			v := uint(id)
			billID = &v
		}
	}

	amountStr := strings.TrimSpace(c.FormValue("amount"))
	amount, _ := decimal.NewFromString(amountStr)

	return services.VendorReturnInput{
		VendorID:     uint(vendorID),
		BillID:       billID,
		ReturnDate:   returnDate,
		CurrencyCode: strings.TrimSpace(c.FormValue("currency_code")),
		Amount:       amount,
		Reason:       strings.TrimSpace(c.FormValue("reason")),
		Memo:         strings.TrimSpace(c.FormValue("memo")),
	}
}
