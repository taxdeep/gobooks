// 遵循project_guide.md
package models

import (
	"time"

	"gorm.io/datatypes"
)

// PDFTemplate is the Phase 3 block-based document template (migration 089).
// One row per (company, document_type) variant; the schema_json column holds
// the full layout (page + theme + ordered blocks). Multi-document by design:
// the same table powers Invoice / Quote / SO / Bill / PO / Shipment templates.
//
// CompanyID semantics:
//
//	NULL  — system preset, available to every company (cloned, not edited).
//	non-0 — owned by a specific company (custom or cloned from a system preset).
//
// At-most-one-default invariant: enforced per (company_id, document_type) by
// a partial unique index. System-preset rows (NULL company_id) may also flag
// is_default=true to act as the global fallback when no company default exists.
type PDFTemplate struct {
	ID           uint           `gorm:"primaryKey"`
	CompanyID    *uint          `gorm:"index"`
	DocumentType string         `gorm:"type:varchar(32);not null;index"`
	Name         string         `gorm:"type:varchar(128);not null"`
	Description  string         `gorm:"type:text;not null;default:''"`
	SchemaJSON   datatypes.JSON `gorm:"column:schema_json;type:jsonb;not null;default:'{}'"`
	IsDefault    bool           `gorm:"not null;default:false"`
	IsActive     bool           `gorm:"not null;default:true"`
	// IsSystem flags rows seeded as built-in presets — UI prevents direct
	// edit/delete (operators clone first, then edit the clone). Also used by
	// the renderer to decide whether the row is the global fallback.
	IsSystem bool `gorm:"not null;default:false"`
	// PreviewPNG is a small rasterised thumbnail rendered asynchronously after
	// save (Phase B feature). NULL means "not yet generated".
	PreviewPNG []byte `gorm:"type:bytea"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TableName pins the GORM table name to the snake-plural form used by
// migration 089 (the default pluraliser would also produce "pdf_templates"
// but the explicit override removes any ambiguity).
func (PDFTemplate) TableName() string {
	return "pdf_templates"
}

// PDFDocumentType is the typed enum for PDFTemplate.DocumentType.
// Stored as plain strings in the DB; this type exists for compile-time
// safety at call sites (renderer dispatch, field-registry lookup, etc.).
type PDFDocumentType string

const (
	PDFDocInvoice       PDFDocumentType = "invoice"
	PDFDocQuote         PDFDocumentType = "quote"
	PDFDocSalesOrder    PDFDocumentType = "sales_order"
	PDFDocBill          PDFDocumentType = "bill"
	PDFDocPurchaseOrder PDFDocumentType = "purchase_order"
	PDFDocShipment      PDFDocumentType = "shipment"
)

// AllPDFDocumentTypes is the canonical iteration order for seeding presets
// and rendering the document-type filter on the management page.
var AllPDFDocumentTypes = []PDFDocumentType{
	PDFDocInvoice,
	PDFDocQuote,
	PDFDocSalesOrder,
	PDFDocBill,
	PDFDocPurchaseOrder,
	PDFDocShipment,
}
