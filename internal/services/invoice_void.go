// 遵循project_guide.md
package services

// invoice_void.go — VoidInvoice: lifecycle transition sent → voided.
//
// Void pipeline (Phase 5 + Phase 6 concurrency controls):
//
//   1. Load invoice with original JournalEntry + Lines
//   2. Validate invoice.status == sent (posted)   [fast pre-flight outside transaction]
//   3. Transaction:
//        a. SELECT FOR UPDATE on invoice row; re-validate status inside lock
//           (prevents concurrent double-void; second caller blocks, sees status='voided')
//        b. INSERT journal_entries   — reversal header, status=posted, reversed_from_id=original
//                                      SourceType=reversal, SourceID=inv.ID
//           wrapUniqueViolation converts 23505 → ErrConcurrentPostingConflict
//        c. INSERT journal_lines     — mirrored lines with debit ↔ credit swapped
//        d. UPDATE journal_entries   — original JE status → reversed
//        e. MarkLedgerEntriesReversed — original JE ledger entries → reversed
//        f. ProjectToLedger          — reversal JE lines → new active ledger entries
//        g. UPDATE invoices          — status → voided
//        h. WriteAuditLog
//
// Journal / ledger status synchronisation after this function:
//
//   original JE          status = reversed
//   original ledger rows status = reversed
//   reversal JE          status = posted
//   reversal ledger rows status = active
//   source invoice       status = voided
//
//   All five transitions happen atomically inside a single DB transaction.

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ErrInvoiceNotVoidable is returned when voiding is attempted on an invoice
// that has not been posted or is already voided.
var ErrInvoiceNotVoidable = errors.New("only posted invoices can be voided (must be issued or sent)")

// VoidInvoice reverses the accounting for a posted invoice and marks it voided.
// Only invoices with status = "sent" are voidable.
// Paid invoices require the payment to be reversed first.
func VoidInvoice(db *gorm.DB, companyID, invoiceID uint, actor string, userID *uuid.UUID) error {
	// ── 1. Load invoice with original JE ─────────────────────────────────────
	var inv models.Invoice
	if err := db.
		Preload("JournalEntry.Lines").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return fmt.Errorf("load invoice: %w", err)
	}

	// ── 2. Pre-flight checks ──────────────────────────────────────────────────
	// Voidable if posted (issued, sent, partially_paid, overdue) — not draft, not already voided
	if inv.Status == models.InvoiceStatusDraft || inv.Status == models.InvoiceStatusVoided {
		return ErrInvoiceNotVoidable
	}
	if inv.JournalEntryID == nil || inv.JournalEntry == nil {
		return errors.New("invoice has no linked journal entry — cannot void")
	}
	origJE := inv.JournalEntry
	if len(origJE.Lines) == 0 {
		return errors.New("original journal entry has no lines")
	}

	// ── 3. Transaction ────────────────────────────────────────────────────────
	return db.Transaction(func(tx *gorm.DB) error {
		// a. Lock invoice row and re-validate status inside the lock.
		var locked models.Invoice
		if err := applyLockForUpdate(
			tx.Select("id", "company_id", "status").
				Where("id = ? AND company_id = ?", inv.ID, companyID),
		).First(&locked).Error; err != nil {
			return fmt.Errorf("lock invoice: %w", err)
		}
		if locked.Status == models.InvoiceStatusDraft || locked.Status == models.InvoiceStatusVoided {
			return ErrInvoiceNotVoidable
		}

		// b. Reversal JE header — status=posted, linked back to the original.
		//    SourceType=reversal + SourceID=inv.ID pairs with the original posting JE
		//    (SourceType=invoice + SourceID=inv.ID, now status=reversed) without conflict.
		reversalJE := models.JournalEntry{
			CompanyID:      companyID,
			EntryDate:      origJE.EntryDate,
			JournalNo:      "VOID-" + inv.InvoiceNumber,
			ReversedFromID: &origJE.ID,
			Status:         models.JournalEntryStatusPosted,
			SourceType:     models.LedgerSourceReversal,
			SourceID:       inv.ID,
		}
		if err := wrapUniqueViolation(tx.Create(&reversalJE).Error, "create reversal journal entry"); err != nil {
			return fmt.Errorf("create reversal journal entry: %w", err)
		}

		// c. Reversal lines — debit/credit swapped from original.
		//    Collect created rows for the ledger projection step.
		createdRevLines := make([]models.JournalLine, 0, len(origJE.Lines))
		for _, l := range origJE.Lines {
			line := models.JournalLine{
				CompanyID:      companyID,
				JournalEntryID: reversalJE.ID,
				AccountID:      l.AccountID,
				Debit:          l.Credit, // swap
				Credit:         l.Debit,  // swap
				Memo:           "VOID: " + l.Memo,
				PartyType:      l.PartyType,
				PartyID:        l.PartyID,
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create reversal line: %w", err)
			}
			createdRevLines = append(createdRevLines, line)
		}

		// d. Mark original JE as reversed.
		//    Targeted UPDATE guards against overwriting other fields under concurrency.
		if err := tx.Model(&models.JournalEntry{}).
			Where("id = ? AND company_id = ?", origJE.ID, companyID).
			Update("status", models.JournalEntryStatusReversed).Error; err != nil {
			return fmt.Errorf("mark original journal entry reversed: %w", err)
		}

		// e. Mark original JE's ledger entries as reversed.
		//    No-op for JEs created before Phase 4 (ledger_entries didn't exist yet);
		//    correct and necessary for all JEs created from Phase 4 onward.
		if err := MarkLedgerEntriesReversed(tx, companyID, origJE.ID); err != nil {
			return fmt.Errorf("mark ledger entries reversed: %w", err)
		}

		// f. Project reversal JE to ledger — new active entries for the reversal,
		//    so the full picture (original debit + reversal credit, etc.) is in
		//    ledger_entries for reporting.
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: reversalJE,
			Lines:        createdRevLines,
			SourceType:   models.LedgerSourceReversal,
			SourceID:     inv.ID,
		}); err != nil {
			return fmt.Errorf("project reversal to ledger: %w", err)
		}

		// g. Mark invoice voided.
		if err := tx.Model(&inv).Updates(map[string]any{
			"status": string(models.InvoiceStatusVoided),
		}).Error; err != nil {
			return fmt.Errorf("update invoice status: %w", err)
		}

		// h. Audit log.
		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "invoice.voided", "invoice", inv.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID, nil,
			map[string]any{
				"invoice_number":    inv.InvoiceNumber,
				"reversal_entry_id": reversalJE.ID,
				"total":             inv.Amount.StringFixed(2),
			},
		)
	})
}
