# Phase I Pilot Enablement — Execution Playbook

**Document type:** Execution supplement to [PHASE_I_RUNBOOK.md](PHASE_I_RUNBOOK.md).
**Status:** Active after I.5 merge (`fd3398f`); active through the
first batch of pilot enablements under the current Phase I scope
selection **Phase I.B**.
**Applies to:** One-at-a-time `shipment_required=true` enablements
on selected pilot companies.
**Semantic reference:** Every definition of Shipment / Invoice /
`waiting_for_invoice` / matching / by-design vs bug in this document
defers to `PHASE_I_RUNBOOK.md`. If this doc and the runbook
disagree, the runbook wins.
**Independence from Phase H:** `receipt_required` and
`shipment_required` are independent capability rails. A company may
enable one, both, or neither. This playbook governs **only** the
sell-side enablement. A company's Phase H pilot state is not a
precondition for starting a Phase I pilot, and vice versa — but see
§2 criterion 10 on avoiding both pilots simultaneously on the same
company.

---

## 1. Purpose / Audience / Non-goals

### Purpose

Execute a **controlled, reversible pilot** of the Shipment-first
outbound model on a single low-risk company, verify the I.3 / I.4 /
I.5 contract holds in that company's real posting traffic, and
produce the evidence needed to decide whether to expand or freeze
further rollout.

### Audience

| Role | Responsibility |
|---|---|
| **Ops** | Executes admin actions; runs checks; escalates on triggered conditions. |
| **Engineering** | Receives escalations; investigates lock / invariant regressions; does not drive rollout pace. |
| **Accounting** | Owns period-end reconciliation of the shipped-but-unbilled GL gap against the `waiting_for_invoice` queue. |
| **CS** | Watches the candidate customer's reported experience; uses `PHASE_I_RUNBOOK.md` §5 triage for customer-facing answers. |

### Non-goals

- This is **not** a general rollout playbook. Second and subsequent
  enablements reuse this document but §7 gates each one.
- This is **not** customer-facing. Customer language belongs in the
  runbook.
- This document does **not** define semantics. Any "what does
  X mean?" question resolves via the runbook reference.
- This document does **not** describe UI. Phase I.B current workflow
  = admin/API path. A UI layer ships in a later scope trigger; the
  pilot deliberately uses the API to prove the semantic contract.
- This document does **not** touch Phase H. If the candidate
  company is also on Phase H, use `PHASE_H_PILOT_ENABLEMENT.md`
  separately; see §2 criterion 10.

---

## 2. Pilot candidate selection criteria

A company is a **valid Phase I pilot candidate** if it clears every
row below. Any red row disqualifies.

| # | Criterion | Why | Verification query / check | Owner |
|---|---|---|---|---|
| 1 | **Trailing 30-day** Invoice volume ≤ 50 | Smaller audit surface; CS can actually read every Invoice | `SELECT COUNT(*) FROM invoices WHERE company_id=$CID AND invoice_date >= NOW() - INTERVAL '30 days' AND status != 'draft'` | Ops |
| 2 | **Trailing 30-day** stock-item Invoice line volume ≤ 200 | Matching volume manageable by a human operator during pilot | `SELECT COUNT(*) FROM invoice_lines il JOIN invoices i ON i.id=il.invoice_id JOIN product_services p ON p.id=il.product_service_id WHERE i.company_id=$CID AND p.is_stock_item=true AND i.invoice_date >= NOW() - INTERVAL '30 days'` | Ops |
| 3 | Has at least one stock-item `ProductService` | No stock items = no Shipment-first outbound traffic to validate; such a company is not a meaningful pilot candidate regardless of other criteria | `SELECT COUNT(*) FROM product_services WHERE company_id=$CID AND is_stock_item=true` returns **> 0** | Ops |
| 4 | Tracked-item share of stock catalog = **0%** | Phase I.B does not yet support tracked outbound on the Shipment path; a tracked item cannot ship under flag=true (fails at `inventory.validateOutboundTracking`). A candidate with any tracked items in its sell-side catalog cannot run the Shipment-first flow for those items. | `SELECT SUM(CASE WHEN tracking_mode != 'none' THEN 1 ELSE 0 END) FROM product_services WHERE company_id=$CID AND is_stock_item=true AND can_be_sold=true` returns **= 0**. Criterion 3's "stock items exist" gate is independent. | Ops |
| 5 | Predictable ship-then-invoice cadence | Operator can reliably link each Invoice line to its ShipmentLine; reduces unmatched / ambiguous posts | Sample last 20 stock Invoices: at least 15 correspond to a prior fulfilled delivery recorded somewhere in the customer's workflow (packing slip, order-management system, etc.) | CS + Ops |
| 6 | Cooperative named operator on the customer side | Someone accountable for posting Shipments first and setting `shipment_line_id` on Invoice lines | Written confirmation from AM + operator's direct contact on file | CS |
| 7 | No known manual revenue / COGS adjustments in the current / open period | Unresolved manual AR / Revenue / COGS JEs can make the pilot's shipped-but-unbilled reconciliation ambiguous — were they pre-pilot or pilot-induced? Start from a clean, accountable state. | **Ledger review** by Accounting on the company's AR, Revenue, Inventory Asset, and COGS accounts for the open period; **plus** explicit Accounting lead confirmation in the pilot ticket that no known manual adjustments are outstanding. SQL-only naming-pattern filters (e.g. `journal_no LIKE 'ADJ-%'`) are **not** sufficient. | Accounting |
| 8 | No prior `shipment_required` enablement attempt | First-time enablement on the company | `SELECT COUNT(*) FROM audit_logs WHERE company_id=$CID AND action IN ('company.shipment_required.enabled','company.shipment_required.disabled')` returns 0 | Ops |
| 9 | Not cutting a fiscal period in the next 30 days | Avoid straddling a close with an in-flight pilot | Per customer's fiscal calendar | Accounting + CS |
| 10 | Not concurrently starting a Phase H pilot on the same company | A single company in both pilots simultaneously produces ambiguous observation windows — any GL deviation can't be cleanly attributed to inbound or outbound. Phase H and Phase I pilots can run on **different** companies in parallel, but not on the same one. | Confirm no active entry in any Phase H pilot ticket for `$CID`. If the company is already in a Phase H pilot, finish that one first (pass, fail, or rollback per `PHASE_H_PILOT_ENABLEMENT.md` §7) before opening a Phase I pilot on it. | Ops + CS |

**Disqualifying:** any row returning a red result. Do not negotiate
around any of the ten.

**Escalation if a strong candidate partially fails one criterion:**
engineering review **required** before proceeding — the pilot's
purpose is to validate the contract cleanly, not to stress-test
under adverse conditions.

---

## 3. Pre-flight checklist

Executed in the hour before §4's enablement workflow. Maps to
`PHASE_I_RUNBOOK.md` §1 (five gates) and §6 (enablement steps).

### Gate 1 — Catalog readiness (item-level, BOTH sides)

**Check A:** Every stock item that `can_be_sold` has
`product_services.inventory_account_id` set.

```sql
SELECT id, name, sku
FROM product_services
WHERE company_id = $CID
  AND is_stock_item = true
  AND can_be_sold = true
  AND inventory_account_id IS NULL;
```

- **Expected result:** 0 rows.
- **Stop condition:** Any row returned → fix the catalog before
  continuing. Owner: **Ops** (coordinates with the customer).
- **No workaround.** `PostShipment` fails loud with
  `ErrShipmentInventoryAccountMissing` on the first stock line
  otherwise.

**Check B:** Every stock item that `can_be_sold` has
`product_services.cogs_account_id` set.

```sql
SELECT id, name, sku
FROM product_services
WHERE company_id = $CID
  AND is_stock_item = true
  AND can_be_sold = true
  AND cogs_account_id IS NULL;
```

- **Expected result:** 0 rows.
- **Stop condition:** Any row returned → fix the catalog before
  continuing. Owner: **Ops**.
- **No workaround.** `PostShipment` fails loud with
  `ErrShipmentCOGSAccountMissing`. This is a **Phase I addition**
  over Phase H (H only required `inventory_account_id`; I
  requires both).

**Check C:** No tracked items in the sell-side catalog.

```sql
SELECT id, name, sku, tracking_mode
FROM product_services
WHERE company_id = $CID
  AND is_stock_item = true
  AND can_be_sold = true
  AND tracking_mode != 'none';
```

- **Expected result:** 0 rows.
- **Stop condition:** Any row returned → this company cannot
  currently pilot Phase I.B (see §2 criterion 4). Owner: **Ops +
  CS** (move to a different candidate, or wait for the
  tracking-selection slice).

### Gate 2 — No company-level account wiring

Phase I.B has **no** GR/IR / PPV analog. There is no
`ChangeCompanyShipment*Account` setter in the service layer, and
no company column to point at such an account. This gate is
informational: if anyone in the enablement chain is looking for
"which accounts to wire for Phase I", the answer is **none**.

**Check (optional, defensive):** Confirm no ticket or admin note
is waiting to wire a hypothetical Phase I clearing account. Any
such ticket is itself an escalation — close it and link this
section.

### Gate 3 — Current capability rail is OFF

**Check:** `shipment_required` currently equals `false` for the
target company.

```sql
SELECT shipment_required
FROM companies
WHERE id = $CID;
```

- **Hard expectation:** `shipment_required = false`.
- **Stop condition:** `shipment_required` already `true` → halt
  immediately. This contradicts criterion 8 in §2 and means the
  company is not a first-time enablement. Owner: **Ops +
  Engineering** to investigate `audit_logs`.

Unlike Phase H's Gate 3 (which also soft-checks pre-existing
GR/IR + PPV account linkage), Phase I has no account linkage to
check. The rail itself is the entire company-level surface.

### Gate 4 — `waiting_for_invoice` queue baseline is empty for this company

**Check:** No rows in `waiting_for_invoice_items` for this company.

```sql
SELECT COUNT(*) AS open_wfi_count,
       SUM(CASE WHEN status='open' THEN 1 ELSE 0 END) AS open_rows,
       SUM(CASE WHEN status='closed' THEN 1 ELSE 0 END) AS closed_rows,
       SUM(CASE WHEN status='voided' THEN 1 ELSE 0 END) AS voided_rows
FROM waiting_for_invoice_items
WHERE company_id = $CID;
```

- **Expected:** `open_wfi_count = 0` (total).
- **Stop condition:**
  - `open_rows > 0` → queue has open items. A first-time pilot
    cannot have pre-existing open rows; this indicates a prior
    undocumented flag enablement (contradicting criterion 8).
    Owner: **Ops + Engineering**.
  - `closed_rows > 0` or `voided_rows > 0` → queue has historical
    resolution rows. Also inconsistent with a first-time pilot.
    Escalate same as above.
- **Rationale:** pilot starts against a known-empty WFI so
  subsequent queue population is attributable to pilot traffic.

### Gate 5a — Schema patch IN.7 is applied (CRITICAL — blocks post)

Phase I.3 wired `PostShipment` under `shipment_required=true` to
write the JE id back via `shipments.journal_entry_id`, but
`migrations/076_shipments_and_lines.sql` never created the column.
**migration 084** (IN.7) closes this gap. Verify:

```sql
SELECT EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_name = 'shipments' AND column_name = 'journal_entry_id'
) AS has_journal_entry_id;
-- MUST return true before enabling shipment_required.
```

If this returns `false`, the first `PostShipment` under the flipped
rail will fail with `column "journal_entry_id" does not exist` and
block the pilot. Apply migration 084 + all intermediate migrations
before flipping the rail.

Dev / test DBs that were previously populated via GORM AutoMigrate
(tests, local dev with fresh install) already have the column —
this gate primarily protects production DBs that took the
SQL-only migration path.

### Gate 5 — Operator & CS readiness sign-off

**Check:** Written or ticketed confirmation from:

- Customer-side operator: "I will create a Shipment before posting
  the matching Invoice, and I will set
  `invoice_lines.shipment_line_id` when linking."
- CS / AM: "I have the customer's two-week observation commitment."
- Accounting lead: "I will handle period-end reconciliation of
  shipped-but-unbilled balance against the `waiting_for_invoice`
  queue."

- **Expected:** three explicit confirmations captured in the
  pilot ticket before proceeding.
- **Stop condition:** Any missing confirmation → wait.

---

## 4. Enablement workflow

Sequence is strict. Each step has an immediate audit verification.
Any failed audit = stop, investigate, **do not** proceed to the
next step.

**Executor:** Ops, running against production DB under the pilot
ticket. **Reviewer:** Engineering, double-checks each audit row.

### Step 1 — No-op (no GR/IR wiring)

Phase H's Step 1 (`ChangeCompanyGRIRClearingAccount`) has no Phase
I equivalent. Skip directly to Step 2.

### Step 2 — No-op (no PPV wiring)

Phase H's Step 2 (`ChangeCompanyPPVAccount`) has no Phase I
equivalent. Skip directly to Step 3.

### Step 3 — Flip the capability rail (Owner: Ops, Reviewer: Engineering)

**Action:**

```go
services.ChangeCompanyShipmentRequired(db,
    services.ChangeCompanyShipmentRequiredInput{
        CompanyID: $CID,
        Required:  true,
        Actor:     "ops@taxdeep/pilot-$TICKET",
    })
```

**Expected audit row:**

```sql
SELECT action, actor, details_json
FROM audit_logs
WHERE company_id = $CID
  AND action = 'company.shipment_required.enabled'
ORDER BY id DESC LIMIT 1;
```

- `action = 'company.shipment_required.enabled'`
- `actor` matches the executor string
- `details_json.before.shipment_required = false`
- `details_json.after.shipment_required = true`

**Stop condition if:** missing audit, wrong before-state, or rail
did not actually flip. Call
`ChangeCompanyShipmentRequired(…, Required=false,…)` to back out
and halt pilot.

### Step 4 — End-to-end smoke cycle (Owner: Ops + Customer operator)

Runs against a **single, low-value, non-tracked** stock item.
Purpose: prove the full Shipment → match → Invoice chain produces
the expected artefacts. Do **not** allow ordinary customer posting
under the pilot company until this smoke cycle passes — Step 3
already touched the pilot company's production database, but only
admin configuration; the smoke cycle is the first end-to-end
Shipment+Invoice traffic on the flipped rail and must clear before
the customer resumes normal posting.

Pre-condition: the item must already have at least the qty to be
shipped on hand (inventory balance). Pilot does not perform the
receiving half — if the item is out of stock, receive first via
the normal Phase G / Phase H path, then run this smoke.

**Sub-step 4a — Post a Shipment.** Customer operator creates a
Shipment for (say) 5 units of the chosen stock item in a chosen
warehouse; then `services.PostShipment(…)`.

**Verify:**

```sql
-- (i) Shipment row status
SELECT status, posted_at, journal_entry_id
FROM shipments WHERE id = $SHIPMENT_ID;
-- expect: status='posted', posted_at not null, journal_entry_id not null

-- (ii) Inventory movement exists, source_type='shipment'
SELECT id, source_type, source_id, quantity_delta
FROM inventory_movements
WHERE company_id = $CID AND source_type = 'shipment' AND source_id = $SHIPMENT_ID;
-- expect: exactly one row, quantity_delta = -5 (stock leaving)

-- (iii) JE shape: Dr COGS, Cr Inventory (equal amounts; cost authoritative from IssueStock)
SELECT account_id, debit, credit
FROM journal_lines
WHERE journal_entry_id = (SELECT journal_entry_id FROM shipments WHERE id=$SHIPMENT_ID);
-- expect: one debit on the item's cogs_account_id
--         one credit on the item's inventory_account_id
--         debit amount == credit amount == 5 × peeled unit cost

-- (iv) Exactly one open WFI row per stock line
SELECT id, shipment_id, shipment_line_id, product_service_id,
       qty_pending, unit_cost_base, status, ship_date
FROM waiting_for_invoice_items
WHERE company_id = $CID AND shipment_id = $SHIPMENT_ID;
-- expect: exactly one row, status='open', qty_pending=5,
--         unit_cost_base = peeled unit cost from (iii)

-- (v) Audit
SELECT action FROM audit_logs
WHERE entity_type='shipment' AND entity_id=$SHIPMENT_ID
ORDER BY id DESC LIMIT 1;
-- expect: 'shipment.posted'
```

**Stop condition if:** any of (i)-(v) deviates.

**Sub-step 4b — Post a matched Invoice.** Operator creates an
Invoice with one line at 5 units @ (say) $15.00 for the same item,
sets `invoice_lines.shipment_line_id` to the ShipmentLine's ID,
then `services.PostInvoice(…)`.

**Verify:**

```sql
-- (i) Invoice status
SELECT status, journal_entry_id
FROM invoices WHERE id = $INVOICE_ID;
-- expect: status='sent', journal_entry_id not null

-- (ii) NO invoice-sourced inventory movement
SELECT COUNT(*) FROM inventory_movements
WHERE company_id=$CID AND source_type='invoice' AND source_id=$INVOICE_ID;
-- expect: 0

-- (iii) JE shape: Dr AR, Cr Revenue (plus Cr Tax if taxable); NO COGS line, NO Inventory line
SELECT account_id, debit, credit, memo
FROM journal_lines
WHERE journal_entry_id = (SELECT journal_entry_id FROM invoices WHERE id=$INVOICE_ID);
-- expect: Dr on AR account for invoice total
--         Cr on Revenue account for invoice net
--         Cr on Tax account for tax (if any)
--         ZERO lines on the item's cogs_account_id
--         ZERO lines on the item's inventory_account_id

-- (iv) WFI row closed with resolution fields populated
SELECT id, status, resolved_invoice_id, resolved_invoice_line_id, resolved_at
FROM waiting_for_invoice_items
WHERE company_id=$CID AND shipment_id=$SHIPMENT_ID;
-- expect: status='closed'
--         resolved_invoice_id = $INVOICE_ID
--         resolved_invoice_line_id = the invoice line ID
--         resolved_at not null
```

**Stop condition if:** inventory movement appears on the Invoice,
COGS or Inventory lines appear in the Invoice JE, the WFI row did
not transition to closed, or the resolution identity fields are
not populated.

**Sub-step 4c — No `waiting_for_invoice` residual for this
matched pair.**

```sql
SELECT COUNT(*) AS open_rows
FROM waiting_for_invoice_items
WHERE company_id=$CID AND shipment_id=$SHIPMENT_ID AND status='open';
-- expect: 0
```

**Stop condition if:** non-zero. A smoke-cycle matched pair must
leave the queue in a clean closed state. (Note: this is
per-shipment; the company-level queue may have open rows from
other Shipments under normal operation. For the smoke cycle
only, we scope to `$SHIPMENT_ID` to prove the match closed
properly.)

**On smoke pass:** pilot enablement is technically complete.
Observation window (§5) begins.

---

## 5. Observation window

Two calendar weeks from Step 3's audit timestamp. Every finding in
this window categorises into one of three buckets:

| Bucket | Owner to act | Meaning |
|---|---|---|
| **By-design residual** | None (record only) | Normal Phase I.B behaviour per `PHASE_I_RUNBOOK.md` §4 and §5. |
| **Operational reconciliation** | Accounting | WFI queue or GL picture needs human attention (post missing Invoices, document exceptions); not engineering. |
| **Engineering escalation** | Engineering | Invariant violation or unexplained state — stop pilot expansion until resolved. |

### Daily checks (every business day, 09:00 customer-TZ)

**D1 — New Shipments posted.**

```sql
SELECT id, shipment_number, posted_at, journal_entry_id
FROM shipments
WHERE company_id=$CID AND posted_at >= NOW() - INTERVAL '24 hours'
  AND status='posted';
```

Expect: every row has `journal_entry_id` non-null unless the
Shipment had only service lines (runbook §5 service-only
scenario). Record any service-only Shipment — if more than
expected, raise with Ops. Spot-check that each row produced an
inventory_movements row with `source_type='shipment'` and a
WFI row.

**D2 — New Invoices posted + match rate.**

```sql
SELECT i.id, i.invoice_number, i.posted_at, i.journal_entry_id,
       COUNT(il.id) AS line_count,
       SUM(CASE WHEN p.is_stock_item THEN 1 ELSE 0 END) AS stock_line_count,
       SUM(CASE WHEN il.shipment_line_id IS NOT NULL THEN 1 ELSE 0 END) AS matched_lines,
       SUM(CASE WHEN il.shipment_line_id IS NULL AND p.is_stock_item THEN 1 ELSE 0 END) AS unmatched_stock_lines
FROM invoices i
JOIN invoice_lines il ON il.invoice_id = i.id
LEFT JOIN product_services p ON p.id = il.product_service_id
WHERE i.company_id=$CID AND i.posted_at >= NOW() - INTERVAL '24 hours'
  AND i.status != 'draft'
GROUP BY i.id;
```

Routing rules based on explicit thresholds (per day, over the
24-hour window of this query):

Let `U` = sum of `unmatched_stock_lines` across the day's posted
Invoices, and `T` = sum of `stock_line_count` across the same set.

`T` is deliberately **stock-only**, not `line_count`. Service / tax
/ fee / free-form lines have no shipment linkage by definition and
would dilute the ratio if included. `U / T` therefore reads as
"fraction of this day's stock-line invoice traffic that went out
without a Shipment match." A company whose Invoices are mostly
service lines will often have `T` much smaller than `line_count` —
that is the intent: the ratio only measures the stock half.

Guard against `T = 0` (day's Invoices had zero stock lines): the
match-rate check is vacuous for that day. Record `U=0, T=0` and
move on — no operational concern, no engineering concern.

- **By-design (record only, no action):** `U = 0`, OR `U > 0`
  with `U / T ≤ 0.20` on a single day. Unmatched stock lines are
  legal per `PHASE_I_RUNBOOK.md` §3-§4; up to 20% of stock-line
  traffic on any one day is within expected operator variance
  (short-ship via legacy workflow, fee lines misclassified as
  stock items, etc.).
- **Operational concern (Owner: CS + Ops):** **either** of:
  - Single-day: `U / T > 0.20`. CS asks the operator the next
    business day for the specific reason on each unmatched
    stock-line Invoice.
  - Trend: `U / T > 0.10` on each of **three consecutive**
    business days. Schedule an operator re-training session with
    the customer by end of the following week.
- **Not engineering** unless a D5 trigger also fires.

**D3 — `waiting_for_invoice` queue state sanity.**

```sql
-- A: Closed WFI rows for Invoices posted in the last 24h must all
-- have resolved_invoice_id set (not NULL) and a matching invoice line.
SELECT wfi.id AS wfi_id, wfi.status, wfi.resolved_invoice_id,
       wfi.resolved_invoice_line_id, wfi.resolved_at
FROM waiting_for_invoice_items wfi
WHERE wfi.company_id = $CID
  AND wfi.status = 'closed'
  AND wfi.resolved_at >= NOW() - INTERVAL '24 hours'
  AND (wfi.resolved_invoice_id IS NULL OR wfi.resolved_invoice_line_id IS NULL);
-- expect: 0 rows

-- B: Open WFI rows whose parent Shipment is voided.
SELECT wfi.id AS wfi_id, wfi.shipment_id, s.status AS shipment_status
FROM waiting_for_invoice_items wfi
JOIN shipments s ON s.id = wfi.shipment_id
WHERE wfi.company_id = $CID
  AND wfi.status = 'open'
  AND s.status = 'voided';
-- expect: 0 rows (VoidShipment voids every WFI attached to the shipment)
```

If either query returns rows: **engineering escalation** (D5
triggers T2 or T3 below).

**D4 — COGS + Revenue + WFI queue depth snapshot.**

```sql
-- Step 1 — resolve the distinct set of COGS and Revenue account IDs
-- used by stock-item ProductServices in this company. Recorded once
-- at enablement time (or re-resolved if the catalog changed); these
-- IDs feed steps 2 and 3.
--
-- DO NOT join journal_lines directly to product_services by
-- cogs_account_id / revenue_account_id — multiple items may share the
-- same COGS or Revenue account, and the join would multiply a single
-- journal line by the number of items pointing at that account,
-- producing inflated sums. Resolving to a distinct account-ID set
-- first and then filtering ledger / journal rows by that set is the
-- only safe form.

SELECT DISTINCT cogs_account_id FROM product_services
WHERE company_id = $CID AND is_stock_item = true AND cogs_account_id IS NOT NULL;
-- record result as $COGS_ACCOUNT_IDS (comma-separated list for IN clause)

SELECT DISTINCT revenue_account_id FROM product_services
WHERE company_id = $CID AND is_stock_item = true AND revenue_account_id IS NOT NULL;
-- record result as $REVENUE_ACCOUNT_IDS

-- Step 2 — COGS balance across the resolved account set.
SELECT COALESCE(SUM(le.debit_amount) - SUM(le.credit_amount), 0) AS cogs_balance,
       COUNT(*) AS cogs_ledger_line_count
FROM ledger_entries le
WHERE le.company_id = $CID
  AND le.status = 'active'
  AND le.account_id IN ($COGS_ACCOUNT_IDS);

-- Step 3 — Revenue balance across the resolved account set (for
-- cross-reference with Invoice posting cadence; revenue naturally
-- sits on the credit side).
SELECT COALESCE(SUM(le.credit_amount) - SUM(le.debit_amount), 0) AS revenue_balance,
       COUNT(*) AS revenue_ledger_line_count
FROM ledger_entries le
WHERE le.company_id = $CID
  AND le.status = 'active'
  AND le.account_id IN ($REVENUE_ACCOUNT_IDS);

-- Step 4 — Open WFI queue depth (unchanged; no account join involved).
SELECT COUNT(*) AS open_wfi_rows,
       COUNT(DISTINCT shipment_id) AS distinct_shipments,
       SUM(qty_pending) AS total_pending_qty,
       SUM(qty_pending * unit_cost_base) AS total_pending_cost_base
FROM waiting_for_invoice_items
WHERE company_id = $CID AND status = 'open';
```

Record. A growing queue is **expected** during normal operation —
the Invoice cadence lags the Shipment cadence. Watch the
**trend**, not the absolute.

Note on account-set resolution: resolve $COGS_ACCOUNT_IDS and
$REVENUE_ACCOUNT_IDS **once** at enablement time and reuse each
day. Only re-resolve if the catalog adds / removes a stock item or
a stock item's COGS / Revenue account assignment changes during
the pilot. A mid-pilot catalog change is itself worth recording in
the pilot ticket — comparing pre-change vs post-change trend
otherwise becomes noisy.

**D5 — Engineering-escalation triggers for today.**

Escalate **immediately** (Ops pages Engineering) if ANY of:

- **T1:** Inventory movement with `source_type='invoice'` dated
  after Step 3's audit timestamp → Invoice-forms-COGS regression
  (I.4 guard failed).
  ```sql
  SELECT id, source_id, created_at FROM inventory_movements
  WHERE company_id=$CID AND source_type='invoice'
    AND created_at > $STEP3_TIMESTAMP;
  ```

- **T2:** Open WFI row attached to a voided Shipment (the
  D3-query-B case above).

- **T3:** Closed WFI row with NULL resolution identity fields (the
  D3-query-A case above).

- **T4:** Shipment posted with stock lines under
  `shipment_required=true` but no `journal_entry_id`.
  ```sql
  SELECT s.id FROM shipments s
  WHERE s.company_id=$CID AND s.status='posted'
    AND s.posted_at > $STEP3_TIMESTAMP
    AND s.journal_entry_id IS NULL
    AND EXISTS (
      SELECT 1 FROM shipment_lines sl
      JOIN product_services p ON p.id = sl.product_service_id
      WHERE sl.shipment_id = s.id AND p.is_stock_item = true
    );
  ```

- **T5:** Invoice posted under `shipment_required=true` with a
  line carrying `shipment_line_id`, but the corresponding WFI row
  did not transition to `closed`.
  ```sql
  SELECT il.id AS invoice_line_id, il.invoice_id, il.shipment_line_id,
         wfi.id AS wfi_id, wfi.status
  FROM invoice_lines il
  JOIN invoices i ON i.id = il.invoice_id
  LEFT JOIN waiting_for_invoice_items wfi
    ON wfi.shipment_line_id = il.shipment_line_id
   AND wfi.company_id = i.company_id
  WHERE i.company_id = $CID
    AND i.status != 'draft'
    AND i.posted_at > $STEP3_TIMESTAMP
    AND il.shipment_line_id IS NOT NULL
    AND (wfi.id IS NULL OR wfi.status != 'closed'
         OR wfi.resolved_invoice_id != i.id);
  ```

- **T6:** Invoice posted under `shipment_required=true` lacks
  `journal_entry_id` (post succeeded but JE missing).
  `services.PostInvoice` always creates a JE for a non-draft
  Invoice; a non-draft row with NULL JE link is a data-integrity
  regression.
  ```sql
  SELECT id, invoice_number, posted_at
  FROM invoices
  WHERE company_id = $CID
    AND status != 'draft'
    AND posted_at > $STEP3_TIMESTAMP
    AND journal_entry_id IS NULL;
  ```

- **T7:** Any T1-T6 reproduces after a clean re-post attempt
  (void the offending document, recreate, repost; if the T1-T6
  query still returns rows, the regression is deterministic, not
  a transient fluke).

If no T1-T7 hits, the day's check is complete. Record the
snapshot and move on.

### Weekly checks (every Friday, end of business)

**W1 — Reconcile `waiting_for_invoice` queue to source documents.**

**Volume-based review scope (pick one, do not do both):**

- **Full review** applies when the week's stock-line Shipment +
  Invoice volume ≤ 50 combined: for every posted Shipment and
  Invoice in the last 7 days with at least one stock line,
  confirm:
  - Each Shipment produced exactly one WFI row per stock line,
    all rows `status in ('open','closed','voided')` consistent
    with the Shipment + matching Invoice state.
  - Each Invoice with `shipment_line_id` set on any line closed
    the corresponding WFI row and recorded resolution identity.
- **Sampled review** applies when the week's volume > 50: take a
  random sample of **max(10, ceil(0.10 × weekly stock line
  count))** stock-touching Shipments and Invoices from the last
  7 days. Run the same per-document reconciliation on the
  sampled set only. Record the sample size in the pilot ticket.

In either mode, the outcomes classify as:

- **By design:** open WFI rows at week-end are non-zero on the
  company level (runbook §4). This by itself is not a W1
  finding.
- **Operational (Owner: Accounting + CS):** a specific Shipment
  shows an open WFI row with no follow-up Invoice for ≥ 14
  calendar days → flag to customer as "Invoice expected, please
  confirm."
- **Engineering (Owner: Engineering):** any single document in
  the reviewed / sampled set where the Shipment → WFI → Invoice
  → WFI resolution chain is broken (missing WFI row, WFI state
  inconsistent with the Shipment + Invoice state, resolution
  identity pointing at wrong Invoice, etc.). File a bug ticket
  with the specific Shipment / Invoice IDs and halt pilot
  expansion until resolved.

**W2 — Shipped-but-unbilled GL gap trend.**

The GL analog of the WFI queue is: COGS debited without matching
Revenue credited for the shipped items. This gap widens on
Shipment post and narrows on Invoice post. Track weekly.

Query uses the $COGS_ACCOUNT_IDS and $REVENUE_ACCOUNT_IDS sets
resolved in D4 Step 1. Reusing the pre-resolved account set avoids
the account ↔ product_services fan-out: if a single journal line
sits on a COGS account shared by 3 stock items, joining to
product_services by `cogs_account_id = jl.account_id` would return
3 rows and triple-count the line. Filter by the distinct account
set instead.

```sql
-- COGS booked over the week from Shipments under flag=on.
-- $COGS_ACCOUNT_IDS resolved once from D4 Step 1 — do NOT re-derive
-- via JOIN on product_services here.
SELECT DATE_TRUNC('week', je.entry_date) AS week,
       SUM(jl.debit) AS weekly_cogs_debit
FROM journal_lines jl
JOIN journal_entries je ON je.id = jl.journal_entry_id
WHERE jl.company_id = $CID
  AND jl.account_id IN ($COGS_ACCOUNT_IDS)
  AND je.source_type = 'shipment'
  AND je.entry_date >= NOW() - INTERVAL '60 days'
GROUP BY 1 ORDER BY 1;

-- Revenue booked over the week from Invoices.
SELECT DATE_TRUNC('week', je.entry_date) AS week,
       SUM(jl.credit) AS weekly_revenue_credit
FROM journal_lines jl
JOIN journal_entries je ON je.id = jl.journal_entry_id
WHERE jl.company_id = $CID
  AND jl.account_id IN ($REVENUE_ACCOUNT_IDS)
  AND je.source_type = 'invoice'
  AND je.entry_date >= NOW() - INTERVAL '60 days'
GROUP BY 1 ORDER BY 1;
```

Compare. The weekly COGS and Revenue cadences should roughly
track the customer's ship-vs-invoice cadence. Sudden divergence
(COGS way ahead of Revenue for multiple weeks running) may
indicate Invoices are not being issued — operational, not
engineering. Sudden inversion (Revenue way ahead of COGS) under
flag=true would be a bug: that implies Invoice formed COGS too,
contradicting I.4.

Caveat on non-stock Revenue contamination: $REVENUE_ACCOUNT_IDS is
resolved from stock-item ProductServices only (D4 Step 1). If the
company has non-stock ProductServices (services, fees) whose
`revenue_account_id` happens to be the same account, those Invoice
posts will also count toward `weekly_revenue_credit`. This is
acceptable for the gap-trend signal — overall Revenue cadence is
what matters — but explicitly out of scope for "shipped-line
revenue only." If a future slice needs the pure stock-line revenue
cadence, it can filter `journal_lines` by joining to
`invoice_lines` on a per-line basis with `shipment_line_id IS NOT
NULL`, at the cost of a heavier query.

**W3 — Pilot fitness check against §2 criteria.**

Re-run §2 criteria 1 (Invoice volume), 2 (stock-line volume), and
4 (tracked-item share) against the trailing-30-day window as of
the check day. Explicit pause triggers (pilot expansion halts;
existing pilot continues under tighter watch, not rolled back
automatically):

- Criterion 1 current reading > 75 (i.e. exceeded its 50 ceiling
  by ≥50%), OR
- Criterion 2 current reading > 300 (exceeded 200 ceiling by
  ≥50%), OR
- Criterion 4 current reading > 0 (any tracked sell-side items
  have appeared in the catalog since enablement). Tracked items
  in the sell-side catalog are a **hard** fail for Phase I.B
  regardless of volume — they cannot ship under flag=true.

Any one trigger = pause further rollout decisions until week-2
re-check. Criterion-4 trigger also requires an immediate
customer conversation: the newly-tracked items cannot be sold
under the active pilot until either the tracking-selection slice
ships or the items are routed through the legacy flag-off path
(which requires taking the customer off the pilot). Two or more
triggers in the same check = pilot is no longer representative
of "low-risk"; run §6 rollback on this company and pick a
different candidate for the second pilot.

---

## 6. Rollback / disablement procedure (execution version)

Execution form of `PHASE_I_RUNBOOK.md` §7. Use this when the pilot
ends (normally or aborted) and the customer needs to return to
`shipment_required=false`.

### Trigger conditions

Disable is the right action when **any** of:

- Pilot completed its two-week window and §7 sign-off decides to
  pause rollout on this company.
- Engineering escalation (§5 T1-T7) occurred and requires a
  clean slate to investigate.
- Customer requests disable for business reasons (ops change, new
  workflow, etc.).
- Fiscal period-end approaches and decision is to freeze Phase I
  traffic until the next period.
- Tracked sell-side items appeared in the catalog (W3 criterion-4
  hard trigger).

### Execution

**Step 1 — Flip the rail back (Owner: Ops).**

```go
services.ChangeCompanyShipmentRequired(db,
    services.ChangeCompanyShipmentRequiredInput{
        CompanyID: $CID,
        Required:  false,
        Actor:     "ops@taxdeep/pilot-$TICKET/disable",
    })
```

**Verify:**

```sql
SELECT action, details_json FROM audit_logs
WHERE company_id=$CID
  AND action='company.shipment_required.disabled'
ORDER BY id DESC LIMIT 1;
```

Non-blocking: `waiting_for_invoice` queue depth is **not** gated
on disable. Disable succeeds regardless of open rows — per
`PHASE_I_RUNBOOK.md` §7.

**Step 2 — Snapshot the exit state (Owner: Ops).**

Record for the pilot ticket:

```sql
-- Open WFI rows at disable time
SELECT COUNT(*) AS open_wfi_at_disable,
       COUNT(DISTINCT shipment_id) AS distinct_shipments,
       SUM(qty_pending * unit_cost_base) AS total_pending_cost_base
FROM waiting_for_invoice_items
WHERE company_id=$CID AND status='open';

-- Closed WFI rows during the pilot window
SELECT COUNT(*) AS closed_wfi_during_pilot
FROM waiting_for_invoice_items
WHERE company_id=$CID AND status='closed'
  AND resolved_at > $STEP3_TIMESTAMP;

-- Count of matched invoice lines during pilot
SELECT COUNT(*) AS matched_invoice_lines
FROM invoice_lines il
JOIN invoices i ON i.id = il.invoice_id
WHERE i.company_id=$CID AND il.shipment_line_id IS NOT NULL
  AND i.posted_at > $STEP3_TIMESTAMP;

-- Shipments posted during pilot
SELECT COUNT(*) AS shipments_posted_during_pilot
FROM shipments
WHERE company_id=$CID AND posted_at > $STEP3_TIMESTAMP
  AND status='posted';
```

**Step 3 — Decide on queue closeout (Owner: Accounting + CS).**

Per `PHASE_I_RUNBOOK.md` §7 Step 4: non-empty WFI queue at
disable is **not** an engineering event.

| Queue state | Action | Engineering involvement |
|---|---|---|
| Zero open rows | None | None |
| Open rows, all explained by in-flight Shipments awaiting Invoice | Optional: post missing Invoices with `shipment_line_id` set **before** disable (preferred — preserves clean chain). Post-disable, those Invoice posts reject `shipment_line_id` and the queue can only be drained via manual decision. | None |
| Open rows, customer wants clean state post-disable | Document in period-end notes and leave — WFI carries no GL impact. Engineering cannot automatically "close" the rows post-disable without violating the audit contract (a closed row must point at a real Invoice line). | None |
| Open rows **not** explained by in-flight Shipments | Investigate with engineering support | **Yes** — possible I.3 / I.5 regression |

Special note on **closed** and **voided** WFI rows: those stay in
the table forever as audit artefacts. They do not require any
action at disable time. Do **not** delete historical WFI rows
even if the customer asks — they are the match-history record.

**Step 4 — No account linkage to decide.**

Phase I.B has no company-level GR/IR / PPV / SNI accounts to
leave linked or unlink. Skip to Step 5.

**Step 5 — Record pilot outcome.**

Attach to the pilot ticket: exit state snapshot, closeout
decision, and recommendation (expand / hold / freeze) per §7.

### What disable does NOT do

This is the fastest thing to get wrong. State it in the pilot
ticket explicitly:

- Disable **does not** rewrite past Shipments or Invoices.
- Disable **does not** convert pre-disable
  `source_type='shipment'` inventory movements into anything
  else.
- Disable **does not** auto-close or auto-void open WFI rows.
- Disable **does not** re-create `source_type='invoice'`
  inventory movements or Invoice COGS lines for Invoices posted
  during the pilot window. Those Invoices keep their I.4 JE
  shape forever.

---

## 7. Sign-off / exit criteria

At the end of the two-week observation window, the pilot
**passes** if every condition holds. Failure of any single
condition sends the pilot to §6 rollback with the appropriate
escalation.

### Pass conditions (all required)

| # | Condition | Owner to verify |
|---|---|---|
| P1 | Zero §5 T1-T7 engineering escalations triggered during the window | Engineering |
| P2 | All D1-D4 daily checks recorded every business day; no silent gap | Ops |
| P3 | W1 weekly reconcile passed for each of the two weeks | Accounting |
| P4 | Customer operator submitted no support tickets that traced to a Phase I misunderstanding CS could not resolve via `PHASE_I_RUNBOOK.md` §5 | CS |
| P5 | `waiting_for_invoice` queue state at window close is explainable: every open row maps to a real in-flight Shipment awaiting Invoice per customer operations; every closed row points at a real Invoice line; every voided row has a voided source Shipment | Accounting + Ops |
| P6 | Shipped-but-unbilled GL gap (COGS debit vs Revenue credit) tracks consistent with the customer's actual ship-to-invoice cadence | Accounting |
| P7 | No pilot-fitness regression: customer still fits §2 criteria at week 2 (criterion 4 **hard** — any tracked sell-side items disqualify regardless of other conditions) | CS + Ops |

### On pass — expansion decision

Each P condition ends the window in one of three states, recorded
explicitly in the pilot ticket:

- **Clean pass:** zero exceptions for this condition during the
  window.
- **Marginal pass:** exactly **one** exception recorded and
  resolved within the window (e.g. P2 had a single day with one
  missed check that was backfilled the next morning; P3 had a
  single reconciliation item that Accounting cleared during the
  week).
- **Fail:** **two or more** exceptions on the same condition, OR
  any exception that escalated to engineering, OR any exception
  that was left unresolved at window close.

Expansion decision uses these three states:

- **Green-light second company:** P1-P7 all **clean pass** AND
  the second candidate has independently cleared §2. Second
  company gets its own pilot ticket; this document is re-used
  as-is.
- **Hold:** P1-P7 no fails, but **one or more** conditions in
  **marginal pass**. Run another observation window on the same
  pilot company before touching a second. Do not batch.
- **Freeze expansion:** **two or more** conditions in marginal
  pass, OR any condition in fail, OR any manual intervention
  that required engineering participation during the window.
  Freeze further enablement pending engineering + accounting
  joint review of the specific friction points.

### On fail — rollback + lessons

Fail of any P condition triggers §6 rollback. In addition:

- Write a short post-mortem referencing the failing P condition.
- **Engineering-involving failures** (P1 invariant break) get a
  dedicated bug ticket; do not enable the next pilot until the
  root cause is fixed and a matching hardening slice (pattern:
  `I-hardening-N`) ships.
- **Customer-experience failures** (P4, P6) route to CS and AM
  to refine the customer-selection criteria in §2 for the next
  candidate — do not soften the pass conditions.

### Freeze conditions (hard stop)

Stop **all** Phase I enablement activity if any of:

- Two independent pilots both failed P1 on different invariants.
- A regression is confirmed in I.3 / I.4 / I.5 code that survived
  the existing test suite.
- An accounting close at a pilot customer produces a result that
  cannot be reconciled by Accounting to the Phase I.B ledger
  records (Shipment → Invoice chain) without engineering
  support.
- A `waiting_for_invoice` row is discovered in a state the I.3 /
  I.5 state machine does not allow (status outside open / closed
  / voided; closed row without resolution identity; open row
  whose source Shipment is voided).

Unfreezing requires engineering + accounting joint sign-off on
the specific fix or correction.

---

## Document change log

| Date | Change |
|---|---|
| 2026-04-20 | Initial draft, post I.5 merge (`fd3398f`), before first pilot enablement. Applies to current Phase I scope selection **Phase I.B** only. Phase H pilot playbook remains separate. |
