// 遵循project_guide.md
package web

import (
	"errors"
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

// parseIDParam parses the :id route parameter as a uint.
func parseIDParam(c *fiber.Ctx) (uint, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid id")
	}
	return uint(id), nil
}

// ── List ─────────────────────────────────────────────────────────────────────

func (s *Server) handleQuotes(c *fiber.Ctx) error {
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

	quotes, err := services.ListQuotes(s.DB, companyID, services.QuoteListFilter{
		Status:     filterStatus,
		CustomerID: customerID,
		DateFrom:   dateFrom,
		DateTo:     dateTo,
	})
	if err != nil {
		quotes = nil
	}

	return pages.Quotes(pages.QuotesVM{
		HasCompany:          true,
		Quotes:              quotes,
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

func (s *Server) handleQuoteNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.QuoteDetailVM{HasCompany: true}
	vm.Quote.QuoteDate = time.Now()
	if customerIDRaw := strings.TrimSpace(c.Query("customer_id")); customerIDRaw != "" {
		if customerID64, err := strconv.ParseUint(customerIDRaw, 10, 64); err == nil && customerID64 > 0 {
			var customer models.Customer
			if err := s.DB.Select("id", "currency_code").
				Where("id = ? AND company_id = ? AND is_active = true", uint(customerID64), companyID).
				First(&customer).Error; err == nil {
				vm.Quote.CustomerID = customer.ID
				vm.Quote.CurrencyCode = customer.CurrencyCode
			}
		}
	}
	s.loadQuoteFormData(companyID, &vm)
	return pages.QuoteDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleQuoteDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/quotes", fiber.StatusSeeOther)
	}

	q, err := services.GetQuote(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/quotes", fiber.StatusSeeOther)
	}

	vm := pages.QuoteDetailVM{
		HasCompany: true,
		Quote:      *q,
		Saved:      c.Query("saved") == "1",
		Sent:       c.Query("sent") == "1",
		Accepted:   c.Query("accepted") == "1",
		Rejected:   c.Query("rejected") == "1",
		Converted:  c.Query("converted") == "1",
		Cancelled:  c.Query("cancelled") == "1",
	}
	s.loadQuoteFormData(companyID, &vm)
	return pages.QuoteDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleQuoteSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	quoteIDStr := strings.TrimSpace(c.FormValue("quote_id"))
	var quoteID uint
	if quoteIDStr != "" {
		if id, err := strconv.ParseUint(quoteIDStr, 10, 64); err == nil {
			quoteID = uint(id)
		}
	}

	in, err := parseQuoteInput(c)
	if err != nil {
		vm := pages.QuoteDetailVM{HasCompany: true, FormError: err.Error()}
		if quoteID > 0 {
			if q, e := services.GetQuote(s.DB, companyID, quoteID); e == nil {
				vm.Quote = *q
			}
		}
		s.loadQuoteFormData(companyID, &vm)
		return pages.QuoteDetail(vm).Render(c.Context(), c)
	}

	if quoteID == 0 {
		q, err := services.CreateQuote(s.DB, companyID, in)
		if err != nil {
			vm := pages.QuoteDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadQuoteFormData(companyID, &vm)
			return pages.QuoteDetail(vm).Render(c.Context(), c)
		}
		_ = producers.ProjectQuote(c.Context(), s.DB, s.SearchProjector, companyID, q.ID)
		return c.Redirect("/quotes/"+strconv.FormatUint(uint64(q.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdateQuote(s.DB, companyID, quoteID, in)
	if err != nil {
		vm := pages.QuoteDetailVM{HasCompany: true, FormError: err.Error()}
		if q, e := services.GetQuote(s.DB, companyID, quoteID); e == nil {
			vm.Quote = *q
		}
		s.loadQuoteFormData(companyID, &vm)
		return pages.QuoteDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectQuote(c.Context(), s.DB, s.SearchProjector, companyID, quoteID)
	return c.Redirect("/quotes/"+strconv.FormatUint(uint64(quoteID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Status transitions ────────────────────────────────────────────────────────

func (s *Server) handleQuoteSend(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/quotes", fiber.StatusSeeOther)
	}
	_ = services.SendQuote(s.DB, companyID, id, "")
	_ = producers.ProjectQuote(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/quotes/"+strconv.FormatUint(uint64(id), 10)+"?sent=1", fiber.StatusSeeOther)
}

func (s *Server) handleQuoteAccept(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/quotes", fiber.StatusSeeOther)
	}
	_ = services.AcceptQuote(s.DB, companyID, id)
	_ = producers.ProjectQuote(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/quotes/"+strconv.FormatUint(uint64(id), 10)+"?accepted=1", fiber.StatusSeeOther)
}

func (s *Server) handleQuoteReject(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/quotes", fiber.StatusSeeOther)
	}
	_ = services.RejectQuote(s.DB, companyID, id)
	_ = producers.ProjectQuote(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/quotes/"+strconv.FormatUint(uint64(id), 10)+"?rejected=1", fiber.StatusSeeOther)
}

func (s *Server) handleQuoteCancel(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/quotes", fiber.StatusSeeOther)
	}
	_ = services.CancelQuote(s.DB, companyID, id)
	_ = producers.ProjectQuote(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/quotes/"+strconv.FormatUint(uint64(id), 10)+"?cancelled=1", fiber.StatusSeeOther)
}

func (s *Server) handleQuoteConvert(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/quotes", fiber.StatusSeeOther)
	}
	so, err := services.ConvertQuoteToSalesOrder(s.DB, companyID, id, "", nil)
	if err != nil {
		return c.Redirect("/quotes/"+strconv.FormatUint(uint64(id), 10)+"?converted=0", fiber.StatusSeeOther)
	}
	// Convert flips quote.status to "converted" AND creates the new SO
	// — project both so search reflects the linkage immediately.
	_ = producers.ProjectQuote(c.Context(), s.DB, s.SearchProjector, companyID, id)
	_ = producers.ProjectSalesOrder(c.Context(), s.DB, s.SearchProjector, companyID, so.ID)
	return c.Redirect("/sales-orders/"+strconv.FormatUint(uint64(so.ID), 10)+"?created=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) loadQuoteFormData(companyID uint, vm *pages.QuoteDetailVM) {
	var company models.Company
	if err := s.DB.Select("id", "base_currency_code").First(&company, companyID).Error; err == nil {
		vm.BaseCurrencyCode = strings.ToUpper(strings.TrimSpace(company.BaseCurrencyCode))
	}
	if vm.BaseCurrencyCode == "" {
		vm.BaseCurrencyCode = "CAD"
	}
	vm.Customers, _ = s.customersForCompany(companyID)
	if strings.TrimSpace(vm.Quote.CurrencyCode) == "" {
		for _, customer := range vm.Customers {
			if customer.ID == vm.Quote.CustomerID && strings.TrimSpace(customer.CurrencyCode) != "" {
				vm.Quote.CurrencyCode = strings.ToUpper(strings.TrimSpace(customer.CurrencyCode))
				break
			}
		}
	}
	if strings.TrimSpace(vm.Quote.CurrencyCode) == "" {
		vm.Quote.CurrencyCode = vm.BaseCurrencyCode
	}
	s.DB.Where("company_id = ? AND is_active = true AND scope != ?",
		companyID, models.TaxScopePurchase).Order("name asc").Find(&vm.TaxCodes)
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").Find(&vm.ProductServices)
}

func parseQuoteInput(c *fiber.Ctx) (services.QuoteInput, error) {
	customerIDStr := strings.TrimSpace(c.FormValue("customer_id"))
	if customerIDStr == "" {
		return services.QuoteInput{}, fiber.NewError(fiber.StatusBadRequest, "customer is required")
	}
	cid, err := strconv.ParseUint(customerIDStr, 10, 64)
	if err != nil || cid == 0 {
		return services.QuoteInput{}, fiber.NewError(fiber.StatusBadRequest, "invalid customer")
	}

	quoteDateStr := strings.TrimSpace(c.FormValue("quote_date"))
	quoteDate := time.Now()
	if quoteDateStr != "" {
		if d, ok := parseDocumentDateValue(quoteDateStr); ok {
			quoteDate = d
		}
	}

	var expiryDate *time.Time
	if ed := strings.TrimSpace(c.FormValue("expiry_date")); ed != "" {
		if d, ok := parseDocumentDateValue(ed); ok {
			expiryDate = &d
		}
	}

	exchangeRate := decimal.NewFromInt(1)
	if rateRaw := strings.TrimSpace(c.FormValue("exchange_rate")); rateRaw != "" {
		rate, err := decimal.NewFromString(rateRaw)
		if err != nil || !rate.GreaterThan(decimal.Zero) {
			return services.QuoteInput{}, fiber.NewError(fiber.StatusBadRequest, "exchange rate must be greater than 0")
		}
		exchangeRate = rate.RoundBank(8)
	}

	lines := parseDocumentLines(c)
	if len(lines) == 0 {
		return services.QuoteInput{}, fiber.NewError(fiber.StatusBadRequest, "at least one line is required")
	}

	in := services.QuoteInput{
		CustomerID:   uint(cid),
		CurrencyCode: strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code"))),
		ExchangeRate: exchangeRate,
		QuoteDate:    quoteDate,
		ExpiryDate:   expiryDate,
		Notes:        strings.TrimSpace(c.FormValue("notes")),
		Memo:         strings.TrimSpace(c.FormValue("memo")),
	}

	for _, l := range lines {
		in.Lines = append(in.Lines, services.QuoteLineInput{
			ProductServiceID: l.ProductServiceID,
			TaxCodeID:        l.TaxCodeID,
			Description:      l.Description,
			Quantity:         l.Quantity,
			UnitPrice:        l.UnitPrice,
		})
	}
	return in, nil
}

func parseDocumentDateValue(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", "2006/01/02", "2006.01.02", "20060102", "02/01/2006", "02-01-2006", "02.01.2006"} {
		if d, err := time.Parse(layout, value); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}

// documentLine is an internal helper for parsing form line items.
// Shared across Quote and SalesOrder handlers; extending it
// propagates to both call sites at once. ProductServiceID is
// optional — the downstream service layers (QuoteLineInput,
// SalesOrderLineInput) already carry the field; this helper simply
// surfaces the form value they both need.
type documentLine struct {
	ProductServiceID *uint
	Description      string
	Quantity         decimal.Decimal
	UnitPrice        decimal.Decimal
	TaxCodeID        *uint
}

// parseDocumentLines scans form values for
// line_product_service_id_N / line_description_N / line_qty_N /
// line_price_N / line_tax_N. A line is considered present when any
// of product / description / qty / price is non-empty; trailing
// empty rows are silently skipped so the UI can submit slots for
// removed rows without polluting the document.
func parseDocumentLines(c *fiber.Ctx) []documentLine {
	var lines []documentLine
	for i := 0; i < 200; i++ {
		psIDStr := strings.TrimSpace(c.FormValue("line_product_service_id_" + strconv.Itoa(i)))
		desc := strings.TrimSpace(c.FormValue("line_description_" + strconv.Itoa(i)))
		qtyStr := strings.TrimSpace(c.FormValue("line_qty_" + strconv.Itoa(i)))
		priceStr := strings.TrimSpace(c.FormValue("line_price_" + strconv.Itoa(i)))
		taxStr := strings.TrimSpace(c.FormValue("line_tax_" + strconv.Itoa(i)))
		if psIDStr == "" && desc == "" && qtyStr == "" && priceStr == "" && taxStr == "" {
			continue
		}

		qty, err := decimal.NewFromString(qtyStr)
		if err != nil || qty.IsZero() {
			qty = decimal.NewFromInt(1)
		}
		price, err := decimal.NewFromString(priceStr)
		if err != nil {
			price = decimal.Zero
		}
		if psIDStr == "" && desc == "" && taxStr == "" && price.IsZero() {
			continue
		}

		var taxCodeID *uint
		if taxStr != "" {
			if id, err := strconv.ParseUint(taxStr, 10, 64); err == nil && id > 0 {
				uid := uint(id)
				taxCodeID = &uid
			}
		}

		var productServiceID *uint
		if psIDStr != "" {
			if id, err := strconv.ParseUint(psIDStr, 10, 64); err == nil && id > 0 {
				uid := uint(id)
				productServiceID = &uid
			}
		}

		lines = append(lines, documentLine{
			ProductServiceID: productServiceID,
			Description:      desc,
			Quantity:         qty,
			UnitPrice:        price,
			TaxCodeID:        taxCodeID,
		})
	}
	return lines
}
