# Phase I.6 — Return Receipts Charter

> Status: **scope locked, pending implementation**. Q1–Q9 decisions
> in §5 were pinned 2026-04-21. This charter is the binding scope
> reference until I.6.0 merges the final section into
> `INVENTORY_MODULE_API.md` §7 Phase I.

---

## 1. Why I.6 exists

Phase I.6 closes two related gaps that IN.5 and IN.6a each deferred:

| Rail state | AR return (customer → us) | AP return (us → vendor) |
|---|---|---|
| `shipment_required=false` (legacy) | **IN.5 shipped** — Credit Note restores inventory at traced cost | — |
| `receipt_required=false` (legacy) | — | **IN.6a shipped** — Vendor Credit Note reverses Bill movement at traced cost |
| `shipment_required=true` (controlled) | **Rejected today** — `ErrCreditNoteStockItemRequiresReturnReceipt` | — |
| `receipt_required=true` (controlled) | — | **Rejected today** — `ErrVendorCreditNoteStockItemRequiresReturnReceipt` |

Under controlled mode, stock-item returns have **no path at all**
today. Operators must either (a) post a compensating manual journal
entry, (b) drop to legacy mode temporarily, or (c) decline to
record the return formally — all of which break Rule #4 / the
physical-truth principle that motivates the controlled rails.

I.6 introduces the **Return Receipt** document pair — the sell-side
and buy-side mirrors of the existing Receipt (H.3) / Shipment (I.3)
documents, specialised for the return direction:

- **AR Return Receipt** — inbound stock document; customer returns
  goods, warehouse books them back in. Mirrors Receipt in H.3's
  shape.
- **Vendor Return Shipment** — outbound stock document; we ship
  goods back to the vendor. Mirrors Shipment in I.3's shape.

Under controlled mode these documents become the **authoritative
physical-truth** for returns, and the companion Credit Note /
Vendor Credit Note drops back to being the financial-only
document — symmetric with how H.3 and I.3 reshaped Bill and
Invoice in the forward direction.

## 2. Existing reservations being discharged

- `INVENTORY_MODULE_API.md` §7 Phase I "Deliberately-deferred-to-
  later-phase": *"Phase I.6 — customer return workflow (return
  receive, inspect, disposition, return-to-vendor). Scheduled as
  a dedicated follow-on after I.5; not blocking I.5's close."*
- `RULE4_RUNBOOK.md` §10a: *"Phase I.6 Return Receipt is the
  planned owner"* for controlled-mode AR stock credits.
- `RULE4_RUNBOOK.md` §10b: *"a future Vendor Return Receipt slice"*
  for controlled-mode AP stock credits.
- `INVENTORY_MODULE_API.md` §11: the `inventory.return.*` and
  `inventory.return-to-vendor.*` permission strings are already
  reserved for Phase J to wire after I.6 ships.

## 3. Scope summary  *(locked)*

I.6 ships **two new business documents** plus the capability-rail
dispatch that makes them the movement owners:

### 3.1 ARReturnReceipt (customer-return inbound)

- New model + table `ar_return_receipts` / `ar_return_receipt_lines`.
- Lifecycle: `draft → posted → voided` (mirrors Receipt in H.3).
- Linked to a Credit Note header; each line links to a specific
  `CreditNoteLine` (which itself carries `OriginalInvoiceLineID`
  from IN.5). That gives the full chain:

  ```
  InvoiceLine → CreditNoteLine → ARReturnReceiptLine → inventory_movement
  ```

- Post emits `Dr Inventory / Cr COGS` at the authoritative traced
  cost (from the original Invoice's movement), and forms an
  `inventory_movements` row with `source_type='ar_return_receipt'`.
- Under `shipment_required=true`, ARReturnReceipt **is** the
  Rule #4 movement owner for AR-return stock lines. CreditNote
  accepts stock lines again but becomes financial-only: it posts
  `Dr Revenue / Cr AR` for the per-line revenue reversal, and
  relies on the paired ARReturnReceipt for the physical leg.

### 3.2 VendorReturnShipment (vendor-return outbound)

- New model + table `vendor_return_shipments` /
  `vendor_return_shipment_lines`.
- Lifecycle: `draft → posted → voided`.
- Linked to a Vendor Credit Note header; each line links to a
  specific `VendorCreditNoteLine` (which carries
  `OriginalBillLineID` from IN.6a). Chain:

  ```
  BillLine → VendorCreditNoteLine → VendorReturnShipmentLine → inventory_movement
  ```

- Post emits `Dr AP / Cr Inventory` at the traced original Bill
  cost, and forms an `inventory_movements` row with
  `source_type='vendor_return_shipment'`.
- Under `receipt_required=true`, VendorReturnShipment **is** the
  Rule #4 movement owner for AP-return stock lines.

### 3.3 Controlled-mode dispatch retrofits

- **CreditNote post** under `shipment_required=true`: stop
  rejecting stock lines. Instead, require at least one posted
  ARReturnReceipt covering the credited qty, and book only the
  revenue leg.
- **VendorCreditNote post** under `receipt_required=true`: stop
  rejecting stock lines. Require at least one posted
  VendorReturnShipment covering the credited qty, and book only
  the AP-reduction leg.
- Update `Rule4DocCreditNote` / `Rule4DocVendorCreditNote` to
  surrender movement ownership to the new Return-Receipt types
  when the relevant rail is `true`.

### 3.4 Legacy mode untouched

Under both rails `false`, IN.5 / IN.6a continue to work byte-
identically. Return Receipt documents are **optional** under
legacy: operators may post a CreditNote alone (traced cost on
stock lines) without creating a Return Receipt. The new
documents become **required** only when the matching rail is
`true`.

## 4. Non-scope (belongs to follow-on slices)

- **Partial-qty AP return at traced cost** (the gap IN.6a
  documented). Under `receipt_required=true` via
  VendorReturnShipment, this becomes tractable because the
  shipment is a first-class document with its own qty; see Q3.
- **Return inspection / disposition** (accept back, reject,
  quarantine, dispose). Receipt lifecycle here is the simple
  `draft → posted → voided`. An inspection sub-status layer
  (e.g. `received → inspected → acceptable / damaged`) is a
  future slice.
- **Return-to-stock vs return-to-scrap disposition accounting.**
  Today the return always restocks. Write-off on return is a
  separate slice.
- **Multi-warehouse split returns.** One Return Receipt lands
  in exactly one warehouse. Splitting a credit across warehouses
  produces multiple Return Receipts, matching H.3 / I.3's
  single-warehouse rule.
- **Restocking fees and vendor restock charges.** Those remain on
  the financial (Credit Note) surface, not the Return Receipt
  surface.
- **Permission enforcement.** Reserved catalog strings
  (`inventory.return.receive`, `inventory.return.reverse`,
  `inventory.return-to-vendor.*`) become real permissions in
  Phase J, not I.6.
- **Historical backfill.** Credits posted before I.6 stay on
  IN.5 / IN.6a semantics; no retroactive Return Receipt
  generation.

## 5. Design decisions  *(all LOCKED 2026-04-21)*

Q1–Q9 below record the pinned decisions and the alternatives
considered. Each decision block notes which path was chosen and
why.

---

### Q1. One slice or two?  *(LOCKED 2026-04-21)*

**Question.** Ship I.6 as one combined slice (AR + AP together),
or split into **I.6a = ARReturnReceipt** and **I.6b =
VendorReturnShipment**?

**Decision.** *Split by side.* I.6a (AR side) ships first
because IN.5 has been in controlled-mode "rejected" state
longer; I.6b (AP side) follows.

Rationale: AR and AP return flows are structurally symmetric
but involve independent UI, independent editor flows, and
independent enablement pilots (one rail may flip before the
other). Splitting halves the review surface per slice and
lets pilot observation focus on one side at a time.

**Alternatives (rejected):**
- ~~(a) Combined — one migration, one charter close.~~ Larger
  PR, larger pilot surface.
- ~~(c) Split per layer (data / service / UI).~~ No operator
  sees any UX improvement until the last sub-slice ships.

### Q2. Document naming on the AP side  *(LOCKED 2026-04-21)*

**Question.** AR side is unambiguous — goods come *back in*, so
"Return **Receipt**" mirrors Receipt. On AP the goods go *out*
to the vendor — "Receipt" is backwards. What do we call it?

**Decision.**
- **Internal model / table / source_type**: `VendorReturnShipment`
  (`vendor_return_shipments` / `vendor_return_shipment_lines` /
  `source_type='vendor_return_shipment'`). Parallels the I.3
  Shipment document shape and keeps direction-accuracy.
- **UI label**: "Return to Vendor" (not "Vendor Return Shipment"
  in surface copy). This avoids collision with the existing
  `VendorReturn` business-semantic type already defined in
  `models.VendorReturn` (pre-existing AP concept linked from
  VendorCreditNote.VendorReturnID). "Return to Vendor" reads
  naturally in menus / breadcrumbs / page titles.
- The RULE4_RUNBOOK §10b current placeholder "Vendor Return
  Receipt" is a misnomer and will be corrected when I.6b runbook
  lands.

### Q3. Partial-qty returns on the AP side  *(LOCKED 2026-04-21)*

**Question.** IN.6a rejected partial-qty AP returns with
`ErrVendorCreditNotePartialReturnNotSupported` because
`inventory.IssueStock` refuses a caller-supplied cost. Under
I.6b with a dedicated VendorReturnShipment document, does the
partial path open back up?

**Decision.** *Yes, via a dedicated narrow-semantic inventory
verb — **NOT** a generic cost override on `IssueStock`.*

The new verb (name to be finalised during I.6b.2a; working
names: `IssueVendorReturn` / `ReturnToVendorAtTracedCost`) will:

- accept **lineage + intent only** from the caller — specifically
  the `OriginalBillLineID` (or equivalent) identifying which
  prior receipt's cost to trace, and the return quantity.
- execute the traced-cost lookup **inside the inventory module**
  — the module reads the original movement's `unit_cost_base`
  and writes the outflow at that exact cost.
- never expose a path where `IssueStock` or its callers supply
  a cost.

**Why not a generic `UnitCostOverride` on `IssueStock` with an
allow-list.** The charter author's first draft proposed that
route (a single optional field, gated by a source-type allow-
list). User override: that pattern weakens the inventory
engine's cost authority. Even gated, the allow-list becomes a
living hole — over time callers pressure to join it. The
authority principle in this repo has been consistent:
**Correctness > Flexibility**, **Backend Authority > Frontend
Assumptions**, and **AR/AP never own inventory qty or cost
truth**. A dedicated verb keeps the contract narrow by
construction: callers express lineage, not cost.

**Implementation note.** The new verb is an internal inventory
API surface, not a general-purpose outflow primitive. Scope is
bounded to return-to-vendor semantics. If later phases (scrap /
write-off at historical cost, tracked-lot manual reversal)
need the same traced-cost-outflow shape, they open their own
narrow-verb slice rather than widen this one.

**Alternatives (rejected):**
- ~~(a) Open partial via `IssueStockInput.UnitCostOverride`
  with allow-list.~~ **Rejected** — erodes inventory cost
  authority even if gated.
- **(b) Dedicated narrow verb (chosen).**
- (c) Leave partial unsupported — user-hostile; keeps IN.6a's
  documented gap open. Rejected.

### Q4. Auto-create vs manual linking  *(LOCKED 2026-04-21)*

**Question.** When an operator saves a stock-line Credit Note in
controlled mode, should the system auto-create a draft
ARReturnReceipt (pre-filled from the credit note lines), or
require the operator to create and link both documents
explicitly?

**Decision.** *Manual with a shortcut.* Keep the Credit Note and
Return Receipt independent documents. Expose a **"Create
matching Return Receipt"** action on the Credit Note detail
page that pre-fills an ARReturnReceipt draft from the credit-
note lines — same UX pattern as "Convert to refund" on VCN
today. Same shape on the AP side: "Create Return to Vendor"
action on VCN detail.

Rationale: hides nothing from the operator; preserves the
"physical truth is operator-driven" framing of H.3 / I.3;
retains the auditability of two explicit document creations.

**Alternatives (rejected):**
- ~~(a) Auto-create silently.~~ Conflicts with operator-driven
  physical truth.
- ~~(c) Fully manual, no pre-fill.~~ Operator must re-enter
  every line; encourages skipping the rail.

### Q5. Posted-void semantics  *(LOCKED 2026-04-21)*

**Question.** Can a posted Return Receipt be voided?

**Decision.** *Yes, with movement reversal — **document-local,
not chain-cascading**.*

Follow Receipt (H.3) and Shipment (I.3)'s existing pattern:

- Void of a posted ARReturnReceipt reverses the inventory
  movement via `inventory.ReverseMovement` and flips the document
  to `voided`.
- Void of a posted VendorReturnShipment does the mirror on the
  outbound side (reverse the outflow, back to on-hand).
- **Document-local rule.** The void only unwinds its **own**
  movement. The paired Credit Note / VCN post state is NOT
  cascaded — to fully unwind, the operator voids both documents
  separately, in whichever order makes sense for their audit
  trail. This matches the existing source-linked auditability
  posture: every reversal is its own explicit act, no silent
  cascades.

Symmetry note: enabling posted-void on VendorReturnShipment means
its paired VCN should also gain a posted-void path (IN.6a today
allows draft-void only). That VCN-posted-void extension lands in
the I.6b.3 slice alongside the controlled-mode retrofit, so the
documents stay symmetric.

**Alternatives (rejected):**
- ~~(b) Draft-void only — like today's VCN.~~ Operators can't
  correct mistakes on the rail side at all.
- ~~(c) Void only when NOT linked to a posted credit.~~ Partial
  solution, confusing.

### Q6. Qty-reconciliation rule at Credit Note post  *(LOCKED 2026-04-21)*

**Question.** Under controlled mode, when the Credit Note posts,
should we require that posted ARReturnReceipts fully cover the
credit-note qty per-line? Or accept partial coverage (credit
issued for more than physically received)?

**Decision.** *Full coverage required at post, per stock line.*

For every stock-item `CreditNoteLine X`:
`Σ(posted ARReturnReceiptLine.qty where credit_note_line_id = X)
== CreditNoteLine X.qty`

must hold before the Credit Note may post. Same shape for AP
(`VendorReturnShipmentLine` ↔ `VendorCreditNoteLine`).

Rationale: mirrors H.5's Bill-Receipt matching and preserves
the rail's physical-truth guarantee. Credit cannot outrun
physical return.

**Alternatives (rejected):**
- ~~(b) Partial coverage allowed.~~ Credit outruns receipt —
  reintroduces the silent-swallow class Rule #4 exists to
  prevent.
- ~~(c) Configurable per-company.~~ Adds a policy surface
  without operational justification.

### Q7. Identity chain — receipt-line FK direction  *(LOCKED 2026-04-21)*

**Question.** Does the ARReturnReceiptLine link UP to
CreditNoteLine (like H.5's `bill_line.receipt_line_id`), or
does CreditNoteLine link DOWN to a nullable
`ar_return_receipt_line_id` (like I.5's
`invoice_line.shipment_line_id`)?

**Decision.** *Junior = Return Receipt.*
- `ar_return_receipt_lines.credit_note_line_id` (nullable FK at
  schema, required at post).
- `vendor_return_shipment_lines.vendor_credit_note_line_id` (same
  shape).

**Hard rules** (promoted from Q7's risk mitigation):

1. **FK is nullable at schema layer.** Any physical document row
   may be inserted without a commercial-document link, so orphan
   data is recoverable rather than schema-locked.
2. **Legality is enforced at service / post time**, not by DB
   constraint. Draft Return Receipt may exist without the link;
   post requires it. This mirrors how IN.5 / IN.6a handle
   `OriginalInvoiceLineID` / `OriginalBillLineID` — nullable at
   schema, required at service.
3. **Posted CreditNote / VCN in controlled mode requires
   coverage at post time.** `Σ(posted ARReturnReceiptLine.qty
   where credit_note_line_id = X) ≥ CreditNoteLine.qty` must
   hold for every stock-item `CreditNoteLine X` before the
   Credit Note may post. Same shape for AP. Full coverage rule
   from Q6.
4. **Orphan Return Receipt rows are recoverable**, not
   destructive. If a commercial document is voided after the
   physical movement, the Return Receipt stays (its own void
   reverses its own movement independently — see Q5).

**Alternatives (rejected):**
- ~~(b) Junior = Credit Note.~~ Reverses the H/I convention.

### Q8. Standalone Return Receipt (no credit note)?  *(LOCKED 2026-04-21)*

**Question.** May an ARReturnReceipt exist without a linked
Credit Note? (E.g. operator wants to record physical return
movement before the credit-issuance paperwork catches up.)

**Decision.** *No — Return Receipt requires a draft-or-posted
Credit Note link at save time.* Same rule for VendorReturnShipment
vs VCN.

**Rationale — "conservative first cut", not "ideal shape".** This
decision keeps I.6.0's link topology closed and its pilot clean:
exactly one commercial document corresponds to each physical
document, chain direction fixed, post-time coverage rule
expressible. It intentionally does **not** try to solve
"warehouse received goods first, finance cuts the credit later"
as a first-class workflow in I.6.

**If real operational data later shows the physical-first pattern
is common**, that opens a dedicated follow-on slice
("standalone physical return + deferred credit reconciliation")
with its own charter. We don't widen I.6's scope to
accommodate it in advance. Operator workaround in the interim:
draft the Credit Note first (no accounting effect until posted)
to unblock the Return Receipt, then finalise Credit Note
content later.

**Alternatives (rejected):**
- ~~(b) Allow standalone.~~ Reintroduces orphan-movement risk
  and complicates coverage enforcement at Credit Note post. Not
  in I.6's scope.

### Q9. Capability-rail gate — can I.6 enable without I.5 / H.3 closure?  *(LOCKED 2026-04-21)*

**Question.** Does I.6 require the matching rail's main pilot to
be closed before I.6 is available on the same rail? Example: can
a company on `receipt_required=true` (H.3 pilot green) enable
I.6b independently of whether I.5 pilot is closed on their
`shipment_required`?

**Decision.** *I.6 follows the rail it sits on, not the cross-
rail — with an operational "don't stack pilots" constraint.*

- **I.6a** requires Phase I pilot closure (`shipment_required`
  green) on the target company before the company can enable
  stock-line Credit Notes in controlled mode.
- **I.6b** requires Phase H pilot closure (`receipt_required`
  green) on the target company.
- The two rails are independent: a company may run I.6a without
  `receipt_required` being enabled at all, and vice versa.

**Operational "don't stack pilots" constraint.** Even though the
rails are independent, a single company must not be placed in
*two* active pilot windows simultaneously. Specifically:

- If company X has `shipment_required` still in its Phase I
  stabilisation / observation window, do **not** begin an I.6b
  pilot on the same company X. Wait until one rail closes out.
- Same rule on the opposite side: I.6a pilot does not start if
  `receipt_required` pilot is still active on the same company.
- The rule is operational (CS / pilot runbook enforces it), not
  schema-level — code cannot distinguish "pilot window" from
  "stable operating state".

**Rationale.** Existing Phase H / Phase I playbook framing
explicitly treats the rails as independent but also explicitly
avoids concurrent pilots on the same real company because
observation signal and incident attribution get dirty when more
than one controlled-mode change is in flight. I.6 inherits that
discipline.

**Alternatives (rejected):**
- ~~(b) Require both rails closed before any I.6 on the company.~~
  Overly conservative — couples unrelated timelines.
- ~~(c) I.6 as a third independent pilot.~~ Adds another rail to
  gate; unnecessary since I.6's pilot is a narrower variant of
  the main rail's pilot.

## 6. Slice plan  *(LOCKED 2026-04-21)*

| Slice | Scope | Entry gate | Status |
|---|---|---|---|
| **I.6.0** | This charter adopted into `INVENTORY_MODULE_API.md` §7 Phase I, all Q/A pinned. | Q1–Q9 locked (done) | **READY TO START** |
| **I.6a.1** | Migration + model for `ar_return_receipts` / `ar_return_receipt_lines`. Nullable FK `credit_note_line_id` at schema. GORM registration. No service behaviour yet. | I.6.0 adopted | — |
| **I.6a.2** | Service layer: `CreateARReturnReceipt` / `PostARReturnReceipt` / `VoidARReturnReceipt`. Uses `inventory.ReceiveStock` at traced cost (via `CreditNoteLine.OriginalInvoiceLineID` → Invoice movement). `source_type='ar_return_receipt'`. Posted-void reverses own movement (Q5 document-local rule). | I.6a.1 shipped | — |
| **I.6a.3** | CreditNote controlled-mode retrofit. Under `shipment_required=true`: stop rejecting stock lines, accept them iff posted ARReturnReceipts provide **exact** per-line coverage (Q6 full-coverage rule), book revenue-only JE. `Rule4DocCreditNote.IsMovementOwner` surrenders ownership to `Rule4DocARReturnReceipt`. | I.6a.2 shipped | — |
| **I.6a.4** | UI — ARReturnReceipt editor (list / detail / new / post / void). "Create matching Return Receipt" action on CreditNote detail page (Q4 shortcut). | I.6a.3 shipped | — |
| **I.6a.5** | Pilot enablement doc + smoke suite. Only after pilot-green may any real company enable stock credits under `shipment_required=true`. Pilot stacking rule from Q9 applies. | I.6a.4 shipped | — |
| **I.6b.1** | Migration + model for `vendor_return_shipments` / `vendor_return_shipment_lines`. Nullable FK `vendor_credit_note_line_id`. GORM registration. | I.6a.5 shipped (AR first per Q1) | — |
| **I.6b.2a** | **Dedicated narrow-semantic inventory verb for return-to-vendor traced-cost outflow** (Q3 decision). Working names: `IssueVendorReturn` / `ReturnToVendorAtTracedCost` (final name pinned at slice start). Caller passes lineage + intent (`OriginalMovementID` of the source Bill movement, plus return qty); the verb reads `unit_cost_base` inside the module and writes the outflow at that exact cost. Writes no PPV leg; creates `inventory_movements` row with caller-supplied `SourceType` (e.g. `'vendor_return_shipment'`). **This slice does not touch `IssueStock`.** It's a new verb with a narrow contract. | I.6b.1 shipped | — |
| **I.6b.2** | Service layer: `CreateVendorReturnShipment` / `PostVendorReturnShipment` / `VoidVendorReturnShipment`. Calls the new verb from I.6b.2a. `source_type='vendor_return_shipment'`. Posted-void reverses own movement. | I.6b.2a shipped | — |
| **I.6b.3** | VendorCreditNote controlled-mode retrofit. Under `receipt_required=true`: stop rejecting stock lines, accept against posted VendorReturnShipment coverage (Q6 full-coverage rule). `Rule4DocVendorCreditNote.IsMovementOwner` surrenders ownership. Extends VCN posted-void to cascade-free reversal of its own JE only (paired VendorReturnShipment must be voided separately). Finally closes IN.6a's partial-qty-return deferred gap — partial returns work under I.6b via a sequence of smaller VendorReturnShipments. | I.6b.2 shipped | — |
| **I.6b.4** | UI — VendorReturnShipment editor. "Create Return to Vendor" shortcut on VCN detail (Q2 UI label, Q4 shortcut). | I.6b.3 shipped | — |
| **I.6b.5** | Pilot enablement doc + smoke suite. Pilot stacking rule from Q9 applies. | I.6b.4 shipped | — |

**Expected slice count: 11.** Comparable to Phase H + Phase I
main bodies given the cross-rail symmetry.

**Ordering note.** I.6a.1 through I.6a.5 ship end-to-end before
I.6b starts. This lets AR-side pilot close cleanly (no cross-
rail signal noise) and gives the inventory verb in I.6b.2a its
first full-spec cycle before wiring.

## 7. Exit conditions (all must hold before I.6 closes)

1. `ar_return_receipts` + `ar_return_receipt_lines` and
   `vendor_return_shipments` + `vendor_return_shipment_lines`
   persist; CRUD + lifecycle tested.
2. Under `shipment_required=true`, a posted CreditNote with
   stock-item lines requires (and is bound to) at least one
   posted ARReturnReceipt covering those lines; Rule #4
   invariant passes via dispatch to the new doc type.
3. Under `receipt_required=true`, same for VendorCreditNote ↔
   VendorReturnShipment pair. Partial-qty AP returns now work.
4. Under both rails `false`, IN.5 / IN.6a / legacy byte-
   identical.
5. Void of a posted Return Receipt reverses its inventory
   movement via `ReverseMovement`; the paired Credit Note's
   post is not cascaded.
6. Smoke suite covers: happy (credit → return receipt → match);
   partial (credit qty 10, return receipts qty 6+4); over-
   credit (credit qty 10, return receipt qty 6 only) → loud
   rejection on credit post; tracked-lot return; void + re-post.
7. Pilot enablement docs for each rail exist; operator runbook
   (`PHASE_I6_RUNBOOK.md`) exists.
8. `inventory.return.*` permission strings remain reserved in
   §11 — actual wiring deferred to Phase J.

## 8. Risks

- **Rail coupling creep.** If controlled-mode CreditNote starts
  depending on ARReturnReceipt state in subtle ways (e.g.
  automatic status transitions), the two documents' lifecycles
  can entangle. Discipline: the CreditNote post reads
  ARReturnReceipt **totals** only (per Q6's coverage rule), not
  subscribes to its status, and void is document-local (Q5) so
  cascades never run implicitly.
- **New inventory verb surface.** I.6b.2a introduces a new
  narrow-semantic outflow verb that reads `unit_cost_base` on
  the module side. Mitigations:
  - Verb contract is **narrow by name** — `IssueVendorReturn`
    (or equivalent) signals its single intent; there is no
    general `UnitCostOverride` parameter that future callers
    can hijack.
  - Caller passes lineage (`OriginalMovementID`), not cost.
    The cost-authority principle (AR/AP never own inventory
    cost truth) is preserved.
  - Scope is bounded to return-to-vendor semantics for I.6.
    Future similar verbs (scrap-at-historical-cost, etc.) open
    their own slices and their own names.
- **Identity-chain length.** Four hops for AR:
  InvoiceLine → CreditNoteLine → ARReturnReceiptLine → movement.
  Deep chains are harder to debug when something breaks. The
  Q7 hard rules (FK nullable at schema, legality enforced at
  post time, posted coverage required, orphan rows recoverable)
  are the mitigation — each layer can be inspected and repaired
  independently.
- **Operator training load.** Controlled mode now has 4 return
  documents operators may touch (CreditNote, VendorCreditNote,
  ARReturnReceipt, VendorReturnShipment) plus their creating /
  voiding rules. Runbook clarity matters; I.6a.5 / I.6b.5
  explicitly include a `PHASE_I6_RUNBOOK.md` deliverable.
- **Pilot stacking risk (Q9).** Two controlled-mode pilots on
  the same real company at the same time would make incident
  attribution messy. Operational rule in Q9 keeps pilots
  serialised per company; enforcement is CS-side, not code.

## 9. Out of charter (explicitly)

- No changes to `inventory_costing_method`.
- No changes to H.3 / H.5 / I.3 / I.5 document shapes beyond
  the new Rule4Doc dispatch entries.
- No new permission strings beyond the reservations already in
  §11 of INVENTORY_MODULE_API.md.
- No attempt to solve inspection / quarantine / disposition — a
  follow-on slice may layer those onto ARReturnReceipt.

---

**Change log**

| Date | Change |
|---|---|
| 2026-04-21 | Initial draft. Pending Q1–Q9 decisions. |
| 2026-04-21 | Q1–Q9 locked. Q2 split internal name (`VendorReturnShipment`) from UI label ("Return to Vendor") to avoid collision with existing `VendorReturn` model. Q3 flipped to dedicated narrow-semantic inventory verb, no generic `UnitCostOverride`. Q5 added "document-local, not chain-cascading" void rule. Q7 risk-mitigations promoted to hard rules. Q9 added operational "don't stack pilots on same company". Slice plan rewritten to reflect Q3 shift (I.6b.2a = new verb slice, not API extension). Status: scope locked, pending implementation. |
