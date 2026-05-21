// 遵循project_guide.md
package web

import "balanciz/internal/web/templates/pages"

func breadcrumbSettingsCompanyHub() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: ""},
	}
}

func breadcrumbSettingsCompanyProfile() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Profile", Href: ""},
	}
}

func breadcrumbSettingsCompanyTemplates() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Templates", Href: ""},
	}
}

func breadcrumbSettingsCompanyFeatures() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Features", Href: ""},
	}
}

func breadcrumbSettingsCompanySalesTax() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Sales Tax", Href: ""},
	}
}

func breadcrumbSettingsCompanyNumbering() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Numbering", Href: ""},
	}
}

func breadcrumbSettingsAIConnect() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "AI Connect", Href: ""},
	}
}

func breadcrumbSettingsCompanyNotifications() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Notifications", Href: ""},
	}
}

func breadcrumbSettingsCompanySecurity() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Security", Href: ""},
	}
}

func breadcrumbSettingsCompanyPaymentTerms() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Payment Terms", Href: ""},
	}
}

func breadcrumbSettingsCompanyCurrency() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Currency", Href: ""},
	}
}

func breadcrumbSettingsExchangeRates() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/setting/company"},
		{Label: "Currency", Href: "/setting/company/currency"},
		{Label: "Exchange Rates", Href: ""},
	}
}
