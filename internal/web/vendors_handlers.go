// 遵循project_guide.md
package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/searchprojection/producers"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleVendors(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	filterQ := strings.TrimSpace(c.Query("q"))
	filterStatus := normaliseListStatus(c.Query("status"))

	// Default to active-only; the Status select can flip to inactive or all.
	// Substring filter (q) matches name OR email so the operator can find
	// by either without choosing which column upfront.
	listQuery := s.DB.Where("company_id = ?", companyID)
	switch filterStatus {
	case "inactive":
		listQuery = listQuery.Where("is_active = false")
	case "all":
		// no status filter
	default:
		listQuery = listQuery.Where("is_active = true")
	}
	if filterQ != "" {
		like := "%" + strings.ToLower(filterQ) + "%"
		listQuery = listQuery.Where("LOWER(name) LIKE ? OR LOWER(email) LIKE ?", like, like)
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
		FilterQ:             filterQ,
		FilterStatus:        filterStatus,
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

// handleVendorQuickCreate creates a vendor from an inline picker drawer and
// returns the new record for immediate SmartPicker selection.
func (s *Server) handleVendorQuickCreate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	var in struct {
		Name         string `json:"name"`
		Email        string `json:"email"`
		Phone        string `json:"phone"`
		Address      string `json:"address"`
		CurrencyCode string `json:"currency_code"`
		Notes        string `json:"notes"`
		PaymentTerm  string `json:"payment_term"`
	}
	if err := c.BodyParser(&in); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body."})
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Vendor name is required."})
	}
	if len(name) > 200 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Name must be 200 characters or fewer."})
	}

	var count int64
	if err := s.DB.Model(&models.Vendor{}).
		Where("company_id = ? AND lower(name) = lower(?)", companyID, name).
		Count(&count).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Could not validate vendor name."})
	}
	if count > 0 {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "A vendor with this name already exists."})
	}

	multiCurrency, _, _ := s.vendorCurrencyInfo(companyID)
	currencyCode := strings.ToUpper(strings.TrimSpace(in.CurrencyCode))
	if !multiCurrency {
		currencyCode = ""
	} else if currencyCode != "" && len(currencyCode) != 3 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid currency code."})
	}

	paymentTerm := strings.TrimSpace(in.PaymentTerm)
	if paymentTerm != "" {
		var termCount int64
		if err := s.DB.Model(&models.PaymentTerm{}).
			Where("company_id = ? AND code = ? AND is_active = true", companyID, paymentTerm).
			Count(&termCount).Error; err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Could not validate payment term."})
		}
		if termCount == 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid payment term."})
		}
	}

	vendor := models.Vendor{
		CompanyID:              companyID,
		Name:                   name,
		Email:                  strings.TrimSpace(in.Email),
		Phone:                  strings.TrimSpace(in.Phone),
		Address:                strings.TrimSpace(in.Address),
		CurrencyCode:           currencyCode,
		Notes:                  strings.TrimSpace(in.Notes),
		DefaultPaymentTermCode: paymentTerm,
		IsActive:               true,
	}
	if err := s.DB.Create(&vendor).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Could not create vendor."})
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
		"source":     "quick_create",
	}, &cid, &uid)
	if s.SPAcceleration != nil {
		s.SPAcceleration.InvalidateCompany(companyID)
	}

	return c.JSON(fiber.Map{
		"id":            vendor.ID,
		"name":          vendor.Name,
		"currency_code": vendor.CurrencyCode,
		"payment_term":  vendor.DefaultPaymentTermCode,
	})
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
