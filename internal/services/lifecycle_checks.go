// 遵循project_guide.md
package services

// lifecycle_checks.go - consistency checkers between business documents and
// their associated journal entries / ledger entries.

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"gobooks/internal/models"
)

var ErrDraftWithActiveJournal = errors.New("draft document has a posted journal entry - expected no active JE")
var ErrPostedWithoutJournal = errors.New("posted document has no linked journal entry")
var ErrPostedWithUnpostedJournal = errors.New("posted document's journal entry is not in posted status")
var ErrVoidedWithActiveEntries = errors.New("voided document's journal entry still has active ledger entries")
var ErrVoidedWithoutReversal = errors.New("voided document has no posted reversal journal entry")

func CheckInvoiceConsistency(db *gorm.DB, companyID, invoiceID uint) error {
	var inv models.Invoice
	if err := db.
		Preload("JournalEntry").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return fmt.Errorf("load invoice: %w", err)
	}

	switch inv.Status {
	case models.InvoiceStatusDraft:
		if inv.JournalEntry != nil && inv.JournalEntry.Status == models.JournalEntryStatusPosted {
			return ErrDraftWithActiveJournal
		}
	case models.InvoiceStatusSent, models.InvoiceStatusPaid:
		if inv.JournalEntryID == nil || inv.JournalEntry == nil {
			return ErrPostedWithoutJournal
		}
		if inv.JournalEntry.Status != models.JournalEntryStatusPosted {
			return ErrPostedWithUnpostedJournal
		}
	case models.InvoiceStatusVoided:
		return checkVoidedDocumentJournal(db, companyID, inv.JournalEntryID, inv.JournalEntry, "invoice")
	}

	return nil
}

func CheckBillConsistency(db *gorm.DB, companyID, billID uint) error {
	var bill models.Bill
	if err := db.
		Preload("JournalEntry").
		Where("id = ? AND company_id = ?", billID, companyID).
		First(&bill).Error; err != nil {
		return fmt.Errorf("load bill: %w", err)
	}

	switch bill.Status {
	case models.BillStatusDraft:
		if bill.JournalEntry != nil && bill.JournalEntry.Status == models.JournalEntryStatusPosted {
			return ErrDraftWithActiveJournal
		}
	case models.BillStatusPosted, models.BillStatusPaid:
		if bill.JournalEntryID == nil || bill.JournalEntry == nil {
			return ErrPostedWithoutJournal
		}
		if bill.JournalEntry.Status != models.JournalEntryStatusPosted {
			return ErrPostedWithUnpostedJournal
		}
	case models.BillStatusVoided:
		return checkVoidedDocumentJournal(db, companyID, bill.JournalEntryID, bill.JournalEntry, "bill")
	}

	return nil
}

func checkVoidedDocumentJournal(db *gorm.DB, companyID uint, journalEntryID *uint, journalEntry *models.JournalEntry, label string) error {
	if journalEntryID == nil || journalEntry == nil {
		return ErrPostedWithoutJournal
	}

	hasReversal, err := hasPostedReversal(db, companyID, journalEntry.ID)
	if err != nil {
		return fmt.Errorf("check posted reversal: %w", err)
	}
	if !hasReversal {
		return ErrVoidedWithoutReversal
	}

	switch journalEntry.Status {
	case models.JournalEntryStatusPosted:
		return nil
	case models.JournalEntryStatusReversed:
		activeCount, err := countActiveLedgerEntries(db, companyID, journalEntry.ID)
		if err != nil {
			return fmt.Errorf("check active ledger entries: %w", err)
		}
		if activeCount > 0 {
			return ErrVoidedWithActiveEntries
		}
		return nil
	default:
		return fmt.Errorf("voided %s's journal entry has status %q - expected 'posted'", label, journalEntry.Status)
	}
}

func countActiveLedgerEntries(db *gorm.DB, companyID, journalEntryID uint) (int64, error) {
	var count int64
	err := db.Model(&models.LedgerEntry{}).
		Where("company_id = ? AND journal_entry_id = ? AND status = ?",
			companyID, journalEntryID, models.LedgerEntryStatusActive).
		Count(&count).Error
	return count, err
}

func hasPostedReversal(db *gorm.DB, companyID, journalEntryID uint) (bool, error) {
	var count int64
	err := db.Model(&models.JournalEntry{}).
		Where("company_id = ? AND reversed_from_id = ? AND status = ? AND source_type = ?",
			companyID, journalEntryID, models.JournalEntryStatusPosted, models.LedgerSourceReversal).
		Count(&count).Error
	return count > 0, err
}
