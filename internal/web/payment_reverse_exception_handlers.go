// 遵循project_guide.md
package web

// payment_reverse_exception_handlers.go — Batch 23/26: Payment reverse exception UI.
//
// Routes (registered in routes.go):
//
//   GET  /settings/payment-gateways/reverse-exceptions
//          — list all payment reverse exceptions for the active company
//   GET  /settings/payment-gateways/reverse-exceptions/:id
//          — exception detail + status action forms + linked transaction summaries
//            + allocation rollup (Batch 25) + hooks + attempts (Batch 26)
//   POST /settings/payment-gateways/reverse-exceptions/:id/review
//          — transition open → reviewed
//   POST /settings/payment-gateways/reverse-exceptions/:id/dismiss
//          — transition to dismissed (terminal)
//   POST /settings/payment-gateways/reverse-exceptions/:id/resolve
//          — transition to resolved (terminal)
//   POST /settings/payment-gateways/reverse-exceptions/:id/hooks/:hookType
//          — execute an execution hook (Batch 26)

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

const reverseExceptionListBase = "/settings/payment-gateways/reverse-exceptions"

// handlePaymentReverseExceptionList renders the exception list page.
// GET /settings/payment-gateways/reverse-exceptions
func (s *Server) handlePaymentReverseExceptionList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	exceptions, _ := services.ListPaymentReverseExceptions(s.DB, companyID)
	vm := pages.PaymentReverseExceptionListVM{
		HasCompany:   true,
		Exceptions:   exceptions,
		JustActioned: c.Query("actioned") == "1",
		FormError:    strings.TrimSpace(c.Query("error")),
	}
	return pages.PaymentReverseExceptionList(vm).Render(c.Context(), c)
}

// handlePaymentReverseExceptionDetail renders the exception detail page.
// GET /settings/payment-gateways/reverse-exceptions/:id
func (s *Server) handlePaymentReverseExceptionDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect(reverseExceptionListBase, fiber.StatusSeeOther)
	}

	ex, err := services.GetPaymentReverseException(s.DB, companyID, uint(id64))
	if err != nil {
		return c.Redirect(reverseExceptionListBase, fiber.StatusSeeOther)
	}

	vm := pages.PaymentReverseExceptionDetailVM{
		HasCompany:   true,
		Exception:    ex,
		JustActioned: c.Query("actioned") == "1",
		ActionError:  strings.TrimSpace(c.Query("error")),
		HookActioned: c.Query("hook_actioned") == "1",
		HookError:    strings.TrimSpace(c.Query("hook_error")),
	}

	// Build allocation rollup (Batch 25).  Errors degrade gracefully — the
	// exception detail page is still rendered without the rollup section.
	if rollup, err := services.BuildPaymentReverseDetailRollup(s.DB, companyID, ex); err == nil {
		vm.Rollup = rollup
		// Populate ReverseTxn / OriginalTxn from rollup to avoid a second DB query.
		vm.ReverseTxn = rollup.ReverseTxn
		vm.OriginalTxn = rollup.OriginalTxn
	} else {
		// Rollup failed: fall back to bare txn loads so the linked-txn card still works.
		if ex.ReverseTxnID != nil {
			var reverseTxn models.PaymentTransaction
			if err := s.DB.Where("id = ? AND company_id = ?", *ex.ReverseTxnID, companyID).First(&reverseTxn).Error; err == nil {
				vm.ReverseTxn = &reverseTxn
			}
		}
		if ex.OriginalTxnID != nil {
			var originalTxn models.PaymentTransaction
			if err := s.DB.Where("id = ? AND company_id = ?", *ex.OriginalTxnID, companyID).First(&originalTxn).Error; err == nil {
				vm.OriginalTxn = &originalTxn
			}
		}
	}

	// Batch 26: load hook policy (always computed, even for terminal exceptions).
	vm.Hooks = services.AvailablePaymentReverseHooks(s.DB, companyID, ex)

	// Batch 26: load recent attempts (newest first, cap at 10).
	attempts, _ := services.ListRecentPRAttempts(s.DB, companyID, ex.ID, 10)
	vm.Attempts = attempts

	return pages.PaymentReverseExceptionDetail(vm).Render(c.Context(), c)
}

// handlePaymentReverseExceptionHook executes an execution hook.
// POST /settings/payment-gateways/reverse-exceptions/:id/hooks/:hookType
func (s *Server) handlePaymentReverseExceptionHook(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect(reverseExceptionListBase, fiber.StatusSeeOther)
	}
	detailBase := reverseExceptionListBase + "/" + c.Params("id")

	hookType := models.PRHookType(strings.TrimSpace(c.Params("hookType")))
	if !models.IsPRExecutionHook(hookType) {
		// Silently redirect for navigation hooks — they should never POST.
		return c.Redirect(detailBase, fiber.StatusSeeOther)
	}

	execErr := services.ExecutePaymentReverseHook(s.DB, companyID, uint(id64), hookType, exceptionActor(c))
	if execErr != nil {
		return redirectWithQuery(c, detailBase, "hook_error", reverseHookErrMessage(execErr))
	}
	return redirectWithQuery(c, detailBase, "hook_actioned", "1")
}

// handlePaymentReverseExceptionReview processes the review transition.
// POST /settings/payment-gateways/reverse-exceptions/:id/review
func (s *Server) handlePaymentReverseExceptionReview(c *fiber.Ctx) error {
	return s.applyReverseExceptionStatusAction(c, func(companyID, id uint, actor string) error {
		return services.ReviewPaymentReverseException(s.DB, companyID, id, actor)
	})
}

// handlePaymentReverseExceptionDismiss processes the dismiss transition.
// POST /settings/payment-gateways/reverse-exceptions/:id/dismiss
func (s *Server) handlePaymentReverseExceptionDismiss(c *fiber.Ctx) error {
	note := strings.TrimSpace(c.FormValue("note"))
	return s.applyReverseExceptionStatusAction(c, func(companyID, id uint, actor string) error {
		return services.DismissPaymentReverseException(s.DB, companyID, id, actor, note)
	})
}

// handlePaymentReverseExceptionResolve processes the resolve transition.
// POST /settings/payment-gateways/reverse-exceptions/:id/resolve
func (s *Server) handlePaymentReverseExceptionResolve(c *fiber.Ctx) error {
	note := strings.TrimSpace(c.FormValue("note"))
	return s.applyReverseExceptionStatusAction(c, func(companyID, id uint, actor string) error {
		return services.ResolvePaymentReverseException(s.DB, companyID, id, actor, note)
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (s *Server) applyReverseExceptionStatusAction(
	c *fiber.Ctx,
	action func(companyID, id uint, actor string) error,
) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return c.Redirect(reverseExceptionListBase, fiber.StatusSeeOther)
	}
	detailBase := reverseExceptionListBase + "/" + c.Params("id")

	if err := action(companyID, uint(id64), exceptionActor(c)); err != nil {
		return redirectWithQuery(c, detailBase, "error", reverseExceptionActionErrMessage(err))
	}
	return redirectWithQuery(c, detailBase, "actioned", "1")
}

// reverseExceptionActionErrMessage translates status-transition errors into user-facing messages.
func reverseExceptionActionErrMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrPRExceptionAlreadyClosed):
		return "This exception is already in a terminal state and cannot be changed."
	case errors.Is(err, services.ErrPRExceptionTransitionInvalid):
		return "Invalid status transition."
	case errors.Is(err, services.ErrPRExceptionDismissNote):
		return "Dismissal note is required."
	case errors.Is(err, services.ErrPRExceptionResolveNote):
		return "Resolution note is required."
	case errors.Is(err, services.ErrPRExceptionNotFound):
		return "Exception not found."
	default:
		return "Action failed: " + err.Error()
	}
}

// reverseHookErrMessage translates hook execution errors into user-facing messages.
func reverseHookErrMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrPRHookTypeUnsupported):
		return "This hook type is not supported."
	case errors.Is(err, services.ErrPRHookExceptionClosed):
		return "This exception is already closed — hooks cannot be executed."
	case errors.Is(err, services.ErrPRHookDuplicate):
		return "This hook already succeeded for this exception; duplicate execution was rejected."
	case errors.Is(err, services.ErrPRHookNotAvailable):
		return "This hook is not currently available for this exception."
	case errors.Is(err, services.ErrPRExceptionNotFound):
		return "Exception not found."
	default:
		return "Hook execution failed: " + err.Error()
	}
}
