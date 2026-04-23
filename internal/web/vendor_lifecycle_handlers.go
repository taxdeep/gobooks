// 遵循project_guide.md
package web

// vendor_lifecycle_handlers.go — AP mirror of customer_lifecycle_handlers.
// See that file for the overall rule (delete vs deactivate based on
// presence of referencing AP documents).

import (
	"github.com/gofiber/fiber/v2"

	"gobooks/internal/searchprojection/producers"
	"gobooks/internal/services"
)

// POST /vendors/:id/delete
func (s *Server) handleVendorDelete(c *fiber.Ctx) error {
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

	if err := services.DeleteVendor(s.DB, companyID, vendorID); err != nil {
		if err == services.ErrPartyHasRecords {
			return redirectErr(c,
				"/vendors/"+c.Params("id"),
				"This vendor now has related records and can no longer be deleted. Deactivate it instead.")
		}
		return redirectErr(c, "/vendors/"+c.Params("id"), "Could not delete vendor.")
	}
	_ = producers.DeleteVendorProjection(c.Context(), s.SearchProjector, companyID, vendorID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "vendor.deleted", "vendor", vendorID, actor, map[string]any{
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/vendors?deleted=1", fiber.StatusSeeOther)
}

// POST /vendors/:id/deactivate
func (s *Server) handleVendorDeactivate(c *fiber.Ctx) error {
	return setVendorActiveAndRedirect(s, c, false, "vendor.deactivated", "deactivated")
}

// POST /vendors/:id/reactivate
func (s *Server) handleVendorReactivate(c *fiber.Ctx) error {
	return setVendorActiveAndRedirect(s, c, true, "vendor.reactivated", "reactivated")
}

func setVendorActiveAndRedirect(s *Server, c *fiber.Ctx, active bool, auditAction, queryFlag string) error {
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

	if err := services.SetVendorActive(s.DB, companyID, vendorID, active); err != nil {
		return redirectErr(c, "/vendors/"+c.Params("id"), "Could not update vendor status.")
	}
	_ = producers.ProjectVendor(c.Context(), s.DB, s.SearchProjector, companyID, vendorID)

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, auditAction, "vendor", vendorID, actor, map[string]any{
		"company_id": companyID,
	}, &cid, &uid)
	s.SPAcceleration.InvalidateCompany(companyID)

	return c.Redirect("/vendors/"+c.Params("id")+"?"+queryFlag+"=1", fiber.StatusSeeOther)
}
