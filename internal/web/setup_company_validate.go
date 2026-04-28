// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"
	"time"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/pages"
)

// NormalizePostalCode uppercases trimmed input (country-agnostic; no spacing rules).
func NormalizePostalCode(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// NormalizeIncorporatedDate accepts YYYYMMDD, YYYY-MM-DD, or YYYY/MM/DD and
// returns the canonical YYYY-MM-DD form. Returns "" if the input cannot be
// parsed as a valid calendar date. Safe to call on untrusted input.
func NormalizeIncorporatedDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Strip all non-digit characters to obtain exactly 8 raw digits.
	raw := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(raw) != 8 {
		return ""
	}
	candidate := raw[:4] + "-" + raw[4:6] + "-" + raw[6:8]
	if _, err := time.Parse("2006-01-02", candidate); err != nil {
		return ""
	}
	return candidate
}

// NormalizeFiscalYearEnd accepts MMDD, MM-DD, or MM/DD and returns the
// canonical MM-DD form. Returns "" if the result is not a valid month/day
// combination. Safe to call on untrusted input.
func NormalizeFiscalYearEnd(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	raw := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(raw) != 4 {
		return ""
	}
	candidate := raw[:2] + "-" + raw[2:4]
	if _, err := time.Parse("01-02", candidate); err != nil {
		return ""
	}
	return candidate
}

// validateSetupCompanyForm validates the same company fields as the first-time setup wizard.
// When the returned errors struct has any field set, the request must not proceed.
func validateSetupCompanyForm(values pages.SetupFormValues) pages.SetupFormErrors {
	var errs pages.SetupFormErrors

	if values.CompanyName == "" {
		errs.CompanyName = "Company Name is required."
	}

	if _, err := models.ParseEntityType(values.EntityType); err != nil {
		errs.EntityType = "Entity Type is required."
	}

	if _, err := models.ParseIndustry(values.Industry); err != nil {
		errs.Industry = "Industry is required."
	}

	if values.AddressLine == "" {
		errs.AddressLine = "Address Line is required."
	}
	if values.City == "" {
		errs.City = "City is required."
	}
	if values.Province == "" {
		errs.Province = "Province is required."
	}
	if values.PostalCode == "" {
		errs.PostalCode = "Postal Code is required."
	}
	if values.Country == "" {
		errs.Country = "Country is required."
	}
	if values.BusinessNumber == "" {
		errs.BusinessNumber = "Business Number is required."
	}
	if values.Industry == "" {
		errs.Industry = "Industry is required."
	}

	if values.IncorporatedDate == "" {
		errs.IncorporatedDate = "Incorporated Date is required."
	} else if NormalizeIncorporatedDate(values.IncorporatedDate) == "" {
		errs.IncorporatedDate = "Incorporated Date must be a valid date."
	}

	if values.FiscalYearEnd == "" {
		errs.FiscalYearEnd = "Fiscal Year End is required."
	} else if NormalizeFiscalYearEnd(values.FiscalYearEnd) == "" {
		errs.FiscalYearEnd = "Fiscal Year End must be a valid month and day (MM-DD)."
	}

	if msg := validateAccountCodeLengthField(values.AccountCodeLength); msg != "" {
		errs.AccountCodeLength = msg
	}

	return errs
}

// validateAccountCodeLengthField validates the setup/bootstrap choice (4–12). Empty means default 4.
func validateAccountCodeLengthField(raw string) string {
	_, msg := ParseAccountCodeLengthChoice(raw)
	return msg
}

// ParseAccountCodeLengthChoice parses account code length from setup form; empty defaults to 4.
// On error, length is 0 and msg is non-empty.
func ParseAccountCodeLengthChoice(raw string) (int, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 4, ""
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < models.AccountCodeLengthMin || n > models.AccountCodeLengthMax {
		return 0, "Account code length must be from 4 to 12 digits."
	}
	return n, ""
}

// parseSetupCompanyForm parses validated company form values into model-ready fields.
// Call only when validateSetupCompanyForm returned no errors (HasAny() is false).
func parseSetupCompanyForm(values pages.SetupFormValues) (models.EntityType, models.BusinessType, models.Industry, error) {
	entityType, err := models.ParseEntityType(values.EntityType)
	if err != nil {
		return "", "", "", err
	}
	businessType := defaultBusinessTypeForEntity(entityType)
	industryValue, err := models.ParseIndustry(values.Industry)
	if err != nil {
		return "", "", "", err
	}
	return entityType, businessType, industryValue, nil
}
