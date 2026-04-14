// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── List ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorCreditNoteList(c *fiber.Ctx) error {
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

	vcns, err := services.ListVendorCreditNotes(s.DB, companyID, filterStatus, vendorID)
	if err != nil {
		vcns = nil
	}

	return pages.VendorCreditNotes(pages.VendorCreditNotesVM{
		HasCompany:   true,
		CreditNotes:  vcns,
		Vendors:      vendors,
		FilterStatus: filterStatus,
		FilterVendor: filterVendor,
		Created:      c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleVendorCreditNoteNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.VendorCreditNoteDetailVM{HasCompany: true}
	vm.CreditNote.CreditNoteDate = time.Now()
	vm.CreditNote.ExchangeRate = decimal.NewFromInt(1)
	s.loadVCNFormData(companyID, &vm)
	return pages.VendorCreditNoteDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorCreditNoteDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-credit-notes", fiber.StatusSeeOther)
	}

	vcn, err := services.GetVendorCreditNote(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/vendor-credit-notes", fiber.StatusSeeOther)
	}

	vm := pages.VendorCreditNoteDetailVM{
		HasCompany: true,
		CreditNote: *vcn,
		Saved:      c.Query("saved") == "1",
		Posted:     c.Query("posted") == "1",
		Voided:     c.Query("voided") == "1",
	}
	s.loadVCNFormData(companyID, &vm)
	return pages.VendorCreditNoteDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleVendorCreditNoteSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vcnIDStr := strings.TrimSpace(c.FormValue("credit_note_id"))
	var vcnID uint
	if vcnIDStr != "" {
		if id, err := strconv.ParseUint(vcnIDStr, 10, 64); err == nil {
			vcnID = uint(id)
		}
	}

	in := parseVCNInput(c)

	if vcnID == 0 {
		vcn, err := services.CreateVendorCreditNote(s.DB, companyID, in)
		if err != nil {
			vm := pages.VendorCreditNoteDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadVCNFormData(companyID, &vm)
			return pages.VendorCreditNoteDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/vendor-credit-notes/"+strconv.FormatUint(uint64(vcn.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err := services.UpdateVendorCreditNote(s.DB, companyID, vcnID, in)
	if err != nil {
		vm := pages.VendorCreditNoteDetailVM{HasCompany: true, FormError: err.Error()}
		if vcn, e := services.GetVendorCreditNote(s.DB, companyID, vcnID); e == nil {
			vm.CreditNote = *vcn
		}
		s.loadVCNFormData(companyID, &vm)
		return pages.VendorCreditNoteDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-credit-notes/"+strconv.FormatUint(uint64(vcnID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Post ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorCreditNotePost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-credit-notes", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if postErr := services.PostVendorCreditNote(s.DB, companyID, id, actor, actorID); postErr != nil {
		vcn, _ := services.GetVendorCreditNote(s.DB, companyID, id)
		vm := pages.VendorCreditNoteDetailVM{HasCompany: true, FormError: postErr.Error()}
		if vcn != nil {
			vm.CreditNote = *vcn
		}
		s.loadVCNFormData(companyID, &vm)
		return pages.VendorCreditNoteDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-credit-notes/"+strconv.FormatUint(uint64(id), 10)+"?posted=1", fiber.StatusSeeOther)
}

// ── Void ──────────────────────────────────────────────────────────────────────

func (s *Server) handleVendorCreditNoteVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-credit-notes", fiber.StatusSeeOther)
	}

	if voidErr := services.VoidVendorCreditNote(s.DB, companyID, id); voidErr != nil {
		vcn, _ := services.GetVendorCreditNote(s.DB, companyID, id)
		vm := pages.VendorCreditNoteDetailVM{HasCompany: true, FormError: voidErr.Error()}
		if vcn != nil {
			vm.CreditNote = *vcn
		}
		s.loadVCNFormData(companyID, &vm)
		return pages.VendorCreditNoteDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-credit-notes/"+strconv.FormatUint(uint64(id), 10)+"?voided=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadVCNFormData(companyID uint, vm *pages.VendorCreditNoteDetailVM) {
	vm.Vendors, _ = s.vendorsForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("code asc").Find(&vm.Accounts)
	if vm.CreditNote.VendorID > 0 {
		s.DB.Where("company_id = ? AND vendor_id = ?", companyID, vm.CreditNote.VendorID).
			Order("bill_date desc").Find(&vm.Bills)
	}
}

func parseVCNInput(c *fiber.Ctx) services.VendorCreditNoteInput {
	vendorIDStr := strings.TrimSpace(c.FormValue("vendor_id"))
	vendorID, _ := strconv.ParseUint(vendorIDStr, 10, 64)

	dateStr := strings.TrimSpace(c.FormValue("credit_note_date"))
	var creditNoteDate time.Time
	if dateStr != "" {
		creditNoteDate, _ = time.Parse("2006-01-02", dateStr)
	}

	var billID *uint
	if billStr := strings.TrimSpace(c.FormValue("bill_id")); billStr != "" {
		if id, err := strconv.ParseUint(billStr, 10, 64); err == nil {
			v := uint(id)
			billID = &v
		}
	}

	var apAccountID *uint
	if apStr := strings.TrimSpace(c.FormValue("ap_account_id")); apStr != "" {
		if id, err := strconv.ParseUint(apStr, 10, 64); err == nil {
			v := uint(id)
			apAccountID = &v
		}
	}

	var offsetAccountID *uint
	if offStr := strings.TrimSpace(c.FormValue("offset_account_id")); offStr != "" {
		if id, err := strconv.ParseUint(offStr, 10, 64); err == nil {
			v := uint(id)
			offsetAccountID = &v
		}
	}

	rateStr := strings.TrimSpace(c.FormValue("exchange_rate"))
	rate, _ := decimal.NewFromString(rateStr)
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	amountStr := strings.TrimSpace(c.FormValue("amount"))
	amount, _ := decimal.NewFromString(amountStr)

	return services.VendorCreditNoteInput{
		VendorID:        uint(vendorID),
		BillID:          billID,
		CreditNoteDate:  creditNoteDate,
		CurrencyCode:    strings.TrimSpace(c.FormValue("currency_code")),
		ExchangeRate:    rate,
		Amount:          amount,
		APAccountID:     apAccountID,
		OffsetAccountID: offsetAccountID,
		Reason:          strings.TrimSpace(c.FormValue("reason")),
		Memo:            strings.TrimSpace(c.FormValue("memo")),
	}
}
