// 遵循project_guide.md
package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/searchprojection/producers"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleVendors(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	showInactive := c.Query("show_inactive") == "1"

	// Default to active-only; include inactive when the toggle is on.
	listQuery := s.DB.Where("company_id = ?", companyID)
	if !showInactive {
		listQuery = listQuery.Where("is_active = true")
	}
	var vendors []models.Vendor
	if err := listQuery.Order("name asc").Find(&vendors).Error; err != nil {
		return pages.Vendors(pages.VendorsVM{
			HasCompany: true,
			FormError:  "Could not load vendors.",
			Vendors:    []models.Vendor{},
		}).Render(c.Context(), c)
	}

	var inactiveCount int64
	s.DB.Model(&models.Vendor{}).
		Where("company_id = ? AND is_active = false", companyID).
		Count(&inactiveCount)

	var paymentTerms []models.PaymentTerm
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&paymentTerms)

	multiCurrency, baseCurrency, currencies := s.vendorCurrencyInfo(companyID)

	return pages.Vendors(pages.VendorsVM{
		HasCompany:          true,
		Vendors:             vendors,
		Created:             c.Query("created") == "1",
		PaymentTerms:        paymentTerms,
		MultiCurrency:       multiCurrency,
		BaseCurrencyCode:    baseCurrency,
		Currencies:          currencies,
		ShowInactive:        showInactive,
		InactiveVendorCount: int(inactiveCount),
	}).Render(c.Context(), c)
}

func (s *Server) handleVendorCreate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	name         := strings.TrimSpace(c.FormValue("name"))
	email        := strings.TrimSpace(c.FormValue("email"))
	phone        := strings.TrimSpace(c.FormValue("phone"))
	address      := strings.TrimSpace(c.FormValue("address"))
	currencyCode := strings.TrimSpace(c.FormValue("currency_code"))
	notes        := strings.TrimSpace(c.FormValue("notes"))
	paymentTerm  := strings.TrimSpace(c.FormValue("payment_term"))

	multiCurrency, baseCurrency, currencies := s.vendorCurrencyInfo(companyID)

	vm := pages.VendorsVM{
		HasCompany:             true,
		Name:                   name,
		Email:                  email,
		Phone:                  phone,
		Address:                address,
		CurrencyCode:           currencyCode,
		Notes:                  notes,
		DefaultPaymentTermCode: paymentTerm,
		MultiCurrency:          multiCurrency,
		BaseCurrencyCode:       baseCurrency,
		Currencies:             currencies,
	}
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&vm.PaymentTerms)

	if name == "" {
		vm.NameError = "Name is required."
	}

	vendors, listErr := s.vendorsForCompany(companyID)
	if listErr != nil {
		vm.FormError = "Could not load vendors."
	} else {
		vm.Vendors = vendors
	}

	if vm.NameError != "" {
		return pages.Vendors(vm).Render(c.Context(), c)
	}

	var count int64
	if err := s.DB.Model(&models.Vendor{}).
		Where("company_id = ? AND lower(name) = lower(?)", companyID, name).
		Count(&count).Error; err != nil {
		vm.FormError = "Could not validate vendor name."
		return pages.Vendors(vm).Render(c.Context(), c)
	}
	if count > 0 {
		vm.NameError = "A vendor with this name already exists for this company."
		return pages.Vendors(vm).Render(c.Context(), c)
	}

	// Discard currency selection when multi-currency is not enabled.
	if !multiCurrency {
		currencyCode = ""
	}

	vendor := models.Vendor{
		CompanyID:              companyID,
		Name:                   name,
		Email:                  email,
		Phone:                  phone,
		Address:                address,
		CurrencyCode:           currencyCode,
		Notes:                  notes,
		DefaultPaymentTermCode: paymentTerm,
	}
	if err := s.DB.Create(&vendor).Error; err != nil {
		vm.FormError = "Could not create vendor. Please try again."
		return pages.Vendors(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectVendor(c.Context(), s.DB, s.SearchProjector, companyID, vendor.ID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "vendor.created", "vendor", vendor.ID, actor, map[string]any{
		"name":       name,
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/vendors?created=1")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/vendors?created=1", fiber.StatusSeeOther)
}

// vendorsForCompany returns ACTIVE vendors for a company, alphabetical.
// Mirror of customersForCompany — same reasoning: use this for every new-
// document picker (bill, PO, expense, VCN, vendor prepayment/refund/return)
// and list-filter dropdown. Deactivated vendors are hidden unless the caller
// explicitly asks via allVendorsForCompany.
func (s *Server) vendorsForCompany(companyID uint) ([]models.Vendor, error) {
	var vendors []models.Vendor
	err := s.DB.
		Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").
		Find(&vendors).Error
	return vendors, err
}

// allVendorsForCompany returns every vendor (active + inactive) alphabetically.
func (s *Server) allVendorsForCompany(companyID uint) ([]models.Vendor, error) {
	var vendors []models.Vendor
	err := s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vendors).Error
	return vendors, err
}

// vendorCurrencyInfo returns the multi-currency flag, base currency code, and
// (when multi-currency is on) the list of enabled currencies for the dropdown.
// On any DB error it returns safe defaults (single-currency, no list).
func (s *Server) vendorCurrencyInfo(companyID uint) (multiCurrency bool, baseCurrency string, currencies []models.Currency) {
	var co models.Company
	if err := s.DB.Select("id, base_currency_code, multi_currency_enabled").First(&co, companyID).Error; err != nil {
		return false, "", nil
	}
	baseCurrency = co.BaseCurrencyCode
	if !co.MultiCurrencyEnabled {
		return false, baseCurrency, nil
	}

	// Multi-currency: collect base + active foreign currency codes.
	codes := []string{baseCurrency}
	var foreign []models.CompanyCurrency
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Find(&foreign)
	for _, f := range foreign {
		if f.CurrencyCode != baseCurrency {
			codes = append(codes, f.CurrencyCode)
		}
	}
	_ = s.DB.Where("code IN ? AND is_active = true", codes).Order("code asc").Find(&currencies)
	return true, baseCurrency, currencies
}
