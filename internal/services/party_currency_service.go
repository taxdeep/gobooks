// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Validation ────────────────────────────────────────────────────────────────

// ValidateDocumentCurrency checks whether docCurrencyCode is permitted for the
// given party under its CurrencyPolicy. Returns a user-facing error if not.
//
// Enforcement rules:
//
//   - CurrencyPolicy = "single", CurrencyCode = "": base currency only.
//     baseCurrencyCode is the company base; docCurrency must equal it.
//
//   - CurrencyPolicy = "single", CurrencyCode != "": docCurrency must equal
//     the party's explicit default currency.
//
//   - CurrencyPolicy = "multi_allowed", no rows in allowed-currency table:
//     any document currency is accepted (open policy).
//
//   - CurrencyPolicy = "multi_allowed", rows present: docCurrency must be
//     in the allowed list OR equal the party's default CurrencyCode.
//
// partyType must be models.PartyTypeCustomer or models.PartyTypeVendor.
func ValidateDocumentCurrency(
	db *gorm.DB,
	companyID, partyID uint,
	partyType models.PartyType,
	docCurrencyCode, baseCurrencyCode string,
) error {
	docCurrencyCode = strings.ToUpper(strings.TrimSpace(docCurrencyCode))
	if docCurrencyCode == "" {
		docCurrencyCode = baseCurrencyCode
	}

	switch partyType {
	case models.PartyTypeCustomer:
		return validateCustomerCurrency(db, companyID, partyID, docCurrencyCode, baseCurrencyCode)
	case models.PartyTypeVendor:
		return validateVendorCurrency(db, companyID, partyID, docCurrencyCode, baseCurrencyCode)
	default:
		return nil // no party → no restriction
	}
}

func validateCustomerCurrency(db *gorm.DB, companyID, customerID uint, docCurrency, baseCurrency string) error {
	var cust models.Customer
	if err := db.Select("id", "currency_code", "currency_policy").
		Where("id = ? AND company_id = ?", customerID, companyID).
		First(&cust).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // customer not found — let posting handle it
		}
		return fmt.Errorf("load customer for currency check: %w", err)
	}

	switch cust.CurrencyPolicy {
	case models.CustomerCurrencyPolicySingle, "": // "" = default (single)
		defaultCurr := cust.CurrencyCode
		if defaultCurr == "" {
			defaultCurr = baseCurrency
		}
		if !currencyMatch(docCurrency, defaultCurr) {
			return fmt.Errorf(
				"customer currency policy is 'single': document currency %s does not match the customer's default currency %s",
				docCurrency, defaultCurr,
			)
		}

	case models.CustomerCurrencyPolicyMultiAllowed:
		var allowed []models.CustomerAllowedCurrency
		if err := db.Where("company_id = ? AND customer_id = ?", companyID, customerID).
			Find(&allowed).Error; err != nil {
			return fmt.Errorf("load customer allowed currencies: %w", err)
		}
		if len(allowed) == 0 {
			return nil // open policy — any currency accepted
		}
		// Also always allow the customer's default currency.
		defaultCurr := cust.CurrencyCode
		if currencyMatch(docCurrency, defaultCurr) {
			return nil
		}
		for _, a := range allowed {
			if currencyMatch(docCurrency, a.CurrencyCode) {
				return nil
			}
		}
		return fmt.Errorf(
			"currency %s is not in the allowed list for this customer",
			docCurrency,
		)
	}
	return nil
}

func validateVendorCurrency(db *gorm.DB, companyID, vendorID uint, docCurrency, baseCurrency string) error {
	var vendor models.Vendor
	if err := db.Select("id", "currency_code", "currency_policy").
		Where("id = ? AND company_id = ?", vendorID, companyID).
		First(&vendor).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("load vendor for currency check: %w", err)
	}

	switch vendor.CurrencyPolicy {
	case models.VendorCurrencyPolicySingle, "":
		defaultCurr := vendor.CurrencyCode
		if defaultCurr == "" {
			defaultCurr = baseCurrency
		}
		if !currencyMatch(docCurrency, defaultCurr) {
			return fmt.Errorf(
				"vendor currency policy is 'single': document currency %s does not match the vendor's default currency %s",
				docCurrency, defaultCurr,
			)
		}

	case models.VendorCurrencyPolicyMultiAllowed:
		var allowed []models.VendorAllowedCurrency
		if err := db.Where("company_id = ? AND vendor_id = ?", companyID, vendorID).
			Find(&allowed).Error; err != nil {
			return fmt.Errorf("load vendor allowed currencies: %w", err)
		}
		if len(allowed) == 0 {
			return nil
		}
		defaultCurr := vendor.CurrencyCode
		if currencyMatch(docCurrency, defaultCurr) {
			return nil
		}
		for _, a := range allowed {
			if currencyMatch(docCurrency, a.CurrencyCode) {
				return nil
			}
		}
		return fmt.Errorf(
			"currency %s is not in the allowed list for this vendor",
			docCurrency,
		)
	}
	return nil
}

// currencyMatch compares two ISO 4217 codes case-insensitively.
// An empty reference means "no restriction" (always matches).
func currencyMatch(doc, reference string) bool {
	if reference == "" {
		return true
	}
	return strings.EqualFold(doc, reference)
}

// ── Customer allowed currencies ───────────────────────────────────────────────

// AddCustomerAllowedCurrency adds a currency to a customer's allowed list.
// Idempotent — silently succeeds if already present.
func AddCustomerAllowedCurrency(db *gorm.DB, companyID, customerID uint, currencyCode string) error {
	currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode))
	if currencyCode == "" || len(currencyCode) != 3 {
		return errors.New("currency code must be exactly 3 characters (ISO 4217)")
	}

	var count int64
	db.Model(&models.CustomerAllowedCurrency{}).
		Where("company_id = ? AND customer_id = ? AND currency_code = ?",
			companyID, customerID, currencyCode).Count(&count)
	if count > 0 {
		return nil // already present — idempotent
	}

	row := models.CustomerAllowedCurrency{
		CompanyID:    companyID,
		CustomerID:   customerID,
		CurrencyCode: currencyCode,
	}
	if err := db.Create(&row).Error; err != nil {
		return fmt.Errorf("add customer allowed currency: %w", err)
	}
	return nil
}

// RemoveCustomerAllowedCurrency removes a currency from a customer's allowed list.
func RemoveCustomerAllowedCurrency(db *gorm.DB, companyID, customerID uint, currencyCode string) error {
	result := db.Where("company_id = ? AND customer_id = ? AND currency_code = ?",
		companyID, customerID, strings.ToUpper(currencyCode)).
		Delete(&models.CustomerAllowedCurrency{})
	if result.Error != nil {
		return fmt.Errorf("remove customer allowed currency: %w", result.Error)
	}
	return nil
}

// ListCustomerAllowedCurrencies returns all allowed currencies for a customer.
func ListCustomerAllowedCurrencies(db *gorm.DB, companyID, customerID uint) ([]models.CustomerAllowedCurrency, error) {
	var rows []models.CustomerAllowedCurrency
	if err := db.Where("company_id = ? AND customer_id = ?", companyID, customerID).
		Order("currency_code asc").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list customer allowed currencies: %w", err)
	}
	return rows, nil
}

// SetCustomerCurrencyPolicy updates a customer's CurrencyPolicy.
func SetCustomerCurrencyPolicy(db *gorm.DB, companyID, customerID uint, policy models.CustomerCurrencyPolicy) error {
	if policy != models.CustomerCurrencyPolicySingle && policy != models.CustomerCurrencyPolicyMultiAllowed {
		return fmt.Errorf("unknown currency policy %q", policy)
	}
	if err := db.Model(&models.Customer{}).
		Where("id = ? AND company_id = ?", customerID, companyID).
		Update("currency_policy", string(policy)).Error; err != nil {
		return fmt.Errorf("set customer currency policy: %w", err)
	}
	return nil
}

// ── Vendor allowed currencies ─────────────────────────────────────────────────

// AddVendorAllowedCurrency adds a currency to a vendor's allowed list.
func AddVendorAllowedCurrency(db *gorm.DB, companyID, vendorID uint, currencyCode string) error {
	currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode))
	if currencyCode == "" || len(currencyCode) != 3 {
		return errors.New("currency code must be exactly 3 characters (ISO 4217)")
	}

	var count int64
	db.Model(&models.VendorAllowedCurrency{}).
		Where("company_id = ? AND vendor_id = ? AND currency_code = ?",
			companyID, vendorID, currencyCode).Count(&count)
	if count > 0 {
		return nil
	}

	row := models.VendorAllowedCurrency{
		CompanyID:    companyID,
		VendorID:     vendorID,
		CurrencyCode: currencyCode,
	}
	if err := db.Create(&row).Error; err != nil {
		return fmt.Errorf("add vendor allowed currency: %w", err)
	}
	return nil
}

// RemoveVendorAllowedCurrency removes a currency from a vendor's allowed list.
func RemoveVendorAllowedCurrency(db *gorm.DB, companyID, vendorID uint, currencyCode string) error {
	result := db.Where("company_id = ? AND vendor_id = ? AND currency_code = ?",
		companyID, vendorID, strings.ToUpper(currencyCode)).
		Delete(&models.VendorAllowedCurrency{})
	if result.Error != nil {
		return fmt.Errorf("remove vendor allowed currency: %w", result.Error)
	}
	return nil
}

// ListVendorAllowedCurrencies returns all allowed currencies for a vendor.
func ListVendorAllowedCurrencies(db *gorm.DB, companyID, vendorID uint) ([]models.VendorAllowedCurrency, error) {
	var rows []models.VendorAllowedCurrency
	if err := db.Where("company_id = ? AND vendor_id = ?", companyID, vendorID).
		Order("currency_code asc").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list vendor allowed currencies: %w", err)
	}
	return rows, nil
}

// SetVendorCurrencyPolicy updates a vendor's CurrencyPolicy.
func SetVendorCurrencyPolicy(db *gorm.DB, companyID, vendorID uint, policy models.VendorCurrencyPolicy) error {
	if policy != models.VendorCurrencyPolicySingle && policy != models.VendorCurrencyPolicyMultiAllowed {
		return fmt.Errorf("unknown currency policy %q", policy)
	}
	if err := db.Model(&models.Vendor{}).
		Where("id = ? AND company_id = ?", vendorID, companyID).
		Update("currency_policy", string(policy)).Error; err != nil {
		return fmt.Errorf("set vendor currency policy: %w", err)
	}
	return nil
}
