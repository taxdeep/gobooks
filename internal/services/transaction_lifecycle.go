// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"balanciz/internal/models"
)

// ErrDeletePostedInvoice is returned when a delete is attempted on a non-draft invoice.
var ErrDeletePostedInvoice = errors.New("posted or voided invoices cannot be deleted; void the invoice first if applicable")

// ErrDeletePostedBill is returned when a delete is attempted on a non-draft bill.
var ErrDeletePostedBill = errors.New("posted bills cannot be deleted; reverse or void through the appropriate workflow")

// ValidateInvoiceDeletable ensures draft-only deletion and no linked posted journal entry.
func ValidateInvoiceDeletable(inv models.Invoice) error {
	if inv.Status != models.InvoiceStatusDraft {
		return fmt.Errorf("%w (status=%s)", ErrDeletePostedInvoice, inv.Status)
	}
	if inv.JournalEntryID != nil && *inv.JournalEntryID != 0 {
		return fmt.Errorf("%w (journal_entry_id=%d)", ErrDeletePostedInvoice, *inv.JournalEntryID)
	}
	return nil
}

// ValidateBillDeletable ensures draft-only deletion for bills.
func ValidateBillDeletable(bill models.Bill) error {
	if bill.Status != models.BillStatusDraft {
		return fmt.Errorf("%w (status=%s)", ErrDeletePostedBill, bill.Status)
	}
	if bill.JournalEntryID != nil && *bill.JournalEntryID != 0 {
		return fmt.Errorf("%w (journal_entry_id=%d)", ErrDeletePostedBill, *bill.JournalEntryID)
	}
	return nil
}
