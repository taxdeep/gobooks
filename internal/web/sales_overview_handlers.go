// 遵循project_guide.md
package web

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleSalesOverview(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	overview, err := services.BuildSalesOverview(s.DB, companyID, time.Now())
	vm := pages.SalesOverviewVM{
		HasCompany: true,
		Overview:   overview,
	}
	if err != nil {
		vm.FormError = "Could not load Sales Overview. Please refresh the page. If this continues, check that invoices, receipts, and customers are available for this company."
	}
	return pages.SalesOverview(vm).Render(c.Context(), c)
}
