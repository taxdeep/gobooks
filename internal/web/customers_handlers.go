// 遵循产品需求 v1.0
package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleCustomers(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	var customers []models.Customer
	if err := s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&customers).Error; err != nil {
		return pages.Customers(pages.CustomersVM{
			HasCompany: true,
			FormError:  "Could not load customers.",
			Customers:  []models.Customer{},
		}).Render(c.Context(), c)
	}

	return pages.Customers(pages.CustomersVM{
		HasCompany: true,
		Customers:  customers,
		Created:    c.Query("created") == "1",
	}).Render(c.Context(), c)
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

	name := strings.TrimSpace(c.FormValue("name"))
	address := strings.TrimSpace(c.FormValue("address"))
	paymentTerm := strings.TrimSpace(c.FormValue("payment_term"))

	vm := pages.CustomersVM{
		HasCompany:  true,
		Name:        name,
		Address:     address,
		PaymentTerm: paymentTerm,
	}

	if name == "" {
		vm.NameError = "Name is required."
	}

	customers, listErr := s.customersForCompany(companyID)
	if listErr != nil {
		vm.FormError = "Could not load customers."
	} else {
		vm.Customers = customers
	}

	if vm.NameError != "" {
		return pages.Customers(vm).Render(c.Context(), c)
	}

	var count int64
	if err := s.DB.Model(&models.Customer{}).
		Where("company_id = ? AND lower(name) = lower(?)", companyID, name).
		Count(&count).Error; err != nil {
		vm.FormError = "Could not validate customer name."
		return pages.Customers(vm).Render(c.Context(), c)
	}
	if count > 0 {
		vm.NameError = "A customer with this name already exists for this company."
		return pages.Customers(vm).Render(c.Context(), c)
	}

	customer := models.Customer{
		CompanyID:   companyID,
		Name:        name,
		Address:     address,
		PaymentTerm: paymentTerm,
	}
	if err := s.DB.Create(&customer).Error; err != nil {
		vm.FormError = "Could not create customer. Please try again."
		return pages.Customers(vm).Render(c.Context(), c)
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

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/customers?created=1")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/customers?created=1", fiber.StatusSeeOther)
}

func (s *Server) customersForCompany(companyID uint) ([]models.Customer, error) {
	var customers []models.Customer
	err := s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&customers).Error
	return customers, err
}
