// 遵循project_guide.md
package web

// customer_detail_update_handlers.go — POST /customers/:id/update
// Detail-scoped variant of handleCustomerUpdate. The existing
// handleCustomerUpdate at customers_handlers.go:224 redirects back to the
// list-page drawer (/customers?updated=1); this handler instead redirects
// back to /customers/:id?saved=1, mirroring the vendor detail edit flow.
//
// Validation + persistence logic is deliberately kept parallel to
// handleCustomerUpdate (same parseCustomerForm + validateCustomerFields +
// per-company case-insensitive uniqueness check) — only the error re-render
// path and success redirect differ.

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleCustomerDetailUpdate updates a customer from the /customers/:id edit
// form and returns to the detail page.
//
// POST /customers/:id/update
func (s *Server) handleCustomerDetailUpdate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customerID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || customerID64 == 0 {
		return redirectErr(c, "/customers", "invalid customer ID")
	}
	customerID := uint(customerID64)

	var existing models.Customer
	if err := s.DB.Where("id = ? AND company_id = ?", customerID, companyID).First(&existing).Error; err != nil {
		return redirectErr(c, "/customers", "customer not found")
	}

	multiCurrency, _, _ := s.vendorCurrencyInfo(companyID)
	name, email, currencyCode, paymentTerm,
		addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry := parseCustomerForm(c)

	// Rebuilds the full detail VM in edit mode. Used on any validation or
	// persistence failure so the user sees errors in place with their typed
	// values preserved.
	buildEditVM := func(nameErr, currencyErr, formErr string) pages.CustomerDetailVM {
		vm := pages.CustomerDetailVM{
			HasCompany:         true,
			Tab:                "details",
			Customer:           existing,
			Editing:            true,
			FormName:           name,
			FormEmail:          email,
			FormCurrencyCode:   currencyCode,
			FormPaymentTerm:    paymentTerm,
			FormAddrStreet1:    addrStreet1,
			FormAddrStreet2:    addrStreet2,
			FormAddrCity:       addrCity,
			FormAddrProvince:   addrProvince,
			FormAddrPostalCode: addrPostalCode,
			FormAddrCountry:    addrCountry,
			NameError:          nameErr,
			CurrencyError:      currencyErr,
			FormError:          formErr,
		}
		s.loadCustomerEditFormData(companyID, &vm)
		return vm
	}

	var nameErr, currencyErr string
	if formErr := validateCustomerFields(name, email, currencyCode, multiCurrency, paymentTerm,
		addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry,
		&nameErr, &currencyErr); formErr != "" {
		return pages.CustomerDetail(buildEditVM(nameErr, currencyErr, formErr)).Render(c.Context(), c)
	}
	if nameErr != "" || currencyErr != "" {
		return pages.CustomerDetail(buildEditVM(nameErr, currencyErr, "")).Render(c.Context(), c)
	}

	// Duplicate name check excluding self.
	var count int64
	if err := s.DB.Model(&models.Customer{}).
		Where("company_id = ? AND lower(name) = lower(?) AND id <> ?", companyID, name, customerID).
		Count(&count).Error; err != nil {
		return pages.CustomerDetail(buildEditVM("", "", "Could not validate customer name.")).Render(c.Context(), c)
	}
	if count > 0 {
		return pages.CustomerDetail(buildEditVM("A customer with this name already exists for this company.", "", "")).Render(c.Context(), c)
	}

	existing.Name = name
	existing.Email = email
	existing.CurrencyCode = currencyCode
	existing.DefaultPaymentTermCode = paymentTerm
	existing.AddrStreet1 = addrStreet1
	existing.AddrStreet2 = addrStreet2
	existing.AddrCity = addrCity
	existing.AddrProvince = addrProvince
	existing.AddrPostalCode = addrPostalCode
	existing.AddrCountry = addrCountry

	if err := s.DB.Save(&existing).Error; err != nil {
		return pages.CustomerDetail(buildEditVM("", "", "Could not update customer. Please try again.")).Render(c.Context(), c)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "customer.updated", "customer", existing.ID, actor, map[string]any{
		"name":       name,
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/customers/"+c.Params("id")+"?tab=details&saved=1", fiber.StatusSeeOther)
}
