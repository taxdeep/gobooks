// 遵循project_guide.md
package web

// payment_gateway_payout_handlers.go — Batch 14: Gateway Payout Bridge handlers.
//
// Routes (registered in routes.go):
//
//	GET  /settings/payment-gateways/payouts          — list payouts
//	GET  /settings/payment-gateways/payouts/new      — new payout form
//	POST /settings/payment-gateways/payouts          — create payout (submit form)
//	GET  /settings/payment-gateways/payouts/:id      — payout detail

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

// handleGatewayPayoutList renders the list of all payouts for the company.
// GET /settings/payment-gateways/payouts
func (s *Server) handleGatewayPayoutList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	payouts, _ := services.ListGatewayPayouts(s.DB, companyID)

	vm := pages.GatewayPayoutsListVM{
		HasCompany:  true,
		Payouts:     payouts,
		JustCreated: c.Query("created") == "1",
	}
	return pages.GatewayPayoutsList(vm).Render(c.Context(), c)
}

// handleGatewayPayoutNew renders the form for creating a new payout bridge.
// GET /settings/payment-gateways/payouts/new
func (s *Server) handleGatewayPayoutNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// Load gateway accounts for the dropdown.
	var gateways []pages.GatewaySelectItem
	s.DB.Table("payment_gateway_accounts").
		Select("id, display_name, provider_type").
		Where("company_id = ? AND is_active = true", companyID).
		Order("display_name asc").
		Scan(&gateways)

	// Parse optional gateway filter from query.
	gwIDRaw := strings.TrimSpace(c.Query("gateway_account_id"))

	// Load unbridged settlements.
	var gwID uint
	if id64, err := strconv.ParseUint(gwIDRaw, 10, 64); err == nil && id64 > 0 {
		gwID = uint(id64)
	}
	settlements, _ := services.UnbridgedSettlements(s.DB, companyID, gwID)

	// Bank accounts.
	bankAccounts, _ := s.bankAccountsForCompany(companyID)

	errMsg := strings.TrimSpace(c.Query("error"))

	vm := pages.GatewayPayoutNewVM{
		HasCompany:         true,
		Gateways:           gateways,
		Settlements:        settlements,
		BankAccounts:       bankAccounts,
		GatewayAccountID:   gwIDRaw,
		PayoutDate:         time.Now().Format("2006-01-02"),
		ErrorMsg:           errMsg,
	}
	return pages.GatewayPayoutNew(vm).Render(c.Context(), c)
}

// handleGatewayPayoutCreate processes the payout creation form.
// POST /settings/payment-gateways/payouts
func (s *Server) handleGatewayPayoutCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// ── Parse form fields ────────────────────────────────────────────────────
	gwIDRaw := strings.TrimSpace(c.FormValue("gateway_account_id"))
	providerPayoutID := strings.TrimSpace(c.FormValue("provider_payout_id"))
	payoutDateRaw := strings.TrimSpace(c.FormValue("payout_date"))
	feeAmountRaw := strings.TrimSpace(c.FormValue("fee_amount"))
	bankIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	settlementIDsRaw := c.Request().PostArgs().PeekMulti("settlement_ids[]")

	redirectNewWithErr := func(msg string) error {
		q := url.Values{}
		q.Set("error", msg)
		if gwIDRaw != "" {
			q.Set("gateway_account_id", gwIDRaw)
		}
		return c.Redirect("/settings/payment-gateways/payouts/new?"+q.Encode(), fiber.StatusSeeOther)
	}

	// ── Validate: gateway account ID ─────────────────────────────────────────
	gwID64, err := strconv.ParseUint(gwIDRaw, 10, 64)
	if err != nil || gwID64 == 0 {
		return redirectNewWithErr("Gateway account is required.")
	}

	// ── Validate: provider payout ID ─────────────────────────────────────────
	if providerPayoutID == "" {
		return redirectNewWithErr("Provider Payout ID is required.")
	}

	// ── Validate: payout date ────────────────────────────────────────────────
	payoutDate, err := time.Parse("2006-01-02", payoutDateRaw)
	if err != nil {
		return redirectNewWithErr("Payout date is invalid (expected YYYY-MM-DD).")
	}

	// ── Validate: fee amount ─────────────────────────────────────────────────
	feeAmount := decimal.Zero
	if feeAmountRaw != "" {
		feeAmount, err = decimal.NewFromString(feeAmountRaw)
		if err != nil || feeAmount.IsNegative() {
			return redirectNewWithErr("Fee amount must be a non-negative number.")
		}
	}

	// ── Validate: bank account ID ─────────────────────────────────────────────
	bankID64, err := strconv.ParseUint(bankIDRaw, 10, 64)
	if err != nil || bankID64 == 0 {
		return redirectNewWithErr("Bank account is required.")
	}

	// ── Validate: settlement IDs ─────────────────────────────────────────────
	if len(settlementIDsRaw) == 0 {
		return redirectNewWithErr("Select at least one settlement to bridge.")
	}
	settlementIDs := make([]uint, 0, len(settlementIDsRaw))
	for _, raw := range settlementIDsRaw {
		id64, err := strconv.ParseUint(string(raw), 10, 64)
		if err != nil || id64 == 0 {
			return redirectNewWithErr("Invalid settlement ID in selection.")
		}
		settlementIDs = append(settlementIDs, uint(id64))
	}

	// ── Call service ─────────────────────────────────────────────────────────
	inp := services.CreateGatewayPayoutInput{
		CompanyID:        companyID,
		GatewayAccountID: uint(gwID64),
		ProviderPayoutID: providerPayoutID,
		PayoutDate:       payoutDate,
		FeeAmount:        feeAmount,
		BankAccountID:    uint(bankID64),
		SettlementIDs:    settlementIDs,
	}

	result, err := services.CreateGatewayPayout(s.DB, inp)
	if err != nil {
		msg := payoutErrMessage(err)
		return redirectNewWithErr(msg)
	}

	return c.Redirect(
		fmt.Sprintf("/settings/payment-gateways/payouts/%d", result.Payout.ID),
		fiber.StatusSeeOther,
	)
}

// handleGatewayPayoutDetail renders the detail page for a single payout.
// GET /settings/payment-gateways/payouts/:id
func (s *Server) handleGatewayPayoutDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := c.Params("id")
	id64, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/payment-gateways/payouts", fiber.StatusSeeOther)
	}

	payout, err := services.GetGatewayPayoutByID(s.DB, companyID, uint(id64))
	if err != nil {
		return c.Redirect("/settings/payment-gateways/payouts", fiber.StatusSeeOther)
	}

	linkedSettlements, _ := services.ListGatewayPayoutSettlements(s.DB, companyID, payout.ID)

	// Batch 19: load components and compute expected net.
	components, _ := services.ListGatewayPayoutComponents(s.DB, companyID, payout.ID)
	expectedNet, _ := services.ComputeGatewayPayoutExpectedNet(s.DB, companyID, payout)

	// Reconciliation state.
	reconciliation, _ := services.GetPayoutReconciliation(s.DB, companyID, payout.ID)

	vm := pages.GatewayPayoutDetailVM{
		HasCompany:           true,
		Payout:               payout,
		Settlements:          linkedSettlements,
		Components:           components,
		ExpectedNet:          expectedNet,
		Reconciliation:       reconciliation,
		JustAddedComponent:   c.Query("component_added") == "1",
		JustDeletedComponent: c.Query("component_deleted") == "1",
		ComponentFormError:   strings.TrimSpace(c.Query("component_error")),
	}
	return pages.GatewayPayoutDetail(vm).Render(c.Context(), c)
}

// payoutErrMessage maps service sentinel errors to user-facing messages.
func payoutErrMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrPayoutGatewayAccountInvalid):
		return "Gateway account not found or does not belong to this company."
	case errors.Is(err, services.ErrPayoutDuplicate):
		return "A payout with this Provider Payout ID already exists."
	case errors.Is(err, services.ErrPayoutSettlementAlreadyBridged):
		return "One or more selected settlements are already linked to a payout."
	case errors.Is(err, services.ErrPayoutSettlementNotFound):
		return "One or more selected settlements could not be found."
	case errors.Is(err, services.ErrPayoutSettlementGatewayMismatch):
		return "All settlements must belong to the selected gateway account."
	case errors.Is(err, services.ErrPayoutSettlementCurrencyMismatch):
		return "All settlements must share the same currency."
	case errors.Is(err, services.ErrPayoutBankAccountInvalid):
		return "Bank account not found or does not belong to this company."
	case errors.Is(err, services.ErrPayoutBankAccountInactive):
		return "Bank account is inactive."
	case errors.Is(err, services.ErrPayoutBankAccountNotAsset):
		return "Selected account must be an asset (bank) account."
	case errors.Is(err, services.ErrPayoutNoClearingAccount):
		return "Gateway clearing account is not configured in the accounting mapping."
	case errors.Is(err, services.ErrPayoutNoFeeExpenseAccount):
		return "Gateway fee expense account is not configured (required when fee > 0)."
	case errors.Is(err, services.ErrPayoutFeeExceedsGross):
		return "Fee amount exceeds the total gross amount of selected settlements."
	case errors.Is(err, services.ErrPayoutGrossZero):
		return "Total settlement amount must be positive."
	default:
		return "Failed to create payout: " + err.Error()
	}
}
