// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// ── Accounting Mappings ──────────────────────────────────────────────────────

func (s *Server) handleAccountingMappings(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	accounts, _ := services.ListChannelAccounts(s.DB, companyID)

	// Load all GL accounts for dropdowns.
	var allAccounts []models.Account
	s.DB.Where("company_id = ? AND is_active = true", companyID).Order("code ASC").Find(&allAccounts)

	// Load existing mappings per channel account.
	mappings := make(map[uint]*models.ChannelAccountingMapping)
	for _, a := range accounts {
		m, _ := services.GetAccountingMapping(s.DB, companyID, a.ID)
		if m != nil {
			mappings[a.ID] = m
		}
	}

	vm := pages.AccountingMappingsVM{
		HasCompany:      true,
		ChannelAccounts: accounts,
		GLAccounts:      allAccounts,
		Mappings:        mappings,
		FormError:       strings.TrimSpace(c.Query("error")),
		Saved:           c.Query("saved") == "1",
	}
	return pages.AccountingMappings(vm).Render(c.Context(), c)
}

func (s *Server) handleAccountingMappingSave(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	acctIDRaw := strings.TrimSpace(c.FormValue("channel_account_id"))
	acctID, _ := strconv.ParseUint(acctIDRaw, 10, 64)
	if acctID == 0 {
		return redirectErr(c, "/settings/channels/accounting", "channel account is required")
	}

	m := models.ChannelAccountingMapping{
		CompanyID:                 companyID,
		ChannelAccountID:          uint(acctID),
		ClearingAccountID:         parseOptionalUint(c.FormValue("clearing_account_id")),
		FeeExpenseAccountID:       parseOptionalUint(c.FormValue("fee_expense_account_id")),
		RefundAccountID:           parseOptionalUint(c.FormValue("refund_account_id")),
		ShippingIncomeAccountID:   parseOptionalUint(c.FormValue("shipping_income_account_id")),
		ShippingExpenseAccountID:  parseOptionalUint(c.FormValue("shipping_expense_account_id")),
		MarketplaceTaxAccountID:   parseOptionalUint(c.FormValue("marketplace_tax_account_id")),
	}

	if err := services.SaveAccountingMapping(s.DB, &m); err != nil {
		return redirectErr(c, "/settings/channels/accounting", err.Error())
	}
	return c.Redirect("/settings/channels/accounting?saved=1", fiber.StatusSeeOther)
}

// ── Settlements ──────────────────────────────────────────────────────────────

func (s *Server) handleSettlements(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	settlements, _ := services.ListSettlements(s.DB, companyID, 50)
	accounts, _ := services.ListChannelAccounts(s.DB, companyID)

	// Build summaries with unmapped count.
	var summaries []pages.SettlementSummary
	for _, st := range settlements {
		lines, _ := services.GetSettlementLines(s.DB, companyID, st.ID)
		totals := services.ComputeSettlementTotals(lines)
		st.GrossAmount = totals.GrossAmount
		st.FeeAmount = totals.FeeAmount
		st.NetAmount = totals.NetAmount

		unmapped := services.CountUnmappedLines(s.DB, companyID, st.ID)
		summaries = append(summaries, pages.SettlementSummary{Settlement: st, UnmappedCount: unmapped})
	}

	vm := pages.SettlementsVM{
		HasCompany:  true,
		Settlements: summaries,
		Accounts:    accounts,
		Created:     c.Query("created") == "1",
		CreateError: c.Query("createerror") == "1",
	}
	return pages.Settlements(vm).Render(c.Context(), c)
}

func (s *Server) handleSettlementCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	acctIDRaw := strings.TrimSpace(c.FormValue("channel_account_id"))
	extID := strings.TrimSpace(c.FormValue("external_settlement_id"))
	dateRaw := strings.TrimSpace(c.FormValue("settlement_date"))
	currency := strings.TrimSpace(c.FormValue("currency_code"))
	lineCountRaw := strings.TrimSpace(c.FormValue("line_count"))

	acctID, _ := strconv.ParseUint(acctIDRaw, 10, 64)
	if acctID == 0 {
		return c.Redirect("/settings/channels/settlements", fiber.StatusSeeOther)
	}

	var settlementDate *time.Time
	if d, err := time.Parse("2006-01-02", dateRaw); err == nil {
		settlementDate = &d
	}

	settlement := models.ChannelSettlement{
		CompanyID:            companyID,
		ChannelAccountID:     uint(acctID),
		ExternalSettlementID: extID,
		SettlementDate:       settlementDate,
		CurrencyCode:         currency,
		RawPayload:           datatypes.JSON("{}"),
	}

	lineCount, _ := strconv.Atoi(lineCountRaw)
	var lines []models.ChannelSettlementLine
	for i := 0; i < lineCount; i++ {
		lt := strings.TrimSpace(c.FormValue(strings.Replace("line_type[%d]", "%d", strconv.Itoa(i), 1)))
		desc := strings.TrimSpace(c.FormValue(strings.Replace("line_desc[%d]", "%d", strconv.Itoa(i), 1)))
		amtRaw := strings.TrimSpace(c.FormValue(strings.Replace("line_amount[%d]", "%d", strconv.Itoa(i), 1)))

		if lt == "" {
			continue
		}

		amt, _ := decimal.NewFromString(amtRaw)
		lineType := models.SettlementLineType(lt)

		lines = append(lines, models.ChannelSettlementLine{
			LineType:    lineType,
			Description: desc,
			Amount:      amt,
			RawPayload:  datatypes.JSON("{}"),
		})
	}

	totals := services.ComputeSettlementTotals(lines)
	settlement.GrossAmount = totals.GrossAmount
	settlement.FeeAmount = totals.FeeAmount
	settlement.NetAmount = totals.NetAmount

	if err := services.CreateSettlementWithLines(s.DB, &settlement, lines); err != nil {
		return c.Redirect("/settings/channels/settlements?createerror=1", fiber.StatusSeeOther)
	}
	return c.Redirect("/settings/channels/settlements?created=1", fiber.StatusSeeOther)
}

func (s *Server) handleSettlementRecordPayout(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id64 == 0 {
		return c.Redirect("/settings/channels/settlements", fiber.StatusSeeOther)
	}
	detailPath := "/settings/channels/settlements/" + c.Params("id")

	bankAcctIDRaw := strings.TrimSpace(c.FormValue("bank_account_id"))
	bankAcctID, _ := strconv.ParseUint(bankAcctIDRaw, 10, 64)
	if bankAcctID == 0 {
		return redirectErr(c, detailPath, "bank account is required to record a payout")
	}

	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}

	_, err := services.RecordPayout(s.DB, services.RecordPayoutInput{
		CompanyID:     companyID,
		SettlementID:  uint(id64),
		BankAccountID: uint(bankAcctID),
		EntryDate:     time.Now(),
	}, actor)
	if err != nil {
		return redirectErr(c, detailPath, err.Error())
	}

	return c.Redirect(detailPath+"?payout=1", fiber.StatusSeeOther)
}

func (s *Server) handleSettlementReverseFee(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id64, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id64 == 0 {
		return c.Redirect("/settings/channels/settlements", fiber.StatusSeeOther)
	}
	detailPath := "/settings/channels/settlements/" + c.Params("id")
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	_, err := services.ReverseSettlementFeePosting(s.DB, companyID, uint(id64), actor)
	if err != nil {
		return redirectErr(c, detailPath, err.Error())
	}
	return c.Redirect(detailPath+"?feereversed=1", fiber.StatusSeeOther)
}

func (s *Server) handleSettlementReversePayout(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id64, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id64 == 0 {
		return c.Redirect("/settings/channels/settlements", fiber.StatusSeeOther)
	}
	detailPath := "/settings/channels/settlements/" + c.Params("id")
	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}
	_, err := services.ReversePayoutRecording(s.DB, companyID, uint(id64), actor)
	if err != nil {
		return redirectErr(c, detailPath, err.Error())
	}
	return c.Redirect(detailPath+"?payoutreversed=1", fiber.StatusSeeOther)
}

func (s *Server) handleSettlementPost(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id64 == 0 {
		return c.Redirect("/settings/channels/settlements", fiber.StatusSeeOther)
	}

	user := UserFromCtx(c)
	actor := "system"
	if user != nil && user.Email != "" {
		actor = user.Email
	}

	_, err := services.PostSettlementToJournalEntry(s.DB, companyID, uint(id64), actor)
	if err != nil {
		return c.Redirect("/settings/channels/settlements/"+c.Params("id")+"?posterr=1", fiber.StatusSeeOther)
	}

	return c.Redirect("/settings/channels/settlements/"+c.Params("id")+"?posted=1", fiber.StatusSeeOther)
}

func (s *Server) handleSettlementDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	id64, _ := strconv.ParseUint(c.Params("id"), 10, 64)
	if id64 == 0 {
		return c.Redirect("/settings/channels/settlements", fiber.StatusSeeOther)
	}

	settlement, err := services.GetSettlement(s.DB, companyID, uint(id64))
	if err != nil {
		return c.Redirect("/settings/channels/settlements", fiber.StatusSeeOther)
	}

	sLines, _ := services.GetSettlementLines(s.DB, companyID, settlement.ID)
	displayTotals := services.ComputeSettlementTotals(sLines)
	settlement.GrossAmount = displayTotals.GrossAmount
	settlement.FeeAmount = displayTotals.FeeAmount
	settlement.NetAmount = displayTotals.NetAmount
	unmapped := services.CountUnmappedLines(s.DB, companyID, settlement.ID)

	// Check postability.
	postErr := services.ValidateSettlementPostable(s.DB, companyID, settlement.ID)

	// Check payout recordability.
	payoutErr := services.ValidatePayoutRecordable(s.DB, companyID, settlement.ID)
	payoutSubmitErr := strings.TrimSpace(c.Query("error"))

	// Load bank accounts for payout form.
	var bankAccounts []models.Account
	s.DB.Where("company_id = ? AND is_active = true AND detail_account_type = ?",
		companyID, models.DetailBank).Order("code ASC").Find(&bankAccounts)

	// Check reversal eligibility.
	feeRevErr := services.ValidateSettlementFeeReversible(s.DB, companyID, settlement.ID)
	payoutRevErr := services.ValidatePayoutReversible(s.DB, companyID, settlement.ID)

	vm := pages.SettlementDetailVM{
		HasCompany:    true,
		Settlement:    *settlement,
		Lines:         sLines,
		UnmappedCount: int(unmapped),
		IsPostable:    postErr == nil,
		PostableError: "",
		JustPosted:    c.Query("posted") == "1",
		// Payout state
		IsPayoutRecordable: payoutErr == nil,
		PayoutError:        "",
		PayoutSubmitError:  payoutSubmitErr,
		JustPayout:         c.Query("payout") == "1",
		BankAccounts:       bankAccounts,
		// Reversal state
		IsFeeReversible:    feeRevErr == nil,
		JustFeeReversed:    c.Query("feereversed") == "1",
		IsPayoutReversible: payoutRevErr == nil,
		JustPayoutReversed: c.Query("payoutreversed") == "1",
	}
	if postErr != nil {
		vm.PostableError = postErr.Error()
	}
	if payoutErr != nil {
		vm.PayoutError = payoutErr.Error()
	}
	if feeRevErr != nil {
		vm.FeeReverseError = feeRevErr.Error()
	}
	if payoutRevErr != nil {
		vm.PayoutReverseError = payoutRevErr.Error()
	}
	return pages.SettlementDetail(vm).Render(c.Context(), c)
}
