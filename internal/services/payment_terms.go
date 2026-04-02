// 遵循project_guide.md
package services

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Queries ──────────────────────────────────────────────────────────────────

// ListPaymentTerms returns all payment terms for a company ordered by sort_order.
// When activeOnly is true, only active terms are returned.
func ListPaymentTerms(db *gorm.DB, companyID uint, activeOnly bool) ([]models.PaymentTerm, error) {
	q := db.Where("company_id = ?", companyID)
	if activeOnly {
		q = q.Where("is_active = true")
	}
	var terms []models.PaymentTerm
	err := q.Order("sort_order asc, id asc").Find(&terms).Error
	return terms, err
}

// GetPaymentTermByCode looks up a term by its code (case-insensitive).
func GetPaymentTermByCode(db *gorm.DB, companyID uint, code string) (models.PaymentTerm, error) {
	var pt models.PaymentTerm
	err := db.Where("company_id = ? AND lower(code) = lower(?)", companyID, strings.TrimSpace(code)).
		First(&pt).Error
	return pt, err
}

// GetDefaultPaymentTerm returns the company's default payment term.
// Falls back to the first active term if no default is set.
func GetDefaultPaymentTerm(db *gorm.DB, companyID uint) (models.PaymentTerm, error) {
	var pt models.PaymentTerm
	err := db.Where("company_id = ? AND is_default = true", companyID).First(&pt).Error
	if err == nil {
		return pt, nil
	}
	// Fallback: first active term ordered by sort_order.
	err = db.Where("company_id = ? AND is_active = true", companyID).
		Order("sort_order asc, id asc").First(&pt).Error
	return pt, err
}

// BuildPaymentTermSnapshot retrieves a term by code and returns its snapshot.
// If the term is not found it returns an error.
func BuildPaymentTermSnapshot(db *gorm.DB, companyID uint, code string) (models.PaymentTermSnapshot, error) {
	pt, err := GetPaymentTermByCode(db, companyID, code)
	if err != nil {
		return models.PaymentTermSnapshot{}, fmt.Errorf("payment term %q not found for this company", code)
	}
	return models.BuildSnapshot(pt), nil
}

// ResolveInitialTermCode returns the term code to use when creating a new document.
// Priority: contactCode (customer/vendor default) → company default → "".
func ResolveInitialTermCode(db *gorm.DB, companyID uint, contactCode string) string {
	if strings.TrimSpace(contactCode) != "" {
		// Validate the contact's term still exists and is active.
		pt, err := GetPaymentTermByCode(db, companyID, contactCode)
		if err == nil && pt.IsActive {
			return pt.Code
		}
	}
	def, err := GetDefaultPaymentTerm(db, companyID)
	if err == nil {
		return def.Code
	}
	return ""
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreatePaymentTermInput carries validated fields for creating a new PaymentTerm.
type CreatePaymentTermInput struct {
	CompanyID    uint
	Code         string
	Description  string
	DiscountDays int
	DiscountPct  decimal.Decimal
	NetDays      int
	IsDefault    bool
	SortOrder    int
}

// CreatePaymentTerm creates a new payment term after enforcing business rules.
// If IsDefault is true, it clears the existing default for the company first.
func CreatePaymentTerm(db *gorm.DB, in CreatePaymentTermInput) (models.PaymentTerm, error) {
	code := strings.TrimSpace(in.Code)
	description := strings.TrimSpace(in.Description)

	if err := models.ValidatePaymentTerm(code, description, in.DiscountDays, in.DiscountPct, in.NetDays); err != nil {
		return models.PaymentTerm{}, err
	}

	// Code uniqueness check (case-insensitive).
	var count int64
	db.Model(&models.PaymentTerm{}).
		Where("company_id = ? AND lower(code) = lower(?)", in.CompanyID, code).
		Count(&count)
	if count > 0 {
		return models.PaymentTerm{}, fmt.Errorf("a payment term with code %q already exists", code)
	}

	pt := models.PaymentTerm{
		CompanyID:    in.CompanyID,
		Code:         code,
		Description:  description,
		DiscountDays: in.DiscountDays,
		DiscountPct:  in.DiscountPct,
		NetDays:      in.NetDays,
		IsDefault:    in.IsDefault,
		IsActive:     true,
		SortOrder:    in.SortOrder,
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if in.IsDefault {
			if err := clearDefault(tx, in.CompanyID, 0); err != nil {
				return err
			}
		}
		return tx.Create(&pt).Error
	})
	return pt, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdatePaymentTermInput carries fields that may be changed on an existing term.
// Code is immutable once the term has been referenced.
type UpdatePaymentTermInput struct {
	Description  string
	DiscountDays int
	DiscountPct  decimal.Decimal
	NetDays      int
	SortOrder    int
}

// UpdatePaymentTerm updates an existing term.
// Code cannot be changed (immutable once referenced).
func UpdatePaymentTerm(db *gorm.DB, companyID, id uint, in UpdatePaymentTermInput) (models.PaymentTerm, error) {
	var pt models.PaymentTerm
	if err := db.Where("id = ? AND company_id = ?", id, companyID).First(&pt).Error; err != nil {
		return pt, fmt.Errorf("payment term not found")
	}
	description := strings.TrimSpace(in.Description)
	if err := models.ValidatePaymentTerm(pt.Code, description, in.DiscountDays, in.DiscountPct, in.NetDays); err != nil {
		return pt, err
	}
	err := db.Model(&pt).Updates(map[string]any{
		"description":   description,
		"discount_days": in.DiscountDays,
		"discount_pct":  in.DiscountPct,
		"net_days":      in.NetDays,
		"sort_order":    in.SortOrder,
	}).Error
	return pt, err
}

// ── Set default ───────────────────────────────────────────────────────────────

// SetDefaultPaymentTerm marks one term as default and clears all others for the company.
func SetDefaultPaymentTerm(db *gorm.DB, companyID, id uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := clearDefault(tx, companyID, id); err != nil {
			return err
		}
		return tx.Model(&models.PaymentTerm{}).
			Where("id = ? AND company_id = ?", id, companyID).
			Update("is_default", true).Error
	})
}

func clearDefault(db *gorm.DB, companyID, exceptID uint) error {
	q := db.Model(&models.PaymentTerm{}).Where("company_id = ?", companyID)
	if exceptID != 0 {
		q = q.Where("id <> ?", exceptID)
	}
	return q.Update("is_default", false).Error
}

// ── Toggle active ─────────────────────────────────────────────────────────────

// TogglePaymentTermActive flips the is_active flag of a term.
// A default term can still be deactivated (caller's responsibility).
func TogglePaymentTermActive(db *gorm.DB, companyID, id uint) (models.PaymentTerm, error) {
	var pt models.PaymentTerm
	if err := db.Where("id = ? AND company_id = ?", id, companyID).First(&pt).Error; err != nil {
		return pt, fmt.Errorf("payment term not found")
	}
	newActive := !pt.IsActive
	err := db.Model(&pt).Update("is_active", newActive).Error
	pt.IsActive = newActive
	return pt, err
}

// ── Delete ────────────────────────────────────────────────────────────────────

// DeletePaymentTerm deletes a term.
// Returns an error if the term is referenced by any customer, vendor, invoice, or bill.
func DeletePaymentTerm(db *gorm.DB, companyID, id uint) error {
	var pt models.PaymentTerm
	if err := db.Where("id = ? AND company_id = ?", id, companyID).First(&pt).Error; err != nil {
		return fmt.Errorf("payment term not found")
	}

	// Check references.
	type refCheck struct {
		table  string
		column string
	}
	refs := []refCheck{
		{"customers", "default_payment_term_code"},
		{"vendors", "default_payment_term_code"},
		{"invoices", "term_code"},
		{"bills", "term_code"},
	}
	for _, r := range refs {
		var count int64
		db.Raw(fmt.Sprintf(
			"SELECT COUNT(*) FROM %s WHERE company_id = ? AND lower(%s) = lower(?)",
			r.table, r.column,
		), companyID, pt.Code).Scan(&count)
		if count > 0 {
			return fmt.Errorf("cannot delete: payment term %q is in use by %d %s record(s)", pt.Code, count, r.table)
		}
	}

	return db.Delete(&pt).Error
}
