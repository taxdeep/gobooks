// 遵循project_guide.md
package web

// payment_gateway_dispute_handlers.go — Batch 15: Dispute lifecycle handlers.
//
// Routes (registered in routes.go):
//
//	GET  /settings/payment-gateways/disputes          — list disputes
//	GET  /settings/payment-gateways/disputes/new      — open dispute form
//	POST /settings/payment-gateways/disputes          — create/open dispute
//	GET  /settings/payment-gateways/disputes/:id      — dispute detail
//	POST /settings/payment-gateways/disputes/:id/win  — mark dispute won
//	POST /settings/payment-gateways/disputes/:id/lose — mark dispute lost + create chargeback

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleGatewayDisputeList renders the list of all disputes for the company.
func (s *Server) handleGatewayDisputeList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	disputes, _ := services.ListGatewayDisputes(s.DB, companyID)

	vm := pages.GatewayDisputeListVM{
		HasCompany:  true,
		Disputes:    disputes,
		JustCreated: c.Query("created") == "1",
	}
	return pages.GatewayDisputeList(vm).Render(c.Context(), c)
}

// handleGatewayDisputeNew renders the form for opening a new dispute.
func (s *Server) handleGatewayDisputeNew(c *fiber.Ctx) error {
	_, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	errMsg := strings.TrimSpace(c.Query("error"))
	vm := pages.GatewayDisputeNewVM{
		HasCompany:  true,
		OpenedDate:  time.Now().Format("2006-01-02"),
		ErrorMsg:    errMsg,
	}
	return pages.GatewayDisputeNew(vm).Render(c.Context(), c)
}

// handleGatewayDisputeCreate processes the open-dispute form.
// POST /settings/payment-gateways/disputes
func (s *Server) handleGatewayDisputeCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	redirectNewErr := func(msg string) error {
		q := url.Values{}
		q.Set("error", msg)
		return c.Redirect("/settings/payment-gateways/disputes/new?"+q.Encode(), fiber.StatusSeeOther)
	}

	// ── Parse form ────────────────────────────────────────────────────────────
	gwIDRaw := strings.TrimSpace(c.FormValue("gateway_account_id"))
	chargeTxnIDRaw := strings.TrimSpace(c.FormValue("payment_transaction_id"))
	providerDisputeID := strings.TrimSpace(c.FormValue("provider_dispute_id"))
	amountRaw := strings.TrimSpace(c.FormValue("amount"))
	currencyCode := strings.TrimSpace(c.FormValue("currency_code"))
	openedDateRaw := strings.TrimSpace(c.FormValue("opened_date"))

	gwID64, err := strconv.ParseUint(gwIDRaw, 10, 64)
	if err != nil || gwID64 == 0 {
		return redirectNewErr("Gateway account is required.")
	}

	chargeTxnID64, err := strconv.ParseUint(chargeTxnIDRaw, 10, 64)
	if err != nil || chargeTxnID64 == 0 {
		return redirectNewErr("Original charge transaction ID is required.")
	}

	if providerDisputeID == "" {
		return redirectNewErr("Provider Dispute ID is required.")
	}

	amount, err := decimal.NewFromString(amountRaw)
	if err != nil || !amount.IsPositive() {
		return redirectNewErr("Amount must be a positive number.")
	}

	openedAt := time.Now()
	if openedDateRaw != "" {
		if t, err := time.Parse("2006-01-02", openedDateRaw); err == nil {
			openedAt = t
		}
	}

	// ── Call service ──────────────────────────────────────────────────────────
	inp := services.OpenDisputeInput{
		CompanyID:            companyID,
		GatewayAccountID:     uint(gwID64),
		PaymentTransactionID: uint(chargeTxnID64),
		ProviderDisputeID:    providerDisputeID,
		Amount:               amount,
		CurrencyCode:         currencyCode,
		OpenedAt:             openedAt,
	}
	dispute, err := services.OpenGatewayDispute(s.DB, inp)
	if err != nil {
		return redirectNewErr(disputeErrMessage(err))
	}

	return c.Redirect(
		fmt.Sprintf("/settings/payment-gateways/disputes/%d?created=1", dispute.ID),
		fiber.StatusSeeOther,
	)
}

// handleGatewayDisputeDetail renders the detail page for a single dispute.
func (s *Server) handleGatewayDisputeDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := c.Params("id")
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/payment-gateways/disputes", fiber.StatusSeeOther)
	}

	dispute, err := services.GetGatewayDisputeByID(s.DB, companyID, uint(id64))
	if err != nil {
		return c.Redirect("/settings/payment-gateways/disputes", fiber.StatusSeeOther)
	}

	vm := pages.GatewayDisputeDetailVM{
		HasCompany:  true,
		Dispute:     dispute,
		JustCreated: c.Query("created") == "1",
	}
	return pages.GatewayDisputeDetail(vm).Render(c.Context(), c)
}

// handleGatewayDisputeWin marks a dispute as won.
// POST /settings/payment-gateways/disputes/:id/win
func (s *Server) handleGatewayDisputeWin(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id64, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/payment-gateways/disputes", fiber.StatusSeeOther)
	}

	if _, err := services.WinGatewayDispute(s.DB, companyID, uint(id64)); err != nil {
		return redirectErr(c, fmt.Sprintf("/settings/payment-gateways/disputes/%d", id64), err.Error())
	}
	return c.Redirect(
		fmt.Sprintf("/settings/payment-gateways/disputes/%d", id64),
		fiber.StatusSeeOther,
	)
}

// handleGatewayDisputeLose marks a dispute as lost and creates a chargeback transaction.
// POST /settings/payment-gateways/disputes/:id/lose
func (s *Server) handleGatewayDisputeLose(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id64, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/payment-gateways/disputes", fiber.StatusSeeOther)
	}

	if _, _, err := services.LoseGatewayDispute(s.DB, companyID, uint(id64)); err != nil {
		return redirectErr(c, fmt.Sprintf("/settings/payment-gateways/disputes/%d", id64), err.Error())
	}
	return c.Redirect(
		fmt.Sprintf("/settings/payment-gateways/disputes/%d", id64),
		fiber.StatusSeeOther,
	)
}

// disputeErrMessage maps service sentinel errors to user-facing messages.
func disputeErrMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrDisputeDuplicate):
		return "A dispute with this Provider Dispute ID already exists."
	case errors.Is(err, services.ErrDisputeChargeNotFound):
		return "Charge transaction not found or does not belong to this company."
	case errors.Is(err, services.ErrDisputeChargeNotPosted):
		return "The charge transaction must be posted before opening a dispute."
	case errors.Is(err, services.ErrDisputeWrongOriginalTxnType):
		return "Only charge or capture transactions can be disputed."
	case errors.Is(err, services.ErrDisputeGatewayMismatch):
		return "The charge transaction belongs to a different gateway account."
	case errors.Is(err, services.ErrDisputeAmountInvalid):
		return "Dispute amount must be positive."
	case errors.Is(err, services.ErrDisputeProviderIDEmpty):
		return "Provider Dispute ID is required."
	case errors.Is(err, services.ErrPayoutGatewayAccountInvalid):
		return "Gateway account not found or does not belong to this company."
	case errors.Is(err, services.ErrDisputeAlreadyResolved):
		return "This dispute has already been resolved."
	case errors.Is(err, services.ErrDisputeInvalidTransition):
		return "Invalid dispute status transition."
	default:
		return "Failed to process dispute: " + err.Error()
	}
}
