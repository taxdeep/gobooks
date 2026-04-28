// 遵循project_guide.md
package web

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleInventoryStock(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	report, err := services.GetStockReport(s.DB, companyID)
	if err != nil {
		report = nil
	}

	return pages.InventoryStock(pages.StockReportVM{
		HasCompany: true,
		Report:     report,
	}).Render(c.Context(), c)
}
