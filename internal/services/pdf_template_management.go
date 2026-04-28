// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// pdf_template_management.go — CRUD-style operations for the Phase 3 (G6)
// management page at /pdf-templates. Editing the schema_json itself is
// G7 — this commit only adds clone + set-default + delete + list. Users
// can switch their default template via this page; visual customisation
// requires G7's editor.

// ListPDFTemplatesForCompany returns every template visible to companyID:
// company-owned rows + system presets. Rows are sorted by document_type,
// then is_system DESC (system presets last, company customs first), then
// name. Inactive rows excluded.
func ListPDFTemplatesForCompany(db *gorm.DB, companyID uint) ([]models.PDFTemplate, error) {
	var rows []models.PDFTemplate
	err := db.
		Where("(company_id = ? OR company_id IS NULL) AND is_active = true", companyID).
		Order("document_type ASC, is_system ASC, name ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ClonePDFTemplate copies a system preset (or any template) into a new
// company-owned row that operators can later customise via the editor.
// The new row inherits everything except IsSystem (always false on the
// clone) and IsDefault (always false; operator promotes via SetDefault).
func ClonePDFTemplate(db *gorm.DB, companyID, srcID uint, newName string) (*models.PDFTemplate, error) {
	var src models.PDFTemplate
	if err := db.First(&src, srcID).Error; err != nil {
		return nil, fmt.Errorf("source template not found: %w", err)
	}
	if src.CompanyID != nil && *src.CompanyID != companyID {
		return nil, errors.New("source template belongs to a different company")
	}
	if newName == "" {
		newName = src.Name + " (Copy)"
	}
	cloned := models.PDFTemplate{
		CompanyID:    &companyID,
		DocumentType: src.DocumentType,
		Name:         newName,
		Description:  src.Description,
		SchemaJSON:   datatypes.JSON(append([]byte{}, src.SchemaJSON...)), // defensive copy
		IsDefault:    false,
		IsActive:     true,
		IsSystem:     false,
	}
	if err := db.Create(&cloned).Error; err != nil {
		return nil, fmt.Errorf("clone template: %w", err)
	}
	return &cloned, nil
}

// SetDefaultPDFTemplate promotes one company-owned template to the default
// for its document type and demotes all peers in the same transaction so
// the partial unique index never trips.
func SetDefaultPDFTemplate(db *gorm.DB, companyID, tmplID uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var t models.PDFTemplate
		if err := tx.First(&t, tmplID).Error; err != nil {
			return fmt.Errorf("template not found: %w", err)
		}
		if t.CompanyID == nil {
			return errors.New("system templates cannot be set as default — clone first")
		}
		if *t.CompanyID != companyID {
			return errors.New("template belongs to a different company")
		}
		// Demote peers.
		if err := tx.Model(&models.PDFTemplate{}).
			Where("company_id = ? AND document_type = ? AND id <> ? AND is_default = ?",
				companyID, t.DocumentType, t.ID, true).
			Update("is_default", false).Error; err != nil {
			return fmt.Errorf("demote peers: %w", err)
		}
		return tx.Model(&t).Update("is_default", true).Error
	})
}

// UpdatePDFTemplateSchema replaces the schema_json on a company-owned
// template. Validates the schema parses (the renderer would otherwise
// fail at PDF time). System rows are rejected — clone first, then edit
// the clone.
func UpdatePDFTemplateSchema(db *gorm.DB, companyID, tmplID uint, name, description string, schemaJSON []byte) error {
	if len(schemaJSON) == 0 {
		return errors.New("schema_json is empty")
	}
	var t models.PDFTemplate
	if err := db.First(&t, tmplID).Error; err != nil {
		return fmt.Errorf("template not found: %w", err)
	}
	if t.IsSystem || t.CompanyID == nil {
		return errors.New("system templates cannot be edited — clone first")
	}
	if *t.CompanyID != companyID {
		return errors.New("template belongs to a different company")
	}
	updates := map[string]any{
		"schema_json": datatypes.JSON(schemaJSON),
	}
	if name != "" {
		updates["name"] = name
	}
	// Always allow description update (including clearing to empty).
	updates["description"] = description
	return db.Model(&t).Updates(updates).Error
}

// DeletePDFTemplate removes a company-owned template. System rows are
// rejected (operators clone first, then can delete the clone). Removing
// the current default falls back to the system preset on next render.
func DeletePDFTemplate(db *gorm.DB, companyID, tmplID uint) error {
	var t models.PDFTemplate
	if err := db.First(&t, tmplID).Error; err != nil {
		return fmt.Errorf("template not found: %w", err)
	}
	if t.IsSystem || t.CompanyID == nil {
		return errors.New("system templates cannot be deleted")
	}
	if *t.CompanyID != companyID {
		return errors.New("template belongs to a different company")
	}
	return db.Delete(&t).Error
}
