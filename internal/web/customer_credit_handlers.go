// 遵循project_guide.md
package web

// customer_credit_handlers.go — Batch 16: Customer credit balance handlers.
//
// Routes:
//   GET  /customers/:id/credits           — list credits + apply form
//   POST /customers/:id/credits/apply     — submit credit apply

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleCustomerCredits renders the credit list + apply form for a customer.
// GET /customers/:id/credits
func (s *Server) handleCustomerCredits(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customerID, err := parseCustomerIDParam(c)
	if err != nil {
		return redirectErr(c, "/customers", "invalid customer ID")
	}

	var customer models.Customer
	if err := s.DB.Where("id = ? AND company_id = ?", customerID, companyID).First(&customer).Error; err != nil {
		return redirectErr(c, "/customers", "customer not found")
	}

	credits, _ := services.ListCustomerCredits(s.DB, companyID, customerID)
	activeCredits, _ := services.ListActiveCustomerCredits(s.DB, companyID, customerID)
	outstandingInvoices, _ := services.ListCustomerOutstandingInvoices(s.DB, companyID, customerID, 50)
	total, _ := services.CustomerCreditTotalRemaining(s.DB, companyID, customerID)
	refunds, _ := services.ListARRefunds(s.DB, companyID, services.ARRefundListFilter{CustomerID: customerID})

	vm := pages.CustomerCreditsVM{
		HasCompany:          true,
		Customer:            customer,
		Credits:             credits,
		ActiveCredits:       activeCredits,
		OutstandingInvoices: outstandingInvoices,
		TotalRemaining:      total,
		Refunds:             refunds,
		JustApplied:         c.Query("applied") == "1",
		FormError:           strings.TrimSpace(c.Query("error")),
	}
	return pages.CustomerCredits(vm).Render(c.Context(), c)
}

// handleCustomerCreditApply processes the credit apply form.
// POST /customers/:id/credits/apply
func (s *Server) handleCustomerCreditApply(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	customerID, err := parseCustomerIDParam(c)
	if err != nil {
		return redirectErr(c, "/customers", "invalid customer ID")
	}

	creditsBase := "/customers/" + c.Params("id") + "/credits"

	creditID64, err := strconv.ParseUint(strings.TrimSpace(c.FormValue("credit_id")), 10, 64)
	if err != nil || creditID64 == 0 {
		return redirectErr(c, creditsBase, "credit is required")
	}
	invoiceID64, err := strconv.ParseUint(strings.TrimSpace(c.FormValue("invoice_id")), 10, 64)
	if err != nil || invoiceID64 == 0 {
		return redirectErr(c, creditsBase, "invoice is required")
	}
	amtRaw := strings.TrimSpace(c.FormValue("amount"))
	if amtRaw == "" {
		return redirectErr(c, creditsBase, "amount is required")
	}
	amount, err := decimal.NewFromString(amtRaw)
	if err != nil || !amount.IsPositive() {
		return redirectErr(c, creditsBase, "amount must be a positive number")
	}

	// Verify credit belongs to this customer + company before calling service.
	credit, err := services.GetCustomerCredit(s.DB, companyID, uint(creditID64))
	if err != nil || credit.CustomerID != customerID {
		return redirectErr(c, creditsBase, "credit not found or does not belong to this customer")
	}

	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}

	if err := services.ApplyCustomerCreditToInvoice(
		s.DB, companyID, uint(creditID64), uint(invoiceID64), amount, actor,
	); err != nil {
		return redirectErr(c, creditsBase, creditErrMessage(err))
	}

	return c.Redirect(creditsBase+"?applied=1", fiber.StatusSeeOther)
}

// ── Batch 17: Credit multi-allocation ────────────────────────────────────────

// handleCreditMultiAllocateForm renders the credit multi-invoice allocation form.
// GET /customers/:id/credits/:creditID/allocate
func (s *Server) handleCreditMultiAllocateForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	customerID, err := parseCustomerIDParam(c)
	if err != nil {
		return redirectErr(c, "/customers", "invalid customer ID")
	}
	creditsBase := "/customers/" + c.Params("id") + "/credits"

	creditID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("creditID")), 10, 64)
	if err != nil || creditID64 == 0 {
		return redirectErr(c, creditsBase, "invalid credit ID")
	}

	credit, err := services.GetCustomerCredit(s.DB, companyID, uint(creditID64))
	if err != nil || credit.CustomerID != customerID {
		return redirectErr(c, creditsBase, "credit not found or does not belong to this customer")
	}

	existing, _ := services.ListCreditApplications(s.DB, companyID, uint(creditID64))
	invoices, _ := services.ListAllocatableInvoicesForCustomer(s.DB, companyID, customerID)

	var invoiceRows []pages.AllocatableInvoiceRow
	existingSet := make(map[uint]struct{}, len(existing))
	for _, app := range existing {
		existingSet[app.InvoiceID] = struct{}{}
	}
	for _, inv := range invoices {
		if _, alreadyApplied := existingSet[inv.ID]; alreadyApplied {
			continue
		}
		invoiceRows = append(invoiceRows, pages.AllocatableInvoiceRow{Invoice: inv})
	}

	vm := pages.CreditAllocationVM{
		HasCompany:           true,
		Credit:               *credit,
		CustomerID:           customerID,
		Invoices:             invoiceRows,
		Success:              c.Query("ok") == "1",
		FormError:            strings.TrimSpace(c.Query("error")),
		ExistingApplications: existing,
	}
	return pages.CreditAllocation(vm).Render(c.Context(), c)
}

// handleCreditMultiAllocateSubmit processes the credit multi-allocation form.
// POST /customers/:id/credits/:creditID/allocate
func (s *Server) handleCreditMultiAllocateSubmit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	customerID, err := parseCustomerIDParam(c)
	if err != nil {
		return redirectErr(c, "/customers", "invalid customer ID")
	}

	creditID64, err := strconv.ParseUint(strings.TrimSpace(c.Params("creditID")), 10, 64)
	if err != nil || creditID64 == 0 {
		return redirectErr(c, "/customers/"+c.Params("id")+"/credits", "invalid credit ID")
	}
	allocBase := "/customers/" + c.Params("id") + "/credits/" + c.Params("creditID") + "/allocate"

	// Verify credit ownership.
	credit, err := services.GetCustomerCredit(s.DB, companyID, uint(creditID64))
	if err != nil || credit.CustomerID != customerID {
		return redirectErr(c, "/customers/"+c.Params("id")+"/credits", "credit not found or does not belong to this customer")
	}

	// Parse allocation lines from form fields "amount_<invoiceID>".
	var lines []services.AllocationLine
	c.Request().PostArgs().VisitAll(func(key, val []byte) {
		k := string(key)
		if len(k) <= 7 || k[:7] != "amount_" {
			return
		}
		invIDStr := k[7:]
		invID, parseErr := strconv.ParseUint(invIDStr, 10, 64)
		if parseErr != nil || invID == 0 {
			return
		}
		amtStr := strings.TrimSpace(string(val))
		if amtStr == "" || amtStr == "0" || amtStr == "0.00" {
			return
		}
		amt, parseErr := decimal.NewFromString(amtStr)
		if parseErr != nil || !amt.IsPositive() {
			return
		}
		lines = append(lines, services.AllocationLine{InvoiceID: uint(invID), Amount: amt})
	})

	if len(lines) == 0 {
		return redirectErr(c, allocBase, "no allocation amounts entered")
	}

	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}

	if err := services.AllocateCustomerCreditToMultipleInvoices(s.DB, companyID, uint(creditID64), lines, actor); err != nil {
		return redirectErr(c, allocBase, err.Error())
	}
	return c.Redirect(allocBase+"?ok=1", fiber.StatusSeeOther)
}

// parseCustomerIDParam parses :id from the route into a uint.
func parseCustomerIDParam(c *fiber.Ctx) (uint, error) {
	id64, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || id64 == 0 {
		return 0, err
	}
	return uint(id64), nil
}

// creditErrMessage translates service errors into user-facing strings.
func creditErrMessage(err error) string {
	switch {
	case err == services.ErrCreditNotFound:
		return "Credit not found."
	case err == services.ErrCreditExhausted:
		return "This credit has already been fully used."
	case err == services.ErrCreditAmountInvalid:
		return "Amount must be greater than zero."
	case err == services.ErrCreditExceedsBalance:
		return "Amount exceeds the credit remaining balance."
	case err == services.ErrCreditExceedsInvoice:
		return "Amount exceeds the invoice balance due."
	case err == services.ErrCreditCurrencyMismatch:
		return "Currency mismatch: credit and invoice must use the same currency."
	case err == services.ErrCreditCustomerMismatch:
		return "This credit belongs to a different customer."
	case err == services.ErrCreditChannelInvoice:
		return "Channel-origin invoices cannot receive credit applications."
	case err == services.ErrCreditInvoiceStatus:
		return "Invoice status does not allow credit application."
	default:
		return "Failed to apply credit: " + err.Error()
	}
}
