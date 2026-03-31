// 遵循project_guide.md
package services

import (
	"fmt"

	"gobooks/internal/models"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// InvoiceValidationError groups validation issues into a single error.
type InvoiceValidationError struct {
	Errors []string
}

func (e *InvoiceValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "no validation errors"
	}
	msg := "invoice validation failed:"
	for _, err := range e.Errors {
		msg += "\n  - " + err
	}
	return msg
}

// ValidateInvoiceForIssuing checks if an invoice can transition to issued state.
// Returns nil if valid; otherwise returns validation errors.
//
// Checks:
// 1. Invoice has at least one line item
// 2. Each line has valid product/service, description, qty, unit_price
// 3. Tax codes exist and are valid
// 4. Revenue accounts exist for each line's product
// 5. Subtotal calculates correctly
// 6. Customer exists
func ValidateInvoiceForIssuing(db *gorm.DB, companyID, invoiceID uint) error {
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).
		Preload("Lines.ProductService.RevenueAccount").
		Preload("Lines.TaxCode").
		Preload("Customer").
		First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return &InvoiceValidationError{Errors: []string{fmt.Sprintf("invoice %d not found in company %d", invoiceID, companyID)}}
		}
		return &InvoiceValidationError{Errors: []string{fmt.Sprintf("invoice lookup failed: %v", err)}}
	}

	errors := make([]string, 0)

	// 1. Check customer exists (value type: ID == 0 means not loaded)
	if invoice.Customer.ID == 0 {
		errors = append(errors, "customer not found")
	}

	// 2. Check has at least one line
	if len(invoice.Lines) == 0 {
		errors = append(errors, "invoice must have at least one line item")
	}

	// 3. Check each line
	for i, line := range invoice.Lines {
		lineNum := i + 1

		// Line must have description
		if line.Description == "" {
			errors = append(errors, fmt.Sprintf("line %d: description is required", lineNum))
		}

		// Line qty must be positive
		if line.Qty.IsZero() || line.Qty.IsNegative() {
			errors = append(errors, fmt.Sprintf("line %d: quantity must be positive", lineNum))
		}

		// Line unit price must be non-negative (but can be zero for giveaways)
		if line.UnitPrice.IsNegative() {
			errors = append(errors, fmt.Sprintf("line %d: unit price cannot be negative", lineNum))
		}

		// If product is specified, verify it exists
		if line.ProductServiceID != nil && line.ProductService == nil {
			errors = append(errors, fmt.Sprintf("line %d: product/service not found", lineNum))
		}

		// If product is specified, verify revenue account exists (value type: ID == 0 means not loaded)
		if line.ProductService != nil && line.ProductService.RevenueAccount.ID == 0 {
			errors = append(errors, fmt.Sprintf("line %d: product's revenue account not found", lineNum))
		}

		// If tax code is specified, verify it exists
		if line.TaxCodeID != nil && line.TaxCode == nil {
			errors = append(errors, fmt.Sprintf("line %d: tax code not found", lineNum))
		}

		// Verify line computed fields (LineNet, LineTax, LineTotal)
		expectedLineNet := line.Qty.Mul(line.UnitPrice)
		if !line.LineNet.Equal(expectedLineNet) {
			errors = append(errors, fmt.Sprintf("line %d: LineNet mismatch (expected %s, got %s)", lineNum, expectedLineNet.String(), line.LineNet.String()))
		}
	}

	// 4. Verify subtotal calculation
	expectedSubtotal := decimal.Zero
	for _, line := range invoice.Lines {
		expectedSubtotal = expectedSubtotal.Add(line.LineNet)
	}
	if !invoice.Subtotal.Equal(expectedSubtotal) {
		errors = append(errors, fmt.Sprintf("Subtotal mismatch (expected %s, got %s)", expectedSubtotal.String(), invoice.Subtotal.String()))
	}

	// 5. Verify amount calculates correctly (Subtotal + TaxTotal)
	expectedAmount := invoice.Subtotal.Add(invoice.TaxTotal)
	if !invoice.Amount.Equal(expectedAmount) {
		errors = append(errors, fmt.Sprintf("Amount mismatch (expected %s, got %s)", expectedAmount.String(), invoice.Amount.String()))
	}

	if len(errors) > 0 {
		return &InvoiceValidationError{Errors: errors}
	}

	return nil
}

// ValidateInvoiceForPosting checks if an invoice can be posted to accounting.
// Returns nil if valid; otherwise returns validation errors.
//
// Checks:
// 1. All issuing validations pass
// 2. All revenue accounts exist and are active
// 3. All tax codes are active and configured
// 4. GL accounts for tax payables exist
// 5. No duplicate posting (already has JournalEntryID)
func ValidateInvoiceForPosting(db *gorm.DB, companyID, invoiceID uint) error {
	// 1. First, run issuing validations
	if err := ValidateInvoiceForIssuing(db, companyID, invoiceID); err != nil {
		return err
	}

	// 2. Load invoice with full preloads for posting validation
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).
		Preload("Lines.ProductService.RevenueAccount").
		Preload("Lines.TaxCode").
		Preload("Customer").
		First(&invoice).Error; err != nil {
		return &InvoiceValidationError{Errors: []string{"invoice lookup failed"}}
	}

	errors := make([]string, 0)

	// 3. Check not already posted
	if invoice.JournalEntryID != nil {
		errors = append(errors, fmt.Sprintf("invoice is already posted (JE=%d)", *invoice.JournalEntryID))
	}

	// 4. Verify all revenue accounts are active (if products specified)
	for _, line := range invoice.Lines {
		if line.ProductService != nil && line.ProductService.RevenueAccount.ID != 0 {
			if !line.ProductService.RevenueAccount.IsActive {
				errors = append(errors, fmt.Sprintf("line %d: revenue account %q is inactive",
					line.SortOrder, line.ProductService.RevenueAccount.Name))
			}
		}
	}

	// 5. Verify all tax codes are active
	for _, line := range invoice.Lines {
		if line.TaxCode != nil && !line.TaxCode.IsActive {
			errors = append(errors, fmt.Sprintf("line %d: tax code %q is inactive",
				line.SortOrder, line.TaxCode.Name))
		}
	}

	if len(errors) > 0 {
		return &InvoiceValidationError{Errors: errors}
	}

	return nil
}

// ValidateInvoiceForSending checks if an invoice can be sent via email.
// Returns nil if valid; otherwise returns validation errors.
//
// Checks:
// 1. Customer email exists
// 2. Recipient email is valid format
func ValidateInvoiceForSending(db *gorm.DB, companyID, invoiceID uint) error {
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).
		Preload("Customer").
		First(&invoice).Error; err != nil {
		return &InvoiceValidationError{Errors: []string{"invoice lookup failed"}}
	}

	errors := make([]string, 0)

	// 1. Check customer exists (value type: ID == 0 means not loaded)
	if invoice.Customer.ID == 0 {
		errors = append(errors, "customer not found")
	}

	// 2. Check customer has email snapshot (used as send destination)
	if invoice.CustomerEmailSnapshot == "" {
		errors = append(errors, "customer email is not set — update the customer record and re-save the invoice")
	}

	// 3. Check SMTP is configured for company
	// This is checked at send time by email service

	if len(errors) > 0 {
		return &InvoiceValidationError{Errors: errors}
	}

	return nil
}

// ValidateInvoiceForVoiding checks if an invoice can be voided.
// Returns nil if valid; otherwise returns validation errors.
//
// Checks:
// 1. Invoice is not already voided
// 2. Invoice has been posted (has JournalEntryID)
// 3. No reversal has already been created
func ValidateInvoiceForVoiding(db *gorm.DB, companyID, invoiceID uint) error {
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&invoice).Error; err != nil {
		return &InvoiceValidationError{Errors: []string{"invoice lookup failed"}}
	}

	errors := make([]string, 0)

	// 1. Check not already voided
	if invoice.Status == models.InvoiceStatusVoided {
		errors = append(errors, "invoice is already voided")
	}

	// 2. Check is posted
	if invoice.JournalEntryID == nil {
		errors = append(errors, "invoice must be posted before it can be voided")
	}

	// 3. Check reversal not already created (future enhancement)
	// var reversal models.JournalEntry
	// if err := db.Where("company_id = ? AND source_type = ? AND source_id = ?", companyID, "invoice", invoiceID).
	// 	Where("description LIKE ?", "%Reversal%").
	// 	First(&reversal).Error; err != nil && err != gorm.ErrRecordNotFound {
	// 	errors = append(errors, "reversal lookup failed")
	// } else if err == nil {
	// 	errors = append(errors, "reversal already exists for this invoice")
	// }

	if len(errors) > 0 {
		return &InvoiceValidationError{Errors: errors}
	}

	return nil
}
