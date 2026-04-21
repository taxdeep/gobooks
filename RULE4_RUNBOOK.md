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

Credit Note is **not** the outbound-return owner under controlled
mode. Stock-item credit notes are rejected at post with
`ErrCreditNoteStockItemRequiresReturnReceipt`. Phase I.6 Return
Receipt is the planned owner; until I.6 ships, the operator should
adjust inventory via a manual journal entry or wait for I.6.

### Triage additions

| Symptom | Bug or by-design? | What to say |
|---|---|---|
| Customer returned goods, ledger shows revenue reversal but inventory didn't increase | **Pre-IN.5 behaviour — confirm post date.** | Credit notes posted BEFORE IN.5 shipped did not form inventory returns. Void + repost under the new path to get the inventory-side reversal (customer-facing AR balance stays the same; internal COGS / Inventory accounts correct). |
| `ErrCreditNoteStockItemRequiresInvoice` on credit note post | **Operator error.** | Standalone credit notes (no Invoice linkage) cannot carry a stock-item line — there is no original sale to trace cost from. Either link to the originating invoice OR remove the stock item and use a pure-service credit line. |
| `ErrCreditNoteStockItemRequiresOriginalLine` on credit note post | **Operator error (or pre-IN.5 data).** | The stock line needs `original_invoice_line_id` set — the specific invoice line being reversed. If operator needs help picking: match item + customer + ship date on the invoice. |
| `ErrCreditNoteStockItemRequiresReturnReceipt` on credit note post | **Q2 controlled-mode rejection.** | Not a bug. Phase I.6 Return Receipt is the correct path; until it ships, customer needs a manual adjustment JE or deferred processing. |
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

## 11. Change log

| Date | Change |
|---|---|
| 2026-04-21 | Initial draft covering IN.1–IN.3 behaviour. Complements Phase H / Phase I runbooks. |
| 2026-04-21 | Added §10a Credit Note (IN.5) — AR return path inventory restoration at authoritative original cost. |
| (future) | Update §3 triage table when a tracked-on-Bill or per-line-warehouse slice ships. |
| (future) | Replace §10a controlled-mode rejection guidance with Phase I.6 Return Receipt workflow when that slice ships. |

---

**One-line summary for CS dashboards:**

*Rule #4 = stock-item line must form inventory OR be rejected loudly. Blank item = amount-only legacy. Expense + receipt_required=true = rejected (use Receipt). `rule4 violation` errors are always engineering.*
