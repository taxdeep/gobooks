// 遵循project_guide.md
package inventory

import (
	"time"

	"github.com/shopspring/decimal"
)

// ── Enums ────────────────────────────────────────────────────────────────────

// CostingMethod selects how a single IssueStock call is priced. An empty
// value means "use the item's default", which in Phase D is weighted
// average for every product. FIFO / Specific are reserved for Phase E.
type CostingMethod string

const (
	CostingMethodDefault       CostingMethod = ""
	CostingMethodWeightedAvg   CostingMethod = "weighted_avg"
	CostingMethodFIFO          CostingMethod = "fifo"
	CostingMethodSpecific      CostingMethod = "specific"
)

// AdjustmentReason taxonomizes why an AdjustStock event occurred. It is
// persisted on the movement row's Memo for reporting; the set is open —
// new reasons can be added without migration.
type AdjustmentReason string

const (
	AdjustmentReasonCountVariance AdjustmentReason = "count_variance"
	AdjustmentReasonDamage        AdjustmentReason = "damage"
	AdjustmentReasonExpiry        AdjustmentReason = "expiry"
	AdjustmentReasonTheft         AdjustmentReason = "theft"
	AdjustmentReasonOther         AdjustmentReason = "other"
)

// ReversalReason classifies why a prior movement is being reversed.
type ReversalReason string

const (
	ReversalReasonCustomerReturn  ReversalReason = "customer_return"
	ReversalReasonVendorReturn    ReversalReason = "vendor_return"
	ReversalReasonCancellation    ReversalReason = "cancellation"
	ReversalReasonErrorCorrection ReversalReason = "error_correction"
)

// MovementDirection filters query results to inflows or outflows.
type MovementDirection string

const (
	MovementDirectionIn   MovementDirection = "in"
	MovementDirectionOut  MovementDirection = "out"
	MovementDirectionBoth MovementDirection = "both"
)

// ValuationGroupBy shapes the breakdown of GetValuationSnapshot.
type ValuationGroupBy string

const (
	ValuationGroupByNone      ValuationGroupBy = "none"
	ValuationGroupByItem      ValuationGroupBy = "item"
	ValuationGroupByWarehouse ValuationGroupBy = "warehouse"
	ValuationGroupByCategory  ValuationGroupBy = "category"
)

// ── IN: ReceiveStock ─────────────────────────────────────────────────────────

// ReceiveStockInput is the complete payload for an inflow event. All
// non-optional fields must be set or the function returns a validation
// error before touching the database.
type ReceiveStockInput struct {
	// Locator
	CompanyID   uint
	ItemID      uint
	WarehouseID uint
	Quantity    decimal.Decimal // strictly positive

	// Event date (business-document date, not time.Now).
	MovementDate time.Time

	// Cost at receipt, in document currency.
	UnitCost     decimal.Decimal
	CurrencyCode string          // ISO-4217; empty string = company base
	ExchangeRate decimal.Decimal // document->base conversion; base = document × rate

	// Landed cost allocated to this line, in base currency. Caller
	// computes the apportionment across lines before calling.
	LandedCostAllocation decimal.Decimal

	// Traceability
	SourceType   string // e.g. "bill", "opening", "adjustment", "transfer_in"
	SourceID     uint
	SourceLineID *uint

	// Unit of measure (Phase E). Leave UoMCode empty / UoMFactor zero for
	// base-unit receipts.
	UoMCode   string
	UoMFactor decimal.Decimal

	// Lot / serial / expiry (Phase F).
	LotNumber     string
	SerialNumbers []string
	ExpiryDate    *time.Time

	// Audit + idempotency.
	IdempotencyKey string
	ActorUserID    *uint
	Memo           string
}

// ReceiveStockResult reports the booked movement ID and the base-currency
// figures downstream consumers (GL) need to post their own entries.
type ReceiveStockResult struct {
	MovementID         uint
	UnitCostBase       decimal.Decimal
	InventoryValueBase decimal.Decimal // Qty × UnitCostBase
}

// ── IN: IssueStock ───────────────────────────────────────────────────────────

type IssueStockInput struct {
	CompanyID    uint
	ItemID       uint
	WarehouseID  uint
	Quantity     decimal.Decimal // strictly positive; package flips sign internally
	MovementDate time.Time

	SourceType   string
	SourceID     uint
	SourceLineID *uint

	AllowNegative bool
	CostingMethod CostingMethod
	SpecificLotID *uint // required iff CostingMethod == Specific

	IdempotencyKey string
	ActorUserID    *uint
	Memo           string
}

// IssueStockResult returns the cost computed by inventory. Callers must not
// assume a cost ahead of the call — that is the keystone of the bounded-
// context design.
type IssueStockResult struct {
	MovementID      uint
	UnitCostBase    decimal.Decimal
	CostOfIssueBase decimal.Decimal // Qty × UnitCostBase
	CostLayers      []CostLayerConsumed
}

// CostLayerConsumed is non-empty only under FIFO or Specific methods. It
// documents which historical receipts this issue draws from, so GL can
// post multi-layer COGS if desired (or simply take the summed total).
type CostLayerConsumed struct {
	SourceMovementID uint
	Quantity         decimal.Decimal
	UnitCostBase     decimal.Decimal
	TotalCostBase    decimal.Decimal
}

// ── IN: AdjustStock ──────────────────────────────────────────────────────────

type AdjustStockInput struct {
	CompanyID     uint
	ItemID        uint
	WarehouseID   uint
	QuantityDelta decimal.Decimal // signed: positive for gain, negative for loss
	MovementDate  time.Time

	Reason AdjustmentReason

	// Cost fields are only consulted when QuantityDelta > 0 (gain). On loss
	// the current weighted-average cost is used automatically.
	UnitCost     *decimal.Decimal
	CurrencyCode string
	ExchangeRate *decimal.Decimal

	SourceType     string
	SourceID       uint
	IdempotencyKey string
	ActorUserID    *uint
	Memo           string
}

type AdjustStockResult struct {
	MovementID          uint
	UnitCostBase        decimal.Decimal
	AdjustmentValueBase decimal.Decimal // signed; base × delta
}

// ── IN: TransferStock ────────────────────────────────────────────────────────

type TransferStockInput struct {
	CompanyID       uint
	TransferID      uint
	ItemID          uint
	FromWarehouseID uint
	ToWarehouseID   uint
	Quantity        decimal.Decimal
	ShippedDate     time.Time
	ReceivedDate    *time.Time // nil = in transit, only issue leg runs

	IdempotencyKey string
	ActorUserID    *uint
	Memo           string
}

type TransferStockResult struct {
	IssueMovementID   uint
	ReceiveMovementID *uint
	UnitCostBase      decimal.Decimal // from-warehouse avg at ship; applied to both legs
	TransitValueBase  decimal.Decimal
}

// ── IN: ReverseMovement ──────────────────────────────────────────────────────

type ReverseMovementInput struct {
	CompanyID          uint
	OriginalMovementID uint
	MovementDate       time.Time

	Reason     ReversalReason
	SourceType string
	SourceID   uint

	IdempotencyKey string
	ActorUserID    *uint
	Memo           string
}

type ReverseMovementResult struct {
	ReversalMovementID uint
	UnitCostBase       decimal.Decimal // copied from original; never current avg
	ReversalValueBase  decimal.Decimal // sign opposite of original
}

// ── IN: ReserveStock / ReleaseStock (Phase E placeholders) ──────────────────

// ReserveStockInput / ReleaseStockInput are defined but their implementations
// are deferred to Phase E. The placeholder types keep the package surface
// stable so early callers can compile-reference them.
type ReserveStockInput struct {
	CompanyID    uint
	ItemID       uint
	WarehouseID  uint
	Quantity     decimal.Decimal
	SourceType   string
	SourceID     uint
	SourceLineID *uint

	IdempotencyKey string
	ActorUserID    *uint
	Memo           string
}

type ReserveStockResult struct {
	QuantityReserved decimal.Decimal // resulting reserved quantity at that warehouse
}

type ReleaseStockInput = ReserveStockInput

// ── OUT: GetOnHand ───────────────────────────────────────────────────────────

type OnHandQuery struct {
	CompanyID   uint
	ItemID      uint       // 0 = all items
	WarehouseID uint       // 0 = all warehouses
	AsOfDate    *time.Time // nil = current; otherwise reconstruct historical
	IncludeZero bool
}

type OnHandRow struct {
	ItemID            uint
	WarehouseID       uint
	QuantityOnHand    decimal.Decimal
	QuantityReserved  decimal.Decimal
	QuantityAvailable decimal.Decimal // OnHand − Reserved
	AverageCostBase   decimal.Decimal
	TotalValueBase    decimal.Decimal
}

// ── OUT: GetMovements ────────────────────────────────────────────────────────

type MovementQuery struct {
	CompanyID             uint
	ItemID                *uint
	WarehouseID           *uint
	FromDate              *time.Time
	ToDate                *time.Time
	SourceType            string
	SourceID              *uint
	Direction             *MovementDirection
	Limit                 int
	Offset                int
	IncludeRunningBalance bool
}

type MovementRow struct {
	ID               uint
	MovementDate     time.Time
	ItemID           uint
	WarehouseID      *uint
	MovementType     string
	QuantityDelta    decimal.Decimal
	UnitCostBase     decimal.Decimal
	TotalCostBase    decimal.Decimal
	SourceType       string
	SourceID         *uint
	SourceLineID     *uint
	RunningQuantity  decimal.Decimal // populated when IncludeRunningBalance
	RunningValueBase decimal.Decimal
	Memo             string
	ActorUserID      *uint
	CreatedAt        time.Time
}

// ── OUT: GetItemLedger ───────────────────────────────────────────────────────

type ItemLedgerQuery struct {
	CompanyID   uint
	ItemID      uint
	WarehouseID *uint // nil = aggregate across warehouses
	FromDate    time.Time
	ToDate      time.Time
}

type ItemLedgerReport struct {
	ItemID           uint
	WarehouseID      *uint
	OpeningQuantity  decimal.Decimal
	OpeningValueBase decimal.Decimal
	OpeningUnitCost  decimal.Decimal
	Movements        []MovementRow
	ClosingQuantity  decimal.Decimal
	ClosingValueBase decimal.Decimal
	ClosingUnitCost  decimal.Decimal
	TotalInQty       decimal.Decimal
	TotalInValue     decimal.Decimal
	TotalOutQty      decimal.Decimal
	TotalOutCostBase decimal.Decimal // period COGS contribution
}

// ── OUT: ExplodeBOM ──────────────────────────────────────────────────────────

type BOMExplodeQuery struct {
	CompanyID           uint
	ParentItemID        uint
	Quantity            decimal.Decimal
	MultiLevel          bool
	IncludeCostEstimate bool
	IncludeAvailability bool
	WarehouseID         *uint // required when IncludeAvailability
}

type BOMExplodeRow struct {
	ComponentItemID        uint
	Depth                  int
	Path                   []uint // ancestor chain (audit for cycle detection)
	QuantityPerUnit        decimal.Decimal
	TotalQuantity          decimal.Decimal
	ScrapPct               decimal.Decimal
	EstimatedUnitCostBase  *decimal.Decimal
	EstimatedTotalCostBase *decimal.Decimal
	AvailableQuantity      *decimal.Decimal
	ShortBy                *decimal.Decimal
}

// ── OUT: GetValuationSnapshot ────────────────────────────────────────────────

type ValuationQuery struct {
	CompanyID   uint
	AsOfDate    time.Time
	GroupBy     ValuationGroupBy
	WarehouseID *uint
}

type ValuationRow struct {
	GroupKey   string
	GroupLabel string
	Quantity   decimal.Decimal
	ValueBase  decimal.Decimal
}

// ── OUT: GetCostingPreview ───────────────────────────────────────────────────

type CostingPreviewQuery struct {
	CompanyID   uint
	ItemID      uint
	WarehouseID uint
	Quantity    decimal.Decimal
	AsOfDate    *time.Time
}

type CostingPreviewResult struct {
	UnitCostBase  decimal.Decimal
	TotalCostBase decimal.Decimal
	CostLayers    []CostLayerConsumed
	Feasible      bool
	ShortBy       decimal.Decimal
}
