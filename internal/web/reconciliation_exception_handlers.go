// 遵循project_guide.md
package web

// reconciliation_exception_handlers.go — Batch 20/21: Reconciliation exception UI.
//
// Routes (registered in routes.go):
//
//   GET  /settings/payment-gateways/reconciliation-exceptions
//          — list all exceptions for the active company
//   GET  /settings/payment-gateways/reconciliation-exceptions/new
//          — manual filing form (operator-initiated exceptions only)
//   POST /settings/payment-gateways/reconciliation-exceptions
//          — submit a manually-filed exception
//   GET  /settings/payment-gateways/reconciliation-exceptions/:id
//          — exception detail + status action forms + available hooks + recent attempts
//   POST /settings/payment-gateways/reconciliation-exceptions/:id/review
//          — transition open → reviewed
//   POST /settings/payment-gateways/reconciliation-exceptions/:id/dismiss
//          — transition to dismissed (terminal)
//   POST /settings/payment-gateways/reconciliation-exceptions/:id/resolve
//          — transition to resolved (terminal)
//   POST /settings/payment-gateways/reconciliation-exceptions/:id/hooks/:hook_type
//          — execute an execution hook (e.g. retry_match)

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

const exceptionListBase = "/settings/payment-gateways/reconciliation-exceptions"

// handleReconciliationExceptionList renders the exception list page.
// GET /settings/payment-gateways/reconciliation-exceptions
func (s *Server) handleReconciliationExceptionList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	exceptions, _ := services.ListReconciliationExceptions(s.DB, companyID)
	vm := pages.ReconciliationExceptionListVM{
		HasCompany:   true,
		Exceptions:   exceptions,
		JustFiled:    c.Query("filed") == "1",
		JustActioned: c.Query("actioned") == "1",
		FormError:    strings.TrimSpace(c.Query("error")),
	}
	return pages.ReconciliationExceptionList(vm).Render(c.Context(), c)
}

// handleReconciliationExceptionNew renders the manual-filing form.
// GET /settings/payment-gateways/reconciliation-exceptions/new
func (s *Server) handleReconciliationExceptionNew(c *fiber.Ctx) error {
	_, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.ReconciliationExceptionNewVM{
		HasCompany: true,
		FormError:  strings.TrimSpace(c.Query("error")),
	}
	return pages.ReconciliationExceptionNew(vm).Render(c.Context(), c)
}

// handleReconciliationExceptionCreate processes the manual-filing form submission.
// POST /settings/payment-gateways/reconciliation-exceptions
func (s *Server) handleReconciliationExceptionCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	newBase := exceptionListBase + "/new"

	exTypeRaw := strings.TrimSpace(c.FormValue("exception_type"))
	summary := strings.TrimSpace(c.FormValue("summary"))
	detail := strings.TrimSpace(c.FormValue("detail"))

	if exTypeRaw == "" {
		return redirectWithQuery(c, newBase, "error", "Exception type is required.")
	}
	if summary == "" {
		return redirectWithQuery(c, newBase, "error", "Summary is required.")
	}

	// Only manually-filable types are accepted via the UI form.
	exType := models.ReconciliationExceptionType(exTypeRaw)
	allowed := false
	for _, t := range models.ManuallyFilableExceptionTypes() {
		if exType == t {
			allowed = true
			break
		}
	}
	if !allowed {
		return redirectWithQuery(c, newBase, "error", "This exception type cannot be filed manually.")
	}

	inp := services.CreateReconciliationExceptionInput{
		CompanyID:      companyID,
		ExceptionType:  exType,
		Summary:        summary,
		Detail:         detail,
		CreatedByActor: exceptionActor(c),
	}

	// Optional payout / bank entry references.
	if payoutIDRaw := strings.TrimSpace(c.FormValue("gateway_payout_id")); payoutIDRaw != "" {
		if id64, err := strconv.ParseUint(payoutIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			inp.GatewayPayoutID = &id
		}
	}
	if bankEntryIDRaw := strings.TrimSpace(c.FormValue("bank_entry_id")); bankEntryIDRaw != "" {
		if id64, err := strconv.ParseUint(bankEntryIDRaw, 10, 64); err == nil && id64 > 0 {
			id := uint(id64)
			inp.BankEntryID = &id
		}
	}

	_, _, err := services.CreateReconciliationException(s.DB, inp)
	if err != nil {
		return redirectWithQuery(c, newBase, "error", exceptionCreateErrMessage(err))
	}
	return c.Redirect(exceptionListBase+"?filed=1", fiber.StatusSeeOther)
}

// handleReconciliationExceptionDetail renders the exception detail page.
// GET /settings/payment-gateways/reconciliation-exceptions/:id
func (s *Server) handleReconciliationExceptionDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect(exceptionListBase, fiber.StatusSeeOther)
	}

	ex, err := services.GetReconciliationException(s.DB, companyID, uint(id64))
	if err != nil {
		return c.Redirect(exceptionListBase, fiber.StatusSeeOther)
	}

	// Compute available resolution hooks and load recent attempts.
	rawHooks := services.AvailableHooksForException(s.DB, companyID, ex)
	hookVMs := make([]pages.ExceptionResolutionHookVM, len(rawHooks))
	for i, h := range rawHooks {
		hookVMs[i] = pages.ExceptionResolutionHookVM{
			Type:              h.Type,
			Label:             h.Label,
			Description:       h.Description,
			Available:         h.Available,
			UnavailableReason: h.UnavailableReason,
			NavigateURL:       h.NavigateURL,
		}
	}
	attempts, _ := services.ListRecentResolutionAttempts(s.DB, companyID, uint(id64), 10)

	vm := pages.ReconciliationExceptionDetailVM{
		HasCompany:     true,
		Exception:      ex,
		AvailableHooks: hookVMs,
		RecentAttempts: attempts,
		JustActioned:   c.Query("actioned") == "1",
		ActionError:    strings.TrimSpace(c.Query("error")),
		HookSuccess:    c.Query("hook") == "ok",
		HookError:      strings.TrimSpace(c.Query("hook_error")),
	}

	if ex.GatewayPayoutID != nil {
		var payout models.GatewayPayout
		if err := s.DB.Where("id = ? AND company_id = ?", *ex.GatewayPayoutID, companyID).First(&payout).Error; err == nil {
			vm.LinkedPayout = &payout
			if expectedNet, err := services.ComputeGatewayPayoutExpectedNet(s.DB, companyID, &payout); err == nil {
				vm.LinkedPayoutExpectedNet = expectedNet.StringFixed(2)
			}
			var payoutMatch models.PayoutReconciliation
			if err := s.DB.Where("company_id = ? AND gateway_payout_id = ?", companyID, payout.ID).First(&payoutMatch).Error; err == nil {
				vm.LinkedPayoutReconciliation = &payoutMatch
			}
		}
	}
	if ex.BankEntryID != nil {
		var bankEntry models.BankEntry
		if err := s.DB.Where("id = ? AND company_id = ?", *ex.BankEntryID, companyID).First(&bankEntry).Error; err == nil {
			vm.LinkedBankEntry = &bankEntry
			var bankMatch models.PayoutReconciliation
			if err := s.DB.Where("company_id = ? AND bank_entry_id = ?", companyID, bankEntry.ID).First(&bankMatch).Error; err == nil {
				vm.LinkedBankEntryMatch = &bankMatch
			}
		}
	}
	return pages.ReconciliationExceptionDetail(vm).Render(c.Context(), c)
}

// handleExceptionHookExecute executes an execution hook for the exception.
// POST /settings/payment-gateways/reconciliation-exceptions/:id/hooks/:hook_type
func (s *Server) handleExceptionHookExecute(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect(exceptionListBase, fiber.StatusSeeOther)
	}
	detailBase := exceptionListBase + "/" + c.Params("id")

	hookType := models.ResolutionHookType(strings.TrimSpace(c.Params("hook_type")))

	execErr := services.ExecuteResolutionHook(s.DB, companyID, uint(id64), hookType, exceptionActor(c))
	if execErr != nil {
		return redirectWithQuery(c, detailBase, "hook_error", hookHookErrMessage(execErr))
	}
	return redirectWithQuery(c, detailBase, "hook", "ok")
}

// handleReconciliationExceptionReview processes the review transition.
// POST /settings/payment-gateways/reconciliation-exceptions/:id/review
func (s *Server) handleReconciliationExceptionReview(c *fiber.Ctx) error {
	return s.applyExceptionStatusAction(c, func(companyID, id uint, actor string) error {
		return services.ReviewReconciliationException(s.DB, companyID, id, actor)
	})
}

// handleReconciliationExceptionDismiss processes the dismiss transition.
// POST /settings/payment-gateways/reconciliation-exceptions/:id/dismiss
func (s *Server) handleReconciliationExceptionDismiss(c *fiber.Ctx) error {
	note := strings.TrimSpace(c.FormValue("note"))
	return s.applyExceptionStatusAction(c, func(companyID, id uint, actor string) error {
		return services.DismissReconciliationException(s.DB, companyID, id, actor, note)
	})
}

// handleReconciliationExceptionResolve processes the resolve transition.
// POST /settings/payment-gateways/reconciliation-exceptions/:id/resolve
func (s *Server) handleReconciliationExceptionResolve(c *fiber.Ctx) error {
	note := strings.TrimSpace(c.FormValue("note"))
	return s.applyExceptionStatusAction(c, func(companyID, id uint, actor string) error {
		return services.ResolveReconciliationException(s.DB, companyID, id, actor, note)
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// applyExceptionStatusAction is the shared logic for review/dismiss/resolve
// handlers.  On success it redirects to the detail page with actioned=1.
// On error it redirects to the detail page with the error message.
func (s *Server) applyExceptionStatusAction(
	c *fiber.Ctx,
	action func(companyID, id uint, actor string) error,
) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect(exceptionListBase, fiber.StatusSeeOther)
	}
	detailBase := exceptionListBase + "/" + c.Params("id")

	if err := action(companyID, uint(id64), exceptionActor(c)); err != nil {
		return redirectWithQuery(c, detailBase, "error", exceptionActionErrMessage(err))
	}
	return redirectWithQuery(c, detailBase, "actioned", "1")
}

// exceptionActor returns the current user's email or "system".
func exceptionActor(c *fiber.Ctx) string {
	user := UserFromCtx(c)
	if user != nil && strings.TrimSpace(user.Email) != "" {
		return strings.TrimSpace(user.Email)
	}
	return "system"
}

// exceptionCreateErrMessage translates create errors into user-facing messages.
func exceptionCreateErrMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrExceptionTypeInvalid):
		return "Unsupported exception type."
	case errors.Is(err, services.ErrExceptionSourceRequired):
		return "Link at least one gateway payout or bank entry before filing the exception."
	case errors.Is(err, services.ErrExceptionPayoutNotFound):
		return "Gateway payout not found for this company."
	case errors.Is(err, services.ErrExceptionBankEntryNotFound):
		return "Bank entry not found for this company."
	case errors.Is(err, services.ErrExceptionReconciliationMissing):
		return "Reconciliation record not found for this company."
	case errors.Is(err, services.ErrExceptionSourceMismatch):
		return "Linked payout, bank entry, and reconciliation record do not refer to the same exception context."
	default:
		return "Could not file exception: " + err.Error()
	}
}

// exceptionActionErrMessage translates status-transition errors into user-facing messages.
func exceptionActionErrMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrExceptionAlreadyClosed):
		return "This exception is already in a terminal state and cannot be changed."
	case errors.Is(err, services.ErrExceptionTransitionInvalid):
		return "Invalid status transition."
	case errors.Is(err, services.ErrExceptionDismissNoteRequired):
		return "Dismissal note is required."
	case errors.Is(err, services.ErrExceptionNotFound):
		return "Exception not found."
	default:
		return "Action failed: " + err.Error()
	}
}

// hookHookErrMessage translates hook execution errors into user-facing messages.
func hookHookErrMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrHookTypeUnsupported):
		return "This hook type is not supported."
	case errors.Is(err, services.ErrHookNotAvailable):
		return "Hook is not available for this exception in its current state."
	case errors.Is(err, services.ErrHookExceptionClosed):
		return "Cannot execute hook: exception is already closed."
	case errors.Is(err, services.ErrHookMissingSourcePayout):
		return "Hook requires a linked gateway payout on this exception."
	case errors.Is(err, services.ErrHookMissingSourceEntry):
		return "Hook requires a linked bank entry on this exception."
	default:
		return "Hook execution failed: " + err.Error()
	}
}
