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

	// Lot / serial / expiry — captured when the item is lot- or
	// serial-tracked (Phase F2). Validation:
	//   - tracking_mode="none": all three MUST be empty (reject loud).
	//   - tracking_mode="lot": LotNumber required; ExpiryDate optional;
	//     SerialNumbers / SerialExpiryDates must be empty.
	//   - tracking_mode="serial": SerialNumbers required, len==Quantity;
	//     SerialExpiryDates (if provided) must be len==len(SerialNumbers);
	//     LotNumber must be empty.
	LotNumber         string
	ExpiryDate        *time.Time
	SerialNumbers     []string
	SerialExpiryDates []*time.Time

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

	// ── Phase F3 tracked outbound selections ────────────────────────
	// These fields are REQUIRED when the item's tracking_mode is
	// 'lot' or 'serial'. They are validated against the item's
	// configured mode, not the caller's intent.
	//
	// LotSelections: explicit lot-level allocation for lot-tracked
	// items. SUM(LotSelections.Quantity) must equal Quantity. No
	// allocator — the caller picks which lots and how much from each.
	// (Phase F3 does not ship FEFO or any implicit allocator.)
	//
	// SerialSelections: explicit serial list for serial-tracked items.
	// len(SerialSelections) must equal Quantity. Each must currently
	// be in 'on_hand' state for the same (company, item).
	LotSelections    []LotSelection
	SerialSelections []string

	IdempotencyKey string
	ActorUserID    *uint
	Memo           string
}

// LotSelection names one lot and the quantity to draw from it in a
// tracked outbound event.
type LotSelection struct {
	LotID    uint
	Quantity decimal.Decimal
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
//
// LayerID is the inventory_cost_layers row consumed; Phase E2.1 uses it
// to write an inventory_layer_consumption record linking the layer to
// the issue movement so reversal can restore RemainingQuantity.
type CostLayerConsumed struct {
	LayerID          uint
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

// ── IN: PostInventoryBuild ──────────────────────────────────────────────────
//
// Builds (assemblies) consume N components and produce 1 finished good. The
// orchestrator is a thin layer over IssueStock + ReceiveStock; it does not
// own a separate header table — the paired movements (linked by SourceID)
// are the system of record. A future business-document layer can persist a
// Build header on top if richer reporting is needed.

type PostInventoryBuildInput struct {
	CompanyID      uint
	ParentItemID   uint   // assembly being built; must be inventory-tracked
	WarehouseID    uint   // both consume and produce hit the same warehouse
	Quantity       decimal.Decimal // finished-good units to produce; > 0
	BuildDate      time.Time
	BuildRef       uint   // caller-provided reference (links the movement pair)

	// Optional non-component costs blended into the finished-good unit cost.
	// Both are in base currency for the whole build, not per-unit.
	LaborCostBase    decimal.Decimal
	OverheadCostBase decimal.Decimal

	// Components: when nil, PostInventoryBuild reads the parent's BOM
	// (item_components) and uses each row's per-unit quantity. When set,
	// caller fully overrides the BOM for this build (supports rework /
	// substitutes without mutating master data).
	ComponentOverrides []BuildComponentInput

	IdempotencyKey string
	ActorUserID    *uint
	Memo           string
}

// BuildComponentInput overrides one BOM row for this single build.
// PerUnitQuantity is consumed × Quantity; for example PerUnitQuantity=2 +
// Quantity=10 issues 20 of this component.
type BuildComponentInput struct {
	ItemID          uint
	PerUnitQuantity decimal.Decimal
}

// PostInventoryBuildResult exposes both the produced movement and the
// consumed-component breakdown so callers can post a journal entry that
// debits the finished-good inventory account and credits each component's
// inventory account by its blended-out cost.
type PostInventoryBuildResult struct {
	ProduceMovementID    uint
	UnitCostBase         decimal.Decimal // sum(component cost) / produced qty + labor/overhead per unit
	FinishedValueBase    decimal.Decimal // Quantity × UnitCostBase
	ComponentCostBase    decimal.Decimal // raw materials only (excludes labor/overhead)
	LaborCostBase        decimal.Decimal // echoed from input for completeness
	OverheadCostBase     decimal.Decimal
	Components           []BuildComponentConsumed
}

// BuildComponentConsumed reports each issued component leg so the caller
// can render a build report and post a multi-leg JE if their COA splits
// inventory accounts by item.
type BuildComponentConsumed struct {
	ItemID          uint
	IssueMovementID uint
	Quantity        decimal.Decimal // total consumed (PerUnit × build qty)
	UnitCostBase    decimal.Decimal // weighted-avg at time of consumption
	TotalCostBase   decimal.Decimal // Quantity × UnitCostBase
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
