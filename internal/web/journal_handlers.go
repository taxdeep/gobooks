// 遵循project_guide.md
package web

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/searchprojection/producers"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

var buildExchangeRateProvider = func() services.ExchangeRateProvider {
	return services.NewFrankfurterProvider()
}

func journalEntryPageVM(companyID uint, currencyCtx services.CompanyCurrencyContext, accounts []models.Account, customers []models.Customer, vendors []models.Vendor, formError string, saved bool) pages.JournalEntryVM {
	return pages.JournalEntryVM{
		HasCompany:                 true,
		ActiveCompanyID:            companyID,
		BaseCurrencyCode:           currencyCtx.BaseCurrencyCode,
		MultiCurrencyEnabled:       currencyCtx.MultiCurrencyEnabled,
		CompanyCurrencies:          currencyCtx.CompanyCurrencies,
		TransactionCurrencyOptions: currencyCtx.AllowedCurrencyOptions,
		DefaultTransactionCurrency: currencyCtx.BaseCurrencyCode,
		Accounts:                   accounts,
		AccountsDataJSON:           pages.JournalAccountsDataJSON(accounts),
		Customers:                  customers,
		Vendors:                    vendors,
		FormError:                  formError,
		Saved:                      saved,
	}
}

func (s *Server) handleJournalEntryForm(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	currencyCtx, err := services.LoadCompanyCurrencyContext(s.DB, companyID)
	if err != nil {
		return pages.JournalEntryPage(pages.JournalEntryVM{
			HasCompany:       true,
			ActiveCompanyID:  companyID,
			FormError:        "Could not load company currency settings.",
			AccountsDataJSON: "[]",
		}).Render(c.Context(), c)
	}

	accounts, err := s.activeAccountsForCompany(companyID)
	if err != nil {
		return pages.JournalEntryPage(pages.JournalEntryVM{
			HasCompany:                 true,
			ActiveCompanyID:            companyID,
			BaseCurrencyCode:           currencyCtx.BaseCurrencyCode,
			MultiCurrencyEnabled:       currencyCtx.MultiCurrencyEnabled,
			CompanyCurrencies:          currencyCtx.CompanyCurrencies,
			TransactionCurrencyOptions: currencyCtx.AllowedCurrencyOptions,
			DefaultTransactionCurrency: currencyCtx.BaseCurrencyCode,
			FormError:                  "Could not load accounts.",
			AccountsDataJSON:           "[]",
		}).Render(c.Context(), c)
	}

	var customers []models.Customer
	_ = s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&customers).Error
	var vendors []models.Vendor
	_ = s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vendors).Error

	return pages.JournalEntryPage(journalEntryPageVM(companyID, currencyCtx, accounts, customers, vendors, "", c.Query("saved") == "1")).Render(c.Context(), c)
}

type postedLine struct {
	AccountID string
	Debit     string
	Credit    string
	Memo      string
	Party     string
}

func parseJournalExchangeRateCandidate(c *fiber.Ctx) (services.JournalEntrySnapshotCandidate, error) {
	candidate := services.JournalEntrySnapshotCandidate{
		ExchangeRateSource: strings.TrimSpace(c.FormValue("exchange_rate_source")),
	}

	rateRaw := strings.TrimSpace(c.FormValue("exchange_rate"))
	if rateRaw != "" {
		rate, err := decimal.NewFromString(rateRaw)
		if err != nil {
			return candidate, fmt.Errorf("exchange rate must be a valid number")
		}
		candidate.ExchangeRate = rate
	}

	dateRaw := strings.TrimSpace(c.FormValue("exchange_rate_date"))
	if dateRaw != "" {
		date, err := time.Parse("2006-01-02", dateRaw)
		if err != nil {
			return candidate, fmt.Errorf("exchange-rate date must be a valid date")
		}
		candidate.ExchangeRateDate = date
	}

	snapshotIDRaw := strings.TrimSpace(c.FormValue("exchange_rate_snapshot_id"))
	if snapshotIDRaw != "" {
		id, err := services.ParseUint(snapshotIDRaw)
		if err != nil || id == 0 {
			return candidate, fmt.Errorf("exchange-rate snapshot is invalid")
		}
		snapshotID := uint(id)
		candidate.SnapshotID = &snapshotID
	}

	return candidate, nil
}

func (s *Server) handleJournalEntryPost(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	currencyCtx, _ := services.LoadCompanyCurrencyContext(s.DB, companyID)

	accounts, _ := s.activeAccountsForCompany(companyID)
	var customers []models.Customer
	_ = s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&customers).Error
	var vendors []models.Vendor
	_ = s.DB.Where("company_id = ?", companyID).Order("name asc").Find(&vendors).Error

	renderFormError := func(msg string) error {
		return pages.JournalEntryPage(journalEntryPageVM(companyID, currencyCtx, accounts, customers, vendors, msg, false)).Render(c.Context(), c)
	}

	entryDateRaw := strings.TrimSpace(c.FormValue("entry_date"))
	journalNo := strings.TrimSpace(c.FormValue("journal_no"))
	transactionCurrencyCode := strings.TrimSpace(c.FormValue("transaction_currency_code"))

	if entryDateRaw == "" {
		return renderFormError("Date is required.")
	}

	entryDate, err := time.Parse("2006-01-02", entryDateRaw)
	if err != nil {
		return renderFormError("Date must be a valid date.")
	}

	snapshotCandidate, err := parseJournalExchangeRateCandidate(c)
	if err != nil {
		return renderFormError(err.Error())
	}

	re := regexp.MustCompile(`^lines\[(\d+)\]\[(account_id|debit|credit|memo|party)\]$`)
	linesMap := map[string]*postedLine{}

	c.Context().PostArgs().VisitAll(func(k, v []byte) {
		key := string(k)
		m := re.FindStringSubmatch(key)
		if len(m) != 3 {
			return
		}

		idx := m[1]
		field := m[2]
		val := strings.TrimSpace(string(v))

		pl := linesMap[idx]
		if pl == nil {
			pl = &postedLine{}
			linesMap[idx] = pl
		}

		switch field {
		case "account_id":
			pl.AccountID = val
		case "debit":
			pl.Debit = val
		case "credit":
			pl.Credit = val
		case "memo":
			pl.Memo = val
		case "party":
			pl.Party = val
		}
	})

	drafts := make([]services.JournalLineDraft, 0, len(linesMap))
	for _, pl := range linesMap {
		drafts = append(drafts, services.JournalLineDraft{
			AccountID: pl.AccountID,
			Debit:     pl.Debit,
			Credit:    pl.Credit,
			Memo:      pl.Memo,
			Party:     pl.Party,
		})
	}

	prepared, err := services.PrepareJournalEntryForSave(s.DB, services.PrepareJournalEntryInput{
		CompanyID:               companyID,
		EntryDate:               entryDate,
		JournalNo:               journalNo,
		TransactionCurrencyCode: transactionCurrencyCode,
		Snapshot:                snapshotCandidate,
		LineDrafts:              drafts,
	})
	if err != nil {
		return renderFormError(err.Error())
	}

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID

	var postedJEID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		je := prepared.JournalEntry
		if err := tx.Create(&je).Error; err != nil {
			return err
		}
		postedJEID = je.ID

		for i := range prepared.JournalLines {
			prepared.JournalLines[i].JournalEntryID = je.ID
		}
		if err := tx.Create(&prepared.JournalLines).Error; err != nil {
			return err
		}
		// Secondary book amounts — no-op when no secondary books are configured.
		if err := services.WriteSecondaryBookAmounts(tx, companyID, prepared.JournalLines,
			prepared.JournalEntry.TransactionCurrencyCode,
			prepared.JournalEntry.EntryDate,
			models.FXPostingReasonTransaction); err != nil {
			return err
		}
		return services.ProjectToLedger(tx, companyID, services.LedgerPostInput{
			JournalEntry: je,
			Lines:        prepared.JournalLines,
		})
	}); err != nil {
		return renderFormError("Could not save journal entry. Please try again.")
	}

	services.TryWriteAuditLogWithContext(s.DB, "journal.posted", "journal_entry", postedJEID, actor, map[string]any{
		"journal_no":                   journalNo,
		"line_count":                   len(prepared.JournalLines),
		"entry_date":                   entryDateRaw,
		"company_id":                   companyID,
		"transaction_currency_code":    prepared.JournalEntry.TransactionCurrencyCode,
		"exchange_rate":                prepared.JournalEntry.ExchangeRate.String(),
		"exchange_rate_source":         prepared.JournalEntry.ExchangeRateSource,
		"exchange_rate_effective_date": prepared.JournalEntry.ExchangeRateDate.Format("2006-01-02"),
	}, &cid, &uid)
	s.ReportCache.InvalidateCompany(companyID)
	_ = producers.ProjectJournalEntry(c.Context(), s.DB, s.SearchProjector, companyID, postedJEID)

	return c.Redirect("/journal-entry?saved=1", fiber.StatusSeeOther)
}

func (s *Server) handleExchangeRateGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "active company required"})
	}

	currencyCtx, err := services.LoadCompanyCurrencyContext(s.DB, companyID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load company currency settings"})
	}

	transactionCurrencyCode, err := services.NormalizeTransactionCurrencyCode(currencyCtx, c.Query("transaction_currency_code"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	dateRaw := strings.TrimSpace(c.Query("date"))
	rateDate := time.Now().UTC()
	if dateRaw != "" {
		rateDate, err = time.Parse("2006-01-02", dateRaw)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "date must be a valid YYYY-MM-DD value"})
		}
	}

	allowProviderFetch := strings.EqualFold(strings.TrimSpace(c.Query("allow_provider_fetch")), "1") ||
		strings.EqualFold(strings.TrimSpace(c.Query("allow_provider_fetch")), "true")
	snapshot, err := services.ResolveExchangeRateSnapshot(context.Background(), s.DB, services.ExchangeRateResolveOptions{
		CompanyID:               companyID,
		TransactionCurrencyCode: transactionCurrencyCode,
		BaseCurrencyCode:        currencyCtx.BaseCurrencyCode,
		Date:                    rateDate,
		AllowProviderFetch:      allowProviderFetch,
		Provider:                buildExchangeRateProvider(),
	})
	if err != nil {
		status := fiber.StatusUnprocessableEntity
		if err == services.ErrNoRate {
			status = fiber.StatusNotFound
		}
		return c.Status(status).JSON(fiber.Map{"error": err.Error()})
	}

	response := fiber.Map{
		"transaction_currency_code": snapshot.TransactionCurrencyCode,
		"base_currency_code":        currencyCtx.BaseCurrencyCode,
		"exchange_rate":             snapshot.ExchangeRate.String(),
		"exchange_rate_date":        snapshot.ExchangeRateDate.Format("2006-01-02"),
		"exchange_rate_source":      snapshot.ExchangeRateSource,
		"source_label":              snapshot.SourceLabel,
		"is_identity":               snapshot.IsIdentity,
	}
	if snapshot.SnapshotID != nil {
		response["snapshot_id"] = *snapshot.SnapshotID
	}
	return c.JSON(response)
}

func (s *Server) handleJournalEntryDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idU64, err := services.ParseUint(strings.TrimSpace(c.Params("id")))
	if err != nil || idU64 == 0 {
		return c.Redirect("/journal-entry/list", fiber.StatusSeeOther)
	}

	var je models.JournalEntry
	if err := s.DB.Preload("Lines.Account").
		Where("id = ? AND company_id = ?", uint(idU64), companyID).
		First(&je).Error; err != nil {
		return c.Redirect("/journal-entry/list", fiber.StatusSeeOther)
	}

	var company models.Company
	if err := s.DB.Select("id", "base_currency_code").First(&company, companyID).Error; err != nil {
		return c.Redirect("/journal-entry/list", fiber.StatusSeeOther)
	}

	customerIDs := make([]uint, 0)
	vendorIDs := make([]uint, 0)
	for _, line := range je.Lines {
		switch line.PartyType {
		case models.PartyTypeCustomer:
			customerIDs = append(customerIDs, line.PartyID)
		case models.PartyTypeVendor:
			vendorIDs = append(vendorIDs, line.PartyID)
		}
	}

	customersByID := map[uint]string{}
	if len(customerIDs) > 0 {
		var customers []models.Customer
		_ = s.DB.Select("id", "name").Where("company_id = ? AND id IN ?", companyID, customerIDs).Find(&customers).Error
		for _, customer := range customers {
			customersByID[customer.ID] = customer.Name
		}
	}
	vendorsByID := map[uint]string{}
	if len(vendorIDs) > 0 {
		var vendors []models.Vendor
		_ = s.DB.Select("id", "name").Where("company_id = ? AND id IN ?", companyID, vendorIDs).Find(&vendors).Error
		for _, vendor := range vendors {
			vendorsByID[vendor.ID] = vendor.Name
		}
	}

	fxResolver := services.NewJournalEntryFXResolver(s.DB, company.BaseCurrencyCode)
	fxState, err := fxResolver.BuildReadState(je)
	if err != nil {
		return c.Redirect("/journal-entry/list", fiber.StatusSeeOther)
	}

	lines := make([]pages.JournalEntryDetailLineItem, 0, len(je.Lines))
	txDebitTotal := decimal.Zero
	txCreditTotal := decimal.Zero
	baseDebitTotal := decimal.Zero
	baseCreditTotal := decimal.Zero
	for _, line := range je.Lines {
		txDebitLabel := "—"
		txCreditLabel := "—"
		if fxState.TransactionAmountsPresent {
			txDebit := line.TxDebit
			txCredit := line.TxCredit
			if fxState.TransactionCurrencyCode == company.BaseCurrencyCode {
				if txDebit.IsZero() && !line.Debit.IsZero() {
					txDebit = line.Debit
				}
				if txCredit.IsZero() && !line.Credit.IsZero() {
					txCredit = line.Credit
				}
			}
			txDebitTotal = txDebitTotal.Add(txDebit)
			txCreditTotal = txCreditTotal.Add(txCredit)
			txDebitLabel = pages.Money(txDebit)
			txCreditLabel = pages.Money(txCredit)
		}
		baseDebitTotal = baseDebitTotal.Add(line.Debit)
		baseCreditTotal = baseCreditTotal.Add(line.Credit)
		lines = append(lines, pages.JournalEntryDetailLineItem{
			AccountCode: line.Account.Code,
			AccountName: line.Account.Name,
			Memo:        line.Memo,
			PartyLabel:  pages.JournalPartyLabel(line, customersByID, vendorsByID),
			TxDebit:     txDebitLabel,
			TxCredit:    txCreditLabel,
			Debit:       pages.Money(line.Debit),
			Credit:      pages.Money(line.Credit),
		})
	}

	txDebitTotalLabel := "—"
	txCreditTotalLabel := "—"
	if fxState.TransactionAmountsPresent {
		txDebitTotalLabel = pages.Money(txDebitTotal)
		txCreditTotalLabel = pages.Money(txCreditTotal)
	}

	exchangeRateLabel := ""
	if fxState.ExchangeRate.GreaterThan(decimal.Zero) {
		exchangeRateLabel = fxState.ExchangeRate.String()
	}
	exchangeRateDateLabel := ""
	if !fxState.ExchangeRateDate.IsZero() {
		exchangeRateDateLabel = fxState.ExchangeRateDate.Format("2006-01-02")
	}

	return pages.JournalEntryDetailPage(pages.JournalEntryDetailVM{
		HasCompany:                 true,
		ID:                         je.ID,
		JournalNo:                  je.JournalNo,
		EntryDate:                  je.EntryDate.Format("2006-01-02"),
		Status:                     string(je.Status),
		BaseCurrencyCode:           company.BaseCurrencyCode,
		TransactionCurrencyCode:    fxState.TransactionCurrencyCode,
		TransactionCurrencyDisplay: fxState.TransactionCurrencyDisplay,
		ExchangeRate:               exchangeRateLabel,
		ExchangeRateDate:           exchangeRateDateLabel,
		ExchangeRateSource:         fxState.ExchangeRateSource,
		ExchangeRateSourceLabel:    fxState.ExchangeRateSourceLabel,
		IsForeignCurrency:          fxState.IsForeignCurrency,
		TransactionAmountsPresent:  fxState.TransactionAmountsPresent,
		FXSnapshotNote:             fxState.SnapshotNote,
		Lines:                      lines,
		TxDebitTotal:               txDebitTotalLabel,
		TxCreditTotal:              txCreditTotalLabel,
		BaseDebitTotal:             pages.Money(baseDebitTotal),
		BaseCreditTotal:            pages.Money(baseCreditTotal),
	}).Render(c.Context(), c)
}

func (s *Server) handleJournalEntryList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	formError := ""
	switch c.Query("error") {
	case "already-reversed":
		formError = "This journal entry is already reversed."
	case "legacy-fx-unavailable":
		formError = services.LegacyForeignJournalEntryReversalBlockedMessage
	case "reverse-failed":
		formError = "Could not reverse this journal entry."
	}

	var company models.Company
	if err := s.DB.Select("id", "base_currency_code").First(&company, companyID).Error; err != nil {
		return pages.JournalEntryListPage(pages.JournalEntryListVM{
			HasCompany: true,
			Active:     "Journal Entry",
			Items:      []pages.JournalEntryListItem{},
			FormError:  "Could not load company currency settings.",
		}).Render(c.Context(), c)
	}

	var entries []models.JournalEntry
	if err := s.DB.Preload("Lines").Where("company_id = ?", companyID).Order("entry_date desc, id desc").Limit(200).Find(&entries).Error; err != nil {
		return pages.JournalEntryListPage(pages.JournalEntryListVM{
			HasCompany: true,
			Active:     "Journal Entry",
			Items:      []pages.JournalEntryListItem{},
			FormError:  "Could not load journal entries.",
		}).Render(c.Context(), c)
	}

	reversedFromSet := map[uint]bool{}
	for _, e := range entries {
		if e.ReversedFromID != nil {
			reversedFromSet[*e.ReversedFromID] = true
		}
	}

	fxResolver := services.NewJournalEntryFXResolver(s.DB, company.BaseCurrencyCode)
	items := make([]pages.JournalEntryListItem, 0, len(entries))
	for _, e := range entries {
		totalDebit := decimal.Zero
		totalCredit := decimal.Zero
		for _, l := range e.Lines {
			totalDebit = totalDebit.Add(l.Debit)
			totalCredit = totalCredit.Add(l.Credit)
		}
		canReverse := e.ReversedFromID == nil && !reversedFromSet[e.ID]
		reverseHint := ""
		if e.ReversedFromID != nil {
			reverseHint = "This is already a reversal entry."
		} else if reversedFromSet[e.ID] {
			reverseHint = "Already reversed."
		}
		fxState, err := fxResolver.BuildReadState(e)
		if err != nil {
			return pages.JournalEntryListPage(pages.JournalEntryListVM{
				HasCompany: true,
				Active:     "Journal Entry",
				Items:      []pages.JournalEntryListItem{},
				FormError:  "Could not resolve journal-entry FX summaries.",
			}).Render(c.Context(), c)
		}
		if canReverse && !fxState.ReversalAllowed {
			canReverse = false
			reverseHint = fxState.ReversalBlockedReason
		}
		items = append(items, pages.JournalEntryListItem{
			ID:                         e.ID,
			EntryDate:                  e.EntryDate.Format("2006-01-02"),
			JournalNo:                  e.JournalNo,
			LineCount:                  len(e.Lines),
			TotalDebit:                 pages.Money(totalDebit),
			TotalCredit:                pages.Money(totalCredit),
			TransactionCurrencyDisplay: fxState.TransactionCurrencyDisplay,
			ExchangeRateSourceLabel:    fxState.ExchangeRateSourceLabel,
			CanReverse:                 canReverse,
			ReverseHint:                reverseHint,
		})
	}

	return pages.JournalEntryListPage(pages.JournalEntryListVM{
		HasCompany: true,
		Active:     "Journal Entry",
		Items:      items,
		FormError:  formError,
		Reversed:   c.Query("reversed") == "1",
	}).Render(c.Context(), c)
}

func (s *Server) handleJournalEntryReverse(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	idRaw := strings.TrimSpace(c.Params("id"))
	idU64, err := services.ParseUint(idRaw)
	if err != nil || idU64 == 0 {
		return c.Redirect("/journal-entry/list", fiber.StatusSeeOther)
	}

	reverseDate := time.Now()
	reverseDateRaw := strings.TrimSpace(c.FormValue("reverse_date"))
	if reverseDateRaw != "" {
		if d, err := time.Parse("2006-01-02", reverseDateRaw); err == nil {
			reverseDate = d
		}
	}

	var reversedID uint
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		newID, err := services.ReverseJournalEntry(tx, companyID, uint(idU64), reverseDate)
		if err != nil {
			return err
		}
		reversedID = newID
		return nil
	}); err != nil {
		switch {
		case errors.Is(err, services.ErrJournalEntryAlreadyReversed):
			return c.Redirect("/journal-entry/list?error=already-reversed", fiber.StatusSeeOther)
		case errors.Is(err, services.ErrJournalEntryLegacyFXUnavailable):
			return c.Redirect("/journal-entry/list?error=legacy-fx-unavailable", fiber.StatusSeeOther)
		default:
			return c.Redirect("/journal-entry/list?error=reverse-failed", fiber.StatusSeeOther)
		}
	}

	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	cid := companyID
	uid := user.ID
	services.TryWriteAuditLogWithContext(s.DB, "journal.reversed", "journal_entry", reversedID, actor, map[string]any{
		"original_id":  idU64,
		"reverse_date": reverseDate.Format("2006-01-02"),
		"company_id":   companyID,
	}, &cid, &uid)
	s.ReportCache.InvalidateCompany(companyID)
	// Both the original (now status=reversed) and the new reversal JE
	// need projecting so search reflects the linkage.
	_ = producers.ProjectJournalEntry(c.Context(), s.DB, s.SearchProjector, companyID, uint(idU64))
	_ = producers.ProjectJournalEntry(c.Context(), s.DB, s.SearchProjector, companyID, reversedID)

	return c.Redirect("/journal-entry/list?reversed=1", fiber.StatusSeeOther)
}
