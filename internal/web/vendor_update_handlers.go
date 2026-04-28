// 遵循project_guide.md
package web

// vendor_update_handlers.go — POST /vendors/:id/update
// Companion to handleVendorCreate (vendors_handlers.go:45). Kept in a separate
// file because the detail page is where edit happens — co-locating edit logic
// with the detail handler keeps the relationship discoverable.

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/searchprojection/producers"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleVendorUpdate updates a vendor's editable profile fields and writes
// an audit entry. Mirrors handleVendorCreate's validation shape: required
// name, case-insensitive uniqueness check per company (excluding self),
// currency discarded when multi-currency is disabled on the company.
//
// POST /vendors/:id/update
func (s *Server) handleVendorUpdate(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vendorID, err := parseVendorIDParam(c)
	if err != nil {
		return redirectErr(c, "/vendors", "invalid vendor ID")
	}

	var vendor models.Vendor
	if err := s.DB.Where("id = ? AND company_id = ?", vendorID, companyID).First(&vendor).Error; err != nil {
		return redirectErr(c, "/vendors", "vendor not found")
	}

	name := strings.TrimSpace(c.FormValue("name"))
	email := strings.TrimSpace(c.FormValue("email"))
	phone := strings.TrimSpace(c.FormValue("phone"))
	address := strings.TrimSpace(c.FormValue("address"))
	currencyCode := strings.TrimSpace(c.FormValue("currency_code"))
	notes := strings.TrimSpace(c.FormValue("notes"))
	paymentTerm := strings.TrimSpace(c.FormValue("payment_term"))

	// Build the VM up-front so validation failures can short-circuit without
	// rebuilding all the context (dropdowns, aggregates).
	buildEditVM := func() pages.VendorDetailVM {
		vm := pages.VendorDetailVM{
			HasCompany:                 true,
			Tab:                        "details",
			Vendor:                     vendor,
			Editing:                    true,
			FormName:                   name,
			FormEmail:                  email,
			FormPhone:                  phone,
			FormAddress:                address,
			FormCurrencyCode:           currencyCode,
			FormNotes:                  notes,
			FormDefaultPaymentTermCode: paymentTerm,
		}
		s.loadVendorEditFormData(companyID, &vm)
		return vm
	}

	if name == "" {
		vm := buildEditVM()
		vm.NameError = "Name is required."
		return pages.VendorDetail(vm).Render(c.Context(), c)
	}

	// Uniqueness check — exclude self so a pure email/address edit doesn't trip.
	var dup int64
	if err := s.DB.Model(&models.Vendor{}).
		Where("company_id = ? AND lower(name) = lower(?) AND id <> ?", companyID, name, vendorID).
		Count(&dup).Error; err != nil {
		vm := buildEditVM()
		vm.FormError = "Could not validate vendor name."
		return pages.VendorDetail(vm).Render(c.Context(), c)
	}
	if dup > 0 {
		vm := buildEditVM()
		vm.NameError = "A vendor with this name already exists for this company."
		return pages.VendorDetail(vm).Render(c.Context(), c)
	}

	// Currency selection is only persisted when the company has multi-currency
	// enabled — same rule as create.
	multiCurrency, _, _ := s.vendorCurrencyInfo(companyID)
	if !multiCurrency {
		currencyCode = ""
	}

	// Persist. Use a map update so zero-value strings (e.g. clearing notes)
	// are written instead of skipped by GORM's default struct-update behaviour.
	updates := map[string]any{
		"name":                      name,
		"email":                     email,
		"phone":                     phone,
		"address":                   address,
		"currency_code":             currencyCode,
		"notes":                     notes,
		"default_payment_term_code": paymentTerm,
	}
	if err := s.DB.Model(&models.Vendor{}).
		Where("id = ? AND company_id = ?", vendorID, companyID).
		Updates(updates).Error; err != nil {
		vm := buildEditVM()
		vm.FormError = "Could not save vendor. Please try again."
		return pages.VendorDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectVendor(c.Context(), s.DB, s.SearchProjector, companyID, vendorID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "vendor.updated", "vendor", vendorID, actor, map[string]any{
		"name":       name,
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/vendors/"+c.Params("id")+"?tab=details&saved=1", fiber.StatusSeeOther)
}
