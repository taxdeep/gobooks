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

func (s *Server) handleRefundList(c *fiber.Ctx) error {
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

	refunds, err := services.ListARRefunds(s.DB, companyID, services.ARRefundListFilter{
		Status:     filterStatus,
		CustomerID: customerID,
		DateFrom:   dateFrom,
		DateTo:     dateTo,
	})
	if err != nil {
		refunds = nil
	}

	return pages.Refunds(pages.RefundsVM{
		HasCompany:          true,
		Refunds:             refunds,
		FilterStatus:        filterStatus,
		FilterCustomer:      filterCustomer,
		FilterCustomerLabel: lookupCustomerName(s.DB, companyID, customerID),
		FilterDateFrom:      filterFromStr,
		FilterDateTo:        filterToStr,
		Created:             c.Query("created") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleRefundNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.RefundDetailVM{HasCompany: true}
	vm.Refund.RefundDate = time.Now()
	vm.Refund.ExchangeRate = decimal.NewFromInt(1)
	vm.Refund.SourceType = models.ARRefundSourceOther

	// Pre-fill from source document when deep-linked from Invoice or Credit Note
	// detail page. Credit Note takes precedence — it's the specific "Convert to
	// refund" flow that needs amount + linkage carried through.
	if cnID := c.QueryInt("credit_note_id", 0); cnID > 0 {
		if cn, err := services.GetCreditNote(s.DB, companyID, uint(cnID)); err == nil {
			vm.Refund.CustomerID = cn.CustomerID
			vm.Refund.SourceType = models.ARRefundSourceCreditNote
			cnIDUint := cn.ID
			vm.Refund.CreditNoteID = &cnIDUint
			vm.Refund.CurrencyCode = cn.CurrencyCode
			vm.Refund.Amount = cn.BalanceRemaining
		}
	} else if invID := c.QueryInt("invoice_id", 0); invID > 0 {
		var inv models.Invoice
		if err := s.DB.Where("company_id = ? AND id = ?", companyID, uint(invID)).First(&inv).Error; err == nil {
			vm.Refund.CustomerID = inv.CustomerID
			vm.Refund.CurrencyCode = inv.CurrencyCode
			vm.Refund.SourceType = models.ARRefundSourceOverpayment
		}
	}

	s.loadRefundFormData(companyID, &vm)
	return pages.RefundDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleRefundDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/refunds", fiber.StatusSeeOther)
	}

	ref, err := services.GetARRefund(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/refunds", fiber.StatusSeeOther)
	}

	vm := pages.RefundDetailVM{
		HasCompany: true,
		Refund:     *ref,
		Saved:      c.Query("saved") == "1",
		Posted:     c.Query("posted") == "1",
		Voided:     c.Query("voided") == "1",
		Reversed:   c.Query("reversed") == "1",
	}
	s.loadRefundFormData(companyID, &vm)
	return pages.RefundDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleRefundCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	refundIDStr := strings.TrimSpace(c.FormValue("refund_id"))
	var refundID uint
	if refundIDStr != "" {
		if id, err := strconv.ParseUint(refundIDStr, 10, 64); err == nil {
			refundID = uint(id)
		}
	}

	in, err := parseRefundInput(c)
	if err != nil {
		vm := pages.RefundDetailVM{HasCompany: true, FormError: err.Error()}
		if refundID > 0 {
			if ref, e := services.GetARRefund(s.DB, companyID, refundID); e == nil {
				vm.Refund = *ref
			}
		}
		s.loadRefundFormData(companyID, &vm)
		return pages.RefundDetail(vm).Render(c.Context(), c)
	}

	if refundID == 0 {
		ref, err := services.CreateARRefund(s.DB, companyID, in)
		_ = producers.ProjectARRefund(c.Context(), s.DB, s.SearchProjector, companyID, ref.ID)
		if err != nil {
			vm := pages.RefundDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadRefundFormData(companyID, &vm)
			return pages.RefundDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/refunds/"+strconv.FormatUint(uint64(ref.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdateARRefund(s.DB, companyID, refundID, in)
	_ = producers.ProjectARRefund(c.Context(), s.DB, s.SearchProjector, companyID, refundID)
	if err != nil {
		vm := pages.RefundDetailVM{HasCompany: true, FormError: err.Error()}
		if ref, e := services.GetARRefund(s.DB, companyID, refundID); e == nil {
			vm.Refund = *ref
		}
		s.loadRefundFormData(companyID, &vm)
		return pages.RefundDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/refunds/"+strconv.FormatUint(uint64(refundID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Post ──────────────────────────────────────────────────────────────────────

func (s *Server) handleRefundPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/refunds", fiber.StatusSeeOther)
	}

	debitAcctStr := strings.TrimSpace(c.FormValue("debit_account_id"))
	debitAcctID, _ := strconv.ParseUint(debitAcctStr, 10, 64)

	actor, actorID := depositActor(c)
	if postErr := services.PostARRefund(s.DB, companyID, id, uint(debitAcctID), actor, actorID); postErr != nil {
		ref, _ := services.GetARRefund(s.DB, companyID, id)
		vm := pages.RefundDetailVM{HasCompany: true, FormError: postErr.Error()}
		if ref != nil {
			vm.Refund = *ref
		}
		s.loadRefundFormData(companyID, &vm)
		return pages.RefundDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectARRefund(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/refunds/"+strconv.FormatUint(uint64(id), 10)+"?posted=1", fiber.StatusSeeOther)
}

// ── Void ──────────────────────────────────────────────────────────────────────

func (s *Server) handleRefundVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/refunds", fiber.StatusSeeOther)
	}

	if voidErr := services.VoidARRefund(s.DB, companyID, id); voidErr != nil {
		ref, _ := services.GetARRefund(s.DB, companyID, id)
		vm := pages.RefundDetailVM{HasCompany: true, FormError: voidErr.Error()}
		if ref != nil {
			vm.Refund = *ref
		}
		s.loadRefundFormData(companyID, &vm)
		return pages.RefundDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectARRefund(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/refunds/"+strconv.FormatUint(uint64(id), 10)+"?voided=1", fiber.StatusSeeOther)
}

// ── Reverse ───────────────────────────────────────────────────────────────────

func (s *Server) handleRefundReverse(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/refunds", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if revErr := services.ReverseARRefund(s.DB, companyID, id, actor, actorID); revErr != nil {
		ref, _ := services.GetARRefund(s.DB, companyID, id)
		vm := pages.RefundDetailVM{HasCompany: true, FormError: revErr.Error()}
		if ref != nil {
			vm.Refund = *ref
		}
		s.loadRefundFormData(companyID, &vm)
		return pages.RefundDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectARRefund(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/refunds/"+strconv.FormatUint(uint64(id), 10)+"?reversed=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadRefundFormData(companyID uint, vm *pages.RefundDetailVM) {
	vm.Customers, _ = s.customersForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("code asc").Find(&vm.Accounts)
}

func parseRefundInput(c *fiber.Ctx) (services.ARRefundInput, error) {
	customerIDStr := strings.TrimSpace(c.FormValue("customer_id"))
	customerID, _ := strconv.ParseUint(customerIDStr, 10, 64)

	dateStr := strings.TrimSpace(c.FormValue("refund_date"))
	var refundDate time.Time
	if dateStr != "" {
		refundDate, _ = time.Parse("2006-01-02", dateStr)
	}

	bankAcctStr := strings.TrimSpace(c.FormValue("bank_account_id"))
	var bankAccountID *uint
	if bankAcctStr != "" {
		if bid, err := strconv.ParseUint(bankAcctStr, 10, 64); err == nil {
			v := uint(bid)
			bankAccountID = &v
		}
	}

	rateStr := strings.TrimSpace(c.FormValue("exchange_rate"))
	rate, _ := decimal.NewFromString(rateStr)
	if rate.IsZero() {
		rate = decimal.NewFromInt(1)
	}

	amountStr := strings.TrimSpace(c.FormValue("amount"))
	amount, _ := decimal.NewFromString(amountStr)

	sourceType := models.ARRefundSourceType(strings.TrimSpace(c.FormValue("source_type")))
	if sourceType == "" {
		sourceType = models.ARRefundSourceOther
	}

	pmeth := models.PaymentMethod(strings.TrimSpace(c.FormValue("payment_method")))
	if pmeth == "" {
		pmeth = models.PaymentMethodOther
	}

	// Preserve source document linkage across round-trips (carried as a hidden
	// field on the refund form when deep-linked from Credit Note detail).
	var creditNoteID *uint
	if raw := strings.TrimSpace(c.FormValue("credit_note_id")); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
			v := uint(id)
			creditNoteID = &v
		}
	}

	return services.ARRefundInput{
		CustomerID:    uint(customerID),
		BankAccountID: bankAccountID,
		SourceType:    sourceType,
		CreditNoteID:  creditNoteID,
		RefundDate:    refundDate,
		CurrencyCode:  strings.TrimSpace(c.FormValue("currency_code")),
		ExchangeRate:  rate,
		Amount:        amount,
		PaymentMethod: pmeth,
		Reference:     strings.TrimSpace(c.FormValue("reference")),
		Memo:          strings.TrimSpace(c.FormValue("memo")),
	}, nil
}
