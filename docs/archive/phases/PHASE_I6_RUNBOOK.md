# Phase I.6 Runbook — Return Receipts

> **Audience.** Customer Support, operators on pilot companies, and
> engineers triaging stock-return issues. AR side (I.6a) is the first
> body of this runbook. The AP side (I.6b — `VendorReturnShipment`)
> lands in its own section when that slice ships.

---

## TL;DR

Under **controlled mode** (`companies.shipment_required=true`) a
customer-return stock-item credit **cannot post on the Credit Note
alone**. The operator must:

1. **Draft the Credit Note** with the stock-item lines (do NOT post).
2. **Create a matching Return Receipt** from the CN detail page's
   *More → Create matching Return Receipt* button.
3. **Adjust Return Receipt line qty** to what actually came back to
   the warehouse (partial returns supported via multiple Return
   Receipts summing to the CN line qty).
4. **Post the Return Receipt.** This books `Dr Inventory / Cr COGS`
   at the **original sale's traced cost** and lands an
   `inventory_movements` row with `source_type='ar_return_receipt'`.
5. **Post the Credit Note.** This books the **revenue-only JE**
   (`Dr Revenue / Cr AR`). The Credit Note NEVER touches Inventory /
   COGS under controlled mode — that leg is owned by the paired
   Return Receipt.

Under **legacy mode** (`shipment_required=false`), the pre-I.6a path
still works byte-identically: a stock-item Credit Note can post
directly and IN.5 restores inventory at traced cost. Return Receipts
are **optional** under legacy (physical-tracking convenience only,
no system-truth effect).

---

## 1. When does the Return Receipt path apply

| Company rail state | AR stock-credit behavior |
|---|---|
| `shipment_required=false` (legacy) | Credit Note direct post forms inventory (IN.5). Return Receipt optional and inert. |
| `shipment_required=true` (controlled, Phase I piloted) | Credit Note requires posted Return Receipt coverage. Return Receipt owns the inventory leg; CN is revenue-only. |

The rail is set at the company level and flipped ON only after Phase
I pilot-green. See `PHASE_I_PILOT_ENABLEMENT.md` for the main rail
pilot; see `PHASE_I6A_PILOT_ENABLEMENT.md` for the I.6a-specific
layered pilot.

---

## 2. The operator workflow (controlled mode)

### Step 1 — Draft the Credit Note

- Normal Credit Note creation flow. Link to the original Invoice
  (required for stock-item lines).
- Per stock-item line, set `OriginalInvoiceLineID` to the specific
  invoice line being reversed.
- **Do NOT post yet.** Leave in draft.

### Step 2 — Open the Credit Note detail page

- The **More** menu shows **"Create matching Return Receipt"**
  whenever the CN has a stock-item line.

### Step 3 — Create the Return Receipt (pre-filled)

- Clicking **Create matching Return Receipt** opens the Return
  Receipt form with:
  - Customer copied from the CN.
  - Warehouse pre-filled (first active warehouse; adjust if goods
    landed elsewhere).
  - Each stock-item CN line as a pre-filled Return Receipt line
    (product, qty, `CreditNoteLineID` link).
- The operator may **edit line qtys** down (e.g. only 6 of 10 came
  back on this receipt; the rest will be a second Return Receipt).
- **Save as draft.**

### Step 4 — Post the Return Receipt

- On the Return Receipt detail page, click **Post**.
- What happens:
  - `inventory_movements` row created with
    `source_type='ar_return_receipt'`.
  - Cost = **original sale's unit cost** (traced via
    `CreditNoteLine.OriginalInvoiceLineID` → Invoice / Shipment
    movement's `unit_cost_base`), NOT today's weighted average.
  - JE booked: `Dr Inventory / Cr COGS = qty × traced_cost` per
    line.
  - Return Receipt status → `posted`; JE linked via
    `journal_entry_id`.

### Step 5 — (Optional) Create additional Return Receipts

- For partial / split returns, repeat Steps 3–4 with a second
  Return Receipt covering the remaining qty. The Credit Note post
  (Step 6) checks `Σ(posted ARR-line.qty WHERE
  credit_note_line_id=X) == CreditNoteLine.qty` exactly, so split
  returns must sum to the CN line qty before CN post.

### Step 6 — Post the Credit Note

- Open the CN detail, click **Issue**.
- The CN post runs the **coverage check** (charter Q6):
  - For every stock-item CN line, posted Return Receipt lines
    linked to it must sum to EXACTLY the CN line qty.
  - Under-coverage → `ErrCreditNoteStockItemRequiresReturnReceipt`
    with wrapped `cn_qty=X posted_arr_coverage=Y`. Fix by posting
    more Return Receipts or adjusting CN line qty.
  - Over-coverage → same error. Fix by voiding excess Return
    Receipts or adjusting CN line qty.
  - Exact match → CN posts. JE is `Dr Revenue / Cr AR (+ tax
    reversal)` — **no Inventory / COGS touch**. Those were already
    booked by the Return Receipt.

---

## 3. Identity chain

Under controlled mode the full chain from original sale to inventory
restoration is:

```
InvoiceLine
    ↓ (CreditNoteLine.OriginalInvoiceLineID, IN.5 field)
CreditNoteLine
    ↓ (ARReturnReceiptLine.CreditNoteLineID, Q7 junior-side FK)
ARReturnReceiptLine
    ↓ (source_line_id on inventory_movements)
inventory_movement (source_type='ar_return_receipt')
```

Each hop is a nullable FK at the schema layer (Q7 hard rule #1) —
orphan rows stay recoverable. Legality is enforced at service layer
(save time on link fields; post time on coverage).

---

## 4. Bug vs by-design — AR return triage

| Symptom | Bug or by-design? | What to say |
|---|---|---|
| Credit Note post fails with `ErrCreditNoteStockItemRequiresReturnReceipt`, message cites `cn_qty=X posted_arr_coverage=Y` | **By design (Q6 coverage shortfall).** | Not a bug. Check posted Return Receipts linked to this CN. Difference = how much more to post (or how much to reduce CN line qty). |
| Return Receipt post fails with `ErrARReturnReceiptLineMissingOriginalInvoiceLine` | **Operator error (pre-IN.5 CN data).** | The CN line was created before IN.5 shipped or without an invoice trace. Edit the CN line to set `OriginalInvoiceLineID`, then retry the Return Receipt. |
| Return Receipt post fails with `ErrARReturnReceiptLineOriginalMovementNotFound` | **Data integrity.** | The original invoice line's inventory movement can't be found. Possible causes: controlled-mode invoice without matching shipment; legacy invoice that was voided. Escalate to Engineering. |
| Return Receipt post fails with `ErrARReturnReceiptCreditNoteVoided` (create/update) | **Operator error (Q8).** | The linked CN has been voided. Either un-void the CN (if still in draft/issued) or link the Return Receipt to a different, non-voided CN. |
| Return Receipt created but has no effect on inventory after post | **By design (legacy mode).** | Under `shipment_required=false`, Return Receipt post is status-flip only — IN.5's CN post still owns the inventory leg. The Return Receipt is physical-tracking documentation only under legacy. |
| After voiding a posted Return Receipt, the CN still posts | **Operator timing.** | Possible if the CN was posted BEFORE the Return Receipt void (coverage check only runs at CN post). If CN is still draft, its next post attempt WILL recheck coverage and reject if now short. |
| Voiding a posted Return Receipt did NOT void the paired Credit Note | **By design (Q5 document-local).** | Void is document-local — reverses only its own movement + JE. Operators must void the Credit Note separately if needed. This is intentional — cascades would silently unwind audit-linked documents. |

---

## 5. Known limitations (I.6a scope)

These are **deliberately deferred** in I.6a and surface as either
scope-out errors or workaround paths until a follow-on slice opens:

1. **Tracked lot / serial returns.** Return Receipt lines don't yet
   carry lot_number / expiry / serial-unit selections. Tracked-item
   returns under controlled mode fail at the inventory layer's
   `validateInboundTracking` guard. Workaround: temporarily drop
   `shipment_required=false`, post under legacy (IN.5 carries the
   tracking fields from the CN line), flip back. A dedicated
   tracking-enabled Return Receipt slice will replace this.
2. **Multi-warehouse split returns.** One Return Receipt = one
   warehouse. If a customer ships different items back to different
   warehouses, create one Return Receipt per warehouse; each must
   link to the same CN with lines summing to the per-line CN qty
   by product.
3. **Inspection / disposition statuses.** Return Receipt lifecycle
   is `draft → posted → voided`. No `received → inspected →
   acceptable/damaged/quarantined` layer. Damaged returns still
   restock today; write-off must be booked separately.
4. **Restocking fees.** Stay on the CN's financial surface, not the
   Return Receipt. Add as a non-stock CN line or a separate fee
   invoice.
5. **Standalone Return Receipts.** Charter Q8 forbids Return
   Receipts without a CN link. Operator workaround for
   physical-first workflows: draft the CN first (no accounting
   effect until posted), then create the Return Receipt.

---

## 6. Observation metrics (daily, during pilot)

See `PHASE_I6A_PILOT_ENABLEMENT.md` §5 for the full daily / weekly
checklist. Key metrics:

- **Open coverage shortfall count** — Draft CNs under
  `shipment_required=true` that have stock-item lines whose
  `Σ(posted ARR-line.qty) < CreditNoteLine.qty`. These block CN
  posting until resolved.
- **Return Receipt → CN post lag** — time from Return Receipt
  posting to corresponding CN post. Abnormally long lag suggests
  operator confusion.
- **Rule #4 invariant failures** — zero tolerance; any
  `rule4 violation (silent swallow)` or `rule4 violation (duplicate
  owner)` error in logs is an immediate escalation.
- **Return Receipt voided without CN void** — expected (Q5
  document-local); count only for operational visibility.

---

## 7. Escalation

### Engineering owns

- Any `rule4 violation` error message.
- `ErrARReturnReceiptLineOriginalMovementNotFound` with
  consistently reproducible data.
- Inventory balance drift between
  `SUM(inventory_movements.quantity_delta)` and
  `inventory_balances.quantity_on_hand` for any item after Return
  Receipt + CN post cycle.
- Any double-count symptom: both a `credit_note`-sourced AND an
  `ar_return_receipt`-sourced inventory_movement for the same
  return (should never happen — `Rule4DocCreditNote.IsMovementOwner`
  surrenders under controlled mode).

### CS owns

- Operator coverage-shortfall confusion — walk through Steps 3–6
  above.
- Return Receipt form pre-fill missing — verify CN is draft and has
  stock-item lines.
- Post-void re-post flow — the same Return Receipt cannot be
  re-posted after void; create a new Return Receipt.

---

## 8. What lands when Phase I.6b ships (AP side)

The AP-side body of this runbook (§§9–15 below) was added with the
I.6b.5 slice when the Return-to-Vendor surface became operationally
testable. Key AP-side facts surfaced below:

- Posted-void extends to Vendor Credit Notes under controlled mode
  (IN.6a's draft-only restriction relaxed by I.6b.3 for
  `receipt_required=true` companies).
- Partial-qty AP returns — IN.6a's deferred gap — tractable via
  multiple `VendorReturnShipment`s summing to the VCN line qty.
- Pilot stacking rule (charter Q9) applies: do NOT run an I.6b
  pilot on the same company with an active I.6a pilot window.

---

## 9. AP side — TL;DR

Under **AP controlled mode** (`companies.receipt_required=true`) a
vendor-return stock-item credit cannot post on the Vendor Credit
Note alone. Operator flow:

1. Draft the **Vendor Credit Note** (VCN) with stock-item lines,
   each carrying `OriginalBillLineID`.
2. Use **More → Create Return to Vendor** on VCN detail (Q4
   shortcut) to open a `VendorReturnShipment` (VRS; UI label
   "Return to Vendor") pre-filled from VCN lines.
3. Adjust VRS qty for partial / split returns.
4. **Post the VRS.** Books `Dr AP / Cr Inventory` at the traced
   original Bill cost via the `inventory.IssueVendorReturn`
   narrow verb (charter Q3).
5. **Post the VCN.** Coverage check: Σ(posted VRS qty) ==
   VCN-line qty per stock line. If stock-only at cost, VCN posts
   with NO JE — VRS already did the full reversal.

Under **legacy mode** (`receipt_required=false`) IN.6a still works
byte-identically: VCN-direct post forms the inventory reversal at
traced cost (full-qty only). VRS is optional and inert under
legacy.

---

## 10. AP side — operator workflow (controlled mode)

### Step 1 — Draft the VCN

Normal VCN creation. Link to original Bill; per stock-item line,
set `OriginalBillLineID` to the specific Bill line being reversed.
**Do NOT post yet.**

### Step 2 — Open VCN detail

The **More** menu shows **"Create Return to Vendor"** whenever the
VCN has a stock-item line.

### Step 3 — Create the Return to Vendor

Clicking pre-fills the VRS form with:
- Vendor copied from VCN.
- Warehouse pre-filled (first active; adjust if goods left a
  different warehouse).
- Each stock-item VCN line as a pre-filled VRS line (product, qty,
  `VendorCreditNoteLineID`).

Edit line qtys for partial returns (e.g. vendor credits for 10 but
only 6 physically shipped back on this VRS; the remaining 4 are a
second VRS). Save draft.

### Step 4 — Post the VRS

On VRS detail click **Post**. Effects:
- `inventory_movements` row with `source_type='vendor_return_shipment'`
  and `movement_type='vendor_return'`.
- Cost = traced Bill `unit_cost_base`.
- JE: `Dr AP / Cr Inventory = qty × traced_cost` per line,
  self-balancing.
- VRS status → `posted`.

### Step 5 — Additional VRSs if needed (split / partial)

Repeat Steps 3–4 with additional VRSs covering remaining qty.
VCN post (Step 6) requires total VRS coverage to match VCN line
qty EXACTLY.

### Step 6 — Post the VCN

VCN post runs the coverage check (Q6):
- For every stock-item VCN line, posted VRS qty must sum to VCN
  line qty. Short → rejection; over → rejection.
- Match → VCN posts. If stock-only at cost, NO JE produced
  (VRS owned everything). Non-stock lines book Dr AP / Cr Offset.

---

## 11. AP side — identity chain

```
BillLine
    ↓ (VendorCreditNoteLine.OriginalBillLineID, IN.6a field)
VendorCreditNoteLine
    ↓ (VendorReturnShipmentLine.VendorCreditNoteLineID, Q7 junior-side FK)
VendorReturnShipmentLine
    ↓ (source_line_id on inventory_movements)
inventory_movement (source_type='vendor_return_shipment',
                    movement_type='vendor_return')
```

Every hop is nullable at schema (Q7); legality at service / post
time; orphan rows recoverable.

---

## 12. AP side — bug vs by-design triage

| Symptom | Bug or by-design? | What to say |
|---|---|---|
| VCN post fails `ErrVendorCreditNoteStockItemRequiresReturnReceipt` with `vcn_qty=X posted_vrs_coverage=Y` | **Q6 coverage shortfall.** | Not a bug. Diff = how much more VRS to post, or how much to reduce VCN line qty. |
| VRS post fails `ErrVendorReturnShipmentLineMissingOriginalBill` | **Pre-IN.6a VCN data.** | VCN line needs `OriginalBillLineID`. Edit the VCN line, retry. |
| VRS post fails `ErrVendorReturnShipmentLineOriginalMovementNotFound` | **Data integrity.** | Original Bill movement can't be located. Possible causes: Bill was voided; Bill posted under controlled mode (no movement). Escalate. |
| VRS post fails `ErrInsufficientStock` | **Operator error / timing.** | Not a bug. Goods not currently in the named warehouse. Check transfers; adjust VRS warehouse or quantity. |
| VRS posted but VCN still rejected with coverage shortfall | **Operator timing — cache miss.** | Refresh VCN detail page. Coverage query reads live `vendor_return_shipments.status`. |
| Legacy-mode posted VCN cannot be voided | **By design.** | IN.6a reversal rows cannot be re-reversed; a fresh-inflow-at-original-cost path is a follow-on slice. Workaround: reverse via manual JE. |
| Controlled-mode posted VCN voided — VRS still posted | **Q5 document-local.** | Intentional. Void VRS separately if full unwind needed. |
| VCN posted but no JE produced | **Stock-only VCN at cost (controlled mode).** | By design — VRS booked the full Dr AP / Cr Inventory. VCN's JE-less post is correct. Bill application still reduces balance_due via metadata. |

---

## 13. AP side — known limitations

These are **deliberately deferred** in I.6b scope:

1. **Legacy-mode posted-VCN void.** IN.6a reversal rows can't be
   re-reversed by inventory.ReverseMovement. A follow-on slice may
   add a fresh-ReceiveStock-at-traced-cost path.
2. **Stock-line amount ≠ traced cost** (vendor credits at a price
   different from what we paid). I.6b.3 assumes amounts match;
   variance handling is a separate slice.
3. **Tracked lot / serial returns.** VRS lines don't yet carry
   lot/serial selections. Tracked-item returns under controlled
   mode fail at inventory-layer tracking guards. Workaround:
   temporary `receipt_required=false` flip for tracked returns.
4. **Multi-warehouse split returns.** One VRS = one source
   warehouse. Multi-warehouse split = multiple VRSs per VCN.
5. **Goods moved between receipt and return.** If a transfer
   occurred, VRS should leave from the current warehouse. Caller
   supplies WarehouseID; module honors it.

---

## 14. AP side — observation metrics (daily)

See `PHASE_I6B_PILOT_ENABLEMENT.md` §5 for the full SQL checklist.
Key signals:

- VCN coverage-shortfall backlog (growing = operator confusion).
- VRS → VCN post lag (long = workflow friction).
- `rule4 violation` log count (zero tolerance).
- Balance invariant (sum of movements == on-hand).
- Traced-cost match (VRS cost == Bill anchor cost).

---

## 15. AP side — escalation

### Engineering owns

- Any `rule4 violation` error.
- `ErrVendorReturnShipmentLineOriginalMovementNotFound` with
  reproducible input.
- Traced-cost mismatch (VRS movement cost ≠ source Bill movement
  cost).
- Any `vendor_credit_note`-sourced inventory_movement on a
  company with `receipt_required=true` (should never happen — VCN
  surrendered ownership).

### CS owns

- Coverage-shortfall operator confusion → walk through Steps 3–6.
- Missing "Create Return to Vendor" button → verify VCN has
  stock-item line and is not voided.
- Legacy-mode posted-void attempts → explain the deferred
  workaround.

---

## Change log

| Date | Change |
|---|---|
| 2026-04-21 | Initial draft — AR side (I.6a) complete through I.6a.5. AP side (I.6b) deferred to its own slice. |
| 2026-04-21 | **AP body added (I.6b.5)** — §§9–15 cover VRS + VCN controlled-mode workflow, identity chain, triage, known limits, daily metrics, escalation. |
