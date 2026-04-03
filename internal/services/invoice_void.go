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
	"github.com/shopspring/decimal"
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

	// Block void if any payment transaction has been applied to this invoice.
	// Applied payment transactions carry an AR credit (Dr GW Clearing, Cr AR) that
	// was posted separately. Voiding the invoice reverses the invoice's own JE but
	// leaves the payment AR credit intact — creating a phantom AR credit. The user
	// must unapply all payment transactions before voiding.
	var appliedTxnCount int64
	if err := db.Model(&models.PaymentTransaction{}).
		Where("applied_invoice_id = ? AND company_id = ?", invoiceID, companyID).
		Count(&appliedTxnCount).Error; err != nil {
		return fmt.Errorf("check applied payment transactions: %w", err)
	}
	if appliedTxnCount > 0 {
		return errors.New("cannot void invoice: it has applied payment transactions — unapply them first")
	}

	// Block void if any settlement allocation references this invoice.
	// Settlement allocations are created by RecordReceivePayment (Phase-4 path) and
	// represent a payment directly linked via the allocation record; voiding without
	// removing the allocation would leave an orphaned AP release entry.
	var allocCount int64
	if err := db.Model(&models.SettlementAllocation{}).
		Where("document_type = ? AND document_id = ? AND company_id = ?",
			models.SettlementDocInvoice, invoiceID, companyID).
		Count(&allocCount).Error; err != nil {
		return fmt.Errorf("check settlement allocations: %w", err)
	}
	if allocCount > 0 {
		return errors.New("cannot void invoice: it has settlement allocations — remove the payment allocation first")
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

		// g. Reverse inventory movements for stock items (same transaction).
		if err := ReverseSaleMovements(tx, companyID, inv, reversalJE.ID); err != nil {
			return fmt.Errorf("reverse inventory movements: %w", err)
		}

		// h. Mark invoice voided and zero out the balance fields.
		// Voided invoices owe nothing; keeping non-zero balance_due / balance_due_base
		// would corrupt any code path that reads those fields (reports, recalculation, etc.).
		if err := tx.Model(&inv).Updates(map[string]any{
			"status":           string(models.InvoiceStatusVoided),
			"balance_due":      decimal.Zero,
			"balance_due_base": decimal.Zero,
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
