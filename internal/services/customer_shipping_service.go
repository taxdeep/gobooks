// 遵循project_guide.md
package services

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// customer_shipping_service.go — CRUD for the Customer multi-shipping-address
// catalogue introduced in migration 088. The Customer's billing address stays
// on the Customer row itself; this table stores additional ship-to locations.
//
// Single-default invariant: at most one shipping address row per customer has
// is_default=true. The service enforces it on every Add / Update / SetDefault
// call by clearing peer defaults inside the same transaction.

// CustomerShippingAddressInput holds the editable fields for create / update.
type CustomerShippingAddressInput struct {
	Label          string
	AddrStreet1    string
	AddrStreet2    string
	AddrCity       string
	AddrProvince   string
	AddrPostalCode string
	AddrCountry    string
	IsDefault      bool
}

// ListCustomerShippingAddresses returns all shipping addresses for a customer,
// sorted with the default first (so callers can pre-select index 0).
func ListCustomerShippingAddresses(db *gorm.DB, companyID, customerID uint) ([]models.CustomerShippingAddress, error) {
	if !customerBelongsToCompany(db, companyID, customerID) {
		return nil, errors.New("customer not found")
	}
	var rows []models.CustomerShippingAddress
	err := db.Where("customer_id = ?", customerID).
		Order("is_default DESC, id ASC").
		Find(&rows).Error
	return rows, err
}

// AddCustomerShippingAddress inserts a new shipping address. When in.IsDefault
// is true, all peer rows for the customer are demoted to is_default=false in
// the same transaction so the single-default invariant holds.
func AddCustomerShippingAddress(db *gorm.DB, companyID, customerID uint, in CustomerShippingAddressInput) (*models.CustomerShippingAddress, error) {
	in = sanitiseShippingInput(in)
	if !shippingHasAnyAddressLine(in) {
		return nil, errors.New("at least one address line is required")
	}
	if !customerBelongsToCompany(db, companyID, customerID) {
		return nil, errors.New("customer not found")
	}
	row := models.CustomerShippingAddress{
		CustomerID:     customerID,
		Label:          in.Label,
		AddrStreet1:    in.AddrStreet1,
		AddrStreet2:    in.AddrStreet2,
		AddrCity:       in.AddrCity,
		AddrProvince:   in.AddrProvince,
		AddrPostalCode: in.AddrPostalCode,
		AddrCountry:    in.AddrCountry,
		IsDefault:      in.IsDefault,
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if in.IsDefault {
			if err := clearPeerDefaults(tx, customerID, 0); err != nil {
				return err
			}
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// DeleteCustomerShippingAddress removes a shipping address by ID. No-op for
// non-existent rows. Verifies the row belongs to a customer in companyID.
func DeleteCustomerShippingAddress(db *gorm.DB, companyID, customerID, addrID uint) error {
	if !customerBelongsToCompany(db, companyID, customerID) {
		return errors.New("customer not found")
	}
	res := db.Where("id = ? AND customer_id = ?", addrID, customerID).
		Delete(&models.CustomerShippingAddress{})
	return res.Error
}

// SetDefaultCustomerShippingAddress promotes one address to default and demotes
// all peers for the same customer in the same transaction.
func SetDefaultCustomerShippingAddress(db *gorm.DB, companyID, customerID, addrID uint) error {
	if !customerBelongsToCompany(db, companyID, customerID) {
		return errors.New("customer not found")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var target models.CustomerShippingAddress
		if err := tx.Where("id = ? AND customer_id = ?", addrID, customerID).First(&target).Error; err != nil {
			return err
		}
		if err := clearPeerDefaults(tx, customerID, addrID); err != nil {
			return err
		}
		return tx.Model(&target).Update("is_default", true).Error
	})
}

func clearPeerDefaults(tx *gorm.DB, customerID, exceptID uint) error {
	q := tx.Model(&models.CustomerShippingAddress{}).
		Where("customer_id = ? AND is_default = ?", customerID, true)
	if exceptID != 0 {
		q = q.Where("id <> ?", exceptID)
	}
	return q.Update("is_default", false).Error
}

func customerBelongsToCompany(db *gorm.DB, companyID, customerID uint) bool {
	var n int64
	db.Model(&models.Customer{}).Where("id = ? AND company_id = ?", customerID, companyID).Count(&n)
	return n > 0
}

// shippingHasAnyAddressLine returns true when at least one address line is
// non-empty. A blank label is allowed (defaults to empty), but an entirely
// empty address row is rejected.
func shippingHasAnyAddressLine(in CustomerShippingAddressInput) bool {
	return in.AddrStreet1 != "" || in.AddrStreet2 != "" || in.AddrCity != "" ||
		in.AddrProvince != "" || in.AddrPostalCode != "" || in.AddrCountry != ""
}

func sanitiseShippingInput(in CustomerShippingAddressInput) CustomerShippingAddressInput {
	in.Label = strings.TrimSpace(in.Label)
	in.AddrStreet1 = strings.TrimSpace(in.AddrStreet1)
	in.AddrStreet2 = strings.TrimSpace(in.AddrStreet2)
	in.AddrCity = strings.TrimSpace(in.AddrCity)
	in.AddrProvince = strings.TrimSpace(in.AddrProvince)
	in.AddrPostalCode = strings.TrimSpace(in.AddrPostalCode)
	in.AddrCountry = strings.TrimSpace(in.AddrCountry)
	return in
}
