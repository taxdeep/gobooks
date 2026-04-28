// 遵循project_guide.md
package pages

import "balanciz/internal/numbering"

// NumberingSettingsVM is Settings > Company > Numbering (display numbering only).
type NumberingSettingsVM struct {
	HasCompany bool
	Breadcrumb []SettingsBreadcrumbPart
	Rules      []numbering.DisplayRule
	FormError  string
	Saved      bool
}
