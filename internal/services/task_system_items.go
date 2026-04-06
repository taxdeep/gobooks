// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"gobooks/internal/models"

	"gorm.io/gorm"
)

// systemTaskItemDef describes a system-reserved item to be bootstrapped per company.
type systemTaskItemDef struct {
	SystemCode string
	Name       string
	Type       models.ProductServiceType
}

// systemTaskItems lists all system items that every company must have for the
// Task + Billable Expense module.
//
//   - TASK_LABOR  (service)        – used for task labor lines on invoices.
//   - TASK_REIM   (non_inventory)  – used for billable expense reimbursement lines.
var systemTaskItems = []systemTaskItemDef{
	{SystemCode: "TASK_LABOR", Name: "Task", Type: models.ProductServiceTypeService},
	{SystemCode: "TASK_REIM", Name: "Task Reim", Type: models.ProductServiceTypeNonInventory},
}

var (
	// ErrSystemItemTypeImmutable blocks changing the accounting type of a
	// company-scoped system item such as TASK_LABOR or TASK_REIM.
	ErrSystemItemTypeImmutable = errors.New("system items cannot change type")
	// ErrSystemItemCannotBeInactivated blocks disabling a company-scoped system
	// item that downstream task billing flows depend on.
	ErrSystemItemCannotBeInactivated = errors.New("system items cannot be marked inactive")
)

// EnsureSystemTaskItems creates the TASK_LABOR and TASK_REIM system items for
// the given company if they do not already exist.
//
// It is idempotent: running it twice for the same company is safe and produces
// no duplicate rows (guarded by the uq_product_services_company_system_code
// partial unique index added in migration 042).
//
// Revenue account selection: ProductService.RevenueAccountID is NOT NULL, so
// this function looks up a suitable revenue account for the company in order:
//  1. Any account with detail_type = 'service_revenue'
//  2. Any account with detail_type = 'operating_revenue'
//  3. Any account with root_type = 'revenue'
//
// If no revenue account exists for the company the function returns an error;
// this should not happen in practice because CreateDefaultAccountsForCompany
// always runs before this function in both company-init paths.
//
// tx may be an open transaction or the root DB handle; both are supported.
func EnsureSystemTaskItems(tx *gorm.DB, companyID uint) error {
	revenueAccountID, err := findRevenueAccountForCompany(tx, companyID)
	if err != nil {
		return fmt.Errorf("EnsureSystemTaskItems: find revenue account for company %d: %w", companyID, err)
	}

	for _, def := range systemTaskItems {
		code := def.SystemCode // local copy for pointer

		// Pre-check: skip if this system item already exists for the company.
		// We do not rely solely on the DB unique index here so that the function
		// works correctly in both PostgreSQL (production) and SQLite (tests).
		var existing models.ProductService
		err := tx.Where("company_id = ? AND system_code = ?", companyID, code).
			First(&existing).Error
		if err == nil {
			// Already exists — idempotent skip.
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return fmt.Errorf("EnsureSystemTaskItems: check %s for company %d: %w",
				def.SystemCode, companyID, err)
		}

		item := models.ProductService{
			CompanyID:        companyID,
			Name:             def.Name,
			Type:             def.Type,
			RevenueAccountID: revenueAccountID,
			IsActive:         true,
			IsSystem:         true,
			SystemCode:       &code,
		}
		item.ApplyTypeDefaults()

		if err := tx.Create(&item).Error; err != nil {
			return fmt.Errorf("EnsureSystemTaskItems: create %s for company %d: %w",
				def.SystemCode, companyID, err)
		}
	}
	return nil
}

// LookupSystemTaskItem returns the ProductService row for the given system_code
// within the company.  Used by the Draft Generator (Batch 4) to obtain the
// item ID without hard-coding a numeric ID.
func LookupSystemTaskItem(db *gorm.DB, companyID uint, systemCode string) (*models.ProductService, error) {
	var item models.ProductService
	err := db.Where("company_id = ? AND system_code = ? AND is_active = true", companyID, systemCode).
		First(&item).Error
	if err != nil {
		return nil, fmt.Errorf("LookupSystemTaskItem: %s not found for company %d: %w",
			systemCode, companyID, err)
	}
	return &item, nil
}

// ValidateSystemItemTypeChange blocks type mutations for company-scoped system
// items. Non-system items are unaffected.
func ValidateSystemItemTypeChange(existing models.ProductService, desiredType models.ProductServiceType) error {
	if existing.IsSystem && existing.Type != desiredType {
		return ErrSystemItemTypeImmutable
	}
	return nil
}

// ValidateSystemItemInactivation blocks deactivation of company-scoped system
// items. Non-system items are unaffected.
func ValidateSystemItemInactivation(item models.ProductService) error {
	if item.IsSystem {
		return ErrSystemItemCannotBeInactivated
	}
	return nil
}

// findRevenueAccountForCompany returns the ID of a suitable revenue GL account
// for the company, using a priority-order fallback strategy.
func findRevenueAccountForCompany(tx *gorm.DB, companyID uint) (uint, error) {
	// Priority 1: service_revenue
	if id, ok := findAccountByDetailType(tx, companyID, string(models.DetailServiceRevenue)); ok {
		return id, nil
	}
	// Priority 2: operating_revenue
	if id, ok := findAccountByDetailType(tx, companyID, string(models.DetailOperatingRevenue)); ok {
		return id, nil
	}
	// Priority 3: any revenue root
	var acct models.Account
	err := tx.Select("id").
		Where("company_id = ? AND root_account_type = ? AND is_active = true", companyID, string(models.RootRevenue)).
		First(&acct).Error
	if err != nil {
		return 0, fmt.Errorf("no active revenue account found: %w", err)
	}
	return acct.ID, nil
}

func findAccountByDetailType(tx *gorm.DB, companyID uint, detailType string) (uint, bool) {
	var acct models.Account
	err := tx.Select("id").
		Where("company_id = ? AND detail_account_type = ? AND is_active = true", companyID, detailType).
		First(&acct).Error
	if err != nil {
		return 0, false
	}
	return acct.ID, true
}
