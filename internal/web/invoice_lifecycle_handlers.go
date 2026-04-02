// 遵循project_guide.md
package web

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
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

	return redirectTo(c, fmt.Sprintf("/invoices/%d?sent=1", invoiceID))
}

// handleInvoiceMarkPaid transitions an invoice to paid.
// POST /invoices/:id/mark-paid
func (s *Server) handleInvoiceMarkPaid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return redirectErr(c, "/invoices", "company context required")
	}

	invoiceID, err := parseInvoiceID(c)
	if err != nil {
		return redirectErr(c, "/invoices", "invalid invoice ID")
	}

	_, err = services.MarkInvoicePaid(s.DB, companyID, invoiceID)
	if err != nil {
		return redirectErr(c, fmt.Sprintf("/invoices/%d", invoiceID), "Could not mark invoice as paid.")
	}

	return redirectTo(c, fmt.Sprintf("/invoices/%d?paid=1", invoiceID))
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

	return redirectTo(c, "/invoices?deleted=1")
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

	accounts, _ := s.activeAccountsForCompany(companyID)

	vm := pages.InvoiceReceivePaymentVM{
		HasCompany: true,
		Invoice:    inv,
		Accounts:   accounts,
		EntryDate:  time.Now().Format("2006-01-02"),
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

	accounts, _ := s.activeAccountsForCompany(companyID)

	entryDateRaw := strings.TrimSpace(c.FormValue("entry_date"))
	bankIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	arIDRaw := strings.TrimSpace(c.FormValue("ar_account_id"))
	memo := strings.TrimSpace(c.FormValue("memo"))

	vm := pages.InvoiceReceivePaymentVM{
		HasCompany:    true,
		Invoice:       inv,
		Accounts:      accounts,
		EntryDate:     entryDateRaw,
		BankAccountID: bankIDRaw,
		ARAccountID:   arIDRaw,
		Memo:          memo,
	}

	entryDate, err := time.Parse("2006-01-02", entryDateRaw)
	if err != nil {
		vm.DateError = "Payment date is required."
	}

	bankU64, err := services.ParseUint(bankIDRaw)
	if err != nil || bankU64 == 0 {
		vm.BankError = "Bank account is required."
	}

	arU64, err := services.ParseUint(arIDRaw)
	if err != nil || arU64 == 0 {
		vm.ARError = "Accounts Receivable account is required."
	}

	if vm.DateError != "" || vm.BankError != "" || vm.ARError != "" {
		return pages.InvoiceReceivePayment(vm).Render(c.Context(), c)
	}

	// Determine the amount from the invoice balance.
	amount := inv.BalanceDue
	if !amount.IsPositive() {
		amount = inv.Amount
	}

	var jeID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		jeID, txErr = services.RecordReceivePayment(tx, services.ReceivePaymentInput{
			CompanyID:     companyID,
			CustomerID:    inv.CustomerID,
			EntryDate:     entryDate,
			BankAccountID: uint(bankU64),
			ARAccountID:   uint(arU64),
			InvoiceID:     &invoiceID,
			Amount:        amount,
			Memo:          memo,
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
		"invoice_id":  invoiceID,
		"customer_id": inv.CustomerID,
		"amount":      amount.StringFixed(2),
		"entry_date":  entryDateRaw,
		"company_id":  companyID,
	}, &cid, &uid)

	return redirectTo(c, fmt.Sprintf("/invoices/%d?paid=1", invoiceID))
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
