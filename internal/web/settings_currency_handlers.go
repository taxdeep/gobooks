// 遵循project_guide.md
package web

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// ── Company Currency Settings ─────────────────────────────────────────────────

func (s *Server) handleCompanyCurrencyGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm, err := s.buildCurrencyVM(companyID)
	if err != nil {
		return pages.CompanyCurrency(pages.CompanyCurrencyVM{
			HasCompany: true,
			Breadcrumb: breadcrumbSettingsCompanyCurrency(),
			FormError:  "Could not load currency settings.",
		}).Render(c.Context(), c)
	}
	vm.Enabled = c.Query("enabled") == "1"
	vm.Added = c.Query("added") == "1"
	return pages.CompanyCurrency(vm).Render(c.Context(), c)
}

// handleCompanyCurrencyEnableMulti enables multi-currency for the company.
func (s *Server) handleCompanyCurrencyEnableMulti(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	if err := services.EnableMultiCurrency(s.DB, companyID); err != nil {
		vm, _ := s.buildCurrencyVM(companyID)
		vm.FormError = err.Error()
		return pages.CompanyCurrency(vm).Render(c.Context(), c)
	}
	return c.Redirect("/settings/company/currency?enabled=1", fiber.StatusSeeOther)
}

// handleCompanyCurrencyAdd adds a foreign currency to the company.
func (s *Server) handleCompanyCurrencyAdd(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	code := strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
	if code == "" {
		vm, _ := s.buildCurrencyVM(companyID)
		vm.AddCode = code
		vm.AddError = "Currency code is required."
		return pages.CompanyCurrency(vm).Render(c.Context(), c)
	}
	if err := services.AddCompanyCurrency(s.DB, companyID, code); err != nil {
		vm, _ := s.buildCurrencyVM(companyID)
		vm.AddCode = code
		vm.AddError = err.Error()
		return pages.CompanyCurrency(vm).Render(c.Context(), c)
	}
	return c.Redirect("/settings/company/currency?added=1", fiber.StatusSeeOther)
}

func (s *Server) buildCurrencyVM(companyID uint) (pages.CompanyCurrencyVM, error) {
	var company models.Company
	if err := s.DB.Select("id", "base_currency_code", "multi_currency_enabled").
		First(&company, companyID).Error; err != nil {
		return pages.CompanyCurrencyVM{}, err
	}
	companyCurrencies, err := services.ListCompanyCurrencies(s.DB, companyID)
	if err != nil {
		return pages.CompanyCurrencyVM{}, err
	}
	allCurrencies, err := services.ListActiveCurrencies(s.DB)
	if err != nil {
		return pages.CompanyCurrencyVM{}, err
	}
	return pages.CompanyCurrencyVM{
		HasCompany:           true,
		Breadcrumb:           breadcrumbSettingsCompanyCurrency(),
		BaseCurrencyCode:     company.BaseCurrencyCode,
		MultiCurrencyEnabled: company.MultiCurrencyEnabled,
		Currencies:           companyCurrencies,
		AllCurrencies:        allCurrencies,
	}, nil
}

// ── Exchange Rates ─────────────────────────────────────────────────────────────

func (s *Server) handleExchangeRatesGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm, err := s.buildExchangeRatesVM(companyID)
	if err != nil {
		return pages.CompanyExchangeRates(pages.CompanyExchangeRatesVM{
			HasCompany: true,
			Breadcrumb: breadcrumbSettingsExchangeRates(),
			FormError:  "Could not load exchange rates.",
		}).Render(c.Context(), c)
	}
	vm.Added = c.Query("added") == "1"
	vm.Deleted = c.Query("deleted") == "1"
	return pages.CompanyExchangeRates(vm).Render(c.Context(), c)
}

// handleExchangeRatesAdd creates or updates an exchange rate.
func (s *Server) handleExchangeRatesAdd(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	baseCode := strings.ToUpper(strings.TrimSpace(c.FormValue("base")))
	targetCode := strings.ToUpper(strings.TrimSpace(c.FormValue("target")))
	rateRaw := strings.TrimSpace(c.FormValue("rate"))
	dateRaw := strings.TrimSpace(c.FormValue("effective_date"))

	vm, _ := s.buildExchangeRatesVM(companyID)
	vm.AddBase = baseCode
	vm.AddTarget = targetCode
	vm.AddRate = rateRaw
	vm.AddDate = dateRaw

	rate, err := decimal.NewFromString(rateRaw)
	if err != nil || !rate.IsPositive() {
		vm.FormError = "Rate must be a positive number."
		return pages.CompanyExchangeRates(vm).Render(c.Context(), c)
	}
	effectiveDate, err := time.Parse("2006-01-02", dateRaw)
	if err != nil {
		vm.FormError = "Date must be in YYYY-MM-DD format."
		return pages.CompanyExchangeRates(vm).Render(c.Context(), c)
	}

	cid := companyID
	_, err = services.UpsertExchangeRate(s.DB, services.UpsertExchangeRateInput{
		CompanyID: &cid,
		Base:      baseCode,
		Target:    targetCode,
		Rate:      rate,
		RateType:  "spot",
		Source:    services.ExchangeRateRowSourceManual,
		Date:      effectiveDate,
	})
	if err != nil {
		vm.FormError = err.Error()
		return pages.CompanyExchangeRates(vm).Render(c.Context(), c)
	}
	return c.Redirect("/settings/company/exchange-rates?added=1", fiber.StatusSeeOther)
}

// handleExchangeRatesDelete removes a company exchange rate by ID.
func (s *Server) handleExchangeRatesDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	idRaw := strings.TrimSpace(c.FormValue("id"))
	id, err := services.ParseUint(idRaw)
	if err != nil || id == 0 {
		return c.Redirect("/settings/company/exchange-rates", fiber.StatusSeeOther)
	}
	cid := companyID
	_ = services.DeleteExchangeRate(s.DB, &cid, uint(id))
	return c.Redirect("/settings/company/exchange-rates?deleted=1", fiber.StatusSeeOther)
}

func (s *Server) buildExchangeRatesVM(companyID uint) (pages.CompanyExchangeRatesVM, error) {
	var company models.Company
	if err := s.DB.Select("id", "base_currency_code").First(&company, companyID).Error; err != nil {
		return pages.CompanyExchangeRatesVM{}, err
	}
	cid := companyID
	rates, err := services.ListExchangeRates(s.DB, &cid, "", "")
	if err != nil {
		return pages.CompanyExchangeRatesVM{}, err
	}
	companyCurrencies, err := services.ListCompanyCurrencies(s.DB, companyID)
	if err != nil {
		return pages.CompanyExchangeRatesVM{}, err
	}
	return pages.CompanyExchangeRatesVM{
		HasCompany:        true,
		Breadcrumb:        breadcrumbSettingsExchangeRates(),
		BaseCurrencyCode:  company.BaseCurrencyCode,
		Rates:             rates,
		CompanyCurrencies: companyCurrencies,
		AddBase:           company.BaseCurrencyCode,
		AddDate:           time.Now().Format("2006-01-02"),
	}, nil
}
