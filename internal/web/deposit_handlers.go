// 遵循project_guide.md
package web

import (
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
)

// ── List ─────────────────────────────────────────────────────────────────────

func (s *Server) handleDeposits(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	filterStatus := strings.TrimSpace(c.Query("status"))
	filterCustomer := strings.TrimSpace(c.Query("customer_id"))
	filterFromStr := strings.TrimSpace(c.Query("from"))
	filterToStr := strings.TrimSpace(c.Query("to"))

	var customerID uint
	if filterCustomer != "" {
		if id, err := strconv.ParseUint(filterCustomer, 10, 64); err == nil {
			customerID = uint(id)
		}
	}

	dateFrom, dateTo := parseListDateRange(filterFromStr, filterToStr)

	deposits, err := services.ListCustomerDeposits(s.DB, companyID, services.CustomerDepositListFilter{
		Status:     filterStatus,
		CustomerID: customerID,
		DateFrom:   dateFrom,
		DateTo:     dateTo,
	})
	if err != nil {
		deposits = nil
	}

	customerLabel := lookupCustomerName(s.DB, companyID, customerID)

	return pages.Deposits(pages.DepositsVM{
		HasCompany:          true,
		Deposits:            deposits,
		FilterStatus:        filterStatus,
		FilterCustomer:      filterCustomer,
		FilterCustomerLabel: customerLabel,
		FilterDateFrom:      filterFromStr,
		FilterDateTo:        filterToStr,
		Created:             c.Query("created") == "1",
		Saved:               c.Query("saved") == "1",
	}).Render(c.Context(), c)
}

// ── New form ──────────────────────────────────────────────────────────────────

func (s *Server) handleDepositNew(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	vm := pages.DepositDetailVM{HasCompany: true}
	vm.Deposit.DepositDate = time.Now()
	s.loadDepositFormData(companyID, &vm)
	return pages.DepositDetail(vm).Render(c.Context(), c)
}

// ── Detail ────────────────────────────────────────────────────────────────────

func (s *Server) handleDepositDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/deposits", fiber.StatusSeeOther)
	}

	dep, err := services.GetCustomerDeposit(s.DB, companyID, id)
	if err != nil {
		return c.Redirect("/deposits", fiber.StatusSeeOther)
	}

	vm := pages.DepositDetailVM{
		HasCompany: true,
		Deposit:    *dep,
		Saved:      c.Query("saved") == "1",
		Posted:     c.Query("posted") == "1",
		Voided:     c.Query("voided") == "1",
		Applied:    c.Query("applied") == "1",
	}
	s.loadDepositFormData(companyID, &vm)
	return pages.DepositDetail(vm).Render(c.Context(), c)
}

// ── Save (create / update) ────────────────────────────────────────────────────

func (s *Server) handleDepositSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	depositIDStr := strings.TrimSpace(c.FormValue("deposit_id"))
	var depositID uint
	if depositIDStr != "" {
		if id, err := strconv.ParseUint(depositIDStr, 10, 64); err == nil {
			depositID = uint(id)
		}
	}

	in, err := parseDepositInput(c)
	if err != nil {
		vm := pages.DepositDetailVM{HasCompany: true, FormError: err.Error()}
		if depositID > 0 {
			if dep, e := services.GetCustomerDeposit(s.DB, companyID, depositID); e == nil {
				vm.Deposit = *dep
			}
		}
		s.loadDepositFormData(companyID, &vm)
		return pages.DepositDetail(vm).Render(c.Context(), c)
	}

	if depositID == 0 {
		dep, err := services.CreateCustomerDeposit(s.DB, companyID, in)
		_ = producers.ProjectCustomerDeposit(c.Context(), s.DB, s.SearchProjector, companyID, dep.ID)
		if err != nil {
			vm := pages.DepositDetailVM{HasCompany: true, FormError: err.Error()}
			s.loadDepositFormData(companyID, &vm)
			return pages.DepositDetail(vm).Render(c.Context(), c)
		}
		return c.Redirect("/deposits/"+strconv.FormatUint(uint64(dep.ID), 10)+"?created=1", fiber.StatusSeeOther)
	}

	_, err = services.UpdateCustomerDeposit(s.DB, companyID, depositID, in)
	_ = producers.ProjectCustomerDeposit(c.Context(), s.DB, s.SearchProjector, companyID, depositID)
	if err != nil {
		vm := pages.DepositDetailVM{HasCompany: true, FormError: err.Error()}
		if dep, e := services.GetCustomerDeposit(s.DB, companyID, depositID); e == nil {
			vm.Deposit = *dep
		}
		s.loadDepositFormData(companyID, &vm)
		return pages.DepositDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/deposits/"+strconv.FormatUint(uint64(depositID), 10)+"?saved=1", fiber.StatusSeeOther)
}

// ── Post ──────────────────────────────────────────────────────────────────────

func (s *Server) handleDepositPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/deposits", fiber.StatusSeeOther)
	}

	actor, actorID := depositActor(c)
	if postErr := services.PostCustomerDeposit(s.DB, companyID, id, actor, actorID); postErr != nil {
		dep, _ := services.GetCustomerDeposit(s.DB, companyID, id)
		vm := pages.DepositDetailVM{HasCompany: true, FormError: postErr.Error()}
		if dep != nil {
			vm.Deposit = *dep
		}
		s.loadDepositFormData(companyID, &vm)
		return pages.DepositDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectCustomerDeposit(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/deposits/"+strconv.FormatUint(uint64(id), 10)+"?posted=1", fiber.StatusSeeOther)
}

// ── Apply ─────────────────────────────────────────────────────────────────────

func (s *Server) handleDepositApply(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/deposits", fiber.StatusSeeOther)
	}

	invoiceIDStr := strings.TrimSpace(c.FormValue("invoice_id"))
	invoiceID, _ := strconv.ParseUint(invoiceIDStr, 10, 64)
	amountStr := strings.TrimSpace(c.FormValue("amount_applied"))
	amount, _ := decimal.NewFromString(amountStr)

	_, actorID := depositActor(c)
	in := services.ApplyDepositInput{
		DepositID:     id,
		InvoiceID:     uint(invoiceID),
		AmountApplied: amount,
	}

	if applyErr := services.ApplyDepositToInvoice(s.DB, companyID, in, actorID); applyErr != nil {
		dep, _ := services.GetCustomerDeposit(s.DB, companyID, id)
		vm := pages.DepositDetailVM{HasCompany: true, FormError: applyErr.Error()}
		if dep != nil {
			vm.Deposit = *dep
		}
		s.loadDepositFormData(companyID, &vm)
		return pages.DepositDetail(vm).Render(c.Context(), c)
	}
	return c.Redirect("/deposits/"+strconv.FormatUint(uint64(id), 10)+"?applied=1", fiber.StatusSeeOther)
}

// ── Void ──────────────────────────────────────────────────────────────────────

func (s *Server) handleDepositVoid(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := parseIDParam(c)
	if err != nil {
		return c.Redirect("/deposits", fiber.StatusSeeOther)
	}

	actor, _ := depositActor(c)
	if voidErr := services.VoidCustomerDeposit(s.DB, companyID, id, actor); voidErr != nil {
		dep, _ := services.GetCustomerDeposit(s.DB, companyID, id)
		vm := pages.DepositDetailVM{HasCompany: true, FormError: voidErr.Error()}
		if dep != nil {
			vm.Deposit = *dep
		}
		s.loadDepositFormData(companyID, &vm)
		return pages.DepositDetail(vm).Render(c.Context(), c)
	}
	_ = producers.ProjectCustomerDeposit(c.Context(), s.DB, s.SearchProjector, companyID, id)
	return c.Redirect("/deposits/"+strconv.FormatUint(uint64(id), 10)+"?voided=1", fiber.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// depositActor returns (email string, *uuid.UUID) for the logged-in user.
func depositActor(c *fiber.Ctx) (string, *uuid.UUID) {
	user := UserFromCtx(c)
	if user == nil {
		return "system", nil
	}
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	uid := user.ID
	return actor, &uid
}

func (s *Server) loadDepositFormData(companyID uint, vm *pages.DepositDetailVM) {
	vm.Customers, _ = s.customersForCompany(companyID)
	s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("code asc").Find(&vm.Accounts)

	// Load open invoices for this customer (for apply form).
	if vm.Deposit.CustomerID != 0 {
		s.DB.Where("company_id = ? AND customer_id = ? AND status IN ? AND balance_due > 0",
			companyID, vm.Deposit.CustomerID,
			[]models.InvoiceStatus{models.InvoiceStatusSent, models.InvoiceStatusPartiallyPaid},
		).Order("invoice_date asc").Find(&vm.Invoices)
	}
}

func parseDepositInput(c *fiber.Ctx) (services.CustomerDepositInput, error) {
	customerIDStr := strings.TrimSpace(c.FormValue("customer_id"))
	if customerIDStr == "" {
		return services.CustomerDepositInput{}, fiber.NewError(fiber.StatusBadRequest, "customer is required")
	}
	cid, err := strconv.ParseUint(customerIDStr, 10, 64)
	if err != nil || cid == 0 {
		return services.CustomerDepositInput{}, fiber.NewError(fiber.StatusBadRequest, "invalid customer")
	}

	depositDateStr := strings.TrimSpace(c.FormValue("deposit_date"))
	depositDate := time.Now()
	if depositDateStr != "" {
		if d, e := time.Parse("2006-01-02", depositDateStr); e == nil {
			depositDate = d
		}
	}

	amountStr := strings.TrimSpace(c.FormValue("amount"))
	amount, _ := decimal.NewFromString(amountStr)

	in := services.CustomerDepositInput{
		CustomerID:    uint(cid),
		DepositDate:   depositDate,
		CurrencyCode:  strings.ToUpper(strings.TrimSpace(c.FormValue("currency_code"))),
		Amount:        amount,
		PaymentMethod: models.PaymentMethod(strings.TrimSpace(c.FormValue("payment_method"))),
		Reference:     strings.TrimSpace(c.FormValue("reference")),
		Memo:          strings.TrimSpace(c.FormValue("memo")),
	}

	if bankStr := strings.TrimSpace(c.FormValue("bank_account_id")); bankStr != "" {
		if bid, e := strconv.ParseUint(bankStr, 10, 64); e == nil && bid > 0 {
			uid := uint(bid)
			in.BankAccountID = &uid
		}
	}
	if liabStr := strings.TrimSpace(c.FormValue("deposit_liability_account_id")); liabStr != "" {
		if lid, e := strconv.ParseUint(liabStr, 10, 64); e == nil && lid > 0 {
			uid := uint(lid)
			in.DepositLiabilityAccountID = &uid
		}
	}

	return in, nil
}
