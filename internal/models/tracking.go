// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// tracking.go — Phase F tracking truth models (lot / serial / expiry).
//
// Tracking data is KEPT ORTHOGONAL TO COSTING. These rows record what
// was physically received and issued, not how each unit is costed —
// costing still flows through inventory_balances (moving-average) or
// inventory_cost_layers + inventory_layer_consumption (FIFO). The
// tracking tables answer "which lot / serial is where, when does it
// expire" without participating in COGS computation.

// ── Inventory lot (lot-tracked items) ────────────────────────────────────────

// InventoryLot represents one bucket of units sharing a lot number and
// (optionally) an expiry date for a single (company, item). Its
// RemainingQuantity is a live counter that decrements on outbound and
// can be topped up by a second inbound of the same lot number (F2 does
// the top-up logic; F1 only defines the shape).
//
// Uniqueness: (company_id, item_id, lot_number) — enforced by a unique
// index in migration 063.
type InventoryLot struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`
	ItemID    uint `gorm:"not null;index"`

	LotNumber  string     `gorm:"type:text;not null"`
	ExpiryDate *time.Time `gorm:"type:date"`

	ReceivedDate time.Time `gorm:"type:date;not null"`

	OriginalQuantity  decimal.Decimal `gorm:"type:numeric(18,4);not null"`
	RemainingQuantity decimal.Decimal `gorm:"type:numeric(18,4);not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName pins to the migration-063 table.
func (InventoryLot) TableName() string { return "inventory_lots" }

// ── Inventory serial unit (serial-tracked items) ─────────────────────────────

// SerialState is the current lifecycle state of an individual serial.
// Transitions:
//
//	(nil) --inbound--> on_hand
//	on_hand --reserve--> reserved
//	reserved --release--> on_hand
//	on_hand --issue--> issued
//	issued --reversal--> on_hand   (reversed by the original reversal anchor)
//	on_hand --void--> void_archived (hard stop — cannot return via this path)
type SerialState string

const (
	SerialStateOnHand       SerialState = "on_hand"
	SerialStateReserved     SerialState = "reserved"
	SerialStateIssued       SerialState = "issued"
	SerialStateVoidArchived SerialState = "void_archived"
)

// InventorySerialUnit represents one individual unit of a
// serial-tracked item. Quantity is always 1 by definition; the row
// itself is the unit.
//
// Uniqueness: at most one row with the same
// (company_id, item_id, serial_number) may be in on_hand or reserved
// state concurrently (enforced by partial unique index in migration 064).
// Issued and void_archived rows persist alongside any re-received row
// for audit history.
type InventorySerialUnit struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`
	ItemID    uint `gorm:"not null;index"`

	SerialNumber string      `gorm:"type:text;not null"`
	CurrentState SerialState `gorm:"type:text;not null"`

	ExpiryDate   *time.Time `gorm:"type:date"`
	ReceivedDate time.Time  `gorm:"type:date;not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName pins to the migration-064 table.
func (InventorySerialUnit) TableName() string { return "inventory_serial_units" }

// ── Inventory tracking consumption (lot/serial outbound log) ─────────────────

// InventoryTrackingConsumption records one lot or serial consumed by a
// tracked outbound movement. Exactly one of LotID or SerialUnitID is
// non-nil per row (DB CHECK constraint). Phase F3.
//
// This table mirrors inventory_layer_consumption (E2.1) for the FIFO
// cost side. On reversal of the outbound movement, the reverse path
// reads these rows, restores the lot.RemainingQuantity or flips the
// serial.CurrentState back to on_hand, then stamps
// ReversedByMovementID so a second reversal attempt cannot double-
// restore.
type InventoryTrackingConsumption struct {
	ID              uint `gorm:"primaryKey"`
	CompanyID       uint `gorm:"not null;index"`
	IssueMovementID uint `gorm:"not null;index"`
	ItemID          uint `gorm:"not null;index"`

	// Exactly one of these is non-nil per row (DB CHECK in migration 065).
	LotID        *uint `gorm:"index"`
	SerialUnitID *uint `gorm:"index"`

	// For lot rows this is the consumed quantity. For serial rows it
	// MUST be 1 (each serial is one unit by definition).
	QuantityDrawn decimal.Decimal `gorm:"type:numeric(18,4);not null"`

	ReversedByMovementID *uint

	CreatedAt time.Time
}

// TableName pins to the migration-065 table.
func (InventoryTrackingConsumption) TableName() string { return "inventory_tracking_consumption" }
