// 遵循project_guide.md
package web

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// handleAPAging renders the AP Aging report page.
func (s *Server) handleAPAging(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	asOfStr := strings.TrimSpace(c.Query("as_of"))
	if asOfStr == "" {
		asOfStr = time.Now().Format("2006-01-02")
	}

	asOf, err := time.Parse("2006-01-02", asOfStr)
	if err != nil {
		return pages.APAging(pages.APAgingVM{
			HasCompany: true,
			AsOf:       asOfStr,
			FormError:  "Invalid date format.",
		}).Render(c.Context(), c)
	}

	report, err := services.GetAPAging(s.DB, companyID, asOf)
	if err != nil {
		return pages.APAging(pages.APAgingVM{
			HasCompany: true,
			AsOf:       asOfStr,
			FormError:  err.Error(),
		}).Render(c.Context(), c)
	}

	return pages.APAging(pages.APAgingVM{
		HasCompany: true,
		AsOf:       asOfStr,
		Report:     report,
	}).Render(c.Context(), c)
}
