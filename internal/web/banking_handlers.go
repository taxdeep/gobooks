// 遵循project_guide.md
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
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

type reconcileWorkspacePayload struct {
	AccountID           string                       `json:"account_id"`
	AccountName         string                       `json:"account_name"`
	StatementDate       string                       `json:"statement_date"`
	EndingBalance       string                       `json:"ending_balance"`
	BeginningBalance    string                       `json:"beginning_balance"`
	Candidates          []reconcileCandidatePayload  `json:"candidates"`
	SelectedLineIDs     []string                     `json:"selected_line_ids"`
	Suggestions         []reconcileSuggestionPayload `json:"suggestions"`
	SaveDraftURL        string                       `json:"save_draft_url"`
	SaveProgressURL     string                       `json:"save_progress_url"`
	FinishURL           string                       `json:"finish_url"`
	AutoMatchURL        string                       `json:"auto_match_url"`
	AcceptSuggestionURL string                       `json:"accept_suggestion_url"`
	RejectSuggestionURL string                       `json:"reject_suggestion_url"`
}

type reconcileCandidatePayload struct {
	ID              string `json:"id"`
	LineID          uint   `json:"line_id"`
	JournalEntryID  uint   `json:"journal_entry_id"`
	Date            string `json:"date"`
	Type            string `json:"type"`
	SourceType      string `json:"source_type"`
	SourceID        uint   `json:"source_id"`
	Reference       string `json:"reference"`
	Payee           string `json:"payee"`
	Memo            string `json:"memo"`
	Amount          string `json:"amount"`
	Payment         string `json:"payment"`
	Deposit         string `json:"deposit"`
	IsPayment       bool   `json:"is_payment"`
	IsDeposit       bool   `json:"is_deposit"`
	DetailURL       string `json:"detail_url"`
	IsReversal      bool   `json:"is_reversal"`
	IsReversalPair  bool   `json:"is_reversal_pair"`
	ReversalPairKey string `json:"reversal_pair_key,omitempty"`
}

type reconcileSuggestionPayload struct {
	ID            uint                        `json:"id"`
	Status        string                      `json:"status"`
	TypeLabel     string                      `json:"type_label"`
	ConfidencePct string                      `json:"confidence_pct"`
	Summary       string                      `json:"summary"`
	NetAmount     string                      `json:"net_amount"`
	LineIDs       []uint                      `json:"line_ids"`
	JournalNos    []string                    `json:"journal_nos"`
	Signals       []reconcileSuggestionSignal `json:"signals"`
}

type reconcileSuggestionSignal struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Stars  string `json:"stars"`
}

func buildReconcileWorkspaceJSON(vm pages.BankReconcileVM) string {
	payload := reconcileWorkspacePayload{
		AccountID:           vm.AccountID,
		AccountName:         vm.AccountName,
		StatementDate:       vm.StatementDate,
		EndingBalance:       vm.EndingBalance,
		BeginningBalance:    vm.BeginningBalance,
		Candidates:          buildReconcileCandidatePayloads(vm.Candidates),
		SelectedLineIDs:     parseReconcileSelectedLineIDs(vm.AcceptedLineIDsJSON),
		Suggestions:         buildReconcileSuggestionPayloads(vm.Suggestions),
		SaveDraftURL:        "/api/banking/reconcile/draft",
		SaveProgressURL:     "/banking/reconcile/save-progress",
		FinishURL:           "/banking/reconcile",
		AutoMatchURL:        "/banking/reconcile/auto-match",
		AcceptSuggestionURL: "/banking/reconcile/suggest/accept",
		RejectSuggestionURL: "/banking/reconcile/suggest/reject",
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func buildReconcileCandidatePayloads(cands []services.ReconcileCandidate) []reconcileCandidatePayload {
	reversalPairs := identifyReversalPairs(cands)
	out := make([]reconcileCandidatePayload, 0, len(cands))
	for _, cand := range cands {
		pairKey := ""
		if cand.ReversedFromID != nil {
			pairKey = fmt.Sprintf("je:%d:%d", *cand.ReversedFromID, cand.JournalEntryID)
		} else if reversalPairs[cand.JournalEntryID] {
			pairKey = fmt.Sprintf("je:%d", cand.JournalEntryID)
		}
		out = append(out, reconcileCandidatePayload{
			ID:              fmt.Sprintf("%d", cand.LineID),
			LineID:          cand.LineID,
			JournalEntryID:  cand.JournalEntryID,
			Date:            cand.EntryDate.Format("2006-01-02"),
			Type:            pages.SourceTypeLabel(cand.SourceType),
			SourceType:      cand.SourceType,
			SourceID:        cand.SourceID,
			Reference:       reconcileReference(cand),
			Payee:           cand.PayeeName,
			Memo:            cand.Memo,
			Amount:          cand.Amount.StringFixed(2),
			Payment:         cand.Payment.StringFixed(2),
			Deposit:         cand.Deposit.StringFixed(2),
			IsPayment:       cand.Payment.IsPositive(),
			IsDeposit:       cand.Deposit.IsPositive(),
			DetailURL:       reconcileDocumentURL(cand.SourceType, cand.SourceID, cand.JournalEntryID),
			IsReversal:      models.LedgerSourceType(cand.SourceType) == models.LedgerSourceReversal,
			IsReversalPair:  reversalPairs[cand.JournalEntryID],
			ReversalPairKey: pairKey,
		})
	}
	return out
}

func identifyReversalPairs(cands []services.ReconcileCandidate) map[uint]bool {
	byJE := make(map[uint]services.ReconcileCandidate, len(cands))
	for _, cand := range cands {
		byJE[cand.JournalEntryID] = cand
	}
	out := make(map[uint]bool)
	tolerance := decimal.RequireFromString("0.005")
	for _, cand := range cands {
		if cand.ReversedFromID == nil {
			continue
		}
		orig, ok := byJE[*cand.ReversedFromID]
		if !ok {
			continue
		}
		if cand.Amount.Add(orig.Amount).Abs().LessThan(tolerance) {
			out[cand.JournalEntryID] = true
			out[orig.JournalEntryID] = true
		}
	}
	return out
}

func buildReconcileSuggestionPayloads(suggestions []pages.MatchSuggestionVM) []reconcileSuggestionPayload {
	out := make([]reconcileSuggestionPayload, 0, len(suggestions))
	for _, sugg := range suggestions {
		signals := make([]reconcileSuggestionSignal, 0, len(sugg.Signals))
		for _, sig := range sugg.Signals {
			stars := strings.Repeat("*", sig.StarsFull) + strings.Repeat(".", sig.StarsEmpty)
			signals = append(signals, reconcileSuggestionSignal{Name: sig.Name, Detail: sig.Detail, Stars: stars})
		}
		out = append(out, reconcileSuggestionPayload{
			ID:            sugg.ID,
			Status:        sugg.Status,
			TypeLabel:     sugg.TypeLabel,
			ConfidencePct: sugg.ConfidencePct,
			Summary:       sugg.Summary,
			NetAmount:     sugg.NetAmount,
			LineIDs:       sugg.LineIDs,
			JournalNos:    sugg.JournalNos,
			Signals:       signals,
		})
	}
	return out
}

func parseReconcileSelectedLineIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return []string{}
	}
	var items []any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				out = append(out, strings.TrimSpace(v))
			}
		case float64:
			if v > 0 {
				out = append(out, fmt.Sprintf("%.0f", v))
			}
		}
	}
	return out
}

func reconcileReference(cand services.ReconcileCandidate) string {
	if strings.TrimSpace(cand.JournalNo) != "" {
		return cand.JournalNo
	}
	if cand.SourceID > 0 {
		return fmt.Sprintf("%s-%d", pages.SourceTypeLabel(cand.SourceType), cand.SourceID)
	}
	return fmt.Sprintf("JE-%d", cand.JournalEntryID)
}

func reconcileDocumentURL(sourceType string, sourceID, journalEntryID uint) string {
	if sourceID == 0 {
		return reconcileJEURL(journalEntryID)
	}
	switch models.LedgerSourceType(sourceType) {
	case models.LedgerSourceInvoice:
		return fmt.Sprintf("/invoices/%d", sourceID)
	case models.LedgerSourceBill:
		return fmt.Sprintf("/bills/%d", sourceID)
	case models.LedgerSourceExpense:
		return fmt.Sprintf("/expenses/%d/edit", sourceID)
	case models.LedgerSourceReceipt, models.LedgerSourceCustomerReceipt:
		return fmt.Sprintf("/receipts/%d", sourceID)
	case models.LedgerSourceCreditNote:
		return fmt.Sprintf("/credit-notes/%d", sourceID)
	case models.LedgerSourceVendorCreditNote:
		return fmt.Sprintf("/vendor-credit-notes/%d", sourceID)
	case models.LedgerSourceARRefund:
		return fmt.Sprintf("/refunds/%d", sourceID)
	case models.LedgerSourceVendorRefund:
		return fmt.Sprintf("/vendor-refunds/%d", sourceID)
	case models.LedgerSourceCustomerDeposit:
		return fmt.Sprintf("/deposits/%d", sourceID)
	case models.LedgerSourceVendorPrepayment:
		return fmt.Sprintf("/vendor-prepayments/%d", sourceID)
	default:
		return reconcileJEURL(journalEntryID)
	}
}

func reconcileJEURL(journalEntryID uint) string {
	if journalEntryID == 0 {
		return ""
	}
	return fmt.Sprintf("/journal-entry/%d", journalEntryID)
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
		ProgressSaved:       c.Query("progress_saved") == "1",
		FormError:           formError,
		BeginningBalance:    "0.00",
		PreviouslyCleared:   "0.00",
		CandidatesJSON:      "[]",
		AcceptedLineIDsJSON: "[]",
		Candidates:          []services.ReconcileCandidate{},
	}

	if accountIDStr == "" {
		// No account selected: show account selector only.
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
	vm.AccountName = accRow.Code + " - " + accRow.Name

	// ── Entry Gate ────────────────────────────────────────────────────────────
	// Work mode requires BOTH statement_date AND ending_balance to be present in
	// the URL — they must be explicitly confirmed by the user (via "Start" or
	// "Resume" buttons in the entry panels). A URL with just account_id lands
	// in entry/setup mode and never shows the working page directly.
	isWorkMode := statementDateStr != "" && endingBalanceStr != ""

	if !isWorkMode {
		// ── Entry / Setup / Resume mode ───────────────────────────────────────
		defaults, defErr := services.ComputeReconcileDefaults(s.DB, companyID, accountID)
		if defErr != nil {
			vm.FormError = "Could not load reconciliation state."
			return pages.BankReconcile(vm).Render(c.Context(), c)
		}

		vm.StatementDate = defaults.StatementDate
		vm.EndingBalance = defaults.EndingBalance
		vm.LastStatementDateDisplay = defaults.LastStatementDate

		switch defaults.Source {
		case services.ReconcileDefaultsDraft:
			vm.EntryMode = "resume"
		case services.ReconcileDefaultsInferred:
			vm.EntryMode = "setup"
		default: // ReconcileDefaultsBlank
			vm.EntryMode = "setup"
		}

		// Load expense / income account lists for the setup form dropdowns.
		// Only needed in setup mode; skipped in resume mode to avoid unnecessary queries.
		if vm.EntryMode == "setup" {
			vm.ExpenseAccounts, _ = s.expenseAccountsForCompany(companyID)
			vm.IncomeAccounts, _ = s.incomeAccountsForCompany(companyID)
		}

		return pages.BankReconcile(vm).Render(c.Context(), c)
	}

	// ── Work mode ─────────────────────────────────────────────────────────────
	vm.EntryMode = "work"

	statementDate, err := time.Parse("2006-01-02", statementDateStr)
	if err != nil {
		vm.FormError = "Statement Date must be a valid date."
		vm.EntryMode = "setup"
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}
	vm.StatementDate = statementDateStr
	vm.StatementDateTime = statementDate

	if _, err := services.ParseDecimalMoney(endingBalanceStr); err != nil {
		vm.FormError = "Ending Balance must be a number."
		vm.EntryMode = "setup"
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}
	vm.EndingBalance = endingBalanceStr

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

	// Load match-engine suggestions.
	pendingSuggs, _ := services.LoadActiveSuggestions(s.DB, companyID, accountID)
	candidatesByLineID := make(map[uint]services.ReconcileCandidate, len(cands))
	for _, cd := range cands {
		candidatesByLineID[cd.LineID] = cd
	}
	vm.Suggestions = pages.BuildMatchSuggestionVMs(pendingSuggs, candidatesByLineID)
	vm.SuggestionCount = len(vm.Suggestions)

	// Pre-select lines from accepted suggestions, then override with draft if present.
	acceptedIDs, _ := services.LoadAcceptedLineIDs(s.DB, companyID, accountID)
	vm.AcceptedLineIDs = acceptedIDs
	if len(acceptedIDs) > 0 {
		b, _ := json.Marshal(acceptedIDs)
		vm.AcceptedLineIDsJSON = string(b)
	}

	// Draft selected IDs take priority — they capture the user's most recent check state.
	if draft, _ := services.GetReconcileDraft(s.DB, companyID, accountID); draft != nil {
		if draft.SelectedLineIDs != "" && draft.SelectedLineIDs != "[]" {
			vm.AcceptedLineIDsJSON = draft.SelectedLineIDs
		}
		vm.ResumingDraft = true
	}
	vm.WorkspaceJSON = buildReconcileWorkspaceJSON(vm)

	return pages.BankReconcile(vm).Render(c.Context(), c)
}

// handleBankReconcileDiscardDraft deletes the in-progress draft for an account
// and redirects back to entry/setup mode so the user can start a new reconciliation.
func (s *Server) handleBankReconcileDiscardDraft(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	accountIDStr := strings.TrimSpace(c.FormValue("account_id"))
	accountIDU64, err := services.ParseUint(accountIDStr)
	if err != nil || accountIDU64 == 0 {
		return c.Redirect("/banking/reconcile", fiber.StatusSeeOther)
	}
	_ = services.DeleteReconcileDraft(s.DB, companyID, uint(accountIDU64))
	return c.Redirect("/banking/reconcile?account_id="+accountIDStr, fiber.StatusSeeOther)
}

// handleBankReconcileSetup processes the Setup form (new-period reconciliation).
// It creates bank service charge and/or interest earned journal entries if the
// user supplied non-zero amounts, then redirects to work mode.
func (s *Server) handleBankReconcileSetup(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accountIDStr := strings.TrimSpace(c.FormValue("account_id"))
	statementDateStr := strings.TrimSpace(c.FormValue("statement_date"))
	endingBalanceStr := strings.TrimSpace(c.FormValue("ending_balance"))

	// Helper: re-render setup with an error.
	renderSetupError := func(msg string) error {
		accounts, _ := s.bankAccountsForCompany(companyID)
		var accRow models.Account
		_ = s.DB.Where("id = ? AND company_id = ?", accountIDStr, companyID).First(&accRow).Error
		expAccounts, _ := s.expenseAccountsForCompany(companyID)
		incAccounts, _ := s.incomeAccountsForCompany(companyID)
		vm := pages.BankReconcileVM{
			HasCompany:          true,
			Accounts:            accounts,
			AccountID:           accountIDStr,
			AccountName:         accRow.Code + " - " + accRow.Name,
			StatementDate:       statementDateStr,
			EndingBalance:       endingBalanceStr,
			Active:              "Bank Reconcile",
			EntryMode:           "setup",
			FormError:           msg,
			ExpenseAccounts:     expAccounts,
			IncomeAccounts:      incAccounts,
			CandidatesJSON:      "[]",
			AcceptedLineIDsJSON: "[]",
			BeginningBalance:    "0.00",
			PreviouslyCleared:   "0.00",
			Candidates:          nil,
		}
		return pages.BankReconcile(vm).Render(c.Context(), c)
	}

	// Validate required fields.
	if accountIDStr == "" || statementDateStr == "" || endingBalanceStr == "" {
		return renderSetupError("Account, statement date, and ending balance are required.")
	}
	accountIDU64, err := services.ParseUint(accountIDStr)
	if err != nil || accountIDU64 == 0 {
		return renderSetupError("Invalid account selected.")
	}
	accountID := uint(accountIDU64)

	statementDate, err := time.Parse("2006-01-02", statementDateStr)
	if err != nil {
		return renderSetupError("Statement Date must be a valid date.")
	}
	if _, err := services.ParseDecimalMoney(endingBalanceStr); err != nil {
		return renderSetupError("Ending Balance must be a valid number.")
	}

	// Parse optional service charge.
	scAmtStr := strings.TrimSpace(c.FormValue("service_charge"))
	scDateStr := strings.TrimSpace(c.FormValue("service_charge_date"))
	scAcctIDStr := strings.TrimSpace(c.FormValue("service_charge_account_id"))

	// Parse optional interest earned.
	intAmtStr := strings.TrimSpace(c.FormValue("interest_earned"))
	intDateStr := strings.TrimSpace(c.FormValue("interest_earned_date"))
	intAcctIDStr := strings.TrimSpace(c.FormValue("interest_earned_account_id"))

	in := services.ReconcileSetupEntriesInput{
		CompanyID:     companyID,
		BankAccountID: accountID,
	}

	if scAmtStr != "" {
		if scAmt, err := decimal.NewFromString(scAmtStr); err == nil && scAmt.IsPositive() {
			in.ServiceCharge = scAmt
			in.ServiceChargeDate = statementDate
			if scDateStr != "" {
				if d, err := time.Parse("2006-01-02", scDateStr); err == nil {
					in.ServiceChargeDate = d
				}
			}
			scAcctID, _ := services.ParseUint(scAcctIDStr)
			in.ServiceChargeAccountID = uint(scAcctID)
		}
	}

	if intAmtStr != "" {
		if intAmt, err := decimal.NewFromString(intAmtStr); err == nil && intAmt.IsPositive() {
			in.InterestEarned = intAmt
			in.InterestEarnedDate = statementDate
			if intDateStr != "" {
				if d, err := time.Parse("2006-01-02", intDateStr); err == nil {
					in.InterestEarnedDate = d
				}
			}
			intAcctID, _ := services.ParseUint(intAcctIDStr)
			in.InterestEarnedAccountID = uint(intAcctID)
		}
	}

	if err := services.CreateReconcileSetupEntries(s.DB, in); err != nil {
		return renderSetupError("Could not create adjustment entries: " + err.Error())
	}

	return c.Redirect(
		"/banking/reconcile?account_id="+accountIDStr+
			"&statement_date="+statementDateStr+
			"&ending_balance="+endingBalanceStr,
		fiber.StatusSeeOther,
	)
}

func (s *Server) handleBankReconcileSaveProgress(c *fiber.Ctx) error {
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

	lineIDBytes := c.Context().PostArgs().PeekMulti("line_ids")
	selectedIDs := make([]string, 0, len(lineIDBytes))
	for _, b := range lineIDBytes {
		selectedIDs = append(selectedIDs, string(b))
	}
	lineIDsJSON := "[]"
	if len(selectedIDs) > 0 {
		b, _ := json.Marshal(selectedIDs)
		lineIDsJSON = string(b)
	}

	_ = services.UpsertReconcileDraft(s.DB, companyID, accountID, statementDateStr, endingBalanceStr, lineIDsJSON)

	return c.Redirect("/banking/reconcile?account_id="+accountIDStr+"&statement_date="+statementDateStr+"&ending_balance="+endingBalanceStr+"&progress_saved=1", fiber.StatusSeeOther)
}

type bankReconcileDraftRequest struct {
	AccountID       string   `json:"account_id"`
	StatementDate   string   `json:"statement_date"`
	EndingBalance   string   `json:"ending_balance"`
	SelectedLineIDs []string `json:"selected_line_ids"`
}

func (s *Server) handleBankReconcileDraftAPI(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "company context required")
	}

	var req bankReconcileDraftRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request")
	}
	accountIDStr := strings.TrimSpace(req.AccountID)
	accountIDU64, err := services.ParseUint(accountIDStr)
	if err != nil || accountIDU64 == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "invalid account")
	}
	accountID := uint(accountIDU64)
	if err := s.DB.Where("id = ? AND company_id = ?", accountID, companyID).First(new(models.Account)).Error; err != nil {
		return fiber.NewError(fiber.StatusForbidden, "account not available")
	}
	statementDate, err := time.Parse("2006-01-02", strings.TrimSpace(req.StatementDate))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid statement date")
	}
	if _, err := services.ParseDecimalMoney(strings.TrimSpace(req.EndingBalance)); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid ending balance")
	}

	candidates, err := services.ListReconcileCandidates(s.DB, companyID, accountID, statementDate)
	if err != nil {
		slog.Warn("bank reconcile draft candidate validation failed", "company_id", companyID, "account_id", accountID, "error", err)
		return fiber.NewError(fiber.StatusInternalServerError, "could not validate draft")
	}
	validIDs := make(map[string]struct{}, len(candidates))
	for _, cand := range candidates {
		validIDs[fmt.Sprintf("%d", cand.LineID)] = struct{}{}
	}

	selectedIDs := make([]string, 0, len(req.SelectedLineIDs))
	for _, raw := range req.SelectedLineIDs {
		id := strings.TrimSpace(raw)
		u, err := services.ParseUint(id)
		if err != nil || u == 0 {
			continue
		}
		if _, ok := validIDs[id]; !ok {
			continue
		}
		selectedIDs = append(selectedIDs, id)
	}
	lineIDsJSON := "[]"
	if len(selectedIDs) > 0 {
		b, _ := json.Marshal(selectedIDs)
		lineIDsJSON = string(b)
	}
	if err := services.UpsertReconcileDraft(s.DB, companyID, accountID, strings.TrimSpace(req.StatementDate), strings.TrimSpace(req.EndingBalance), lineIDsJSON); err != nil {
		slog.Warn("bank reconcile draft save failed", "company_id", companyID, "account_id", accountID, "error", err)
		return fiber.NewError(fiber.StatusInternalServerError, "could not save draft")
	}
	return c.JSON(fiber.Map{"ok": true})
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

	// Clear any in-progress draft — reconciliation is now complete.
	_ = services.DeleteReconcileDraft(s.DB, companyID, accountID)

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
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").Find(&customers).Error

	bankAccounts, _ := s.bankAccountsForCompany(companyID)

	vm := pages.ReceivePaymentVM{
		HasCompany:          true,
		Customers:           customers,
		BankAccounts:        bankAccounts,
		Saved:               c.Query("saved") == "1",
		EntryDate:           time.Now().Format("2006-01-02"),
		OpenInvoicesJSON:    buildOpenInvoicesJSON(s, companyID),
		OpenDepositsJSON:    buildOpenDepositsJSON(s, companyID),
		OpenCreditNotesJSON: buildOpenCreditNotesJSON(s, companyID),
	}

	if customerIDRaw := strings.TrimSpace(c.Query("customer_id")); customerIDRaw != "" {
		if customerID64, err := services.ParseUint(customerIDRaw); err == nil && customerID64 > 0 {
			var customer models.Customer
			if err := s.DB.Select("id", "company_id").
				Where("id = ? AND company_id = ? AND is_active = true", uint(customerID64), companyID).
				First(&customer).Error; err == nil {
				vm.CustomerID = customerIDRaw
			}
		}
	}

	// Deep-link from invoice detail "Apply Credits / Deposits" button:
	// `/banking/receive-payment?invoice_id=X` pre-selects the invoice +
	// its customer so the Apply table renders the customer's open CNs +
	// Deposits ready to tick. The Alpine init reads `data-initial-invoice`
	// to also pre-tick the invoice row.
	if invIDRaw := strings.TrimSpace(c.Query("invoice_id")); invIDRaw != "" {
		if invID64, err := services.ParseUint(invIDRaw); err == nil && invID64 > 0 {
			var inv models.Invoice
			if err := s.DB.Select("id", "customer_id", "company_id").
				Where("id = ? AND company_id = ?", uint(invID64), companyID).
				First(&inv).Error; err == nil {
				vm.InvoiceID = invIDRaw
				vm.CustomerID = fmt.Sprintf("%d", inv.CustomerID)
			}
		}
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
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).Order("name asc").Find(&customers).Error
	bankAccounts, _ := s.bankAccountsForCompany(companyID)

	customerIDRaw := strings.TrimSpace(c.FormValue("customer_id"))
	paymentMethodRaw := strings.TrimSpace(c.FormValue("payment_method"))
	entryDateRaw := strings.TrimSpace(c.FormValue("entry_date"))
	bankIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	invoiceIDRaw := strings.TrimSpace(c.FormValue("invoice_id"))
	amountRaw := strings.TrimSpace(c.FormValue("amount"))
	memo := strings.TrimSpace(c.FormValue("memo"))
	newDepositAmountRaw := strings.TrimSpace(c.FormValue("new_deposit_amount"))

	// Multi-invoice allocation arrays. When the operator ticks N
	// checkboxes in the Apply-to-Invoice table, the form posts N
	// entries in allocation_invoice_id[] + allocation_amount[]. The
	// legacy single-invoice_id / amount path still works — used when
	// the operator records an unlinked payment on account.
	allocInvoiceIDs := c.Context().PostArgs().PeekMulti("allocation_invoice_id")
	allocAmounts := c.Context().PostArgs().PeekMulti("allocation_amount")
	// Deposit consumption arrays — same parallel-array convention as
	// invoices. Each ticked CustomerDeposit row posts its ID + the
	// amount the operator wants to consume from its BalanceRemaining.
	depositIDs := c.Context().PostArgs().PeekMulti("deposit_id")
	depositAmounts := c.Context().PostArgs().PeekMulti("deposit_amount")
	// Credit note consumption arrays — parallel to deposits. Each ticked
	// CN row posts its ID + the amount the operator consumes from
	// BalanceRemaining.
	cnIDs := c.Context().PostArgs().PeekMulti("credit_note_id")
	cnAmounts := c.Context().PostArgs().PeekMulti("credit_note_amount")

	vm := pages.ReceivePaymentVM{
		HasCompany:          true,
		Customers:           customers,
		BankAccounts:        bankAccounts,
		OpenInvoicesJSON:    buildOpenInvoicesJSON(s, companyID),
		OpenDepositsJSON:    buildOpenDepositsJSON(s, companyID),
		OpenCreditNotesJSON: buildOpenCreditNotesJSON(s, companyID),
		PaymentMethod:       paymentMethodRaw,
		CustomerID:          customerIDRaw,
		EntryDate:           entryDateRaw,
		BankAccountID:       bankIDRaw,
		InvoiceID:           invoiceIDRaw,
		Amount:              amountRaw,
		Memo:                memo,
		NewDepositAmount:    newDepositAmountRaw,
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

	// Build the allocation slice from the parsed arrays. Skip rows
	// with zero/invalid IDs or non-positive amounts — they represent
	// unticked rows whose inputs still POST as empty strings.
	var allocations []services.InvoiceAllocation
	var allocTotal decimal.Decimal
	if len(allocInvoiceIDs) > 0 {
		if len(allocInvoiceIDs) != len(allocAmounts) {
			vm.FormError = "Allocation arrays out of sync. Please retry."
		} else {
			for i := range allocInvoiceIDs {
				invID, invErr := services.ParseUint(string(allocInvoiceIDs[i]))
				if invErr != nil || invID == 0 {
					continue
				}
				amt, amtErr := services.ParseDecimalMoney(string(allocAmounts[i]))
				if amtErr != nil || amt.LessThanOrEqual(decimal.Zero) {
					continue
				}
				allocations = append(allocations, services.InvoiceAllocation{
					InvoiceID: uint(invID),
					Amount:    amt,
				})
				allocTotal = allocTotal.Add(amt)
			}
		}
	}

	// Build the deposit-application slice. Same skip-invalid-row rules
	// as invoices — unchecked rows POST empty strings that we drop.
	var depositApps []services.DepositApplication
	var depositTotal decimal.Decimal
	if len(depositIDs) > 0 {
		if len(depositIDs) != len(depositAmounts) {
			vm.FormError = "Deposit arrays out of sync. Please retry."
		} else {
			for i := range depositIDs {
				depID, dErr := services.ParseUint(string(depositIDs[i]))
				if dErr != nil || depID == 0 {
					continue
				}
				amt, aErr := services.ParseDecimalMoney(string(depositAmounts[i]))
				if aErr != nil || amt.LessThanOrEqual(decimal.Zero) {
					continue
				}
				depositApps = append(depositApps, services.DepositApplication{
					DepositID: uint(depID),
					Amount:    amt,
				})
				depositTotal = depositTotal.Add(amt)
			}
		}
	}

	// Build the credit-note-consumption slice. Same skip-invalid-row rules.
	var cnApps []services.CreditNoteConsumption
	var cnTotal decimal.Decimal
	if len(cnIDs) > 0 {
		if len(cnIDs) != len(cnAmounts) {
			vm.FormError = "Credit note arrays out of sync. Please retry."
		} else {
			for i := range cnIDs {
				cnID, cErr := services.ParseUint(string(cnIDs[i]))
				if cErr != nil || cnID == 0 {
					continue
				}
				amt, aErr := services.ParseDecimalMoney(string(cnAmounts[i]))
				if aErr != nil || amt.LessThanOrEqual(decimal.Zero) {
					continue
				}
				cnApps = append(cnApps, services.CreditNoteConsumption{
					CreditNoteID: uint(cnID),
					Amount:       amt,
				})
				cnTotal = cnTotal.Add(amt)
			}
		}
	}

	// Parse optional new-deposit amount (overpayment → new Customer Deposit).
	newDepositAmount := decimal.Zero
	if newDepositAmountRaw != "" {
		n, err := services.ParseDecimalMoney(newDepositAmountRaw)
		if err != nil || n.LessThan(decimal.Zero) {
			vm.NewDepositAmountError = "Extra deposit amount must be a non-negative number."
		} else {
			newDepositAmount = n
		}
	}

	// Resolve the "amount" figure. In the unified flow this is an
	// informational display value equal to bank = Σ invoice − Σ CN −
	// Σ deposit + new deposit. When there are no invoice allocations and
	// no new deposit, the form falls back to the legacy manual-amount
	// path (unlinked payment on account).
	var amount decimal.Decimal
	if len(allocations) > 0 || len(depositApps) > 0 || len(cnApps) > 0 || newDepositAmount.IsPositive() {
		amount = allocTotal.Sub(cnTotal).Sub(depositTotal).Add(newDepositAmount)
	} else {
		a, aErr := services.ParseDecimalMoney(amountRaw)
		if aErr != nil || a.LessThanOrEqual(decimal.Zero) {
			vm.AmountError = "Amount must be greater than 0 (or select invoices above)."
		} else {
			amount = a
			if invoiceIDRaw == "" || invoiceIDRaw == "0" {
				newDepositAmount = a
			}
		}
	}

	// Auto-resolve the Accounts Receivable account for this company.
	arU64, arErr := s.defaultARAccountID(companyID)
	if arErr != nil {
		vm.ARError = "No Accounts Receivable account found. Please add one to your Chart of Accounts."
	}

	if vm.CustomerError != "" || vm.PaymentMethodError != "" || vm.DateError != "" || vm.BankError != "" || vm.ARError != "" || vm.AmountError != "" || vm.FormError != "" || vm.NewDepositAmountError != "" {
		return pages.ReceivePayment(vm).Render(c.Context(), c)
	}

	// Legacy single-invoice fallback — operator didn't tick any rows
	// but did pick one via the legacy hidden input (e.g. invoice
	// detail page's "Receive Payment" button that pre-fills one row).
	if len(allocations) == 0 && invoiceIDRaw != "" && invoiceIDRaw != "0" {
		if invU64, err := services.ParseUint(invoiceIDRaw); err == nil && invU64 > 0 {
			allocations = []services.InvoiceAllocation{{
				InvoiceID: uint(invU64),
				Amount:    amount,
			}}
		}
	}

	var jeID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		input := services.ReceivePaymentInput{
			CompanyID:        companyID,
			CustomerID:       uint(custU64),
			EntryDate:        entryDate,
			BankAccountID:    uint(bankU64),
			PaymentMethod:    paymentMethod,
			ARAccountID:      arU64,
			Amount:           amount,
			Memo:             memo,
			Allocations:      allocations,
			Deposits:         depositApps,
			CreditNotes:      cnApps,
			NewDepositAmount: newDepositAmount,
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
	s.ReportCache.InvalidateCompany(companyID)
	slog.Info("report.invalidate",
		"company_id", companyID,
		"reason", "receive_payment",
		"journal_entry_id", jeID,
	)

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

// buildOpenCreditNotesJSON returns a JSON array of issued/partially-applied
// credit notes with BalanceRemaining > 0 — the AR-negative documents that
// can offset invoices in the Receive Payment table. Shape mirrors
// buildOpenDepositsJSON so the unified Alpine renderer works on either.
func buildOpenCreditNotesJSON(s *Server, companyID uint) string {
	type cnJSON struct {
		ID             uint   `json:"id"`
		CustomerID     uint   `json:"customer_id"`
		DocumentNumber string `json:"document_number"`
		DocumentDate   string `json:"document_date"`
		OriginalAmount string `json:"original_amount"`
		Amount         string `json:"amount"`
		Type           string `json:"type"`
	}
	var cns []models.CreditNote
	openStatuses := []models.CreditNoteStatus{
		models.CreditNoteStatusIssued,
		models.CreditNoteStatusPartiallyApplied,
	}
	_ = s.DB.Where("company_id = ? AND status IN ? AND balance_remaining > 0", companyID, openStatuses).
		Order("credit_note_date asc, id asc").
		Find(&cns).Error

	items := make([]cnJSON, 0, len(cns))
	for _, c := range cns {
		items = append(items, cnJSON{
			ID:             c.ID,
			CustomerID:     c.CustomerID,
			DocumentNumber: c.CreditNoteNumber,
			DocumentDate:   c.CreditNoteDate.Format("2006-01-02"),
			OriginalAmount: c.Amount.StringFixed(2),
			Amount:         c.BalanceRemaining.StringFixed(2),
			Type:           "credit_note",
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// buildOpenDepositsJSON returns a JSON array of unapplied Customer Deposits
// for the company — the "negative document" rows the Receive Payment Alpine
// component renders alongside open invoices. Each row mirrors the invoice
// JSON shape so one template can render both.
//
// Only posted / partially_applied deposits with BalanceRemaining > 0 are
// included; applied-out and voided deposits stay off the picker.
func buildOpenDepositsJSON(s *Server, companyID uint) string {
	type depJSON struct {
		ID             uint   `json:"id"`
		CustomerID     uint   `json:"customer_id"`
		DocumentNumber string `json:"document_number"`
		DocumentDate   string `json:"document_date"`
		OriginalAmount string `json:"original_amount"`
		Amount         string `json:"amount"` // = balance_remaining
		Type           string `json:"type"`   // always "deposit" for this endpoint
	}
	var deposits []models.CustomerDeposit
	openStatuses := []models.CustomerDepositStatus{
		models.CustomerDepositStatusPosted,
		models.CustomerDepositStatusPartiallyApplied,
	}
	_ = s.DB.Where("company_id = ? AND status IN ? AND balance_remaining > 0", companyID, openStatuses).
		Order("deposit_date asc, id asc").
		Find(&deposits).Error

	items := make([]depJSON, 0, len(deposits))
	for _, d := range deposits {
		items = append(items, depJSON{
			ID:             d.ID,
			CustomerID:     d.CustomerID,
			DocumentNumber: d.DepositNumber,
			DocumentDate:   d.DepositDate.Format("2006-01-02"),
			OriginalAmount: d.Amount.StringFixed(2),
			Amount:         d.BalanceRemaining.StringFixed(2),
			Type:           "deposit",
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
	var company models.Company
	s.DB.Select("id", "base_currency_code").First(&company, companyID)

	vm := pages.PayBillsVM{
		HasCompany:        true,
		Accounts:          accounts,
		OpenBills:         openBills,
		BaseCurrency:      company.BaseCurrencyCode,
		AccountCurrencies: buildAccountCurrencies(accounts),
		Saved:             c.Query("saved") == "1",
		EntryDate:         time.Now().Format("2006-01-02"),
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
	var company models.Company
	s.DB.Select("id", "base_currency_code").First(&company, companyID)
	baseCurrency := company.BaseCurrencyCode

	entryDateRaw := strings.TrimSpace(c.FormValue("entry_date"))
	bankIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	exchangeRateRaw := strings.TrimSpace(c.FormValue("exchange_rate"))
	memo := strings.TrimSpace(c.FormValue("memo"))

	vm := pages.PayBillsVM{
		HasCompany:        true,
		Accounts:          accounts,
		OpenBills:         openBills,
		BaseCurrency:      baseCurrency,
		AccountCurrencies: buildAccountCurrencies(accounts),
		EntryDate:         entryDateRaw,
		BankAccountID:     bankIDRaw,
		ExchangeRate:      exchangeRateRaw,
		Memo:              memo,
	}

	entryDate, err := time.Parse("2006-01-02", entryDateRaw)
	if err != nil {
		vm.DateError = "Payment date is required."
	}

	bankU64, err := services.ParseUint(bankIDRaw)
	if err != nil || bankU64 == 0 {
		vm.BankError = "Bank account is required."
	}

	if vm.DateError != "" || vm.BankError != "" {
		return pages.PayBills(vm).Render(c.Context(), c)
	}

	// Build a bill lookup map from the already-loaded bills.
	billByID := make(map[uint]models.Bill, len(openBills))
	for _, b := range openBills {
		billByID[b.ID] = b
	}

	// Collect selected bills and their payment amounts from the form.
	selectedIDs := c.Request().PostArgs().PeekMultiBytes([]byte("bill_selected"))
	if len(selectedIDs) == 0 {
		vm.FormError = "Please select at least one bill to pay."
		return pages.PayBills(vm).Render(c.Context(), c)
	}

	billAmounts := make(map[string]string, len(selectedIDs))
	var billPayments []services.BillPayment
	var detectedCurrency string // normalised: "" = base, "USD" = foreign
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

		// Validate same currency across all selected bills.
		bill, found := billByID[uint(idU64)]
		if !found {
			vm.FormError = "Bill not found."
			vm.BillAmounts = billAmounts
			return pages.PayBills(vm).Render(c.Context(), c)
		}
		billCurr := bill.CurrencyCode
		if billCurr == baseCurrency {
			billCurr = "" // normalise base currency to empty
		}
		if len(billPayments) == 0 {
			detectedCurrency = billCurr
		} else if billCurr != detectedCurrency {
			vm.FormError = "All selected bills must use the same currency. Please pay bills of different currencies separately."
			vm.BillAmounts = billAmounts
			return pages.PayBills(vm).Render(c.Context(), c)
		}

		billPayments = append(billPayments, services.BillPayment{
			BillID: uint(idU64),
			Amount: amt,
		})
	}

	// Resolve AP account automatically by bill currency.
	apAccountID, apErr := s.resolveAPAccount(companyID, detectedCurrency)
	if apErr != nil {
		effectiveCurr := detectedCurrency
		if effectiveCurr == "" {
			effectiveCurr = baseCurrency
		}
		vm.FormError = "No Accounts Payable account found for currency " + effectiveCurr +
			". Please set up a matching AP account under Company > Sales Tax or Chart of Accounts."
		vm.BillAmounts = billAmounts
		return pages.PayBills(vm).Render(c.Context(), c)
	}

	// Parse optional user-supplied exchange rate override.
	var exchangeRateOverride decimal.Decimal
	if exchangeRateRaw != "" {
		exchangeRateOverride, err = services.ParseDecimalMoney(exchangeRateRaw)
		if err != nil || !exchangeRateOverride.IsPositive() {
			vm.ExchangeRateError = "Exchange rate must be a positive number (e.g. 0.73)."
			vm.BillAmounts = billAmounts
			return pages.PayBills(vm).Render(c.Context(), c)
		}
	}

	var jeID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		jeID, txErr = services.RecordPayBills(tx, services.PayBillsInput{
			CompanyID:            companyID,
			EntryDate:            entryDate,
			BankAccountID:        uint(bankU64),
			APAccountID:          apAccountID,
			Bills:                billPayments,
			Memo:                 memo,
			ExchangeRateOverride: exchangeRateOverride,
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
	s.ReportCache.InvalidateCompany(companyID)
	slog.Info("report.invalidate",
		"company_id", companyID,
		"reason", "pay_bills",
		"journal_entry_id", jeID,
	)

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

	// Preserve current in-progress selections and add the accepted suggestion lines
	// so a redirect/resume shows exactly what the user accepted.
	selectedSet := map[string]struct{}{}
	if draft, err := services.GetReconcileDraft(s.DB, companyID, sugg.AccountID); err == nil && draft != nil {
		for _, id := range parseReconcileSelectedLineIDs(draft.SelectedLineIDs) {
			selectedSet[id] = struct{}{}
		}
	}
	for _, id := range lineIDs {
		selectedSet[fmt.Sprintf("%d", id)] = struct{}{}
	}
	selectedIDs := make([]string, 0, len(selectedSet))
	for id := range selectedSet {
		selectedIDs = append(selectedIDs, id)
	}
	lineIDsJSON := "[]"
	if len(selectedIDs) > 0 {
		b, _ := json.Marshal(selectedIDs)
		lineIDsJSON = string(b)
	}
	if statementDateStr != "" && endingBalanceStr != "" {
		_ = services.UpsertReconcileDraft(s.DB, companyID, sugg.AccountID, statementDateStr, endingBalanceStr, lineIDsJSON)
	}

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

// resolveAPAccount finds the active AP account for the given bill currency.
// currencyCode is empty (or equals base currency, already normalised to "") for
// base-currency bills. Foreign bills pass the ISO 4217 code (e.g. "USD").
func (s *Server) resolveAPAccount(companyID uint, currencyCode string) (uint, error) {
	var acc models.Account
	q := s.DB.Where("company_id = ? AND is_active = ? AND detail_account_type = ?",
		companyID, true, string(models.DetailAccountsPayable))
	if currencyCode == "" {
		q = q.Where("currency_mode = ?", string(models.CurrencyModeBaseOnly))
	} else {
		q = q.Where("currency_mode = ? AND currency_code = ?",
			string(models.CurrencyModeFixedForeign), currencyCode)
	}
	if err := q.First(&acc).Error; err != nil {
		return 0, err
	}
	return acc.ID, nil
}

// buildAccountCurrencies returns a map of account ID → currency code for
// accounts with currency_mode = fixed_foreign. Base-only accounts are omitted
// (the frontend treats missing entries as base currency).
func buildAccountCurrencies(accounts []models.Account) map[uint]string {
	m := make(map[uint]string)
	for _, a := range accounts {
		if a.CurrencyMode == models.CurrencyModeFixedForeign && a.CurrencyCode != nil {
			m[a.ID] = *a.CurrencyCode
		}
	}
	return m
}

// expenseAccountsForCompany returns all active expense accounts for a company,
// used to populate the service charge account selector in reconciliation setup.
func (s *Server) expenseAccountsForCompany(companyID uint) ([]models.Account, error) {
	var accounts []models.Account
	err := s.DB.
		Where("company_id = ? AND root_account_type = ? AND is_active = true", companyID, models.RootExpense).
		Order("code asc").
		Find(&accounts).Error
	return accounts, err
}

// incomeAccountsForCompany returns all active revenue/income accounts for a company,
// used to populate the interest earned account selector in reconciliation setup.
func (s *Server) incomeAccountsForCompany(companyID uint) ([]models.Account, error) {
	var accounts []models.Account
	err := s.DB.
		Where("company_id = ? AND root_account_type = ? AND is_active = true", companyID, models.RootRevenue).
		Order("code asc").
		Find(&accounts).Error
	return accounts, err
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
