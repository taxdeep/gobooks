// 遵循project_guide.md
package ui

import "strings"

// SectionKeyForActivePage maps SidebarVM.Active to a collapsible group key.
// Used to keep the section containing the current route expanded on load.
func SectionKeyForActivePage(active string) string {
	switch active {
	case "Dashboard", "Setup":
		return "core"
	case "Customers", "Receive Payment":
		return "sales"
	case "Expenses", "Vendors", "Pay Bills":
		return "expenses"
	case "Bank Reconcile", "Reports", "Accounts":
		return "accounting"
	case "AI Connect Settings", "Members Settings", "Audit Log", "Products & Services",
		"Payment Gateways", "Gateway Settlement Review", "Gateway Payouts", "Gateway Disputes",
		"Recon Exceptions", "Investigation Workspace",
		"User Preferences Hub", "User Preferences System Setup",
		"Accounting Books", "AR/AP Control":
		return "settings"
	default:
		if IsCompanySettingsNavActive(active) {
			return "settings"
		}
		return ""
	}
}

// IsCompanySettingsNavActive is true on Settings > Company hub and all company sub-pages.
// Active strings for those routes must start with "Company " (see layout SidebarVM on each page).
func IsCompanySettingsNavActive(active string) bool {
	return strings.HasPrefix(active, "Company ")
}

// IsUserPreferencesNavActive is true on Settings > User Preferences hub and all sub-pages.
func IsUserPreferencesNavActive(active string) bool {
	return strings.HasPrefix(active, "User Preferences")
}

// BoolStr returns "true" or "false" for HTML data attributes.
func BoolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
