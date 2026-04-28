// 遵循project_guide.md
package services

// ar_return_service.go — ARReturn: customer return / cancellation business-fact object.
//
// ARReturn does NOT create journal entries. It is a pure business-layer object.
// Accounting consequences (CreditNote, ARRefund) must be created explicitly
// by the user after the return is approved.
//
// State machine:
//   draft → submitted → approved → processed
//                  ↘ rejected
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
	ErrReturnNotFound      = errors.New("AR return not found")
	ErrReturnInvalidStatus = errors.New("action not allowed in current return status")
)

// ── Input types ───────────────────────────────────────────────────────────────

// ARReturnInput holds all data needed to create or update an ARReturn.
type ARReturnInput struct {
	CustomerID   uint
	InvoiceID    uint
	ReturnDate   time.Time
	Reason       models.ARReturnReason
	Description  string
	CurrencyCode string
	ReturnAmount decimal.Decimal
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// nextReturnNumber derives the next return document number for a company.
func nextReturnNumber(db *gorm.DB, companyID uint) string {
	var last models.ARReturn
	db.Where("company_id = ?", companyID).
		Order("id desc").
		Select("return_number").
		First(&last)
	return NextDocumentNumber(last.ReturnNumber, "RTN-0001")
}

// ── Create ────────────────────────────────────────────────────────────────────

// CreateARReturn creates a new draft return. No JE is generated.
func CreateARReturn(db *gorm.DB, companyID uint, in ARReturnInput) (*models.ARReturn, error) {
	if in.CustomerID == 0 {
		return nil, errors.New("customer is required")
	}
	if in.InvoiceID == 0 {
		return nil, errors.New("originating invoice is required")
	}
	if !in.ReturnAmount.IsPositive() {
		return nil, errors.New("return amount must be positive")
	}

	reason := in.Reason
	if reason == "" {
		reason = models.ARReturnReasonOther
	}

	ret := models.ARReturn{
		CompanyID:    companyID,
		CustomerID:   in.CustomerID,
		InvoiceID:    in.InvoiceID,
		ReturnNumber: nextReturnNumber(db, companyID),
		Status:       models.ARReturnStatusDraft,
		ReturnDate:   in.ReturnDate,
		Reason:       reason,
		Description:  in.Description,
		CurrencyCode: in.CurrencyCode,
		ReturnAmount: in.ReturnAmount.Round(2),
	}

	if err := db.Create(&ret).Error; err != nil {
		return nil, fmt.Errorf("create AR return: %w", err)
	}
	return &ret, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetARReturn loads a return with customer and invoice for the given company.
func GetARReturn(db *gorm.DB, companyID, returnID uint) (*models.ARReturn, error) {
	var ret models.ARReturn
	err := db.Preload("Customer").Preload("Invoice").
		Where("id = ? AND company_id = ?", returnID, companyID).
		First(&ret).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrReturnNotFound
	}
	return &ret, err
}

// ARReturnListFilter bundles the optional list-page filters. Mirrors
// the SalesOrderListFilter shape so the AR list pages stay aligned.
type ARReturnListFilter struct {
	Status     string     // empty = all statuses
	CustomerID uint       // 0 = all customers
	DateFrom   *time.Time // nil = no lower bound on return_date
	DateTo     *time.Time // nil = no upper bound on return_date
}

// ListARReturns returns returns for a company, newest first.
func ListARReturns(db *gorm.DB, companyID uint, f ARReturnListFilter) ([]models.ARReturn, error) {
	q := db.Preload("Customer").Where("company_id = ?", companyID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.CustomerID > 0 {
		q = q.Where("customer_id = ?", f.CustomerID)
	}
	if f.DateFrom != nil {
		q = q.Where("return_date >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		q = q.Where("return_date <= ?", *f.DateTo)
	}
	var returns []models.ARReturn
	err := q.Order("id desc").Find(&returns).Error
	return returns, err
}

// ── Update ────────────────────────────────────────────────────────────────────

// UpdateARReturn updates a draft return.
func UpdateARReturn(db *gorm.DB, companyID, returnID uint, in ARReturnInput) (*models.ARReturn, error) {
	var ret models.ARReturn
	if err := db.Where("id = ? AND company_id = ?", returnID, companyID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReturnNotFound
		}
		return nil, err
	}
	if ret.Status != models.ARReturnStatusDraft {
		return nil, fmt.Errorf("%w: only draft returns may be edited", ErrReturnInvalidStatus)
	}

	reason := in.Reason
	if reason == "" {
		reason = models.ARReturnReasonOther
	}

	updates := map[string]any{
		"customer_id":   in.CustomerID,
		"invoice_id":    in.InvoiceID,
		"return_date":   in.ReturnDate,
		"reason":        string(reason),
		"description":   in.Description,
		"currency_code": in.CurrencyCode,
		"return_amount": in.ReturnAmount.Round(2),
	}
	if err := db.Model(&ret).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update AR return: %w", err)
	}
	return &ret, nil
}

// ── Status transitions ────────────────────────────────────────────────────────

// SubmitARReturn transitions a draft return to submitted.
func SubmitARReturn(db *gorm.DB, companyID, returnID uint) error {
	return returnTransition(db, companyID, returnID,
		models.ARReturnStatusDraft, models.ARReturnStatusSubmitted, nil)
}

// ApproveARReturn transitions a submitted return to approved.
func ApproveARReturn(db *gorm.DB, companyID, returnID uint, actor string) error {
	now := time.Now()
	return returnTransition(db, companyID, returnID,
		models.ARReturnStatusSubmitted, models.ARReturnStatusApproved,
		map[string]any{"approved_at": &now, "approved_by": actor})
}

// RejectARReturn transitions a submitted return to rejected.
func RejectARReturn(db *gorm.DB, companyID, returnID uint) error {
	return returnTransition(db, companyID, returnID,
		models.ARReturnStatusSubmitted, models.ARReturnStatusRejected, nil)
}

// CancelARReturn cancels a draft or submitted return.
func CancelARReturn(db *gorm.DB, companyID, returnID uint) error {
	var ret models.ARReturn
	if err := db.Where("id = ? AND company_id = ?", returnID, companyID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReturnNotFound
		}
		return err
	}
	if ret.Status != models.ARReturnStatusDraft && ret.Status != models.ARReturnStatusSubmitted {
		return fmt.Errorf("%w: only draft or submitted returns can be cancelled", ErrReturnInvalidStatus)
	}
	return db.Model(&ret).Update("status", models.ARReturnStatusCancelled).Error
}

// MarkReturnProcessed marks an approved return as processed (credit/refund issued).
func MarkReturnProcessed(db *gorm.DB, companyID, returnID uint) error {
	now := time.Now()
	return returnTransition(db, companyID, returnID,
		models.ARReturnStatusApproved, models.ARReturnStatusProcessed,
		map[string]any{"processed_at": &now})
}

// returnTransition is a generic status transition helper.
func returnTransition(db *gorm.DB, companyID, returnID uint,
	from, to models.ARReturnStatus, extraUpdates map[string]any) error {
	var ret models.ARReturn
	if err := db.Where("id = ? AND company_id = ?", returnID, companyID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReturnNotFound
		}
		return err
	}
	if ret.Status != from {
		return fmt.Errorf("%w: expected status %s, got %s", ErrReturnInvalidStatus, from, ret.Status)
	}
	updates := map[string]any{"status": string(to)}
	for k, v := range extraUpdates {
		updates[k] = v
	}
	return db.Model(&ret).Updates(updates).Error
}
