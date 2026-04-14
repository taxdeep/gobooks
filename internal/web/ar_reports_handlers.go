// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── Customer Statement ────────────────────────────────────────────────────────

func (s *Server) handleCustomerStatement(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customers, _ := s.customersForCompany(companyID)
	customerIDStr := strings.TrimSpace(c.Query("customer_id"))
	fromStr := strings.TrimSpace(c.Query("from"))
	toStr := strings.TrimSpace(c.Query("to"))

	vm := pages.CustomerStatementVM{
		HasCompany: true,
		Customers:  customers,
		CustomerID: customerIDStr,
		FromDate:   fromStr,
		ToDate:     toStr,
	}

	if customerIDStr != "" && fromStr != "" && toStr != "" {
		customerID, err := strconv.ParseUint(customerIDStr, 10, 64)
		if err != nil {
			vm.FormError = "Invalid customer."
		} else {
			fromDate, err := time.Parse("2006-01-02", fromStr)
			if err != nil {
				vm.FormError = "Invalid from date."
			} else {
				toDate, err := time.Parse("2006-01-02", toStr)
				if err != nil {
					vm.FormError = "Invalid to date."
				} else {
					stmt, err := services.GetCustomerStatement(s.DB, companyID, uint(customerID), fromDate, toDate)
					if err != nil {
						vm.FormError = "Error generating statement: " + err.Error()
					} else {
						vm.Statement = stmt
					}
				}
			}
		}
	}

	return pages.CustomerStatement(vm).Render(c.Context(), c)
}
