// 遵循产品需求 v1.0
package pages

import "gobooks/internal/models"

// SettingsBreadcrumbPart is a segment for settings sub-pages (Settings / Company / …).
type SettingsBreadcrumbPart struct {
	Label string
	Href  string // empty = current page (not a link)
}

// CompanyHubVM is the Company settings landing page.
type CompanyHubVM struct {
	HasCompany  bool
	Breadcrumb  []SettingsBreadcrumbPart
}

// CompanySubpageVM is a lightweight VM for placeholder company sub-pages (templates, etc.).
type CompanySubpageVM struct {
	HasCompany bool
	Breadcrumb []SettingsBreadcrumbPart
}

// SalesTaxVM is the view-model for Settings > Company > Sales Tax.
type SalesTaxVM struct {
	HasCompany bool
	Breadcrumb []SettingsBreadcrumbPart

	// Table data — all codes for this company (active and inactive).
	Items []models.TaxCode

	// Drawer state.
	DrawerOpen bool
	DrawerMode string // "create" or "edit"
	EditingID  uint

	// Form fields (strings so POST round-trips preserve user input on validation errors).
	Name                         string
	Rate                         string // display as percentage (e.g. "5" for 5%)
	RecoveryMode                 string
	RecoveryRate                 string // 0–100 percentage; only relevant when RecoveryMode = partial
	SalesTaxAccountID            string
	PurchaseRecoverableAccountID string

	// Field-level validation errors.
	NameError                         string
	RateError                         string
	RecoveryModeError                 string
	RecoveryRateError                 string
	SalesTaxAccountIDError            string
	PurchaseRecoverableAccountIDError string
	FormError                         string

	// Success banners.
	Created    bool
	Updated    bool
	InactiveOK bool

	// Dropdown data.
	LiabilityAccounts []models.Account // for Sales Tax Account (GST/HST Payable, etc.)
	AssetAccounts     []models.Account // for Purchase Recoverable Account (ITC Receivable, etc.)
}
