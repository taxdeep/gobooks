package services

import (
	"testing"

	"balanciz/internal/models"
)

func TestValidateInvoiceDeletable_draftOK(t *testing.T) {
	inv := models.Invoice{Status: models.InvoiceStatusDraft}
	if err := ValidateInvoiceDeletable(inv); err != nil {
		t.Fatal(err)
	}
}

func TestValidateInvoiceDeletable_sentBlocked(t *testing.T) {
	inv := models.Invoice{Status: models.InvoiceStatusSent}
	if err := ValidateInvoiceDeletable(inv); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateBillDeletable_draftOK(t *testing.T) {
	b := models.Bill{Status: models.BillStatusDraft}
	if err := ValidateBillDeletable(b); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBillDeletable_postedBlocked(t *testing.T) {
	b := models.Bill{Status: models.BillStatusPosted}
	if err := ValidateBillDeletable(b); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateInvoiceDeletable_draftWithJournalEntryBlocked(t *testing.T) {
	jeID := uint(123)
	inv := models.Invoice{Status: models.InvoiceStatusDraft, JournalEntryID: &jeID}
	if err := ValidateInvoiceDeletable(inv); err == nil {
		t.Fatal("expected error: draft invoice with linked journal entry must not be deletable")
	}
}

func TestValidateBillDeletable_draftWithJournalEntryBlocked(t *testing.T) {
	jeID := uint(456)
	b := models.Bill{Status: models.BillStatusDraft, JournalEntryID: &jeID}
	if err := ValidateBillDeletable(b); err == nil {
		t.Fatal("expected error: draft bill with linked journal entry must not be deletable")
	}
}
