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

// ── List ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorRefundList(c *fiber.Ctx) error {
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

	vrfs, err := services.ListVendorRefunds(s.DB, companyID, services.VendorRefundListFilter{
		Status:   filterStatus,
		VendorID: vendorID,
		DateFrom: dateFrom,
		DateTo:   dateTo,
	})
	if err != nil {
		vrfs = nil
	}

	return pages.VendorRefunds(pages.VendorRefundsVM{
		HasCompany:        true,
		Refunds:           vrfs,
		FilterStatus:      filterStatus,
		FilterVendor:      filterVendor,
		FilterVendorLabel: lookupVendorName(s.DB, companyID, vendorID),
		FilterDateFrom:    filterFromStr,
		FilterDateTo:      filterToStr,
		Created:           c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleVendorRefundNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.VendorRefundDetailVM{HasCompany: true}
	vm.Refund.RefundDate = time.Now()
	vm.Refund.ExchangeRate = decimal.NewFromInt(1)
	vm.Refund.SourceType = models.VendorRefundSourceOther
	vm.Refund.PaymentMethod = models.PaymentMethodOther

	// Pre-fill from source document when deep-linked from Bill or Vendor Credit
	// Note detail. VCN takes precedence — it carries amount + linkage for the
	// "Convert to refund" flow.
	if vcnID := c.QueryInt("vendor_credit_note_id", 0); vcnID > 0 {
		if vcn, err := services.GetVendorCreditNote(s.DB, companyID, uint(vcnID)); err == nil {
			vm.Refund.VendorID = vcn.VendorID
			vm.Refund.SourceType = models.VendorRefundSourceCreditNote
			vcnIDUint := vcn.ID
			vm.Refund.VendorCreditNoteID = &vcnIDUint
			vm.Refund.CurrencyCode = vcn.CurrencyCode
			vm.Refund.Amount = vcn.RemainingAmount
		}
	} else if billID := c.QueryInt("bill_id", 0); billID > 0 {
		var bill models.Bill
		if err := s.DB.Where("company_id = ? AND id = ?", companyID, uint(billID)).First(&bill).Error; err == nil {
			vm.Refund.VendorID = bill.VendorID
			vm.Refund.CurrencyCode = bill.CurrencyCode
		}
	}

	s.loadVRFFormData(companyID, &vm)
	return pages.VendorRefundDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorRefundDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-refunds", fiber.StatusSeeOther)
	}

	vrf, err := services.GetVendorRefund(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/vendor-refunds", fiber.StatusSeeOther)
	}

	vm := pages.VendorRefundDetailVM{
		HasCompany: true,
		Refund:     *vrf,
		Saved:      c.Query("saved") == "1",
		Posted:     c.Query("posted") == "1",
		Voided:     c.Query("voided") == "1",
		Reversed:   c.Query("reversed") == "1",
	}
	s.loadVRFFormData(companyID, &vm)
	return pages.VendorRefundDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleVendorRefundSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vrfIDStr := strings.TrimSpace(c.FormValue("refund_id"))
	var vrfID uint
	if vrfIDStr != "" {
		if id, err := strconv.ParseUint(vrfIDStr, 10, 64); err == nil {
			vrfID = uint(id)
		}
	}

	in := parseVRFInput(c)

	if vrfID == 0 {
		vrf, err := services.CreateVendorRefund(s.DB, companyID, in)
		_ = producers.ProjectVendorRefund(c.Context(), s.DB, s.SearchProjector, companyID, vrf.ID)
		if err != nil {
			vm := pages.VendorRefundDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadVRFFormData(companyID, &vm)
			return pages.VendorRefundDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/vendor-refunds/"+strconv.FormatUint(uint64(vrf.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err := services.UpdateVendorRefund(s.DB, companyID, vrfID, in)
	_ = producers.ProjectVendorRefund(c.Context(), s.DB, s.SearchProjector, companyID, vrfID)
	if err != nil {
		vm := pages.VendorRefundDetailVM{HasCompany: true, FormError: err.Error()}
		if vrf, e := services.GetVendorRefund(s.DB, companyID, vrfID); e == nil {
			vm.Refund = *vrf
		}
		s.loadVRFFormData(companyID, &vm)
		return pages.VendorRefundDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-refunds/"+strconv.FormatUint(uint64(vrfID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Post ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorRefundPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-refunds", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if postErr := services.PostVendorRefund(s.DB, companyID, id, actor, actorID); postErr != nil {
		vrf, _ := services.GetVendorRefund(s.DB, companyID, id)
		vm := pages.VendorRefundDetailVM{HasCompany: true, FormError: postErr.Error()}
		if vrf != nil {
			vm.Refund = *vrf
		}
		s.loadVRFFormData(companyID, &vm)
		return pages.VendorRefundDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectVendorRefund(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/vendor-refunds/"+strconv.FormatUint(uint64(id), 10)+"?posted=1", fiber.StatusSeeOther)
}

// ── Void ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorRefundVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-refunds", fiber.StatusSeeOther)
	}

	if voidErr := services.VoidVendorRefund(s.DB, companyID, id); voidErr != nil {
		vrf, _ := services.GetVendorRefund(s.DB, companyID, id)
		vm := pages.VendorRefundDetailVM{HasCompany: true, FormError: voidErr.Error()}
		if vrf != nil {
			vm.Refund = *vrf
		}
		s.loadVRFFormData(companyID, &vm)
		return pages.VendorRefundDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectVendorRefund(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/vendor-refunds/"+strconv.FormatUint(uint64(id), 10)+"?voided=1", fiber.StatusSeeOther)
}

// ── Reverse ───────────────────────────────────────────────────────────────────

func (s *Server) handleVendorRefundReverse(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-refunds", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if revErr := services.ReverseVendorRefund(s.DB, companyID, id, actor, actorID); revErr != nil {
		vrf, _ := services.GetVendorRefund(s.DB, companyID, id)
		vm := pages.VendorRefundDetailVM{HasCompany: true, FormError: revErr.Error()}
		if vrf != nil {
			vm.Refund = *vrf
		}
		s.loadVRFFormData(companyID, &vm)
		return pages.VendorRefundDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-refunds/"+strconv.FormatUint(uint64(id), 10)+"?reversed=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadVRFFormData(companyID uint, vm *pages.VendorRefundDetailVM) {
	vm.Vendors, _ = s.vendorsForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("code asc").Find(&vm.Accounts)
}

func parseVRFInput(c *fiber.Ctx) services.VendorRefundInput {
	vendorIDStr := strings.TrimSpace(c.FormValue("vendor_id"))
	vendorID, _ := strconv.ParseUint(vendorIDStr, 10, 64)

	dateStr := strings.TrimSpace(c.FormValue("refund_date"))
	var refundDate time.Time
	if dateStr != "" {
		refundDate, _ = time.Parse("2006-01-02", dateStr)
	}

	var bankAccountID *uint
	if bankStr := strings.TrimSpace(c.FormValue("bank_account_id")); bankStr != "" {
		if id, err := strconv.ParseUint(bankStr, 10, 64); err == nil {
			v := uint(id)
			bankAccountID = &v
		}
	}

	var creditAccountID *uint
	if credStr := strings.TrimSpace(c.FormValue("credit_account_id")); credStr != "" {
		if id, err := strconv.ParseUint(credStr, 10, 64); err == nil {
			v := uint(id)
			creditAccountID = &v
		}
	}

	rateStr := strings.TrimSpace(c.FormValue("exchange_rate"))
	rate, _ := decimal.NewFromString(rateStr)
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	amountStr := strings.TrimSpace(c.FormValue("amount"))
	amount, _ := decimal.NewFromString(amountStr)

	sourceTypeStr := strings.TrimSpace(c.FormValue("source_type"))
	sourceType := models.VendorRefundSourceType(sourceTypeStr)
	if sourceType == "" {
		sourceType = models.VendorRefundSourceOther
	}

	pmStr := strings.TrimSpace(c.FormValue("payment_method"))
	pm := models.PaymentMethod(pmStr)
	if pm == "" {
		pm = models.PaymentMethodOther
	}

	// Preserve source document linkage across round-trips (hidden field carried
	// when deep-linked from Vendor Credit Note detail).
	var vendorCreditNoteID *uint
	if raw := strings.TrimSpace(c.FormValue("vendor_credit_note_id")); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
			v := uint(id)
			vendorCreditNoteID = &v
		}
	}

	return services.VendorRefundInput{
		VendorID:           uint(vendorID),
		SourceType:         sourceType,
		VendorCreditNoteID: vendorCreditNoteID,
		RefundDate:         refundDate,
		CurrencyCode:       strings.TrimSpace(c.FormValue("currency_code")),
		ExchangeRate:       rate,
		Amount:             amount,
		BankAccountID:      bankAccountID,
		CreditAccountID:    creditAccountID,
		PaymentMethod:      pm,
		Reference:          strings.TrimSpace(c.FormValue("reference")),
		Memo:               strings.TrimSpace(c.FormValue("memo")),
	}
}
