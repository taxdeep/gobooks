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
9. **Authoritative cost principle** — the ONLY valid source of COGS / sale
   cost for a journal entry is the result returned by `IssueStock`
   (equivalently the cost map returned by `CreateSaleMovements`).
   `GetCostingPreview` is for *pre-checks and UI estimates only*; its
   output must never be persisted as the JE amount. Today's posting flow
   obeys this: the transaction opens → `IssueStock` runs → its
   `UnitCostBase` drives `BuildCOGSFragments` → the JE is created. Any
   future mutation-intent code path must enforce the same order. The rule
   exists because any read-before-mutate window admits a concurrent
   re-average that silently de-synchronises the JE from the movement
   ledger by rounding-level amounts — which is an *accounting-truth* bug,
   not a cosmetic one.

   Restated as a directional rule:

   > *Inventory owns stock truth and cost truth; the document/posting
   > layer owns accounting truth. COGS is not the posting layer's
   > opinion — it is the value inventory returned to the posting layer.*

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
    LayerID          uint  // inventory_cost_layers row drawn from
    SourceMovementID uint
    Quantity         decimal.Decimal
    UnitCostBase     decimal.Decimal
    TotalCostBase    decimal.Decimal
}

func IssueStock(db *gorm.DB, in IssueStockInput) (*IssueStockResult, error)
```

**FIFO draws produce an audit log (Phase E2.1).** Every consumed layer is
recorded in `inventory_layer_consumption` with
`(issue_movement_id, layer_id, quantity_drawn, unit_cost_base)`.
`ReverseMovement` (§3.5) consumes this log to restore layer remainings
during voids. Weighted-average issues write nothing to this log.

**Unsupported: specific-identification costing.**
`CostingMethod=CostingMethodSpecific` returns
`"inventory.IssueStock: costing method "specific" not yet implemented"`.
Callers that need lot-level cost tracking must stay on FIFO or
weighted-avg and defer until that slice ships.

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

**FIFO layer restoration (Phase E2.1).** When the original movement is a
FIFO-costed issue, ReverseMovement walks the
`inventory_layer_consumption` rows for that movement and adds each
`quantity_drawn` back to its layer's `RemainingQuantity`, stamping the
consumption row with `reversed_by_movement_id` so a second reversal
attempt cannot double-restore. The invariant
`SUM(cost_layers.remaining_quantity) == inventory_balances.quantity_on_hand`
is preserved across post/void cycles for FIFO companies.

**Guarded: legacy FIFO issues without a consumption log.** Movements
created before Phase E2.1 have no rows in
`inventory_layer_consumption`. Reversing them still succeeds — on-hand
is correctly restored using the snapshot cost — but the FIFO layer
counters stay stale against on-hand. Phase E2.3 ships
`InspectFIFOLayerDrift` to surface these cells; the
`genesis_no_layers` subclass is auto-repaired by
`RepairFIFOLayerDrift`, while the `positive_needs_investigation`
subclass (reversed pre-E2.1 issue) is reported with an explicit
"cannot auto-restore without consumption history" note. Companies
running heavy void traffic on legacy FIFO data should schedule a
targeted manual re-seat until a repair policy is agreed.

**Guarded: cannot reverse a reversal.** Reversal rows themselves cannot
be reversed (`ReversalOfMovementID != nil` → error). Book a new forward
movement instead. This keeps the reversal chain unambiguous.

**Guarded: double reversal.** `ReversedByMovementID` on the original is
set after the first successful reverse; a second attempt returns
`ErrReversalAlreadyApplied`. Tests lock this guard for both FIFO and
weighted-avg paths.

### 3.6 `PostInventoryBuild` — assembly / manufacturing build

A build is a value-transforming event: N component items are consumed at
their current weighted-average cost, optional labor and overhead are added
in base currency, and 1 finished-good item is produced at the blended
unit cost. Thin orchestrator over `IssueStock` + `ReceiveStock`; does NOT
own a header table. A build is identified by the caller-supplied
`BuildRef` which appears as `SourceID` on both the N consume movements
(`source_type="build_consume"`) and the 1 produce movement
(`source_type="build_produce"`). The movement pair is the system of
record; a business-document layer can persist its own Build header on top
if richer reporting is needed (same pattern as `WarehouseTransfer` vs
`TransferStock`).

```go
func PostInventoryBuild(db *gorm.DB, in PostInventoryBuildInput) (*PostInventoryBuildResult, error)

type PostInventoryBuildInput struct {
    CompanyID          uint
    ParentItemID       uint            // assembly being built; must be inventory-tracked
    WarehouseID        uint            // both consume and produce hit the same warehouse
    Quantity           decimal.Decimal // finished-good units to produce; > 0
    BuildDate          time.Time
    BuildRef           uint            // caller reference; links the movement pair

    // Optional non-component costs blended into the finished-good unit cost
    // (base currency; total for the build, not per-unit).
    LaborCostBase      decimal.Decimal
    OverheadCostBase   decimal.Decimal

    // nil = read the parent's item_components (BOM)
    // non-nil = override the BOM for this single build (rework / substitutes)
    ComponentOverrides []BuildComponentInput

    IdempotencyKey string
    ActorUserID    *uint
    Memo           string
}

type PostInventoryBuildResult struct {
    ProduceMovementID uint
    UnitCostBase      decimal.Decimal // sum(component + labor + overhead) / qty
    FinishedValueBase decimal.Decimal
    ComponentCostBase decimal.Decimal // raw components only
    LaborCostBase     decimal.Decimal
    OverheadCostBase  decimal.Decimal
    Components        []BuildComponentConsumed
}
```

Atomicity: `PostInventoryBuild` does NOT open its own transaction. Callers
MUST wrap the call in `db.Transaction(func(tx *gorm.DB)…)` so a mid-build
failure (insufficient stock on component N of M) rolls every earlier leg
back. Same contract as `TransferStock`.

Idempotency: derived per-leg from the caller's build-level key —
`"<key>:produce"` for the finished good and `"<key>:consume:<itemID>"` for
each component. A replay returns the cached result reconstructed from the
persisted movements; labor / overhead collapse into the cached
`UnitCostBase` on replay.

### 3.7 `ReserveStock` / `ReleaseStock` — *shipped in Phase E1*

Reservation (SO confirmed, not shipped) is maintained on
`inventory_balances.quantity_reserved` (added by migration 058). No
movement rows are written; a reservation is a live counter separate from
on-hand. `GetOnHand` surfaces it as `QuantityReserved` and derives
`QuantityAvailable = QuantityOnHand − QuantityReserved`.

```go
func ReserveStock(db *gorm.DB, in ReserveStockInput) (*ReserveStockResult, error)
func ReleaseStock(db *gorm.DB, in ReleaseStockInput) error
```

Bounds:
- `Reserved` never goes below 0 — `ReleaseStock` past zero returns
  `ErrReservationUnderflow`.
- `Reserved` cannot exceed `OnHand` at reserve time — `ReserveStock` past
  available returns `ErrInsufficientAvailable`. (A future flag could
  permit "over-commit" backorder reservations; not in E1.)

**E1 idempotency gap (intentional).** `ReserveStockInput` still carries an
`IdempotencyKey` field for API surface stability, but the slice does not
persist per-reservation rows so replay safety is the caller's
responsibility. A later slice can introduce an `inventory_reservations`
ledger table, enforce `(company_id, idempotency_key)` uniquely, and
reconcile its SUM against the counter.

### 3.8 Tracking truth — lot / serial / expiry  *(shipped in Phase F)*

Tracking captures WHICH physical units moved, not HOW they're costed.
The two concepts are orthogonal: a lot/serial identity never substitutes
for a cost layer, and costing never consults tracking tables.

#### ProductService.TrackingMode

Each item opts into tracking via `product_services.tracking_mode`:

| Value | Meaning |
|---|---|
| `none` (default) | No tracking. Aggregate quantity semantics only. |
| `lot` | Units received together share a lot number and (optional) expiry. |
| `serial` | Every unit has its own serial identity and (optional) expiry. |

Hard rules enforced by `ValidateTrackingMode` + `ApplyTypeDefaults`:
- Non-stock items (service / non_inventory / other_charge) MUST stay on
  `none`. Stock items may be any of the three.
- `ChangeTrackingMode` refuses to switch while any on-hand OR layer
  remaining > 0 exists for the item. No silent conversion.
- Every mode change writes `AuditLog{action: "product_service.tracking_mode.changed"}`
  with before/after values.

#### Inbound tracking capture — `ReceiveStock` extensions

```go
type ReceiveStockInput struct {
    // ... existing fields ...
    LotNumber         string
    ExpiryDate        *time.Time  // lot-level; ignored for serial items
    SerialNumbers     []string
    SerialExpiryDates []*time.Time // optional, parallel to SerialNumbers
}
```

Validation matrix:

| tracking_mode | required inbound data | rejected |
|---|---|---|
| `none` | (nothing) | any lot/serial/expiry → `ErrTrackingDataOnUntrackedItem` |
| `lot` | `LotNumber` | `SerialNumbers` / `SerialExpiryDates` non-empty → `ErrTrackingModeMismatch` |
| `serial` | `len(SerialNumbers) == Quantity` | `LotNumber` / header `ExpiryDate` non-empty → `ErrTrackingModeMismatch`; duplicate live serial → `ErrDuplicateSerialInbound` |

Lot top-up rule: a second inbound of the same `(company, item, lot_number)`
increments `OriginalQuantity` and `RemainingQuantity`. A top-up with a
different `ExpiryDate` is **rejected** — reusing a lot number across
different shelf lives would make the expiry field meaningless.

#### Outbound tracking — `IssueStock` extensions

```go
type IssueStockInput struct {
    // ... existing fields ...
    LotSelections    []LotSelection   // {LotID, Quantity}; lot-tracked only
    SerialSelections []string         // serial-tracked only
}
```

- Lot: `SUM(LotSelections.Quantity) == Quantity`; each lot must have
  enough `RemainingQuantity`.
- Serial: `len(SerialSelections) == Quantity`; each must be in
  `on_hand` state for the same `(company, item)`.
- **No allocator.** Phase F does not ship FEFO or any implicit
  lot-picking. Callers supply explicit selections.
- Every lot draw decrements `inventory_lots.RemainingQuantity`; every
  serial issue flips `CurrentState` from `on_hand` to `issued`.
- One `inventory_tracking_consumption` row per draw links it to the
  issuing movement — the anchor ReverseMovement uses to unwind.

#### Reversal — layer restoration by anchor

On `ReverseMovement` of a tracked outbound:
- `inventory_tracking_consumption` rows for the movement are loaded.
- Lot rows: `RemainingQuantity` incremented back.
- Serial rows: `CurrentState` flipped back from `issued` to `on_hand`.
- Consumption rows stamped with `ReversedByMovementID` so a second
  reversal attempt cannot double-restore.
- **If no anchors exist for a tracked reversal, `ErrTrackingAnchorMissing`
  is returned.** Same correctness-gate stance as E2.1 for FIFO layers —
  legacy or missing trace data does not get "looks plausible" auto-
  repair.

#### Default policies (locked) vs future configurable policies

The six decisions below are deliberate scope boundaries, not missing
features. Anything in the "Default (today)" column is the canonical
behaviour the codebase must preserve; anything in "Future configurable"
is deferred work with an anchor name — do not implement until the
corresponding slice is greenlit.

| Area | Default (today) | Future configurable |
|---|---|---|
| Expired stock issue | Visibility + warning. `GetExpiringLots` / `GetExpiringSerials` surface risk; `IssueStock` does NOT block on expiry. | Per-company policy column selecting `warn_only` (default) / `hard_block` / `allow_with_override`. Override path must write a deviation audit. |
| FEFO / auto-allocator | None. `IssueStock` always requires explicit `LotSelections` / `SerialSelections`. Recommendation layers MAY suggest lots but the authoritative selection stays in the caller. | Advisory-only suggestion API. **No silent auto-allocator, ever.** If/when promoted, opt-in per call, never implicit. |
| Serial expiry required | Optional on both lot and serial rows. | Per-item / per-company "expiry required" flag for regulated industries (pharma, biologics, implants). |
| Tracking-mode conversion with existing on-hand | Rejected — `ChangeTrackingMode` returns `ErrTrackingModeHasStock`. Operators drain stock and re-receive. | **Not scheduled.** An opening-tracking initialization wizard would bulk-create lot/serial rows; must NOT use the H2 synthetic-genesis pattern blindly — tracking truth demands real unit identity. |
| E2.3b positive-with-layers drift repair | Manual-only. `InspectFIFOLayerDrift` surfaces the drift; `RepairFIFOLayerDrift` does NOT touch this class. | Deferred until a real incident forces the policy choice. An "assisted repair workspace" could drive Inspect-based manual selections, never auto-repair. |
| Tracked TransferStock / PostInventoryBuild | Guarded fail: IssueStock rejects with `ErrLotSelectionMissing` / `ErrSerialSelectionMissing` bubbled up. | First-class support. If prioritised later: **TransferStock before Build** (transfers are more operationally fundamental). |

#### Guarded / unsupported (call sites that fail loud today)

- Tracked TransferStock / PostInventoryBuild — see row above.
- `CostingMethodSpecific` — still `not yet implemented`. Phase F does
  NOT use lot/serial identity as a cost identity (tracking truth ≠ cost
  truth).
- Reserve-a-specific-serial — reservation counter stays item-level.
- Tracking-mode flip with on-hand — rejected by design.

Locked by tests:
`TestTransferStock_OnLotTrackedItem_FailsWithoutSelections`,
`TestPostInventoryBuild_TrackedComponent_RejectsWithoutSelections`,
`TestTracking_CostingOrthogonality_FIFOCompanyLotTracked`
(proves tracking draws and FIFO layer draws are independent on the
same issue).

### 3.9 Event type matrix — self-check that IN is closed

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
| Inventory Build (post)             | PostInventoryBuild (IssueStock × N + ReceiveStock) | Inventory computed |
| Inventory Unbuild                  | ReverseMovement on Build legs | Build snapshots      |
| Stock count gain                   | AdjustStock (+)               | Caller or current avg|
| Stock count loss / damage / theft  | AdjustStock (−)               | Current avg          |
| Opening balance import             | ReceiveStock (source=opening) | Caller               |
| External sync (FBA / 3PL)          | ReceiveStock / IssueStock     | Per channel mapping  |

Every row has a contractual IN call. No business scenario should write
`InventoryMovement` rows directly.

For items where `tracking_mode != "none"`, every IN call MUST carry the
matching tracking data (lot/serial/expiry for inbound; selections for
outbound). The IN layer rejects mismatches — there is no silent
fall-through to untracked semantics.

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

**Historical valuation dispatch (Phase E3).** When `AsOfDate` is set,
`GetOnHand` picks the algorithm based on the company's
`inventory_costing_method`:

| Costing method | As-of algorithm |
|---|---|
| `moving_average` | `replayHistoricalValue` — walk every movement chronologically, apply each row's recorded `UnitCostBase` signed by `QuantityDelta`, derive running avg. |
| `fifo` | `historicalFIFOValue` — for each layer that existed by `AsOfDate`, subtract `SUM(consumption.quantity_drawn)` where the issue movement happened ≤ asOfDate and the reversal (if any) happened > asOfDate. Value = Σ(remaining × layer.unit_cost_base). |

Cross-method aliasing is explicitly forbidden: a FIFO company must NOT
be served weighted-avg replay as an approximation. Moving-average
callers must NOT consume layer state. The two algorithms are incommensurable
past the first issue.

**Guarded: FIFO-company historical value before Phase E2.1.** Historical
valuation for a FIFO company whose data predates the
`inventory_layer_consumption` log will over-report — layers appear fully
intact regardless of draws because the draw history is missing.
`InspectFIFOLayerDrift` (§E2.3 in the migration plan) detects this class
as `positive_needs_investigation` but does not auto-repair — restoring
the specific layers the original issue drew from requires the history
that got lost. The `genesis_no_layers` case (FIFO company migrating
from pre-migration-059 data) IS auto-repaired by
`RepairFIFOLayerDrift`.

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

`OpeningUnitCost` / `OpeningValueBase` / `ClosingUnitCost` /
`ClosingValueBase` all route through the same Phase E3 dispatcher as
`GetOnHand` (§4.1) — moving-average companies get a replay, FIFO
companies get layer-based point-in-time value. The same guards apply.

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
margin previews, pre-flight stock-availability checks.

**Not a persisting source of truth.** Per §2.9 the result of this function
must never be written to a journal entry or any other authoritative
record. The only valid flow is: preview → friendly error if infeasible →
open tx → `IssueStock` → build JE from the returned cost. `WarehouseID=0`
means "aggregate across warehouses" (blended company-level avg), matching
`GetOnHand`'s semantics.

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

### 4.8 Tracking inquiry  *(shipped in Phase F4)*

Read-only views over the lot / serial / expiry tables. All queries are
company-scoped unconditionally — passing a wrong company never leaks
another tenant's data.

```go
// Lot inventory inspection. includeZero=false hides drained lots.
func GetLotsForItem(db *gorm.DB, companyID, itemID uint, includeZero bool) ([]LotInfo, error)

// Serial units for an item, optionally filtered by state(s).
// Passing nil for stateFilter returns all states.
func GetSerialsForItem(db *gorm.DB, companyID, itemID uint, stateFilter []models.SerialState) ([]SerialInfo, error)

// Traceability: lot/serial anchors tied to a specific outbound
// movement. Reversed anchors remain in the result with
// ReversedByMovementID populated so auditors see full history.
func GetTracesForMovement(db *gorm.DB, companyID, movementID uint) ([]TraceEntry, error)

// Trace every tracking draw for (item, [fromDate, toDate]).
func GetTracesForItem(db *gorm.DB, companyID, itemID uint, fromDate, toDate time.Time) ([]TraceEntry, error)

// Lots / serials whose expiry ≤ asOf + withinDays AND remaining > 0
// (lot) or live state (serial). Ordered expiry ASC; already-expired
// rows report negative DaysUntilExpiry.
func GetExpiringLots(db *gorm.DB, companyID uint, asOf time.Time, withinDays int) ([]ExpiringLotRow, error)
func GetExpiringSerials(db *gorm.DB, companyID uint, asOf time.Time, withinDays int) ([]ExpiringSerialRow, error)
```

**Scope — visibility only (locked policy).** These queries do NOT block
outbound on already-expired stock. The default expiry policy is
**warn only**; `IssueStock` is intentionally not coupled to expiry in
this slice. Per-company `warn_only | hard_block | allow_with_override`
is catalogued as a future configurable surface — see §7 "Phase F
post-decision upgrade anchors".

**FEFO is advisory only (locked policy).** These queries expose the
raw data that an advisory layer can use to suggest FIFO / FEFO lots
to operators. The inventory module itself never auto-picks. Explicit
`LotSelections` / `SerialSelections` remain the authoritative source
for `IssueStock`. Any future recommendation surface is opt-in per
call and wraps these queries — never replaces them.

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

### Phase D.1 — BOM and Kit sales  *(shipped)*
1. Used the existing `item_components` table (discovered during design) —
   no new schema needed. `ProductService.ItemStructureType`
   (`single` / `bundle` / `assembly`) is the canonical structure enum.
2. Real `ExplodeBOM` with cycle detection + depth cap (5), optional cost
   estimate and availability enrichments.
3. `GetAvailableForBuild` reports the bottleneck component.
4. Kit / bundle sales cascade via the pre-existing `bundle_service.go`
   path; no new work needed at the sales side.

### Phase D.2 — Inventory Build (assembly)  *(shipped)*
1. **No new tables.** The design decision was to treat a build as a pair
   of movements sharing a `SourceID` — the movement ledger IS the system
   of record. A future business-document layer can add a Build header if
   needed; the inventory module does not require one.
2. `PostInventoryBuild` orchestrator in
   `internal/services/inventory/build.go`: issues N components (at
   current avg cost), receives the finished good at the blended unit
   cost (component cost + labor + overhead). Returns the full cost
   breakdown so the caller can post its own GL entries.
3. Unbuild via `ReverseMovement` on each build leg (same pattern as
   voiding any other document).

### Phase D cleanup  *(shipped)*
1. Dropped the legacy `InventoryMovement.JournalEntryID` reverse coupling
   (migration 057).
2. Retired the legacy `CostingEngine` from production code paths;
   `ValidateStockForInvoice` now uses `inventory.GetCostingPreview`. The
   engine survives as a test-only fixture until the remaining
   `phase_*_inventory_test.go` suites are ported.
3. Removed the unused `jeID` parameter from `CreatePurchaseMovements`,
   `CreateSaleMovements`, `ReverseSaleMovements`,
   `ReversePurchaseMovements` — GL linkage resolves through
   `source_type + source_id -> document -> document.journal_entry_id`.
4. Idempotency keys generated by the facades are now versioned
   (`:v<n>`) so a voided document can, in the future, be re-posted
   without colliding against its prior movements. The version is picked
   once per post attempt via `nextIdempotencyVersion`.

### Phase E0 — Correctness hardening  *(shipped)*
Gate before any Phase E feature work.

1. **Authoritative COGS** — restructured `PostInvoice` so that
   `CreateSaleMovements` (which calls `IssueStock`) runs *before* the JE
   is created. The returned per-item cost map drives
   `BuildCOGSFragments`, guaranteeing JE COGS equals
   `UnitCostBase × |QuantityDelta|` exactly. Test
   `TestPostInvoice_COGSAgreesWithMovementUnitCostBase` locks the
   invariant. Side-benefit: removes the pre-existing double-FX-scaling of
   COGS on foreign-currency invoices, since COGS is now built inside the
   tx after FX scaling runs on the non-COGS side only.
2. Stale web tests fixed (bill-post redirect, JE base-imbalance → anchor
   absorption, report-cache invalidation test fixtures).

### Phase E1 — Reservations  *(shipped)*
1. Migration 058 adds `inventory_balances.quantity_reserved` (non-null,
   default 0).
2. `ReserveStock` / `ReleaseStock` implemented as atomic counter
   operations in `internal/services/inventory/reserve.go`. No movement
   rows; the counter is authoritative.
3. `GetOnHand` now populates `QuantityReserved` and
   `QuantityAvailable = OnHand − Reserved`.
4. New sentinel errors `ErrInsufficientAvailable` /
   `ErrReservationUnderflow`.
5. **Known gap:** `IdempotencyKey` is on the input struct but not yet
   enforced — callers own replay safety in E1. A future slice can add an
   `inventory_reservations` ledger table to close this.

### Phase E2 — FIFO layers  *(shipped)*
1. Migration 059 adds `inventory_cost_layers` — one row per
   `ReceiveStock`, tracking `original_quantity`, `remaining_quantity`,
   `unit_cost_base`, and `received_date` for FIFO ordering.
2. Every receipt writes a layer regardless of the company's costing
   method, so switching a company to FIFO later starts from the correct
   historical stack.
3. `IssueStock` with `CostingMethod=CostingMethodFIFO` draws from the
   oldest available layers (ordered by `received_date, id`) and populates
   `CostLayerConsumed[]` on the result. Blended `UnitCostBase =
   Σ(consumed_qty × layer_unit_cost) / total_qty`.
4. Weighted-average callers are untouched — layers accumulate silently.

### Phase E2.1 — FIFO correctness gate  *(shipped)*
Required before FIFO can be considered production-ready.

1. Migration 060 adds `inventory_layer_consumption` — per-layer draw log
   linking `issue_movement_id` → `layer_id` with `quantity_drawn`,
   `unit_cost_base`, and a nullable `reversed_by_movement_id`.
2. `IssueStock` under FIFO writes one consumption row per touched layer
   after the movement row persists.
3. `ReverseMovement` on a FIFO issue walks the consumption rows, adds
   each `quantity_drawn` back to its layer's `RemainingQuantity`, and
   stamps the consumption row's `reversed_by_movement_id` so a second
   reversal can't double-restore. Invariant
   `SUM(cost_layers.remaining_quantity) == inventory_balances.quantity_on_hand`
   holds across void cycles.
4. Weighted-average path unaffected (no consumption rows written, none
   read).
5. **Guarded: legacy FIFO data (pre-E2.1).** Issues that predate the
   consumption log fall back to snapshot-cost reversal on on-hand but
   leave layer counters stale. Reconcile job needed for drift repair;
   scheduled as E2.3.
6. **Unsupported: specific-identification costing.**
   `CostingMethodSpecific` still returns
   `not yet implemented`. Layer-aware but per-lot selection requires its
   own slice (E2.4).

### Phase E2.3 — FIFO layer drift reconcile  *(shipped, scoped)*

1. `InspectFIFOLayerDrift(db, companyID)` — read-only scan of every
   balance row; reports each cell where
   `SUM(cost_layers.remaining) != quantity_on_hand`. Each report carries
   a `Drift` value and a `LayerRowCount`, plus diagnostic notes naming
   the drift class.
2. `RepairFIFOLayerDrift(db, companyID)` — Inspect + bounded auto-fix.
3. Three drift classifications:
   - `genesis_no_layers` — positive drift with zero layer rows.
     Happens when a company flips to FIFO with pre-migration-059
     inventory. **Auto-repaired** by synthesizing one layer at the
     balance's current avg cost (see "Synthetic genesis layer
     provenance" below).
   - `positive_needs_investigation` — positive drift with existing
     layers. Typically a post-E2.1 reversal of a pre-E2.1 issue.
     **Not auto-repaired** — restoring layer remainings correctly
     requires knowing which layer the original issue drew from, which
     is precisely the history that got lost. A future slice (E2.3b) can
     introduce a policy (restore to youngest layer, synthesize a new
     layer at period avg cost, …) once operators pick one.
   - `negative_needs_investigation` — layers exceed on-hand.
     **Not auto-repaired** — typically a double-reversal or hand-edit.
4. Both functions guard: only FIFO companies may call them. Moving-avg
   companies get a clear error — drift there is designed, not a bug.
5. `RepairFIFOLayerDrift` refuses to synthesize a genesis layer when
   the cell has no inbound movement at all (data anomaly); the report
   surfaces the refusal instead of silently fabricating stock.

**Synthetic genesis layer provenance (Phase H2 / migration 061).**
Every `inventory_cost_layers` row now carries two provenance fields:

| Field | Receipt layer | Synthetic genesis layer |
|---|---|---|
| `IsSynthetic` | `false` | `true` |
| `ProvenanceType` | `"receipt"` | `"synthetic_genesis"` |
| `SourceMovementID` | Authoritative — the inbound movement that created the layer | **FK anchor only** — an oldest-inbound movement picked to satisfy NOT NULL |

**Reading rule** — any report / audit / traceability path that needs to
attribute a layer to a real event MUST branch on `ProvenanceType` /
`IsSynthetic`. `SourceMovementID` on a synthetic row points at
*whichever* inbound was handy; it is NOT provenance. A DB CHECK
constraint enforces that the two fields stay in lock-step
(`chk_inventory_cost_layers_provenance_consistency` in migration 061).

**Repair semantics locked by tests**:
- `TestRepairFIFOLayerDrift_GenesisLayer_HasExplicitProvenance` — repair
  sets `IsSynthetic=true, ProvenanceType=synthetic_genesis`.
- `TestRepairFIFOLayerDrift_GenesisRepair_Idempotent` — running repair
  twice on the same cell does not create a second synthetic layer or
  mutate the first (drift=0 after first run → second run skips).
- `TestRepairFIFOLayerDrift_GenesisRollback_NoPartialState` — when the
  anchor lookup fails (orphan balance, no inbound movement), the
  transaction rolls back cleanly: no layer written, balance untouched.

### Phase E2 remaining slices — still pending
- **E2.2 (unsupported — specific-identification costing).** Requires
  lot-aware IssueStock to honour `SpecificLotID`.
- **E2.4 (lot tracking).** Phase F; serial / lot / expiry surface.

### Phase E3 — Historical valuation  *(shipped, hardened to method)*
1. Two algorithms, one dispatcher:
   - `replayHistoricalValue(...)` — weighted-average replay walking
     every movement chronologically, using each row's recorded
     `UnitCostBase`.
   - `historicalFIFOValue(...)` — layer-based point-in-time value. For
     each layer that existed by `asOfDate`, subtract the live net
     consumption (issue date ≤ asOfDate, reversal date > asOfDate or
     null) and value the remaining units at the layer's unit cost.
2. `historicalValueAt(...)` picks the algorithm by looking up the
   company's `inventory_costing_method`. Hardened: FIFO companies are
   NEVER served weighted-avg replay under any fallback.
3. `GetOnHand(AsOfDate=…)` and `GetItemLedger` route through the
   dispatcher. Moving-avg companies get their running-average history
   populated; FIFO companies get true layer state.
4. Legacy rows (pre-migration-056) with `UnitCost` but no
   `UnitCostBase` fall back to `UnitCost` in weighted-avg replay.

### Phase E4 — CostingEngine retirement  *(partially shipped)*
1. All production call sites already migrated off the legacy engine in
   earlier slices (E0.2 invoice COGS, D cleanup preview, D facades).
2. `CostingMethod` enum in `services/` now includes
   `CostingMethodFIFO` so callers touching the company field can reference
   it symbolically. The authoritative costing enum is
   `services/inventory.CostingMethod` — `services.CostingMethod` is kept
   for the Company column only.
3. The doc-comment at the top of `costing_engine.go` now states in plain
   terms that new callers must NOT use the engine.

**Rule — legacy engine is fixture-only.** The remaining
`CostingEngine` surface is regression scaffolding: usable as a test
seeder or to assert parity between legacy behavior and the new IN verbs.
It must not be the truth oracle for any new feature, and code that
shapes a new business flow around it fails review.

**E4.1 — full engine removal.** Deferred. The `CostingEngine` interface,
`MovingAverageCostingEngine` struct, `InboundRequest/Result`,
`OutboundRequest`, and the `phase_*_inventory_test.go` +
`costing_engine_test.go` suites still exist. Removing them is a
standalone mechanical porting slice (~2,000 lines). No correctness debt
— just dead-code hygiene. Schedule independently.

### Phase F — Serial / lot / expiry tracking  *(shipped)*

Foundation-quality tracking. Shipped in five slices:

**F1 — Tracking model foundation.** Migration 062 adds
`product_services.tracking_mode` with CHECK `IN ('none','lot','serial')`.
Migrations 063 / 064 create `inventory_lots` and
`inventory_serial_units` respectively, with their own uniqueness
(unique `(company, item, lot_number)` for lots; partial-unique
`(company, item, serial_number) WHERE state IN live_states` for
serials). `ValidateTrackingMode` / `ApplyTypeDefaults` enforce that
non-stock items stay on `none`. `ChangeTrackingMode` (service) refuses
to switch while stock exists and writes an audit log entry on every
change. Locked by `TestApplyTypeDefaults_NonStockForcedToNone`,
`TestChangeTrackingMode_BlockedByOnHand`,
`TestChangeTrackingMode_BlockedByLayerRemaining`.

**F2 — Inbound tracked stock.** `ReceiveStock` validates + persists
lot / serial / expiry alongside (not inside) the existing cost-layer
path. Lot top-up semantics; per-serial expiry; rejects mismatches with
sentinel errors (`ErrTrackingDataMissing`, `ErrTrackingDataOnUntrackedItem`,
`ErrSerialCountMismatch`, `ErrDuplicateSerialInbound`,
`ErrTrackingModeMismatch`). Untracked items keep working unchanged.

**F3 — Outbound tracked issue + exact reversal.** Migration 065 adds
`inventory_tracking_consumption` — one row per lot/serial consumed by
an outbound movement. Exactly one of `lot_id` / `serial_unit_id` is
set per row (CHECK constraint). `IssueStock` demands explicit
selections for tracked items (no allocator). `ReverseMovement` unwinds
anchors exactly: restores lot remaining or flips serial state back to
on_hand, stamps `ReversedByMovementID`. Tracked reversal with no
anchors returns `ErrTrackingAnchorMissing` — same correctness-gate
stance as E2.1 for FIFO cost layers.

**F4 — Tracking inquiry / traceability / expiry visibility.**
`GetLotsForItem`, `GetSerialsForItem`, `GetTracesForMovement`,
`GetTracesForItem`, `GetExpiringLots`, `GetExpiringSerials`. All
company-scoped. Visibility-only — expiry does not yet block outbound.

**F5 — Closeout / integration guardrails.** Self-audit + docs. Key
cross-slice invariants verified:
- Tracking truth and cost truth are independent paths on the same
  `IssueStock` call (lot consumption + FIFO layer consumption write
  distinct tables, exact-cardinality different by design).
- TransferStock / PostInventoryBuild on tracked items fail loud
  because their IssueStock legs don't populate selections — documented
  unsupported until a first-class tracked transfer/build ships.

### Phase F deferred / unsupported

- **Tracked TransferStock + PostInventoryBuild.** Current behavior:
  hard rejection (ErrLotSelectionMissing / ErrSerialSelectionMissing).
  If promoted later, **TransferStock before PostInventoryBuild**.
- **Specific-identification costing.** `CostingMethodSpecific` still
  "not yet implemented". Tracking is tracking; costing is costing.
- **Tracking mode conversion tool.** No migration path for items with
  existing on-hand.
- **FEFO / auto-allocator.** Not implemented; callers pick explicitly.
  See "FEFO advisory-only" lock in §3.8.
- **Expiry policy** (warn / hard block / override). Current default:
  warn only. Per-company configurable policy is a future anchor below.
- **Reserve-a-specific-serial.** Reservation counter remains
  item-level.

### Phase F post-decision upgrade anchors

Catalogued with explicit names so a future slice can pick them up
without rediscovering the requirements. **None are scheduled.**

1. **Item-level expiry policy (per-item / per-company).** Surface:
   `product_services.expiry_required` (bool) + `companies.expiry_issue_policy`
   (`warn_only` default | `hard_block` | `allow_with_override`). Touches
   `IssueStock` validation only. Override path MUST write a deviation
   audit (see #2). Regulated industries (pharma, biologics, implants)
   are the primary callers.

2. **Deviation audit.** When `allow_with_override` is active and an
   operator issues expired stock, a dedicated audit row captures
   actor, item, lot/serial, expiry_date, override reason. Required
   before #1's `allow_with_override` mode can be considered safe.

3. **Opening tracking initialization wizard.** Bulk-create lot / serial
   rows for items with existing on-hand when a company onboards
   tracking. The wizard MUST capture real unit identities from the
   operator (serials, lot numbers) rather than synthesize them. This
   is explicitly NOT a copy of H2's synthetic-genesis pattern —
   tracking truth demands real identity, not FK-anchor placeholders.

4. **Assisted repair workspace.** Operator-facing UI driving
   `InspectFIFOLayerDrift` output into manual selections for the
   `positive_needs_investigation` drift class (E2.3b). Never
   auto-repairs; every resolution is an explicit operator action with
   its own audit trail.

These anchors share one rule: each must be its own slice with a
deliberate correctness review. They are NOT candidates for convenience
add-ons to future feature work.

### Phase G — Bill inventory-grade lifecycle + tracking integration  *(in progress)*

Transitional bridge-to-Receipt phase. **This is not "better Bill
inventory"** — the goal is to stop the correctness-debt bleed while
Phase H's Receipt-first model is built. Exit conditions are fixed
from the first slice; see the authority baseline for the full
contract.

#### Slice G.1 — `tracking_enabled` capability gate  *(shipped)*

First concrete implementation of the F.7 capability-gate pattern:

1. Migration 066 adds `companies.tracking_enabled BOOLEAN NOT NULL
   DEFAULT FALSE`. Existing companies stay safe by default.
2. `ChangeTrackingMode` enforces the gate before item-level checks
   when `NewMode != TrackingNone`. The "flip-down-to-none" direction
   stays unconditionally allowed (reducing tracking footprint never
   introduces tracking truth).
3. `ChangeCompanyTrackingCapability(companyID, enabled, actor)` is
   the audited admin surface:
   - Enabling: unconditional (company must exist).
   - Disabling: rejected if any product_service still has
     `tracking_mode != 'none'` — refuses to silently orphan live
     tracking data.
   - Every effective flip writes an audit row
     (`company.tracking_capability.enabled` / `.disabled`) with
     before/after state.
4. New sentinels `ErrTrackingCapabilityNotEnabled` /
   `ErrTrackingCapabilityHasTrackedItems`.

Closes the §F.1 foot-gun documented in the Phase F closeout: no admin
can flip an item into lot/serial tracking mode until the company
owner has explicitly opted-in at the company level, and even then
only while business-document integration for tracked items is
understood to be on the operator's side.

Tests locking the semantics:
`TestChangeTrackingMode_BlockedByCapabilityGate`,
`TestChangeTrackingMode_GateOffButReturnToNone_Allowed`,
`TestChangeCompanyTrackingCapability_EnableWritesAuditAndUnlocksGate`,
`TestChangeCompanyTrackingCapability_DisableBlockedByTrackedItems`,
`TestChangeCompanyTrackingCapability_DisableAllowedWhenNoItemsTracked`,
`TestChangeCompanyTrackingCapability_NoOpWhenAlreadyInState`.

#### Slice G.2 — `ValidateStockForInvoice` tracking-aware  *(shipped)*

Invoice preview now rejects tracked items early with
`ErrTrackedItemNotSupportedByInvoice` instead of letting the IssueStock
tracking guard surface a raw sentinel at post time. Both single lines
and bundle-expanded components are checked. The error message names
the item and the tracking mode, and points at the Phase I shipment-
driven flow as the correct future path. Locked by
`TestValidateStockForInvoice_RejectsTrackedSingleLine`.

#### Slice G.3 — Bill lifecycle codification  *(shipped — doc anchor)*

The Bill state machine already existed in code (`bill_post.go` /
`bill_void.go` enforce transitions). G.3 codifies the canonical states
and transitions in this spec so downstream phases build on a fixed
contract rather than on the current handler's implicit assumptions.

**States (`models.BillStatus`):**

| State | Meaning |
|---|---|
| `draft` | Creator editing; no GL, no inventory, no AP. |
| `posted` | JE generated, AP recorded. For stock bills this ALSO creates inventory movements (transitional — will narrow in Phase H). |
| `partially_paid` | Posted + at least one payment applied; balance > 0. |
| `paid` | Posted + fully paid; balance = 0. |
| `voided` | Reversed. Source-doc identity preserved, JE + inventory movements fully reversed (E2.1 anchor-driven). |

**Valid transitions (enforced by services today):**

```
draft      → posted   (via PostBill; requires Status==draft)
posted     → partially_paid   (via payment application)
posted     → paid             (via full payment)
partially_paid → paid
posted     → voided   (via VoidBill; requires Status∈{posted,partially_paid})
partially_paid → voided
```

Terminal states: `paid`, `voided`. `draft` can also be deleted outright
(no audit trail required since nothing has posted).

**What G.3 does NOT add (deliberately deferred):**
- `submitted` state + approval workflow — Phase J control hardening
- `cancelled` distinct from `voided` — voided already covers both
  "post then reverse" and "never-posted cancelled". If a future
  control slice needs them distinct, that's a Phase J decision.

Every G.4+ slice that touches Bill must preserve these transitions.
Adding a new state requires its own slice + migration + doc update —
not a quiet enum extension.

#### Slice G.4 — Bill line tracking receipt data  *(shipped, lot-only)*

Migration 067 adds `bill_lines.lot_number` and `bill_lines.lot_expiry_date`.
`CreatePurchaseMovements` now forwards these to `ReceiveStock`, landing
the lot into `inventory_lots` per F2 rules (create-or-top-up).

**Supported:** lot-tracked items on Bill lines. Operator captures
lot + optional expiry at line level; the post-bill flow persists the
lot end-to-end.

**Deliberately NOT supported:** serial-tracked items via Bill. The Bill
format has no natural N-serials-per-line surface, and serialized items
typically arrive through a dedicated receipt flow (Phase H Receipt).
Serial-tracked Bill lines continue to fail loud at
`inventory.validateInboundTracking` with `ErrTrackingDataMissing` —
the pre-existing F2 guard acts as the backstop and is the intended
outcome until Phase H.

Tests locking the three cases:
- `TestCreatePurchaseMovements_LotTrackedBillLine_PersistsLot`
  (happy path: lot_number + expiry → inventory_lots row)
- `TestCreatePurchaseMovements_LotTrackedBillLine_MissingLotRejected`
  (operator forgot to fill lot → bubbles `ErrTrackingDataMissing`)
- `TestCreatePurchaseMovements_SerialTrackedBillLine_RejectedAsUnsupported`
  (serial via Bill → guarded fail, anchors the unsupported edge)

Phase G.4 is **transitional** by §C.G.1. These fields live on BillLine
only because Bill is still forming inventory in Phase G. In Phase H
they migrate to ReceiptLine and BillLine reverts to financial-only.

#### Slice G.5 — Permission catalog naming  *(shipped)*

See §10 "Permission catalog naming" below. Convention anchor set;
implementation lands in Phase J.

Phase G carries a hard **exit condition** from day one: new companies
adopt Receipt-first immediately after Phase H stabilises; old companies
retain Bill-direct-to-inventory only behind a legacy flag. Phase G is
not an architecture; it's a transition window.

### Phase H — Receipt as first-class inventory document  *(scope locked, H.0 pinned)*

Phase G's transitional exit condition now comes due. Phase H lifts
Receipt to a first-class inventory document, retires Bill's long-term
role as an inventory-truth producer, and introduces GR/IR clearing as
the accounting bridge between receiving goods and receiving an
invoice. These three shifts are coupled by design: each individually
is incomplete, and any one without the other two produces a broken
half-state. Phase H is **not** "better Bill receiving"; it is the
architectural settle-up that Phase G was designed to survive until.

#### Scope

1. New document type: `Receipt` + `ReceiptLine` (company-scoped,
   draft/posted lifecycle, minimum-viable CRUD). First-class — not a
   Bill attachment, not a field container hung off `bill_lines`.
2. New capability flag: `companies.receipt_required BOOLEAN NOT NULL
   DEFAULT FALSE`. Installed as a **dormant rail** in H.1 — the
   column and the audited admin surface exist; no real company is
   flipped; the flag does not gate anything operationally until H.5.
3. Receipt posting produces inventory truth
   (`ReceiveStockFromReceipt`) and accrues `Dr Inventory / Cr GR/IR`
   at the business-document layer.
4. Tracked inbound data (`lot_number`, `lot_expiry_date`,
   `warehouse_id`) migrates in *semantic ownership* to ReceiptLine.
   The Phase G.4 columns on `bill_lines` are preserved physically
   for legacy companies but stop being the authoritative source once
   `receipt_required=true`.
5. Bill posting, under `receipt_required=true`, no longer forms
   inventory. Bill clears GR/IR against posted Receipts; unmatched /
   partial / over / under cases have explicit rules. Price deltas
   flow to a Purchase-Price-Variance (PPV) account in H.5.

#### Non-scope

- **Sell-side Shipment / tracked invoicing / customer returns.**
  All sell-side document separation is Phase I. Phase H does not
  touch the outbound path.
- **Manufacturing receipt / work-order receipt.** Build-side
  tracked inbound is Phase K.
- **Admin UI for `receipt_required`.** No customer-visible toggle
  ships in Phase H. Enablement stays engineering-only until H.5
  locks safety. A UI lands separately, deliberately after H.5.
- **Bulk backfill of historical Bills into synthetic Receipts.**
  Legacy receiving history stays on Bill. Phase H is forward-
  looking; it does not rewrite the past.
- **Changes to Bill's state machine.** §Phase G.3 contract stands
  verbatim — `draft → posted → partially_paid → paid` and
  `posted|partially_paid → voided`. Phase H changes what Bill
  *does* at post, never what states it inhabits.
- **Multi-company or cross-warehouse Receipt.** One Receipt lives
  in one company, lands in one warehouse. Split / transfer /
  multi-warehouse receiving is explicitly not a Phase H shape.
- **`inventory_costing_method` changes.** FIFO / moving-average
  semantics are untouched. Receipt feeds the same costing pipeline
  that Bill fed in Phase G.
- **Permission catalog implementation.** `receipt.*` permission
  strings are reserved (§10 naming convention) but their
  enforcement lands with Phase J.

#### Exit conditions (all must hold before Phase H closes)

1. `receipts` + `receipt_lines` tables persisted, CRUD surfaced,
   lifecycle tested (draft → posted, posted → voided).
2. `receipt_required` rail present, audited on flip, and **NOT
   flipped ON for any real company** — dormant by design until
   H.5.
3. Under `receipt_required=true` (test companies only, during
   engineering verification):
   - Receipt posting forms inventory movements **and** a
     business-document-layer journal `Dr Inventory / Cr GR/IR`.
   - Bill posting forms AP only; `CreatePurchaseMovements` is not
     called; no inventory movement, no stock row touched by Bill.
   - Bill post attempts GR/IR clearing against matching Receipts;
     differences land in PPV. Over / under / partial cases have
     deterministic outcomes locked by test.
   - BillLine tracking fields (`lot_number` / `lot_expiry_date` /
     `warehouse_id`) are not read by the inventory integration
     and not written by any new code path.
4. Under `receipt_required=false` (legacy default, every existing
   company), Phase G behavior is **byte-identical** — no
   regression, no stealth migration, no audit noise from flags
   that didn't flip.
5. Smoke suite covers at minimum: happy-path Receipt →
   matching-Bill clearing, over-match, under-match, unmatched-
   Bill (GR/IR accrual with no clearing yet), unmatched-Receipt
   aging, tracked Receipt (lot + expiry), serial-tracked Receipt
   (the pattern Phase G.4 could not support).
6. Only **after** all of the above, `receipt_required=true`
   becomes a permitted operational state. No company flip before
   H.5 close, under any pretext.

#### Three hard rules (non-negotiable)

1. **Receipt is the inventory-truth entry.** Under Phase H's
   receipt-first semantics, inbound `ReceiveStock` originates from
   Receipt posting. Bill MAY carry tracking fields for legacy
   (`receipt_required=false`) companies, but the inventory module
   treats Bill-originated inbound as a legacy path gated by the
   flag. Any new code path that needs to form inbound inventory
   under Phase H rules MUST route through Receipt. A Bill-shaped
   backdoor into `ReceiveStock` under `receipt_required=true` is
   a spec violation.

2. **Bill stops being a long-term inventory-truth producer.**
   Under `receipt_required=true`, `CreatePurchaseMovements` does
   not fire on bill post. The `bill_lines.lot_number` /
   `.lot_expiry_date` / `.warehouse_id` columns remain in schema
   for legacy compatibility, but they are **frozen as a
   transitional home**: no new feature may extend their semantics,
   and no new capture path may write them. New tracked-receipt
   capture lands on `receipt_lines`. Legacy companies
   (`receipt_required=false`) retain Phase G behavior
   indefinitely; they are not forced off.

3. **GR/IR is the bridge, not a leak.** The GR/IR clearing account
   and all accrual / clearing / PPV logic live in the business-
   document layer (bill-post and receipt-post orchestration). The
   inventory package continues to return cost-only shapes and stays
   GL-agnostic: it must remain production-import-free of
   accounting, chart-of-accounts, and journal-entry packages. Any
   GR/IR posting logic belongs in the business-document layer, not
   in the inventory package. A test lock (added in H.3) enforces
   this at the semantic level; the concrete import-path matcher in
   the test may be updated if packages are renamed or relocated,
   but the rule being tested is directional and does not depend on
   specific paths. Any future pressure to "push GR/IR into the
   inventory module for convenience" is rejected on sight.

#### Slice plan (binding)

| Slice | Scope | Entry gate |
|---|---|---|
| **H.0** | This spec section. Scope / non-scope / exit / hard rules / slice plan / borders pinned in the canonical API doc. | — |
| **H.1** | `companies.receipt_required BOOLEAN NOT NULL DEFAULT FALSE` column. `ChangeCompanyReceiptRequired(companyID, required, actor)` audited admin surface, F.7 capability-gate pattern. **Dormant rail.** No UI, no default-true, no existing company flipped, no consumer reads the flag yet. | H.0 approved |
| **H.2** | `receipts` + `receipt_lines` tables. `models.Receipt`, `models.ReceiptLine`. Minimal lifecycle (`draft`, `posted`, `voided`). CRUD in services. Source-identity reservation fields for future Phase I linkage (purchase-order-line anchor, at minimum). **No Bill decoupling yet, no matching yet, no inventory formation yet.** | H.1 shipped |
| **H.3** | `ReceiveStockFromReceipt` inventory service. Receipt post → inventory movements (through the existing `ReceiveStock` path, tracked data read from `receipt_lines`) + business-document-layer journal `Dr Inventory / Cr GR/IR`. Inventory module stays GL-agnostic; GR/IR journal lives in `receipt_post.go`. | H.2 shipped |
| **H.4** | Transitional decoupling. Under `receipt_required=true`, `CreatePurchaseMovements` is a no-op for inventory and BillLine tracking fields are neither read nor written by the inventory integration. Under `receipt_required=false` (legacy, default), Phase G behavior is byte-identical. Fields stay physically on `bill_lines`. Documented as frozen transitional home. | H.3 shipped |
| **H-hardening-1** *(shipped, post-H.5)* | Row-level write lock (`SELECT ... FOR UPDATE` on PostgreSQL; no-op on SQLite) on the referenced receipt_line during Bill post matching. Fixes a correctness-level concurrency gap identified in H.5 closeout: two concurrent PostBills targeting the same receipt line could both read `prior_matched` before either committed, producing cumulative over-match. The lock serialises those paths; second tx recomputes against the first's committed bill_line. Pre-enablement hardening — not a new feature. See `resolveBillLineMatchingContext` in `internal/services/bill_receipt_matching.go`. | H.5 shipped |
| **H.5** *(shipped)* | Bill ↔ Receipt line-to-line matching via `bill_lines.receipt_line_id` (nullable FK to `receipt_lines`). Bill post under `receipt_required=true` with a matched stock line: Dr GR/IR at the Receipt's unit cost, Dr/Cr PPV for the per-unit variance (single account, sign-based), and continue H.4 blind GR/IR on any qty overflow. Cumulative partial settlement supported — one Receipt line may be referenced by multiple Bill lines over time; no reverse pointer on Receipt. Configuration requires `companies.purchase_price_variance_account_id` to be set (P&L root; Expense or CostOfSales) when any matched line is present at post time; otherwise `ErrPPVAccountNotConfigured` + rollback. **Operational enablement of `receipt_required=true` on real companies is now technically permitted** (Border 1 released), but remains deliberately opt-in — no auto-flip, `ChangeCompanyReceiptRequired` is still an explicit admin action with audit. | H.4 shipped |

#### Two hard borders

**Border 1 — Phase H entering ≠ `receipt_required` enabling.**
*(Released at H.5 ship — see "H.5 released the border" below.)*
H.1 installed the column and the admin surface; every real company
stayed `receipt_required=false` through H.2–H.4 because any flip
before H.5 produced a half-bridged state (Receipt formed inventory,
Bill could not clear GR/IR precisely) that was strictly worse than
Phase G. The border was enforced by engineering discipline — code
cannot distinguish a "test company" from a "real company" — so the
rule lived in this spec and in review.

**H.5 released the border.** With matching + PPV shipped, Bill now
clears GR/IR at the Receipt's unit cost and posts the variance to
PPV cleanly. `receipt_required=true` on real companies is now
technically permitted. The flip is still **deliberately opt-in** via
`ChangeCompanyReceiptRequired`, audited; there is no auto-migration
of existing companies. Operational rollout is an onboarding / CS
decision, not an engineering one — the spec only says "now safe",
not "now done".

**Border 2 — H.4 and H.5 stay separated.** H.4 is a
data-ownership migration (where tracking truth comes from). H.5 is
a financial-clearing addition (how GR/IR resolves against Bill).
They fail in completely different ways: H.4 regressions corrupt
tracking capture; H.5 regressions produce stuck GR/IR balances,
wrong variance signs, or silent over-clearing. Reviewing both
together obscures which side broke. Any proposal to bundle these
slices for "simplicity" is rejected by this border.

#### Deliberately-deferred-to-later-phase

- **Phase I** — Shipment as first-class document, sell-side source
  identity (SO → Shipment → Sales-Issue → Invoice), tracked
  invoicing, customer return workflow.
- **Phase J** — permission catalog enforcement (the `receipt.*`
  strings become real permissions with assignable roles).
- **Phase K** — manufacturing: tracked component consumption via
  Build, work-order receipt flow.
- **Legacy Bill-forms-inventory path.** Remains available only as
  a transitional compatibility mode for companies on
  `receipt_required=false`, and only until a separate migration /
  deprecation slice retires it. It is **not** part of the long-
  term authoritative architecture — consistent with Phase G's
  framing as a transition window, not a permanent shape. Phase H
  does not schedule the retirement; a future slice will, and that
  slice is where the deprecation clock starts. The `bill_lines`
  tracking columns live on the same clock: transitional home, not
  long-term home.

### Phase I — Shipment-first fulfillment  *(shipped, I.B scope complete)*

**Current Phase I scope selection is Phase I.B**: shipment-first
fulfillment, shipment-recognized cost, invoice-recognized revenue.

I.B is a scope choice, not a roadmap phase name. Any future work
that evolves the sell-side accounting model (e.g. introducing
revenue-at-shipment / contract asset / SNI bridge) lands as a
subsequent scope selection within Phase I, not as a renamed
top-level phase. The `shipment_required` capability rail stays the
same across any scope evolution; only the accounting overlay may
change.

Phase I is the sell-side mirror of Phase H in document shape and
physical truth, but not yet a full accounting mirror.

Under `shipment_required=true`:

- Shipment becomes the authoritative sell-side fulfillment
  document.
- Shipment posting creates ship truth and recognizes cost, but
  does not recognize revenue.
- Invoice narrows to the revenue-and-AR document only.
- A posted Shipment also creates an AR operational item
  `waiting_for_invoice`, so billing can see shipped-but-not-yet-
  invoiced work immediately.

This is a deliberate practical-conservative design. It restores
shipment truth as a first-class sell-side document, removes
inventory / COGS responsibility from Invoice, and improves
operational billing visibility, without yet introducing a
shipped-not-invoiced revenue bridge such as contract asset / SNI.

Phase I is triggered before Phase H pilot stabilisation closes.
Rationale: end-to-end stock-level semantics cannot be validated
with only the receive side instrumented — on-hand quantity, cost
layer consumption, and tracked-lot depletion all require a
working outbound path. Running Phase I engineering in parallel
with Phase H pilot is permitted; enabling BOTH capability rails
on the same real company is not, until both sides close per their
own pilots.

#### Scope item — Shipment posting semantics

Shipment posting produces:

1. **Ship truth.** Shipment is the only authoritative entry point
   for sell-side physical outbound truth when
   `shipment_required=true`.

2. **Inventory / cost recognition.** Shipment post must issue
   stock through the inventory module and post:

   ```
   Dr COGS / Cr Inventory
   ```

   using the authoritative cost returned by `IssueStock`.

3. **AR operational reminder.** Shipment post creates a
   `waiting_for_invoice` operational item on the AR dashboard.

Shipment post does **not** recognize revenue in Phase I.B.

#### Scope item — Invoice semantics under `shipment_required=true`

Under `shipment_required=true`, Invoice becomes the
revenue-and-AR document only.

Invoice posting must:

- derive billable quantity from shipped-eligible quantity, not
  raw Sales Order quantity
- post:

  ```
  Dr Accounts Receivable / Cr Revenue
  ```

Invoice posting must **not**:

- create inventory movements
- recognize COGS
- act as the authoritative outbound tracking / fulfillment truth

Tracked sales remain shipment-driven. Lot / serial selection
happens at Shipment time, not Invoice time.

#### Non-scope clarification

Phase I.B explicitly does **not** add:

- revenue-at-shipment recognition
- contract asset / SNI / unbilled revenue bridge
- sales price variance routing
- customer return accounting in the same slice

Those belong to later follow-on decisions or slices.

Additional non-scope carried from the Phase H framing:

- **Receive-side anything**: Phase H owns it.
- **Multi-warehouse split shipments**: one Shipment lives in one
  company, lands against one warehouse. Splitting a sales order
  across warehouses produces multiple Shipments; no
  single-shipment-multi-warehouse shape.
- **UI admin toggle for `shipment_required`**: engineering-only
  until I.5 locks safety.
- **SO → Invoice direct bypass**: Phase I deliberately does NOT
  reintroduce a direct-conversion shortcut that skips Shipment.
  Under `shipment_required=true`, Invoice always derives from
  Shipment. Under flag=false, legacy Invoice posts COGS directly
  — unchanged.
- **Manufacturing / assembly / work-order**: Phase K.
- **`inventory_costing_method` changes**: FIFO / moving-average
  semantics untouched.
- **Permission catalog implementation**: `shipment.*` and
  `return.*` permission strings reserved; enforcement lands in
  Phase J.
- **Bulk backfill of historical Invoices into synthetic
  Shipments**: legacy history stays on Invoice. Phase I is
  forward-looking.

#### Hard rules (non-negotiable)

**Rule 1 — Shipment is the only ship-truth entry point.**
When `shipment_required=true`, sell-side physical truth must
enter through Shipment, not Invoice.

**Rule 2 — Shipment recognizes cost; Invoice does not.**
Shipment post must create `Dr COGS / Cr Inventory`. Invoice must
not create inventory / COGS effects in the shipment-required
path.

**Rule 3 — Invoice recognizes revenue only against shipped
quantity.** Invoice may bill only shipped-eligible quantity. It
must not bypass Shipment and become fulfillment truth again.

Supporting constraint (extending Phase H.3's Hard Rule #3): the
`internal/services/inventory` module stays production-import-free
of accounting, chart-of-accounts, and journal-entry packages. All
revenue and AR journals live in the business-document layer
(`invoice_post.go`, `shipment_post.go`).

#### Waiting-for-invoice operational queue

A posted Shipment must create an AR operational item
`waiting_for_invoice`.

Its purpose is:

- to surface shipped-but-not-yet-invoiced activity to billing /
  finance
- to support operational follow-up and cash-flow discipline
- to make sell-side lag visible without using Shipment itself as
  an AR document

`waiting_for_invoice` is:

- an operational queue item
- **not** a journal entry
- **not** a substitute for Shipment
- **not** a substitute for Invoice
- **not** an accounting truth bucket

The queue item is cleared (resolved) when an Invoice line links
back to the originating Shipment line via the identity chain
below.

#### Price rule

For Phase I.B, no sales price variance mechanism is introduced.

Invoice remains the commercial price authority in this phase.
Accordingly:

- Phase I.B does not create an SPV account.
- Phase I.B does not add shipment-vs-invoice price variance
  posting.

If post-shipment commercial price adjustment is needed later, it
must be introduced as its own explicit slice, not as hidden
drift inside Phase I.B.

#### Identity chain

The authoritative sell-side identity chain becomes:

```
SO line → Shipment line → Sales-Issue → Invoice line
```

Invoice lines are linked only to shipped truth (via a nullable FK
`invoice_lines.shipment_line_id`, mirror of Phase H.5's
`bill_lines.receipt_line_id`), never used as the origin of that
truth. Cumulative invoiced qty is computed dynamically from
posted invoice lines grouped by `shipment_line_id` — no cached
counter on ShipmentLine, matching Phase H.5's choice.

#### Accounting summary

Phase I.B accounting is intentionally split as follows:

| Surface | Responsibility |
|---|---|
| **Shipment** | fulfillment truth + inventory issue + COGS recognition (`Dr COGS / Cr Inventory`) |
| **Invoice** | AR recognition + revenue recognition (`Dr AR / Cr Revenue`) |
| **`waiting_for_invoice`** | operational reminder only; no accounting effect |

This means Phase I.B is shipment-first operationally and for cost
recognition, while revenue remains invoice-recognized.

#### Capability-gate rule

Entering Phase I engineering does not enable
`shipment_required=true` for any company.

As with Phase H:

- engineering may build the rail first
- the rail remains dormant by default
- real-company enablement happens only after Phase I
  stabilisation criteria and its own pilot discipline are
  satisfied

Any PR flipping a real company's `shipment_required` before I.5
close and a clean pilot observation is rejected on sight. Code
cannot distinguish "test company" from "real company"; the rule
lives in this spec and in review.

#### Exit conditions (all must hold before Phase I closes under the I.B scope)

1. `shipments` + `shipment_lines` tables persisted, CRUD surfaced,
   lifecycle tested (draft → posted, posted → voided).
2. `shipment_required` rail present, audited on flip, and NOT
   flipped ON for any real company — dormant by design until I.5.
3. Under `shipment_required=true` (test companies only, during
   engineering verification):
   - Shipment posting forms inventory movements with
     `source_type='shipment'` and a business-document-layer
     journal `Dr COGS / Cr Inventory`.
   - Shipment posting creates a `waiting_for_invoice` operational
     item linked to the Shipment.
   - Invoice posting forms AR only; `IssueStock` is not called by
     the invoice path; no inventory movement, no COGS journal
     line from Invoice. Invoice line billable qty ≤ shipped-
     eligible qty per identity-chain matching.
   - `waiting_for_invoice` items resolve (clear from queue) when
     an Invoice line links to the Shipment line.
4. Under `shipment_required=false` (legacy default, every existing
   company), Phase G + H + invoice behavior is byte-identical —
   no regression.
5. Smoke suite covers: happy Shipment → matching Invoice; partial
   invoice (part of a Shipment billed); multiple Shipments for
   one Invoice (cumulative matching); unmatched-Shipment aging
   (persistent `waiting_for_invoice`); tracked Shipment (lot +
   expiry + serial).
6. Only after all of the above, `shipment_required=true` becomes
   a permitted operational state.

#### Slice plan (binding)

| Slice | Scope | Entry gate | Status |
|---|---|---|---|
| **I.0** | This spec section. Phase I current scope selection (I.B) / hard rules / accounting split / identity chain pinned. | — | shipped `e8cbdd0` |
| **I.1** | `companies.shipment_required BOOLEAN NOT NULL DEFAULT FALSE` column + `ChangeCompanyShipmentRequired(companyID, required, actor)` audited admin surface, F.7 capability-gate pattern. Dormant rail. | I.0 approved | shipped `e9e2e18` |
| **I.2** | `shipments` + `shipment_lines` tables. `models.Shipment`, `models.ShipmentLine`. Minimum lifecycle (draft/posted/voided). CRUD in services. SO-line source-identity reservation fields. No inventory formation yet, no matching yet. | I.1 shipped | shipped `9b128ee` |
| **I.3** | `IssueStockFromShipment` inventory service. Shipment post → inventory movements (through existing `IssueStock` path, tracked selections read from `shipment_lines`) + business-document-layer journal `Dr COGS / Cr Inventory` + `waiting_for_invoice` queue item creation. Inventory module stays GL-agnostic. | I.2 shipped | shipped `5b462cd` |
| **I.4** | Invoice decoupling. Under `shipment_required=true`, the invoice post's COGS path is a no-op for inventory and for any COGS fragment. Invoice narrows to `Dr AR / Cr Revenue`. Under flag=false, Phase G + H behavior is byte-identical. | I.3 shipped | shipped `b6b787e` |
| **I.5** | Invoice ↔ Shipment matching via `invoice_lines.shipment_line_id`. `waiting_for_invoice` queue item resolution on Invoice post (1:1 atomic closure, double-match rejected). No SPV in this slice. **Operational enablement unlocked only at I.5 close.** | I.4 shipped | shipped `fd3398f` |

**I.3 / I.5 implementation notes (actual vs slice plan):**
- Tracked selections (lot / serial) on ShipmentLine are deferred to a
  dedicated slice. I.3 ships lot-untracked issue truth; tracked items
  fail loud via `inventory.validateOutboundTracking` as intended.
- Partial invoicing of a shipment line is not supported. I.5 is 1:1
  atomic — Invoice line qty should match ShipmentLine qty. Partial
  invoicing requires a separate scope trigger.
- Phase G.2's tracked-invoice guard is not yet bypassed on flag=true;
  tracked outbound through the Shipment path will land with the
  tracking-selection slice above.

#### Cross-phase relationship with Phase H

Phase H and Phase I are **independent capability rails** on
independent sides of the inventory cycle. A company may be in any
of four states:

| `receipt_required` | `shipment_required` | Meaning |
|---|---|---|
| `false` | `false` | Fully legacy: Bill forms inventory, Invoice posts COGS. |
| `true` | `false` | Phase H only: Receipt-first inbound, legacy outbound. |
| `false` | `true` | Phase I (current scope I.B) only: legacy inbound, Shipment-first outbound. |
| `true` | `true` | Both-sides-first: the eventual target state. |

*Revenue-recognition timing may still evolve in later slices; this
row (and the matrix overall) describes capability state, not the
final accounting overlay.*

Posture on concurrent enablement:

- H and I engineering work may proceed in parallel.
- Both rails remain dormant by default.
- A real company should not run both rails enabled until each has
  passed its own pilot stabilisation.
- Enabling either rail is an independent capability decision;
  completion of one rail does not automatically open the other.

Phase G.2's guard on tracked items
(`ErrTrackedItemNotSupportedByInvoice`) is retained under
`shipment_required=false` and bypassed under
`shipment_required=true` (tracked items route through Shipment).

#### Deliberately-deferred-to-later-phase

- **Revenue-at-shipment recognition.** Phase I.B recognizes
  revenue at Invoice time only. A future slice may introduce
  ASC 606-style control-transfer accrual with a contract asset
  (SNI / unbilled revenue) bridge. Decision belongs to a later,
  explicit slice.
- **Sales price variance.** If post-shipment commercial price
  adjustment becomes needed, it ships as its own slice, not as
  hidden drift inside Phase I.B.
- **Phase I.6** — customer return workflow (return receive,
  inspect, disposition, return-to-vendor). Scheduled as a
  dedicated follow-on after I.5; not blocking I.5's close.
- **Phase J** — permission catalog enforcement (the `shipment.*`
  / `return.*` strings become real permissions with assignable
  roles).
- **Phase K** — manufacturing: Build / work-order / assembly with
  tracked components.
- **Legacy off-ramp.** Companies that run
  `shipment_required=false` indefinitely stay supported until a
  dedicated retirement slice — same transitional-compatibility
  framing as the Bill-forms-inventory path.

---

## 8. Open questions — status after Phase D / E / F

1. **Costing method default.** *Resolved for D, extended in E2:*
   weighted average is the default; FIFO is opt-in per company via
   `company.inventory_costing_method = "fifo"`. Specific-identification
   still unimplemented — returns `not yet implemented` from
   `IssueStock`.
2. **LandedCost allocation ownership.** *Resolved:* caller computes
   per-line apportionment and passes `LandedCostAllocation` on
   `ReceiveStockInput`. No sign of needing a shared helper yet.
3. **`inventory_balances` as cache vs. aggregate.** *Resolved for D:*
   materialized. `GetOnHand(AsOfDate=…)` falls back to aggregate
   replay when historical quantity is needed.
4. **Historical average-cost reconstruction.** *Resolved in E3 +
   hardened.* Moving-average companies get chronological replay; FIFO
   companies get layer-based point-in-time value via
   `historicalFIFOValue`. Cross-method aliasing forbidden.
5. **COGS preview/apply window.** *Resolved in Phase E0.* The JE's COGS
   amount now comes from `IssueStock`'s return value, not from the
   pre-tx preview. Locked by
   `TestPostInvoice_COGSAgreesWithMovementUnitCostBase`.
6. **BOM scrap percentage.** Currently hardcoded to zero in
   `ExplodeBOM`. `models.ItemComponent` has no scrap column; if one
   is added, `queries.go` has a single line to update.
7. **Expired stock issue policy.** *Resolved for Phase F:* default
   `warn_only` (visibility + warning); `IssueStock` does NOT block on
   expiry. Per-company `warn_only | hard_block | allow_with_override`
   is a future anchor (§7 upgrade anchors #1), not scheduled.
8. **FEFO / auto-allocator.** *Resolved for Phase F:* advisory-only
   discipline. `IssueStock` always requires explicit
   `LotSelections` / `SerialSelections`. An advisory recommendation
   API is a future anchor, never a silent allocator.
9. **Serial expiry required-vs-optional.** *Resolved for Phase F:*
   optional. Per-item or per-company "expiry required" flag is a
   future anchor for regulated industries.
10. **Tracking-mode conversion with existing on-hand.**
    *Resolved for Phase F:* rejected (`ErrTrackingModeHasStock`).
    No conversion tool today; a future "opening tracking
    initialization wizard" would capture real identities from the
    operator, not synthesize them (§7 upgrade anchor #3).
11. **E2.3b positive-with-layers drift repair.**
    *Resolved — manual-only.* `InspectFIFOLayerDrift` surfaces the
    drift; `RepairFIFOLayerDrift` does NOT touch this class. This
    is a deliberate scope boundary. Future "assisted repair
    workspace" (§7 upgrade anchor #4) is deferred.
12. **Tracked TransferStock / PostInventoryBuild first-class support.**
    *Resolved — deferred as guarded-fail.* If promoted, TransferStock
    ships before PostInventoryBuild.

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
- **FIFO invariant (E2.1 onward):** for companies on
  `inventory_costing_method = "fifo"`,
  `SUM(inventory_cost_layers.remaining_quantity) per (item, warehouse)
  == inventory_balances.quantity_on_hand`.
  `InspectFIFOLayerDrift` (Phase E2.3) surfaces any cell that breaks
  the invariant so operators can schedule targeted repairs or run
  `RepairFIFOLayerDrift` for the genesis-migration case.
- **FIFO historical exactness:** `historicalFIFOValue` at `asOfDate T`
  on a FIFO company must equal the on-hand state at the *first* instant
  strictly after `T` — i.e. asking `AsOfDate = movementDate` includes
  that day's movements. Locked by
  `TestHistoricalValueAt_FIFOCompany_ReversalRestoresLayerAsOf`.
- **Authoritative COGS:** JE COGS line debit must equal
  `|QuantityDelta| × UnitCostBase` on the matching sale movement, to
  the cent. Locked by
  `TestPostInvoice_COGSAgreesWithMovementUnitCostBase`.
- **Tracking / costing orthogonality (F5):** on a FIFO company
  issuing a lot-tracked item, the FIFO layer consumption rows and
  the lot tracking consumption rows are independent — a single
  issue produces anchors in both tables at their own granularities
  (multi-layer FIFO cost rows vs. one row per selected lot). Locked
  by `TestTracking_CostingOrthogonality_FIFOCompanyLotTracked`.
- **Tracked reversal requires anchors (F3):** reversing a tracked
  outbound with no `inventory_tracking_consumption` rows returns
  `ErrTrackingAnchorMissing`. No auto-guess. Locked by
  `TestReverseMovement_LotTracked_MissingAnchorRejected`.
- **Non-stock items can never carry lot/serial (F1):** enforced at
  model + service layers. Locked by
  `TestApplyTypeDefaults_NonStockForcedToNone`,
  `TestValidateTrackingMode_NonStockRejectsLotSerial`,
  `TestChangeTrackingMode_NonStockRejected`.
- **Tracking mode cannot be silently flipped (F1):** any attempt
  while `on_hand > 0` OR `layer.remaining > 0` returns
  `ErrTrackingModeHasStock`. Locked by
  `TestChangeTrackingMode_BlockedByOnHand`,
  `TestChangeTrackingMode_BlockedByLayerRemaining`.
- **Tracked company isolation (F3+F4):** no lot/serial query or
  consumption can cross company boundaries. Locked by
  `TestReceiveStock_SerialTracked_CompanyIsolationAllowsSameSerial`,
  `TestIssueStock_LotTracked_CrossCompanyLotRejected`,
  `TestGetTracesForMovement_CompanyIsolation`,
  `TestGetExpiringLots_CompanyIsolation`.
- **Company-level tracking gate (G.1):** no item may leave
  `tracking_mode='none'` while `companies.tracking_enabled=FALSE`.
  Disabling the gate is rejected while any item is still tracked.
  Every flip produces an audit row. Locked by
  `TestChangeTrackingMode_BlockedByCapabilityGate`,
  `TestChangeCompanyTrackingCapability_DisableBlockedByTrackedItems`,
  `TestChangeCompanyTrackingCapability_EnableWritesAuditAndUnlocksGate`.

These are integration-test territory; a small seeded dataset that exercises
each path lives in `internal/services/inventory/testdata/` (TBD).

---

## 10. Permission catalog naming (Phase G.5 — convention anchor)

The full permission model lands in Phase J (control hardening). Ahead of
that, this section **fixes the naming convention** so any handler work
from Phase G onwards can introduce new permission identifiers without
drifting.

### Naming form

```
<domain>.<document>.<action>
```

- **Domain** — bounded context that owns the permission:
  `inventory` / `ap` / `ar` / `company`.
- **Document** — the specific document type the action operates on.
  Use the document's canonical singular (`bill`, not `bills`; `receipt`,
  not `receipts`).
- **Action** — one of the canonical lifecycle verbs:
  `create` / `submit` / `approve` / `post` / `cancel` / `reverse` /
  `send` / `void` / plus role-split verbs where relevant
  (`request` / `approve`, `ship` / `receive`).

### Canonical permission list (anchor — do not rename without passing this doc)

**Inventory documents:**

```
inventory.receipt.create
inventory.receipt.post
inventory.receipt.cancel
inventory.receipt.reverse
inventory.receipt.discrepancy_report

inventory.shipment.create
inventory.shipment.post
inventory.shipment.cancel
inventory.shipment.reverse

inventory.transfer.draft
inventory.transfer.ship
inventory.transfer.receive
inventory.transfer.cancel
inventory.transfer.reverse
inventory.transfer.discrepancy_report

inventory.writeoff.request
inventory.writeoff.approve
inventory.writeoff.post
inventory.writeoff.reverse

inventory.return.receive
inventory.return.inspect
inventory.return.disposition

inventory.adjustment.post
inventory.adjustment.reverse

inventory.build.post
inventory.build.reverse
```

**AP documents:**

```
ap.bill.create
ap.bill.submit
ap.bill.approve
ap.bill.post
ap.bill.cancel
ap.bill.reverse

ap.payment.create
ap.payment.post
ap.payment.void
```

**AR documents:**

```
ar.invoice.create
ar.invoice.send
ar.invoice.post
ar.invoice.cancel
ar.invoice.reverse

ar.receipt.create
ar.receipt.post
ar.receipt.void
```

**Company capability gates (F.7 family, governed by company admin):**

```
company.tracking_capability.enable
company.tracking_capability.disable
company.receipt_required.flip        (future, Phase H)
company.shipment_required.flip       (future, Phase I)
company.manufacturing_enabled.flip   (future, Phase K)
```

### Hard rules

1. **One document / one lifecycle action = one permission.** Do NOT
   collapse `create + post` into a single permission because "usually
   the same person does both." Role composition is an orthogonal
   layer (see below).
2. **Reverse is always separate.** It is frequently an elevated
   permission even where post is routine. Never bundle into post.
3. **Approve exists iff the document has an approval semantic.** Do
   not add approve permissions where no workflow consumes them.
4. **No "admin" / "super" catch-all permissions.** Every permission
   names a concrete action on a concrete document. The union-of-all
   is expressed through role composition, not through a wildcard
   permission.
5. **Capability-gate permissions are named
   `<domain>.<gate>.<enable|disable>` or `.flip`**, not
   `company.<gate>.admin`. The audit trail needs to record which
   direction the flip went.

### Role composition (not shipped in G.5; anchor only)

Roles are **compositions of permissions**, not aliases for them.
System-provided role names should reflect business function, not
privilege level:

- `warehouse_clerk_shipping`: `inventory.shipment.create` +
  `inventory.transfer.ship`
- `warehouse_clerk_receiving`: `inventory.receipt.post` +
  `inventory.transfer.receive` + `inventory.return.receive`
- `inventory_controller`: `inventory.writeoff.request` +
  `inventory.adjustment.post` + `inventory.return.inspect`
- `inventory_controller_senior`: above + `inventory.writeoff.approve`
  + `inventory.*.reverse`
- `company_admin_inventory`: capability-gate permissions

Client-defined roles may copy and extend the above. Clients may NOT
mutate system-provided roles directly (prevents privilege escalation
by silent edit).

### Phase J commitments

Phase J will:
- Build the `permissions` + `role_permissions` schema that realises
  this naming
- Retrofit existing handlers to check the catalog instead of ad-hoc
  `role == "admin"` checks
- Add per-flip audit for every capability-gate permission
- NOT expand the catalog beyond what this section names; additions
  require a new permission-catalog slice (not a handler-driven
  afterthought)
