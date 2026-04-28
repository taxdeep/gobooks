// 遵循project_guide.md
package web

// payout_reconciliation_handlers.go — Batch 18: Payout ↔ bank entry matching.
//
// Routes (registered in routes.go):
//
//   GET  /settings/payment-gateways/payout-reconciliation
//          — reconciliation overview (unmatched payouts, unmatched entries, matched)
//   POST /settings/payment-gateways/payout-reconciliation/bank-entries
//          — create a manually-entered bank deposit
//   GET  /settings/payment-gateways/payouts/:id/reconcile
//          — per-payout match form (shows candidate bank entries)
//   POST /settings/payment-gateways/payouts/:id/reconcile
//          — submit the 1:1 match

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handlePayoutReconciliationOverview renders the reconciliation overview page.
// GET /settings/payment-gateways/payout-reconciliation
func (s *Server) handlePayoutReconciliationOverview(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	unmatched, _ := services.ListUnmatchedGatewayPayouts(s.DB, companyID)
	unmatchedEntries, _ := services.ListUnmatchedBankEntries(s.DB, companyID)
	matched, _ := services.ListMatchedPayoutReconciliations(s.DB, companyID)

	vm := pages.PayoutReconciliationOverviewVM{
		HasCompany:       true,
		UnmatchedPayouts: unmatched,
		UnmatchedEntries: unmatchedEntries,
		MatchedRecords:   matched,
		JustMatched:      c.Query("matched") == "1",
		JustCreated:      c.Query("created") == "1",
		FormError:        strings.TrimSpace(c.Query("error")),
	}
	return pages.PayoutReconciliationOverview(vm).Render(c.Context(), c)
}

// handleBankEntryCreate processes the bank entry creation form.
// POST /settings/payment-gateways/payout-reconciliation/bank-entries
func (s *Server) handleBankEntryCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	reconBase := "/settings/payment-gateways/payout-reconciliation"

	bankIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	bankID64, err := strconv.ParseUint(bankIDRaw, 10, 64)
	if err != nil || bankID64 == 0 {
		return redirectErr(c, reconBase, "bank account ID is required")
	}

	entryDateRaw := strings.TrimSpace(c.FormValue("entry_date"))
	entryDate, err := time.Parse("2006-01-02", entryDateRaw)
	if err != nil {
		return redirectErr(c, reconBase, "entry date must be YYYY-MM-DD")
	}

	amtRaw := strings.TrimSpace(c.FormValue("amount"))
	if amtRaw == "" {
		return redirectErr(c, reconBase, "amount is required")
	}
	amt, err := decimal.NewFromString(amtRaw)
	if err != nil || !amt.IsPositive() {
		return redirectErr(c, reconBase, "amount must be a positive number")
	}

	description := strings.TrimSpace(c.FormValue("description"))
	// Currency: default to empty (company base currency) for now.
	currencyCode := ""

	_, err = services.CreateBankEntry(s.DB, services.CreateBankEntryInput{
		CompanyID:     companyID,
		BankAccountID: uint(bankID64),
		EntryDate:     entryDate,
		Amount:        amt,
		CurrencyCode:  currencyCode,
		Description:   description,
	})
	if err != nil {
		return redirectErr(c, reconBase, reconErrMessage(err))
	}
	return c.Redirect(reconBase+"?created=1", fiber.StatusSeeOther)
}

// handlePayoutMatchForm renders the per-payout bank-entry selection form.
// GET /settings/payment-gateways/payouts/:id/reconcile
func (s *Server) handlePayoutMatchForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/payment-gateways/payouts", fiber.StatusSeeOther)
	}

	payout, err := services.GetGatewayPayoutByID(s.DB, companyID, uint(id64))
	if err != nil {
		return redirectErr(c, "/settings/payment-gateways/payouts", "payout not found")
	}

	rec, _ := services.GetPayoutReconciliation(s.DB, companyID, payout.ID)
	candidates, _ := services.ListCandidateBankEntries(s.DB, companyID, payout)

	// Batch 19: load components and expected net for the match form.
	components, _ := services.ListGatewayPayoutComponents(s.DB, companyID, payout.ID)
	expectedNet, _ := services.ComputeGatewayPayoutExpectedNet(s.DB, companyID, payout)

	vm := pages.PayoutMatchFormVM{
		HasCompany:       true,
		Payout:           payout,
		Reconciliation:   rec,
		CandidateEntries: candidates,
		Components:       components,
		ExpectedNet:      expectedNet,
		FormError:        strings.TrimSpace(c.Query("error")),
	}
	return pages.PayoutMatchForm(vm).Render(c.Context(), c)
}

// handlePayoutMatchSubmit processes the bank entry selection and creates the match.
// POST /settings/payment-gateways/payouts/:id/reconcile
func (s *Server) handlePayoutMatchSubmit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect("/settings/payment-gateways/payouts", fiber.StatusSeeOther)
	}
	matchBase := "/settings/payment-gateways/payouts/" + c.Params("id") + "/reconcile"

	bankEntryIDRaw := strings.TrimSpace(c.FormValue("bank_entry_id"))
	bankEntryID64, err := strconv.ParseUint(bankEntryIDRaw, 10, 64)
	if err != nil || bankEntryID64 == 0 {
		return redirectErr(c, matchBase, "please select a bank entry")
	}

	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}

	if err := services.MatchGatewayPayoutToBankEntry(
		s.DB, companyID, uint(id64), uint(bankEntryID64), actor,
	); err != nil {
		// Auto-create a reconciliation exception for structural failures.
		// Input / not-found errors are NOT recorded as exceptions.
		if exType, isStructural := services.ExceptionTypeForMatchError(err); isStructural {
			payoutID := uint(id64)
			bankEntryID := uint(bankEntryID64)
			_, _, _ = services.CreateReconciliationException(s.DB, services.CreateReconciliationExceptionInput{
				CompanyID:       companyID,
				ExceptionType:   exType,
				GatewayPayoutID: &payoutID,
				BankEntryID:     &bankEntryID,
				Summary:         fmt.Sprintf("Match attempt failed: %s", err.Error()),
				CreatedByActor:  actor,
			})
		}
		return redirectErr(c, matchBase, reconErrMessage(err))
	}
	return c.Redirect("/settings/payment-gateways/payout-reconciliation?matched=1", fiber.StatusSeeOther)
}

// reconErrMessage translates service errors into user-facing messages.
func reconErrMessage(err error) string {
	switch {
	case err == services.ErrReconPayoutNotFound:
		return "Gateway payout not found."
	case err == services.ErrReconBankEntryNotFound:
		return "Bank entry not found."
	case err == services.ErrReconPayoutAlreadyMatched:
		return "This payout has already been matched to a bank entry."
	case err == services.ErrReconBankEntryAlreadyMatched:
		return "This bank entry has already been matched to a payout."
	case err == services.ErrReconAmountMismatch:
		return "The payout net amount does not match the bank entry amount. Exact equality is required."
	case err == services.ErrReconAccountMismatch:
		return "The payout bank account does not match the bank entry account."
	case err == services.ErrReconCurrencyMismatch:
		return "Currency mismatch between payout and bank entry."
	case err == services.ErrReconBankEntryInvalid:
		return "Bank entry amount must be positive."
	case err == services.ErrReconBankAccountInvalid:
		return "Bank account not found or invalid."
	default:
		return "Reconciliation failed: " + err.Error()
	}
}
