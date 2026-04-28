// 遵循project_guide.md
package services

import (
	"balanciz/internal/models"
)

// CompanyAuditSnapshot returns a non-sensitive map of company profile fields for audit before/after.
func CompanyAuditSnapshot(c models.Company) map[string]any {
	return map[string]any{
		"name":             c.Name,
		"entity_type":      c.EntityType,
		"business_type":    c.BusinessType,
		"address_line":     c.AddressLine,
		"city":             c.City,
		"province":         c.Province,
		"postal_code":      c.PostalCode,
		"country":          c.Country,
		"business_number":  c.BusinessNumber,
		"industry":         c.Industry,
		"incorporated_date": c.IncorporatedDate,
		"fiscal_year_end":   c.FiscalYearEnd,
	}
}

// AIConnectionAuditSnapshot returns a safe snapshot (no raw API key) for audit before/after.
func AIConnectionAuditSnapshot(row models.AIConnectionSettings) map[string]any {
	m := map[string]any{
		"provider":        row.Provider,
		"api_base_url":    row.APIBaseURL,
		"model_name":      row.ModelName,
		"enabled":         row.Enabled,
		"vision_enabled":  row.VisionEnabled,
		"has_api_key":     row.APIKey != "",
		"last_test_ok":    row.LastTestOK,
		"last_test_message": row.LastTestMessage,
	}
	if row.APIKey != "" {
		m["api_key_hint"] = MaskAPIKey(row.APIKey)
	} else {
		m["api_key_hint"] = ""
	}
	if row.LastTestAt != nil {
		m["last_test_at"] = row.LastTestAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return m
}
