// 遵循project_guide.md
package web

// customer_shipping_handlers.go — POST /customers/:id/shipping-addresses
// (and the per-row delete / set-default endpoints). All redirect back to
// /customers/:id with ?saved=1 / ?error=… on failure, mirroring the existing
// customer-detail update flow.
//
// Permissions: shipping-address management requires the same permission as
// editing other customer fields (ActionInvoiceCreate — see existing
// /customers/:id/update wiring), since it changes data that downstream
// invoices snapshot at save time.

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
)

func parseShippingAddrID(c *fiber.Ctx) (uint, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Params("addrID")), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid addrID")
	}
	return uint(id), nil
}

// POST /customers/:id/shipping-addresses
//
// Form fields: label, addr_street1, addr_street2, addr_city, addr_province,
// addr_postal_code, addr_country, is_default ("1" for true).
func (s *Server) handleCustomerShippingAddressCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	customerID, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/customers", fiber.StatusSeeOther)
	}
	in := services.CustomerShippingAddressInput{
		Label:          c.FormValue("label"),
		AddrStreet1:    c.FormValue("addr_street1"),
		AddrStreet2:    c.FormValue("addr_street2"),
		AddrCity:       c.FormValue("addr_city"),
		AddrProvince:   c.FormValue("addr_province"),
		AddrPostalCode: c.FormValue("addr_postal_code"),
		AddrCountry:    c.FormValue("addr_country"),
		IsDefault:      strings.TrimSpace(c.FormValue("is_default")) == "1",
	}
	if _, err := services.AddCustomerShippingAddress(s.DB, companyID, customerID, in); err != nil {
		return redirectErr(c, customerDetailURL(customerID), err.Error())
	}
	return c.Redirect(customerDetailURL(customerID)+"?saved=1", fiber.StatusSeeOther)
}

// POST /customers/:id/shipping-addresses/:addrID/delete
func (s *Server) handleCustomerShippingAddressDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	customerID, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/customers", fiber.StatusSeeOther)
	}
	addrID, err := parseShippingAddrID(c)
	if err != nil {
		return c.Redirect(customerDetailURL(customerID), fiber.StatusSeeOther)
	}
	if err := services.DeleteCustomerShippingAddress(s.DB, companyID, customerID, addrID); err != nil {
		return redirectErr(c, customerDetailURL(customerID), err.Error())
	}
	return c.Redirect(customerDetailURL(customerID)+"?saved=1", fiber.StatusSeeOther)
}

// POST /customers/:id/shipping-addresses/:addrID/set-default
func (s *Server) handleCustomerShippingAddressSetDefault(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	customerID, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/customers", fiber.StatusSeeOther)
	}
	addrID, err := parseShippingAddrID(c)
	if err != nil {
		return c.Redirect(customerDetailURL(customerID), fiber.StatusSeeOther)
	}
	if err := services.SetDefaultCustomerShippingAddress(s.DB, companyID, customerID, addrID); err != nil {
		return redirectErr(c, customerDetailURL(customerID), err.Error())
	}
	return c.Redirect(customerDetailURL(customerID)+"?saved=1", fiber.StatusSeeOther)
}

func customerDetailURL(customerID uint) string {
	return "/customers/" + strconv.FormatUint(uint64(customerID), 10)
}
