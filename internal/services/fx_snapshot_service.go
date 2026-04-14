// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// CreateFXSnapshotInput carries all required fields for a new immutable FX snapshot.
type CreateFXSnapshotInput struct {
	CompanyID     uint
	FromCurrency  string
	ToCurrency    string
	Rate          decimal.Decimal
	EffectiveDate time.Time
	RateType      models.FXRateType
	QuoteBasis    models.FXQuoteBasis
	PostingReason models.FXPostingReason
	RateCategory  models.FXRateCategory
	Source        string
}

// CreateFXSnapshot persists an immutable FX rate snapshot.
//
// Rules:
//   - Rate must be > 0.
//   - FromCurrency and ToCurrency must be 3-character ISO 4217 codes.
//   - EffectiveDate must not be zero.
//   - IsImmutable is always set to true; callers may not override it.
//
// This function must be called inside the same DB transaction as the posting
// event it documents, so that the snapshot and the JE/settlement are committed
// atomically.
func CreateFXSnapshot(db *gorm.DB, input CreateFXSnapshotInput) (*models.FXSnapshot, error) {
	if !input.Rate.GreaterThan(decimal.Zero) {
		return nil, fmt.Errorf("fx snapshot: rate must be greater than 0, got %s", input.Rate)
	}
	if len(input.FromCurrency) != 3 {
		return nil, fmt.Errorf("fx snapshot: from_currency must be a 3-character ISO 4217 code, got %q", input.FromCurrency)
	}
	if len(input.ToCurrency) != 3 {
		return nil, fmt.Errorf("fx snapshot: to_currency must be a 3-character ISO 4217 code, got %q", input.ToCurrency)
	}
	if input.EffectiveDate.IsZero() {
		return nil, fmt.Errorf("fx snapshot: effective_date is required")
	}
	if input.CompanyID == 0 {
		return nil, fmt.Errorf("fx snapshot: company_id is required")
	}

	snap := &models.FXSnapshot{
		CompanyID:     input.CompanyID,
		FromCurrency:  input.FromCurrency,
		ToCurrency:    input.ToCurrency,
		Rate:          input.Rate,
		EffectiveDate: input.EffectiveDate,
		RateType:      input.RateType,
		QuoteBasis:    input.QuoteBasis,
		PostingReason: input.PostingReason,
		RateCategory:  input.RateCategory,
		Source:        input.Source,
		IsImmutable:   true,
	}
	if err := db.Create(snap).Error; err != nil {
		return nil, fmt.Errorf("create fx snapshot: %w", err)
	}
	return snap, nil
}

// LinkSnapshotToJournalEntry sets fx_snapshot_id on a journal entry.
// The snapshot must belong to the same company as the journal entry.
// Must be called inside the transaction that created both the snapshot and the JE.
func LinkSnapshotToJournalEntry(db *gorm.DB, jeID uint, snapshotID uint, companyID uint) error {
	var snap models.FXSnapshot
	if err := db.Select("id", "company_id").First(&snap, snapshotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("fx snapshot %d not found", snapshotID)
		}
		return fmt.Errorf("lookup fx snapshot: %w", err)
	}
	if snap.CompanyID != companyID {
		return fmt.Errorf("fx snapshot %d does not belong to company %d", snapshotID, companyID)
	}

	res := db.Model(&models.JournalEntry{}).
		Where("id = ? AND company_id = ?", jeID, companyID).
		Update("fx_snapshot_id", snapshotID)
	if res.Error != nil {
		return fmt.Errorf("link fx snapshot to journal entry: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("journal entry %d not found for company %d", jeID, companyID)
	}
	return nil
}

// LinkSnapshotToSettlementAllocation sets fx_snapshot_id on a settlement allocation.
// The snapshot must belong to the same company as the allocation.
func LinkSnapshotToSettlementAllocation(db *gorm.DB, allocationID uint, snapshotID uint, companyID uint) error {
	var snap models.FXSnapshot
	if err := db.Select("id", "company_id").First(&snap, snapshotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("fx snapshot %d not found", snapshotID)
		}
		return fmt.Errorf("lookup fx snapshot: %w", err)
	}
	if snap.CompanyID != companyID {
		return fmt.Errorf("fx snapshot %d does not belong to company %d", snapshotID, companyID)
	}

	res := db.Model(&models.SettlementAllocation{}).
		Where("id = ? AND company_id = ?", allocationID, companyID).
		Update("fx_snapshot_id", snapshotID)
	if res.Error != nil {
		return fmt.Errorf("link fx snapshot to settlement allocation: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("settlement allocation %d not found for company %d", allocationID, companyID)
	}
	return nil
}
