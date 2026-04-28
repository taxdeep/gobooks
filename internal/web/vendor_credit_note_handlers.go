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

func (s *Server) handleVendorCreditNoteList(c *fiber.Ctx) error {
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

	vcns, err := services.ListVendorCreditNotes(s.DB, companyID, services.VendorCreditNoteListFilter{
		Status:   filterStatus,
		VendorID: vendorID,
		DateFrom: dateFrom,
		DateTo:   dateTo,
	})
	if err != nil {
		vcns = nil
	}

	return pages.VendorCreditNotes(pages.VendorCreditNotesVM{
		HasCompany:        true,
		CreditNotes:       vcns,
		FilterStatus:      filterStatus,
		FilterVendor:      filterVendor,
		FilterVendorLabel: lookupVendorName(s.DB, companyID, vendorID),
		FilterDateFrom:    filterFromStr,
		FilterDateTo:      filterToStr,
		Created:           c.Query("created") == "1",
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

	// Pre-fill vendor + source bill when deep-linked from Bill detail.
	// Mirrors the AR CreditNote?invoice_id= pre-fill pattern.
	if billID := c.QueryInt("bill_id", 0); billID > 0 {
		var bill models.Bill
		if err := s.DB.Where("company_id = ? AND id = ?", companyID, uint(billID)).First(&bill).Error; err == nil {
			vm.CreditNote.VendorID = bill.VendorID
			bID := bill.ID
			vm.CreditNote.BillID = &bID
			vm.CreditNote.CurrencyCode = bill.CurrencyCode
		}
	}

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

	removeErr := ""
	if c.Query("removeerr") == "1" {
		removeErr = "Failed to remove credit application."
	}
	vm := pages.VendorCreditNoteDetailVM{
		HasCompany:  true,
		CreditNote:  *vcn,
		Saved:       c.Query("saved") == "1",
		Posted:      c.Query("posted") == "1",
		Voided:      c.Query("voided") == "1",
		Applied:     c.Query("applied") == "1",
		Removed:     c.Query("removed") == "1",
		RemoveError: removeErr,
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
		_ = producers.ProjectVendorCreditNote(c.Context(), s.DB, s.SearchProjector, companyID, vcn.ID)
		if err != nil {
			vm := pages.VendorCreditNoteDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadVCNFormData(companyID, &vm)
			return pages.VendorCreditNoteDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/vendor-credit-notes/"+strconv.FormatUint(uint64(vcn.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err := services.UpdateVendorCreditNote(s.DB, companyID, vcnID, in)
	_ = producers.ProjectVendorCreditNote(c.Context(), s.DB, s.SearchProjector, companyID, vcnID)
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
	_ = producers.ProjectVendorCreditNote(c.Context(), s.DB, s.SearchProjector, companyID, id)
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
	_ = producers.ProjectVendorCreditNote(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/vendor-credit-notes/"+strconv.FormatUint(uint64(id), 10)+"?voided=1", fiber.StatusSeeOther)
}

// ── Apply to Bill ──────────────────────────────────────────────────────────────

// handleVCNApplyToBill handles POST /vendor-credit-notes/:id/apply-to-bill
func (s *Server) handleVCNApplyToBill(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-credit-notes", fiber.StatusSeeOther)
	}

	billIDStr := strings.TrimSpace(c.FormValue("bill_id"))
	billID64, _ := strconv.ParseUint(billIDStr, 10, 64)
	billID := uint(billID64)

	amountStr := strings.TrimSpace(c.FormValue("amount_to_apply"))
	amount, _ := decimal.NewFromString(amountStr)

	if applyErr := services.ApplyVendorCreditNoteToBill(s.DB, companyID, id, billID, amount); applyErr != nil {
		vcn, _ := services.GetVendorCreditNote(s.DB, companyID, id)
		vm := pages.VendorCreditNoteDetailVM{HasCompany: true, ApplyError: applyErr.Error()}
		if vcn != nil {
			vm.CreditNote = *vcn
		}
		s.loadVCNFormData(companyID, &vm)
		return pages.VendorCreditNoteDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/vendor-credit-notes/"+strconv.FormatUint(uint64(id), 10)+"?applied=1", fiber.StatusSeeOther)
}

// ── Remove application ────────────────────────────────────────────────────────

// handleVCNRemoveApplication handles POST /vendor-credit-notes/applications/:id/remove
func (s *Server) handleVCNRemoveApplication(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	appID, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/vendor-credit-notes", fiber.StatusSeeOther)
	}

	// Load the application to get the VCN ID for the redirect.
	vcnID := strings.TrimSpace(c.FormValue("vcn_id"))

	if removeErr := services.ReverseAPCreditApplication(s.DB, companyID, appID); removeErr != nil {
		if vcnID != "" {
			return c.Redirect("/vendor-credit-notes/"+vcnID+"?removeerr=1", fiber.StatusSeeOther)
		}
		return c.Redirect("/vendor-credit-notes", fiber.StatusSeeOther)
	}
	if vcnID != "" {
		return c.Redirect("/vendor-credit-notes/"+vcnID+"?removed=1", fiber.StatusSeeOther)
	}
	return c.Redirect("/vendor-credit-notes", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadVCNFormData(companyID uint, vm *pages.VendorCreditNoteDetailVM) {
	vm.Vendors, _ = s.vendorsForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("code asc").Find(&vm.Accounts)
	if vm.CreditNote.VendorID > 0 {
		s.DB.Where("company_id = ? AND vendor_id = ?", companyID, vm.CreditNote.VendorID).
			Order("bill_date desc").Find(&vm.Bills)
		vm.OpenBills, _ = services.ListOpenBillsForVendor(s.DB, companyID, vm.CreditNote.VendorID)
	}

	// IN.6b — load bill lines (source for OriginalBillLineID picker)
	// and active stock items (product picker for return lines). Lines
	// are only useful when the VCN links to a specific Bill; skip the
	// query otherwise.
	if vm.CreditNote.BillID != nil && *vm.CreditNote.BillID > 0 {
		s.DB.Preload("ProductService").
			Where("company_id = ? AND bill_id = ?", companyID, *vm.CreditNote.BillID).
			Order("sort_order asc").Find(&vm.BillLines)
	}
	s.DB.Where("company_id = ? AND is_active = true AND is_stock_item = true",
		companyID).Order("name asc").Find(&vm.Products)
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
		Lines:           parseVCNLineInputs(c),
	}
}

// parseVCNLineInputs reads the parallel line arrays from the form
// and builds VendorCreditNoteLineInput rows. Returns nil (NOT an
// empty slice) when no lines were submitted so the service layer
// continues down the legacy "don't touch lines" path on update.
// Blank rows (no description + no product) are skipped so the
// operator can hold an empty row open in the UI.
func parseVCNLineInputs(c *fiber.Ctx) []services.VendorCreditNoteLineInput {
	args := c.Request().PostArgs()
	descriptions := args.PeekMulti("line_description[]")
	productIDs := args.PeekMulti("line_product_service_id[]")
	origLineIDs := args.PeekMulti("line_original_bill_line_id[]")
	qtys := args.PeekMulti("line_qty[]")
	prices := args.PeekMulti("line_unit_price[]")

	maxLen := len(descriptions)
	if n := len(productIDs); n > maxLen {
		maxLen = n
	}
	if n := len(qtys); n > maxLen {
		maxLen = n
	}
	if maxLen == 0 {
		return nil
	}

	out := make([]services.VendorCreditNoteLineInput, 0, maxLen)
	for i := 0; i < maxLen; i++ {
		desc := ""
		if i < len(descriptions) {
			desc = strings.TrimSpace(string(descriptions[i]))
		}
		pidRaw := ""
		if i < len(productIDs) {
			pidRaw = strings.TrimSpace(string(productIDs[i]))
		}
		// Skip a fully empty row so the operator can keep a blank
		// slot visible without forcing a save-time error.
		if desc == "" && pidRaw == "" {
			continue
		}

		qty := decimal.Zero
		if i < len(qtys) {
			qty, _ = decimal.NewFromString(strings.TrimSpace(string(qtys[i])))
		}
		price := decimal.Zero
		if i < len(prices) {
			price, _ = decimal.NewFromString(strings.TrimSpace(string(prices[i])))
		}
		var productID *uint
		if pidRaw != "" {
			if id, err := strconv.ParseUint(pidRaw, 10, 64); err == nil && id > 0 {
				v := uint(id)
				productID = &v
			}
		}
		var origLineID *uint
		if i < len(origLineIDs) {
			raw := strings.TrimSpace(string(origLineIDs[i]))
			if raw != "" {
				if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
					v := uint(id)
					origLineID = &v
				}
			}
		}
		out = append(out, services.VendorCreditNoteLineInput{
			SortOrder:          uint(i + 1),
			ProductServiceID:   productID,
			OriginalBillLineID: origLineID,
			Description:        desc,
			Qty:                qty,
			UnitPrice:          price,
		})
	}
	return out
}
