// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handlePaymentTermsGet renders Settings > Company > Payment Terms.
func (s *Server) handlePaymentTermsGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vm := pages.PaymentTermsVM{
		HasCompany: true,
		Breadcrumb: breadcrumbSettingsCompanyPaymentTerms(),
		Created:    c.Query("created") == "1",
		Updated:    c.Query("updated") == "1",
		Deleted:    c.Query("deleted") == "1",
		DefaultSet: c.Query("default") == "1",
		Toggled:    c.Query("toggled") == "1",
	}

	// Load all terms for this company.
	terms, err := services.ListPaymentTerms(s.DB, companyID, false)
	if err != nil {
		vm.FormError = "Could not load payment terms."
	} else {
		vm.Items = terms
	}

	// Open create drawer.
	if c.Query("new") == "1" {
		vm.DrawerOpen = true
		vm.DrawerMode = "create"
	}

	// Open edit drawer.
	if editRaw := strings.TrimSpace(c.Query("edit")); editRaw != "" {
		if id64, err := strconv.ParseUint(editRaw, 10, 64); err == nil && id64 > 0 {
			var pt models.PaymentTerm
			if err := s.DB.Where("id = ? AND company_id = ?", uint(id64), companyID).First(&pt).Error; err == nil {
				vm.DrawerOpen = true
				vm.DrawerMode = "edit"
				vm.EditingID = uint(id64)
				vm.Code = pt.Code
				vm.Description = pt.Description
				vm.NetDays = strconv.Itoa(pt.NetDays)
				vm.DiscountPct = pt.DiscountPct.StringFixed(2)
				if pt.DiscountPct.IsZero() {
					vm.DiscountPct = ""
				}
				vm.DiscountDays = strconv.Itoa(pt.DiscountDays)
				if pt.DiscountDays == 0 {
					vm.DiscountDays = ""
				}
				vm.SortOrder = strconv.Itoa(pt.SortOrder)
			}
		}
	}

	return pages.CompanyPaymentTerms(vm).Render(c.Context(), c)
}

// handlePaymentTermCreate processes POST /settings/company/payment-terms.
func (s *Server) handlePaymentTermCreate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	code := strings.TrimSpace(c.FormValue("code"))
	description := strings.TrimSpace(c.FormValue("description"))
	netDaysRaw := strings.TrimSpace(c.FormValue("net_days"))
	discountPctRaw := strings.TrimSpace(c.FormValue("discount_pct"))
	discountDaysRaw := strings.TrimSpace(c.FormValue("discount_days"))
	sortOrderRaw := strings.TrimSpace(c.FormValue("sort_order"))
	isDefault := c.FormValue("is_default") == "1"

	vm := s.ptBaseVM(companyID, "create")
	vm.Code = code
	vm.Description = description
	vm.NetDays = netDaysRaw
	vm.DiscountPct = discountPctRaw
	vm.DiscountDays = discountDaysRaw
	vm.SortOrder = sortOrderRaw
	vm.IsDefault = isDefault
	vm.DrawerOpen = true

	netDays, _ := strconv.Atoi(netDaysRaw)
	discountDays, _ := strconv.Atoi(discountDaysRaw)
	sortOrder, _ := strconv.Atoi(sortOrderRaw)
	discountPct := decimal.Zero
	if discountPctRaw != "" {
		discountPct, _ = decimal.NewFromString(discountPctRaw)
	}

	pt, err := services.CreatePaymentTerm(s.DB, services.CreatePaymentTermInput{
		CompanyID:    companyID,
		Code:         code,
		Description:  description,
		NetDays:      netDays,
		DiscountPct:  discountPct,
		DiscountDays: discountDays,
		IsDefault:    isDefault,
		SortOrder:    sortOrder,
	})
	if err != nil {
		vm.FormError = err.Error()
		return pages.CompanyPaymentTerms(vm).Render(c.Context(), c)
	}

	actor := "user"
	if user.Email != "" {
		actor = user.Email
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "settings.payment_term.created", "payment_term", pt.ID, actor, map[string]any{
		"code":       pt.Code,
		"company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/settings/company/payment-terms?created=1", fiber.StatusSeeOther)
}

// handlePaymentTermUpdate processes POST /settings/company/payment-terms/update.
func (s *Server) handlePaymentTermUpdate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.FormValue("payment_term_id"))
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/company/payment-terms", fiber.StatusSeeOther)
	}
	termID := uint(id64)

	description := strings.TrimSpace(c.FormValue("description"))
	netDaysRaw := strings.TrimSpace(c.FormValue("net_days"))
	discountPctRaw := strings.TrimSpace(c.FormValue("discount_pct"))
	discountDaysRaw := strings.TrimSpace(c.FormValue("discount_days"))
	sortOrderRaw := strings.TrimSpace(c.FormValue("sort_order"))

	vm := s.ptBaseVM(companyID, "edit")
	vm.EditingID = termID
	vm.Description = description
	vm.NetDays = netDaysRaw
	vm.DiscountPct = discountPctRaw
	vm.DiscountDays = discountDaysRaw
	vm.SortOrder = sortOrderRaw
	vm.DrawerOpen = true

	// Read the code for display in the drawer.
	var existing models.PaymentTerm
	if dbErr := s.DB.Where("id = ? AND company_id = ?", termID, companyID).First(&existing).Error; dbErr == nil {
		vm.Code = existing.Code
	}

	netDays, _ := strconv.Atoi(netDaysRaw)
	discountDays, _ := strconv.Atoi(discountDaysRaw)
	sortOrder, _ := strconv.Atoi(sortOrderRaw)
	discountPct := decimal.Zero
	if discountPctRaw != "" {
		discountPct, _ = decimal.NewFromString(discountPctRaw)
	}

	_, err = services.UpdatePaymentTerm(s.DB, companyID, termID, services.UpdatePaymentTermInput{
		Description:  description,
		NetDays:      netDays,
		DiscountPct:  discountPct,
		DiscountDays: discountDays,
		SortOrder:    sortOrder,
	})
	if err != nil {
		vm.FormError = err.Error()
		return pages.CompanyPaymentTerms(vm).Render(c.Context(), c)
	}

	actor := "user"
	if user.Email != "" {
		actor = user.Email
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "settings.payment_term.updated", "payment_term", termID, actor, map[string]any{
		"company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/settings/company/payment-terms?updated=1", fiber.StatusSeeOther)
}

// handlePaymentTermSetDefault processes POST /settings/company/payment-terms/set-default.
func (s *Server) handlePaymentTermSetDefault(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	idRaw := strings.TrimSpace(c.FormValue("payment_term_id"))
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/company/payment-terms", fiber.StatusSeeOther)
	}
	if err := services.SetDefaultPaymentTerm(s.DB, companyID, uint(id64)); err != nil {
		return c.Redirect("/settings/company/payment-terms?error=1", fiber.StatusSeeOther)
	}
	return c.Redirect("/settings/company/payment-terms?default=1", fiber.StatusSeeOther)
}

// handlePaymentTermToggle processes POST /settings/company/payment-terms/toggle.
func (s *Server) handlePaymentTermToggle(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	idRaw := strings.TrimSpace(c.FormValue("payment_term_id"))
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/company/payment-terms", fiber.StatusSeeOther)
	}
	if _, err := services.TogglePaymentTermActive(s.DB, companyID, uint(id64)); err != nil {
		return c.Redirect("/settings/company/payment-terms?error=1", fiber.StatusSeeOther)
	}
	return c.Redirect("/settings/company/payment-terms?toggled=1", fiber.StatusSeeOther)
}

// handlePaymentTermDelete processes POST /settings/company/payment-terms/delete.
func (s *Server) handlePaymentTermDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	idRaw := strings.TrimSpace(c.FormValue("payment_term_id"))
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/company/payment-terms", fiber.StatusSeeOther)
	}
	if err := services.DeletePaymentTerm(s.DB, companyID, uint(id64)); err != nil {
		// Show the error on the list page.
		vm := s.ptBaseVM(companyID, "")
		vm.FormError = err.Error()
		return pages.CompanyPaymentTerms(vm).Render(c.Context(), c)
	}
	return c.Redirect("/settings/company/payment-terms?deleted=1", fiber.StatusSeeOther)
}

// ptBaseVM builds a base VM with items loaded from DB.
func (s *Server) ptBaseVM(companyID uint, drawerMode string) pages.PaymentTermsVM {
	vm := pages.PaymentTermsVM{
		HasCompany: true,
		Breadcrumb: breadcrumbSettingsCompanyPaymentTerms(),
		DrawerMode: drawerMode,
	}
	terms, _ := services.ListPaymentTerms(s.DB, companyID, false)
	vm.Items = terms
	return vm
}
