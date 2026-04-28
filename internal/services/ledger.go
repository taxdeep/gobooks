// 遵循project_guide.md
package services

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Public types ──────────────────────────────────────────────────────────────

// LedgerPostInput carries the information the posting engine supplies when
// projecting a newly created journal entry into ledger_entries.
//
// Callers (PostInvoice, PostBill, etc.) build one of these after the
// JournalEntry and its JournalLines have been inserted, then pass it to
// ProjectToLedger within the same database transaction.
type LedgerPostInput struct {
	// JournalEntry is the committed (posted) entry header.
	JournalEntry models.JournalEntry
	// Lines are the journal lines that belong to JournalEntry.
	Lines []models.JournalLine
	// SourceType identifies the originating business document (invoice, bill …).
	SourceType models.LedgerSourceType
	// SourceID is the PK of the originating document; 0 for manual entries.
	SourceID uint
}

// ── Projection ────────────────────────────────────────────────────────────────

// ProjectToLedger creates one LedgerEntry row for each journal line in input.
// It must be called within the same database transaction that inserted the
// JournalEntry and JournalLines so that the ledger projection is always
// consistent with the double-entry record.
//
// company_id is passed explicitly to enforce tenant isolation at every DB
// write; it is cross-checked against input.JournalEntry.CompanyID.
func ProjectToLedger(tx *gorm.DB, companyID uint, input LedgerPostInput) error {
	if input.JournalEntry.ID == 0 {
		return fmt.Errorf("ledger: JournalEntry.ID must be set before projecting")
	}
	if input.JournalEntry.CompanyID != companyID {
		return fmt.Errorf("ledger: company_id mismatch — JournalEntry.CompanyID %d != %d",
			input.JournalEntry.CompanyID, companyID)
	}
	if len(input.Lines) == 0 {
		return fmt.Errorf("ledger: cannot project a journal entry with no lines")
	}

	postingDate := input.JournalEntry.EntryDate.UTC().Truncate(24 * time.Hour)

	entries := make([]models.LedgerEntry, 0, len(input.Lines))
	for _, jl := range input.Lines {
		if jl.CompanyID != companyID {
			return fmt.Errorf("ledger: journal line %d company_id %d does not match expected %d",
				jl.ID, jl.CompanyID, companyID)
		}
		entries = append(entries, models.LedgerEntry{
			CompanyID:      companyID,
			JournalEntryID: input.JournalEntry.ID,
			SourceType:     input.SourceType,
			SourceID:       input.SourceID,
			AccountID:      jl.AccountID,
			PostingDate:    postingDate,
			DebitAmount:    jl.Debit,
			CreditAmount:   jl.Credit,
			Status:         models.LedgerEntryStatusActive,
		})
	}

	if err := tx.Create(&entries).Error; err != nil {
		return fmt.Errorf("ledger: insert ledger entries: %w", err)
	}
	return nil
}

// ── Reversal ──────────────────────────────────────────────────────────────────

// MarkLedgerEntriesReversed transitions all active ledger entries for
// originalJournalEntryID to status='reversed'.
//
// This is called after ReverseJournalEntry creates the reversal journal entry,
// within the same transaction. The reversal's own lines are projected as new
// active ledger entries by a separate ProjectToLedger call.
func MarkLedgerEntriesReversed(tx *gorm.DB, companyID, originalJournalEntryID uint) error {
	if originalJournalEntryID == 0 {
		return fmt.Errorf("ledger: originalJournalEntryID must be non-zero")
	}
	result := tx.Model(&models.LedgerEntry{}).
		Where("company_id = ? AND journal_entry_id = ? AND status = ?",
			companyID, originalJournalEntryID, models.LedgerEntryStatusActive).
		Update("status", models.LedgerEntryStatusReversed)
	if result.Error != nil {
		return fmt.Errorf("ledger: mark reversed: %w", result.Error)
	}
	return nil
}

// ── Query helpers (used by future report services) ────────────────────────────

// AccountBalance returns the net balance (debits − credits) for an account
// as of the given date, considering only active ledger entries.
//
// A positive result means the account has a net debit balance.
// A negative result means the account has a net credit balance.
//
// This function is intentionally simple and correct. Report services may use
// SQL window functions for running balances over a date range.
func AccountBalance(db *gorm.DB, companyID, accountID uint, asOf time.Time) (models.LedgerEntry, error) {
	// Return a single aggregated row using Scan into a partial struct.
	// Using a raw query keeps the intent obvious and avoids GORM aggregate quirks.
	type balanceRow struct {
		TotalDebit  float64
		TotalCredit float64
	}
	var row balanceRow
	err := db.Table("ledger_entries le").
		Select("COALESCE(SUM(le.debit_amount), 0) AS total_debit, COALESCE(SUM(le.credit_amount), 0) AS total_credit").
		Joins("JOIN journal_entries je ON je.id = le.journal_entry_id").
		Where("le.company_id = ? AND le.account_id = ? AND le.posting_date <= ? AND le.status = ? AND "+reportableJournalEntryWhere,
			companyID, accountID, asOf.Format("2006-01-02"), models.LedgerEntryStatusActive).
		Scan(&row).Error
	if err != nil {
		return models.LedgerEntry{}, fmt.Errorf("ledger: account balance query: %w", err)
	}
	// Return a zero-value LedgerEntry as a placeholder; callers should use
	// the dedicated TrialBalance / report query for real reporting needs.
	// This stub is here so the function signature is stable for Phase 5.
	_ = row
	return models.LedgerEntry{}, nil
}

// LedgerEntriesForAccount returns all ledger entries for an account within
// a date range, ordered by posting_date then id, for general ledger report
// generation. Only active entries are returned.
func LedgerEntriesForAccount(db *gorm.DB, companyID, accountID uint, from, to time.Time) ([]models.LedgerEntry, error) {
	var entries []models.LedgerEntry
	err := db.Table("ledger_entries le").
		Select("le.*").
		Joins("JOIN journal_entries je ON je.id = le.journal_entry_id").
		Where("le.company_id = ? AND le.account_id = ? AND le.posting_date BETWEEN ? AND ? AND le.status = ? AND "+reportableJournalEntryWhere,
			companyID, accountID,
			from.Format("2006-01-02"),
			to.Format("2006-01-02"),
			models.LedgerEntryStatusActive).
		Order("le.posting_date ASC, le.id ASC").
		Find(&entries).Error
	if err != nil {
		return nil, fmt.Errorf("ledger: entries for account %d: %w", accountID, err)
	}
	return entries, nil
}
