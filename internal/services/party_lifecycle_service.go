// 遵循project_guide.md
package services

// party_lifecycle_service.go — delete / deactivate / reactivate helpers for
// Customer and Vendor. Separates the "can I safely delete this party?"
// question from the handler layer so the rule is enforced once.
//
// Rule: a party can be fully deleted ONLY when no referencing records exist
// across any related table. If any document (invoice, bill, quote, PO, etc.)
// references the party, the caller must deactivate instead — preserving the
// history while hiding the party from day-to-day workflows.

import (
	"errors"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ErrPartyHasRecords is returned by DeleteCustomer / DeleteVendor when the
// party is still referenced by at least one business document.
var ErrPartyHasRecords = errors.New("party has related records and cannot be deleted")

// customerRecordTables lists every table that may reference customers.id via
// a company-scoped customer_id column. Keep in lock-step with the set of
// AR-side document types the app ships.
var customerRecordTables = []string{
	"invoices",
	"quotes",
	"sales_orders",
	"customer_deposits",
	"customer_receipts",
	"ar_returns",
	"ar_refunds",
	"ar_write_offs",
	"credit_notes",
	"customer_credits",
}

// vendorRecordTables lists every table that may reference vendors.id via
// a company-scoped vendor_id column. expenses is included because expenses
// optionally reference a vendor (VendorID *uint); the WHERE clause naturally
// excludes rows where vendor_id IS NULL.
var vendorRecordTables = []string{
	"bills",
	"purchase_orders",
	"expenses",
	"vendor_prepayments",
	"vendor_returns",
	"vendor_credit_notes",
	"vendor_refunds",
}

// CustomerHasRecords reports whether any AR document references this customer.
// Returns on the first hit — cheaper than summing counts across all tables.
func CustomerHasRecords(db *gorm.DB, companyID, customerID uint) (bool, error) {
	return partyHasRecords(db, companyID, customerID, "customer_id", customerRecordTables)
}

// VendorHasRecords reports whether any AP document references this vendor.
func VendorHasRecords(db *gorm.DB, companyID, vendorID uint) (bool, error) {
	return partyHasRecords(db, companyID, vendorID, "vendor_id", vendorRecordTables)
}

func partyHasRecords(db *gorm.DB, companyID, partyID uint, fkColumn string, tables []string) (bool, error) {
	for _, table := range tables {
		var exists bool
		query := "SELECT EXISTS(SELECT 1 FROM " + table +
			" WHERE company_id = ? AND " + fkColumn + " = ?)"
		if err := db.Raw(query, companyID, partyID).Scan(&exists).Error; err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

// SetCustomerActive flips the IsActive flag. Safe to call multiple times.
func SetCustomerActive(db *gorm.DB, companyID, customerID uint, active bool) error {
	return db.Model(&models.Customer{}).
		Where("id = ? AND company_id = ?", customerID, companyID).
		Update("is_active", active).Error
}

// SetVendorActive flips the IsActive flag.
func SetVendorActive(db *gorm.DB, companyID, vendorID uint, active bool) error {
	return db.Model(&models.Vendor{}).
		Where("id = ? AND company_id = ?", vendorID, companyID).
		Update("is_active", active).Error
}

// DeleteCustomer removes a customer row IFF no documents reference it.
// Returns ErrPartyHasRecords when deletion is not permitted — callers should
// fall back to SetCustomerActive(false) and surface a message to the user.
func DeleteCustomer(db *gorm.DB, companyID, customerID uint) error {
	hasRecords, err := CustomerHasRecords(db, companyID, customerID)
	if err != nil {
		return err
	}
	if hasRecords {
		return ErrPartyHasRecords
	}
	// Also clean up the customer-scoped ancillary data that doesn't itself
	// constitute a "business record" (allowed currencies list, etc.).
	if err := db.Where("customer_id = ?", customerID).
		Delete(&models.CustomerAllowedCurrency{}).Error; err != nil {
		return err
	}
	return db.Where("id = ? AND company_id = ?", customerID, companyID).
		Delete(&models.Customer{}).Error
}

// DeleteVendor removes a vendor row IFF no documents reference it.
func DeleteVendor(db *gorm.DB, companyID, vendorID uint) error {
	hasRecords, err := VendorHasRecords(db, companyID, vendorID)
	if err != nil {
		return err
	}
	if hasRecords {
		return ErrPartyHasRecords
	}
	return db.Where("id = ? AND company_id = ?", vendorID, companyID).
		Delete(&models.Vendor{}).Error
}
