// 遵循project_guide.md
package services

import (
	"fmt"
	"time"

	"balanciz/internal/models"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// JournalEntryReportLine is one debit/credit row in the journal entry report.
type JournalEntryReportLine struct {
	AccountCode string
	AccountName string
	Memo        string
	Debit       decimal.Decimal
	Credit      decimal.Decimal
}

// JournalEntryReportEntry is one journal with its lines for reporting.
type JournalEntryReportEntry struct {
	ID           uint
	EntryDate    string
	JournalNo    string
	ReversalNote string
	Lines        []JournalEntryReportLine
}

// JournalEntryReport lists journal entries in a date range (inclusive on entry_date),
// ordered by date then id, with lines and accounts loaded.
func JournalEntryReport(db *gorm.DB, companyID uint, fromDate, toDate time.Time) ([]JournalEntryReportEntry, error) {
	toExclusive := toDate.AddDate(0, 0, 1)

	var entries []models.JournalEntry
	err := db.Where("company_id = ? AND entry_date >= ? AND entry_date < ?", companyID, fromDate, toExclusive).
		Preload("Lines", func(tx *gorm.DB) *gorm.DB {
			return tx.Order("journal_lines.id ASC")
		}).
		Preload("Lines.Account").
		Order("entry_date ASC, id ASC").
		Find(&entries).Error
	if err != nil {
		return nil, err
	}

	out := make([]JournalEntryReportEntry, 0, len(entries))
	for _, e := range entries {
		item := JournalEntryReportEntry{
			ID:        e.ID,
			EntryDate: e.EntryDate.Format("2006-01-02"),
			JournalNo: e.JournalNo,
			Lines:     make([]JournalEntryReportLine, 0, len(e.Lines)),
		}
		if e.ReversedFromID != nil {
			item.ReversalNote = fmt.Sprintf("Reversal of entry #%d", *e.ReversedFromID)
		}
		for _, l := range e.Lines {
			code := ""
			name := ""
			if l.AccountID != 0 {
				code = l.Account.Code
				name = l.Account.Name
			}
			item.Lines = append(item.Lines, JournalEntryReportLine{
				AccountCode: code,
				AccountName: name,
				Memo:        l.Memo,
				Debit:       l.Debit,
				Credit:      l.Credit,
			})
		}
		out = append(out, item)
	}
	return out, nil
}
