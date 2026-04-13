// 遵循project_guide.md
package web

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

var rePostalCode = regexp.MustCompile(`^[A-Za-z0-9 \-]*$`)

func (s *Server) handleCustomers(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm := pages.CustomersVM{
		HasCompany: true,
		FormError:  strings.TrimSpace(c.Query("error")),
		Created:    c.Query("created") == "1",
		Updated:    c.Query("updated") == "1",
	}
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&vm.PaymentTerms)

	// Load customer list.
	if err := s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vm.Customers).Error; err != nil {
		vm.FormError = "Could not load customers."
		vm.Customers = []models.Customer{}
		return pages.Customers(vm).Render(c.Context(), c)
	}
	if summaries, err := services.ListCustomerBillableSummaries(s.DB, companyID); err == nil {
		vm.BillableSummaries = summaries
	}

	// Handle ?edit=ID — open drawer pre-populated with the customer's data.
	if editRaw := strings.TrimSpace(c.Query("edit")); editRaw != "" {
		if id64, err := strconv.ParseUint(editRaw, 10, 64); err == nil && id64 > 0 {
			var cust models.Customer
			if err := s.DB.Where("id = ? AND company_id = ?", uint(id64), companyID).First(&cust).Error; err == nil {
				vm.DrawerOpen = true
				vm.EditingID = uint(id64)
				vm.Name = cust.Name
				vm.Email = cust.Email
				vm.DefaultPaymentTermCode = cust.DefaultPaymentTermCode
				vm.AddrStreet1 = cust.AddrStreet1
				vm.AddrStreet2 = cust.AddrStreet2
				vm.AddrCity = cust.AddrCity
				vm.AddrProvince = cust.AddrProvince
				vm.AddrPostalCode = cust.AddrPostalCode
				vm.AddrCountry = cust.AddrCountry
			}
		}
	}

	return pages.Customers(vm).Render(c.Context(), c)
}

func (s *Server) handleCustomerDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customerID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || customerID64 == 0 {
		return redirectErr(c, "/customers", "invalid customer ID")
	}

	workspace, err := services.GetCustomerWorkspace(s.DB, companyID, uint(customerID64))
	if err != nil {
		return redirectErr(c, "/customers", err.Error())
	}

	// Batch 16: load credit summary for customer detail page.
	creditCount := 0
	creditRemaining, _ := services.CustomerCreditTotalRemaining(s.DB, companyID, uint(customerID64))
	if activeCredits, err := services.ListActiveCustomerCredits(s.DB, companyID, uint(customerID64)); err == nil {
		creditCount = len(activeCredits)
	}

	return pages.CustomerDetail(pages.CustomerDetailVM{
		HasCompany:              true,
		Customer:                workspace.Customer,
		DefaultPaymentTermLabel: workspace.DefaultPaymentTermLabel,
		BillableSummary:         workspace.BillableSummary,
		ARSummary:               workspace.ARSummary,
		OutstandingInvoices:     workspace.OutstandingInvoices,
		RecentInvoices:          workspace.RecentInvoices,
		MostRecentInvoice:       workspace.MostRecentInvoice,
		CreditCount:             creditCount,
		CreditRemaining:         creditRemaining,
	}).Render(c.Context(), c)
}

func (s *Server) handleCustomerNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.CustomerNewVM{HasCompany: true}
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&vm.PaymentTerms)
	return pages.CustomerNew(vm).Render(c.Context(), c)
}

func (s *Server) handleCustomerCreate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	name, email, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry := parseCustomerForm(c)

	vm := pages.CustomerNewVM{
		HasCompany:             true,
		Name:                   name,
		Email:                  email,
		DefaultPaymentTermCode: paymentTerm,
		AddrStreet1:            addrStreet1,
		AddrStreet2:            addrStreet2,
		AddrCity:               addrCity,
		AddrProvince:           addrProvince,
		AddrPostalCode:         addrPostalCode,
		AddrCountry:            addrCountry,
	}
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&vm.PaymentTerms)

	if errMsg := validateCustomerFields(name, email, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry, &vm.NameError); errMsg != "" {
		vm.FormError = errMsg
		return pages.CustomerNew(vm).Render(c.Context(), c)
	}
	if vm.NameError != "" {
		return pages.CustomerNew(vm).Render(c.Context(), c)
	}

	// Duplicate name check.
	var count int64
	if err := s.DB.Model(&models.Customer{}).
		Where("company_id = ? AND lower(name) = lower(?)", companyID, name).
		Count(&count).Error; err != nil {
		vm.FormError = "Could not validate customer name."
		return pages.CustomerNew(vm).Render(c.Context(), c)
	}
	if count > 0 {
		vm.NameError = "A customer with this name already exists for this company."
		return pages.CustomerNew(vm).Render(c.Context(), c)
	}

	customer := models.Customer{
		CompanyID:              companyID,
		Name:                   name,
		Email:                  email,
		DefaultPaymentTermCode: paymentTerm,
		AddrStreet1:            addrStreet1,
		AddrStreet2:            addrStreet2,
		AddrCity:               addrCity,
		AddrProvince:           addrProvince,
		AddrPostalCode:         addrPostalCode,
		AddrCountry:            addrCountry,
	}
	if err := s.DB.Create(&customer).Error; err != nil {
		vm.FormError = "Could not create customer. Please try again."
		return pages.CustomerNew(vm).Render(c.Context(), c)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "customer.created", "customer", customer.ID, actor, map[string]any{
		"name":       name,
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/customers?created=1")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/customers?created=1", fiber.StatusSeeOther)
}

func (s *Server) handleCustomerUpdate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.FormValue("customer_id"))
	id64, idErr := strconv.ParseUint(idRaw, 10, 64)
	if idErr != nil || id64 == 0 {
		return c.Redirect("/customers", fiber.StatusSeeOther)
	}
	customerID := uint(id64)

	var existing models.Customer
	if err := s.DB.Where("id = ? AND company_id = ?", customerID, companyID).First(&existing).Error; err != nil {
		return c.Redirect("/customers", fiber.StatusSeeOther)
	}

	name, email, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry := parseCustomerForm(c)

	// Build a VM for re-rendering the list page with the drawer open on error.
	buildErrVM := func(nameErr, formErr string) pages.CustomersVM {
		vm := pages.CustomersVM{
			HasCompany:             true,
			DrawerOpen:             true,
			EditingID:              customerID,
			Name:                   name,
			Email:                  email,
			DefaultPaymentTermCode: paymentTerm,
			AddrStreet1:            addrStreet1,
			AddrStreet2:            addrStreet2,
			AddrCity:               addrCity,
			AddrProvince:           addrProvince,
			AddrPostalCode:         addrPostalCode,
			AddrCountry:            addrCountry,
			NameError:              nameErr,
			FormError:              formErr,
		}
		_ = s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vm.Customers)
		_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&vm.PaymentTerms)
		if summaries, err := services.ListCustomerBillableSummaries(s.DB, companyID); err == nil {
			vm.BillableSummaries = summaries
		}
		return vm
	}

	var nameErr string
	if formErr := validateCustomerFields(name, email, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry, &nameErr); formErr != "" {
		return pages.Customers(buildErrVM(nameErr, formErr)).Render(c.Context(), c)
	}
	if nameErr != "" {
		return pages.Customers(buildErrVM(nameErr, "")).Render(c.Context(), c)
	}

	// Duplicate name check (exclude self).
	var count int64
	if err := s.DB.Model(&models.Customer{}).
		Where("company_id = ? AND lower(name) = lower(?) AND id <> ?", companyID, name, customerID).
		Count(&count).Error; err != nil {
		return pages.Customers(buildErrVM("", "Could not validate customer name.")).Render(c.Context(), c)
	}
	if count > 0 {
		return pages.Customers(buildErrVM("A customer with this name already exists for this company.", "")).Render(c.Context(), c)
	}

	existing.Name = name
	existing.Email = email
	existing.DefaultPaymentTermCode = paymentTerm
	existing.AddrStreet1 = addrStreet1
	existing.AddrStreet2 = addrStreet2
	existing.AddrCity = addrCity
	existing.AddrProvince = addrProvince
	existing.AddrPostalCode = addrPostalCode
	existing.AddrCountry = addrCountry

	if err := s.DB.Save(&existing).Error; err != nil {
		return pages.Customers(buildErrVM("", "Could not update customer. Please try again.")).Render(c.Context(), c)
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

	return c.Redirect("/customers?updated=1", fiber.StatusSeeOther)
}

func (s *Server) customersForCompany(companyID uint) ([]models.Customer, error) {
	var customers []models.Customer
	err := s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&customers).Error
	return customers, err
}

// parseCustomerForm reads and trims all customer form fields from a POST request.
func parseCustomerForm(c *fiber.Ctx) (name, email, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry string) {
	name = strings.TrimSpace(c.FormValue("name"))
	email = strings.TrimSpace(c.FormValue("email"))
	paymentTerm = strings.TrimSpace(c.FormValue("payment_term"))
	addrStreet1 = strings.TrimSpace(c.FormValue("addr_street1"))
	addrStreet2 = strings.TrimSpace(c.FormValue("addr_street2"))
	addrCity = strings.TrimSpace(c.FormValue("addr_city"))
	addrProvince = strings.TrimSpace(c.FormValue("addr_province"))
	addrPostalCode = strings.TrimSpace(c.FormValue("addr_postal_code"))
	addrCountry = strings.TrimSpace(c.FormValue("addr_country"))
	return
}

// validateCustomerFields validates all customer form fields.
// Sets *nameErr if the name is invalid; returns a non-empty formErr string for all other errors.
func validateCustomerFields(name, email, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry string, nameErr *string) string {
	if name == "" {
		*nameErr = "Name is required."
	} else if len(name) > 200 {
		*nameErr = "Name must be 200 characters or fewer."
	}
	if len(email) > 200 {
		return "Email must be 200 characters or fewer."
	}
	if len(paymentTerm) > 100 {
		return "Payment term must be 100 characters or fewer."
	}
	if len(addrStreet1) > 200 || len(addrStreet2) > 200 {
		return "Street address must be 200 characters or fewer."
	}
	if len(addrCity) > 100 || len(addrProvince) > 100 || len(addrCountry) > 100 {
		return "City, province, and country must be 100 characters or fewer."
	}
	if len(addrPostalCode) > 20 {
		return "Postal code must be 20 characters or fewer."
	}
	if addrPostalCode != "" && !rePostalCode.MatchString(addrPostalCode) {
		return "Postal code may only contain letters, numbers, spaces, and hyphens."
	}
	return ""
}

// handleCustomerQuickCreate creates a minimal customer record from an inline
// Quick Create panel (e.g. the invoice editor drawer). Accepts JSON {name} and
// returns JSON {id, name} on success, or {error} on failure.
func (s *Server) handleCustomerQuickCreate(c *fiber.Ctx) error {
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
		CurrencyCode string `json:"currency_code"`
	}
	if err := c.BodyParser(&in); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body."})
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Customer name is required."})
	}
	if len(name) > 200 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Name must be 200 characters or fewer."})
	}

	// Validate currency code format if provided (must be 3 uppercase letters or empty).
	currencyCode := strings.ToUpper(strings.TrimSpace(in.CurrencyCode))
	if currencyCode != "" && len(currencyCode) != 3 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid currency code."})
	}

	// Duplicate name check.
	var count int64
	if err := s.DB.Model(&models.Customer{}).
		Where("company_id = ? AND lower(name) = lower(?)", companyID, name).
		Count(&count).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Could not validate customer name."})
	}
	if count > 0 {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "A customer with this name already exists."})
	}

	customer := models.Customer{
		CompanyID:    companyID,
		Name:         name,
		CurrencyCode: currencyCode,
	}
	if err := s.DB.Create(&customer).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Could not create customer."})
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "customer.created", "customer", customer.ID, actor, map[string]any{
		"name":       name,
		"company_id": companyID,
		"source":     "quick_create",
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.JSON(fiber.Map{
		"id":            customer.ID,
		"name":          customer.Name,
		"currency_code": customer.CurrencyCode,
	})
}
