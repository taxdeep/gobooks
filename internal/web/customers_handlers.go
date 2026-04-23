// 遵循project_guide.md
package web

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/searchprojection/producers"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

var rePostalCode = regexp.MustCompile(`^[A-Za-z0-9 \-]*$`)

func (s *Server) handleCustomers(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	multiCurrency, baseCurrency, currencies := s.vendorCurrencyInfo(companyID)

	showInactive := c.Query("show_inactive") == "1"

	vm := pages.CustomersVM{
		HasCompany:       true,
		FormError:        strings.TrimSpace(c.Query("error")),
		Created:          c.Query("created") == "1",
		Updated:          c.Query("updated") == "1",
		MultiCurrency:    multiCurrency,
		BaseCurrencyCode: baseCurrency,
		Currencies:       currencies,
		ShowInactive:     showInactive,
	}
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&vm.PaymentTerms)

	// Load customer list — default to active-only; include inactive when the
	// toggle is on. The unfiltered inactive count feeds the toggle label so
	// users know what's hidden without having to flip it.
	listQuery := s.DB.Where("company_id = ?", companyID)
	if !showInactive {
		listQuery = listQuery.Where("is_active = true")
	}
	if err := listQuery.Order("name asc").Find(&vm.Customers).Error; err != nil {
		vm.FormError = "Could not load customers."
		vm.Customers = []models.Customer{}
		return pages.Customers(vm).Render(c.Context(), c)
	}
	var inactiveCount int64
	s.DB.Model(&models.Customer{}).
		Where("company_id = ? AND is_active = false", companyID).
		Count(&inactiveCount)
	vm.InactiveCustomerCount = int(inactiveCount)
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
				vm.CurrencyCode = cust.CurrencyCode
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

	// Refund aggregates — posted-only so the count reflects actual cash
	// that went back to the customer. Direct COUNT/SUM rather than fetching
	// the full refund list and summing in Go.
	var refundCount int64
	s.DB.Model(&models.ARRefund{}).
		Where("company_id = ? AND customer_id = ? AND status = ?",
			companyID, uint(customerID64), models.ARRefundStatusPosted).
		Count(&refundCount)

	var refundTotalResult struct{ Total decimal.Decimal }
	s.DB.Model(&models.ARRefund{}).
		Select("COALESCE(SUM(amount), 0) AS total").
		Where("company_id = ? AND customer_id = ? AND status = ?",
			companyID, uint(customerID64), models.ARRefundStatusPosted).
		Scan(&refundTotalResult)

	// Pre-invoice commercial-commitment tables — quotes and sales orders.
	// Capped at 25 rows to match the vendor detail Recent POs section.
	const customerDetailCommercialCap = 25
	var recentQuotes []models.Quote
	s.DB.Where("company_id = ? AND customer_id = ?", companyID, uint(customerID64)).
		Order("quote_date desc, id desc").
		Limit(customerDetailCommercialCap).
		Find(&recentQuotes)

	var recentSOs []models.SalesOrder
	s.DB.Where("company_id = ? AND customer_id = ?", companyID, uint(customerID64)).
		Order("order_date desc, id desc").
		Limit(customerDetailCommercialCap).
		Find(&recentSOs)

	// Phase 12: load currency policy data.
	allowedCurrencies, _ := services.ListCustomerAllowedCurrencies(s.DB, companyID, uint(customerID64))
	var company models.Company
	baseCurrencyCode := ""
	if err := s.DB.Select("base_currency_code").First(&company, companyID).Error; err == nil {
		baseCurrencyCode = company.BaseCurrencyCode
	}

	// Lifecycle decision — delete vs deactivate depends on whether the
	// customer is referenced by any AR document.
	hasRecords, _ := services.CustomerHasRecords(s.DB, companyID, uint(customerID64))

	// Migration 088: shipping-address catalogue for the dedicated card.
	// Best-effort: errors leave the list empty (card shows "no addresses").
	shippingAddrs, _ := services.ListCustomerShippingAddresses(s.DB, companyID, uint(customerID64))

	vm := pages.CustomerDetailVM{
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
		RefundCount:             int(refundCount),
		RefundTotal:             refundTotalResult.Total,
		RecentQuotes:            recentQuotes,
		RecentSalesOrders:       recentSOs,
		AllowedCurrencies:       allowedCurrencies,
		BaseCurrencyCode:        baseCurrencyCode,
		CurrencyPolicySaved:     c.Query("policy_saved") == "1",
		Editing:                 c.Query("edit") == "1",
		Saved:                   c.Query("saved") == "1",
		HasRecords:              hasRecords,
		Deactivated:             c.Query("deactivated") == "1",
		Reactivated:             c.Query("reactivated") == "1",
		LifecycleErr:            strings.TrimSpace(c.Query("error")),
		ShippingAddresses:       shippingAddrs,
	}

	// Seed the edit form from the current customer record when entering edit
	// mode. On validation failure handleCustomerDetailUpdate re-renders with
	// these fields overwritten by the POSTed values.
	if vm.Editing {
		s.loadCustomerEditFormData(companyID, &vm)
		vm.FormName = workspace.Customer.Name
		vm.FormEmail = workspace.Customer.Email
		vm.FormCurrencyCode = workspace.Customer.CurrencyCode
		vm.FormPaymentTerm = workspace.Customer.DefaultPaymentTermCode
		vm.FormAddrStreet1 = workspace.Customer.AddrStreet1
		vm.FormAddrStreet2 = workspace.Customer.AddrStreet2
		vm.FormAddrCity = workspace.Customer.AddrCity
		vm.FormAddrProvince = workspace.Customer.AddrProvince
		vm.FormAddrPostalCode = workspace.Customer.AddrPostalCode
		vm.FormAddrCountry = workspace.Customer.AddrCountry
	}

	return pages.CustomerDetail(vm).Render(c.Context(), c)
}

// loadCustomerEditFormData populates dropdown data for the inline edit form.
// Uses vendorCurrencyInfo since customer + vendor share the same company-level
// currency configuration (base + allowed list).
func (s *Server) loadCustomerEditFormData(companyID uint, vm *pages.CustomerDetailVM) {
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("sort_order asc, code asc").
		Find(&vm.PaymentTerms)
	vm.MultiCurrency, vm.BaseCurrencyCode, vm.Currencies = s.vendorCurrencyInfo(companyID)
}

func (s *Server) handleCustomerNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	multiCurrency, baseCurrency, currencies := s.vendorCurrencyInfo(companyID)
	vm := pages.CustomerNewVM{
		HasCompany:       true,
		MultiCurrency:    multiCurrency,
		BaseCurrencyCode: baseCurrency,
		Currencies:       currencies,
	}
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

	multiCurrency, baseCurrency, currencies := s.vendorCurrencyInfo(companyID)
	name, email, currencyCode, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry := parseCustomerForm(c)

	vm := pages.CustomerNewVM{
		HasCompany:             true,
		Name:                   name,
		Email:                  email,
		CurrencyCode:           currencyCode,
		DefaultPaymentTermCode: paymentTerm,
		AddrStreet1:            addrStreet1,
		AddrStreet2:            addrStreet2,
		AddrCity:               addrCity,
		AddrProvince:           addrProvince,
		AddrPostalCode:         addrPostalCode,
		AddrCountry:            addrCountry,
		MultiCurrency:          multiCurrency,
		BaseCurrencyCode:       baseCurrency,
		Currencies:             currencies,
	}
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&vm.PaymentTerms)

	if errMsg := validateCustomerFields(name, email, currencyCode, multiCurrency, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry, &vm.NameError, &vm.CurrencyError); errMsg != "" {
		vm.FormError = errMsg
		return pages.CustomerNew(vm).Render(c.Context(), c)
	}
	if vm.NameError != "" || vm.CurrencyError != "" {
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
		CurrencyCode:           currencyCode,
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
	// Post-commit projection — failure logged, not surfaced to caller.
	_ = producers.ProjectCustomer(c.Context(), s.DB, s.SearchProjector, companyID, customer.ID)

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

	multiCurrency, baseCurrency, currencies := s.vendorCurrencyInfo(companyID)
	name, email, currencyCode, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry := parseCustomerForm(c)

	// Build a VM for re-rendering the list page with the drawer open on error.
	buildErrVM := func(nameErr, currencyErr, formErr string) pages.CustomersVM {
		vm := pages.CustomersVM{
			HasCompany:             true,
			DrawerOpen:             true,
			EditingID:              customerID,
			Name:                   name,
			Email:                  email,
			CurrencyCode:           currencyCode,
			DefaultPaymentTermCode: paymentTerm,
			AddrStreet1:            addrStreet1,
			AddrStreet2:            addrStreet2,
			AddrCity:               addrCity,
			AddrProvince:           addrProvince,
			AddrPostalCode:         addrPostalCode,
			AddrCountry:            addrCountry,
			NameError:              nameErr,
			CurrencyError:          currencyErr,
			FormError:              formErr,
			MultiCurrency:          multiCurrency,
			BaseCurrencyCode:       baseCurrency,
			Currencies:             currencies,
		}
		_ = s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vm.Customers)
		_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("sort_order asc, code asc").Find(&vm.PaymentTerms)
		if summaries, err := services.ListCustomerBillableSummaries(s.DB, companyID); err == nil {
			vm.BillableSummaries = summaries
		}
		return vm
	}

	var nameErr, currencyErr string
	if formErr := validateCustomerFields(name, email, currencyCode, multiCurrency, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry, &nameErr, &currencyErr); formErr != "" {
		return pages.Customers(buildErrVM(nameErr, currencyErr, formErr)).Render(c.Context(), c)
	}
	if nameErr != "" || currencyErr != "" {
		return pages.Customers(buildErrVM(nameErr, currencyErr, "")).Render(c.Context(), c)
	}

	// Duplicate name check (exclude self).
	var count int64
	if err := s.DB.Model(&models.Customer{}).
		Where("company_id = ? AND lower(name) = lower(?) AND id <> ?", companyID, name, customerID).
		Count(&count).Error; err != nil {
		return pages.Customers(buildErrVM("", "", "Could not validate customer name.")).Render(c.Context(), c)
	}
	if count > 0 {
		return pages.Customers(buildErrVM("A customer with this name already exists for this company.", "", "")).Render(c.Context(), c)
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
		return pages.Customers(buildErrVM("", "", "Could not update customer. Please try again.")).Render(c.Context(), c)
	}
	_ = producers.ProjectCustomer(c.Context(), s.DB, s.SearchProjector, companyID, existing.ID)

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

// customersForCompany returns ACTIVE customers for a company, alphabetical.
// This is the standard helper for populating pickers on new-document forms
// (invoice, quote, deposit, receipt, refund, etc.) and list-filter dropdowns.
// Deactivated customers are intentionally hidden — use allCustomersForCompany
// if you need them (e.g. on the /customers list page when show_inactive=1).
func (s *Server) customersForCompany(companyID uint) ([]models.Customer, error) {
	var customers []models.Customer
	err := s.DB.
		Where("company_id = ? AND is_active = true", companyID).
		Order("name asc").
		Find(&customers).Error
	return customers, err
}

// allCustomersForCompany returns every customer (active + inactive) alphabetically.
// Only use on admin screens that explicitly display inactive parties.
func (s *Server) allCustomersForCompany(companyID uint) ([]models.Customer, error) {
	var customers []models.Customer
	err := s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&customers).Error
	return customers, err
}

// parseCustomerForm reads and trims all customer form fields from a POST request.
func parseCustomerForm(c *fiber.Ctx) (name, email, currencyCode, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry string) {
	name = strings.TrimSpace(c.FormValue("name"))
	email = strings.TrimSpace(c.FormValue("email"))
	currencyCode = strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
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
// Sets *nameErr and *currencyErr for field-level errors; returns a non-empty formErr string for all other errors.
func validateCustomerFields(name, email, currencyCode string, multiCurrency bool, paymentTerm, addrStreet1, addrStreet2, addrCity, addrProvince, addrPostalCode, addrCountry string, nameErr, currencyErr *string) string {
	if name == "" {
		*nameErr = "Name is required."
	} else if len(name) > 200 {
		*nameErr = "Name must be 200 characters or fewer."
	}
	if multiCurrency && currencyCode == "" {
		*currencyErr = "Currency is required when multi-currency is enabled."
	} else if currencyCode != "" && len(currencyCode) != 3 {
		*currencyErr = "Currency code must be 3 letters (e.g. CAD, USD)."
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

// handleCustomerCurrencyPolicySet updates the customer's currency policy (single / multi_allowed).
func (s *Server) handleCustomerCurrencyPolicySet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	customerID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || customerID64 == 0 {
		return redirectErr(c, "/customers", "invalid customer ID")
	}
	customerID := uint(customerID64)

	policy := models.CustomerCurrencyPolicy(strings.TrimSpace(c.FormValue("policy")))
	if policy != models.CustomerCurrencyPolicySingle && policy != models.CustomerCurrencyPolicyMultiAllowed {
		policy = models.CustomerCurrencyPolicySingle
	}
	if err := services.SetCustomerCurrencyPolicy(s.DB, companyID, customerID, policy); err != nil {
		return redirectErr(c, "/customers/"+strconv.FormatUint(customerID64, 10), "failed to update currency policy")
	}
	return c.Redirect("/customers/"+strconv.FormatUint(customerID64, 10)+"?policy_saved=1", fiber.StatusSeeOther)
}

// handleCustomerCurrencyPolicyAdd adds a currency to the customer's allowed currency list.
func (s *Server) handleCustomerCurrencyPolicyAdd(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	customerID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || customerID64 == 0 {
		return redirectErr(c, "/customers", "invalid customer ID")
	}
	customerID := uint(customerID64)
	idStr := strconv.FormatUint(customerID64, 10)

	code := strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code_manual")))
	if code == "" {
		code = strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
	}
	if len(code) != 3 {
		return redirectErr(c, "/customers/"+idStr, "currency code must be 3 letters")
	}
	if err := services.AddCustomerAllowedCurrency(s.DB, companyID, customerID, code); err != nil {
		return redirectErr(c, "/customers/"+idStr, "could not add currency: "+err.Error())
	}
	return c.Redirect("/customers/"+idStr+"?policy_saved=1", fiber.StatusSeeOther)
}

// handleCustomerCurrencyPolicyRemove removes a currency from the customer's allowed currency list.
func (s *Server) handleCustomerCurrencyPolicyRemove(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	customerID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || customerID64 == 0 {
		return redirectErr(c, "/customers", "invalid customer ID")
	}
	customerID := uint(customerID64)
	idStr := strconv.FormatUint(customerID64, 10)

	code := strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code")))
	if len(code) != 3 {
		return redirectErr(c, "/customers/"+idStr, "invalid currency code")
	}
	if err := services.RemoveCustomerAllowedCurrency(s.DB, companyID, customerID, code); err != nil {
		return redirectErr(c, "/customers/"+idStr, "could not remove currency")
	}
	return c.Redirect("/customers/"+idStr, fiber.StatusSeeOther)
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
	_ = producers.ProjectCustomer(c.Context(), s.DB, s.SearchProjector, companyID, customer.ID)

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
