// 遵循project_guide.md
package web

// payment_gateway_settlement_review_handler.go — Batch 13: Settlement Review List.
//
// Two handlers:
//   handleGatewaySettlementReview  GET  /settings/payment-gateways/settlement-review
//   handleGatewaySettlementRetry   POST /settings/payment-gateways/settlement-review/:invoiceID/retry
//
// The retry handler delegates entirely to services.RetryGatewaySettlement — the
// same exact path built in Batch 12. No second settlement execution path exists here.

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleGatewaySettlementReview renders the settlement review list.
// GET /settings/payment-gateways/settlement-review
func (s *Server) handleGatewaySettlementReview(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	filterRaw := strings.TrimSpace(c.Query("filter"))
	filter := services.SettlementReviewPending
	if filterRaw == "all" {
		filter = services.SettlementReviewAll
	}

	rows := services.ListSettlementReviewRows(s.DB, companyID, filter)

	// Count pending + failed for the tab badge — always from DB truth, not from rows slice
	// (rows may be filtered to "all").
	pendingRows := services.ListSettlementReviewRows(s.DB, companyID, services.SettlementReviewPending)

	retryError := strings.TrimSpace(c.Query("error"))

	vm := pages.GatewaySettlementReviewVM{
		HasCompany:           true,
		Rows:                 rows,
		Filter:               string(filter),
		PendingCount:         len(pendingRows),
		JustRetried:          c.Query("retried") == "1",
		RetryStillIneligible: c.Query("stillineligible") == "1",
		RetryError:           retryError,
	}
	return pages.GatewaySettlementReview(vm).Render(c.Context(), c)
}

// handleGatewaySettlementRetry retries settlement for a single invoice from the list.
// POST /settings/payment-gateways/settlement-review/:invoiceID/retry
//
// Auth: RequirePermission(ActionJournalCreate) — same as the invoice-detail retry.
// Delegates to services.RetryGatewaySettlement — exact same path as Batch 12.
// No second settlement execution path is introduced here.
func (s *Server) handleGatewaySettlementRetry(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	invoiceIDRaw := c.Params("invoiceID")
	id64, err := strconv.ParseUint(invoiceIDRaw, 10, 64)
	if err != nil || id64 == 0 {
		return redirectErrToList(c, "Invalid invoice ID.")
	}
	invoiceID := uint(id64)

	result, err := services.RetryGatewaySettlement(s.DB, companyID, invoiceID)
	if err != nil {
		if errors.Is(err, services.ErrNoSucceededAttempt) {
			return redirectErrToList(c, "No verified gateway payment found for this invoice.")
		}
		if errors.Is(err, services.ErrSettlementAlreadyDone) {
			// Already settled — idempotent; surface as success.
			return c.Redirect("/settings/payment-gateways/settlement-review?retried=1", fiber.StatusSeeOther)
		}
		return redirectErrToList(c, "Settlement failed: "+err.Error())
	}

	if !result.Eligibility.Eligible {
		return c.Redirect("/settings/payment-gateways/settlement-review?stillineligible=1", fiber.StatusSeeOther)
	}

	return c.Redirect("/settings/payment-gateways/settlement-review?retried=1", fiber.StatusSeeOther)
}

// redirectErrToList redirects back to the review list with an error query parameter.
func redirectErrToList(c *fiber.Ctx, msg string) error {
	return redirectErr(c, "/settings/payment-gateways/settlement-review", msg)
}
