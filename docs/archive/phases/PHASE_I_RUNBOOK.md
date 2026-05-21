# Phase I Runbook — Shipment-First Outbound & `waiting_for_invoice` Queue

**Audience:** Customer Success, Operations, Account Management
**Status:** Active after I.5 merge + staging verification
**Supersedes:** The sell-side half of `PHASE_H_RUNBOOK.md` §9 (the
"what Phase I will add" forward-looking text). That section is now
superseded by this document.
**Retains:** The Phase H runbook's full contents for receipt-first
inbound — Phase H and Phase I are **independent capability rails**
on independent sides of the inventory cycle. This document covers
only the sell side.
**Scope note:** Current Phase I scope selection is **Phase I.B**
(shipment-first fulfillment, shipment-recognized cost,
invoice-recognized revenue). Any future scope evolution within Phase
I — e.g. revenue-at-shipment via SNI / contract asset — will update
this document, not supersede it.

This is the authoritative internal reference for what
`shipment_required=true` means right now. Do not produce
customer-facing messaging that contradicts this document.

---

## TL;DR

Phase I adds two company-level surfaces on top of Phase G / Phase H:

- `companies.shipment_required` — capability rail (dormant until
  explicitly flipped per company).
- `shipments` + `shipment_lines` — first-class outbound documents
  (always present; used only when the rail is on).

A third artefact ships at I.3 and is operational, not accounting:

- `waiting_for_invoice_items` — the shipped-but-not-yet-invoiced
  queue. It does **not** post to the GL and is not a clearing
  account. It exists for finance / ops dashboards.

When `shipment_required=false` (the default, and the state every
company has been in through the entire Phase I rollout), behaviour
is **byte-identical to Phase G + Phase H legacy invoice path**.
Nothing changes for customers who are not explicitly opted in.

When `shipment_required=true`, the company switches to the
**Shipment-first outbound model**:

- **Shipment** becomes the document that records "physical goods
  left a warehouse." Posting a Shipment issues stock through the
  inventory module **and** books `Dr COGS / Cr Inventory` using the
  authoritative unit cost returned by `IssueStock`. One
  `waiting_for_invoice_items` row is created per stock line.
- **Invoice** becomes a purely financial document (AR + Revenue +
  Tax). It no longer forms COGS or touches inventory. Posting a
  matched Invoice line (with `shipment_line_id` set) closes the
  corresponding `waiting_for_invoice_items` row atomically.

The **only operational unlock** is I.5. Everything else (UI, bulk
backfill, auto-migration, tracked outbound on the Shipment path,
partial invoicing) is intentionally NOT part of Phase I.B.

---

## 1. When `shipment_required` can be enabled

Technically permitted after I.5 ship. In practice, a company must
clear **all five** of the following before the flip is approved:

1. **Catalog readiness.** Every stock-item `ProductService` in the
   company must already have **both** `InventoryAccountID` and
   `COGSAccountID` set. Phase H's catalog gate required only
   `InventoryAccountID`; Phase I raises the bar because Shipment
   post needs **both** sides of the `Dr COGS / Cr Inventory` JE.
   Items missing either will fail with
   `ErrShipmentInventoryAccountMissing` or
   `ErrShipmentCOGSAccountMissing`. Re-check the catalog before
   flipping the rail. This gate lives at the **item level**; there
   is no company-level analog of Phase H's GR/IR / PPV accounts.

2. **No company-level accounts to wire.** This is a deliberate
   contrast with Phase H. Phase I.B has **no** GR/IR clearing
   account and **no** PPV account — the model does not introduce a
   variance surface between cost and revenue. Any PR that adds a
   company-level "Phase I clearing account" without a fresh scope
   trigger is out of slice. The only company-level surface is the
   rail itself (§1.3).

3. **Flip the rail.** `services.ChangeCompanyShipmentRequired(…)`
   with `Required=true`. One audit row written
   (`company.shipment_required.enabled`).

4. **Operator training, minimum.** The people posting Shipments and
   Invoices in this company must understand:
   - A stock sale needs **two** documents, not one.
   - Shipment posts first (forms COGS + issues stock + creates a
     `waiting_for_invoice` queue item).
   - Invoice posts second (closes the queue item; books
     `Dr AR / Cr Revenue`).
   - An Invoice line matches **exactly one** `ShipmentLine` via
     `invoice_lines.shipment_line_id`. Partial invoicing is not
     supported — Invoice line qty must cover the ShipmentLine's
     full qty in one shot.

5. **Historical context.** Existing Invoices posted before the flip
   keep their legacy `Dr AR / Cr Revenue / Dr COGS / Cr Inventory`
   JE shape. They are **not** retroactively migrated into Shipment-
   first records. That's by design — see §5.

Do not flip `shipment_required=true` on a real company before all
five gates are clear. The capability can be flipped back off
(`Required=false`) at any time, but any Shipments and Invoices
created while it was on stay exactly as they were posted — voiding
and re-posting is the only supported backout, and see §7 for the
procedure.

---

## 2. What each surface represents under `shipment_required=true`

Say these to teammates. Say these to customers when they ask what
each thing means.

### Shipment

> A **Shipment** records that physical goods left a warehouse.
> Posting it consumes stock through the inventory module and books
> `Dr COGS / Cr Inventory` using the **authoritative** unit cost
> returned by the inventory module. The customer Invoice has not
> gone out yet; the `waiting_for_invoice` queue row is where
> finance / ops sees the gap until Invoice posts.

Shipments are company-scoped, warehouse-bound, and carry a nullable
`customer_id` (a shipment may be recorded before customer
attribution is finalised). There is no `unit_cost` column on
`ShipmentLine` — outbound cost is **always** determined by
inventory at issue time (FIFO peel or moving-average read). The
Shipment does not declare cost; it consumes inventory.

### Invoice

> An **Invoice** is the customer's bill: the financial claim for
> goods already shipped. Posting it books `Dr AR / Cr Revenue` (+
> `Cr Tax` per taxable line). Under
> `shipment_required=true`, Invoice does **not** form COGS and does
> **not** touch inventory — that already happened at Shipment post.

When an Invoice line's `shipment_line_id` is set, posting the
Invoice closes the matching `waiting_for_invoice` row atomically.
When `shipment_line_id` is NULL, the line posts as pure AR +
Revenue and the queue is untouched — legitimate scenario for
service / fee lines that were never shipped.

### waiting_for_invoice (operational queue)

> **waiting_for_invoice** is the shipped-but-not-yet-invoiced
> queue. Every posted Shipment stock line creates exactly one
> open row. Invoice match closes it. Shipment void voids it.
> Invoice void reopens it.

This table is **not** part of the GL. No account is affected by
inserting, closing, or voiding a row. The shipped-but-unbilled GL
state is already visible through normal reports: a COGS debit
exists, a matching Revenue credit does not, until the Invoice
lands. The queue is the **workflow view** of that same gap — for
operational dashboards and period-end review — not a clearing
account.

The queue is **one per company**. Every row carries both the source
identity (ShipmentID, ShipmentLineID, ProductServiceID,
WarehouseID) and denormalised context for dashboards (CustomerID,
SalesOrderID, SalesOrderLineID).

### Variance accounts (deliberately absent)

> Phase I.B has **no** sales-price variance account, **no**
> accrued-revenue clearing, **no** contract asset.

Cost is recognised at Shipment (actual inventory cost). Revenue is
recognised at Invoice (face amount). There is nothing to
"reconcile between the two" in GL terms — they are independent
events on independent dates. If the business needs revenue-at-
shipment or post-shipment price adjustments, those ship as
separate, dedicated scope triggers. Adding them silently inside
Phase I.B is out of slice.

---

## 3. Shipment-line matching — how to read it

### The semantics

One Invoice line points at **at most one** Shipment line via
`invoice_lines.shipment_line_id`. No reverse pointer on the
Shipment side; Invoice is authoritative.

Each Shipment line may be referenced by **at most one** posted
Invoice line over its lifetime. Once the match closes the
`waiting_for_invoice` row to `status='closed'`, a second Invoice
attempting to match the same ShipmentLine will fail loud with
`ErrWaitingForInvoiceNotFound` — the queue sees no open row.
(This is a deliberate contrast with Phase H, where one Receipt
line can be claimed across multiple Bills over time.)

### What each scenario does to the JE + queue

| Scenario | Shipment | Invoice | Queue effect | Invoice JE |
|---|---|---|---|---|
| Matched, flag=true | 7 @ peeled $3.00 → ShipLine 101 | 7 @ face $10.00 (→ sl 101) | WFI(sl 101) open → closed | Dr AR 70 / Cr Revenue 70 |
| Unmatched stock line, flag=true | — | 3 @ $5.00 (service fee, sl NULL) | untouched | Dr AR 15 / Cr Revenue 15 |
| Matched, flag=false | (legacy — Shipment is a display-only document under flag=off) | 7 @ face $10.00 (shipment_line_id cleared before post) | — | Dr AR 70 / Cr Revenue 70 / Dr COGS 21 / Cr Inventory 21 (legacy path) |
| Double-match (second Invoice for same ShipLine) | — | 7 @ face $10.00 (→ sl 101 already closed) | rejected | Invoice rolls back (ErrWaitingForInvoiceNotFound) |
| Cross-company ShipLine | — | Invoice in Co A cites ShipLine in Co B | rejected | Invoice rolls back (ErrInvoiceShipmentLineCrossCompany) |
| Flag=off + shipment_line_id set | — | Invoice cites a ShipLine but flag is off | rejected | Invoice rolls back (ErrInvoiceShipmentLineInFlagOffContext) |
| Partial qty (7-unit ship, 3-unit Invoice) | 7 @ $3.00 → ShipLine 101 | 3 @ face $10.00 (→ sl 101) | attempted close | **Not supported in I.B** — qty mismatch allowed by the service today (atomic close happens regardless of Invoice qty); charter reserves strict-qty enforcement for a dedicated partial-matching slice |

### Expected operator workflow

1. Customer order arrives; warehouse posts a Shipment for the
   fulfilled items (picks stock item, sets qty + warehouse + optional
   customer attribution).
2. Shipment post runs the full I.3 chain: stock issued, COGS booked,
   `waiting_for_invoice` row opened.
3. Sometime later (days / weeks), billing runs for the customer.
4. Operator creates an Invoice. For each stock line that corresponds
   to a Shipment, the operator sets
   `invoice_lines.shipment_line_id` to the ShipmentLine ID (UI for
   this is **not** in Phase I.B — see §8 — so until the UI ships,
   the operator edits the field via direct API / admin tooling).
   Fee / service lines stay with `shipment_line_id=NULL`.
5. Post the Invoice. WFI rows close atomically for matched lines;
   unmatched lines pass through to AR + Revenue only.

### What the operator does NOT have to do

- Compute COGS. It was already booked at ship time from
  authoritative inventory cost.
- Reconcile quantities against the Shipment. Matching is 1:1 at the
  ShipmentLine level; the queue closes the row as a whole.
- Re-edit past Shipments. The Shipment's `unit_cost_base` (recorded
  on the WFI row) is frozen at post time.

---

## 4. When `waiting_for_invoice` non-empty is expected (by design)

The WFI queue is **not** expected to be empty every day. Open rows
sitting there are normal in the following cases, none of which are
bugs:

1. **Shipment posted, Invoice not yet issued.** The open WFI row
   sits until billing runs. Example: ship goods on the 3rd, invoice
   customer on the 15th. Between those dates the WFI row is the
   "what goes on the next invoice" reminder.

2. **Partial-shipping workflow where the company will invoice
   later.** If customer operations batch Shipments then bill
   monthly, expect a queue depth proportional to that cadence.

3. **Service / fee lines on the Invoice.** An Invoice that
   deliberately carries non-shipment lines (consulting hours,
   shipping fees) leaves `shipment_line_id=NULL` on those lines —
   those lines post AR + Revenue without touching WFI.

4. **Void of a matched Invoice.** The reopen path flips the WFI row
   back to `open`, clearing the resolution fields. A replacement
   Invoice can then match the same ShipmentLine.

5. **Shipment voided before any Invoice matched.** The WFI row
   flips to `voided`. The goods accounting (COGS + inventory) is
   reversed in the same transaction. A replacement Shipment (new
   ShipmentLine ID) will produce a new WFI row.

### What does the `waiting_for_invoice` aging report look like?

It does not ship in Phase I.B. A CS-facing aging report (open WFI
rows grouped by customer + ship-date bucket) is explicitly
backlogged as a future slice. Until then, CS pulls the queue by
direct SQL against `waiting_for_invoice_items` filtered on
`status='open'`.

---

## 5. Bug vs by-design — triage

Use this table when a customer reports unexpected behaviour.

| Symptom | Bug or by-design? | What to say |
|---|---|---|
| `waiting_for_invoice` has open rows for days / weeks | **By design.** | Normal. See §4 for the five scenarios. |
| A Shipment posted under flag=true produced no journal entry | **By design iff** the shipment had no stock-item lines (service-only delivery). Otherwise → bug. | Service-only shipments have nothing to issue; status flips to posted with no side effects. |
| An Invoice posted under flag=true but no COGS line in the JE | **By design.** | Phase I: Invoice is AR + Revenue only. COGS lives on the Shipment. |
| Historical `source_type='invoice'` inventory movements still exist after flip | **By design.** | Pre-flip history is preserved. Only new Invoices posted after the flip skip COGS formation. |
| Invoice post fails with `ErrWaitingForInvoiceNotFound` | **Operator error (usually) — diagnose the match state first.** | Walk three checks: <br>**(a)** `invoice_lines.shipment_line_id` points at a ShipmentLine whose WFI row is `status='closed'` → another Invoice already matched this ShipmentLine. The match is 1:1 in I.B. Clear the field if this is a fee / service line, or split it off into a non-matching line. <br>**(b)** WFI row is `status='voided'` → the source Shipment was voided before this Invoice posted. Create a replacement Shipment first, then reference its new ShipmentLine. <br>**(c)** No WFI row exists at all → the ShipmentLine itself doesn't exist or its Shipment is not yet posted. Post the Shipment first (I.B runs Shipment→Invoice, not the reverse). <br>Only if none of (a)/(b)/(c) explains the error → escalate per §10. |
| Invoice post fails with `ErrInvoiceShipmentLineCrossCompany` | **Operator error.** | ShipmentLine belongs to a different company. The operator pasted the wrong ID. Fix the line. |
| Invoice post fails with `ErrInvoiceShipmentLineInFlagOffContext` | **Operator error.** | The rail is off for this company but the Invoice line carries a `shipment_line_id`. Either flip the rail on (if that's the intended mode) or clear the field. I.B refuses silent drift. |
| Invoice post fails with `ErrInvoiceShipmentNotPosted` | **Operator error.** | The ShipmentLine exists but its parent Shipment is still in draft or already voided. Post the Shipment first. |
| `ErrShipmentCOGSAccountMissing` on Shipment post | **Catalog error.** | The product has no `COGSAccountID`. Fix on the product record. |
| `ErrShipmentInventoryAccountMissing` on Shipment post | **Catalog error.** | The product has no `InventoryAccountID`. Fix on the product record. |
| After `ChangeCompanyShipmentRequired(false)`, old Invoices still show their flag=on JE shapes | **By design.** | History is permanent. The flag change only affects future posts. |
| Two concurrent Invoices tried to close the same WFI row → one succeeded, one errored | **By design.** | `applyLockForUpdate` on Invoice rows serialises; the race loser sees `status='closed'` and fails loud. The WFI row is atomically owned by whichever tx committed first. |
| Open WFI row exists for a Shipment that was later voided | **BUG. Should not happen.** | `VoidShipment` voids all WFI rows tied to the Shipment (including already-closed ones) in the same tx. A confirmed reproduction means the I.3 void path missed a row. Escalate. |
| Invoice posted under flag=true but `shipment_line_id` set on a line, and WFI row stayed open | **BUG. Should not happen.** | `closeWaitingForInvoiceMatches` runs unconditionally post-JE under flag=true. Escalate with the invoice ID + line ID. |

---

## 6. Enablement procedure (pilot company)

Step 1. **Confirm the five gates from §1.**
- **Catalog readiness** (gate 1 in §1): every stock item has both
  `InventoryAccountID` and `COGSAccountID` set. Item-level, not
  company-level. Spot-check the catalog with a list query if the
  company has more than a handful of stock items.
- **No company-level accounts to wire** (gate 2 in §1): nothing to
  do here. Phase I.B has no GR/IR / PPV analog. Any ticket asking
  to wire such an account is itself an escalation.
- Operator training done.
- Customer understands this is the Shipment-first model.
- Historical context understood (no retroactive migration of
  pre-flip Invoices).

Step 2. **No company-level wiring step.**

Unlike Phase H (which wires GR/IR + PPV accounts before flipping
the rail), Phase I.B goes directly from catalog check to rail flip.
Confirm there is no `ChangeCompanyShipment*Account` call in
services (there is not) and skip to Step 3.

Step 3. **Flip the capability rail.**
- `ChangeCompanyShipmentRequired(companyID, true, actor)`.
- Verify `audit_logs` captured it with action
  `company.shipment_required.enabled`.

Step 4. **Smoke-test with one ship + invoice cycle.**
- Post a single low-risk Shipment. Confirm:
  - Inventory movement row with `source_type='shipment'` and
    `quantity_delta` equal to the negative of the shipped qty.
  - Journal entry with `Dr COGS / Cr Inventory` in equal amounts
    (= peeled `CostOfIssueBase` from `IssueStock`).
  - Shipment row linked to the JE via `journal_entry_id`.
  - One `waiting_for_invoice_items` row per stock line, all
    `status='open'`.
- Post the matching Invoice with
  `invoice_lines.shipment_line_id` set. Confirm:
  - No COGS line and no `Cr Inventory` line in the Invoice JE.
  - No inventory movement sourced from this Invoice.
  - Journal entry with `Dr AR / Cr Revenue` (+ `Cr Tax` if taxable).
  - Invoice row linked to its own JE.
  - `waiting_for_invoice_items` row for the matched ShipmentLine
    now `status='closed'` with `resolved_invoice_id` +
    `resolved_invoice_line_id` + `resolved_at` populated.

Step 5. **Monitor for two weeks.**
- Daily: eyeball the open WFI queue depth. Confirm it grows with
  each Shipment and shrinks with each matched Invoice, consistent
  with the customer's billing cadence.
- Daily: eyeball the COGS account balance. Small growth per
  Shipment is expected; unexpected jumps deserve a look at which
  Shipments posted.
- Daily: eyeball the Revenue account balance. It should move only
  on Invoice posts.
- Weekly: pull the ledger reports for COGS and Revenue. Spot-check
  a handful of entries against the source documents. Confirm the
  WFI queue state matches the shipped-but-unbilled population
  that customer finance recognises.

Step 6. **Decide on expanding the pilot.**
- If week-2 checks are clean, consider enabling one or two more
  companies with similar sell-side patterns.
- If anything on the §5 triage table turns out to need escalation,
  pause further enablements until it's resolved.

---

## 7. Disablement procedure

A customer who enabled `shipment_required=true` may want to turn it
off. This is supported, with some care.

Step 1. **Understand the implication.**
- Shipments and Invoices posted while the flag was ON keep their
  I.3 / I.4 / I.5 journal shapes forever. Disabling does not
  rewrite history.
- Future Invoices (posted after disable) revert to legacy
  behaviour — Invoice-forms-COGS, `Dr AR / Cr Revenue / Dr COGS /
  Cr Inventory`.
- Any in-flight Shipments that haven't been invoiced yet still
  work fine, but their `waiting_for_invoice` rows cannot be closed
  through the legacy Invoice path (the flag-off Invoice rejects
  `shipment_line_id`). Options:
  - Post the missing Invoices with `shipment_line_id` set
    **before** flipping the rail off — preserves the clean match
    chain.
  - Or accept the open WFI rows as a permanent record and let them
    sit (they are operational, not accounting — no GL impact).

Step 2. **Flip the rail back.**
- `ChangeCompanyShipmentRequired(companyID, false, actor)`.
- Audit row written with action `company.shipment_required.disabled`.

Step 3. **No account linkage to clear.**
- Phase I.B has no company-level account analog to Phase H's
  GR/IR / PPV. The only state to unwind is the rail itself.

Step 4. **`waiting_for_invoice` closeout is operational, not
engineering.**

Disablement is allowed regardless of open WFI row count. A
non-empty queue at disable time is, on its own, **not a bug** — it
is the expected state of any company with shipments awaiting
invoices. The disable itself must not be blocked on this.

Three layers to walk, in order:

1. **Disable is permitted.** `ChangeCompanyShipmentRequired(false)`
   succeeds regardless of WFI state. Do not gate it on the queue.

2. **If the customer wants a clean closeout**, reconcile the queue
   operationally before disable:
   - Post the missing Invoices against the open Shipments **with
     `shipment_line_id` set** — preferred, preserves the Phase I.B
     audit trail.
   - Or accept the open rows as permanent operational records.
     They carry no GL impact; leaving them open only means the
     dashboard will show historical rows.
   - Document the reason on the pilot ticket so the period-end
     review has context.

3. **Escalate to engineering only when:**
   - A WFI row is in an impossible state (e.g. `status='closed'`
     but `resolved_invoice_id IS NULL`).
   - The JE picture disagrees with the queue (Shipment has a
     posted JE with `source_type='shipment'` but no matching WFI
     row).
   - The customer is asking for retroactive history rewrite
     (pre-flip Invoices converted into synthetic Shipments) —
     this is a product decision, not a support ticket.

A non-empty WFI queue by itself does not qualify for escalation.
The triage table in §5 and the escalation list in §10 both treat
open WFI rows as by-design; this step keeps §7 consistent with
them.

---

## 8. Known limitations reference card

Print-ready list for quick CS reference.

**Supported right now:**
- ✅ Shipment as a first-class document (create, post, void)
- ✅ Invoice-to-Shipment line-to-line matching via
  `invoice_lines.shipment_line_id`
- ✅ `waiting_for_invoice` queue creation on Shipment post
- ✅ `waiting_for_invoice` queue closure on matching Invoice post
  (status='closed' + resolution identity populated)
- ✅ `waiting_for_invoice` queue void on Shipment void (all rows
  tied to the Shipment, including already-closed ones)
- ✅ `waiting_for_invoice` queue reopen on Invoice void (only rows
  this Invoice had closed; rows voided by an intervening Shipment
  void stay voided)
- ✅ Void symmetry: voiding a posted Shipment reverses its COGS
  JE + inventory movements + WFI rows
- ✅ Cross-tenant ShipmentLine reference rejected loud
- ✅ Double-match (second Invoice for same ShipmentLine) rejected loud
- ✅ `shipment_line_id` under flag=off rejected loud (no silent drift)
- ✅ Disable `shipment_required=true` back to `false` without
  forcing a data migration
- ✅ `shipment_required=false` byte-identical to Phase G + Phase H
  legacy invoice behavior

**Unsupported right now** (by design, deferred to later scope
triggers inside Phase I or to follow-on phases):
- ❌ UI surfaces for `shipment_required` / `shipment_line_id` /
  Shipment document — admin-tool or API-only in Phase I.B
- ❌ `waiting_for_invoice` aging / dashboard report
- ❌ Partial invoicing of a ShipmentLine (one Ship line billed
  across multiple Invoice lines). Current I.B is 1:1 atomic
  closure; partial split needs a dedicated slice that reshapes
  `qty_pending` semantics.
- ❌ Multiple Shipments bundled onto one Invoice line (one-pointer
  rule is hard; operator must split the Invoice line if goods
  came from multiple Shipments).
- ❌ Tracked (lot / serial) outbound via the Shipment path. I.3
  ships untracked issue; tracked items still fail loud at
  `inventory.validateOutboundTracking`. The tracking-selection
  slice is a separate scope trigger within Phase I.
- ❌ Phase G.2's `ErrTrackedItemNotSupportedByInvoice` guard bypass
  on flag=true. Still active under flag=true because tracked
  outbound on Shipment isn't ready yet. Tracked customers must
  stay on flag=false until the tracking slice ships.
- ❌ Reverse pointer on Shipment side (lookup: "which Invoice
  closed this Shipment?" requires querying `invoice_lines WHERE
  shipment_line_id = ?` OR `waiting_for_invoice_items.resolved_invoice_id`).
- ❌ Per-customer or per-book routing for anything in Phase I.B —
  single per-company rail.
- ❌ Historical Invoice backfill into synthetic Shipments —
  pre-flip history stays as-is.
- ❌ Automatic `shipment_required` flipping for new companies —
  every flip is a deliberate admin action.
- ❌ Revenue-at-shipment / contract-asset (SNI) accrual. I.B books
  revenue at Invoice time only. A future scope trigger may
  introduce ASC 606-style control-transfer accrual; decision
  belongs to a later, explicit slice.
- ❌ Sales price variance account. I.B has no variance surface
  between cost and revenue. Future post-shipment price adjustment
  ships as its own scope trigger.
- ❌ Customer-return workflow (return-receive → inspect →
  disposition → return-to-vendor). Scheduled as Phase I.6, not
  blocking the I.5 close.

**Policy defaults locked** (will not change without an explicit
product decision):
- `shipment_required` default = FALSE on every new company
- Single `waiting_for_invoice` queue per company (no partitioning
  by warehouse, customer, or book)
- No company-level GR/IR / PPV / SNI / contract-asset accounts in
  I.B. Catalog-level `InventoryAccountID` + `COGSAccountID` are
  the only per-item account wirings.
- 1:1 atomic Invoice ↔ Shipment matching (no partial closure)
- Invoice qty is not enforced against ShipmentLine qty by I.5 code
  today; the WFI row closes atomically regardless of Invoice qty.
  Strict-qty enforcement is reserved for a dedicated
  partial-matching slice.

---

## 9. What future scope triggers may add

Do not commit specific dates. Language for customer expectations:

> "Sell-side tracked items, partial invoicing, and customer returns
> are planned as follow-on scope triggers within Phase I. Until
> those ship, tracked sales must stay on the legacy flag=false
> Invoice path and each shipment line is billed in one Invoice line."

High-level follow-on scope (internal, not customer promise):
- **Tracking selection on ShipmentLine** — lot / expiry / serial
  picks routed through inventory's `validateOutboundTracking` +
  Phase G.2 guard bypass under flag=true.
- **Partial invoicing** — `waiting_for_invoice_items.qty_pending`
  becomes decrementable; WFI row closes when qty_pending reaches
  zero. Requires a non-trivial rewrite of the closure semantics.
- **Aging report** — CS-facing dashboard for open WFI rows grouped
  by customer + ship-date bucket.
- **Phase I.6 — customer returns** — return-receive → inspect →
  disposition, mirroring the structural shape of Shipment + WFI
  for returns.
- **Revenue-at-shipment / SNI bridge** — ASC 606-style
  control-transfer accrual with a contract-asset account.
- **Sales price variance account** — post-shipment price
  adjustment absorbed to a P&L account.

---

## 10. Escalation

Loop in engineering if:

- A customer reports an open `waiting_for_invoice` row for a
  Shipment that `status='voided'`. `VoidShipment` voids all WFI
  rows attached to the Shipment; a real reproduction = regressed
  void path.
- An Invoice post left inventory movements with
  `source_type='invoice'` under `shipment_required=true`. I.4's
  guard should have prevented this; reproduction = regressed gate.
- A Shipment post left no inventory movement but flipped status to
  posted under `shipment_required=true` with stock lines. I.3's
  guard should have prevented this.
- An Invoice post under flag=true where a line carried
  `shipment_line_id` but the target WFI row stayed `open` after
  the Invoice committed. `closeWaitingForInvoiceMatches` should
  have closed it; a stale row = bug.
- `audit_logs` has gaps in the expected capability-flip actions
  for this company (we should see at least the
  `company.shipment_required.enabled` row).
- A customer insists on retroactive history migration (converting
  pre-flip Invoices into synthetic Shipments). That's a product
  decision, not a support ticket.

Do not escalate for:
- `waiting_for_invoice` queue has open rows — see §4.
- Invoice under flag=true doesn't show a COGS line — see §2.
- Two Invoices tried to close the same WFI row and one got
  rejected — the serialisation is correct; the race loser should
  not be paired with this WFI row in the first place.
- A customer asking "when can my other companies enable this?" —
  answer from §1 (gates, not dates).
- A customer wanting the UI for matching — "not in Phase I.B;
  admin / API path is the current workflow."
- A customer asking for a Phase I "clearing account" — Phase I.B
  does not have one by design; point them at §2's "Variance
  accounts (deliberately absent)" paragraph.

---

## 11. Change log

| Date | Change |
|---|---|
| 2026-04-20 | Initial draft after I.5 merge (`fd3398f`) + staging verification. Supersedes the sell-side forward-looking text in `PHASE_H_RUNBOOK.md` §9. Applies to current Phase I scope selection **Phase I.B** only. |
| (future) | Tracking-selection slice ships → update §8 tracked-outbound row; add Phase G.2 guard-bypass note. |
| (future) | Partial-invoicing slice ships → rewrite §3 matching scenarios; reshape §5 triage row (a). |
| (future) | Revenue-at-shipment scope trigger ships → rewrite §2 "Variance accounts (deliberately absent)" + §8 policy row. |

---

**One-line summary for CS dashboards:**

*Phase I `shipment_required=true` (scope I.B): Shipment is inventory
truth + COGS; Invoice is AR + Revenue; `waiting_for_invoice` is the
operational queue between them (not a GL account). 1:1 atomic
match. Still opt-in per company, not auto-rolled out.
`shipment_required=false` is byte-identical to Phase G + Phase H
legacy invoice behavior.*
