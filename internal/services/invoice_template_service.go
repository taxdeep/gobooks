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
