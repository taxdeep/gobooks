// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── Payment Gateways Hub ──────────────────────────────────────────────────────

func (s *Server) handlePaymentGatewaysHub(c *fiber.Ctx) error {
	_, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.PaymentGatewaysHubVM{
		HasCompany: true,
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings", Href: "/settings"},
			{Label: "Payment Gateways"},
		},
	}
	return pages.PaymentGatewaysHub(vm).Render(c.Context(), c)
}

// ── Gateway Accounts ─────────────────────────────────────────────────────────

func (s *Server) handlePaymentGateways(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	summaries, _ := services.ListGatewayAccountSummaries(s.DB, companyID)
	vm := pages.PaymentGatewaysVM{
		HasCompany: true,
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings", Href: "/settings"},
			{Label: "Payment Gateways", Href: "/settings/payment-gateways"},
			{Label: "Processors"},
		},
		Accounts:  summaries,
		Created:   c.Query("created") == "1",
		FormError: strings.TrimSpace(c.Query("error")),
	}
	return pages.PaymentGateways(vm).Render(c.Context(), c)
}

func (s *Server) handlePaymentGatewayCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	providerType := strings.TrimSpace(c.FormValue("provider_type"))
	displayName := strings.TrimSpace(c.FormValue("display_name"))
	extRef := strings.TrimSpace(c.FormValue("external_account_ref"))
	webhookSecret := strings.TrimSpace(c.FormValue("webhook_secret"))
	if providerType == "" || displayName == "" {
		return redirectErr(c, "/settings/payment-gateways/processors", "provider type and display name are required")
	}
	webhookStatus := "not_configured"
	if webhookSecret != "" {
		webhookStatus = "configured"
	}
	if err := services.CreateGatewayAccount(s.DB, &models.PaymentGatewayAccount{
		CompanyID: companyID, ProviderType: models.PaymentProviderType(providerType),
		DisplayName: displayName, ExternalAccountRef: extRef,
		WebhookSecret: webhookSecret,
		AuthStatus: "pending", WebhookStatus: webhookStatus, IsActive: true,
	}); err != nil {
		return redirectErr(c, "/settings/payment-gateways/processors", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/processors?created=1", fiber.StatusSeeOther)
}

// handlePaymentGatewayUpdateWebhook updates the webhook signing secret for a
// payment gateway account.
// POST /settings/payment-gateways/:id/update-webhook
//
// Only updates webhook_secret and webhook_status; does not change other gateway
// fields. An empty secret clears the secret and resets webhook_status to "not_configured".
func (s *Server) handlePaymentGatewayUpdateWebhook(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return redirectErr(c, "/settings/payment-gateways", "invalid gateway id")
	}
	// Verify ownership before update.
	var gw models.PaymentGatewayAccount
	if err := s.DB.Where("id = ? AND company_id = ?", uint(id), companyID).First(&gw).Error; err != nil {
		return redirectErr(c, "/settings/payment-gateways", "gateway not found")
	}
	secret := strings.TrimSpace(c.FormValue("webhook_secret"))
	webhookStatus := "not_configured"
	if secret != "" {
		webhookStatus = "configured"
	}
	if err := s.DB.Model(&gw).Updates(map[string]any{
		"webhook_secret": secret,
		"webhook_status": webhookStatus,
	}).Error; err != nil {
		return redirectErr(c, "/settings/payment-gateways", err.Error())
	}
	return c.Redirect("/settings/payment-gateways?webhookUpdated=1", fiber.StatusSeeOther)
}

// ── Payment Accounting Mappings ──────────────────────────────────────────────

func (s *Server) handlePaymentMappings(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	accounts, _ := services.ListGatewayAccounts(s.DB, companyID)
	var allAccounts []models.Account
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("code ASC").Find(&allAccounts)
	mappings := make(map[uint]*models.PaymentAccountingMapping)
	for _, a := range accounts {
		m, _ := services.GetPaymentAccountingMapping(s.DB, companyID, a.ID)
		if m != nil {
			mappings[a.ID] = m
		}
	}
	vm := pages.PaymentMappingsVM{
		HasCompany: true, GatewayAccounts: accounts, GLAccounts: allAccounts,
		Mappings: mappings, Saved: c.Query("saved") == "1",
		FormError: strings.TrimSpace(c.Query("error")),
	}
	return pages.PaymentMappings(vm).Render(c.Context(), c)
}

func (s *Server) handlePaymentMappingSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	gwID, _ := strconv.ParseUint(c.FormValue("gateway_account_id"), 10, 64)
	if gwID == 0 {
		return redirectErr(c, "/settings/payment-gateways/mappings", "gateway account is required")
	}
	m := models.PaymentAccountingMapping{
		CompanyID:           companyID,
		GatewayAccountID:    uint(gwID),
		ClearingAccountID:   parseOptionalUint(c.FormValue("clearing_account_id")),
		FeeExpenseAccountID: parseOptionalUint(c.FormValue("fee_expense_account_id")),
		RefundAccountID:     parseOptionalUint(c.FormValue("refund_account_id")),
		PayoutBankAccountID: parseOptionalUint(c.FormValue("payout_bank_account_id")),
		ChargebackAccountID: parseOptionalUint(c.FormValue("chargeback_account_id")),
	}
	if err := services.SavePaymentAccountingMapping(s.DB, &m); err != nil {
		return redirectErr(c, "/settings/payment-gateways/mappings", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/mappings?saved=1", fiber.StatusSeeOther)
}

// ── Payment Requests ─────────────────────────────────────────────────────────

func (s *Server) handlePaymentRequests(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	reqs, _ := services.ListPaymentRequests(s.DB, companyID, 50)
	accounts, _ := services.ListGatewayAccounts(s.DB, companyID)
	vm := pages.PaymentRequestsVM{
		HasCompany: true, Requests: reqs, Accounts: accounts,
		Created:   c.Query("created") == "1",
		FormError: strings.TrimSpace(c.Query("error")),
	}
	return pages.PaymentRequests(vm).Render(c.Context(), c)
}

func (s *Server) handlePaymentRequestCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	gwID, _ := strconv.ParseUint(c.FormValue("gateway_account_id"), 10, 64)
	amtRaw := strings.TrimSpace(c.FormValue("amount"))
	amt := decimal.Zero
	if amtRaw != "" {
		var err error
		amt, err = decimal.NewFromString(amtRaw)
		if err != nil {
			return redirectErr(c, "/settings/payment-gateways/requests", "amount must be a valid number")
		}
	}
	currency := strings.TrimSpace(c.FormValue("currency_code"))
	desc := strings.TrimSpace(c.FormValue("description"))
	status := strings.TrimSpace(c.FormValue("status"))
	if status == "" {
		status = string(models.PaymentRequestPending)
	}
	if gwID == 0 {
		return redirectErr(c, "/settings/payment-gateways/requests", "gateway account is required")
	}
	if err := services.CreatePaymentRequest(s.DB, &models.PaymentRequest{
		CompanyID: companyID, GatewayAccountID: uint(gwID),
		Amount: amt, CurrencyCode: currency, Status: models.PaymentRequestStatus(status),
		Description: desc,
	}); err != nil {
		return redirectErr(c, "/settings/payment-gateways/requests", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/requests?created=1", fiber.StatusSeeOther)
}

// ── Payment Transactions ─────────────────────────────────────────────────────

func (s *Server) handlePaymentTransactions(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	txns, _ := services.ListPaymentTransactions(s.DB, companyID, 50)
	accounts, _ := services.ListGatewayAccounts(s.DB, companyID)

	txnStates := make(map[uint]services.PaymentActionState)
	for _, t := range txns {
		txnStates[t.ID] = services.ComputePaymentActionState(s.DB, companyID, t)
	}

	// Batch 22: load reverse allocations for any txn that is reverse-allocated.
	reverseAllocs := make(map[uint][]models.PaymentReverseAllocation)
	for txnID, st := range txnStates {
		if st.IsReverseAllocated {
			rows, _ := services.ListReverseAllocationsForTxn(s.DB, companyID, txnID)
			reverseAllocs[txnID] = rows
		}
	}

	vm := pages.PaymentTransactionsVM{
		HasCompany: true, Transactions: txns, Accounts: accounts,
		Created: c.Query("created") == "1", JustPosted: c.Query("posted") == "1",
		TxnStates:   txnStates,
		JustApplied: c.Query("applied") == "1", JustRefundApplied: c.Query("refundapplied") == "1",
		JustChargebackApplied: c.Query("chargebackapplied") == "1",
		JustUnapplied:         c.Query("unapplied") == "1",
		JustReverseAllocApplied: c.Query("revalloc") == "1",
		FormError:             strings.TrimSpace(c.Query("error")),
		ReverseAllocations:    reverseAllocs,
	}
	return pages.PaymentTransactions(vm).Render(c.Context(), c)
}

// handlePaymentTransactionApplyRefundMultiAlloc applies a refund transaction
// via the multi-alloc reverse allocation path (Batch 22).
// POST /settings/payment-gateways/transactions/:id/apply-refund-multialloc
func (s *Server) handlePaymentTransactionApplyRefundMultiAlloc(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	applyErr := services.ApplyRefundReverseAllocations(s.DB, companyID, uint(id), actor)
	if applyErr != nil {
		// Batch 23: structural errors create a payment reverse exception record.
		if exType, ok := services.PaymentReverseExceptionTypeForReverseAllocError(applyErr); ok {
			txnID := uint(id)
			inp := services.CreatePaymentReverseExceptionInput{
				CompanyID:      companyID,
				ExceptionType:  exType,
				ReverseTxnID:   &txnID,
				Summary:        applyErr.Error(),
				CreatedByActor: actor,
			}
			if ex, _, err := services.CreatePaymentReverseException(s.DB, inp); err == nil && ex != nil {
				return c.Redirect("/settings/payment-gateways/reverse-exceptions/"+strconv.FormatUint(uint64(ex.ID), 10), fiber.StatusSeeOther)
			}
		}
		return redirectErr(c, "/settings/payment-gateways/transactions", applyErr.Error())
	}
	return c.Redirect("/settings/payment-gateways/transactions?revalloc=1", fiber.StatusSeeOther)
}

// handlePaymentTransactionApplyChargebackMultiAlloc applies a chargeback
// transaction via the multi-alloc reverse allocation path (Batch 22).
// POST /settings/payment-gateways/transactions/:id/apply-chargeback-multialloc
func (s *Server) handlePaymentTransactionApplyChargebackMultiAlloc(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	applyErr := services.ApplyChargebackReverseAllocations(s.DB, companyID, uint(id), actor)
	if applyErr != nil {
		// Batch 23: structural errors create a payment reverse exception record.
		if exType, ok := services.PaymentReverseExceptionTypeForReverseAllocError(applyErr); ok {
			txnID := uint(id)
			inp := services.CreatePaymentReverseExceptionInput{
				CompanyID:      companyID,
				ExceptionType:  exType,
				ReverseTxnID:   &txnID,
				Summary:        applyErr.Error(),
				CreatedByActor: actor,
			}
			if ex, _, err := services.CreatePaymentReverseException(s.DB, inp); err == nil && ex != nil {
				return c.Redirect("/settings/payment-gateways/reverse-exceptions/"+strconv.FormatUint(uint64(ex.ID), 10), fiber.StatusSeeOther)
			}
		}
		return redirectErr(c, "/settings/payment-gateways/transactions", applyErr.Error())
	}
	return c.Redirect("/settings/payment-gateways/transactions?revalloc=1", fiber.StatusSeeOther)
}

func (s *Server) handlePaymentTransactionApply(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	err := services.ApplyPaymentTransactionToInvoice(s.DB, companyID, uint(id), actor)
	if err != nil {
		return redirectErr(c, "/settings/payment-gateways/transactions", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/transactions?applied=1", fiber.StatusSeeOther)
}

func (s *Server) handlePaymentTransactionUnapply(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	err := services.UnapplyPaymentTransaction(s.DB, companyID, uint(id), actor)
	if err != nil {
		return redirectErr(c, "/settings/payment-gateways/transactions", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/transactions?unapplied=1", fiber.StatusSeeOther)
}

func (s *Server) handlePaymentTransactionApplyRefund(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	err := services.ApplyRefundTransactionToInvoice(s.DB, companyID, uint(id), actor)
	if err != nil {
		return redirectErr(c, "/settings/payment-gateways/transactions", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/transactions?refundapplied=1", fiber.StatusSeeOther)
}

func (s *Server) handlePaymentTransactionApplyChargeback(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	err := services.ApplyChargebackTransactionToInvoice(s.DB, companyID, uint(id), actor)
	if err != nil {
		return redirectErr(c, "/settings/payment-gateways/transactions", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/transactions?chargebackapplied=1", fiber.StatusSeeOther)
}

func (s *Server) handlePaymentTransactionPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	_, err := services.PostPaymentTransactionToJournalEntry(s.DB, companyID, uint(id), actor)
	if err != nil {
		return redirectErr(c, "/settings/payment-gateways/transactions", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/transactions?posted=1", fiber.StatusSeeOther)
}

func (s *Server) handlePaymentTransactionCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	gwID, _ := strconv.ParseUint(c.FormValue("gateway_account_id"), 10, 64)
	txnType := strings.TrimSpace(c.FormValue("transaction_type"))
	amtRaw := strings.TrimSpace(c.FormValue("amount"))
	amt := decimal.Zero
	if amtRaw != "" {
		var err error
		amt, err = decimal.NewFromString(amtRaw)
		if err != nil {
			return redirectErr(c, "/settings/payment-gateways/transactions", "amount must be a valid number")
		}
	}
	currency := strings.TrimSpace(c.FormValue("currency_code"))
	status := strings.TrimSpace(c.FormValue("status"))
	extRef := strings.TrimSpace(c.FormValue("external_txn_ref"))
	if status == "" {
		status = "completed"
	}
	if gwID == 0 {
		return redirectErr(c, "/settings/payment-gateways/transactions", "gateway account is required")
	}
	if txnType == "" {
		return redirectErr(c, "/settings/payment-gateways/transactions", "transaction type is required")
	}
	if err := services.CreatePaymentTransaction(s.DB, &models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: uint(gwID),
		TransactionType: models.PaymentTransactionType(txnType),
		Amount:          amt, CurrencyCode: currency, Status: status,
		ExternalTxnRef: extRef, RawPayload: datatypes.JSON("{}"),
	}); err != nil {
		return redirectErr(c, "/settings/payment-gateways/transactions", err.Error())
	}
	return c.Redirect("/settings/payment-gateways/transactions?created=1", fiber.StatusSeeOther)
}

// ── Batch 17: Payment multi-allocation ───────────────────────────────────────

// handlePaymentMultiAllocateForm renders the multi-invoice allocation form for
// a payment transaction.
// GET /settings/payment-gateways/transactions/:id/allocate
func (s *Server) handlePaymentMultiAllocateForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	txnID, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || txnID == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}

	var txn models.PaymentTransaction
	if err := s.DB.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return redirectErr(c, "/settings/payment-gateways/transactions", "transaction not found")
	}

	// Resolve customer via payment request.
	var customerID uint
	if txn.PaymentRequestID != nil {
		var req models.PaymentRequest
		if s.DB.Where("id = ? AND company_id = ?", *txn.PaymentRequestID, companyID).First(&req).Error == nil {
			if req.CustomerID != nil {
				customerID = *req.CustomerID
			} else if req.InvoiceID != nil {
				var inv models.Invoice
				if s.DB.Where("id = ? AND company_id = ?", *req.InvoiceID, companyID).First(&inv).Error == nil {
					customerID = inv.CustomerID
				}
			}
		}
	}

	allocated := services.PaymentAllocatedTotal(s.DB, companyID, uint(txnID))
	remaining := txn.Amount.Sub(allocated)

	existing, _ := services.ListPaymentAllocations(s.DB, companyID, uint(txnID))

	var invoiceRows []pages.AllocatableInvoiceRow
	if customerID > 0 {
		invs, _ := services.ListAllocatableInvoicesForCustomer(s.DB, companyID, customerID)
		// Exclude invoices already allocated in this txn.
		allocatedSet := make(map[uint]bool, len(existing))
		for _, a := range existing {
			allocatedSet[a.InvoiceID] = true
		}
		for _, inv := range invs {
			if !allocatedSet[inv.ID] {
				invoiceRows = append(invoiceRows, pages.AllocatableInvoiceRow{Invoice: inv})
			}
		}
	}

	vm := pages.PaymentAllocationVM{
		HasCompany:          true,
		Txn:                 txn,
		AlreadyAllocated:    allocated,
		Remaining:           remaining,
		Invoices:            invoiceRows,
		Success:             c.Query("ok") == "1",
		FormError:           strings.TrimSpace(c.Query("error")),
		ExistingAllocations: existing,
	}
	return pages.PaymentAllocation(vm).Render(c.Context(), c)
}

// handlePaymentMultiAllocateSubmit processes the multi-invoice allocation form.
// POST /settings/payment-gateways/transactions/:id/allocate
func (s *Server) handlePaymentMultiAllocateSubmit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	txnID, err := strconv.ParseUint(strings.TrimSpace(c.Params("id")), 10, 64)
	if err != nil || txnID == 0 {
		return c.Redirect("/settings/payment-gateways/transactions", fiber.StatusSeeOther)
	}
	allocBase := "/settings/payment-gateways/transactions/" + c.Params("id") + "/allocate"

	// Parse allocation lines from form fields named "amount_<invoiceID>".
	// Only include lines with a positive, non-zero amount.
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

	if err := services.AllocatePaymentToMultipleInvoices(s.DB, companyID, uint(txnID), lines, actor); err != nil {
		return redirectErr(c, allocBase, err.Error())
	}
	return c.Redirect(allocBase+"?ok=1", fiber.StatusSeeOther)
}
