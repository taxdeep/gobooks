// 遵循project_guide.md
package web

// customer_lifecycle_handlers.go — delete / deactivate / reactivate actions
// triggered from the customer detail page. The rule (enforced in
// services.DeleteCustomer) is: delete is allowed only when no AR document
// references the customer; otherwise deactivation is the only option.

import (
	"github.com/gofiber/fiber/v2"

	"gobooks/internal/searchprojection/producers"
	"gobooks/internal/services"
)

// POST /customers/:id/delete
func (s *Server) handleCustomerDelete(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customerID, err := parseCustomerIDParam(c)
	if err != nil {
		return redirectErr(c, "/customers", "invalid customer ID")
	}

	if err := services.DeleteCustomer(s.DB, companyID, customerID); err != nil {
		if err == services.ErrPartyHasRecords {
			// Race: a document was added between the UI render and this POST.
			// Bounce back to the detail page with a clear message so the user
			// can deactivate instead.
			return redirectErr(c,
				"/customers/"+c.Params("id"),
				"This customer now has related records and can no longer be deleted. Deactivate it instead.")
		}
		return redirectErr(c, "/customers/"+c.Params("id"), "Could not delete customer.")
	}
	_ = producers.DeleteCustomerProjection(c.Context(), s.SearchProjector, companyID, customerID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "customer.deleted", "customer", customerID, actor, map[string]any{
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/customers?deleted=1", fiber.StatusSeeOther)
}

// POST /customers/:id/deactivate
func (s *Server) handleCustomerDeactivate(c *fiber.Ctx) error {
	return setCustomerActiveAndRedirect(s, c, false, "customer.deactivated", "deactivated")
}

// POST /customers/:id/reactivate
func (s *Server) handleCustomerReactivate(c *fiber.Ctx) error {
	return setCustomerActiveAndRedirect(s, c, true, "customer.reactivated", "reactivated")
}

func setCustomerActiveAndRedirect(s *Server, c *fiber.Ctx, active bool, auditAction, queryFlag string) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customerID, err := parseCustomerIDParam(c)
	if err != nil {
		return redirectErr(c, "/customers", "invalid customer ID")
	}

	if err := services.SetCustomerActive(s.DB, companyID, customerID, active); err != nil {
		return redirectErr(c, "/customers/"+c.Params("id"), "Could not update customer status.")
	}
	// Re-project so the row's status flips (active/inactive) in search.
	_ = producers.ProjectCustomer(c.Context(), s.DB, s.SearchProjector, companyID, customerID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, auditAction, "customer", customerID, actor, map[string]any{
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/customers/"+c.Params("id")+"?"+queryFlag+"=1", fiber.StatusSeeOther)
}
