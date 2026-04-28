// 遵循project_guide.md
package web

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"balanciz/internal/searchprojection/producers"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
	"gorm.io/gorm"
)

// handleInvoiceIssue transitions an invoice from draft to issued.
// POST /invoices/:id/issue
func (s *Server) handleInvoiceIssue(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	_, err = services.IssueInvoice(s.DB, companyID, invoiceID)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), "Could not issue invoice.")
	}
	_ = producers.ProjectInvoice(c.Context(), s.DB, s.SearchProjector, companyID, invoiceID)

	return redirectTo(c, fmt.Sprintf("/invoices/%d?issued=1", invoiceID))
}

// handleInvoiceSend transitions an invoice from issued to sent.
// POST /invoices/:id/send
func (s *Server) handleInvoiceSend(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	_, err = services.SendInvoice(s.DB, companyID, invoiceID)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), "Could not mark invoice as sent.")
	}
	_ = producers.ProjectInvoice(c.Context(), s.DB, s.SearchProjector, companyID, invoiceID)

	return redirectTo(c, fmt.Sprintf("/invoices/%d?sent=1", invoiceID))
}

// handleInvoiceMarkPaid is kept for backward compatibility and now redirects
// users into the real Receive Payment flow instead of directly mutating status.
// POST /invoices/:id/mark-paid
func (s *Server) handleInvoiceMarkPaid(c *fiber.Ctx) error {
	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}
	return redirectTo(c, fmt.Sprintf("/invoices/%d/receive-payment", invoiceID))
}

// handleInvoiceVoid voids an invoice and creates a reversal JE.
// POST /invoices/:id/void
func (s *Server) handleInvoiceVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	user := UserFromCtx(c)
	var userID *uuid.UUID
	actor := "system"
	if user != nil {
		uid := user.ID
		userID = &uid
		if user.Email != "" {
			actor = user.Email
		}
	}

	if err := services.VoidInvoice(s.DB, companyID, invoiceID, actor, userID); err != nil {
		return c.Redirect(fmt.Sprintf("/invoices/%d?voiderror=Could+not+void+invoice.", invoiceID), fiber.StatusSeeOther)
	}
	s.ReportCache.InvalidateCompany(companyID)
	_ = producers.ProjectInvoice(c.Context(), s.DB, s.SearchProjector, companyID, invoiceID)

	return redirectTo(c, fmt.Sprintf("/invoices/%d?voided=1", invoiceID))
}

// handleInvoicePost explicitly posts an invoice to accounting.
// POST /invoices/:id/post
func (s *Server) handleInvoicePost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	user := UserFromCtx(c)
	actor := "system"
	var uid *uuid.UUID
	if user != nil {
		u := user.ID
		uid = &u
		if user.Email != "" {
			actor = user.Email
		}
	}

	if err := services.PostInvoice(s.DB, companyID, invoiceID, actor, uid); err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), "Could not post invoice.")
	}
	s.ReportCache.InvalidateCompany(companyID)
	_ = producers.ProjectInvoice(c.Context(), s.DB, s.SearchProjector, companyID, invoiceID)

	return redirectTo(c, fmt.Sprintf("/invoices/%d?issued=1", invoiceID))
}

// handleInvoiceDelete deletes a draft invoice.
// POST /invoices/:id/delete
func (s *Server) handleInvoiceDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	user := UserFromCtx(c)
	var userID *uuid.UUID
	actor := "system"
	if user != nil {
		uid := user.ID
		userID = &uid
		if user.Email != "" {
			actor = user.Email
		}
	}

	if err := services.DeleteInvoice(s.DB, companyID, invoiceID, actor, userID); err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), "Could not delete invoice.")
	}
	_ = producers.DeleteInvoiceProjection(c.Context(), s.SearchProjector, companyID, invoiceID)

	return redirectTo(c, "/invoices?deleted=1")
}

// handleInvoiceRequestPayment creates a payment request linked to an invoice.
// POST /invoices/:id/request-payment
func (s *Server) handleInvoiceRequestPayment(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}
	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}
	gwIDRaw := strings.TrimSpace(c.FormValue("gateway_account_id"))
	gwID, _ := strconv.ParseUint(gwIDRaw, 10, 64)
	if gwID == 0 {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), "gateway account required")
	}

	_, err = services.CreatePaymentRequestForInvoice(s.DB, services.InvoicePaymentRequestInput{
		CompanyID:        companyID,
		InvoiceID:        invoiceID,
		GatewayAccountID: uint(gwID),
	})
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), err.Error())
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d?paymentcreated=1", invoiceID))
}

// handleInvoiceReceivePaymentForm renders the Receive Payment form pre-filled
// for a specific invoice.
// GET /invoices/:id/receive-payment
func (s *Server) handleInvoiceReceivePaymentForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	var inv models.Invoice
	if err := s.DB.Preload("Customer").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return redirectErr(c, "/invoices", "invoice not found")
	}

	switch inv.Status {
	case models.InvoiceStatusIssued, models.InvoiceStatusSent,
		models.InvoiceStatusOverdue, models.InvoiceStatusPartiallyPaid:
		// ok
	default:
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), "invoice is not open for payment")
	}

	bankAccounts, _ := s.bankAccountsForCompany(companyID)
	amount := inv.BalanceDue
	if !amount.IsPositive() {
		amount = inv.Amount
	}

	vm := pages.InvoiceReceivePaymentVM{
		HasCompany:   true,
		Invoice:      inv,
		BankAccounts: bankAccounts,
		EntryDate:    time.Now().Format("2006-01-02"),
		Amount:       amount.StringFixed(2),
	}
	return pages.InvoiceReceivePayment(vm).Render(c.Context(), c)
}

// handleInvoiceReceivePaymentSubmit records the payment for a specific invoice.
// POST /invoices/:id/receive-payment
func (s *Server) handleInvoiceReceivePaymentSubmit(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	var inv models.Invoice
	if err := s.DB.Preload("Customer").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return redirectErr(c, "/invoices", "invoice not found")
	}

	bankAccounts, _ := s.bankAccountsForCompany(companyID)

	entryDateRaw := strings.TrimSpace(c.FormValue("entry_date"))
	paymentMethodRaw := strings.TrimSpace(c.FormValue("payment_method"))
	bankIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	amountRaw := strings.TrimSpace(c.FormValue("amount"))
	memo := strings.TrimSpace(c.FormValue("memo"))

	vm := pages.InvoiceReceivePaymentVM{
		HasCompany:    true,
		Invoice:       inv,
		BankAccounts:  bankAccounts,
		PaymentMethod: paymentMethodRaw,
		EntryDate:     entryDateRaw,
		BankAccountID: bankIDRaw,
		Amount:        amountRaw,
		Memo:          memo,
	}

	entryDate, err := time.Parse("2006-01-02", entryDateRaw)
	if err != nil {
		vm.DateError = "Payment date is required."
	}
	paymentMethod, err := models.ParsePaymentMethod(paymentMethodRaw)
	if err != nil || !models.IsManualPaymentMethod(paymentMethod) {
		vm.PaymentMethodError = "Payment method is required."
	}

	bankU64, err := services.ParseUint(bankIDRaw)
	if err != nil || bankU64 == 0 {
		vm.BankError = "Bank account is required."
	}
	amount, err := services.ParseDecimalMoney(amountRaw)
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		vm.AmountError = "Amount must be greater than 0."
	}
	arID, arErr := s.defaultARAccountID(companyID)
	if arErr != nil {
		vm.ARError = "No Accounts Receivable account found. Please add one to your Chart of Accounts."
	}

	if vm.DateError != "" || vm.PaymentMethodError != "" || vm.BankError != "" || vm.ARError != "" || vm.AmountError != "" {
		return pages.InvoiceReceivePayment(vm).Render(c.Context(), c)
	}

	var jeID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		jeID, txErr = services.RecordReceivePayment(tx, services.ReceivePaymentInput{
			CompanyID:     companyID,
			CustomerID:    inv.CustomerID,
			EntryDate:     entryDate,
			BankAccountID: uint(bankU64),
			PaymentMethod: paymentMethod,
			ARAccountID:   arID,
			Allocations: []services.InvoiceAllocation{{
				InvoiceID: invoiceID,
				Amount:    amount,
			}},
			Memo: memo,
		})
		return txErr
	}); err != nil {
		vm.FormError = "Could not record payment: " + err.Error()
		return pages.InvoiceReceivePayment(vm).Render(c.Context(), c)
	}

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "payment.received", "journal_entry", jeID, actor, map[string]any{
		"invoice_id":     invoiceID,
		"customer_id":    inv.CustomerID,
		"amount":         amount.StringFixed(2),
		"payment_method": string(paymentMethod),
		"entry_date":     entryDateRaw,
		"company_id":     companyID,
	}, &cid, &uid)
	s.ReportCache.InvalidateCompany(companyID)
	slog.Info("report.invalidate",
		"company_id", companyID,
		"reason", "invoice_receive_payment",
		"journal_entry_id", jeID,
		"invoice_id", invoiceID,
	)

	return redirectTo(c, fmt.Sprintf("/invoices/%d?received=1", invoiceID))
}

// handleRetryGatewaySettlement re-runs the settlement bridge for the invoice.
// POST /invoices/:id/retry-gateway-settlement
//
// Finds the latest payment_succeeded HostedPaymentAttempt for the invoice and
// calls RetryGatewaySettlement. The settlement outcome fields on the attempt are
// updated regardless of outcome. Idempotent: repeated calls after successful
// settlement redirect with ?settled=1 without any mutation.
//
// Auth: RequirePermission(ActionJournalCreate) — same level as posting a transaction.
func (s *Server) handleRetryGatewaySettlement(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	result, err := services.RetryGatewaySettlement(s.DB, companyID, invoiceID)
	if err != nil {
		if err == services.ErrNoSucceededAttempt {
			return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID),
				"No verified gateway payment found for this invoice.")
		}
		if err == services.ErrSettlementAlreadyDone {
			return redirectTo(c, fmt.Sprintf("/invoices/%d?settled=1", invoiceID))
		}
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID),
			"Settlement failed: "+err.Error())
	}

	if !result.Eligibility.Eligible {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID),
			"Settlement still ineligible: "+result.Eligibility.Reason)
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d?settled=1", invoiceID))
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func parseInvoiceID(c *fiber.Ctx) (uint, error) {
	idStr := c.Params("id")
	id64, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint(id64), nil
}

func redirectTo(c *fiber.Ctx, path string) error {
	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", path)
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect(path, fiber.StatusSeeOther)
}

func redirectErr(c *fiber.Ctx, path, errMsg string) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return c.Redirect(path+sep+"error="+url.QueryEscape(errMsg), fiber.StatusSeeOther)
}
