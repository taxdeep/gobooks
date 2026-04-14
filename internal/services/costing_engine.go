// 遵循project_guide.md
package services

// costing_engine.go — Pluggable inventory costing abstraction.
//
// The CostingEngine interface decouples inventory movement processing from the
// specific cost-flow algorithm. The posting engine and inventory service call
// these methods instead of writing cost formulas inline.
//
// Current implementation: MovingAverageCostingEngine (moving_average_costing.go).
// Future: FIFOCostingEngine would maintain per-purchase cost layers and consume
// them in FIFO order on outbound — the interface is designed to accommodate
// this without changing callers.

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/gorm"
)

// ── Costing method enum ──────────────────────────────────────────────────────

// CostingMethod identifies the inventory costing algorithm for a company.
type CostingMethod string

const (
	CostingMethodMovingAverage CostingMethod = "moving_average"
	// Future: CostingMethodFIFO CostingMethod = "fifo"
)

// ── Request / Result types ───────────────────────────────────────────────────

// InboundRequest describes an inventory receipt (purchase, opening, positive adjustment).
//
// WarehouseID (optional): when set, routes the movement to the named Warehouse row.
// When nil, falls back to LocationType/LocationRef (legacy single-warehouse path).
type InboundRequest struct {
	CompanyID    uint
	ItemID       uint
	Quantity     decimal.Decimal
	UnitCost     decimal.Decimal
	MovementType models.InventoryMovementType
	LocationType models.LocationType
	LocationRef  string
	WarehouseID  *uint // multi-warehouse routing (nil = legacy path)
	Date         time.Time
}

// InboundResult contains the outcome of applying an inbound movement.
type InboundResult struct {
	UnitCostUsed       decimal.Decimal // cost recorded on the movement
	TotalCost          decimal.Decimal // quantity × unit_cost
	NewQuantityOnHand  decimal.Decimal // balance after inbound
	NewAverageCost     decimal.Decimal // updated average cost (moving avg specific, but useful for all methods)
}

// OutboundRequest describes an inventory issue (sale, negative adjustment).
//
// WarehouseID (optional): when set, routes the movement to the named Warehouse row.
// When nil, falls back to LocationType/LocationRef (legacy single-warehouse path).
type OutboundRequest struct {
	CompanyID    uint
	ItemID       uint
	Quantity     decimal.Decimal // positive value = how many units to remove
	MovementType models.InventoryMovementType
	LocationType models.LocationType
	LocationRef  string
	WarehouseID  *uint // multi-warehouse routing (nil = legacy path)
	Date         time.Time
}

// OutboundResult contains the cost determination for an outbound movement.
type OutboundResult struct {
	UnitCostUsed       decimal.Decimal // cost per unit used for COGS
	TotalCost          decimal.Decimal // quantity × unit_cost_used
	NewQuantityOnHand  decimal.Decimal // balance after outbound
	NewAverageCost     decimal.Decimal // average cost after outbound
}

// ── CostingEngine interface ──────────────────────────────────────────────────

// CostingEngine abstracts the inventory cost-flow algorithm.
//
// All methods that modify state (ApplyInbound, ApplyOutbound) expect to run
// inside an existing DB transaction. They read the current balance (with row
// lock where supported), apply the algorithm, and persist the updated balance.
//
// PreviewOutbound is a read-only check used by the posting engine's pre-flight
// validation: it returns the cost that would be used without modifying state.
type CostingEngine interface {
	// PreviewOutbound returns the unit cost and total cost that would apply
	// for an outbound of the given quantity, without modifying any state.
	// Returns ErrInsufficientStock if quantity exceeds available stock.
	// Does NOT require a transaction — safe to call outside tx.
	PreviewOutbound(db *gorm.DB, req OutboundRequest) (*OutboundResult, error)

	// ApplyInbound processes an inventory receipt: updates the balance row
	// with the new quantity and recalculated cost. Must run inside a tx.
	ApplyInbound(tx *gorm.DB, req InboundRequest) (*InboundResult, error)

	// ApplyOutbound processes an inventory issue: validates sufficient stock,
	// determines cost, and updates the balance row. Must run inside a tx.
	// Returns ErrInsufficientStock if balance is insufficient.
	ApplyOutbound(tx *gorm.DB, req OutboundRequest) (*OutboundResult, error)
}

// ── Engine resolver ──────────────────────────────────────────────────────────

// ErrUnsupportedCostingMethod is returned when a non-empty but unrecognised
// costing method is requested. Empty string is treated as moving_average.
var ErrUnsupportedCostingMethod = errors.New("unsupported inventory costing method")

// GetCostingEngine returns the CostingEngine for the given costing method.
// Empty string defaults to moving average. Unknown non-empty methods return an error.
func GetCostingEngine(method CostingMethod) (CostingEngine, error) {
	switch method {
	case CostingMethodMovingAverage, "":
		return &MovingAverageCostingEngine{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedCostingMethod, method)
	}
}

// ResolveCostingEngineForCompany returns the costing engine configured for the
// given company. Reads the company's InventoryCostingMethod field.
// Falls back to moving average if the field is empty or the company cannot be loaded.
// Returns an error only if the stored method is an unknown non-empty value.
func ResolveCostingEngineForCompany(db *gorm.DB, companyID uint) (CostingEngine, error) {
	var company models.Company
	if err := db.Select("id", "inventory_costing_method").First(&company, companyID).Error; err != nil {
		e, _ := GetCostingEngine(CostingMethodMovingAverage)
		return e, nil
	}
	method := CostingMethod(company.InventoryCostingMethod)
	return GetCostingEngine(method)
}

// ValidateCostingMethodChange checks whether the company's costing method can
// be changed. Returns an error if inventory movements already exist.
var ErrCostingMethodLocked = errors.New("cannot change costing method: inventory movements already exist for this company")

func ValidateCostingMethodChange(db *gorm.DB, companyID uint) error {
	var count int64
	db.Model(&models.InventoryMovement{}).Where("company_id = ?", companyID).Count(&count)
	if count > 0 {
		return ErrCostingMethodLocked
	}
	return nil
}
