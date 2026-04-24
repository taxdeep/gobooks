// 遵循project_guide.md
package web

import (
	"time"

	"gorm.io/gorm"

	"gobooks/internal/models"
)

// parseListDateRange parses the standard `from` / `to` query-string
// inputs every list page exposes (YYYY-MM-DD). Empty / unparseable
// inputs return nil pointers (= "no bound"); valid inputs return
// inclusive bounds — the upper bound is bumped to end-of-day so a row
// dated `to` itself isn't excluded by a < comparison.
//
// Centralised because every list handler does the same parse + the
// same end-of-day bump; copy-pasting it 8 times invited drift.
func parseListDateRange(fromStr, toStr string) (*time.Time, *time.Time) {
	var dateFrom, dateTo *time.Time
	if fromStr != "" {
		if t, err := time.Parse("2006-01-02", fromStr); err == nil {
			dateFrom = &t
		}
	}
	if toStr != "" {
		if t, err := time.Parse("2006-01-02", toStr); err == nil {
			end := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
			dateTo = &end
		}
	}
	return dateFrom, dateTo
}

// lookupCustomerName resolves a customer ID to its display name for
// SmartPicker echo — when the operator filters the list by customer,
// the picker needs the human-readable name to show in its input. One
// indexed-PK lookup, only when a filter is active, scoped to the
// active company so it can't leak cross-tenant.
//
// Returns "" on any failure (zero ID, missing row, db error). Callers
// pass the empty string straight through to the SmartPicker which
// renders an empty echo — no error path needed in the handler.
func lookupCustomerName(db *gorm.DB, companyID, customerID uint) string {
	if customerID == 0 {
		return ""
	}
	var cust models.Customer
	if err := db.Select("name").Where("id = ? AND company_id = ?", customerID, companyID).First(&cust).Error; err != nil {
		return ""
	}
	return cust.Name
}

// lookupVendorName mirrors lookupCustomerName for vendor-keyed filters
// (Bills, POs, Vendor Credit Notes, etc.). Same fail-quiet contract.
func lookupVendorName(db *gorm.DB, companyID, vendorID uint) string {
	if vendorID == 0 {
		return ""
	}
	var vend models.Vendor
	if err := db.Select("name").Where("id = ? AND company_id = ?", vendorID, companyID).First(&vend).Error; err != nil {
		return ""
	}
	return vend.Name
}
