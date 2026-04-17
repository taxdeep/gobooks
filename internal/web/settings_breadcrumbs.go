// 遵循project_guide.md
package web

import "gobooks/internal/web/templates/pages"

func breadcrumbSettingsCompanyHub() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: ""},
	}
}

func breadcrumbSettingsCompanyProfile() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/settings/company"},
		{Label: "Profile", Href: ""},
	}
}

func breadcrumbSettingsCompanyTemplates() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/settings/company"},
		{Label: "Templates", Href: ""},
	}
}

func breadcrumbSettingsCompanySalesTax() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/settings/company"},
		{Label: "Sales Tax", Href: ""},
	}
}

func breadcrumbSettingsCompanyNumbering() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/settings/company"},
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
		{Label: "Company", Href: "/settings/company"},
		{Label: "Notifications", Href: ""},
	}
}

func breadcrumbSettingsCompanySecurity() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/settings/company"},
		{Label: "Security", Href: ""},
	}
}

func breadcrumbSettingsCompanyPaymentTerms() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/settings/company"},
		{Label: "Payment Terms", Href: ""},
	}
}

func breadcrumbSettingsCompanyCurrency() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/settings/company"},
		{Label: "Currency", Href: ""},
	}
}

func breadcrumbSettingsExchangeRates() []pages.SettingsBreadcrumbPart {
	return []pages.SettingsBreadcrumbPart{
		{Label: "Settings", Href: "/settings"},
		{Label: "Company", Href: "/settings/company"},
		{Label: "Currency", Href: "/settings/company/currency"},
		{Label: "Exchange Rates", Href: ""},
	}
}
