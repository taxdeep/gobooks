// 遵循project_guide.md
package services

// revaluation.go — RunRevaluation: period-end unrealized FX revaluation.
//
// At period end, open foreign-currency invoices and bills are revalued to the
// current exchange rate. The difference between the original carrying value
// (BalanceDueBase) and the revalued amount is posted as an unrealized FX
// gain/loss journal entry. An auto-reversal JE is created simultaneously
// with ReversalDate as its entry date, so the adjustment is automatically
// unwound at the start of the next period.
//
// Important: revaluation does NOT update BalanceDue or BalanceDueBase on the
// source documents. Carrying values stay at original posting rates for
// settlement purposes. The revaluation JE is the only record of the unrealized
// adjustment.
//
// Journal entry shape — example: open USD 1 000 invoice posted at 1.37 (base 1 370)
// revalued at period-end rate 1.40:
//
//   Revaluation JE (date = RunDate):
//     DR  1100 Accounts Receivable    30.00   (newBase 1 400 − oldBase 1 370)
//     CR  4900 Unrealized FX Gain     30.00
//
//   Reversal JE (date = ReversalDate):
//     DR  4900 Unrealized FX Gain     30.00   (auto-reverse)
//     CR  1100 Accounts Receivable    30.00
//
// For a USD bill posted at 1.37 revalued at 1.42 (liability increased → loss):
//
//   Revaluation JE:
//     DR  4900 Unrealized FX Loss     50.00
//     CR  2000 Accounts Payable       50.00
//
//   Reversal JE:
//     DR  2000 Accounts Payable       50.00
//     CR  4900 Unrealized FX Loss     50.00
//
// Account resolution — AR/AP accounts:
//   Foreign invoices: system_key "ar_{code}" if present, else first active AR account.
//   Foreign bills:    system_key "ap_{code}" if present, else first active AP account.
//   This mirrors the account resolution used at posting time (invoice_post.go / bill_post.go).
//
// Sign convention for Adjustment = NewBase − OldBase:
//   Positive:  rate rose  → AR worth more (unrealized gain) / AP costs more (unrealized loss).
//   Negative:  rate fell  → AR worth less (unrealized loss) / AP costs less (unrealized gain).

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// RunRevaluationInput carries the parameters for a period-end revaluation run.
type RunRevaluationInput struct {
	CompanyID    uint
	// RunDate is the period-end date and the entry date of the revaluation JE.
	RunDate      time.Time
	// ReversalDate is the first day of the next period.
	// The auto-reversal JE is dated here so it unwinds at the start of the period.
	ReversalDate time.Time
	Actor        string
	UserID       *uuid.UUID
}

// RunRevaluation performs a period-end unrealized FX revaluation.
//
// It finds all open foreign-currency invoices and bills, computes the
// unrealized FX adjustment at the RunDate rate, and creates a paired
// revaluation JE + auto-reversal JE in a single transaction.
//
// Returns the new RevaluationRun.ID, or 0 if there are no open foreign-currency
// documents to revalue (no run is created). Returns an error on failure.
func RunRevaluation(db *gorm.DB, in RunRevaluationInput) (uint, error) {
	if in.CompanyID == 0 {
		return 0, errors.New("company is required")
	}
	if in.RunDate.IsZero() {
		return 0, errors.New("run date is required")
	}
	if in.ReversalDate.IsZero() {
		return 0, errors.New("reversal date is required")
	}
	if !in.ReversalDate.After(in.RunDate) {
		return 0, errors.New("reversal date must be after run date")
	}

	// ── 1. Load company base currency ────────────────────────────────────────
	var company models.Company
	if err := db.Select("id", "base_currency_code").First(&company, in.CompanyID).Error; err != nil {
		return 0, fmt.Errorf("load company: %w", err)
	}
	base := company.BaseCurrencyCode

	// ── 2. Collect open foreign invoices ─────────────────────────────────────
	openInvStatuses := []string{
		string(models.InvoiceStatusIssued),
		string(models.InvoiceStatusSent),
		string(models.InvoiceStatusPartiallyPaid),
		string(models.InvoiceStatusOverdue),
	}
	var invoices []models.Invoice
	if err := db.
		Where("company_id = ? AND status IN ? AND currency_code != '' AND currency_code != ? AND balance_due > 0",
			in.CompanyID, openInvStatuses, base).
		Find(&invoices).Error; err != nil {
		return 0, fmt.Errorf("load open invoices: %w", err)
	}

	// ── 3. Collect open foreign bills ─────────────────────────────────────────
	openBillStatuses := []string{
		string(models.BillStatusPosted),
		string(models.BillStatusPartiallyPaid),
	}
	var bills []models.Bill
	if err := db.
		Where("company_id = ? AND status IN ? AND currency_code != '' AND currency_code != ? AND balance_due > 0",
			in.CompanyID, openBillStatuses, base).
		Find(&bills).Error; err != nil {
		return 0, fmt.Errorf("load open bills: %w", err)
	}

	if len(invoices) == 0 && len(bills) == 0 {
		return 0, nil // nothing to revalue
	}

	// ── 4. Build revaluation line candidates ──────────────────────────────────
	type lineCandidate struct {
		line models.RevaluationLine
		isAR bool // true = invoice (AR side), false = bill (AP side)
	}
	candidates := make([]lineCandidate, 0, len(invoices)+len(bills))

	for _, inv := range invoices {
		rate, err := GetExchangeRate(db, &in.CompanyID, inv.CurrencyCode, base, in.RunDate)
		if err != nil {
			return 0, fmt.Errorf("invoice %s: exchange rate %s→%s not found for %s: %w",
				inv.InvoiceNumber, inv.CurrencyCode, base, in.RunDate.Format("2006-01-02"), err)
		}
		arAccID, err := resolveARAccount(db, in.CompanyID, inv.CurrencyCode)
		if err != nil {
			return 0, fmt.Errorf("invoice %s: %w", inv.InvoiceNumber, err)
		}
		effBal, effBase := effectiveBalances(inv.BalanceDue, inv.BalanceDueBase, inv.Amount, inv.AmountBase, true)
		newBase := effBal.Mul(rate).Round(2)
		adj := newBase.Sub(effBase)
		if adj.IsZero() {
			continue
		}
		candidates = append(candidates, lineCandidate{
			line: models.RevaluationLine{
				CompanyID:       in.CompanyID,
				DocumentType:    "invoice",
				DocumentID:      inv.ID,
				AccountID:       arAccID,
				CurrencyCode:    inv.CurrencyCode,
				BalanceDue:      effBal,
				RevaluationRate: rate,
				OldBase:         effBase,
				NewBase:         newBase,
				Adjustment:      adj,
			},
			isAR: true,
		})
	}

	for _, bill := range bills {
		rate, err := GetExchangeRate(db, &in.CompanyID, bill.CurrencyCode, base, in.RunDate)
		if err != nil {
			return 0, fmt.Errorf("bill %s: exchange rate %s→%s not found for %s: %w",
				bill.BillNumber, bill.CurrencyCode, base, in.RunDate.Format("2006-01-02"), err)
		}
		apAccID, err := resolveAPAccount(db, in.CompanyID, bill.CurrencyCode)
		if err != nil {
			return 0, fmt.Errorf("bill %s: %w", bill.BillNumber, err)
		}
		effBal, effBase := effectiveBalances(bill.BalanceDue, bill.BalanceDueBase, bill.Amount, bill.AmountBase, true)
		newBase := effBal.Mul(rate).Round(2)
		adj := newBase.Sub(effBase)
		if adj.IsZero() {
			continue
		}
		candidates = append(candidates, lineCandidate{
			line: models.RevaluationLine{
				CompanyID:       in.CompanyID,
				DocumentType:    "bill",
				DocumentID:      bill.ID,
				AccountID:       apAccID,
				CurrencyCode:    bill.CurrencyCode,
				BalanceDue:      effBal,
				RevaluationRate: rate,
				OldBase:         effBase,
				NewBase:         newBase,
				Adjustment:      adj,
			},
			isAR: false,
		})
	}

	if len(candidates) == 0 {
		return 0, nil // all adjustments are zero; skip
	}

	// ── 5. Ensure unrealized FX account ──────────────────────────────────────
	fxAccID, err := EnsureUnrealizedFXAccount(db, in.CompanyID)
	if err != nil {
		return 0, err
	}

	// ── 6. Build posting fragments ────────────────────────────────────────────
	//
	// Sign convention (Adjustment = NewBase − OldBase):
	//   Invoice (AR), adj > 0:  rate rose → AR worth more → unrealized gain
	//     DR AR account, CR Unrealized FX
	//   Invoice (AR), adj < 0:  rate fell → AR worth less → unrealized loss
	//     DR Unrealized FX, CR AR account
	//
	//   Bill (AP), adj > 0:  rate rose → AP costs more → unrealized loss
	//     DR Unrealized FX, CR AP account
	//   Bill (AP), adj < 0:  rate fell → AP costs less → unrealized gain
	//     DR AP account, CR Unrealized FX
	frags := make([]PostingFragment, 0, len(candidates)*2)
	for _, c := range candidates {
		absAdj := c.line.Adjustment.Abs()
		if c.isAR {
			if c.line.Adjustment.IsPositive() {
				frags = append(frags,
					PostingFragment{AccountID: c.line.AccountID, Debit: absAdj, Memo: "Revaluation"},
					PostingFragment{AccountID: fxAccID, Credit: absAdj, Memo: "Revaluation"},
				)
			} else {
				frags = append(frags,
					PostingFragment{AccountID: fxAccID, Debit: absAdj, Memo: "Revaluation"},
					PostingFragment{AccountID: c.line.AccountID, Credit: absAdj, Memo: "Revaluation"},
				)
			}
		} else {
			if c.line.Adjustment.IsPositive() {
				frags = append(frags,
					PostingFragment{AccountID: fxAccID, Debit: absAdj, Memo: "Revaluation"},
					PostingFragment{AccountID: c.line.AccountID, Credit: absAdj, Memo: "Revaluation"},
				)
			} else {
				frags = append(frags,
					PostingFragment{AccountID: c.line.AccountID, Debit: absAdj, Memo: "Revaluation"},
					PostingFragment{AccountID: fxAccID, Credit: absAdj, Memo: "Revaluation"},
				)
			}
		}
	}

	jeLines, err := AggregateJournalLines(frags)
	if err != nil {
		return 0, fmt.Errorf("aggregate revaluation lines: %w", err)
	}

	// Reversal lines are the mirror image: DR↔CR swapped on every line.
	reversalLines := negateFragments(jeLines)

	// ── 7. Transaction ────────────────────────────────────────────────────────
	var runID uint
	txErr := db.Transaction(func(tx *gorm.DB) error {
		// a. Create revaluation run (JE IDs filled in below).
		run := models.RevaluationRun{
			CompanyID:    in.CompanyID,
			RunDate:      in.RunDate,
			ReversalDate: in.ReversalDate,
			Status:       models.RevaluationRunStatusPosted,
		}
		if err := tx.Create(&run).Error; err != nil {
			return fmt.Errorf("create revaluation run: %w", err)
		}
		runID = run.ID

		// b. Create revaluation lines.
		for i := range candidates {
			candidates[i].line.RevaluationRunID = runID
			if err := tx.Create(&candidates[i].line).Error; err != nil {
				return fmt.Errorf("create revaluation line: %w", err)
			}
		}

		// c. Create revaluation JE.
		je := models.JournalEntry{
			CompanyID:  in.CompanyID,
			EntryDate:  in.RunDate,
			JournalNo:  fmt.Sprintf("REVAL-%d", runID),
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceRevaluation,
			SourceID:   runID,
		}
		if err := tx.Create(&je).Error; err != nil {
			return fmt.Errorf("create revaluation journal entry: %w", err)
		}

		createdLines := make([]models.JournalLine, 0, len(jeLines))
		for _, jl := range jeLines {
			line := models.JournalLine{
				CompanyID:      in.CompanyID,
				JournalEntryID: je.ID,
				AccountID:      jl.AccountID,
				Debit:          jl.Debit,
				Credit:         jl.Credit,
				Memo:           jl.Memo,
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create revaluation journal line: %w", err)
			}
			createdLines = append(createdLines, line)
		}

		if err := ProjectToLedger(tx, in.CompanyID, LedgerPostInput{
			JournalEntry: je,
			Lines:        createdLines,
			SourceType:   models.LedgerSourceRevaluation,
			SourceID:     runID,
		}); err != nil {
			return fmt.Errorf("project revaluation to ledger: %w", err)
		}

		// d. Create auto-reversal JE (entry date = ReversalDate).
		//    SourceType = reversal; SourceID = original JE ID so it's traceable.
		reversalJE := models.JournalEntry{
			CompanyID:  in.CompanyID,
			EntryDate:  in.ReversalDate,
			JournalNo:  fmt.Sprintf("REVAL-REV-%d", runID),
			Status:     models.JournalEntryStatusPosted,
			SourceType: models.LedgerSourceReversal,
			SourceID:   je.ID,
		}
		if err := tx.Create(&reversalJE).Error; err != nil {
			return fmt.Errorf("create reversal journal entry: %w", err)
		}

		createdReversalLines := make([]models.JournalLine, 0, len(reversalLines))
		for _, jl := range reversalLines {
			line := models.JournalLine{
				CompanyID:      in.CompanyID,
				JournalEntryID: reversalJE.ID,
				AccountID:      jl.AccountID,
				Debit:          jl.Debit,
				Credit:         jl.Credit,
				Memo:           jl.Memo,
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create reversal journal line: %w", err)
			}
			createdReversalLines = append(createdReversalLines, line)
		}

		if err := ProjectToLedger(tx, in.CompanyID, LedgerPostInput{
			JournalEntry: reversalJE,
			Lines:        createdReversalLines,
			SourceType:   models.LedgerSourceReversal,
			SourceID:     je.ID,
		}); err != nil {
			return fmt.Errorf("project reversal to ledger: %w", err)
		}

		// e. Update run with JE IDs.
		reversalJEID := reversalJE.ID
		if err := tx.Model(&run).Updates(map[string]any{
			"journal_entry_id": je.ID,
			"reversal_je_id":   reversalJEID,
		}).Error; err != nil {
			return fmt.Errorf("update revaluation run: %w", err)
		}

		// f. Audit log.
		cid := in.CompanyID
		return WriteAuditLogWithContextDetails(tx, "revaluation.posted", "revaluation_run", runID, in.Actor,
			map[string]any{"company_id": in.CompanyID},
			&cid, in.UserID, nil,
			map[string]any{
				"run_date":         in.RunDate.Format("2006-01-02"),
				"reversal_date":    in.ReversalDate.Format("2006-01-02"),
				"journal_entry_id": je.ID,
				"reversal_je_id":   reversalJE.ID,
				"lines_count":      len(candidates),
			},
		)
	})
	if txErr != nil {
		return 0, txErr
	}
	return runID, nil
}

// EnsureUnrealizedFXAccount returns the ID of the company's unrealized FX
// gain/loss account, creating it if necessary.
//
// The account is:
//   - RootRevenue / DetailOtherIncome
//   - IsSystemGenerated = true
//   - SystemKey = "fx_unrealized_gain_loss"
//   - Name = "Unrealized FX Gain/Loss"
//
// Debiting records an unrealized FX loss; crediting records an unrealized gain.
func EnsureUnrealizedFXAccount(db *gorm.DB, companyID uint) (uint, error) {
	sysKey := "fx_unrealized_gain_loss"

	var acc models.Account
	err := db.Where("company_id = ? AND system_key = ?", companyID, sysKey).First(&acc).Error
	if err == nil {
		return acc.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("lookup unrealized FX account: %w", err)
	}

	var company models.Company
	if err := db.Select("id", "account_code_length").First(&company, companyID).Error; err != nil {
		return 0, fmt.Errorf("load company for unrealized FX account: %w", err)
	}
	codeLen := company.AccountCodeLength
	if codeLen < models.AccountCodeLengthMin || codeLen > models.AccountCodeLengthMax {
		codeLen = models.AccountCodeLengthMin
	}

	accountCode, err := findNextAccountCode(db, companyID, codeLen,
		models.RootRevenue, models.DetailOtherIncome)
	if err != nil {
		return 0, fmt.Errorf("find code for unrealized FX account: %w", err)
	}

	sk := sysKey
	acc = models.Account{
		CompanyID:         companyID,
		Code:              accountCode,
		Name:              "Unrealized FX Gain/Loss",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailOtherIncome,
		IsActive:          true,
		IsSystemGenerated: true,
		SystemKey:         &sk,
	}
	if err := db.Create(&acc).Error; err != nil {
		return 0, fmt.Errorf("create unrealized FX account: %w", err)
	}
	return acc.ID, nil
}

// resolveARAccount finds the AR account for a foreign invoice.
//
// Lookup order:
//  1. system_key = "ar_{currencyCode}" (currency-specific system account)
//  2. First active AR account ordered by code (fallback)
//
// This mirrors the resolution used in invoice_post.go at posting time.
func resolveARAccount(db *gorm.DB, companyID uint, currencyCode string) (uint, error) {
	var acc models.Account
	sysKey := "ar_" + currencyCode
	err := db.Where("company_id = ? AND system_key = ? AND is_active = true", companyID, sysKey).
		First(&acc).Error
	if err == nil {
		return acc.ID, nil
	}
	if err := db.
		Where("company_id = ? AND detail_account_type = ? AND is_active = true",
			companyID, string(models.DetailAccountsReceivable)).
		Order("code asc").First(&acc).Error; err != nil {
		return 0, fmt.Errorf("no active AR account found for company %d", companyID)
	}
	return acc.ID, nil
}

// resolveAPAccount finds the AP account for a foreign bill.
//
// Lookup order:
//  1. system_key = "ap_{currencyCode}" (currency-specific system account)
//  2. First active AP account ordered by code (fallback)
//
// This mirrors the resolution used in bill_post.go at posting time.
func resolveAPAccount(db *gorm.DB, companyID uint, currencyCode string) (uint, error) {
	var acc models.Account
	sysKey := "ap_" + currencyCode
	err := db.Where("company_id = ? AND system_key = ? AND is_active = true", companyID, sysKey).
		First(&acc).Error
	if err == nil {
		return acc.ID, nil
	}
	if err := db.
		Where("company_id = ? AND detail_account_type = ? AND is_active = true",
			companyID, string(models.DetailAccountsPayable)).
		Order("code asc").First(&acc).Error; err != nil {
		return 0, fmt.Errorf("no active AP account found for company %d", companyID)
	}
	return acc.ID, nil
}

// negateFragments swaps Debit↔Credit on every PostingFragment, producing the
// mirror image needed for auto-reversal journal entries.
func negateFragments(lines []PostingFragment) []PostingFragment {
	out := make([]PostingFragment, len(lines))
	for i, f := range lines {
		out[i] = PostingFragment{
			AccountID: f.AccountID,
			Debit:     f.Credit,
			Credit:    f.Debit,
			Memo:      f.Memo,
		}
	}
	return out
}
