// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// CompanySettingsVM is used by the company profile (Settings > Company > Profile) page.
type CompanySettingsVM struct {
	HasCompany bool
	Breadcrumb []SettingsBreadcrumbPart
	Values     SetupFormValues
	Errors     SetupFormErrors
	Saved      bool

	// LogoPath is non-empty when a logo has been uploaded for this company.
	// Used to render a preview image on the profile page.
	LogoPath string
	// LogoError is a human-readable upload validation error (type, size, etc.).
	LogoError string

	// Over-shipment buffer (S3 — 2026-04-25). Company-wide default; warehouses
	// may override on their own profile.  See models.OverShipmentPolicy.
	OverShipmentEnabled bool
	OverShipmentMode    models.OverShipmentMode
	OverShipmentValue   string // raw decimal string for input round-trip
}

