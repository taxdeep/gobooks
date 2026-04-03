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

// ── Gateway Accounts ─────────────────────────────────────────────────────────

func (s *Server) handlePaymentGateways(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	summaries, _ := services.ListGatewayAccountSummaries(s.DB, companyID)
	vm := pages.PaymentGatewaysVM{
		HasCompany: true,
		Accounts:   summaries,
		Created:    c.Query("created") == "1",
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
	if providerType == "" || displayName == "" {
		return c.Redirect("/settings/payment-gateways", fiber.StatusSeeOther)
	}
	if err := services.CreateGatewayAccount(s.DB, &models.PaymentGatewayAccount{
		CompanyID: companyID, ProviderType: models.PaymentProviderType(providerType),
		DisplayName: displayName, ExternalAccountRef: extRef,
		AuthStatus: "pending", WebhookStatus: "not_configured", IsActive: true,
	}); err != nil {
		return c.Redirect("/settings/payment-gateways?createerror=1", fiber.StatusSeeOther)
	}
	return c.Redirect("/settings/payment-gateways?created=1", fiber.StatusSeeOther)
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
		return c.Redirect("/settings/payment-gateways/mappings", fiber.StatusSeeOther)
	}
	m := models.PaymentAccountingMapping{
		CompanyID:           companyID,
		GatewayAccountID:    uint(gwID),
		ClearingAccountID:   parseOptionalUint(c.FormValue("clearing_account_id")),
		FeeExpenseAccountID: parseOptionalUint(c.FormValue("fee_expense_account_id")),
		RefundAccountID:     parseOptionalUint(c.FormValue("refund_account_id")),
		PayoutBankAccountID: parseOptionalUint(c.FormValue("payout_bank_account_id")),
	}
	if err := services.SavePaymentAccountingMapping(s.DB, &m); err != nil {
		return c.Redirect("/settings/payment-gateways/mappings?saveerror=1", fiber.StatusSeeOther)
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
		Created: c.Query("created") == "1",
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
	amt, _ := decimal.NewFromString(amtRaw)
	currency := strings.TrimSpace(c.FormValue("currency_code"))
	desc := strings.TrimSpace(c.FormValue("description"))
	status := strings.TrimSpace(c.FormValue("status"))
	if status == "" {
		status = string(models.PaymentRequestDraft)
	}
	if err := services.CreatePaymentRequest(s.DB, &models.PaymentRequest{
		CompanyID: companyID, GatewayAccountID: uint(gwID),
		Amount: amt, CurrencyCode: currency, Status: models.PaymentRequestStatus(status),
		Description: desc,
	}); err != nil {
		return c.Redirect("/settings/payment-gateways/requests?createerror=1", fiber.StatusSeeOther)
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

	vm := pages.PaymentTransactionsVM{
		HasCompany: true, Transactions: txns, Accounts: accounts,
		Created: c.Query("created") == "1", JustPosted: c.Query("posted") == "1",
		TxnStates: txnStates,
		JustApplied: c.Query("applied") == "1", JustRefundApplied: c.Query("refundapplied") == "1",
		JustUnapplied: c.Query("unapplied") == "1",
	}
	return pages.PaymentTransactions(vm).Render(c.Context(), c)
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
		return c.Redirect("/settings/payment-gateways/transactions?applyerr=1", fiber.StatusSeeOther)
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
		return c.Redirect("/settings/payment-gateways/transactions?unapplyerr=1", fiber.StatusSeeOther)
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
		return c.Redirect("/settings/payment-gateways/transactions?refunderr=1", fiber.StatusSeeOther)
	}
	return c.Redirect("/settings/payment-gateways/transactions?refundapplied=1", fiber.StatusSeeOther)
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
		return c.Redirect("/settings/payment-gateways/transactions?posterr=1", fiber.StatusSeeOther)
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
	amt, _ := decimal.NewFromString(amtRaw)
	currency := strings.TrimSpace(c.FormValue("currency_code"))
	status := strings.TrimSpace(c.FormValue("status"))
	extRef := strings.TrimSpace(c.FormValue("external_txn_ref"))
	if status == "" {
		status = "completed"
	}
	services.CreatePaymentTransaction(s.DB, &models.PaymentTransaction{
		CompanyID: companyID, GatewayAccountID: uint(gwID),
		TransactionType: models.PaymentTransactionType(txnType),
		Amount: amt, CurrencyCode: currency, Status: status,
		ExternalTxnRef: extRef, RawPayload: datatypes.JSON("{}"),
	})
	return c.Redirect("/settings/payment-gateways/transactions?created=1", fiber.StatusSeeOther)
}
