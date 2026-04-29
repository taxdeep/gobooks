// 遵循project_guide.md
package web

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleExpenseOverview(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	overview, err := services.BuildExpenseOverview(s.DB, companyID, time.Now())
	vm := pages.ExpenseOverviewVM{
		HasCompany: true,
		Overview:   overview,
	}
	if err != nil {
		vm.FormError = "Could not load Expense Overview. Please refresh the page. If this continues, check that vendors, bills, expenses, and payment records are available for this company."
	}
	return pages.ExpenseOverview(vm).Render(c.Context(), c)
}
