# Rule #4 Runbook — Item-Nature Invariant (cross-cutting)

**Audience:** Customer Success, Operations, Account Management
**Status:** Active after IN.3 merge (post-time assertion shipped)
**Scope:** Cross-cutting Hard Rule #4 in `INVENTORY_MODULE_API.md`
§2A. Complements — does not replace — `PHASE_H_RUNBOOK.md` and
`PHASE_I_RUNBOOK.md`. Rule #4 governs *which document owns the
inventory movement* on a stock-item line; Phase H / Phase I runbooks
govern the capability rails that steer the dispatch.

This is the authoritative internal reference for how the item
picker, `rule4 violation` errors, and the Expense/Bill/Invoice
controlled-mode behaviour actually work. Do not produce
customer-facing messaging that contradicts this document.

---

## TL;DR

**A line carrying a stock item MUST either produce an inventory
movement or be rejected loudly at post time. Which document owns
the movement depends on the company's workflow mode.** Silent
swallowing is not a legal outcome.

For a line WITHOUT a stock item (pure expense: water, consulting
fees, gas, cheque-book fees), Rule #4 has nothing to enforce — the
line posts as cost-only, no inventory effect, same as before.

Four Rule #4 scope items pinned by the IN.0 charter:

- **Q1** Amount-only fallback preserved — picker first option is
  always `— Expense only (no item) —`. Leaving Item blank keeps
  the legacy cost-only behavior.
- **Q2** Expense is legacy-mode only for inbound inventory. Under
  `receipt_required=true`, an Expense line with a stock item is
  rejected loudly at post; operator must use Receipt.
- **Q3** Header-level Warehouse field on Bill and Expense, visible
  and editable; defaults to the company default warehouse.
- **Q4** Tracked items (lot / serial / expiry) on Bill / Expense
  stock lines are deferred. Tracked inbound still routes through
  existing Phase G.4 / Receipt paths.

---

## 1. The dispatch matrix (who owns the inventory movement)

Every stock-line post routes through one of these owners. No
document forms inventory twice; no document is allowed to silently
form zero.

**Inbound side**:

| Workflow mode | Bill stock line | Expense stock line | Receipt |
|---|---|---|---|
| **Legacy** (`receipt_required=false`) | **Bill is owner** — Dr Inventory / Cr AP | **Expense is owner** — Dr Inventory / Cr Bank/Card | n/a |
| **Controlled** (`receipt_required=true`) | Not owner — Dr GR/IR / Cr AP (Receipt already owned) | **REJECTED at post** — operator must route through Receipt | **Receipt is owner** — Dr Inventory / Cr GR/IR |

**Outbound side**:

| Workflow mode | Invoice stock line | Shipment |
|---|---|---|
| **Legacy** (`shipment_required=false`) | **Invoice is owner** — Dr AR / Cr Revenue + Dr COGS / Cr Inventory | n/a |
| **Controlled** (`shipment_required=true`) | Not owner — Dr AR / Cr Revenue only (Shipment already owned) | **Shipment is owner** — Dr COGS / Cr Inventory |

When CS explains "why the ledger moved / didn't move" to a customer,
first look up which box applies. Everything downstream follows.

---

## 2. The "Item picker" on the editor — what each choice means

All stock-bearing editors (Bill / Expense / Invoice-under-legacy)
now expose the same three-shape line row:

```
[ Item picker ▾ ] [ Category ▾ ] [ Description ] [ Qty ] [ Unit Price ] [ Amount ] [ Tax ] …
```

| Picker value | What happens on save/post |
|---|---|
| `— Expense only (no item) —` (first option) | **Amount-only legacy line.** Qty is frozen to 1 server-side; UnitPrice = Amount. No inventory effect. The line books to the Category account chosen by the operator. This is the right choice for utilities, consulting fees, labour, service subscriptions — anything that is not a physical good. |
| Any stock item (kind shown as `· stock`) | **Rule #4 stock line.** Qty × UnitPrice is authoritative; Amount becomes read-only and auto-computed. On post: inventory movement formed (legacy mode) OR rejected loudly (controlled mode for Expense). |
| Any service item (kind shown as `· service`) | **Catalog-linked but no inventory effect.** Same behavior as amount-only, plus the line carries the ProductService reference for Task reinvoice and catalog reporting. |

Stock vs service is a property of the ProductService record, not of
the document line. If a customer says "my service shows up but my
widget doesn't," check Products & Services: the widget must be
`Type=Inventory` (which implies `IsStockItem=true`) or `Type=Non-Inventory`
with a manual `IsStockItem=true` override.

### Category auto-fill when an item is picked

Selecting an item auto-fills the Category column using this priority:

1. `ProductService.InventoryAccountID` (for stock items)
2. `ProductService.COGSAccountID`
3. *(leaves Category blank — operator picks)*

On Bill / Expense posting, the line's Category is routed according
to the dispatch matrix above — e.g. on a legacy Bill, the line
debiting the Inventory Asset account is redirected by the post
engine through `AdjustBillFragmentsForInventory` so the Dr side
lands on the right account. Operator never has to think about the
redirect.

---

## 3. Bug vs by-design — triage

| Symptom | Bug or by-design? | What to say |
|---|---|---|
| Water bill expense doesn't move inventory | **By design.** | Amount-only lines (no Item) are Q1-preserved legacy behavior. Inventory is only touched by lines with a **stock item** picked. |
| Consulting fee expense doesn't move inventory | **By design.** | Service items don't move inventory even when picked — only stock items do. |
| Credit-card purchase of stock items (Expense post) works in legacy mode but fails under Phase H | **By design. (Q2 invariant.)** | Controlled-mode companies (`receipt_required=true`) close the Expense backdoor so Receipt remains the only inbound surface. Operator creates a Receipt (and matching Bill) instead. The in-editor banner warns up-front. |
| Bill post under `receipt_required=true` books `Dr GR/IR / Cr AP` instead of `Dr Inventory / Cr AP` | **By design.** | Phase H semantic: Receipt already formed the inventory movement; Bill becomes the financial claim only. |
| Invoice post under `shipment_required=true` books Revenue only, no COGS | **By design.** | Phase I semantic: Shipment owns COGS; Invoice narrows to AR. |
| Operator clicked Post but sees `rule4 violation (silent swallow): bill 42 posted with 1 stock line(s) but zero inventory_movements rows` | **BUG.** Escalate. | IN.3 caught a regression class: the post path somehow skipped inventory movement creation. Tx rolled back; Bill stays in draft. Engineering owns. |
| Operator sees `rule4 violation (duplicate owner): invoice 5 is not the movement owner under this workflow but N inventory_movements rows exist` | **BUG.** Escalate. | A legacy code path fired on top of the capability-rail dispatch. Would double-count inventory; tx rolled back. Engineering owns. |
| `ErrExpenseStockItemRequiresReceipt` on Expense post | **Q2 rejection.** | Not a bug. Operator either routes the purchase through Receipt or flips `receipt_required=false` on the company (advise against flipping back unless the customer understands the Phase H trade-off). |
| `ErrExpensePaymentAccountRequiredForPost` on Expense post | **Missing input.** | Operator must set the Payment Account field (Bank / Credit Card / Cash) on the expense header before posting. Payment account is the JE's credit target — required for a balanced entry. |
| `ErrExpenseWarehouseRequiredForStockLine` on Expense post | **Configuration miss.** | Company has no default warehouse AND operator didn't pick one on the header. Either set a default warehouse (Settings → Warehouses → mark one as default) or pick one per expense. |
| Stock item exists in the catalog but doesn't appear in Bill / Expense item picker | **Usually catalog state, not code.** | Check Products & Services: item is `is_active=true`; `Type=Inventory` or `Type=Non-Inventory`. |

---

## 4. The controlled-mode warning banner (Expense editor)

When a company runs `receipt_required=true`, the Expense editor
shows a yellow banner above the line-items block:

> **Receipt-first mode is active for this company.** Expense lines
> that pick a stock item will be **rejected** at post — inbound
> inventory must route through a Receipt. Stock-item purchases
> should be entered as a Receipt (and matching Bill), not an Expense.

This is Q2 surfaced in the UI so operators don't discover the
rejection only at post time. When CS teaches a Phase H customer:
"you are looking at this banner today, and you will keep seeing it
as long as `receipt_required` is on."

---

## 5. Header Warehouse field — how routing decides

Both Bill and Expense grew a header-level Warehouse dropdown (Q3).
Empty = "use the company default warehouse." Populated = route any
stock-line inventory movements from this document to the chosen
warehouse.

Post-time resolution (Expense path is the canonical example):

1. If the header Warehouse field is set → use it.
2. Else fall back to the company's default warehouse
   (`warehouses.is_default=true`).
3. If no default exists and no header override → fail loud with
   `ErrExpenseWarehouseRequiredForStockLine`.

Operators never need to pick a warehouse for pure-expense (no
stock item) documents — the field is still there, but post-time
ignores it when no stock lines exist.

---

## 6. The IN.3 `rule4 violation` errors — what they mean

IN.3 shipped a defensive assertion that runs at the tail of every
Bill / Invoice / Expense post transaction. It verifies that the
movement-owner dispatch actually happened — either the document
formed the expected `inventory_movements` rows OR it correctly
deferred to another owner.

Two error classes, both **engineering escalations**:

### `rule4 violation (silent swallow)`

```
rule4 violation (silent swallow): <doc> <id> posted with N stock line(s)
but zero inventory_movements rows
(workflow: receipt_required=<bool> shipment_required=<bool>)
```

**Meaning:** The document is the movement owner for its workflow
mode, had stock lines, but no inventory was formed. This is the
exact bug class IN.3 exists to catch — historically a future
refactor could accidentally drop the `CreateXxxMovements` call.

**Handling:** Escalate to engineering with the doc type + ID and
the workflow state shown in the error. The transaction rolled back
automatically; the Bill / Invoice / Expense stays in draft, no GL
effect. Do not work around by re-posting — the root cause is code.

### `rule4 violation (duplicate owner)`

```
rule4 violation (duplicate owner): <doc> <id> is not the movement owner
under this workflow (receipt_required=<bool> shipment_required=<bool>)
but N inventory_movements rows exist with source_type=<doc>
```

**Meaning:** The document is NOT supposed to be the owner under
the current rail state — Bill under `receipt_required=true`, or
Invoice under `shipment_required=true`. But `inventory_movements`
rows with its own `source_type` exist. A legacy code path fired
that would double-count inventory (both Bill + Receipt, or both
Invoice + Shipment, forming movement).

**Handling:** Same as above — escalate immediately; tx rolled back.

Both errors are wrapped by the transaction layer so the operator
sees them as "save failed" with the full text. Forward the full
text to engineering; it already includes the doc ID + workflow
state needed to start diagnosis.

---

## 7. Operator workflows — choosing the right document

Simple decision tree for teaching operators:

```
Did physical goods arrive or leave the warehouse?
│
├─ No (pure service, labour, fee) → Expense OR Bill with
│   amount-only line. No Item picked. Pick the matching
│   Category account. Done.
│
├─ Yes, inbound (purchase):
│   │
│   ├─ Company runs legacy (`receipt_required=false`):
│   │   ├─ Paid immediately (card, cash, cheque)  → Expense with stock item + warehouse
│   │   └─ Vendor invoice comes later              → Bill with stock item + warehouse
│   │
│   └─ Company runs controlled (`receipt_required=true`):
│       Always: Receipt (records arrival + forms inventory)
│                  + Bill afterwards (AP claim, clears GR/IR)
│       Never:   Expense with stock item (rejected at post).
│
└─ Yes, outbound (sale):
    │
    ├─ Company runs legacy (`shipment_required=false`):
    │   Invoice with stock item → forms AR + COGS + inventory issue
    │
    └─ Company runs controlled (`shipment_required=true`):
        Shipment first (forms COGS + inventory issue + WFI queue)
        + Invoice afterwards (AR only, closes WFI)
```

Print this tree on the CS internal wiki if helpful; the runbook is
the canonical source for it.

---

## 8. Known limitations (Q4 and deferrals)

Pinned by IN.0 §2A:

- **No tracked items on Bill or Expense stock lines.** Q4. Tracked
  inbound continues to route through Phase G.4 (Bill with
  `bill_lines.lot_number`) or Receipt (Phase H). If a customer on
  IN.1 / IN.2 tries to pick a tracked item on a Bill stock line or
  Expense stock line, inventory rejects with
  `ErrTrackingDataMissing` at post.
- **No per-line warehouse override.** Q3 scoped header only. If a
  customer needs different lines of one document to land in
  different warehouses, the answer today is "split into separate
  documents" until a dedicated per-line-warehouse slice ships.
- **No retroactive linkage for pre-IN.1 / pre-IN.2 documents.**
  Existing amount-only Bill / Expense rows from before the item
  picker landed stay as amount-only memos forever — they never
  formed inventory before and won't start now. Operators wanting
  inventory on old documents must void + recreate with the Item
  picked.
- **No `PurchaseAccountID` field on ProductService.** IN.1 / IN.2
  use `InventoryAccountID → COGSAccountID` as the Category default.
  A dedicated purchase-side account on the catalog is a separate
  slice if demand appears.

---

## 9. Escalation

Loop in engineering if:

- Either `rule4 violation` error appears (either class — always
  engineering; tx already rolled back, no customer-side data
  damage, but root cause must be identified).
- A stock-line post succeeded but the inventory report disagrees
  with the JE amount (Rule #4 invariant broken between post and
  report-read; run the movement lookup by source to verify).
- Customer reports "I saved an Expense with a stock item in
  legacy mode, it posted, and nothing moved in Qty on Hand." Ask
  them to send the expense number; engineering will check for
  IN.3 regression in their specific code path.
- A customer insists on retroactive item linkage on historical
  amount-only documents (not supported; product decision).

Do NOT escalate for:

- Customer confusion about why water / consulting bills don't move
  inventory — that is §3 by-design.
- `ErrExpenseStockItemRequiresReceipt` — that is §4 Q2 surface;
  operator needs a Receipt instead.
- Missing payment account or warehouse — §6 configuration.
- Tracked item on Bill / Expense failing — §8 known limitation.

---

## 10a. Credit Note path (IN.5)

IN.5 extended Rule #4 to the AR-return side. Before IN.5, a customer
credit note for a returned stock item booked only the revenue-side
reversal (Dr Revenue / Cr AR) but never restored inventory or
reversed COGS — a silent-swallow **and** an accounting imbalance
(P&L showed $N of COGS against $0 of net revenue for the returned
goods).

### Legacy mode (`shipment_required=false`)

Credit Note is the movement owner for stock-line returns. On post:

1. Every CreditNoteLine with a stock item **must** carry
   `OriginalInvoiceLineID` — the specific InvoiceLine this return
   applies to. That FK is the cost-trace key.
2. The service looks up the original invoice's inventory_movement
   for that invoice_line, reads its `unit_cost_base` (authoritative
   original cost — March's COGS at March's cost, never today's avg),
   and books a fresh ReceiveStock movement at the return qty ×
   original cost.
3. JE gains `Dr Inventory / Cr COGS` per stock line at that same
   amount, balancing per line.
4. Partial returns supported: if the customer returns 4 of 10 sold,
   the Dr Inventory is 4 × $3 (not 10 × $3).

### Controlled mode (`shipment_required=true`)

Credit Note is **not** the outbound-return movement owner under
controlled mode. Ownership belongs to **Phase I.6a
`ARReturnReceipt`** (scope pinned 2026-04-21 in
[`PHASE_I6_CHARTER.md`](PHASE_I6_CHARTER.md); slice plan in
[`INVENTORY_MODULE_API.md`](INVENTORY_MODULE_API.md) §7 Phase I.6).

**I.6a.3 retrofit (shipped):** stock-item lines on a Credit Note
are no longer unconditionally rejected. They are **accepted iff
posted `ARReturnReceipt`s provide EXACT per-line coverage** — per
charter **Q6**, `Σ(posted ARReturnReceiptLine.qty WHERE
credit_note_line_id = X) == CreditNoteLine.qty` must hold for
every stock-item line. The post books a **revenue-only** JE
(`Dr Revenue / Cr AR`, plus tax reversal) — the Dr Inventory /
Cr COGS leg is owned by the paired `ARReturnReceipt`'s own post
(see `LedgerSourceARReturnReceipt`). `Rule4DocCreditNote.IsMovementOwner`
returns `false` under `shipment_required=true`; CreditNote does
not touch `inventory_movements` at all under controlled mode.

Short / over coverage at Credit Note post fails loud with
`ErrCreditNoteStockItemRequiresReturnReceipt`. The error name is
kept stable for triage-table continuity; the wrapped message now
cites `cn_qty=X posted_arr_coverage=Y` so the operator can diagnose
which ARReturnReceipt to add / post / void.

### Triage additions

| Symptom | Bug or by-design? | What to say |
|---|---|---|
| Customer returned goods, ledger shows revenue reversal but inventory didn't increase | **Pre-IN.5 behaviour — confirm post date.** | Credit notes posted BEFORE IN.5 shipped did not form inventory returns. Void + repost under the new path to get the inventory-side reversal (customer-facing AR balance stays the same; internal COGS / Inventory accounts correct). |
| `ErrCreditNoteStockItemRequiresInvoice` on credit note post | **Operator error.** | Standalone credit notes (no Invoice linkage) cannot carry a stock-item line — there is no original sale to trace cost from. Either link to the originating invoice OR remove the stock item and use a pure-service credit line. |
| `ErrCreditNoteStockItemRequiresOriginalLine` on credit note post | **Operator error (or pre-IN.5 data).** | The stock line needs `original_invoice_line_id` set — the specific invoice line being reversed. If operator needs help picking: match item + customer + ship date on the invoice. |
| `ErrCreditNoteStockItemRequiresReturnReceipt` on credit note post (post-I.6a.3) | **Coverage shortfall (Q6).** | Post-I.6a.3, this sentinel means posted-ARReturnReceipt coverage does NOT match the credit-note stock-line qty exactly. Wrapped message cites `cn_qty=X posted_arr_coverage=Y`. Fix: create / post an `ARReturnReceipt` linked to this CN whose line qtys sum to exactly the CN line qty; or adjust the CN line qty to match posted physical returns. |
| `ErrCreditNoteOriginalLineMismatch` on credit note post | **Data integrity.** | The `original_invoice_line_id` points at a line that isn't on the credit note's linked invoice (or isn't a stock line, or was never invoiced). Operator must re-pick; escalate if the data looks corrupt. |

### Void semantics

VoidCreditNote on a CN that had IN.5 inventory returns now also
reverses those returns — inventory goes back to the post-invoice
state. Note: the pre-existing CN rule "cannot void after
application to an invoice" still applies; if the CN has already
been applied to reduce AR balance, the application must be
removed first by a separate action.

### What is still NOT supported under IN.5

- **Multi-line partial returns where customer returns several
  different items on one credit note**: each line traces to its
  own OriginalInvoiceLineID. Supported, not special.
- **Return of a bundle-component item**: trace to the bundle's
  component movement; OriginalInvoiceLineID on the credit-note
  line matches the header invoice-line, not the bundle-expansion
  pseudo-line. If bundle returns are required, trace resolution
  is more complex; raise as a dedicated slice.
- **Cost-adjusted returns** (customer returns 4 of 10 but says "the
  cost was actually $X, not what's on the original movement"):
  not supported — IN.5 always uses the original snapshot. If
  costs need correction, use a separate inventory adjustment.

---

## 10b. Vendor Credit Note path (IN.6a)

IN.6a mirrors IN.5 on the AP-return side. Before IN.6a, a vendor
credit note for a stock-item return booked only the AP-side
adjustment (Dr AP / Cr Purchase Returns) but never decremented
inventory — a silent-swallow **and** an accounting imbalance
(inventory overstated by the value of goods physically sent back
to the vendor, with no matching asset reduction).

IN.6a is the first slice in AP-side Rule #4 work. It establishes
the line-level data model for Vendor Credit Notes (a new
`vendor_credit_note_lines` table) and wires stock-line returns
through `inventory.ReverseMovement` on the originating Bill
movement. The UI editor still captures a header amount only; line-
level entry lands in IN.6b. Until IN.6b ships, line-based VCNs are
reachable via API / tests; end-user UI flows continue to produce
header-only VCNs (see "Header-only legacy path" below).

### Legacy mode (`receipt_required=false`)

Vendor Credit Note is the movement owner for stock-line returns.
On post:

1. Every VendorCreditNoteLine with a stock item **must** carry
   `OriginalBillLineID` — the specific BillLine this return
   applies to. That FK is the cost-trace key.
2. The service locates the original Bill's inventory_movement for
   that bill_line, verifies the VCN line qty equals the original
   movement qty (full-qty return), and calls
   `inventory.ReverseMovement` on it. ReverseMovement reads the
   original `unit_cost_base` and writes a negative-delta row at
   that cost — inventory out, at the cost we paid.
3. JE gains `Dr OffsetAccount / Cr Inventory` per stock line at
   the traced cost. The Offset account's original credit (from
   the header `Dr AP / Cr Offset` fragment) is aggregated with
   the appended debit so the stock portion nets to `Dr AP /
   Cr Inventory`, the correct shape for a physical return.
4. Partial returns **not** supported (see "What is still NOT
   supported" below).

### Controlled mode (`receipt_required=true`)

Vendor Credit Note is **not** the AP-return movement owner under
controlled mode. Ownership belongs to **Phase I.6b
`VendorReturnShipment`** (UI label: "Return to Vendor"; charter Q2
— internal name avoids collision with pre-existing
`models.VendorReturn`; goods go *out* to the vendor so the shape
mirrors Shipment, not Receipt).

**I.6b.3 retrofit (shipped):** stock-item lines on a Vendor Credit
Note are no longer unconditionally rejected under
`receipt_required=true`. They are **accepted iff posted
`VendorReturnShipment`s provide EXACT per-line coverage** — per
charter **Q6**, `Σ(posted VendorReturnShipmentLine.qty WHERE
vendor_credit_note_line_id = X) == VendorCreditNoteLine.qty` must
hold for every stock-item line. The VCN's header JE
(`Dr AP / Cr Offset`) is reduced by Σ(stock-line amounts) — the
stock portion's accounting is owned entirely by VRS, which already
booked `Dr AP / Cr Inventory` at the traced original Bill cost via
the `IssueVendorReturn` narrow verb (charter Q3). If the VCN is
stock-only at cost, **no JE is produced at all** — VRS covered
everything.

`Rule4DocVendorCreditNote.IsMovementOwner` returns `false` under
`receipt_required=true`; VCN does not touch `inventory_movements`
under controlled mode. Partial-qty AP returns (IN.6a's deferred
gap) are now tractable via multiple `VendorReturnShipment`s summing
to the VCN line qty before VCN post.

Short / over coverage at VCN post fails loud with
`ErrVendorCreditNoteStockItemRequiresReturnReceipt`. The error
name is kept stable for triage continuity; the wrapped message
cites `vcn_qty=X posted_vrs_coverage=Y`.

**Posted-void extension (Q5 symmetry):** under controlled mode, a
posted VCN can be voided — reverses its own JE document-locally;
the paired VRS is not cascaded. Legacy-mode posted-void is still
rejected (IN.6a's inventory-reversal rows can't be re-reversed by
design; a follow-on slice may add a fresh-inflow-at-original-cost
path if demand emerges).

### Header-only legacy path

VCNs created without any lines (all pre-IN.6a VCNs, and the current
UI editor which has no line grid yet) continue through the
original `Dr AP / Cr Offset` two-line JE unchanged. Treat these as
price-adjustment credits, not physical stock returns. If an
operator needs to record a physical stock return for inventory
purposes, they must use the line-based flow (currently API/test
only; IN.6b will expose it in the UI).

### Triage additions

| Symptom | Bug or by-design? | What to say |
|---|---|---|
| Vendor Credit Note posted, AP reduced but inventory stayed up | **Header-only legacy path OR pre-IN.6a.** | If the VCN has zero lines, it's a price adjustment — inventory is correct to stay. For a physical return, the VCN must carry a stock line with `original_bill_line_id`. Use IN.6b UI (when shipped) or the line-based API. |
| `ErrVendorCreditNoteStockItemRequiresReturnReceipt` on VCN post | **Q-parity controlled-mode rejection.** | Not a bug. Controlled mode requires the locked I.6b `VendorReturnShipment` path (charter: [`PHASE_I6_CHARTER.md`](PHASE_I6_CHARTER.md) Q2; UI label "Return to Vendor"; slice **I.6b.3** retires this rejection). Until I.6b ships, use a manual JE (Dr AP / Cr Inventory at original cost) and keep the VCN as draft or remove the stock line. |
| `ErrVendorCreditNoteStockItemRequiresBill` on VCN post | **Operator error.** | Standalone VCN (no linked Bill) cannot carry a stock-item line — there's no original purchase to trace cost from. Either link to the originating Bill OR remove the stock line. |
| `ErrVendorCreditNoteStockItemRequiresOriginalLine` on VCN post | **Operator error (or pre-IN.6a data).** | The stock line needs `original_bill_line_id` set. If operator needs help picking: match item + vendor + receive date on the Bill. |
| `ErrVendorCreditNotePartialReturnNotSupported` on VCN post | **Q-scope limitation in IN.6a.** | Not a bug. The inventory module's outflow verbs don't accept a caller-supplied cost, so partial returns of stock items are deferred. Workarounds: split the original Bill into smaller lines, return each in full; OR use a manual JE for the partial-return portion. |
| `ErrVendorCreditNoteOriginalLineMismatch` on VCN post | **Data integrity.** | The `original_bill_line_id` points at a line that isn't on the VCN's linked Bill (or the Bill movement doesn't exist — e.g. the Bill was posted under controlled mode which skips movement formation). Operator must re-pick; escalate if the data looks corrupt. |

### Void semantics

VoidVendorCreditNote today only handles draft VCNs. Posted VCNs
cannot be voided — this is the pre-IN.6a constraint and IN.6a does
not change it. Consequence: an IN.6a stock-line return's inventory
reversal is permanent once posted. If operators post in error,
they must create a compensating inbound adjustment manually.
Phase **I.6b.3** (charter Q5 symmetry) extends void to posted VCNs
with **document-local, cascade-free** movement reversal — the
posted-void reverses its own JE only; a paired
`VendorReturnShipment` must be voided separately. Not in IN.6a.

### What is still NOT supported under IN.6a

- **Partial-qty stock returns** (return 4 of 10 purchased): the
  inventory module's `IssueStock` verb intentionally does not
  accept a caller-supplied unit cost ("callers never pass a cost
  on outflow"). Phase **I.6b.2a** (charter Q3) ships a **dedicated
  narrow-semantic inventory verb** (working name `IssueVendorReturn`
  / `ReturnToVendorAtTracedCost`) that takes lineage + intent
  only — the module reads `unit_cost_base` internally. Combined
  with `VendorReturnShipment` as a first-class document (I.6b.2)
  and the controlled-mode retrofit (I.6b.3), partial-qty AP returns
  become tractable via a sequence of smaller `VendorReturnShipment`s.
  A generic `UnitCostOverride` on `IssueStock` was explicitly
  **rejected** by Q3 to preserve inventory engine cost authority.
  Legacy-mode (`receipt_required=false`) workaround until I.6b:
  split the original Bill into smaller lines.
- **Posted-void of VCN with inventory effect**: deferred;
  see "Void semantics" above.
- **UI line entry**: deferred to IN.6b. End-user VCNs remain
  header-only until that slice ships.
- **Standalone / cross-Bill stock credits**: VCN must link to a
  single Bill; a VCN cannot span multiple Bills for stock lines.

---

## 10c. Refunds are exempt from Rule #4

An audit (2026-04-21) concluded that both **ARRefund** (customer
refund, [ar_refund.go](internal/models/ar_refund.go)) and **VendorRefund**
(vendor refund, [ap_vendor_refund.go](internal/models/ap_vendor_refund.go))
are **out of scope** for Rule #4. This section documents the
decision so future audits don't re-open the question.

### Why they're exempt

Both are pure cash-movement documents:

- **Header-only by design.** No line-item tables (no `ARRefundLine`
  or `VendorRefundLine`). No `ProductServiceID` fields anywhere on
  the model.
- **Two-line JE, cash ↔ AR/AP/Deposit only.** `PostARRefund`
  ([ar_refund_service.go:215](internal/services/ar_refund_service.go))
  emits `Dr DebitAccount / Cr BankAccount`. `PostVendorRefund`
  ([vendor_refund_service.go:192](internal/services/vendor_refund_service.go))
  emits `Dr BankAccount / Cr CreditAccount`. Neither path touches
  Inventory or COGS accounts.
- **Zero inventory module calls.** No `inventory.ReceiveStock`,
  `IssueStock`, `ReverseMovement`, etc., in either post path. No
  `inventory_movements` rows produced.

### Where the inventory effect actually lives (if any)

A refund that's linked to a CreditNote / VendorCreditNote is the
**cash settlement** of a credit that already posted. The inventory
restoration / reversal happened at the credit note's post time
(IN.5 for AR, IN.6a for AP). The refund just moves the
balance out of the AR/AP/Credit account and into the bank.

Standalone refunds (no linked credit — e.g. refunding an
overpayment or a vendor prepayment) touch only liability /
prepayment accounts. There is no stock nexus by construction.

### When to revisit

If a future slice adds a `refund_lines` table or a
`ProductServiceID` field to either refund model, **this exemption
is void** and Rule #4 must be re-evaluated. Watch for:

- A feature request for "refund with partial stock return detail"
  (operator wants to record which items were physically returned
  when issuing the refund).
- A merge of `VendorReturn` + `VendorRefund` semantics that adds
  item-level detail to the refund.

Either pattern should open a fresh audit slice rather than
smuggle inventory exposure into a refund post path.

---

## 11. Change log

| Date | Change |
|---|---|
| 2026-04-21 | Initial draft covering IN.1–IN.3 behaviour. Complements Phase H / Phase I runbooks. |
| 2026-04-21 | Added §10a Credit Note (IN.5) — AR return path inventory restoration at authoritative original cost. |
| 2026-04-21 | Added §10b Vendor Credit Note (IN.6a) — AP return path inventory reversal via ReverseMovement at authoritative original cost; full-qty only. |
| 2026-04-21 | IN.6b shipped — Vendor Credit Note editor exposes stock-return lines in the UI (+ handler wiring tests). |
| 2026-04-21 | Added §10c — ARRefund / VendorRefund audited and exempted from Rule #4 (cash-movement documents, no stock nexus). |
| 2026-04-21 | **I.6.0** — Phase I.6 charter ([`PHASE_I6_CHARTER.md`](PHASE_I6_CHARTER.md)) adopted into [`INVENTORY_MODULE_API.md`](INVENTORY_MODULE_API.md) §7 Phase I.6. §10a / §10b placeholders repointed from "planned / future" to the locked charter + specific slice references (`ARReturnReceipt` / I.6a.3; `VendorReturnShipment` / I.6b.2a / I.6b.3). Q2 misnomer ("Vendor Return Receipt" → `VendorReturnShipment` with UI label "Return to Vendor") corrected in §10b. |
| 2026-04-21 | **I.6a.1 / I.6a.2** — `ARReturnReceipt` + `ARReturnReceiptLine` migrations, models, CRUD/lifecycle service, Dr Inventory / Cr COGS at traced cost under `shipment_required=true`, Q5 document-local void. |
| 2026-04-21 | **I.6a.3** — CreditNote controlled-mode retrofit: stock lines accepted under `shipment_required=true` iff posted-ARR coverage matches exactly (charter Q6). CN JE becomes revenue-only under controlled mode; `Rule4DocCreditNote.IsMovementOwner` surrenders to new `Rule4DocARReturnReceipt`. Rule #4 post-time invariant added on `PostARReturnReceipt`. |
| 2026-04-21 | **I.6a.4** — UI: `/ar-return-receipts` list / detail / new / post / void / delete. "Create matching Return Receipt" shortcut on CreditNote detail (visible whenever CN has stock-item line, Q4 pattern). Sidebar entry under Inventory + Customers mega-menu entry ("Return Receipt"). |
| 2026-04-21 | **I.6a.5** — `PHASE_I6_RUNBOOK.md` (CS-facing operator runbook, AR side) + `PHASE_I6A_PILOT_ENABLEMENT.md` (layered pilot on Phase-I-green companies, 5 pre-flight gates, daily check SQL, 3-week observation window) + `phase_i6a_smoke_test.go` (split-return 6+4 summing; void + re-post coverage restoration). AR side of Phase I.6 complete. |
| 2026-04-21 | **I.6b.1** — AP side opens. `migrations/083` + `VendorReturnShipment` / `VendorReturnShipmentLine` models + GORM registration. Charter Q2 naming: internal `VendorReturnShipment` avoids collision with pre-existing `models.VendorReturn`; UI label "Return to Vendor". Q7 nullable FKs. Document-shell-only — no inventory/JE side effects until I.6b.2 (which will call the dedicated narrow traced-cost outflow verb from I.6b.2a). |
| 2026-04-21 | **I.6b.2a** — `inventory.IssueVendorReturn` narrow verb (charter Q3 keystone). Caller passes `OriginalMovementID` + qty; module reads `unit_cost_base` from source movement internally. Rejects reversal rows / outflow rows / zero-cost rows as anchors. Emits `movement_type='vendor_return'`, caller-supplied SourceType. No PPV leg, no FIFO layer peel, no `IssueStock` modification (Q3 explicit). Idempotent replay via `IdempotencyKey`. 7 contract tests pin the cost-authority guarantees. |
| 2026-04-21 | **I.6b.2** — VRS service layer (`CreateVendorReturnShipment` / `PostVendorReturnShipment` / `VoidVendorReturnShipment`). Calls `inventory.IssueVendorReturn` per stock line at `source_type='vendor_return_shipment'`. Rail-aware: controlled mode books Dr AP / Cr Inventory at traced Bill cost; legacy = status flip only. Q5 document-local void (no cascade to paired VCN). `Rule4DocVendorReturnShipment` owner dispatch added. Break from AR symmetry documented in service file + `LedgerSourceVendorReturnShipment` comment: VRS carries BOTH legs because original Bill has only Inventory+AP legs (no separate Revenue/COGS split to mirror). 7 contract tests. |
| 2026-04-21 | **I.6b.3** — VCN controlled-mode retrofit: stock lines accepted under `receipt_required=true` iff posted-VRS coverage matches exactly (charter Q6). Header JE `Dr AP / Cr Offset` reduced by Σ(stock-line amounts) — if VCN is stock-only at cost, NO JE produced (VRS already booked full reversal). `Rule4DocVendorCreditNote.IsMovementOwner` surrenders to `Rule4DocVendorReturnShipment` under controlled mode. Inventory-path call (`CreateVendorCreditNoteInventoryReturns`) skipped under controlled mode — zero VCN-sourced movements, non-owner invariant holds. **Posted-void extension (Q5 symmetry)**: controlled-mode posted VCN can be voided — reverses own JE; legacy-mode posted-void still rejected (IN.6a reversal rows can't be re-reversed; follow-on slice). Partial-qty AP returns tractable via multiple VRS summing per VCN line — closes IN.6a's deferred gap (PHASE_I6_RUNBOOK §5 item will be updated when AP-side runbook lands in I.6b.5). 5 contract tests (exact/partial/over coverage + dispatch + posted-void). |
| 2026-04-21 | **I.6b.4** — UI: `/vendor-return-shipments` list / detail / new / post / void / delete (UI label "Return to Vendor" per charter Q2). "Create Return to Vendor" shortcut on VCN detail (visible whenever VCN has stock-item line, Q4 pattern — mirrors ARR's "Create matching Return Receipt"). Sidebar entry under Inventory + Suppliers mega-menu entry ("Return to Vendor"). Charter Q2 UI-vs-internal-name split preserved throughout user-visible strings. |
| 2026-04-21 | **I.6b.5** — `PHASE_I6B_PILOT_ENABLEMENT.md` (layered pilot on Phase-H-green companies; 5 gates + 6 daily SQL checks including traced-cost-match + double-count guards) + `PHASE_I6_RUNBOOK.md` §§9–15 AP body (operator workflow, identity chain, triage, known limits, escalation) + `phase_i6b_smoke_test.go` (split-return 6+4, void + re-post coverage restoration). **Phase I.6 complete** — AP side matches AR side in depth. |
| 2026-04-21 | **IN.7** — Schema patch closing a historical silent gap. `migrations/084_shipments_add_journal_entry_id.sql` adds `shipments.journal_entry_id` — the column Phase I.3 wrote to via `s.JournalEntryID = postedJEID` but which migration 076 never created. Invisible in dev/tests (AutoMigrate quietly added it) but would have failed on first production `shipment_required=true` flip with a "column does not exist" error, blocking both Phase I pilot and Phase I.6 layered pilots. Defense-in-depth also registers Receipt / ReceiptLine / Shipment / ShipmentLine / WaitingForInvoiceItem in `db.AutoMigrate` so future inventory-document column additions are picked up automatically. Analogous to `migrations/070_receipt_post_wiring.sql` which did this same patch for `receipts` at H.3 time. |
| 2026-04-21 | **IN.8** — Rule #4 post-time invariant extension to Receipt (H.3) and Shipment (I.3) post paths. Adds `Rule4DocReceipt` + `Rule4DocShipment` with owner-iff-controlled-rail dispatch (mirror of Bill / Invoice surrender under H.4 / I.4). `PostReceipt` and `PostShipment` now run `AssertRule4PostTimeInvariant` at the tail, catching any future refactor that drops the `CreateReceiptMovements` / `CreateShipmentMovements` call. Rule #4 is now enforced post-time across the full 9-document family (Bill / Invoice / Expense / CreditNote / VCN / ARReturnReceipt / VendorReturnShipment / Receipt / Shipment). Closes the "Does NOT run on Receipt/Shipment posts" gap that pre-IN.8 `rule4_invariant.go` explicitly called out. 7 contract tests. |
| (future) | Update §3 triage table when a tracked-on-Bill or per-line-warehouse slice ships. |
| (future) | Replace §10b partial-return limitation with the narrow inventory verb + `VendorReturnShipment` workflow when slices **I.6b.2a** / **I.6b.2** / **I.6b.3** ship. |
| (future) | Replace §10b controlled-mode rejection guidance with the end-to-end `VendorReturnShipment` workflow when slice **I.6b.3** ships. |
| (future) | Replace §10b posted-VCN void limitation with the cascade-free posted-void path when slice **I.6b.3** ships (charter Q5 symmetry). |
| (future) | Revisit §10c if a refund document gains line-item granularity or ProductServiceID. |

---

**One-line summary for CS dashboards:**

*Rule #4 = stock-item line must form inventory OR be rejected loudly. Blank item = amount-only legacy. Expense + receipt_required=true = rejected (use Receipt). `rule4 violation` errors are always engineering.*
