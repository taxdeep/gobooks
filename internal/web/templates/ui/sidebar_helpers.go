// 遵循project_guide.md
package ui

import "strings"

// SectionKeyForActivePage maps SidebarVM.Active to a collapsible group key.
// Used to keep the section containing the current route expanded on load.
func SectionKeyForActivePage(active string) string {
	switch active {
	case "Dashboard", "Setup":
		return "core"
	case "Customers", "Quotes", "Sales Orders", "Customer Deposits", "Customer Receipts",
		"AR Returns", "AR Refunds", "AR Write-Offs", "Customer Statement", "Receive Payment", "Credit Notes":
		return "sales"
	case "Expenses", "Vendors", "Pay Bills",
		"Purchase Orders", "Vendor Prepayments", "Vendor Returns", "Vendor Credit Notes", "Vendor Refunds", "AP Aging":
		return "expenses"
	case "Bank Reconcile", "Reports", "Accounts":
		return "accounting"
	case "Products & Services", "Warehouses", "Warehouse Transfers", "Stock Report", "Return Receipts", "Returns to Vendor":
		return "inventory"
	default:
		if IsSettingsNavActive(active) {
			return "settings"
		}
		return ""
	}
}

// IsSettingsNavActive is true on the top-level /settings hub and every /settings/* sub-page.
// Used by the collapsed sidebar Settings entry so it stays highlighted anywhere inside settings.
func IsSettingsNavActive(active string) bool {
	switch active {
	case "Settings Hub",
		"AI Connect Settings", "Members Settings", "Audit Log",
		"Channels",
		"Payment Gateways", "Gateway Settlement Review", "Gateway Payouts", "Gateway Disputes",
		"Recon Exceptions", "Investigation Workspace",
		"Accounting Books", "AR/AP Control":
		return true
	}
	return IsCompanySettingsNavActive(active) || IsUserPreferencesNavActive(active)
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
