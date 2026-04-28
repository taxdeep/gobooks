// 遵循project_guide.md
package services

// translation_engine.go — RunTranslation: IAS 21 period-end translation engine.
//
// Overview:
//   Translates a secondary accounting book's accounted amounts (stored in
//   journal_line_book_amounts) from the book's functional currency into a
//   presentation currency for a given reporting period.
//
// IAS 21 rate selection rules applied:
//   Assets + Liabilities            → closing rate (balance sheet date)
//   Revenue + Cost of Sales + Expense → average rate (period approximation)
//   (Equity is excluded from the translated trial balance; it flows through
//    retained earnings and the CTA residual.)
//
// CTA (Cumulative Translation Adjustment):
//   After translating all P&L and B/S items:
//     CTA = sum(translated debits) − sum(translated credits)
//   A non-zero CTA is recognized in OCI via the CTA equity account.
//   Sign convention:
//     CTA > 0 → translation loss  → DR CTA account  (stored as positive)
//     CTA < 0 → translation gain  → CR CTA account  (stored as negative)
//
// Data flow:
//   journal_line_book_amounts (book_id = X, JE.entry_date ∈ [start, end])
//     → group by account_id, sum accounted_debit / accounted_credit
//     → look up account.root_account_type → select rate
//     → multiply by rate, round(2)
//     → compute CTA residual
//     → persist TranslationRun + TranslationLines

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// TranslationInput holds the parameters for a period-end translation run.
type TranslationInput struct {
	CompanyID uint
	// BookID is the secondary AccountingBook whose amounts are being translated.
	BookID uint

	// PeriodStart and PeriodEnd define the inclusive date range.
	PeriodStart time.Time
	PeriodEnd   time.Time

	// PresentationCurrency is the target (reporting) currency (ISO 4217).
	PresentationCurrency string

	// ClosingRate is the period-end exchange rate:
	//   1 FunctionalCurrency = ClosingRate PresentationCurrency units.
	ClosingRate decimal.Decimal

	// AverageRate is the period average rate used for P&L items.
	AverageRate decimal.Decimal

	// Actor and UserID are used for the audit log.
	Actor  string
	UserID *uuid.UUID
}

// accountBalance holds aggregated functional-currency amounts for one account.
type accountBalance struct {
	AccountID         uint
	RootAccountType   models.RootAccountType
	FunctionalDebit   decimal.Decimal
	FunctionalCredit  decimal.Decimal
}

// RunTranslation executes a period-end IAS 21 translation run and persists the
// results. Returns the created TranslationRun.
//
// Returns an error when:
//   - BookID is the primary book (primary books are not translated).
//   - PresentationCurrency matches the book's functional currency (no-op).
//   - ClosingRate or AverageRate ≤ 0.
//   - No JournalLineBookAmount rows exist for the period (nothing to translate).
//   - A TranslationRun for the same book+period already exists (must reverse first).
func RunTranslation(db *gorm.DB, in TranslationInput) (*models.TranslationRun, error) {
	// ── 1. Validate input ────────────────────────────────────────────────────
	if in.PresentationCurrency == "" {
		return nil, errors.New("presentation currency is required")
	}
	if !in.ClosingRate.GreaterThan(decimal.Zero) {
		return nil, errors.New("closing rate must be > 0")
	}
	if !in.AverageRate.GreaterThan(decimal.Zero) {
		return nil, errors.New("average rate must be > 0")
	}
	if in.PeriodEnd.Before(in.PeriodStart) {
		return nil, errors.New("period end must be on or after period start")
	}

	// ── 2. Load the secondary book ───────────────────────────────────────────
	var book models.AccountingBook
	if err := db.Where("id = ? AND company_id = ?", in.BookID, in.CompanyID).
		First(&book).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("accounting book %d not found", in.BookID)
		}
		return nil, fmt.Errorf("load accounting book: %w", err)
	}
	if book.BookType == models.AccountingBookTypePrimary {
		return nil, errors.New("primary books are not translated — use a secondary book")
	}
	if book.FunctionalCurrencyCode == in.PresentationCurrency {
		return nil, fmt.Errorf("book functional currency (%s) matches presentation currency — no translation needed",
			book.FunctionalCurrencyCode)
	}

	// ── 3. Duplicate guard ───────────────────────────────────────────────────
	var existing models.TranslationRun
	err := db.Where(
		"company_id = ? AND book_id = ? AND period_start = ? AND period_end = ? AND status = ?",
		in.CompanyID, in.BookID, in.PeriodStart, in.PeriodEnd,
		models.TranslationRunStatusPosted,
	).First(&existing).Error
	if err == nil {
		return nil, fmt.Errorf("a translation run already exists for book %d period %s–%s (run ID %d); reverse it before re-running",
			in.BookID, in.PeriodStart.Format("2006-01-02"), in.PeriodEnd.Format("2006-01-02"), existing.ID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("check existing translation run: %w", err)
	}

	// ── 4. Aggregate JournalLineBookAmount by account ────────────────────────
	// Join through journal_lines → journal_entries to filter by entry_date.
	type rawRow struct {
		AccountID    uint
		TotalDebit   decimal.Decimal
		TotalCredit  decimal.Decimal
	}
	var rows []rawRow
	if err := db.Raw(`
		SELECT jl.account_id,
		       COALESCE(SUM(jlba.accounted_debit), 0)  AS total_debit,
		       COALESCE(SUM(jlba.accounted_credit), 0) AS total_credit
		FROM journal_line_book_amounts jlba
		JOIN journal_lines    jl  ON jl.id  = jlba.journal_line_id
		JOIN journal_entries  je  ON je.id  = jl.journal_entry_id
		WHERE jlba.book_id    = ?
		  AND jlba.company_id = ?
		  AND je.entry_date  >= ?
		  AND je.entry_date  <= ?
		GROUP BY jl.account_id
	`, in.BookID, in.CompanyID, in.PeriodStart, in.PeriodEnd).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("aggregate book amounts: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no journal line book amounts found for book %d in period %s–%s",
			in.BookID, in.PeriodStart.Format("2006-01-02"), in.PeriodEnd.Format("2006-01-02"))
	}

	// ── 5. Load account root types for rate selection ────────────────────────
	accountIDs := make([]uint, len(rows))
	for i, r := range rows {
		accountIDs[i] = r.AccountID
	}
	var accounts []models.Account
	if err := db.Select("id", "root_account_type").
		Where("id IN ? AND company_id = ?", accountIDs, in.CompanyID).
		Find(&accounts).Error; err != nil {
		return nil, fmt.Errorf("load accounts for translation: %w", err)
	}
	rootTypeByID := make(map[uint]models.RootAccountType, len(accounts))
	for _, a := range accounts {
		rootTypeByID[a.ID] = a.RootAccountType
	}

	// ── 6. Translate each account ────────────────────────────────────────────
	var (
		totalTranslatedDebit  = decimal.Zero
		totalTranslatedCredit = decimal.Zero
	)
	translationLines := make([]models.TranslationLine, 0, len(rows))

	for _, r := range rows {
		rootType := rootTypeByID[r.AccountID]
		rate, rateType := rateForAccountType(rootType, in.ClosingRate, in.AverageRate)

		tDebit  := r.TotalDebit.Mul(rate).Round(2)
		tCredit := r.TotalCredit.Mul(rate).Round(2)
		totalTranslatedDebit  = totalTranslatedDebit.Add(tDebit)
		totalTranslatedCredit = totalTranslatedCredit.Add(tCredit)

		translationLines = append(translationLines, models.TranslationLine{
			CompanyID:        in.CompanyID,
			AccountID:        r.AccountID,
			FunctionalDebit:  r.TotalDebit,
			FunctionalCredit: r.TotalCredit,
			RateApplied:      rate,
			RateType:         rateType,
			TranslatedDebit:  tDebit,
			TranslatedCredit: tCredit,
		})
	}

	// ── 7. Compute CTA ───────────────────────────────────────────────────────
	// CTA = sum(translated debits) − sum(translated credits).
	// Positive → DR CTA account (translation loss).
	// Negative → CR CTA account (translation gain).
	ctaAmount := totalTranslatedDebit.Sub(totalTranslatedCredit)

	// ── 8. Ensure CTA account (only needed when CTA ≠ 0) ─────────────────────
	var ctaAccountID *uint
	if !ctaAmount.IsZero() {
		id, err := EnsureCTAAccount(db, in.CompanyID)
		if err != nil {
			return nil, fmt.Errorf("ensure CTA account: %w", err)
		}
		ctaAccountID = &id
	}

	// ── 9. Persist in a transaction ──────────────────────────────────────────
	var run models.TranslationRun
	return &run, db.Transaction(func(tx *gorm.DB) error {
		run = models.TranslationRun{
			CompanyID:            in.CompanyID,
			BookID:               in.BookID,
			PeriodStart:          in.PeriodStart,
			PeriodEnd:            in.PeriodEnd,
			RunDate:              time.Now(),
			FunctionalCurrency:   book.FunctionalCurrencyCode,
			PresentationCurrency: in.PresentationCurrency,
			ClosingRate:          in.ClosingRate,
			AverageRate:          in.AverageRate,
			CTAAmount:            ctaAmount,
			CTAAccountID:         ctaAccountID,
			Status:               models.TranslationRunStatusPosted,
		}
		if err := tx.Create(&run).Error; err != nil {
			return fmt.Errorf("create translation run: %w", err)
		}

		// Attach the run ID to each line and bulk-insert.
		for i := range translationLines {
			translationLines[i].TranslationRunID = run.ID
		}
		if err := tx.Create(&translationLines).Error; err != nil {
			return fmt.Errorf("create translation lines: %w", err)
		}

		// Audit log.
		cid := in.CompanyID
		return WriteAuditLogWithContextDetails(tx,
			"translation.run", "translation_run", run.ID, in.Actor,
			map[string]any{"company_id": in.CompanyID},
			&cid, in.UserID, nil,
			map[string]any{
				"book_id":               in.BookID,
				"functional_currency":   book.FunctionalCurrencyCode,
				"presentation_currency": in.PresentationCurrency,
				"period_start":          in.PeriodStart.Format("2006-01-02"),
				"period_end":            in.PeriodEnd.Format("2006-01-02"),
				"closing_rate":          in.ClosingRate.StringFixed(8),
				"average_rate":          in.AverageRate.StringFixed(8),
				"cta_amount":            ctaAmount.StringFixed(2),
				"line_count":            len(translationLines),
			},
		)
	})
}

// rateForAccountType returns the translation rate and rate-type label for a given
// root account type, following IAS 21 rules:
//
//	Assets + Liabilities → closing rate
//	Revenue + CostOfSales + Expense → average rate
//	Equity → closing rate as a conservative default (equity is typically held at
//	         historical rates, but without equity issuance history we use closing;
//	         the CTA absorbs any difference — Phase 4 will add historical-rate tracking).
func rateForAccountType(
	rootType models.RootAccountType,
	closingRate, averageRate decimal.Decimal,
) (decimal.Decimal, models.TranslationRateType) {
	switch rootType {
	case models.RootRevenue, models.RootCostOfSales, models.RootExpense:
		return averageRate, models.TranslationRateTypeAverage
	default:
		// Assets, liabilities, equity → closing rate.
		return closingRate, models.TranslationRateTypeClosing
	}
}
