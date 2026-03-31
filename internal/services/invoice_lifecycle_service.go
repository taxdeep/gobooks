// 遵循project_guide.md
package services

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/gorm"
)

// IssueInvoice transitions an invoice from draft → issued state.
// This is the single authoritative entry point for issuing:
//  1. Validates the invoice is complete and correct
//  2. Refreshes customer/account snapshots from current data
//  3. Posts to accounting (creates JE + ledger entries)
//  4. Sets status to issued, timestamps, and audit log
//
// After this call, the invoice is immutable (amounts, lines, customer locked).
func IssueInvoice(db *gorm.DB, companyID, invoiceID uint) (*models.Invoice, error) {
	// 1. Load invoice with all preloads needed for validation and posting
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).
		Preload("Customer").
		Preload("Lines.ProductService.RevenueAccount").
		Preload("Lines.TaxCode").
		First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("invoice %d not found in company %d", invoiceID, companyID)
		}
		return nil, fmt.Errorf("invoice lookup failed: %w", err)
	}

	// 2. Must be draft
	if invoice.Status != models.InvoiceStatusDraft {
		return nil, fmt.Errorf("cannot issue invoice with status %s (only draft invoices can be issued)", invoice.Status)
	}

	// 3. Validate invoice completeness
	if err := ValidateInvoiceForIssuing(db, companyID, invoiceID); err != nil {
		return nil, fmt.Errorf("invoice validation failed: %w", err)
	}

	// 4. Validate has content
	if invoice.Subtotal.IsZero() && invoice.Amount.IsZero() {
		return nil, fmt.Errorf("cannot issue invoice with zero amount")
	}

	// 5. Refresh customer snapshots from current customer data
	invoice.CustomerNameSnapshot = invoice.Customer.Name
	invoice.CustomerEmailSnapshot = invoice.Customer.Email
	invoice.CustomerAddressSnapshot = invoice.Customer.Address

	// 6. Capture principal (revenue) account snapshot from first line with a product
	for _, line := range invoice.Lines {
		if line.ProductService != nil && line.ProductService.RevenueAccount.ID != 0 {
			acctID := line.ProductService.RevenueAccountID
			invoice.PrincipalAccountIDSnapshot = &acctID
			invoice.PrincipalAccountNameSnapshot = line.ProductService.RevenueAccount.Name
			invoice.PrincipalAccountCodeSnapshot = line.ProductService.RevenueAccount.Code
			break
		}
	}

	// 7. Set issued timestamp
	now := time.Now()
	invoice.IssuedAt = &now

	// 8. Save snapshots and timestamp before posting
	if err := db.Save(&invoice).Error; err != nil {
		return nil, fmt.Errorf("invoice snapshot update failed: %w", err)
	}

	// 9. Post to accounting (creates JE + ledger entries, sets status to issued)
	if err := PostInvoice(db, companyID, invoiceID, "system", nil); err != nil {
		return nil, fmt.Errorf("posting failed: %w", err)
	}

	// 10. Reload the invoice to get the final state (with JournalEntryID set by PostInvoice)
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&invoice).Error; err != nil {
		return nil, fmt.Errorf("reload after posting failed: %w", err)
	}

	// 11. Audit log
	TryWriteAuditLogWithContext(
		db, "invoice.issued", "Invoice", invoice.ID, "system",
		map[string]any{
			"invoice_number":   invoice.InvoiceNumber,
			"customer_name":    invoice.CustomerNameSnapshot,
			"amount":           invoice.Amount.String(),
			"journal_entry_id": invoice.JournalEntryID,
		},
		&companyID, nil,
	)

	return &invoice, nil
}

// SendInvoice transitions an invoice from issued/sent → sent state.
// Sets SentAt timestamp to record when invoice was marked as sent.
// Typically called after email send succeeds.
//
// If the invoice has not been posted yet (e.g. issued without posting),
// this will auto-post before transitioning to sent.
func SendInvoice(db *gorm.DB, companyID, invoiceID uint) (*models.Invoice, error) {
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("invoice %d not found in company %d", invoiceID, companyID)
		}
		return nil, fmt.Errorf("invoice lookup failed: %w", err)
	}

	if invoice.Status != models.InvoiceStatusIssued && invoice.Status != models.InvoiceStatusSent {
		return nil, fmt.Errorf("cannot send invoice with status %s (only issued or sent invoices can be sent)", invoice.Status)
	}

	// Auto-post if not yet posted (safety net)
	if invoice.JournalEntryID == nil {
		postedInvoice, err := PostInvoiceAndReturn(db, companyID, invoiceID)
		if err != nil {
			return nil, fmt.Errorf("auto-post on send failed: %w", err)
		}
		invoice = *postedInvoice
	}

	now := time.Now()
	invoice.Status = models.InvoiceStatusSent
	if invoice.SentAt == nil {
		invoice.SentAt = &now
	}

	if err := db.Save(&invoice).Error; err != nil {
		return nil, fmt.Errorf("invoice update failed: %w", err)
	}

	TryWriteAuditLogWithContext(
		db, "invoice.sent", "Invoice", invoice.ID, "system",
		map[string]any{
			"invoice_number": invoice.InvoiceNumber,
			"sent_at":        invoice.SentAt,
		},
		&companyID, nil,
	)

	return &invoice, nil
}

// MarkInvoicePaid transitions an invoice to paid state.
// Sets status to paid and clears balance_due.
// Does not create accounting entries (payment JE is a future feature).
func MarkInvoicePaid(db *gorm.DB, companyID, invoiceID uint) (*models.Invoice, error) {
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("invoice %d not found in company %d", invoiceID, companyID)
		}
		return nil, fmt.Errorf("invoice lookup failed: %w", err)
	}

	if !isValidInvoiceTransition(invoice.Status, models.InvoiceStatusPaid) {
		return nil, fmt.Errorf("cannot mark invoice as paid from status %s", invoice.Status)
	}

	invoice.Status = models.InvoiceStatusPaid
	invoice.BalanceDue = decimal.Zero

	if err := db.Save(&invoice).Error; err != nil {
		return nil, fmt.Errorf("invoice update failed: %w", err)
	}

	TryWriteAuditLogWithContext(
		db, "invoice.paid", "Invoice", invoice.ID, "system",
		map[string]any{
			"invoice_number": invoice.InvoiceNumber,
			"amount":         invoice.Amount.String(),
		},
		&companyID, nil,
	)

	return &invoice, nil
}

// DeleteInvoice permanently deletes a draft invoice and its lines.
// Only draft invoices without a journal entry can be deleted.
func DeleteInvoice(db *gorm.DB, companyID, invoiceID uint, actor string, userID *uuid.UUID) error {
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("invoice %d not found in company %d", invoiceID, companyID)
		}
		return fmt.Errorf("invoice lookup failed: %w", err)
	}

	if err := ValidateInvoiceDeletable(invoice); err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Delete lines first (FK constraint)
		if err := tx.Where("invoice_id = ? AND company_id = ?", invoiceID, companyID).
			Delete(&models.InvoiceLine{}).Error; err != nil {
			return fmt.Errorf("delete invoice lines: %w", err)
		}

		if err := tx.Where("id = ? AND company_id = ?", invoiceID, companyID).
			Delete(&models.Invoice{}).Error; err != nil {
			return fmt.Errorf("delete invoice: %w", err)
		}

		cid := companyID
		return WriteAuditLogWithContextDetails(tx, "invoice.deleted", "invoice", invoiceID, actor,
			map[string]any{"company_id": companyID},
			&cid, userID, nil,
			map[string]any{
				"invoice_number": invoice.InvoiceNumber,
				"customer_id":    invoice.CustomerID,
				"amount":         invoice.Amount.StringFixed(2),
			},
		)
	})
}

// UpdateInvoiceStatus allows explicit status transition with state machine validation.
func UpdateInvoiceStatus(db *gorm.DB, companyID, invoiceID uint, newStatus models.InvoiceStatus) (*models.Invoice, error) {
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("invoice %d not found in company %d", invoiceID, companyID)
		}
		return nil, fmt.Errorf("invoice lookup failed: %w", err)
	}

	if !isValidInvoiceTransition(invoice.Status, newStatus) {
		return nil, fmt.Errorf("invalid status transition: %s → %s", invoice.Status, newStatus)
	}

	oldStatus := invoice.Status
	invoice.Status = newStatus

	now := time.Now()
	switch newStatus {
	case models.InvoiceStatusIssued:
		if invoice.IssuedAt == nil {
			invoice.IssuedAt = &now
		}
	case models.InvoiceStatusSent:
		if invoice.SentAt == nil {
			invoice.SentAt = &now
		}
	case models.InvoiceStatusVoided:
		if invoice.VoidedAt == nil {
			invoice.VoidedAt = &now
		}
	}

	if err := db.Save(&invoice).Error; err != nil {
		return nil, fmt.Errorf("invoice update failed: %w", err)
	}

	TryWriteAuditLogWithContext(
		db, "invoice.status_change", "Invoice", invoice.ID, "system",
		map[string]any{
			"invoice_number": invoice.InvoiceNumber,
			"old_status":     oldStatus,
			"new_status":     newStatus,
		},
		&companyID, nil,
	)

	return &invoice, nil
}

// isValidInvoiceTransition checks if a status transition is allowed per state machine rules.
func isValidInvoiceTransition(from, to models.InvoiceStatus) bool {
	// Voided is terminal: allowed from any non-voided state
	if to == models.InvoiceStatusVoided && from != models.InvoiceStatusVoided {
		return true
	}

	switch from {
	case models.InvoiceStatusDraft:
		return to == models.InvoiceStatusIssued
	case models.InvoiceStatusIssued:
		return to == models.InvoiceStatusSent || to == models.InvoiceStatusPaid
	case models.InvoiceStatusSent:
		return to == models.InvoiceStatusPartiallyPaid ||
			to == models.InvoiceStatusPaid ||
			to == models.InvoiceStatusOverdue
	case models.InvoiceStatusPartiallyPaid:
		return to == models.InvoiceStatusPaid ||
			to == models.InvoiceStatusOverdue
	case models.InvoiceStatusOverdue:
		return to == models.InvoiceStatusPaid
	case models.InvoiceStatusPaid:
		return to == models.InvoiceStatusVoided
	}

	return false
}

// RecalculateInvoiceBalance recalculates balance_due from amount and payment records.
// For MVP, balance_due = amount (no payment tracking yet).
func RecalculateInvoiceBalance(db *gorm.DB, companyID, invoiceID uint) (*models.Invoice, error) {
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("invoice %d not found in company %d", invoiceID, companyID)
		}
		return nil, fmt.Errorf("invoice lookup failed: %w", err)
	}

	invoice.BalanceDue = invoice.Amount

	if err := db.Save(&invoice).Error; err != nil {
		return nil, fmt.Errorf("balance recalculation failed: %w", err)
	}

	return &invoice, nil
}
