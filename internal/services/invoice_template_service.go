// 遵循project_guide.md
package services

import (
	"encoding/json"
	"fmt"

	"gobooks/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// CreateInvoiceTemplate creates a new invoice template in the database.
// Validates company ownership. Only one default template per company allowed.
//
// If isDefault=true and another default already exists, returns an error.
func CreateInvoiceTemplate(db *gorm.DB, companyID uint, name, description string, configJSON []byte, isDefault bool) (*models.InvoiceTemplate, error) {
	// 1. Verify company exists
	var company models.Company
	if err := db.Where("id = ?", companyID).First(&company).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("company %d not found", companyID)
		}
		return nil, fmt.Errorf("company lookup failed: %w", err)
	}

	// 2. Validate name not empty
	if name == "" {
		return nil, fmt.Errorf("template name is required")
	}

	// 3. If isDefault=true, check no other default exists for this company
	if isDefault {
		var existingDefault models.InvoiceTemplate
		result := db.Where("company_id = ? AND is_default = ?", companyID, true).First(&existingDefault)
		if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("default template check failed: %w", result.Error)
		}
		if result.Error == nil {
			return nil, fmt.Errorf("company %d already has a default template (id=%d); remove default from it first", companyID, existingDefault.ID)
		}
	}

	// 4. Validate configJSON is valid JSON
	if configJSON == nil {
		configJSON = []byte("{}")
	}
	var testConfig interface{}
	if err := json.Unmarshal(configJSON, &testConfig); err != nil {
		return nil, fmt.Errorf("invalid JSON in config_json: %w", err)
	}

	// 5. Create template
	template := models.InvoiceTemplate{
		CompanyID:   companyID,
		Name:        name,
		Description: description,
		ConfigJSON:  datatypes.JSON(configJSON),
		IsDefault:   isDefault,
	}

	if err := db.Create(&template).Error; err != nil {
		return nil, fmt.Errorf("template creation failed: %w", err)
	}

	// 6. Audit log
	TryWriteAuditLogWithContext(
		db, "create", "InvoiceTemplate", template.ID, "system",
		map[string]any{"name": name, "is_default": isDefault},
		&companyID, nil,
	)

	return &template, nil
}

// UpdateInvoiceTemplate updates an existing template's name, description, and config.
// Validates company ownership and unique default constraint.
func UpdateInvoiceTemplate(db *gorm.DB, companyID, templateID uint, name, description string, configJSON []byte, isDefault bool) (*models.InvoiceTemplate, error) {
	// 1. Load template and verify company ownership
	var template models.InvoiceTemplate
	if err := db.Where("id = ? AND company_id = ?", templateID, companyID).First(&template).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("template %d not found in company %d", templateID, companyID)
		}
		return nil, fmt.Errorf("template lookup failed: %w", err)
	}

	// 2. Validate name not empty
	if name == "" {
		return nil, fmt.Errorf("template name is required")
	}

	// 3. If marking as default, ensure no other default exists
	if isDefault && !template.IsDefault {
		var existingDefault models.InvoiceTemplate
		result := db.Where("company_id = ? AND is_default = ? AND id != ?", companyID, true, templateID).First(&existingDefault)
		if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("default template check failed: %w", result.Error)
		}
		if result.Error == nil {
			return nil, fmt.Errorf("company %d already has a default template (id=%d); remove default from it first", companyID, existingDefault.ID)
		}
	}

	// 4. Validate configJSON is valid JSON
	if configJSON == nil {
		configJSON = []byte("{}")
	}
	var testConfig interface{}
	if err := json.Unmarshal(configJSON, &testConfig); err != nil {
		return nil, fmt.Errorf("invalid JSON in config_json: %w", err)
	}

	// 5. Capture before state for audit
	beforeState := map[string]any{
		"name":        template.Name,
		"description": template.Description,
		"is_default":  template.IsDefault,
	}

	// 6. Update fields
	template.Name = name
	template.Description = description
	template.ConfigJSON = datatypes.JSON(configJSON)
	template.IsDefault = isDefault

	if err := db.Save(&template).Error; err != nil {
		return nil, fmt.Errorf("template update failed: %w", err)
	}

	// 7. Audit log with before/after
	afterState := map[string]any{
		"name":        template.Name,
		"description": template.Description,
		"is_default":  template.IsDefault,
	}

	TryWriteAuditLogWithContextDetails(
		db, "update", "InvoiceTemplate", template.ID, "system",
		nil, &companyID, nil,
		beforeState, afterState,
	)

	return &template, nil
}

// GetInvoiceTemplate retrieves a template by ID, verifying company ownership.
func GetInvoiceTemplate(db *gorm.DB, companyID, templateID uint) (*models.InvoiceTemplate, error) {
	var template models.InvoiceTemplate
	if err := db.Where("id = ? AND company_id = ?", templateID, companyID).First(&template).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("template %d not found in company %d", templateID, companyID)
		}
		return nil, fmt.Errorf("template lookup failed: %w", err)
	}
	return &template, nil
}

// ListInvoiceTemplates retrieves all templates for a company, ordered by name.
func ListInvoiceTemplates(db *gorm.DB, companyID uint) ([]models.InvoiceTemplate, error) {
	var templates []models.InvoiceTemplate
	if err := db.Where("company_id = ?", companyID).Order("name ASC").Find(&templates).Error; err != nil {
		return nil, fmt.Errorf("template list failed: %w", err)
	}
	return templates, nil
}

// GetDefaultInvoiceTemplate retrieves the default template for a company (if one exists).
// Returns nil (not error) if no default is set.
func GetDefaultInvoiceTemplate(db *gorm.DB, companyID uint) (*models.InvoiceTemplate, error) {
	var template models.InvoiceTemplate
	result := db.Where("company_id = ? AND is_default = ?", companyID, true).First(&template)
	if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("default template lookup failed: %w", result.Error)
	}
	if result.Error == gorm.ErrRecordNotFound {
		return nil, nil // No default set (not an error condition)
	}
	return &template, nil
}

// SetDefaultInvoiceTemplate atomically makes the given template the company default.
// Any previously default template for the same company is unset in the same transaction.
// Returns an error if the template does not belong to the company.
func SetDefaultInvoiceTemplate(db *gorm.DB, companyID, templateID uint) (*models.InvoiceTemplate, error) {
	var target models.InvoiceTemplate
	if err := db.Where("id = ? AND company_id = ?", templateID, companyID).
		First(&target).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("template %d not found in company %d", templateID, companyID)
		}
		return nil, fmt.Errorf("template lookup failed: %w", err)
	}

	if target.IsDefault {
		// Already default — nothing to do.
		return &target, nil
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		// Clear any existing default for this company.
		if err := tx.Model(&models.InvoiceTemplate{}).
			Where("company_id = ? AND is_default = true", companyID).
			Update("is_default", false).Error; err != nil {
			return fmt.Errorf("clear old default: %w", err)
		}
		// Set new default.
		if err := tx.Model(&target).Update("is_default", true).Error; err != nil {
			return fmt.Errorf("set new default: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	target.IsDefault = true
	TryWriteAuditLogWithContext(
		db, "invoice_template.set_default", "InvoiceTemplate", target.ID, "system",
		map[string]any{"name": target.Name},
		&companyID, nil,
	)
	return &target, nil
}

// DeactivateInvoiceTemplate marks a template as inactive.
// Inactive templates are skipped during template resolution for new renders/sends,
// but existing invoice bindings (invoice.TemplateID) are preserved — the render
// pipeline falls back gracefully to the company default or system fallback.
//
// Unlike DeleteInvoiceTemplate, deactivation is allowed even when invoices are bound
// to the template, because the render/send pipeline already handles inactive templates.
func DeactivateInvoiceTemplate(db *gorm.DB, companyID, templateID uint) (*models.InvoiceTemplate, error) {
	var tmpl models.InvoiceTemplate
	if err := db.Where("id = ? AND company_id = ?", templateID, companyID).First(&tmpl).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("template %d not found in company %d", templateID, companyID)
		}
		return nil, fmt.Errorf("template lookup failed: %w", err)
	}

	if !tmpl.IsActive {
		return &tmpl, nil // already inactive — no-op
	}

	// Cannot deactivate the company default while it is still the default.
	// Caller must first set a different template as default (or clear the default).
	if tmpl.IsDefault {
		return nil, fmt.Errorf("template %d is the company default; set another template as default before deactivating this one", templateID)
	}

	if err := db.Model(&tmpl).Update("is_active", false).Error; err != nil {
		return nil, fmt.Errorf("deactivate template failed: %w", err)
	}
	tmpl.IsActive = false

	TryWriteAuditLogWithContext(
		db, "invoice_template.deactivated", "InvoiceTemplate", tmpl.ID, "system",
		map[string]any{"name": tmpl.Name},
		&companyID, nil,
	)
	return &tmpl, nil
}

// BindTemplateToInvoice sets an explicit template reference on a draft invoice.
// Validates company isolation for both the invoice and the template.
// Only allowed on draft invoices (issued/sent invoices have a stable binding).
// The template must be active to be bound.
func BindTemplateToInvoice(db *gorm.DB, companyID, invoiceID, templateID uint) (*models.Invoice, error) {
	// Verify invoice belongs to company and is still draft.
	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).First(&inv).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("invoice %d not found in company %d", invoiceID, companyID)
		}
		return nil, fmt.Errorf("invoice lookup failed: %w", err)
	}
	if inv.Status != models.InvoiceStatusDraft {
		return nil, fmt.Errorf("template binding can only be changed on draft invoices (current status: %s)", inv.Status)
	}

	// Verify template belongs to company and is active.
	var tmpl models.InvoiceTemplate
	if err := db.Where("id = ? AND company_id = ? AND is_active = ?", templateID, companyID, true).
		First(&tmpl).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("template %d not found or inactive in company %d", templateID, companyID)
		}
		return nil, fmt.Errorf("template lookup failed: %w", err)
	}

	if err := db.Model(&inv).Update("template_id", templateID).Error; err != nil {
		return nil, fmt.Errorf("bind template failed: %w", err)
	}
	inv.TemplateID = &tmpl.ID

	TryWriteAuditLogWithContext(
		db, "invoice.template_bound", "Invoice", inv.ID, "system",
		map[string]any{"template_id": templateID, "template_name": tmpl.Name},
		&companyID, nil,
	)
	return &inv, nil
}

// DeleteInvoiceTemplate soft-deletes a template (marks as inactive via status flag if needed).
// For MVP, we hard-delete since templates are immutable configs, not business data.
// Validates company ownership.
func DeleteInvoiceTemplate(db *gorm.DB, companyID, templateID uint) error {
	// 1. Load template and verify company ownership
	var template models.InvoiceTemplate
	if err := db.Where("id = ? AND company_id = ?", templateID, companyID).First(&template).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("template %d not found in company %d", templateID, companyID)
		}
		return fmt.Errorf("template lookup failed: %w", err)
	}

	// 2. Prevent deletion of default template
	if template.IsDefault {
		return fmt.Errorf("cannot delete default template; set another template as default first")
	}

	// 3. Check for active usage (any invoices linked to this template)
	var invoiceCount int64
	if err := db.Model(&models.Invoice{}).
		Where("company_id = ? AND template_id = ?", companyID, templateID).
		Count(&invoiceCount).Error; err != nil {
		return fmt.Errorf("usage check failed: %w", err)
	}
	if invoiceCount > 0 {
		return fmt.Errorf("template is in use by %d invoice(s); cannot delete", invoiceCount)
	}

	// 4. Delete template
	if err := db.Delete(&template).Error; err != nil {
		return fmt.Errorf("template deletion failed: %w", err)
	}

	// 5. Audit log
	TryWriteAuditLogWithContext(
		db, "delete", "InvoiceTemplate", template.ID, "system",
		map[string]any{"name": template.Name},
		&companyID, nil,
	)

	return nil
}
