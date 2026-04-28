// 遵循project_guide.md
package services

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// WriteSecondaryBookAmounts computes and persists JournalLineBookAmount rows for
// all active secondary accounting books of the company.
//
// This function must be called inside the same DB transaction as the JE creation,
// after createdLines have been persisted (so their IDs are populated).
//
// Returns nil immediately (no-op) when the company has no secondary books.
// This is the expected production state: all callers add this call unconditionally
// without any performance penalty.
//
// Parameters:
//   - db: must be a transaction (*gorm.DB from a Transaction callback)
//   - companyID: the owning company
//   - createdLines: JE lines already persisted in this transaction (IDs must be set)
//   - txCurrencyCode: ISO 4217 code of the transaction currency (TxDebit/TxCredit denomination)
//   - txDate: used for exchange rate lookup when conversion is required
//   - postingReason: FXPostingReason for any FXSnapshot created (transaction/settlement/remeasurement)
func WriteSecondaryBookAmounts(
	db *gorm.DB,
	companyID uint,
	createdLines []models.JournalLine,
	txCurrencyCode string,
	txDate time.Time,
	postingReason models.FXPostingReason,
) error {
	// ── Step 1: Load all secondary books for this company ────────────────────
	// Filter: book_type != 'primary'. No IsActive field exists yet (Phase 0 omitted it).
	// Treat a missing table as a no-op (test environments, pre-Phase-6 fresh installs).
	var books []models.AccountingBook
	if err := db.
		Where("company_id = ? AND book_type != ?", companyID, models.AccountingBookTypePrimary).
		Find(&books).Error; err != nil {
		if isNoSuchTableError(err) {
			return nil
		}
		return fmt.Errorf("write secondary book amounts: load books: %w", err)
	}
	if len(books) == 0 || len(createdLines) == 0 {
		return nil // no-op: no secondary books or no lines to process
	}

	// ── Steps 2–5: Process each secondary book ───────────────────────────────
	for _, book := range books {
		var (
			rate       decimal.Decimal
			snapshotID *uint
		)

		if book.FunctionalCurrencyCode == txCurrencyCode {
			// Identity: book functional currency matches transaction currency.
			// No rate lookup or snapshot needed; amounts are 1:1.
			rate = decimal.NewFromInt(1)
		} else {
			// Step 2: look up exchange rate txCurrencyCode → book.FunctionalCurrencyCode.
			cid := companyID
			r, err := GetExchangeRate(db, &cid, txCurrencyCode, book.FunctionalCurrencyCode, txDate)
			if err != nil {
				return fmt.Errorf("write secondary book amounts: book %d (%s→%s on %s): %w",
					book.ID, txCurrencyCode, book.FunctionalCurrencyCode,
					txDate.Format("2006-01-02"), err)
			}
			rate = r

			// Step 4: create immutable FXSnapshot for this book/conversion.
			snap, err := CreateFXSnapshot(db, CreateFXSnapshotInput{
				CompanyID:     companyID,
				FromCurrency:  txCurrencyCode,
				ToCurrency:    book.FunctionalCurrencyCode,
				Rate:          rate,
				EffectiveDate: txDate,
				RateType:      models.FXRateTypeSpot,
				QuoteBasis:    models.FXQuoteBasisDirect,
				PostingReason: postingReason,
				RateCategory:  models.FXRateCategoryTransaction,
				Source:        "system_stored",
			})
			if err != nil {
				return fmt.Errorf("write secondary book amounts: book %d: create fx snapshot: %w",
					book.ID, err)
			}
			snapshotID = &snap.ID
		}

		// Step 3 + 5: convert amounts per line and bulk-insert.
		//
		// Each line is converted independently (per-line multiply + Round(2)).
		// Secondary book amounts are inherently approximate due to the book's own
		// exchange rate; sub-cent residuals between debit and credit totals are
		// accepted at this layer. If exact balance is required in the future, an
		// anchor correction pass can be added here.
		rows := make([]models.JournalLineBookAmount, 0, len(createdLines))
		isIdentity := rate.Equal(decimal.NewFromInt(1))

		for _, line := range createdLines {
			var acctDebit, acctCredit decimal.Decimal
			if isIdentity {
				acctDebit = line.TxDebit
				acctCredit = line.TxCredit
			} else {
				acctDebit = line.TxDebit.Mul(rate).Round(2)
				acctCredit = line.TxCredit.Mul(rate).Round(2)
			}
			rows = append(rows, models.JournalLineBookAmount{
				JournalLineID:   line.ID,
				BookID:          book.ID,
				CompanyID:       companyID,
				AccountedDebit:  acctDebit,
				AccountedCredit: acctCredit,
				FXSnapshotID:    snapshotID,
			})
		}

		// Bulk-insert all rows for this book in one statement.
		if err := db.Create(&rows).Error; err != nil {
			return fmt.Errorf("write secondary book amounts: book %d: bulk insert: %w",
				book.ID, err)
		}
	}

	return nil
}
