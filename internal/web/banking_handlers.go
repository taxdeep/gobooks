// 遵循project_guide.md
package web

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// buildCandidatesJSON serialises reconcile candidates to a compact JSON array
// consumed by the Alpine reconcilePage() component.
func buildCandidatesJSON(cands []services.ReconcileCandidate) string {
	type item struct {
		ID     uint   `json:"id"`
		Amount string `json:"amount"`
	}
	items := make([]item, len(cands))
	for i, c := range cands {
		items[i] = item{ID: c.LineID, Amount: c.Amount.StringFixed(2)}
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func (s *Server) handleBankReconcileForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accounts, err := s.bankAccountsForCompany(companyID)
	if err != nil {
		return pages.BankReconcile(pages.BankReconcileVM{
			HasCompany: true,
			Accounts:   []models.Account{},
			Active:     "Bank Reconcile",
			FormError:  "Could not load accounts.",
		}).Render(c.Context(), c)
	}

	accountIDStr := strings.TrimSpace(c.Query("account_id"))
	statementDateStr := strings.TrimSpace(c.Query("statement_date"))
	endingBalanceStr := strings.TrimSpace(c.Query("ending_balance"))

	formError := ""
	if c.Query("void_error") == "1" {
		formError = "Could not void reconciliation. Please try again."
	}

	vm := pages.BankReconcileVM{
		HasCompany:          true,
		Accounts:            accounts,
		AccountID:           accountIDStr,
		StatementDate:       statementDateStr,
		EndingBalance:       endingBalanceStr,
		Active:              "Bank Reconcile",
		Saved:               c.Query("saved") == "1",
		Voided:              c.Query("voided") == "1",
		AutoMatchRan:        c.Query("auto_match") == "1",
		FormError:           formError,
		BeginningBalance:    "0.00",
		PreviouslyCleared:   "0.00",
		CandidatesJSON:      "[]",
		AcceptedLineIDsJSON: "[]",
		Candidates:          []services.ReconcileCandidate{},
	}

	if accountIDStr == "" {
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}

	accountIDU64, err := services.ParseUint(accountIDStr)
	if err != nil || accountIDU64 == 0 {
		vm.FormError = "Invalid account selected."
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}
	accountID := uint(accountIDU64)

	var accRow models.Account
	if err := s.DB.Where("id = ? AND company_id = ?", accountID, companyID).First(&accRow).Error; err != nil {
		vm.FormError = "Invalid account selected."
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}

	if statementDateStr == "" {
		statementDateStr = time.Now().Format("2006-01-02")
		vm.StatementDate = statementDateStr
	}
	statementDate, err := time.Parse("2006-01-02", statementDateStr)
	if err != nil {
		vm.FormError = "Statement Date must be a valid date."
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}
	vm.StatementDateTime = statementDate

	if endingBalanceStr == "" {
		endingBalanceStr = "0.00"
		vm.EndingBalance = endingBalanceStr
	}
	if _, err := services.ParseDecimalMoney(endingBalanceStr); err != nil {
		vm.FormError = "Ending Balance must be a number."
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}

	prev, err := services.ClearedBalance(s.DB, companyID, accountID, statementDate)
	if err != nil {
		vm.FormError = "Could not load cleared balance."
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}
	prevStr := pages.Money(prev)
	vm.PreviouslyCleared = prevStr
	vm.BeginningBalance = prevStr

	cands, err := services.ListReconcileCandidates(s.DB, companyID, accountID, statementDate)
	if err != nil {
		vm.FormError = "Could not load unreconciled transactions."
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}
	vm.Candidates = cands
	vm.CandidatesJSON = buildCandidatesJSON(cands)

	latest, err := services.LatestActiveReconciliation(s.DB, companyID, accountID)
	if err != nil {
		vm.FormError = "Could not load previous reconciliation."
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}
	vm.LatestReconciliation = latest

	// Load match-engine suggestions — pending (with actions) and accepted (with badge).
	// Accepted suggestions remain visible so the user can see what is driving the
	// pre-selected checkboxes; they show a static badge rather than action buttons.
	pendingSuggs, _ := services.LoadActiveSuggestions(s.DB, companyID, accountID)

	// Build candidate lookup for journal number enrichment.
	candidatesByLineID := make(map[uint]services.ReconcileCandidate, len(cands))
	for _, cd := range cands {
		candidatesByLineID[cd.LineID] = cd
	}
	vm.Suggestions = pages.BuildMatchSuggestionVMs(pendingSuggs, candidatesByLineID)
	vm.SuggestionCount = len(vm.Suggestions)

	// Pre-select lines from accepted suggestions.
	acceptedIDs, _ := services.LoadAcceptedLineIDs(s.DB, companyID, accountID)
	vm.AcceptedLineIDs = acceptedIDs
	if len(acceptedIDs) > 0 {
		b, _ := json.Marshal(acceptedIDs)
		vm.AcceptedLineIDsJSON = string(b)
	}

	return pages.BankReconcile(vm).Render(c.Context(), c)
}

func (s *Server) handleBankReconcileSubmit(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accountIDStr := strings.TrimSpace(c.FormValue("account_id"))
	statementDateStr := strings.TrimSpace(c.FormValue("statement_date"))
	endingBalanceStr := strings.TrimSpace(c.FormValue("ending_balance"))

	accountIDU64, err := services.ParseUint(accountIDStr)
	if err != nil || accountIDU64 == 0 {
		return c.Redirect("/banking/reconcile", fiber.StatusSeeOther)
	}
	accountID := uint(accountIDU64)

	if err := s.DB.Where("id = ? AND company_id = ?", accountID, companyID).First(new(models.Account)).Error; err != nil {
		return c.Redirect("/banking/reconcile", fiber.StatusSeeOther)
	}

	statementDate, err := time.Parse("2006-01-02", statementDateStr)
	if err != nil {
		return c.Redirect("/banking/reconcile?account_id="+accountIDStr, fiber.StatusSeeOther)
	}

	endingBalance, err := services.ParseDecimalMoney(endingBalanceStr)
	if err != nil {
		return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&statement_date="+statementDateStr, fiber.StatusSeeOther)
	}

	lineIDBytes := c.Context().PostArgs().PeekMulti("line_ids")
	lineIDs := make([]string, 0, len(lineIDBytes))
	for _, b := range lineIDBytes {
		lineIDs = append(lineIDs, string(b))
	}
	if len(lineIDs) == 0 {
		return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&statement_date="+statementDateStr+"&ending_balance="+endingBalanceStr, fiber.StatusSeeOther)
	}

	var ids []uint
	for _, sID := range lineIDs {
		u, err := services.ParseUint(sID)
		if err != nil || u == 0 {
			continue
		}
		ids = append(ids, uint(u))
	}
	if len(ids) == 0 {
		return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&statement_date="+statementDateStr+"&ending_balance="+endingBalanceStr, fiber.StatusSeeOther)
	}

	decimalZero := decimal.NewFromInt(0)

	var savedRecID uint
	var clearedSnapshot decimal.Decimal
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		prevCleared, err := services.ClearedBalance(tx, companyID, accountID, statementDate)
		if err != nil {
			return err
		}

		type row struct{ Amount decimal.Decimal }
		var r row
		if err := tx.Raw(
			`
SELECT COALESCE(SUM(jl.debit - jl.credit), 0) AS amount
FROM journal_lines jl
JOIN journal_entries je ON je.id = jl.journal_entry_id
WHERE jl.id IN ?
  AND jl.account_id = ?
  AND jl.company_id = ?
  AND jl.reconciliation_id IS NULL
  AND je.entry_date <= ?
  AND je.company_id = ?
`,
			ids, accountID, companyID, statementDate, companyID,
		).Scan(&r).Error; err != nil {
			return err
		}

		cleared := prevCleared.Add(r.Amount)
		clearedSnapshot = cleared
		diff := endingBalance.Sub(cleared)
		if !diff.Equal(decimalZero) {
			return errors.New("difference not zero")
		}

		rec := models.Reconciliation{
			CompanyID:      companyID,
			AccountID:      accountID,
			StatementDate:  statementDate,
			EndingBalance:  endingBalance,
			ClearedBalance: cleared,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
		savedRecID = rec.ID

		now := time.Now()
		if err := tx.Model(&models.JournalLine{}).
			Where("id IN ?", ids).
			Where("account_id = ?", accountID).
			Where("company_id = ?", companyID).
			Where("reconciliation_id IS NULL").
			Updates(map[string]any{
				"reconciliation_id": rec.ID,
				"reconciled_at":     &now,
			}).Error; err != nil {
			return err
		}

		return nil
	}); err != nil {
		return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&statement_date="+statementDateStr+"&ending_balance="+endingBalanceStr, fiber.StatusSeeOther)
	}

	// Link accepted suggestions to the completed reconciliation for cross-reference.
	// Best-effort: a failure here does not roll back the reconciliation itself.
	_ = services.LinkSuggestionsToReconciliation(s.DB, companyID, accountID, savedRecID)

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "banking.reconciliation.completed", "reconciliation", savedRecID, actor, map[string]any{
		"account_id":      accountID,
		"statement_date":  statementDateStr,
		"line_count":      len(ids),
		"ending_balance":  endingBalance.StringFixed(2),
		"cleared_balance": clearedSnapshot.StringFixed(2),
		"company_id":      companyID,
	}, &cid, &uid)

	return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&statement_date="+statementDateStr+"&ending_balance="+endingBalanceStr+"&saved=1", fiber.StatusSeeOther)
}

func (s *Server) handleReceivePaymentForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	var customers []models.Customer
	_ = s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&customers).Error

	bankAccounts, _ := s.bankAccountsForCompany(companyID)

	vm := pages.ReceivePaymentVM{
		HasCompany:       true,
		Customers:        customers,
		BankAccounts:     bankAccounts,
		Saved:            c.Query("saved") == "1",
		EntryDate:        time.Now().Format("2006-01-02"),
		OpenInvoicesJSON: buildOpenInvoicesJSON(s, companyID),
	}

	return pages.ReceivePayment(vm).Render(c.Context(), c)
}

func (s *Server) handleReceivePaymentSubmit(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	var customers []models.Customer
	_ = s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&customers).Error
	bankAccounts, _ := s.bankAccountsForCompany(companyID)

	customerIDRaw := strings.TrimSpace(c.FormValue("customer_id"))
	paymentMethodRaw := strings.TrimSpace(c.FormValue("payment_method"))
	entryDateRaw := strings.TrimSpace(c.FormValue("entry_date"))
	bankIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	invoiceIDRaw := strings.TrimSpace(c.FormValue("invoice_id"))
	amountRaw := strings.TrimSpace(c.FormValue("amount"))
	memo := strings.TrimSpace(c.FormValue("memo"))

	vm := pages.ReceivePaymentVM{
		HasCompany:       true,
		Customers:        customers,
		BankAccounts:     bankAccounts,
		OpenInvoicesJSON: buildOpenInvoicesJSON(s, companyID),
		PaymentMethod:    paymentMethodRaw,
		CustomerID:       customerIDRaw,
		EntryDate:        entryDateRaw,
		BankAccountID:    bankIDRaw,
		InvoiceID:        invoiceIDRaw,
		Amount:           amountRaw,
		Memo:             memo,
	}

	custU64, err := services.ParseUint(customerIDRaw)
	if err != nil || custU64 == 0 {
		vm.CustomerError = "Customer is required."
	}
	paymentMethod, err := models.ParsePaymentMethod(paymentMethodRaw)
	if err != nil || !models.IsManualPaymentMethod(paymentMethod) {
		vm.PaymentMethodError = "Payment method is required."
	}

	entryDate, err := time.Parse("2006-01-02", entryDateRaw)
	if err != nil {
		vm.DateError = "Date is required."
	}

	bankU64, err := services.ParseUint(bankIDRaw)
	if err != nil || bankU64 == 0 {
		vm.BankError = "Bank account is required."
	}

	amount, err := services.ParseDecimalMoney(amountRaw)
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		vm.AmountError = "Amount must be greater than 0."
	}

	// Auto-resolve the Accounts Receivable account for this company.
	arU64, arErr := s.defaultARAccountID(companyID)
	if arErr != nil {
		vm.ARError = "No Accounts Receivable account found. Please add one to your Chart of Accounts."
	}

	if vm.CustomerError != "" || vm.PaymentMethodError != "" || vm.DateError != "" || vm.BankError != "" || vm.ARError != "" || vm.AmountError != "" {
		return pages.ReceivePayment(vm).Render(c.Context(), c)
	}

	var invoiceIDPtr *uint
	if invoiceIDRaw != "" && invoiceIDRaw != "0" {
		if invU64, err := services.ParseUint(invoiceIDRaw); err == nil && invU64 > 0 {
			id := uint(invU64)
			invoiceIDPtr = &id
		}
	}

	var jeID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		input := services.ReceivePaymentInput{
			CompanyID:     companyID,
			CustomerID:    uint(custU64),
			EntryDate:     entryDate,
			BankAccountID: uint(bankU64),
			PaymentMethod: paymentMethod,
			ARAccountID:   arU64,
			Amount:        amount,
			Memo:          memo,
		}
		if invoiceIDPtr != nil {
			input.Allocations = []services.InvoiceAllocation{{
				InvoiceID: *invoiceIDPtr,
				Amount:    amount,
			}}
		}
		jeID, txErr = services.RecordReceivePayment(tx, input)
		return txErr
	}); err != nil {
		vm.FormError = "Could not record payment: " + err.Error()
		return pages.ReceivePayment(vm).Render(c.Context(), c)
	}

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "payment.received", "journal_entry", jeID, actor, map[string]any{
		"customer_id":    customerIDRaw,
		"amount":         amount.StringFixed(2),
		"payment_method": string(paymentMethod),
		"entry_date":     entryDateRaw,
		"company_id":     companyID,
	}, &cid, &uid)

	return c.Redirect("/banking/receive-payment?saved=1", fiber.StatusSeeOther)
}

// buildOpenInvoicesJSON returns a JSON array of open invoices for the company,
// used by the Receive Payment Alpine component to filter by customer.
// Fields: id, customer_id, invoice_number, invoice_date, original_amount, amount (balance due), due_date
func buildOpenInvoicesJSON(s *Server, companyID uint) string {
	type invJSON struct {
		ID             uint   `json:"id"`
		CustomerID     uint   `json:"customer_id"`
		InvoiceNumber  string `json:"invoice_number"`
		InvoiceDate    string `json:"invoice_date"`
		OriginalAmount string `json:"original_amount"`
		Amount         string `json:"amount"` // balance due
		DueDate        string `json:"due_date"`
	}
	var invoices []models.Invoice
	openStatuses := []models.InvoiceStatus{
		models.InvoiceStatusIssued,
		models.InvoiceStatusSent,
		models.InvoiceStatusOverdue,
		models.InvoiceStatusPartiallyPaid,
	}
	_ = s.DB.Where("company_id = ? AND status IN ?", companyID, openStatuses).
		Order("invoice_date asc").
		Find(&invoices).Error

	items := make([]invJSON, 0, len(invoices))
	for _, inv := range invoices {
		dueDate := ""
		if inv.DueDate != nil {
			dueDate = inv.DueDate.Format("2006-01-02")
		}
		outstanding := inv.BalanceDue
		if outstanding.LessThanOrEqual(decimal.Zero) {
			outstanding = inv.Amount
		}
		items = append(items, invJSON{
			ID:             inv.ID,
			CustomerID:     inv.CustomerID,
			InvoiceNumber:  inv.InvoiceNumber,
			InvoiceDate:    inv.InvoiceDate.Format("2006-01-02"),
			OriginalAmount: inv.Amount.StringFixed(2),
			Amount:         outstanding.StringFixed(2),
			DueDate:        dueDate,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func (s *Server) handlePayBillsForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accounts, _ := s.activeAccountsForCompany(companyID)
	openBills, _ := s.openPostedBillsForCompany(companyID)

	vm := pages.PayBillsVM{
		HasCompany: true,
		Accounts:   accounts,
		OpenBills:  openBills,
		Saved:      c.Query("saved") == "1",
		EntryDate:  time.Now().Format("2006-01-02"),
	}

	return pages.PayBills(vm).Render(c.Context(), c)
}

func (s *Server) handlePayBillsSubmit(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accounts, _ := s.activeAccountsForCompany(companyID)
	openBills, _ := s.openPostedBillsForCompany(companyID)

	entryDateRaw := strings.TrimSpace(c.FormValue("entry_date"))
	bankIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	apIDRaw := strings.TrimSpace(c.FormValue("ap_account_id"))
	memo := strings.TrimSpace(c.FormValue("memo"))

	vm := pages.PayBillsVM{
		HasCompany:    true,
		Accounts:      accounts,
		OpenBills:     openBills,
		EntryDate:     entryDateRaw,
		BankAccountID: bankIDRaw,
		APAccountID:   apIDRaw,
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

	apU64, err := services.ParseUint(apIDRaw)
	if err != nil || apU64 == 0 {
		vm.APError = "A/P account is required."
	}

	if vm.DateError != "" || vm.BankError != "" || vm.APError != "" {
		return pages.PayBills(vm).Render(c.Context(), c)
	}

	// Collect selected bills and their payment amounts from the form.
	// The template posts: bill_selected=<id> (checkbox, may repeat) and
	// pay_amount_<id>=<amount> (one hidden/text input per row).
	selectedIDs := c.Request().PostArgs().PeekMultiBytes([]byte("bill_selected"))
	if len(selectedIDs) == 0 {
		vm.FormError = "Please select at least one bill to pay."
		return pages.PayBills(vm).Render(c.Context(), c)
	}

	billAmounts := make(map[string]string, len(selectedIDs))
	var billPayments []services.BillPayment
	for _, idBytes := range selectedIDs {
		idStr := string(idBytes)
		amtRaw := strings.TrimSpace(c.FormValue("pay_amount_" + idStr))
		billAmounts[idStr] = amtRaw
		amt, aErr := services.ParseDecimalMoney(amtRaw)
		if aErr != nil || amt.LessThanOrEqual(decimal.Zero) {
			vm.FormError = "Payment amount for bill " + idStr + " must be greater than 0."
			vm.BillAmounts = billAmounts
			return pages.PayBills(vm).Render(c.Context(), c)
		}
		idU64, idErr := services.ParseUint(idStr)
		if idErr != nil || idU64 == 0 {
			vm.FormError = "Invalid bill selection."
			vm.BillAmounts = billAmounts
			return pages.PayBills(vm).Render(c.Context(), c)
		}
		billPayments = append(billPayments, services.BillPayment{
			BillID: uint(idU64),
			Amount: amt,
		})
	}

	var jeID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		jeID, txErr = services.RecordPayBills(tx, services.PayBillsInput{
			CompanyID:     companyID,
			EntryDate:     entryDate,
			BankAccountID: uint(bankU64),
			APAccountID:   uint(apU64),
			Bills:         billPayments,
			Memo:          memo,
		})
		return txErr
	}); err != nil {
		vm.FormError = "Could not record payment: " + err.Error()
		return pages.PayBills(vm).Render(c.Context(), c)
	}

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "bills.paid", "journal_entry", jeID, actor, map[string]any{
		"bill_count": len(billPayments),
		"entry_date": entryDateRaw,
		"company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/banking/pay-bills?saved=1", fiber.StatusSeeOther)
}

// ── Auto-match handlers ──────────────────────────────────────────────────────

// handleAutoMatch runs the three-layer matching engine for the given account
// and redirects back to the reconcile page. It does NOT modify any journal line
// or reconciliation record — it only creates suggestion rows.
func (s *Server) handleAutoMatch(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accountIDStr := strings.TrimSpace(c.FormValue("account_id"))
	statementDateStr := strings.TrimSpace(c.FormValue("statement_date"))
	endingBalanceStr := strings.TrimSpace(c.FormValue("ending_balance"))

	redirect := func() error {
		return c.Redirect(
			"/banking/reconcile?account_id="+accountIDStr+
				"&statement_date="+statementDateStr+
				"&ending_balance="+endingBalanceStr+
				"&auto_match=1",
			fiber.StatusSeeOther,
		)
	}

	accountIDU64, err := services.ParseUint(accountIDStr)
	if err != nil || accountIDU64 == 0 {
		return redirect()
	}
	accountID := uint(accountIDU64)

	statementDate, err := time.Parse("2006-01-02", statementDateStr)
	if err != nil {
		return redirect()
	}

	endingBalance, err := services.ParseDecimalMoney(endingBalanceStr)
	if err != nil {
		return redirect()
	}

	// Load the beginning balance (previously cleared for this account + date).
	beginning, _ := services.ClearedBalance(s.DB, companyID, accountID, statementDate)

	// Load candidates.
	cands, err := services.ListReconcileCandidates(s.DB, companyID, accountID, statementDate)
	if err != nil {
		return redirect()
	}

	params := services.AutoMatchParams{
		CompanyID:        companyID,
		AccountID:        accountID,
		StatementDate:    statementDate,
		EndingBalance:    endingBalance,
		BeginningBalance: beginning,
		Candidates:       cands,
	}

	user := UserFromCtx(c)
	actor := "system"
	var uidPtr *uuid.UUID
	if user != nil {
		actor = user.Email
		uidPtr = &user.ID
	}

	suggCount, _ := services.AutoMatch(s.DB, params)

	cid := companyID
	services.TryWriteAuditLogWithContext(s.DB, "banking.reconcile.auto_match.run", "account", accountID, actor, map[string]any{
		"account_id":       accountID,
		"statement_date":   statementDateStr,
		"candidate_count":  len(cands),
		"suggestion_count": suggCount,
		"company_id":       companyID,
	}, &cid, uidPtr)

	return redirect()
}

// handleAcceptSuggestion marks a suggestion as accepted, pre-selects its lines
// via the session layer, and updates the reconciliation memory.
func (s *Server) handleAcceptSuggestion(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	suggIDStr := strings.TrimSpace(c.FormValue("suggestion_id"))
	accountIDStr := strings.TrimSpace(c.FormValue("account_id"))
	statementDateStr := strings.TrimSpace(c.FormValue("statement_date"))
	endingBalanceStr := strings.TrimSpace(c.FormValue("ending_balance"))

	redirect := func() error {
		return c.Redirect(
			"/banking/reconcile?account_id="+accountIDStr+
				"&statement_date="+statementDateStr+
				"&ending_balance="+endingBalanceStr,
			fiber.StatusSeeOther,
		)
	}

	suggIDU64, err := services.ParseUint(suggIDStr)
	if err != nil || suggIDU64 == 0 {
		return redirect()
	}
	suggID := uint(suggIDU64)

	// Atomic CAS: update only if still pending. RowsAffected == 0 means
	// another request already accepted or rejected this suggestion — silently redirect.
	now := time.Now()
	userID := user.ID
	result := s.DB.Model(&models.ReconciliationMatchSuggestion{}).
		Where("id = ? AND company_id = ? AND status = ?", suggID, companyID, models.SuggStatusPending).
		Updates(map[string]any{
			"status":              models.SuggStatusAccepted,
			"accepted_by_user_id": userID,
			"accepted_at":         &now,
			"reviewed_at":         &now,
			"reviewed_by_user_id": userID,
		})
	if result.Error != nil || result.RowsAffected == 0 {
		return redirect()
	}

	// Load the now-accepted suggestion (with lines) for memory update + audit.
	var sugg models.ReconciliationMatchSuggestion
	if err := s.DB.Preload("Lines").
		Where("id = ? AND company_id = ?", suggID, companyID).
		First(&sugg).Error; err != nil {
		return redirect()
	}

	// Update memory for each accepted line.
	lineIDs := make([]uint, 0, len(sugg.Lines))
	for _, l := range sugg.Lines {
		lineIDs = append(lineIDs, l.JournalLineID)
	}
	_ = services.UpdateMemoryFromAcceptedLines(s.DB, companyID, sugg.AccountID, lineIDs)

	// Audit log.
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "reconcile.suggestion.accepted", "reconciliation_match_suggestion", suggID, actor, map[string]any{
		"account_id": sugg.AccountID,
		"line_count": len(lineIDs),
		"confidence": sugg.ConfidenceScore.StringFixed(4),
		"company_id": companyID,
	}, &cid, &uid)

	return redirect()
}

// handleRejectSuggestion marks a suggestion as rejected. No accounting records
// are modified; this is purely a status update on the suggestion row.
func (s *Server) handleRejectSuggestion(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	suggIDStr := strings.TrimSpace(c.FormValue("suggestion_id"))
	accountIDStr := strings.TrimSpace(c.FormValue("account_id"))
	statementDateStr := strings.TrimSpace(c.FormValue("statement_date"))
	endingBalanceStr := strings.TrimSpace(c.FormValue("ending_balance"))

	redirect := func() error {
		return c.Redirect(
			"/banking/reconcile?account_id="+accountIDStr+
				"&statement_date="+statementDateStr+
				"&ending_balance="+endingBalanceStr,
			fiber.StatusSeeOther,
		)
	}

	suggIDU64, err := services.ParseUint(suggIDStr)
	if err != nil || suggIDU64 == 0 {
		return redirect()
	}
	suggID := uint(suggIDU64)

	// Atomic CAS: update only if still pending. RowsAffected == 0 means
	// another request already accepted or rejected this suggestion — silently redirect.
	now := time.Now()
	userID := user.ID
	result := s.DB.Model(&models.ReconciliationMatchSuggestion{}).
		Where("id = ? AND company_id = ? AND status = ?", suggID, companyID, models.SuggStatusPending).
		Updates(map[string]any{
			"status":              models.SuggStatusRejected,
			"rejected_by_user_id": userID,
			"rejected_at":         &now,
			"reviewed_at":         &now,
			"reviewed_by_user_id": userID,
		})
	if result.Error != nil || result.RowsAffected == 0 {
		return redirect()
	}

	// Load account_id for audit log (best-effort; skip if row missing).
	var sugg models.ReconciliationMatchSuggestion
	_ = s.DB.Select("account_id").Where("id = ? AND company_id = ?", suggID, companyID).First(&sugg).Error

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "reconcile.suggestion.rejected", "reconciliation_match_suggestion", suggID, actor, map[string]any{
		"account_id": sugg.AccountID,
		"company_id": companyID,
	}, &cid, &uid)

	return redirect()
}

func (s *Server) handleVoidReconciliation(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	recIDStr := strings.TrimSpace(c.FormValue("rec_id"))
	accountIDStr := strings.TrimSpace(c.FormValue("account_id"))
	reason := strings.TrimSpace(c.FormValue("void_reason"))

	recIDU64, err := services.ParseUint(recIDStr)
	if err != nil || recIDU64 == 0 {
		return c.Redirect("/banking/reconcile?account_id="+accountIDStr, fiber.StatusSeeOther)
	}

	if reason == "" {
		return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&void_error=1", fiber.StatusSeeOther)
	}

	if err := services.VoidReconciliation(s.DB, companyID, uint(recIDU64), user.ID, reason); err != nil {
		return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&void_error=1", fiber.StatusSeeOther)
	}

	// Archive accepted suggestions linked to this reconciliation — preserves audit history.
	_ = services.ArchiveSuggestionsForReconciliation(s.DB, companyID, uint(recIDU64))

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "banking.reconciliation.voided", "reconciliation", uint(recIDU64), actor, map[string]any{
		"account_id": accountIDStr,
		"reason":     reason,
		"company_id": companyID,
	}, &cid, &uid)

	return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&voided=1", fiber.StatusSeeOther)
}

func (s *Server) openPostedBillsForCompany(companyID uint) ([]models.Bill, error) {
	var bills []models.Bill
	err := s.DB.Preload("Vendor").
		Where("company_id = ? AND status IN ?", companyID, []models.BillStatus{models.BillStatusPosted, models.BillStatusPartiallyPaid}).
		Order("bill_date asc, id asc").
		Find(&bills).Error
	return bills, err
}

// bankAccountsForCompany returns all active Asset · Bank accounts for a company,
// used to populate the "Deposit to (Bank)" dropdown on payment forms.
func (s *Server) bankAccountsForCompany(companyID uint) ([]models.Account, error) {
	var accounts []models.Account
	err := s.DB.
		Where("company_id = ? AND detail_account_type = ? AND is_active = true", companyID, models.DetailBank).
		Order("code asc").
		Find(&accounts).Error
	return accounts, err
}

// defaultARAccountID returns the ID of the first active Accounts Receivable account
// for the given company. Returns an error if none is found.
func (s *Server) defaultARAccountID(companyID uint) (uint, error) {
	var acc models.Account
	err := s.DB.
		Where("company_id = ? AND detail_account_type = ? AND is_active = true", companyID, models.DetailAccountsReceivable).
		Order("code asc").
		First(&acc).Error
	if err != nil {
		return 0, errors.New("no Accounts Receivable account found")
	}
	return acc.ID, nil
}
