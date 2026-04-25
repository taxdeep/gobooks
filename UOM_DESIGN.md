# UOM (Unit of Measure) — Design Document

**Status:** Decisions ratified 2026-04-25. U1 implementation cleared to begin.
**Owner area:** `internal/models/`, `internal/services/inventory/`, all
AR/AP document services + UIs.
**Companion docs:** [INVENTORY_MODULE_API.md](INVENTORY_MODULE_API.md)
(reserves `UoMCode` + `UoMFactor` slots in the inventory API but does
not ship the feature), [PROJECT_GUIDE.md](PROJECT_GUIDE.md).

---

## 1. Why we need this

Reported by user 2026-04-25 alongside the integer-qty rule (S1):

> 一箱水拆成 24 瓶来卖之类的。理解后，给我一个方案。

Concrete pain points the existing model can't handle cleanly:

1. **Buy in cases, sell in bottles** — operator buys "Case of 24 bottles"
   from supplier, sells individual bottles to customers. Today the
   operator has to either:
   - Track two SKUs (Case + Bottle) and run a manual stock-transfer to
     "decompose" cases when needed (heavy operationally), OR
   - Use the `assembly` BOM path and run a Build event each time
     (also heavy), OR
   - Track only bottles and convert externally before receiving the bill
     (loses the audit trail of "we bought N cases").

2. **Buy in pallets / boxes / kg, count in eaches / grams** — common in
   manufacturing and grocery.

3. **Sell in fractional units of a stock item** — the user's other
   example: "西瓜切成 8 块来卖". Stock = whole watermelons; sell unit =
   slice. Without UOM, this either violates the integer rule (S1) or
   needs a manual "build slices" event (assembly path, heavy).

The existing `bundle` and `assembly` BOM types solve a different problem
(combining multiple distinct items into a sellable package or assembling
finished goods from raw components). Neither is the right primitive for
"same physical thing, different counting unit".

UOM is the standard accounting/ERP primitive. SAP, Odoo, NetSuite, QB
Enterprise all model it the same way. Adding it lets us close this gap
without bending BOM into a shape it wasn't designed for.

---

## 2. Concept model

### 2.1 The two ratios

Each stock-tracked `ProductService` carries:

- **Stock UOM** (`stock_uom`): the unit the inventory module counts in.
  Costing, on-hand, FIFO layers, transfers — all denominated here. Once
  set, **immutable** while on-hand exists (same rule as TrackingMode).
- **Sell UOM** (`sell_uom`): the unit the AR side defaults to on
  Invoice / Quote / SO / CN lines. May equal Stock UOM (the common case).
- **Purchase UOM** (`purchase_uom`): the unit the AP side defaults to on
  Bill / PO / VCN lines.
- **Conversion factors**: `sell_uom_factor` and `purchase_uom_factor` —
  how many Stock UOMs equal one Sell/Purchase UOM. Stored as
  `decimal(18,6)` for precision (e.g. `0.166666` for "kg sold per gram
  stocked" ≈ 1/6 kg per 6g pack).

### 2.2 Default cases

| Case | Stock UOM | Sell UOM | Sell factor | Purchase UOM | Purchase factor |
|---|---|---|---|---|---|
| Plain item (most products) | `EA` | `EA` | 1 | `EA` | 1 |
| Case → bottle | `BOTTLE` | `BOTTLE` | 1 | `CASE` | 24 |
| Watermelon → slice | `EA` | `SLICE` | 0.125 | `EA` | 1 |
| Bulk grain | `KG` | `KG` | 1 | `BAG_50KG` | 50 |
| Time-bill | (not stock) | — | — | — | — |

**Hard rule**: factor is positive non-zero. `purchase_uom_factor=0` is
rejected at validation.

### 2.3 What UOM is NOT

- **NOT a tax / pricing modifier.** Price-per-unit on a line is in the
  line's UOM (sell or purchase). Tax codes attach to lines, not to UOMs.
- **NOT a different SKU.** Same product, same cost basis, different
  counting unit. If two physical products differ (e.g. premium vs
  standard), they need separate ProductServices.
- **NOT bundle replacement.** A bundle of 3 distinct items still needs
  bundle. UOM is for one item in different units.

---

## 3. Data model changes

### 3.1 `ProductService` — three columns + factors

```go
// Stock-tracking unit. Costing / on-hand / FIFO denominated here.
// Once set, immutable while inventory_balances.qty > 0.
StockUOM string `gorm:"type:varchar(16);not null;default:'EA'"`

// Sell-side default unit. May equal StockUOM (common case).
SellUOM       string          `gorm:"type:varchar(16);not null;default:'EA'"`
SellUOMFactor decimal.Decimal `gorm:"type:numeric(18,6);not null;default:1"`

// Purchase-side default unit. May equal StockUOM (common case).
PurchaseUOM       string          `gorm:"type:varchar(16);not null;default:'EA'"`
PurchaseUOMFactor decimal.Decimal `gorm:"type:numeric(18,6);not null;default:1"`
```

Validation (`ValidateUOMs`):
- All factors > 0
- StockUOM non-empty
- SellUOM == StockUOM ⇒ SellUOMFactor == 1
- PurchaseUOM == StockUOM ⇒ PurchaseUOMFactor == 1
- Non-stock items (`IsStockItem=false`) must use defaults (StockUOM=EA,
  factors=1) — UOM only makes sense for stock-tracked items
- Changing StockUOM with on-hand > 0 → rejected with
  `ErrUOMHasStock` (parallel to `ErrTrackingModeHasStock`)

### 3.2 Per-line UOM snapshot on AR/AP documents

Every line table that previously stored just `Qty` now also stores the
**unit at the moment of save** — like the existing
`CustomerNameSnapshot` pattern. This lets historical documents print
correctly even after the catalog changes:

```go
// Snapshot at save time; immutable thereafter.
LineUOM       string          `gorm:"type:varchar(16);not null;default:'EA'"`
LineUOMFactor decimal.Decimal `gorm:"type:numeric(18,6);not null;default:1"`

// Existing Qty stays in line UOM. We add a base-qty column for the
// inventory side so the conversion is computed once at save time and
// stored — no risk of repeated rounding drift across joins/reports.
QtyInStockUOM decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
```

Tables affected (12 line tables):

| AR / sell side | AP / buy side | Stock-only (already line-UOM-aware via inventory) |
|---|---|---|
| InvoiceLine | BillLine | InventoryMovement (already has `UoMCode`/`UoMFactor` slot) |
| QuoteLine | PurchaseOrderLine | ShipmentLine |
| SalesOrderLine | VendorCreditNoteLine | ReceiptLine |
| CreditNoteLine | ExpenseLine (when stock item picked) | StockAdjustmentLine, WarehouseTransfer |
| ARReturnReceiptLine | VendorReturnShipmentLine | |

For each: at save time the service:
1. Reads `ProductService.SellUOM` / `PurchaseUOM` (depending on doc type)
   and copies into `LineUOM` + `LineUOMFactor` (snapshot).
2. Computes `QtyInStockUOM = Qty * LineUOMFactor`.
3. Persists both.

### 3.3 New table: `units_of_measure` (optional v1, cleanup v2)

For v1 we treat UOM as free-form `varchar(16)`. The simple set
(`EA, BOX, CASE, KG, G, LB, OZ, L, ML, M, CM, PACK_6, PACK_12, PACK_24`)
covers ~95% of cases. Operators type whatever they want.

For v2 (post-launch cleanup) we can introduce a per-company catalog
`units_of_measure` table for picker / autocomplete + spelling
consistency. Not blocking.

### 3.4 Migration plan

`093_uom_columns.sql`:

1. `ALTER TABLE product_services` add the 5 new columns with safe
   defaults.
2. `ALTER TABLE invoice_lines / bill_lines / quote_lines /
   sales_order_lines / purchase_order_lines / credit_note_lines /
   vendor_credit_note_lines / expense_lines / ar_return_receipt_lines /
   vendor_return_shipment_lines` add `line_uom`, `line_uom_factor`,
   `qty_in_stock_uom` with safe defaults (`EA`, `1`, `qty`).
3. Backfill: `qty_in_stock_uom = qty` for existing rows (factor=1).

No data destruction. Existing companies keep working without ever
visiting the UOM settings.

---

## 4. Service layer impact

### 4.1 The conversion happens at the boundary

**Inviolable rule:** the inventory module sees only Stock UOM. No UOM
conversion logic inside `internal/services/inventory/`.

The conversion lives in the **document service layer** (Bill post,
Invoice post, Receive Stock, Shipment post, etc.). At post time:

```go
// In bill_post.go after parsing line:
qtyInStockUOM := line.Qty.Mul(line.LineUOMFactor)
inv.ReceiveStock(tx, inventory.ReceiveStockInput{
    ItemID:    line.ProductServiceID,
    Quantity:  qtyInStockUOM,            // always in Stock UOM
    UoMCode:   line.LineUOM,             // for audit trail
    UoMFactor: line.LineUOMFactor,
    // ...
})
```

This means the existing inventory module needs **zero changes** for the
core path — `UoMCode` and `UoMFactor` are already reserved.

### 4.2 Pricing helper

A new shared helper for unit-price display:

```go
// LinePrice returns the line's per-line-UOM unit price.  Stored
// UnitPrice IS in line UOM (set by operator); we don't persist a
// stock-UOM equivalent because price doesn't need to round-trip.
func LinePrice(line) decimal.Decimal { return line.UnitPrice }

// UnitCostPerStockUOM converts a line's unit price into per-stock-UOM
// terms — used by reports that want "per kg" comparisons across items
// stocked / sold in different units.
func UnitCostPerStockUOM(line) decimal.Decimal {
    if line.LineUOMFactor.IsZero() { return decimal.Zero }
    return line.UnitPrice.Div(line.LineUOMFactor)
}
```

### 4.3 Cost flow correctness

Costing is denominated in **base currency per Stock UOM**. When a Bill
arrives in Purchase UOM:

```
Bill line:  10 CASE × $24.00/CASE = $240
Stock UOM:  10 × 24  = 240 BOTTLE
Unit cost:  $240 / 240 = $1.00/BOTTLE  ← what FIFO/MAC stores
```

When that stock is later sold in Sell UOM (also BOTTLE here, factor 1):
```
Invoice line:  100 BOTTLE × $1.50/BOTTLE = $150 revenue
COGS:          100 × $1.00 = $100        ← inventory module returns this
```

Different Sell UOM (e.g. PACK_6 = 6 bottles):
```
Invoice line:  10 PACK_6 × $9.00/PACK_6 = $90 revenue
Stock-UOM qty: 10 × 6 = 60 BOTTLE
COGS:          60 × $1.00 = $60
```

Math is straightforward; the rounding discipline is the usual `Round(2)`
on monetary amounts and `Round(4)` on stock quantities.

### 4.4 Integer-qty rule (S1) interplay

The integer rule applies to **Stock UOM**, not the line's UOM. A line
selling "0.5 PACK_12" (half a dozen, factor 12) computes
`stock_qty = 0.5 × 12 = 6` — clean integer in Stock UOM.

UI surfaces the rule against line-UOM input but the validation runs
against the converted `qty_in_stock_uom`. Reject when stock-qty isn't a
whole number. Cleanest message: "Stock counts in BOTTLE — your 0.5
PACK_6 leaves 3 BOTTLE which is whole, so this is OK." vs the bad case:
"Your 0.4 PACK_6 leaves 2.4 BOTTLE — pick a multiple of 1 BOTTLE
(0.166 PACK_6 minimum)."

(Better UX: validate against stock-qty integer-ness silently; surface
just "this combination doesn't reduce to whole BOTTLE" when bad.)

### 4.5 Over-shipment buffer (S3) interplay

Buffer applies in the line's UOM (what the operator sees). Same
arithmetic as today; no special case.

---

## 5. UI / UX impact

### 5.1 ProductService edit page (settings section)

Add a "Units of Measure" section, only visible when `IsStockItem=true`:

```
Stock UOM      [EA          ▼]   ← immutable once on-hand > 0
Sell UOM       [BOTTLE      ▼]   factor [1.00     ]
Purchase UOM   [CASE        ▼]   factor [24.00    ]

Preview: "1 CASE = 24 BOTTLE (stock)" / "1 BOTTLE = 1 BOTTLE (stock)"
```

Live preview re-renders on input change so the operator sees the
arithmetic immediately. Save button is disabled while factors are 0 /
empty.

### 5.2 Doc-line input

Each line table that takes Qty + UnitPrice gets a UOM column between
them. When the operator picks a ProductService, the UOM defaults to the
side's UOM (Sell on AR forms, Purchase on AP forms) and the factor is
locked. Changing the UOM (rare; for one-off "this PO actually came in
EA, not the usual CASE") is a small dropdown beside the qty field.

Unit price label dynamically shows the selected UOM: `Unit Price (per
CASE)`.

### 5.3 Doc-detail (read) display

Lines render the stored snapshot:
```
24.00 CASE × $24.00/CASE = $576.00
```

Tooltip on hover: "576 BOTTLE in stock" so reviewers can see the
underlying inventory effect.

### 5.4 Reports

- **Stock value report** stays in Stock UOM.
- **Sales report** can group / pivot by Sell UOM (same item appearing as
  CASE and BOTTLE rows when sold both ways).
- **Cost reports** can compare per-Stock-UOM unit cost across items.

No reports break — they keep displaying what they have today, just
gain optional UOM columns.

---

## 6. Edge cases

### 6.1 UOM change on a posted document

Posted documents are immutable. Snapshot fields lock the UOM at save
time. Even if the catalog later changes (e.g. CASE→BOX rename), the
historical document still prints the original snapshot.

### 6.2 Mixed-UOM journals

The Bill's JE is unaffected — JE lines are denominated in monetary
amounts, never in physical units. UOM lives entirely in the inventory +
document layer.

### 6.3 Returns

`ARReturnReceipt` and `VendorReturnShipment` snapshot the original
sale / purchase line's UOM (via `OriginalInvoiceLineID` /
`OriginalBillLineID` already in place). The operator returning "5 of
the 24 cases" enters `5 CASE`, the system computes `5 × 24 = 120
BOTTLE` returned to stock at the original cost layer.

### 6.4 Bundle / assembly + UOM

Bundle components stay in **their own Stock UOM**. Bundle parent
(non-inventory) doesn't have a Stock UOM but can have a Sell UOM (e.g.
"Gift Box" sells in EA but contains 3 bottles + 1 chocolate bar).

Assembly: parent (inventory) has a Stock UOM. Components reference one
another in their respective Stock UOMs.

No new combinatorial complexity.

### 6.5 FX

UOM and currency are orthogonal. A Bill in USD with line in CASE just
flows through the existing FX path; the per-CASE unit price is in USD,
the per-BOTTLE cost the inventory module sees is also in USD (or
converted to base via existing path).

### 6.6 "1 BOTTLE = 0.0417 CASE" (reciprocal)

Operators sometimes think in the inverse direction. The picker shows
both:
```
Sell UOM: BOTTLE  factor 1.00     "1 BOTTLE = 1 BOTTLE in stock"
Purchase UOM: CASE  factor 24.00  "1 CASE = 24 BOTTLE in stock"
                                  "(1 BOTTLE = 0.0417 CASE)"
```

Stored as `factor` (× to get stock); reciprocal is display-only.

### 6.7 Decimal precision on `factor`

`numeric(18,6)` = up to 1 trillion units with 6 decimals. Covers gram /
mg level conversions. Internally we convert via `decimal.Decimal` which
maintains the precision; round only when persisting derived columns
(Round 4 for qty, Round 2 for money).

### 6.8 Pre-S6 inventory items in production

Set every existing `ProductService.StockUOM = SellUOM = PurchaseUOM =
'EA'` and all factors to 1 in the migration. Backwards-compatible —
operator never has to change anything until they explicitly want UOM.

---

## 7. Implementation phases

Sliced for incremental delivery. Each phase is a shippable chunk that
leaves the system in a consistent state.

### Phase U1 — Schema + ProductService model + UI + tests

- Migration 093 adds the 5 new columns to `product_services` with
  defaults
- `ProductService.ValidateUOMs()` model method
- Edit page UOM section (visible when IsStockItem)
- Service: `ChangeStockUOM` guard against on-hand > 0
- Tests: validator + on-hand guard + UI smoke

**Ships standalone.** Doc lines still stored as Qty alone; nothing else
changes. Operators can configure UOM but nothing consumes it yet.

### Phase U2 — Line snapshot columns + write paths

- Migration 094 adds `line_uom` + `line_uom_factor` + `qty_in_stock_uom`
  to all 12 affected line tables
- Each line write path (Create + Update for SO / Quote / PO / CN / VCN
  + the editors for Invoice / Bill) pulls UOM defaults from the chosen
  ProductService and computes `qty_in_stock_uom`
- Display: detail pages show the snapshot UOM next to qty
- Tests: line-write tests for each table verify the snapshot fields are
  populated correctly

**Visible win.** Operators see "24 CASE" on the bill detail page even if
they entered just "24" (UOM defaulted from the product).

### Phase U3 — Inventory boundary

- Each post path (`PostBill`, `PostInvoice`, `PostReceipt`, `PostShipment`)
  passes `qty_in_stock_uom` (not `Qty`) to the inventory module
- The existing `UoMCode` + `UoMFactor` slots on `ReceiveStock` /
  `IssueStock` get populated from the line snapshot for audit
- COGS comes back in Stock UOM, gets converted back if needed for line
  display
- Tests: ReceiveStock + IssueStock end-to-end with non-1 factor

**The actual feature.** Cases buy → bottles sell now flows correctly
through inventory.

### Phase U4 — UI polish + reports

- Per-line UOM dropdown on editors (defaults from product, override-able)
- Unit price label dynamic ("per CASE" / "per BOTTLE")
- Tooltip showing stock-UOM equivalent on detail pages
- Report adjustments (sales report by sell-UOM grouping, cost
  comparison report)

**Niceties.** The system is functionally complete after U3; this is the
final ergonomic pass.

### Phase U5 — Optional cleanup

- `units_of_measure` per-company catalog table for picker autocomplete /
  spelling consistency
- Bulk import wizard for catalog-loaded UOM definitions

**Skip-able.** Defer until customer feedback shows freeform UOM string
is causing problems (e.g. "ea" / "EA" / "Ea" inconsistency).

---

## 8. Estimated effort

Eyeballing slices, with tests:

| Phase | Effort | Critical-path? |
|---|---|---|
| U1 — Schema + product UI | 0.5 day | Yes |
| U2 — Line snapshot columns + writes | 1.5 day | Yes |
| U3 — Inventory boundary | 1 day | Yes |
| U4 — UI polish + reports | 1 day | No |
| U5 — Catalog table | 0.5 day | No |
| **Total (U1–U3)** | **~3 days** | — |
| **Total (U1–U4)** | **~4 days** | — |

Single-developer estimate. U1–U3 is the minimum to call this "done"; U4
is what the user will actually feel.

---

## 9. Decisions (ratified 2026-04-25)

1. **UOM string set: free-form** (`varchar(16)`). Per-company catalog
   deferred to U5 / dropped if friction never materialises.

2. **Default factors at item creation: `EA / EA / EA / 1 / 1 / 1`**.
   Operator opts in to multi-UOM by editing.

3. **Per-line UOM override: allowed**. Editor renders a UOM dropdown
   per line; defaults from the picked product but can be overridden.
   Snapshot fields (`line_uom` + `line_uom_factor`) carry whatever the
   operator chose.

4. **Reporting grouping: split by side**. Sales / purchases reports
   group by line UOM (operator sees "100 CASE + 50 BOTTLE sold").
   Inventory + cost reports normalise to Stock UOM.

5. **U2 migration: backfill** `qty_in_stock_uom` from current `Qty`
   (every existing line has factor=1, so they're equal). Simpler
   downstream code, no read-side branch.

6. **Stock UOM change with zero on-hand: allowed + audit-logged**.
   Parallels `ChangeTrackingMode`. Service `ChangeStockUOM` writes a
   `product_service.uom.changed` audit row with before/after.

7. **Bundle parent UOM: yes**. Bundle parents (non-inventory) have no
   Stock UOM but can carry a Sell UOM. Default is `EA`; operators editing
   a bundle just pick a Sell UOM if they want non-EA display.

---

## 10. Out of scope (explicitly)

- **Multiple base UOMs.** A ProductService has exactly one Stock UOM.
  Operators can't say "this item is stocked in both KG and EA".
- **UOM aliases / synonyms.** "ea" vs "EA" vs "each" — handled by
  case-folding on save; full alias graph deferred.
- **Per-warehouse UOM overrides.** All warehouses for a given item use
  the same Stock UOM.
- **Time-based UOM changes.** No "before 2026-04-01 we stocked in CASE,
  after we stock in BOTTLE" — change once with a stock count + restart.
- **Auto-conversion suggestions.** No "you typed 24 BOTTLE, did you mean
  1 CASE?" hints. Operator picks the UOM explicitly.

---

## 11. Risks

- **Decimal precision on long conversion chains.** Round 6 on factor +
  Round 4 on qty + Round 2 on money should be safe; need a stress test
  with extreme factors (e.g. mg → tonne, factor = 1,000,000,000) to
  confirm no silent zero drift.

- **Existing inventory tests.** Many tests construct `InventoryMovement`
  with `UoMCode=""` and `UoMFactor=0`. Migration default of `'EA'` / `1`
  should fix the schema side, but services that assume nil need a sweep.
  Plan a grep before U3.

- **Reporting cache invalidation.** Sales report grouped by UOM is a
  new cache key. Existing report invalidation already keys on company
  + date; UOM is downstream so likely OK. Verify in U4.

- **PDF templates.** Printed Invoice / PO / Quote PDFs hardcode "Qty"
  column. They need to either:
  - Show line UOM in the qty cell ("24 CASE")
  - Or add a separate UOM column

  PDF template changes touch [services/pdf/](internal/services/pdf/) —
  modest scope, included in U2.

---

## 12. Status

Decisions ratified 2026-04-25 (see §9). U1 cleared to begin.

Recommended slicing pace: one phase per session, commit + push between
phases (matches the S1–S4 cadence shipped earlier in 2026-04-25).
