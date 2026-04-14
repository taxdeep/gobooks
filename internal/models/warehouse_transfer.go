// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// WarehouseTransferStatus tracks the lifecycle of an inter-warehouse transfer.
type WarehouseTransferStatus string

const (
	TransferStatusDraft     WarehouseTransferStatus = "draft"
	TransferStatusPosted    WarehouseTransferStatus = "posted"
	TransferStatusCancelled WarehouseTransferStatus = "cancelled"
)

// AllTransferStatuses returns all valid status values.
func AllTransferStatuses() []WarehouseTransferStatus {
	return []WarehouseTransferStatus{
		TransferStatusDraft,
		TransferStatusPosted,
		TransferStatusCancelled,
	}
}

// TransferStatusLabel returns the human-readable label for a transfer status.
func TransferStatusLabel(s WarehouseTransferStatus) string {
	switch s {
	case TransferStatusDraft:
		return "Draft"
	case TransferStatusPosted:
		return "Posted"
	case TransferStatusCancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

// WarehouseTransfer records an inter-warehouse stock movement.
// Draft transfers have no inventory impact; posting applies the movement
// (outbound from source, inbound to destination) in a single transaction.
type WarehouseTransfer struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	Reference string                  `gorm:"type:text;not null;default:''"`
	Status    WarehouseTransferStatus `gorm:"type:text;not null;default:'draft'"`

	FromWarehouseID uint       `gorm:"not null;index"`
	FromWarehouse   *Warehouse `gorm:"foreignKey:FromWarehouseID"`
	ToWarehouseID   uint       `gorm:"not null;index"`
	ToWarehouse     *Warehouse `gorm:"foreignKey:ToWarehouseID"`

	ItemID uint           `gorm:"not null;index"`
	Item   *ProductService `gorm:"foreignKey:ItemID"`

	Quantity     decimal.Decimal `gorm:"type:numeric(18,4);not null"`
	TransferDate time.Time `gorm:"type:date;not null"`
	Notes        string    `gorm:"type:text;not null;default:''"`

	// Audit
	CreatedByEmail string `gorm:"type:text;not null;default:''"`
	PostedByEmail  string `gorm:"type:text;not null;default:''"`

	// Links to the inventory movements created on posting (informational)
	OutboundMovementID *uint
	InboundMovementID  *uint

	CreatedAt time.Time
	UpdatedAt time.Time
}
