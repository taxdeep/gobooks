// 遵循产品需求 v1.0
package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleVendors(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	var vendors []models.Vendor
	if err := s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vendors).Error; err != nil {
		return pages.Vendors(pages.VendorsVM{
			HasCompany: true,
			FormError:  "Could not load vendors.",
			Vendors:    []models.Vendor{},
		}).Render(c.Context(), c)
	}

	return pages.Vendors(pages.VendorsVM{
		HasCompany: true,
		Vendors:    vendors,
		Created:    c.Query("created") == "1",
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

	name := strings.TrimSpace(c.FormValue("name"))
	address := strings.TrimSpace(c.FormValue("address"))
	paymentTerm := strings.TrimSpace(c.FormValue("payment_term"))

	vm := pages.VendorsVM{
		HasCompany:  true,
		Name:        name,
		Address:     address,
		PaymentTerm: paymentTerm,
	}

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

	vendor := models.Vendor{
		CompanyID:   companyID,
		Name:        name,
		Address:     address,
		PaymentTerm: paymentTerm,
	}
	if err := s.DB.Create(&vendor).Error; err != nil {
		vm.FormError = "Could not create vendor. Please try again."
		return pages.Vendors(vm).Render(c.Context(), c)
	}

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

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/vendors?created=1")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/vendors?created=1", fiber.StatusSeeOther)
}

func (s *Server) vendorsForCompany(companyID uint) ([]models.Vendor, error) {
	var vendors []models.Vendor
	err := s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vendors).Error
	return vendors, err
}
