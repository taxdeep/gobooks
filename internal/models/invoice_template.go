// 遵循project_guide.md
package models

import (
	"encoding/json"
	"time"

	"gorm.io/datatypes"
)

// InvoiceTemplate stores company-specific invoice template configurations.
// Templates define rendering rules, default line-item layouts, and terms.
// All fields are company-scoped; ConfigJSON is flexible JSONB for future extensibility.
//
// IsDefault: at most one default template per company (enforced by unique partial index).
// ConfigJSON: stores template metadata (e.g., {"default_terms": "net_30", "lines": [...]})
type InvoiceTemplate struct {
	ID        uint   `gorm:"primaryKey"`
	CompanyID uint   `gorm:"not null;index"`
	Name      string `gorm:"not null"`
	Description string `gorm:"not null;default:''"`

	// ConfigJSON stores template configuration as JSONB.
// Use datatypes.JSON for GORM compatibility with PostgreSQL JSONB.
	ConfigJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`

	// IsDefault: only one per company (unique partial index enforced in migration)
	IsDefault bool `gorm:"not null;default:false;index:uk_invoices_templates_company_default,where:is_default = true"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the PostgreSQL table name for GORM.
func (InvoiceTemplate) TableName() string {
	return "invoices_templates"
}

// TemplateConfig represents the structured configuration inside ConfigJSON.
// This allows strongly-typed access to template settings.
type TemplateConfig struct {
	// Visual style
	TemplateStyle string `json:"template_style"` // "classic" or "modern" (default: "classic")
	AccentColor   string `json:"accent_color"`   // hex color for headings/accents (default: "#0066cc")

	// Display toggles
	ShowLogo           bool `json:"show_logo"`            // show company logo (default: true)
	ShowCompanyAddress bool `json:"show_company_address"` // show company address block (default: true)
	ShowShipTo         bool `json:"show_ship_to"`         // show ship-to section (default: false)
	ShowTaxSummary     bool `json:"show_tax_summary"`     // show tax breakdown in summary (default: true)
	ShowNotes          bool `json:"show_notes"`           // show notes/memo section (default: true)
	ShowFooter         bool `json:"show_footer"`          // show footer section (default: true)

	// Content defaults
	DefaultTerms       string `json:"default_terms"`
	DefaultMemo        string `json:"default_memo"`
	FooterText         string `json:"footer_text"`
	PaymentInstructions string `json:"payment_instructions"`

	// Email defaults
	EmailDefaultSubject string `json:"email_default_subject"`
	EmailDefaultBody    string `json:"email_default_body"`

	// Line item presets
	LineItemTemplate []TemplateLineItemConfig `json:"line_item_template"`
}

// DefaultTemplateConfig returns sensible defaults for a new template.
func DefaultTemplateConfig(style string) TemplateConfig {
	if style == "" {
		style = "classic"
	}
	accent := "#0066cc"
	if style == "modern" {
		accent = "#1a1a2e"
	}
	return TemplateConfig{
		TemplateStyle:      style,
		AccentColor:        accent,
		ShowLogo:           true,
		ShowCompanyAddress: true,
		ShowTaxSummary:     true,
		ShowNotes:          true,
		ShowFooter:         true,
	}
}

// TemplateLineItemConfig defines a template line item preset.
type TemplateLineItemConfig struct {
	ProductServiceID uint   `json:"product_service_id,omitempty"`
	Description      string `json:"description"`
	Qty              string `json:"qty"`
	TaxCodeID        uint   `json:"tax_code_id,omitempty"`
}

// UnmarshalConfig parses ConfigJSON into a strongly-typed TemplateConfig.
func (t *InvoiceTemplate) UnmarshalConfig() (*TemplateConfig, error) {
	var config TemplateConfig
	if err := json.Unmarshal(t.ConfigJSON, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// MarshalConfig converts a TemplateConfig back to JSON for storage.
func MarshalConfig(config *TemplateConfig) (datatypes.JSON, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(data), nil
}
