# Phase H Pilot Enablement — Execution Playbook

**Document type:** Execution supplement to [PHASE_H_RUNBOOK.md](PHASE_H_RUNBOOK.md).
**Status:** Active after H.5 + H-hardening-1 merge; active through
the first batch of pilot enablements.
**Applies to:** One-at-a-time `receipt_required=true` enablements on
selected pilot companies.
**Semantic reference:** Every definition of Receipt / Bill / GR/IR /
PPV / matching / by-design vs bug in this document defers to
`PHASE_H_RUNBOOK.md`. If this doc and the runbook disagree, the
runbook wins.

---

## 1. Purpose / Audience / Non-goals

### Purpose

Execute a **controlled, reversible pilot** of the Receipt-first
inbound model on a single low-risk company, verify the H.3/H.4/H.5
contract holds in that company's real posting traffic, and produce
the evidence needed to decide whether to expand or freeze further
rollout.

### Audience

| Role | Responsibility |
|---|---|
| **Ops** | Executes admin actions; runs checks; escalates on triggered conditions. |
| **Engineering** | Receives escalations; investigates lock / invariant regressions; does not drive rollout pace. |
| **Accounting** | Owns GR/IR closeout decisions and period-end reconciliation JEs. |
| **CS** | Watches the candidate customer's reported experience; uses `PHASE_H_RUNBOOK.md` §5 triage for customer-facing answers. |

### Non-goals

- This is **not** a general rollout playbook. Second and subsequent
  enablements reuse this document but §7 gates each one.
- This is **not** customer-facing. Customer language belongs in the
  runbook.
- This document does **not** define semantics. Any "what does
  X mean?" question resolves via the runbook reference.
- This document does **not** describe UI. Phase H current workflow =
  admin/API path. A UI layer ships in a later phase; the pilot
  deliberately uses the API to prove the semantic contract.

---

## 2. Pilot candidate selection criteria

A company is a **valid Phase H pilot candidate** if it clears every
row below. Any red row disqualifies.

| # | Criterion | Why | Verification query / check | Owner |
|---|---|---|---|---|
| 1 | **Trailing 30-day** Bill volume ≤ 50 | Smaller audit surface; CS can actually read every Bill | `SELECT COUNT(*) FROM bills WHERE company_id=$CID AND bill_date >= NOW() - INTERVAL '30 days'` | Ops |
| 2 | **Trailing 30-day** Receipt-eligible Bill line volume ≤ 200 | Matching volume manageable by a human operator during pilot | `SELECT COUNT(*) FROM bill_lines bl JOIN bills b ON b.id=bl.bill_id JOIN product_services p ON p.id=bl.product_service_id WHERE b.company_id=$CID AND p.is_stock_item=true AND b.bill_date >= NOW() - INTERVAL '30 days'` | Ops |
| 3 | Has at least one stock-item `ProductService` | No stock items = no Receipt-first inbound traffic to validate; such a company is not a meaningful pilot candidate regardless of other criteria | `SELECT COUNT(*) FROM product_services WHERE company_id=$CID AND is_stock_item=true` returns **> 0** | Ops |
| 4 | Tracked-item share of stock catalog ≤ 30% | Reduce interaction between Phase H matching and Phase F/G tracking surface | `SELECT SUM(CASE WHEN tracking_mode != 'none' THEN 1 ELSE 0 END)::float / NULLIF(COUNT(*), 0) FROM product_services WHERE company_id=$CID AND is_stock_item=true` returns ≤ 0.30 (NULL treated as pass-through to criterion 3, which is the authoritative stock-item gate) | Ops |
| 5 | Predictable receive-then-bill cadence | Operator can reliably link each Bill to its Receipt; reduces unlinked/blind posts | Sample last 20 stock Bills: at least 15 correspond to a prior deliverable recorded somewhere in the customer's workflow | CS + Ops |
| 6 | Cooperative named operator on the customer side | Someone accountable for following the admin/API workflow during pilot | Written confirmation from AM + operator's direct contact on file | CS |
| 7 | No known manual inventory-related adjustments in the current / open period | Unresolved manual inventory JEs can make the pilot's GR/IR and inventory reconciliation ambiguous — were they pre-pilot or pilot-induced? Start from a clean, accountable state. | **Ledger review** by Accounting on the company's Inventory Asset, COGS, and any pre-existing GR/IR-candidate accounts for the open period; **plus** explicit Accounting lead confirmation in the pilot ticket that no known manual adjustments are outstanding. SQL-only naming-pattern filters (e.g. `journal_no LIKE 'ADJ-%'`) are **not** sufficient — they only find entries that happened to be named a certain way and miss inventory adjustments posted without that convention. | Accounting |
| 8 | No prior `receipt_required` enablement attempt | First-time enablement on the company | `SELECT COUNT(*) FROM audit_logs WHERE company_id=$CID AND action IN ('company.receipt_required.enabled','company.receipt_required.disabled')` returns 0 | Ops |
| 9 | Not cutting a fiscal period in the next 30 days | Avoid straddling a close with an in-flight pilot | Per customer's fiscal calendar | Accounting + CS |

**Disqualifying:** any row returning a red result. Do not negotiate
around any of the nine.

**Escalation if a strong candidate partially fails one criterion:**
engineering review **required** before proceeding — the pilot's
purpose is to validate the contract cleanly, not to stress-test
under adverse conditions.

---

## 3. Pre-flight checklist

Executed in the hour before §4's enablement workflow. Maps to
`PHASE_H_RUNBOOK.md` §1 (five gates) and §6 (enablement steps).

### Gate 1 — Catalog readiness (item-level)

**Check:** Every stock item in the company has
`product_services.inventory_account_id` set.

```sql
SELECT id, name, sku
FROM product_services
WHERE company_id = $CID
  AND is_stock_item = true
  AND inventory_account_id IS NULL;
```

- **Expected result:** 0 rows.
- **Stop condition:** Any row returned → fix the catalog before
  continuing. Owner: **Ops** (coordinates with the customer to
  assign the correct Inventory Asset account).
- **No workaround.** `PostReceipt` will fail loud on the first
  stock line otherwise; attempting enablement without this gate
  clean is an operator mistake, not an engineering problem.

### Gate 2 — Two company-level accounts exist in the CoA

**Check A:** A Liability account earmarked for GR/IR exists.

```sql
SELECT id, code, name, root_account_type, detail_account_type
FROM accounts
WHERE company_id = $CID
  AND root_account_type = 'liability'
  AND is_active = true;
```

- **Expected:** at least one active liability account named
  consistent with GR/IR purpose (typically `GR/IR Clearing` or
  `Goods Received Not Invoiced`).
- **Stop condition:** No candidate liability account → customer
  creates one first. Owner: **Accounting** (customer-side).

**Check B:** An Expense or Cost-of-Sales account earmarked for PPV
exists.

```sql
SELECT id, code, name, root_account_type, detail_account_type
FROM accounts
WHERE company_id = $CID
  AND root_account_type IN ('expense','cost_of_sales')
  AND is_active = true;
```

- **Expected:** at least one active P&L account suitable for PPV
  (typically `Purchase Price Variance`).
- **Stop condition:** No candidate P&L account → customer creates
  one first. Owner: **Accounting** (customer-side).

### Gate 3 — Current capability rail is OFF

**Check:** `receipt_required` currently equals `false` for the
target company. GR/IR and PPV linkage ideally also start from a
clean (NULL) baseline, but this is a soft sub-check — see the
exception note below.

```sql
SELECT receipt_required,
       gr_ir_clearing_account_id,
       purchase_price_variance_account_id
FROM companies
WHERE id = $CID;
```

- **Hard expectation:** `receipt_required = false`.
- **Soft expectation (clean-baseline pilot):**
  `gr_ir_clearing_account_id IS NULL` and
  `purchase_price_variance_account_id IS NULL`.
- **Stop condition (hard):** `receipt_required` already `true` →
  halt immediately. This contradicts criterion 8 in §2 and means
  the company is not a first-time enablement. Owner: **Ops +
  Engineering** to investigate `audit_logs`.

**Exception note — pre-configured linkage without rail flip is NOT
automatically a bug.** If the query returns
`receipt_required=false` but either account ID is non-NULL, it
simply means someone ran `ChangeCompanyGRIRClearingAccount` or
`ChangeCompanyPPVAccount` in advance (e.g. during ops onboarding
for this pilot, or on a prior aborted attempt) without flipping
the rail. Decide which of the two paths applies:

```sql
-- Triage the history first:
SELECT action, actor, created_at, details_json
FROM audit_logs
WHERE company_id = $CID
  AND action IN (
    'company.gr_ir_clearing_account.set',
    'company.gr_ir_clearing_account.cleared',
    'company.ppv_account.set',
    'company.ppv_account.cleared',
    'company.receipt_required.enabled',
    'company.receipt_required.disabled'
  )
ORDER BY id;
```

- **Path A — clear back to clean baseline** (recommended default):
  if the history shows no prior `receipt_required.enabled` audit
  row, the pre-configured linkage is harmless residual but does
  clutter the "what changed during pilot" story. Call
  `ChangeCompanyGRIRClearingAccount(…, AccountID=nil,…)` and
  `ChangeCompanyPPVAccount(…, AccountID=nil,…)` to wipe, then
  re-wire them in §4 Steps 1-2 cleanly. Owner: **Ops**.
- **Path B — accept and proceed** (only if Path A is operationally
  costly): record in the pilot ticket that the company entered
  pilot with pre-existing linkage and explicitly note the account
  IDs. Observation window §5's D4 GR/IR snapshot interprets "base
  balance" against that pre-existing account ID. Owner: **Ops +
  Accounting** jointly, with written ticket entry.

Either path is acceptable. Do **not** escalate pre-configured
linkage to engineering on its face — it only becomes an
engineering concern if the audit history is inconsistent with
what the current row shows (e.g. an `enabled` event with no
matching `disabled` that would explain the current `false` state).

### Gate 4 — GR/IR baseline = zero

**Check:** If a candidate GR/IR account has been identified in
Gate 2 check A, its current balance must be zero (no stray
postings).

```sql
SELECT COALESCE(SUM(debit_amount) - SUM(credit_amount), 0) AS balance
FROM ledger_entries
WHERE company_id = $CID
  AND account_id = $GRIR_CANDIDATE_ID
  AND status = 'active';
```

- **Expected:** `balance = 0`.
- **Stop condition:** Non-zero → Accounting clears it first (manual
  JE to a reconciliation account, or investigate source). Pilot
  starts against a known-zero GR/IR so subsequent movement is
  attributable to pilot traffic. Owner: **Accounting**.

### Gate 5 — Operator & CS readiness sign-off

**Check:** Written or ticketed confirmation from:

- Customer-side operator: "I will create a Receipt before posting
  the matching Bill, and I will set `bill_lines.receipt_line_id`
  when linking."
- CS / AM: "I have the customer's two-week observation commitment."
- Accounting lead: "I will handle GR/IR closeout if residual
  requires operational reconciliation."

- **Expected:** three explicit confirmations captured in the pilot
  ticket before proceeding.
- **Stop condition:** Any missing confirmation → wait.

---

## 4. Enablement workflow

Sequence is strict. Each step has an immediate audit verification.
Any failed audit = stop, investigate, **do not** proceed to the
next step.

**Executor:** Ops, running against production DB under the pilot
ticket. **Reviewer:** Engineering, double-checks each audit row.

### Step 1 — Wire GR/IR clearing account (Owner: Ops, Reviewer: Engineering)

**Action:**

```go
// Go admin harness; substitute actor with the ops engineer's identity.
services.ChangeCompanyGRIRClearingAccount(db,
    services.ChangeCompanyGRIRClearingAccountInput{
        CompanyID: $CID,
        AccountID: &grirAccountID,  // from Gate 2 check A
        Actor:     "ops@taxdeep/pilot-$TICKET",
    })
```

**Expected audit row:**

```sql
SELECT action, actor, details_json
FROM audit_logs
WHERE company_id = $CID
  AND action = 'company.gr_ir_clearing_account.set'
ORDER BY id DESC LIMIT 1;
```

- `action = 'company.gr_ir_clearing_account.set'`
- `actor` matches the executor string
- `details_json.after.gr_ir_clearing_account_id` equals `grirAccountID`

**Stop condition if:** no audit row written, or action is
`.cleared` instead of `.set`, or account ID doesn't match. Halt
pilot; investigate.

### Step 2 — Wire PPV account (Owner: Ops, Reviewer: Engineering)

**Action:**

```go
services.ChangeCompanyPPVAccount(db,
    services.ChangeCompanyPPVAccountInput{
        CompanyID: $CID,
        AccountID: &ppvAccountID,  // from Gate 2 check B
        Actor:     "ops@taxdeep/pilot-$TICKET",
    })
```

**Expected audit row:**

```sql
SELECT action, actor, details_json
FROM audit_logs
WHERE company_id = $CID
  AND action = 'company.ppv_account.set'
ORDER BY id DESC LIMIT 1;
```

- `action = 'company.ppv_account.set'`
- `details_json.after.purchase_price_variance_account_id` equals
  `ppvAccountID`

**Stop condition if:** same pattern as Step 1.

### Step 3 — Flip the capability rail (Owner: Ops, Reviewer: Engineering)

**Action:**

```go
services.ChangeCompanyReceiptRequired(db,
    services.ChangeCompanyReceiptRequiredInput{
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
  AND action = 'company.receipt_required.enabled'
ORDER BY id DESC LIMIT 1;
```

- `action = 'company.receipt_required.enabled'`
- `details_json.before.receipt_required = false`
- `details_json.after.receipt_required = true`

**Stop condition if:** missing audit, wrong before-state, or rail
did not actually flip. Call `ChangeCompanyReceiptRequired(…,
Required=false,…)` to back out and halt pilot.

### Step 4 — End-to-end smoke cycle (Owner: Ops + Customer operator)

Runs against a **single, low-value, non-tracked** stock item.
Purpose: prove the full Receipt → match → Bill chain produces the
expected artefacts. Do **not** allow ordinary customer posting
under the pilot company until this smoke cycle passes — Steps 1-3
already touched the pilot company's production database, but only
admin configuration; the smoke cycle is the first end-to-end
Receipt+Bill traffic on the flipped rail and must clear before
the customer resumes normal posting.

**Sub-step 4a — Post a Receipt.** Customer operator creates a
Receipt for (say) 10 units @ $5 of a chosen stock item in a chosen
warehouse; then `services.PostReceipt(…)`.

**Verify:**

```sql
-- (i) Receipt row status
SELECT status, posted_at, journal_entry_id
FROM receipts WHERE id = $RECEIPT_ID;
-- expect: status='posted', posted_at not null, journal_entry_id not null

-- (ii) Inventory movement exists, source_type='receipt'
SELECT id, source_type, source_id, quantity_delta
FROM inventory_movements
WHERE company_id = $CID AND source_type = 'receipt' AND source_id = $RECEIPT_ID;
-- expect: exactly one row, quantity_delta = +10

-- (iii) JE shape: Dr Inventory, Cr GR/IR
SELECT account_id, debit, credit
FROM journal_lines
WHERE journal_entry_id = (SELECT journal_entry_id FROM receipts WHERE id=$RECEIPT_ID);
-- expect: one debit on the item's inventory_account_id for 50.00
--         one credit on grirAccountID for 50.00

-- (iv) Audit
SELECT action FROM audit_logs
WHERE entity_type='receipt' AND entity_id=$RECEIPT_ID
ORDER BY id DESC LIMIT 1;
-- expect: 'receipt.posted'
```

**Stop condition if:** any of (i)-(iv) deviates.

**Sub-step 4b — Post a matched Bill.** Operator creates a Bill
with one line at 10 units @ $6 for the same item, sets
`bill_lines.receipt_line_id` to the Receipt's line ID, then
`services.PostBill(…)`.

**Verify:**

```sql
-- (i) Bill status
SELECT status, journal_entry_id
FROM bills WHERE id = $BILL_ID;
-- expect: status='posted', journal_entry_id not null

-- (ii) NO bill-sourced inventory movement
SELECT COUNT(*) FROM inventory_movements
WHERE company_id=$CID AND source_type='bill' AND source_id=$BILL_ID;
-- expect: 0

-- (iii) JE shape: Dr GR/IR, Dr PPV (unfavorable), Cr AP
SELECT account_id, debit, credit, memo
FROM journal_lines
WHERE journal_entry_id = (SELECT journal_entry_id FROM bills WHERE id=$BILL_ID);
-- expect: Dr 50 on grirAccountID (matched at receipt cost)
--         Dr 10 on ppvAccountID (10 × (6-5) unfavorable)
--         Cr 60 on AP account
```

**Stop condition if:** inventory movement appears on the Bill, or
the debit decomposition is wrong, or PPV is missing / miscredited.

**Sub-step 4c — GR/IR net balance = zero after the pair.**

```sql
SELECT COALESCE(SUM(debit_amount) - SUM(credit_amount), 0) AS net
FROM ledger_entries
WHERE company_id=$CID AND account_id=$GRIR_ID AND status='active';
-- expect: 0 (Receipt credited 50; Bill debited 50; match exact)
```

**Stop condition if:** non-zero. This is the only scenario in the
smoke cycle where GR/IR residual is a fail signal — matched-exact
traffic with no prior accrual must produce zero.

**On smoke pass:** pilot enablement is technically complete.
Observation window (§5) begins.

---

## 5. Observation window

Two calendar weeks from Step 3's audit timestamp. Every finding in
this window categorises into one of three buckets:

| Bucket | Owner to act | Meaning |
|---|---|---|
| **By-design residual** | None (record only) | Normal Phase H behaviour per `PHASE_H_RUNBOOK.md` §4 and §5. |
| **Operational reconciliation** | Accounting | GR/IR needs human attention (manual JE, post missing Bills, etc.); not engineering. |
| **Engineering escalation** | Engineering | Invariant violation or unexplained state — stop pilot expansion until resolved. |

### Daily checks (every business day, 09:00 customer-TZ)

**D1 — New Receipts posted.**

```sql
SELECT id, receipt_number, posted_at, journal_entry_id
FROM receipts
WHERE company_id=$CID AND posted_at >= NOW() - INTERVAL '24 hours'
  AND status='posted';
```

Expect: every row has `journal_entry_id` non-null unless the
Receipt had only service lines (runbook §5). Record any
service-only Receipt — if more than expected, raise with Ops.

**D2 — New Bills posted.**

```sql
SELECT b.id, b.bill_number, b.posted_at, b.journal_entry_id,
       COUNT(bl.id) AS line_count,
       SUM(CASE WHEN bl.receipt_line_id IS NOT NULL THEN 1 ELSE 0 END) AS matched_lines,
       SUM(CASE WHEN bl.receipt_line_id IS NULL AND p.is_stock_item THEN 1 ELSE 0 END) AS unmatched_stock_lines
FROM bills b
JOIN bill_lines bl ON bl.bill_id = b.id
LEFT JOIN product_services p ON p.id = bl.product_service_id
WHERE b.company_id=$CID AND b.posted_at >= NOW() - INTERVAL '24 hours'
  AND b.status='posted'
GROUP BY b.id;
```

Routing rules based on explicit thresholds (per day, over the
24-hour window of this query):

Let `U` = sum of `unmatched_stock_lines` across the day's posted
Bills, and `T` = sum of `line_count` filtered to stock-item lines
across the same set.

- **By-design (record only, no action):** `U = 0`, OR `U > 0` with
  `U / T ≤ 0.20` on a single day. Unmatched lines are legal per
  `PHASE_H_RUNBOOK.md` §3-§4; up to 20% of stock-line traffic on
  any one day is within expected operator variance.
- **Operational concern (Owner: CS + Ops):** **either** of:
  - Single-day: `U / T > 0.20`. CS asks the operator the next
    business day for the specific reason on each unmatched Bill.
  - Trend: `U / T > 0.10` on each of **three consecutive** business
    days. Schedule an operator re-training session with the
    customer by end of the following week.
- **Not engineering** unless a D5 trigger also fires.

**D3 — Bills with matching but no PPV line when prices match.**

```sql
-- Sanity: a matched Bill with price-eq receipt cost should emit no PPV line.
-- Scan the previous day's matched bills and verify zero-variance cases
-- have zero journal_lines against ppvAccountID.
-- Investigate any PPV line posted for a matched pair where
-- bill_line.unit_price = receipt_line.unit_cost.
```

If anomalies: record and report to engineering by end of day (D5
conditions).

**D4 — GR/IR net balance snapshot.**

```sql
SELECT DATE_TRUNC('day', NOW()) AS snapshot_date,
       COALESCE(SUM(debit_amount) - SUM(credit_amount), 0) AS grir_net,
       COUNT(*) AS ledger_line_count
FROM ledger_entries
WHERE company_id=$CID AND account_id=$GRIR_ID AND status='active';
```

Record. Non-zero is **expected** during normal operation (runbook
§4). Watch the **trend**, not the absolute.

**D5 — Engineering-escalation triggers for today.**

Escalate **immediately** (Ops pages Engineering) if ANY of:

- **T1:** Inventory movement with `source_type='bill'` dated after
  Step 3's audit timestamp → Bill-forms-inventory regression (H.4
  guard failed).
  ```sql
  SELECT id, source_id, created_at FROM inventory_movements
  WHERE company_id=$CID AND source_type='bill'
    AND created_at > $STEP3_TIMESTAMP;
  ```
- **T2:** Cumulative matched qty on any receipt line exceeds the
  receipt line's qty → H-hardening-1 lock regression.
  ```sql
  SELECT rl.id AS receipt_line_id, rl.qty AS receipt_qty,
         SUM(bl.qty) AS cumulative_matched
  FROM receipt_lines rl
  JOIN bill_lines bl ON bl.receipt_line_id = rl.id
  JOIN bills b ON b.id = bl.bill_id
  WHERE rl.company_id=$CID AND b.status='posted'
  GROUP BY rl.id, rl.qty
  HAVING SUM(bl.qty) > rl.qty;
  ```
- **T3:** Receipt posted with stock lines under `receipt_required=true`
  but no `journal_entry_id`.
  ```sql
  SELECT r.id FROM receipts r
  WHERE r.company_id=$CID AND r.status='posted'
    AND r.posted_at > $STEP3_TIMESTAMP
    AND r.journal_entry_id IS NULL
    AND EXISTS (
      SELECT 1 FROM receipt_lines rl
      JOIN product_services p ON p.id = rl.product_service_id
      WHERE rl.receipt_id = r.id AND p.is_stock_item = true
    );
  ```
- **T4:** Bill posted under `receipt_required=true` lacks
  `journal_entry_id` (post succeeded but JE missing). PostBill is
  designed to always create a JE for a posted Bill regardless of
  flag state; a `status='posted'` row with NULL JE link is a
  data-integrity regression.
  ```sql
  SELECT id, bill_number, posted_at
  FROM bills
  WHERE company_id = $CID
    AND status = 'posted'
    AND posted_at > $STEP3_TIMESTAMP
    AND journal_entry_id IS NULL;
  ```
- **T5:** Any T1-T4 reproduces after a clean re-post attempt
  (void the offending document, recreate, repost; if the T1-T4
  query still returns rows, the regression is deterministic, not a
  transient fluke).

If no T1-T5 hits, the day's check is complete. Record the snapshot
and move on.

### Weekly checks (every Friday, end of business)

**W1 — Reconcile GR/IR ledger to source documents.**

**Volume-based review scope (pick one, do not do both):**

- **Full review** applies when the week's stock-line volume ≤ 50:
  for every posted Receipt and Bill in the last 7 days with at
  least one stock line, confirm exactly one JE line hit the GR/IR
  account and the amount reconciles to the source document's
  expected contribution.
- **Sampled review** applies when the week's stock-line volume >
  50: take a random sample of **max(10, ceil(0.10 × weekly stock
  line count))** stock-touching Receipts and Bills from the last 7
  days. Run the same per-document reconciliation on the sampled
  set only. Record the sample size in the pilot ticket.

In either mode, the outcomes classify as:

- **By design:** residual at week-end is non-zero on the company
  level (runbook §4). This by itself is not a W1 finding.
- **Operational (Owner: Accounting + CS):** a specific Receipt
  shows a GR/IR credit with no follow-up Bill for ≥ 7 calendar
  days → flag to customer as "Bill expected, please confirm."
- **Engineering (Owner: Engineering):** any single document in the
  reviewed / sampled set where the source document amount and the
  JE line amount disagree and the gap is not explained by known
  matching behaviour (partial match, over-match to blind GR/IR,
  zero-variance short-circuit). File a bug ticket with the
  specific Bill / Receipt IDs and halt pilot expansion until
  resolved.

**W2 — PPV trend.**

```sql
SELECT DATE_TRUNC('week', je.entry_date) AS week,
       SUM(jl.debit - jl.credit) AS ppv_net_weekly
FROM journal_lines jl
JOIN journal_entries je ON je.id = jl.journal_entry_id
WHERE jl.company_id = $CID AND jl.account_id = $PPV_ID
  AND je.entry_date >= NOW() - INTERVAL '60 days'
GROUP BY 1 ORDER BY 1;
```

Any direction (Dr or Cr) is valid. Sudden jumps warrant a look
(customer renegotiated pricing? one big mismatched Bill?) but are
not bugs on their own.

**W3 — Pilot fitness check against §2 criteria.**

Re-run §2 criteria 1 (Bill volume) and 2 (Receipt-eligible Bill
line volume) against the trailing-30-day window as of the check
day. Explicit pause triggers (pilot expansion halts; existing
pilot continues under tighter watch, not rolled back automatically):

- Criterion 1 current reading > 75 (i.e. exceeded its 50 ceiling
  by ≥50%), OR
- Criterion 2 current reading > 300 (exceeded 200 ceiling by
  ≥50%), OR
- Criterion 4 (tracked-item share) current reading > 0.45
  (exceeded 0.30 ceiling by ≥50%).

Any one trigger = pause further rollout decisions until week-2
re-check. Two or more triggers in the same check = pilot is no
longer representative of "low-risk"; run §6 rollback on this
company and pick a different candidate for the second pilot.

---

## 6. Rollback / disablement procedure (execution version)

Execution form of `PHASE_H_RUNBOOK.md` §7. Use this when the pilot
ends (normally or aborted) and the customer needs to return to
`receipt_required=false`.

### Trigger conditions

Disable is the right action when **any** of:

- Pilot completed its two-week window and §7 sign-off decides to
  pause rollout on this company.
- Engineering escalation (§5 T1-T5) occurred and requires a clean
  slate to investigate.
- Customer requests disable for business reasons (ops change, new
  workflow, etc.).
- Fiscal period-end approaches and decision is to freeze Phase H
  traffic until the next period.

### Execution

**Step 1 — Flip the rail back (Owner: Ops).**

```go
services.ChangeCompanyReceiptRequired(db,
    services.ChangeCompanyReceiptRequiredInput{
        CompanyID: $CID,
        Required:  false,
        Actor:     "ops@taxdeep/pilot-$TICKET/disable",
    })
```

**Verify:**

```sql
SELECT action, details_json FROM audit_logs
WHERE company_id=$CID
  AND action='company.receipt_required.disabled'
ORDER BY id DESC LIMIT 1;
```

Non-blocking: GR/IR balance is **not** gated on disable. Disable
succeeds regardless of residual — per `PHASE_H_RUNBOOK.md` §7.

**Step 2 — Snapshot the exit state (Owner: Ops).**

Record for the pilot ticket:

```sql
-- Final GR/IR balance
SELECT COALESCE(SUM(debit_amount) - SUM(credit_amount), 0) AS grir_at_disable
FROM ledger_entries
WHERE company_id=$CID AND account_id=$GRIR_ID AND status='active';

-- Final PPV balance
SELECT COALESCE(SUM(debit_amount) - SUM(credit_amount), 0) AS ppv_at_disable
FROM ledger_entries
WHERE company_id=$CID AND account_id=$PPV_ID AND status='active';

-- Count of matched bill lines during pilot
SELECT COUNT(*) AS matched_lines_during_pilot
FROM bill_lines bl
JOIN bills b ON b.id = bl.bill_id
WHERE b.company_id=$CID AND bl.receipt_line_id IS NOT NULL
  AND b.posted_at > $STEP3_TIMESTAMP;
```

**Step 3 — Decide on GR/IR closeout (Owner: Accounting).**

Per `PHASE_H_RUNBOOK.md` §7 Step 4: non-zero GR/IR residual at
disable is **not** an engineering event.

| Residual status | Accounting action | Engineering involvement |
|---|---|---|
| Zero | None | None |
| Non-zero, fully explained by open Receipts and Bills | Optional: post missing Bills (preferred), or leave residual and document in period-end notes | None |
| Non-zero, fully explained but customer wants clean account | Manual JE to clear to a reconciliation account with memo | None |
| Non-zero, **not** explained by open documents | Investigate with engineering support | **Yes** — possible H.3/H.4/H.5 regression |

**Step 4 — Decide on account linkage (Owner: Ops + Accounting).**

The GR/IR and PPV accounts remain wired to the company after
disable. This is by design; they do nothing on the legacy
(`receipt_required=false`) path.

- **Leave linked:** preferred default. Re-enabling the pilot later
  does not require re-wiring.
- **Unlink:** only if the customer wants a clean decommission.
  Execute via
  `ChangeCompanyGRIRClearingAccount(…, AccountID=nil,…)` and
  `ChangeCompanyPPVAccount(…, AccountID=nil,…)`. Each emits a
  `.cleared` audit action.

**Step 5 — Record pilot outcome.**

Attach to the pilot ticket: exit state snapshot, closeout decision,
and recommendation (expand / hold / freeze) per §7.

### What disable does NOT do

This is the fastest thing to get wrong. State it in the pilot
ticket explicitly:

- Disable **does not** rewrite past Bills or Receipts.
- Disable **does not** convert pre-disable `source_type='receipt'`
  inventory movements into anything else.
- Disable **does not** auto-clear GR/IR.
- Disable **does not** re-create `source_type='bill'` inventory
  movements for Bills posted during the pilot window. Those Bills
  keep their H.4/H.5 JE shape forever.

---

## 7. Sign-off / exit criteria

At the end of the two-week observation window, the pilot
**passes** if every condition holds. Failure of any single
condition sends the pilot to §6 rollback with the appropriate
escalation.

### Pass conditions (all required)

| # | Condition | Owner to verify |
|---|---|---|
| P1 | Zero §5 T1-T5 engineering escalations triggered during the window | Engineering |
| P2 | All D1-D4 daily checks recorded every business day; no silent gap | Ops |
| P3 | W1 weekly reconcile passed for each of the two weeks | Accounting |
| P4 | Customer operator submitted no support tickets that traced to a Phase H misunderstanding CS could not resolve via `PHASE_H_RUNBOOK.md` §5 | CS |
| P5 | GR/IR balance movement at end of window is explainable: every non-zero component maps to an open Receipt awaiting Bill, an open Bill posted blind, or a documented reconciliation action | Accounting |
| P6 | PPV balance direction is consistent with the customer's actual procurement experience (cost overruns or savings match finance's understanding) | Accounting |
| P7 | No pilot-fitness regression: customer still fits §2 criteria at week 2 | CS + Ops |

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

- **Green-light second company:** P1-P7 all **clean pass** AND the
  second candidate has independently cleared §2. Second company
  gets its own pilot ticket; this document is re-used as-is.
- **Hold:** P1-P7 no fails, but **one or more** conditions in
  **marginal pass**. Run another observation window on the same
  pilot company before touching a second. Do not batch.
- **Freeze expansion:** **two or more** conditions in marginal
  pass, OR any condition in fail, OR any manual intervention that
  required engineering participation during the window. Freeze
  further enablement pending engineering + accounting joint
  review of the specific friction points.

### On fail — rollback + lessons

Fail of any P condition triggers §6 rollback. In addition:

- Write a short post-mortem referencing the failing P condition.
- **Engineering-involving failures** (P1 invariant break) get a
  dedicated bug ticket; do not enable the next pilot until the
  root cause is fixed and a matching hardening slice (pattern:
  `H-hardening-N`) ships.
- **Customer-experience failures** (P4, P6) route to CS and AM to
  refine the customer-selection criteria in §2 for the next
  candidate — do not soften the pass conditions.

### Freeze conditions (hard stop)

Stop **all** Phase H enablement activity if any of:

- Two independent pilots both failed P1 on different invariants.
- A regression is confirmed in H.3/H.4/H.5/H-hardening-1 code that
  survived the existing test suite.
- An accounting close at a pilot customer produces a result that
  cannot be reconciled to the Phase H ledger records by Accounting
  without engineering support.

Unfreezing requires engineering + accounting joint sign-off on the
specific fix or correction.

---

## Document change log

| Date | Change |
|---|---|
| 2026-04-19 | Initial draft, post H.5 + H-hardening-1 merge, before first pilot enablement. |
