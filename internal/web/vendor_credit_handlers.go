// 遵循project_guide.md
package web

// vendor_credit_handlers.go — Vendor-side mirror of customer_credit_handlers.
// Aggregates every open VCN + vendor-refund history for one vendor on a
// single page. Unlike the AR side there is no VendorCredit aggregator model —
// VCNs ARE the credit unit, so the handler reads them directly.
//
// Route:
//   GET /vendors/:id/credits — list VCNs + refunds + link to apply-to-bill

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// handleVendorCredits renders the vendor credits hub page.
// GET /vendors/:id/credits
func (s *Server) handleVendorCredits(c *fiber.Ctx) error {
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

	creditNotes, _ := services.ListVendorCreditNotes(s.DB, companyID, services.VendorCreditNoteListFilter{VendorID: vendorID})
	openBills, _ := services.ListOpenBillsForVendor(s.DB, companyID, vendorID)
	refunds, _ := services.ListVendorRefunds(s.DB, companyID, services.VendorRefundListFilter{VendorID: vendorID})

	// Sum remaining across usable VCNs only (posted + partially_applied);
	// drafts haven't been booked yet and voided/fully_applied have zero left.
	total := decimal.Zero
	for _, cn := range creditNotes {
		if cn.Status == models.VendorCreditNoteStatusPosted ||
			cn.Status == models.VendorCreditNoteStatusPartiallyApplied {
			total = total.Add(cn.RemainingAmount)
		}
	}

	vm := pages.VendorCreditsVM{
		HasCompany:     true,
		Vendor:         vendor,
		CreditNotes:    creditNotes,
		OpenBills:      openBills,
		TotalRemaining: total,
		Refunds:        refunds,
	}
	return pages.VendorCredits(vm).Render(c.Context(), c)
}

// parseVendorIDParam parses :id from a vendor-scoped route into a uint.
func parseVendorIDParam(c *fiber.Ctx) (uint, error) {
	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return 0, err
	}
	return uint(id64), nil
}
