// 遵循project_guide.md
package services

// bill_void.go — VoidBill: lifecycle transition posted → voided.
//
// Follows the same reversal pattern as VoidInvoice:
//   1. Load bill with original JournalEntry + Lines
//   2. Validate bill.status == posted
//   3. Transaction:
//        a. Lock bill row; re-validate status
//        b. Create reversal JE (debit ↔ credit swapped)
//        c. Mark original JE as reversed
//        d. Mark original ledger entries as reversed
//        e. Project reversal JE to ledger
//        f. Reverse inventory purchase movements (stock items only)
//        g. Mark bill as voided
//        h. Audit log

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

var ErrBillNotVoidable = errors.New("only posted bills can be voided")

// VoidBill reverses the accounting for a posted bill and marks it voided.
// For inventory items, also reverses the purchase movements (reduces stock).
// Blocked if reversing would cause negative inventory.
func VoidBill(db *gorm.DB, companyID, billID uint, actor string, userID *uuid.UUID) error {
	// ── 1. Load bill with original JE ────────────────────────────────────────
	var bill models.Bill
	if err := db.
		Preload("JournalEntry.Lines").
		Where("id = ? AND company_id = ?", billID, companyID).
		First(&bill).Error; err != nil {
		return fmt.Errorf("load bill: %w", err)
	}

	// ── 2. Pre-flight checks ─────────────────────────────────────────────────
	if bill.Status != models.BillStatusPosted && bill.Status != models.BillStatusPartiallyPaid {
		return ErrBillNotVoidable
	}
	if bill.JournalEntryID == nil || bill.JournalEntry == nil {
		return errors.New("bill has no linked journal entry — cannot void")
	}
	origJE := bill.JournalEntry
	if len(origJE.Lines) == 0 {
		return errors.New("original journal entry has no lines")
	}

	// Block void if any settlement allocation references this bill.
	// RecordPayBills creates SettlementAllocation records when a bill is paid via
	// the Phase-4 allocation path. Voiding without removing the allocation would
	// leave an orphaned AP release — the JE reversal cancels the AP debit but the
	// allocation record still points at a voided bill.
	var allocCount int64
	if err := db.Model(&models.SettlementAllocation{}).
		Where("document_type = ? AND document_id = ? AND company_id = ?",
			models.SettlementDocBill, billID, companyID).
		Count(&allocCount).Error; err != nil {
		return fmt.Errorf("check settlement allocations: %w", err)
	}
	if allocCount > 0 {
		return errors.New("cannot void bill: it has settlement allocations — remove the payment allocation first")
	}

	// ── 3. Transaction ───────────────────────────────────────────────────────
	return db.Transaction(func(tx *gorm.DB) error {
		// a. Lock bill row.
		var locked models.Bill
		if err := applyLockForUpdate(
			tx.Select("id", "company_id", "status").
				Where("id = ? AND company_id = ?", bill.ID, companyID),
		).First(&locked).Error; err != nil {
			return fmt.Errorf("lock bill: %w", err)
		}
		if locked.Status != models.BillStatusPosted && locked.Status != models.BillStatusPartiallyPaid {
			return ErrBillNotVoidable
		}

		// b. Reversal JE header. SourceID is the original JE ID, not the
		//    bill ID, so reversal records cannot collide across source families.
		reversalJE := models.JournalEntry{
			CompanyID:      companyID,
			EntryDate:      origJE.EntryDate,
			JournalNo:      "VOID-" + bill.BillNumber,
			ReversedFromID: &origJE.ID,
			Status:         models.JournalEntryStatusPosted,
			SourceType:     models.LedgerSourceReversal,
			SourceID:       origJE.ID,
		}
		if err := wrapUniqueViolation(tx.Create(&reversalJE).Error, "create reversal journal entry"); err != nil {
			return fmt.Errorf("create reversal journal entry: %w", err)
		}

		// c. Reversal lines — debit/credit swapped.
		createdRevLines := make([]models.JournalLine, 0, len(origJE.Lines))
		for _, l := range origJE.Lines {
			line := models.JournalLine{
				CompanyID:      companyID,
				JournalEntryID: reversalJE.ID,
				AccountID:      l.AccountID,
				Debit:          l.Credit,
				Credit:         l.Debit,
				Memo:           "VOID: " + l.Memo,
				PartyType:      l.PartyType,
				PartyID:        l.PartyID,
			}
			if err := tx.Create(&line).Error; err != nil {
				return fmt.Errorf("create reversal line: %w", err)
			}
			createdRevLines = append(createdRevLines, line)
		}

		// d. Keep the original JE posted and project a posted reversal. Ordinary
		//    reports hide reversal pairs through reportableJournalEntryWhere;
		//    audit views still have both entries and a direct reversed_from_id link.
		if err := ProjectToLedger(tx, companyID, LedgerPostInput{
			JournalEntry: reversalJE,
			Lines:        createdRevLines,
			SourceType:   models.LedgerSourceReversal,
			SourceID:     origJE.ID,
		}); err != nil {
			return fmt.Errorf("project reversal to ledger: %w", err)
		}

		// f. Reverse inventory purchase movements for stock items.
		if err := ReversePurchaseMovements(tx, companyID, bill); err != nil {
			return fmt.Errorf("reverse inventory movements: %w", err)
		}

		// f2. Reverse any AP credit applications against this bill.
		// The VCN's RemainingAmount was reduced when each application was created;
		// voiding the bill restores those balances so credits can be reused.
		var apApps []models.APCreditApplication
		if err := tx.Where("bill_id = ? AND company_id = ?", bill.ID, companyID).
			Find(&apApps).Error; err != nil {
			return fmt.Errorf("load AP credit applications: %w", err)
		}
		for _, app := range apApps {
			var vcn models.VendorCreditNote
			if err := tx.Where("id = ? AND company_id = ?", app.VendorCreditNoteID, companyID).
				First(&vcn).Error; err != nil {
				return fmt.Errorf("load vendor credit note %d: %w", app.VendorCreditNoteID, err)
			}
			newRemaining := vcn.RemainingAmount.Add(app.AmountApplied)
			newApplied := vcn.AppliedAmount.Sub(app.AmountApplied)
			if newApplied.IsNegative() {
				newApplied = decimal.Zero
			}
			newVCNStatus := models.VendorCreditNoteStatusPartiallyApplied
			if newApplied.IsZero() {
				newVCNStatus = models.VendorCreditNoteStatusPosted
			}
			if err := tx.Model(&vcn).Updates(map[string]any{
				"remaining_amount": newRemaining,
				"applied_amount":   newApplied,
				"status":           string(newVCNStatus),
			}).Error; err != nil {
				return fmt.Errorf("restore vendor credit note %d: %w", app.VendorCreditNoteID, err)
			}
			if err := tx.Delete(&app).Error; err != nil {
				return fmt.Errorf("delete AP credit application %d: %w", app.ID, err)
			}
		}

		// g. Mark bill voided and zero out the balance fields.
		// Voided bills owe nothing; keeping non-zero balance_due / balance_due_base
		// would corrupt any code path that reads those fields (reports, recalculation, etc.).
		if err := tx.Model(&bill).Updates(map[string]any{
			"status":           string(models.BillStatusVoided),
			"balance_due":      decimal.Zero,
			"balance_due_base": decimal.Zero,
		}).Error; err != nil {
			return fmt.Errorf("update bill status: %w", err)
		}

		cid := companyID
		if err := WriteAuditLogWithContextDetails(tx, "journal_entry.reversal.created", "journal_entry", reversalJE.ID, actor,
			map[string]any{
				"company_id":          companyID,
				"source_entity_type":  "bill",
				"source_entity_id":    bill.ID,
				"source_document_no":  bill.BillNumber,
				"original_entry_id":   origJE.ID,
				"reversal_entry_id":   reversalJE.ID,
				"reversal_journal_no": reversalJE.JournalNo,
			},
			&cid, userID,
			map[string]any{
				"journal_entry_id": origJE.ID,
				"status":           string(origJE.Status),
				"source_type":      string(origJE.SourceType),
				"source_id":        origJE.SourceID,
			},
			map[string]any{
				"journal_entry_id": reversalJE.ID,
				"status":           string(reversalJE.Status),
				"source_type":      string(reversalJE.SourceType),
				"source_id":        reversalJE.SourceID,
				"reversed_from_id": origJE.ID,
			},
		); err != nil {
			return err
		}

		// h. Audit log.
		return WriteAuditLogWithContextDetails(tx, "bill.voided", "bill", bill.ID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID,
			map[string]any{
				"status":           string(bill.Status),
				"journal_entry_id": origJE.ID,
				"amount":           bill.Amount.StringFixed(2),
			},
			map[string]any{
				"bill_number":         bill.BillNumber,
				"status":              string(models.BillStatusVoided),
				"original_entry_id":   origJE.ID,
				"reversal_entry_id":   reversalJE.ID,
				"reversal_journal_no": reversalJE.JournalNo,
				"total":               bill.Amount.StringFixed(2),
			},
		)
	})
}
