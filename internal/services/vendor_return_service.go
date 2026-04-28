// 遵循project_guide.md
package services

// vendor_return_service.go — VendorReturn: returning goods/services to vendor.
//
// VendorReturn is a pure business object. No journal entry is created.
// Accounting adjustment is handled by a linked VendorCreditNote.
//
// State machine:
//   draft → submitted → approved → processed
//          ↘ cancelled (from draft or submitted)

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrVendorReturnNotFound      = errors.New("vendor return not found")
	ErrVendorReturnInvalidStatus = errors.New("action not allowed in current vendor return status")
)

// ── Input types ───────────────────────────────────────────────────────────────

// VendorReturnInput holds all data needed to create or update a VendorReturn.
type VendorReturnInput struct {
	VendorID     uint
	BillID       *uint
	ReturnDate   time.Time
	CurrencyCode string
	Amount       decimal.Decimal
	Reason       string
	Memo         string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func nextVendorReturnNumber(db *gorm.DB, companyID uint) string {
	var last models.VendorReturn
	db.Where("company_id = ?", companyID).Order("id desc").Select("return_number").First(&last)
	return NextDocumentNumber(last.ReturnNumber, "VRT-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateVendorReturn creates a new draft vendor return.
func CreateVendorReturn(db *gorm.DB, companyID uint, in VendorReturnInput) (*models.VendorReturn, error) {
	if in.VendorID == 0 {
		return nil, errors.New("vendor is required")
	}

	vr := models.VendorReturn{
		CompanyID:    companyID,
		VendorID:     in.VendorID,
		BillID:       in.BillID,
		ReturnNumber: nextVendorReturnNumber(db, companyID),
		Status:       models.VendorReturnStatusDraft,
		ReturnDate:   in.ReturnDate,
		CurrencyCode: in.CurrencyCode,
		Amount:       in.Amount.Round(2),
		Reason:       in.Reason,
		Memo:         in.Memo,
	}

	if err := db.Create(&vr).Error; err != nil {
		return nil, fmt.Errorf("create vendor return: %w", err)
	}
	return &vr, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetVendorReturn loads a return with vendor for the given company.
func GetVendorReturn(db *gorm.DB, companyID, vrID uint) (*models.VendorReturn, error) {
	var vr models.VendorReturn
	err := db.Preload("Vendor").Preload("Bill").
		Where("id = ? AND company_id = ?", vrID, companyID).First(&vr).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVendorReturnNotFound
	}
	return &vr, err
}

// VendorReturnListFilter bundles the optional list-page filters.
type VendorReturnListFilter struct {
	Status   string     // empty = all statuses
	VendorID uint       // 0 = all vendors
	DateFrom *time.Time // nil = no lower bound on return_date
	DateTo   *time.Time // nil = no upper bound on return_date
}

// ListVendorReturns returns vendor returns for a company, newest first.
func ListVendorReturns(db *gorm.DB, companyID uint, f VendorReturnListFilter) ([]models.VendorReturn, error) {
	q := db.Preload("Vendor").Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.VendorID > 0 {
		q = q.Where("vendor_id = ?", f.VendorID)
	}
	if f.DateFrom != nil {
		q = q.Where("return_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("return_date <= ?", *f.DateTo)
	}
	var vrs []models.VendorReturn
	err := q.Order("id desc").Find(&vrs).Error
	return vrs, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateVendorReturn updates a draft vendor return.
func UpdateVendorReturn(db *gorm.DB, companyID, vrID uint, in VendorReturnInput) (*models.VendorReturn, error) {
	var vr models.VendorReturn
	if err := db.Where("id = ? AND company_id = ?", vrID, companyID).First(&vr).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVendorReturnNotFound
		}
		return nil, err
	}
	if vr.Status != models.VendorReturnStatusDraft {
		return nil, fmt.Errorf("%w: only draft returns may be edited", ErrVendorReturnInvalidStatus)
	}

	updates := map[string]any{
		"vendor_id":     in.VendorID,
		"bill_id":       in.BillID,
		"return_date":   in.ReturnDate,
		"currency_code": in.CurrencyCode,
		"amount":        in.Amount.Round(2),
		"reason":        in.Reason,
		"memo":          in.Memo,
	}
	if err := db.Model(&vr).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update vendor return: %w", err)
	}
	return &vr, nil
}

// ── Status transitions ────────────────────────────────────────────────────────

// SubmitVendorReturn transitions draft → submitted.
func SubmitVendorReturn(db *gorm.DB, companyID, vrID uint) error {
	return vrTransition(db, companyID, vrID, models.VendorReturnStatusDraft, models.VendorReturnStatusSubmitted)
}

// ApproveVendorReturn transitions submitted → approved.
func ApproveVendorReturn(db *gorm.DB, companyID, vrID uint) error {
	return vrTransition(db, companyID, vrID, models.VendorReturnStatusSubmitted, models.VendorReturnStatusApproved)
}

// ProcessVendorReturn transitions approved → processed.
func ProcessVendorReturn(db *gorm.DB, companyID, vrID uint) error {
	return vrTransition(db, companyID, vrID, models.VendorReturnStatusApproved, models.VendorReturnStatusProcessed)
}

// CancelVendorReturn cancels a draft or submitted return.
func CancelVendorReturn(db *gorm.DB, companyID, vrID uint) error {
	var vr models.VendorReturn
	if err := db.Where("id = ? AND company_id = ?", vrID, companyID).First(&vr).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVendorReturnNotFound
		}
		return err
	}
	if vr.Status != models.VendorReturnStatusDraft && vr.Status != models.VendorReturnStatusSubmitted {
		return fmt.Errorf("%w: only draft or submitted returns can be cancelled", ErrVendorReturnInvalidStatus)
	}
	return db.Model(&vr).Update("status", models.VendorReturnStatusCancelled).Error
}

func vrTransition(db *gorm.DB, companyID, vrID uint, from, to models.VendorReturnStatus) error {
	var vr models.VendorReturn
	if err := db.Where("id = ? AND company_id = ?", vrID, companyID).First(&vr).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVendorReturnNotFound
		}
		return err
	}
	if vr.Status != from {
		return fmt.Errorf("%w: expected %s, got %s", ErrVendorReturnInvalidStatus, from, vr.Status)
	}
	return db.Model(&vr).Update("status", to).Error
}
