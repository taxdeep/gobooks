// 遵循project_guide.md
package pdf

import (
	"fmt"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// seed.go — idempotent insert of system-shipped preset rows.
//
// Called from db.Migrate after AutoMigrate has created pdf_templates so the
// table is never empty for a fresh install. Re-running is safe: matching is
// done on (company_id IS NULL, document_type, name) so existing rows update
// in place rather than duplicating.
//
// System rows have CompanyID=NULL — the management page exposes them as
// "system templates" and forces a clone on first edit. Companies that have
// not picked their own default for a doc type fall back to the system
// preset marked is_default=true (Classic, per AllSystemPresets).

// SeedSystemPDFTemplates upserts the 18 system-shipped presets.
// Idempotent: each call leaves the table in the same state.
func SeedSystemPDFTemplates(db *gorm.DB) error {
	for _, p := range AllSystemPresets() {
		schemaJSON := MustMarshalSchema(p.Schema)
		row := models.PDFTemplate{
			CompanyID:    nil,
			DocumentType: string(p.DocumentType),
			Name:         p.Name,
			Description:  p.Description,
			SchemaJSON:   datatypes.JSON(schemaJSON),
			IsDefault:    p.IsDefault,
			IsActive:     true,
			IsSystem:     true,
		}
		// Upsert by (NULL company_id, document_type, name). GORM doesn't
		// have a clean ON CONFLICT helper for nullable columns so we do
		// a SELECT-then-update / insert.
		var existing models.PDFTemplate
		err := db.Where("company_id IS NULL AND document_type = ? AND name = ?",
			row.DocumentType, row.Name).
			First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			if err := db.Create(&row).Error; err != nil {
				return fmt.Errorf("seed pdf preset %s: %w", row.Name, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("seed pdf preset lookup %s: %w", row.Name, err)
		}
		// Refresh schema + description on every seed so renderer fixes /
		// new fields land without manual reseed. Name + IsDefault stay as
		// the operator may have edited those (though clone is the
		// recommended path).
		updates := map[string]any{
			"description": row.Description,
			"schema_json": row.SchemaJSON,
			"is_active":   true,
			"is_system":   true,
		}
		if err := db.Model(&existing).Updates(updates).Error; err != nil {
			return fmt.Errorf("seed pdf preset update %s: %w", row.Name, err)
		}
	}
	return nil
}
