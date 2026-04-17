# Inventory Module — API Contract & Architecture

**Status:** Design ratified; implementation in progress (Phase D.0+).
**Owner area:** `internal/services/inventory/` (package to be created).
**Companion docs:** `PROJECT_GUIDE.md`, `INVOICE_MODULE_ANALYSIS.md`.

---

## 1. Architectural position

GoBooks is a three-layer system. Each layer has a single job and an explicit
contract to the layer(s) it depends on:

```
┌─────────────────────────────────────────────────────────────────────────┐
│  Business Document layer                                                │
│  Bill · Invoice · Purchase Order · Sales Order ·                        │
│  InventoryBuild · WarehouseTransfer · Adjustment · Stock Count          │
│  ↑ user-facing; records what happened in the real world                 │
└───────────┬────────────────────────────────────────┬────────────────────┘
            │                                        │
            ▼                                        ▼
┌──────────────────────────────┐      ┌──────────────────────────────┐
│  Inventory module            │      │  General Ledger module       │
│  • movement ledger           │      │  • journal entries           │
│  • balance + unit cost       │      │  • account balances          │
│  • BOM + build + transfer    │      │  • fiscal periods            │
│  • costing (avg / FIFO)      │      │  • reports (P&L / BS)        │
│  ↑ quantity + cost           │      │  ↑ debit / credit            │
└──────────────────────────────┘      └──────────────────────────────┘
```

**Rules**

1. Inventory and GL do not know each other's internals. They are siblings,
   orchestrated by the Business Document layer.
2. A business document action (Post Bill, Post Invoice, Post Build) is a
   single transaction that writes to (a) its own tables, (b) Inventory via
   its API, (c) GL via its API — in that order.
3. Inventory **does not create journal entries**. It returns costs; the
   document layer hands those costs to GL.
4. GL does not write inventory movements. If it needs "what was the COGS
   for invoice 42?", it asks inventory via `GetMovements(source=invoice,
   id=42)`.
5. Cross-module references go through the common `SourceType + SourceID`
   pair pointing at the originating business document — never module-to-
   module direct FKs.
6. `InventoryMovement.JournalEntryID` (current schema) is a legacy
   backward-direction coupling to be removed during D.0.

---

## 2. Design principles for the Inventory API

1. **IN events carry all context** — currency, FX rate, landed-cost
   allocation, actor, idempotency key. Inventory never reaches back to
   look it up.
2. **OUT queries are read-only**. Any mutation goes through an IN event.
3. **All functions accept `*gorm.DB`** so the caller controls the outer
   transaction boundary. Inventory does not commit on its own.
4. **Idempotency** via `IdempotencyKey` on every IN event. Replays are
   safe.
5. **History is immutable.** "Undo" is a reversing event (`ReverseMovement`),
   never an `UPDATE` or `DELETE`.
6. **Errors are explicit and classified** — see §5. No silent no-ops for
   business-rule violations.
7. **Cost flows from Inventory outward.** Callers never pass a cost on
   `IssueStock`; inventory computes it (weighted avg / FIFO / specific)
   and returns it. This is the keystone.
8. **Base currency values returned on every event** so GL can post
   without re-computing FX.

---

## 3. IN contracts (data enters inventory)

Seven functions. All live in `internal/services/inventory/` and operate on
`*gorm.DB` passed by the caller.

### 3.1 `ReceiveStock`

```go
type ReceiveStockInput struct {
    // Locator
    CompanyID    uint
    ItemID       uint
    WarehouseID  uint
    Quantity     decimal.Decimal   // > 0

    // Economic date of the event (document date, not now())
    MovementDate time.Time

    // Cost at time of receipt (document currency)
    UnitCost     decimal.Decimal
    CurrencyCode string            // ISO 4217
    ExchangeRate decimal.Decimal   // to company base currency

    // Landed costs allocated to this line (base currency, absolute amount)
    // Caller computes the apportionment (by weight / value / line count)
    // and passes the already-allocated figure.
    LandedCostAllocation decimal.Decimal

    // Source traceability
    SourceType   string   // "bill" | "opening" | "adjustment" | "transfer_in"
                          // | "build_produce" | "return_from_customer"
    SourceID     uint
    SourceLineID *uint

    // Unit-of-measure (Phase E; defaults to 1:1 base unit)
    UoMCode   string
    UoMFactor decimal.Decimal

    // Lot / serial / expiry (Phase F)
    LotNumber     string
    SerialNumbers []string
    ExpiryDate    *time.Time

    // Audit + idempotency
    IdempotencyKey string   // e.g. "bill:42:line:3:v1"
    ActorUserID    *uint
    Memo           string
}

type ReceiveStockResult struct {
    MovementID         uint
    UnitCostBase       decimal.Decimal
    InventoryValueBase decimal.Decimal  // Qty × UnitCostBase; GL: Dr Inventory
}

func ReceiveStock(db *gorm.DB, in ReceiveStockInput) (*ReceiveStockResult, error)
```

### 3.2 `IssueStock`

```go
type IssueStockInput struct {
    CompanyID    uint
    ItemID       uint
    WarehouseID  uint
    Quantity     decimal.Decimal  // > 0 (inventory converts to negative delta)
    MovementDate time.Time

    SourceType   string  // "invoice" | "shipment" | "adjustment"
                         // | "transfer_out" | "build_consume" | "scrap"
    SourceID     uint
    SourceLineID *uint

    // Policy
    AllowNegative bool            // default false
    CostingMethod CostingMethod   // "" = use item default; explicit overrides
    SpecificLotID *uint           // required if CostingMethod = Specific

    IdempotencyKey string
    ActorUserID    *uint
    Memo           string
}

type IssueStockResult struct {
    MovementID      uint
    UnitCostBase    decimal.Decimal  // weighted-average or blended FIFO unit
    CostOfIssueBase decimal.Decimal  // Qty × UnitCostBase; GL: Dr COGS / Cr Inventory
    CostLayers      []CostLayerConsumed  // FIFO detail; nil for WeightedAvg
}

type CostLayerConsumed struct {
    SourceMovementID uint
    Quantity         decimal.Decimal
    UnitCostBase     decimal.Decimal
    TotalCostBase    decimal.Decimal
}

func IssueStock(db *gorm.DB, in IssueStockInput) (*IssueStockResult, error)
```

### 3.3 `AdjustStock` — count variance / damage / write-off

```go
type AdjustStockInput struct {
    CompanyID    uint
    ItemID       uint
    WarehouseID  uint
    QuantityDelta decimal.Decimal  // signed: + gain, − loss
    MovementDate time.Time

    Reason       AdjustmentReason  // "count_variance" | "damage" | "expiry"
                                   // | "theft" | "other"
    // Cost: required on positive adjustments (need a unit cost for new stock);
    // ignored on negative (uses current avg).
    UnitCost     *decimal.Decimal
    CurrencyCode string
    ExchangeRate *decimal.Decimal

    SourceType     string  // "stock_count" | "write_off" | "manual"
    SourceID       uint
    IdempotencyKey string
    ActorUserID    *uint
    Memo           string
}

type AdjustStockResult struct {
    MovementID          uint
    UnitCostBase        decimal.Decimal
    AdjustmentValueBase decimal.Decimal  // signed; GL posts Inv Adj Gain/Loss
}

func AdjustStock(db *gorm.DB, in AdjustStockInput) (*AdjustStockResult, error)
```

### 3.4 `TransferStock` — warehouse-to-warehouse (inventory-internal doc)

Atomic "out of From, in to To" — guarantees both legs succeed or neither.

```go
type TransferStockInput struct {
    CompanyID       uint
    TransferID      uint             // warehouse_transfers.id
    ItemID          uint
    FromWarehouseID uint
    ToWarehouseID   uint
    Quantity        decimal.Decimal
    ShippedDate     time.Time         // drives the IssueStock leg
    ReceivedDate    *time.Time        // nil = still in transit; only Issue leg runs

    IdempotencyKey  string
    ActorUserID     *uint
    Memo            string
}

type TransferStockResult struct {
    IssueMovementID   uint
    ReceiveMovementID *uint            // nil if ReceivedDate == nil
    UnitCostBase      decimal.Decimal  // From-warehouse avg at ShippedDate
    TransitValueBase  decimal.Decimal  // Qty × UnitCostBase
}

func TransferStock(db *gorm.DB, in TransferStockInput) (*TransferStockResult, error)
```

Cost invariant: transfer does not change a product's cost basis. The unit
cost snapshotted on the IssueStock leg is applied verbatim to the
ReceiveStock leg.

### 3.5 `ReverseMovement` — returns / cancellations / error correction

Crucial property: the reversal uses the **original movement's snapshot
cost**, not the current average. A December return of a March sale reverses
March's COGS, not current-period cost.

```go
type ReverseMovementInput struct {
    CompanyID          uint
    OriginalMovementID uint
    MovementDate       time.Time

    Reason     ReversalReason  // "customer_return" | "vendor_return"
                               // | "cancellation" | "error_correction"
    SourceType string
    SourceID   uint

    IdempotencyKey string
    ActorUserID    *uint
    Memo           string
}

type ReverseMovementResult struct {
    ReversalMovementID uint
    UnitCostBase       decimal.Decimal  // copied from original
    ReversalValueBase  decimal.Decimal  // signed opposite of original
}

func ReverseMovement(db *gorm.DB, in ReverseMovementInput) (*ReverseMovementResult, error)
```

### 3.6 `ReserveStock` / `ReleaseStock` — Phase E

Reservation (SO confirmed, not shipped) is maintained on
`inventory_balances.quantity_reserved`. No new movement rows; reservation
is a live counter separate from on-hand. Signatures reserved:

```go
func ReserveStock(db *gorm.DB, in ReserveStockInput) (*ReserveStockResult, error)
func ReleaseStock(db *gorm.DB, in ReleaseStockInput) error
```

### 3.7 Event type matrix — self-check that IN is closed

| Business event                     | IN call(s)                    | Cost source          |
|------------------------------------|-------------------------------|----------------------|
| Post Bill                          | ReceiveStock × N              | Bill line            |
| Void Bill                          | ReverseMovement × N           | Original snapshots   |
| Post Invoice                       | IssueStock × N (Kit expanded) | Inventory computed   |
| Void Invoice / AR Refund           | ReverseMovement × N           | Original snapshots   |
| AR Return                          | ReverseMovement × N           | Original snapshots   |
| Vendor Return                      | ReverseMovement × N           | Original snapshots   |
| Warehouse Transfer (shipped)       | TransferStock (issue only)    | From-warehouse avg   |
| Warehouse Transfer (received)      | TransferStock (both legs)     | Same as above        |
| Inventory Build (post)             | IssueStock × N + ReceiveStock × 1 | Inventory computed |
| Inventory Unbuild                  | ReverseMovement on Build legs | Build snapshots      |
| Stock count gain                   | AdjustStock (+)               | Caller or current avg|
| Stock count loss / damage / theft  | AdjustStock (−)               | Current avg          |
| Opening balance import             | ReceiveStock (source=opening) | Caller               |
| External sync (FBA / 3PL)          | ReceiveStock / IssueStock     | Per channel mapping  |

Every row has a contractual IN call. No business scenario should write
`InventoryMovement` rows directly.

---

## 4. OUT contracts (read-only queries)

### 4.1 `GetOnHand`

```go
type OnHandQuery struct {
    CompanyID   uint
    ItemID      uint        // 0 = all
    WarehouseID uint        // 0 = all
    AsOfDate    *time.Time  // nil = now; otherwise reconstruct historical balance
    IncludeZero bool
}

type OnHandRow struct {
    ItemID            uint
    WarehouseID       uint
    QuantityOnHand    decimal.Decimal
    QuantityReserved  decimal.Decimal
    QuantityAvailable decimal.Decimal  // = OnHand − Reserved
    AverageCostBase   decimal.Decimal
    TotalValueBase    decimal.Decimal
}

func GetOnHand(db *gorm.DB, q OnHandQuery) ([]OnHandRow, error)
```

### 4.2 `GetMovements`

```go
type MovementQuery struct {
    CompanyID             uint
    ItemID                *uint
    WarehouseID           *uint
    FromDate              *time.Time
    ToDate                *time.Time
    SourceType            string
    SourceID              *uint
    Direction             *MovementDirection  // "in" | "out" | "both"
    Limit                 int
    Offset                int
    IncludeRunningBalance bool
}

type MovementRow struct {
    ID               uint
    MovementDate     time.Time
    ItemID           uint
    WarehouseID      uint
    Type             InventoryMovementType
    QuantityDelta    decimal.Decimal
    UnitCostBase     decimal.Decimal
    TotalCostBase    decimal.Decimal
    SourceType       string
    SourceID         uint
    SourceLineID     *uint
    RunningQuantity  decimal.Decimal  // iff IncludeRunningBalance
    RunningValueBase decimal.Decimal
    Memo             string
    ActorUserID      *uint
    CreatedAt        time.Time
}

func GetMovements(db *gorm.DB, q MovementQuery) ([]MovementRow, total int64, err error)
```

### 4.3 `GetItemLedger`

Single-item, single-warehouse movement report with opening/closing.

```go
type ItemLedgerQuery struct {
    CompanyID   uint
    ItemID      uint
    WarehouseID *uint   // nil = aggregated across warehouses
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
    TotalOutCostBase decimal.Decimal  // period COGS contribution
}

func GetItemLedger(db *gorm.DB, q ItemLedgerQuery) (*ItemLedgerReport, error)
```

### 4.4 `ExplodeBOM`

```go
type BOMExplodeQuery struct {
    CompanyID           uint
    ParentItemID        uint
    Quantity            decimal.Decimal
    MultiLevel          bool     // true = explode to leaves
    IncludeCostEstimate bool
    IncludeAvailability bool
    WarehouseID         *uint    // required when IncludeAvailability
}

type BOMExplodeRow struct {
    ComponentItemID        uint
    Depth                  int
    Path                   []uint           // ancestor item IDs (cycle detection audit)
    QuantityPerUnit        decimal.Decimal
    TotalQuantity          decimal.Decimal  // ParentQty × QtyPerUnit × (1+scrap)
    ScrapPct               decimal.Decimal
    EstimatedUnitCostBase  *decimal.Decimal
    EstimatedTotalCostBase *decimal.Decimal
    AvailableQuantity      *decimal.Decimal
    ShortBy                *decimal.Decimal
}

func ExplodeBOM(db *gorm.DB, q BOMExplodeQuery) ([]BOMExplodeRow, error)
```

Cycle detection: `visited` set carried through recursion; max depth 5 by
default. Returns `ErrBOMCycle` / `ErrBOMTooDeep` on violation.

### 4.5 `GetValuationSnapshot`

Point-in-time total inventory valuation — used for balance-sheet and
audit reporting.

```go
type ValuationQuery struct {
    CompanyID   uint
    AsOfDate    time.Time
    GroupBy     ValuationGroupBy  // "item" | "warehouse" | "category" | "none"
    WarehouseID *uint
}

type ValuationRow struct {
    GroupKey   string
    GroupLabel string
    Quantity   decimal.Decimal
    ValueBase  decimal.Decimal
}

func GetValuationSnapshot(db *gorm.DB, q ValuationQuery) ([]ValuationRow, totalValue decimal.Decimal, err error)
```

### 4.6 `GetAvailableForBuild`

"Given current component stock in warehouse W, how many units of assembled
item A can I build?" Returns `bottleneckItemID` — the first component to
run out.

```go
func GetAvailableForBuild(db *gorm.DB, companyID, parentItemID, warehouseID uint) (
    maxBuildable decimal.Decimal,
    bottleneckItemID uint,
    err error,
)
```

### 4.7 `GetCostingPreview`

Cost of a hypothetical issue without actually issuing. For quotations,
margin previews, pre-flight cost checks.

```go
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

func GetCostingPreview(db *gorm.DB, q CostingPreviewQuery) (*CostingPreviewResult, error)
```

---

## 5. Error taxonomy

```go
// internal/services/inventory/errors.go
var (
    ErrInsufficientStock      = errors.New("insufficient stock")
    ErrItemNotTracked         = errors.New("item does not track inventory")
    ErrInvalidWarehouse       = errors.New("warehouse not found or not active")
    ErrNegativeQuantity       = errors.New("quantity must be positive")
    ErrDuplicateIdempotency   = errors.New("idempotency key already seen")
    ErrBOMCycle               = errors.New("BOM contains a cycle")
    ErrBOMTooDeep             = errors.New("BOM depth exceeds maximum")
    ErrCostingLayerExhausted  = errors.New("FIFO layers cannot satisfy issue")
    ErrCurrencyRateRequired   = errors.New("exchange rate required for foreign-currency movement")
    ErrMovementImmutable      = errors.New("movement cannot be modified; issue a reversal")
    ErrReversalAlreadyApplied = errors.New("this movement has already been reversed")
)
```

Callers translate these to user-facing messages at the HTTP layer.

---

## 6. Schema impact

### New columns on `inventory_movements`

| Column                 | Type          | Nullable | Rationale                                  |
|------------------------|---------------|----------|--------------------------------------------|
| `currency_code`        | varchar(3)    | yes      | Document currency                          |
| `exchange_rate`        | numeric(20,8) | yes      | Document → base; default 1                 |
| `unit_cost_base`       | numeric(18,4) | yes      | Pre-computed base-currency unit cost       |
| `landed_cost_allocation` | numeric(18,2) | yes    | Apportioned freight/duty (base)            |
| `idempotency_key`      | text          | yes      | Unique partial index for non-null          |
| `actor_user_id`        | bigint        | yes      | Who triggered; nullable for system events  |
| `reversed_by_movement_id` | bigint     | yes      | Set when ReverseMovement is applied        |
| `reversal_of_movement_id`| bigint       | yes      | Set on the reversal entry; points to orig  |

Dropped:
- `journal_entry_id` — reverse coupling to GL. Lookup goes through
  `source_type + source_id → business document → document.journal_entry_id`.

Partial unique index:
```sql
CREATE UNIQUE INDEX uq_inventory_movement_idempotency
  ON inventory_movements (company_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
```

### New tables (later phases, placeholders)

- `product_components` — BOM rows. Phase D.1.
- `inventory_builds` + `inventory_build_lines` — assembly transactions.
  Phase D.2.

---

## 7. Migration plan

### Phase D.0 — API consolidation (foundation)
1. Schema migration for the new columns (all nullable; zero impact on
   existing rows).
2. Create `internal/services/inventory/` package with input/output types,
   interfaces, and `errors.go`.
3. Implement `ReceiveStock`, `IssueStock`, `AdjustStock`, `ReverseMovement`,
   `GetOnHand`, `GetMovements` on top of the existing
   `InventoryMovement`/`InventoryBalance` tables.
4. Migrate callers one by one:
   - `bill_handlers.go` → `ReceiveStock`
   - `invoice_*_handlers.go` → `IssueStock`
   - Existing `adjustment_handlers.go` → `AdjustStock`
   - `warehouse_transfer_handlers.go` → `TransferStock`
5. Remove direct `InventoryMovement.Create(...)` calls outside the
   `inventory` package.
6. Deprecate `InventoryMovement.JournalEntryID` — stop populating it;
   remove in a later cleanup commit once all readers migrate.

### Phase D.1 — BOM and Kit sales
1. `product_components` table + `ProductService.CompositionType` enum.
2. `ExplodeBOM` query.
3. Invoice post flow: if line item is a Kit, explode and issue leaves.

### Phase D.2 — Inventory Build (assembly)
1. `inventory_builds` + `inventory_build_lines`.
2. `PostInventoryBuild` orchestrator: issues components, receives finished
   good, posts the single-entry JE (all internal to an inventory workflow).
3. Unbuild via `ReverseMovement`.

### Phase E — Reservations, UoM, FIFO layering
Out of scope for the D series.

### Phase F — Serial / lot / expiry tracking
Out of scope for D/E.

---

## 8. Open questions to revisit before D.1

1. Costing method default — weighted average for all products, or
   configurable per product? Current code seems weighted-average only.
   Decision needed before FIFO implementation in Phase E.
2. Multi-currency bill with LandedCost — who owns the allocation logic?
   Today: caller computes. Revisit if allocation proves repeated across
   callers.
3. `inventory_balances` as materialized cache vs. on-demand aggregate.
   Current implementation: materialized. Fine for now; monitor for drift
   issues.

---

## 9. Guardrails (testing priorities)

A movement ledger is only as trustworthy as its reconciliation checks.
The following invariants should be verified by tests from Phase D.0
onwards:

- `SUM(QuantityDelta) per (item, warehouse) == inventory_balances.quantity_on_hand` — always.
- `SUM(TotalCostBase signed) per (item, warehouse) / quantity_on_hand ≈ average_cost` — within rounding.
- Reversal pairs: `reversal.TotalCostBase == -original.TotalCostBase`.
- Transfer: `issue.UnitCostBase == receive.UnitCostBase` on the same transfer.
- No two rows share the same `(company_id, idempotency_key)`.
- BOM explosion of any product terminates (no cycle detected, depth ≤ max).

These are integration-test territory; a small seeded dataset that exercises
each path lives in `internal/services/inventory/testdata/` (TBD).
