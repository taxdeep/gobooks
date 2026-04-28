// 遵循project_guide.md
package web

// payout_component_handlers.go — Batch 19: Payout component management handlers.
//
// Routes (registered in routes.go):
//
//   POST /settings/payment-gateways/payouts/:id/components
//          — add a composition component to an unmatched payout
//   POST /settings/payment-gateways/payouts/:id/components/:cid/delete
//          — remove a component from an unmatched payout

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

// handlePayoutComponentAdd processes the add-component form on the payout detail page.
// POST /settings/payment-gateways/payouts/:id/components
func (s *Server) handlePayoutComponentAdd(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	payoutID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || payoutID64 == 0 {
		return c.Redirect("/settings/payment-gateways/payouts", fiber.StatusSeeOther)
	}
	detailBase := "/settings/payment-gateways/payouts/" + c.Params("id")

	componentTypeRaw := strings.TrimSpace(c.FormValue("component_type"))
	directionRaw := strings.TrimSpace(c.FormValue("direction"))
	amountRaw := strings.TrimSpace(c.FormValue("amount"))
	description := strings.TrimSpace(c.FormValue("description"))

	if componentTypeRaw == "" {
		return redirectWithQuery(c, detailBase, "component_error", "Component type is required.")
	}
	if directionRaw == "" {
		return redirectWithQuery(c, detailBase, "component_error", "Direction is required.")
	}
	if amountRaw == "" {
		return redirectWithQuery(c, detailBase, "component_error", "Amount is required.")
	}
	amount, err := decimal.NewFromString(amountRaw)
	if err != nil || !amount.IsPositive() {
		return redirectWithQuery(c, detailBase, "component_error", "Amount must be a positive number.")
	}

	inp := services.AddGatewayPayoutComponentInput{
		CompanyID:       companyID,
		GatewayPayoutID: uint(payoutID64),
		ComponentType:   models.PayoutComponentType(componentTypeRaw),
		Direction:       models.PayoutComponentDirection(directionRaw),
		Amount:          amount,
		Description:     description,
		Actor:           payoutComponentActor(c),
	}

	if _, err := services.AddGatewayPayoutComponent(s.DB, inp); err != nil {
		return redirectWithQuery(c, detailBase, "component_error", componentErrMessage(err))
	}
	return redirectWithQuery(c, detailBase, "component_added", "1")
}

// handlePayoutComponentDelete processes the remove-component form.
// POST /settings/payment-gateways/payouts/:id/components/:cid/delete
func (s *Server) handlePayoutComponentDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	payoutID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || payoutID64 == 0 {
		return c.Redirect("/settings/payment-gateways/payouts", fiber.StatusSeeOther)
	}
	cidRaw := strings.TrimSpace(c.Params("cid"))
	cid64, err := strconv.ParseUint(cidRaw, 10, 64)
	if err != nil || cid64 == 0 {
		return c.Redirect("/settings/payment-gateways/payouts/"+c.Params("id"), fiber.StatusSeeOther)
	}
	detailBase := "/settings/payment-gateways/payouts/" + c.Params("id")

	if err := services.DeleteGatewayPayoutComponentWithActor(s.DB, companyID, uint(payoutID64), uint(cid64), payoutComponentActor(c)); err != nil {
		return redirectWithQuery(c, detailBase, "component_error", componentErrMessage(err))
	}
	return redirectWithQuery(c, detailBase, "component_deleted", "1")
}

// componentErrMessage translates service sentinel errors into user-facing messages.
func componentErrMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrComponentPayoutNotFound):
		return "Gateway payout not found."
	case errors.Is(err, services.ErrComponentPayoutAlreadyMatched):
		return "This payout is already matched to a bank entry. Components cannot be modified."
	case errors.Is(err, services.ErrComponentTypeInvalid):
		return "Unsupported component type."
	case errors.Is(err, services.ErrComponentDirectionInvalid):
		return err.Error()
	case errors.Is(err, services.ErrComponentAmountInvalid):
		return "Component amount must be positive."
	case errors.Is(err, services.ErrComponentDuplicate):
		return "An identical component line already exists on this payout."
	case errors.Is(err, services.ErrComponentNotFound):
		return "Component not found."
	default:
		return "Operation failed: " + err.Error()
	}
}

// redirectWithQuery redirects to base URL with a single query param.
func redirectWithQuery(c *fiber.Ctx, base, key, value string) error {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return c.Redirect(base+sep+key+"="+url.QueryEscape(value), fiber.StatusSeeOther)
}

func payoutComponentActor(c *fiber.Ctx) string {
	user := UserFromCtx(c)
	if user != nil && strings.TrimSpace(user.Email) != "" {
		return strings.TrimSpace(user.Email)
	}
	return "system"
}
