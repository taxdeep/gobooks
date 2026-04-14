// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/gorm"
)

// ── Errors ───────────────────────────────────────────────────────────────────

var (
	ErrTransferNotFound      = errors.New("warehouse transfer not found")
	ErrTransferSameWarehouse = errors.New("source and destination warehouse must be different")
	ErrTransferAlreadyPosted = errors.New("transfer is already posted")
	ErrTransferNotDraft      = errors.New("only draft transfers can be modified")
	ErrTransferZeroQty       = errors.New("transfer quantity must be greater than zero")
)

// ── Input type ────────────────────────────────────────────────────────────────

type TransferInput struct {
	FromWarehouseID uint
	ToWarehouseID   uint
	ItemID          uint
	Quantity        decimal.Decimal
	TransferDate    time.Time
	Notes           string
	Reference       string
	CreatedByEmail  string
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

// ListTransfers returns transfers for a company, optionally filtered by status.
func ListTransfers(db *gorm.DB, companyID uint, filterStatus string) ([]models.WarehouseTransfer, error) {
	q := db.Where("company_id = ?", companyID).
		Preload("FromWarehouse").Preload("ToWarehouse").Preload("Item").
		Order("transfer_date DESC, id DESC")
	if filterStatus != "" {
		q = q.Where("status = ?", filterStatus)
	}
	var ts []models.WarehouseTransfer
	return ts, q.Find(&ts).Error
}

// GetTransfer returns a single transfer by ID.
func GetTransfer(db *gorm.DB, companyID, id uint) (*models.WarehouseTransfer, error) {
	var t models.WarehouseTransfer
	err := db.Where("id = ? AND company_id = ?", id, companyID).
		Preload("FromWarehouse").Preload("ToWarehouse").Preload("Item").
		First(&t).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrTransferNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTransfer creates a draft transfer. No inventory movements are created yet.
func CreateTransfer(db *gorm.DB, companyID uint, in TransferInput) (*models.WarehouseTransfer, error) {
	if err := validateTransferInput(db, companyID, in); err != nil {
		return nil, err
	}

	t := models.WarehouseTransfer{
		CompanyID:       companyID,
		Reference:       in.Reference,
		Status:          models.TransferStatusDraft,
		FromWarehouseID: in.FromWarehouseID,
		ToWarehouseID:   in.ToWarehouseID,
		ItemID:          in.ItemID,
		Quantity:        in.Quantity,
		TransferDate:    in.TransferDate,
		Notes:           in.Notes,
		CreatedByEmail:  in.CreatedByEmail,
	}
	if err := db.Create(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateTransfer updates a draft transfer. Returns ErrTransferNotDraft if already posted/cancelled.
func UpdateTransfer(db *gorm.DB, companyID, id uint, in TransferInput) (*models.WarehouseTransfer, error) {
	t, err := GetTransfer(db, companyID, id)
	if err != nil {
		return nil, err
	}
	if t.Status != models.TransferStatusDraft {
		return nil, ErrTransferNotDraft
	}
	if err := validateTransferInput(db, companyID, in); err != nil {
		return nil, err
	}

	t.Reference       = in.Reference
	t.FromWarehouseID = in.FromWarehouseID
	t.ToWarehouseID   = in.ToWarehouseID
	t.ItemID          = in.ItemID
	t.Quantity        = in.Quantity
	t.TransferDate    = in.TransferDate
	t.Notes           = in.Notes
	if err := db.Save(t).Error; err != nil {
		return nil, err
	}
	return t, nil
}

// ── State transitions ─────────────────────────────────────────────────────────

// PostTransfer executes an inter-warehouse transfer:
//  1. Validates sufficient stock at the source warehouse.
//  2. Applies outbound movement at source (reducing its balance).
//  3. Applies inbound movement at destination (increasing its balance).
//  4. Creates two InventoryMovement records and links them to the transfer.
//  5. Marks the transfer as "posted".
//
// All steps run in a single DB transaction.
func PostTransfer(db *gorm.DB, companyID, id uint, actor string, actorID *uuid.UUID) error {
	t, err := GetTransfer(db, companyID, id)
	if err != nil {
		return err
	}
	if t.Status == models.TransferStatusPosted {
		return ErrTransferAlreadyPosted
	}
	if t.Status != models.TransferStatusDraft {
		return ErrTransferNotDraft
	}

	engine, err := ResolveCostingEngineForCompany(db, companyID)
	if err != nil {
		return fmt.Errorf("resolve costing engine: %w", err)
	}

	// Pre-flight: validate sufficient stock at source.
	_, err = engine.PreviewOutbound(db, OutboundRequest{
		CompanyID:    companyID,
		ItemID:       t.ItemID,
		Quantity:     t.Quantity,
		MovementType: models.MovementTypeAdjustment,
		WarehouseID:  &t.FromWarehouseID,
	})
	if err != nil {
		return fmt.Errorf("insufficient stock at source warehouse: %w", err)
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// a. Outbound from source warehouse.
		outResult, err := engine.ApplyOutbound(tx, OutboundRequest{
			CompanyID:    companyID,
			ItemID:       t.ItemID,
			Quantity:     t.Quantity,
			MovementType: models.MovementTypeAdjustment,
			WarehouseID:  &t.FromWarehouseID,
			Date:         t.TransferDate,
		})
		if err != nil {
			return fmt.Errorf("outbound from source: %w", err)
		}

		outQtyNeg := t.Quantity.Neg()
		outMov := models.InventoryMovement{
			CompanyID:     companyID,
			ItemID:        t.ItemID,
			MovementType:  models.MovementTypeAdjustment,
			QuantityDelta: outQtyNeg,
			UnitCost:      &outResult.UnitCostUsed,
			TotalCost:     &outResult.TotalCost,
			SourceType:    "warehouse_transfer",
			SourceID:      &t.ID,
			ReferenceNote: fmt.Sprintf("Transfer out → WH#%d", t.ToWarehouseID),
			WarehouseID:   &t.FromWarehouseID,
			MovementDate:  t.TransferDate,
		}
		if err := tx.Create(&outMov).Error; err != nil {
			return fmt.Errorf("create outbound movement: %w", err)
		}

		// b. Inbound to destination warehouse at the same unit cost (no cost change on transfers).
		_, err = engine.ApplyInbound(tx, InboundRequest{
			CompanyID:    companyID,
			ItemID:       t.ItemID,
			Quantity:     t.Quantity,
			UnitCost:     outResult.UnitCostUsed,
			MovementType: models.MovementTypeAdjustment,
			WarehouseID:  &t.ToWarehouseID,
			Date:         t.TransferDate,
		})
		if err != nil {
			return fmt.Errorf("inbound to destination: %w", err)
		}

		totalCostPos := outResult.TotalCost
		inMov := models.InventoryMovement{
			CompanyID:     companyID,
			ItemID:        t.ItemID,
			MovementType:  models.MovementTypeAdjustment,
			QuantityDelta: t.Quantity,
			UnitCost:      &outResult.UnitCostUsed,
			TotalCost:     &totalCostPos,
			SourceType:    "warehouse_transfer",
			SourceID:      &t.ID,
			ReferenceNote: fmt.Sprintf("Transfer in ← WH#%d", t.FromWarehouseID),
			WarehouseID:   &t.ToWarehouseID,
			MovementDate:  t.TransferDate,
		}
		if err := tx.Create(&inMov).Error; err != nil {
			return fmt.Errorf("create inbound movement: %w", err)
		}

		// c. Mark transfer as posted and link movements.
		now := time.Now()
		_ = now
		return tx.Model(t).Updates(map[string]any{
			"status":               string(models.TransferStatusPosted),
			"posted_by_email":      actor,
			"outbound_movement_id": outMov.ID,
			"inbound_movement_id":  inMov.ID,
			"updated_at":           time.Now(),
		}).Error
	})
}

// CancelTransfer cancels a draft transfer. Cannot cancel posted transfers.
func CancelTransfer(db *gorm.DB, companyID, id uint) error {
	t, err := GetTransfer(db, companyID, id)
	if err != nil {
		return err
	}
	if t.Status == models.TransferStatusPosted {
		return ErrTransferAlreadyPosted
	}
	return db.Model(t).Update("status", string(models.TransferStatusCancelled)).Error
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func validateTransferInput(db *gorm.DB, companyID uint, in TransferInput) error {
	if in.FromWarehouseID == 0 || in.ToWarehouseID == 0 {
		return fmt.Errorf("source and destination warehouse are required")
	}
	if in.FromWarehouseID == in.ToWarehouseID {
		return ErrTransferSameWarehouse
	}
	if in.ItemID == 0 {
		return fmt.Errorf("item is required")
	}
	if !in.Quantity.IsPositive() {
		return ErrTransferZeroQty
	}

	// Verify warehouses belong to the company.
	if _, err := GetWarehouse(db, companyID, in.FromWarehouseID); err != nil {
		return fmt.Errorf("source warehouse: %w", err)
	}
	if _, err := GetWarehouse(db, companyID, in.ToWarehouseID); err != nil {
		return fmt.Errorf("destination warehouse: %w", err)
	}
	return nil
}
