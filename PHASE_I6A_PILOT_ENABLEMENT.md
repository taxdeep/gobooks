# Phase I.6a Pilot Enablement — AR Return Receipts

> **Layered pilot on top of Phase I.** I.6a does NOT need its own
> `shipment_required` flip — that happens in the main Phase I pilot
> (see `PHASE_I_PILOT_ENABLEMENT.md`). This playbook covers the
> **layered** pilot where a Phase-I-green company starts actually
> using AR stock-return Credit Notes under controlled mode — which
> only became possible when I.6a.3 shipped the CreditNote retrofit.
>
> Before I.6a, Phase-I-green companies had to route AR stock returns
> through manual journal entries or temporarily drop the rail off.
> I.6a closes that gap.

---

## 1. Purpose / Audience / Non-goals

### Purpose

Guide CS + Engineering through enabling I.6a behavior on a
specific pilot company: the company is already
`shipment_required=true` (Phase I pilot-green) and now begins
exercising the stock-return Credit Note path that I.6a.3 enabled.
Operational validation happens over an observation window before
the layer is considered stable.

### Audience

- **CS ops lead** — schedules the pilot window, manages the
  customer communication, runs the daily checklist.
- **Engineering on-call** — receives escalations from daily
  checks; owns Rule #4 invariant failure triage.
- **Pilot customer's accounting operator(s)** — actually clicks
  the buttons; trained on `PHASE_I6_RUNBOOK.md` Step 1–6.

### Non-goals

- NOT about flipping `shipment_required` ON — that's Phase I's
  pilot. I.6a assumes the rail is already true.
- NOT about the AP side (I.6b `VendorReturnShipment`). When I.6b
  ships, its own `PHASE_I6B_PILOT_ENABLEMENT.md` will cover that
  layer.
- NOT a global "enablement" — no capability flag flips. The code
  path activates for any `shipment_required=true` company
  automatically once I.6a.3 ships. This playbook governs the
  **operational rollout** per company, not code behavior.

---

## 2. Pilot candidate selection criteria

A company is a **good I.6a pilot candidate** when ALL hold:

1. **Phase I pilot-green.** `shipment_required=true` in place for
   at least one full business week with zero open
   `waiting_for_invoice` anomalies and zero Rule #4 invariant
   escalations.
2. **Real stock-return volume.** Customer has issued at least one
   stock-item Credit Note per month over the prior 90 days. Zero-
   return customers don't exercise the new path.
3. **Single accounting operator OR fully-trained team.** The pre-CN
   / Return Receipt / post-CN sequence is new; half-trained teams
   produce coverage-shortfall backlogs.
4. **NOT in a Q9 pilot-stacking window.** Specifically: if the
   company is ALSO in a Phase H (`receipt_required`) pilot
   observation window, DELAY I.6a pilot on that company until Phase
   H stabilises. Two controlled-mode pilots on one company at the
   same time pollute incident attribution.

A company is **NOT** a good candidate when ANY hold:

- Active Phase H pilot window.
- High volume of pre-IN.5 Credit Notes without
  `OriginalInvoiceLineID` (data-debt blocks Return Receipt
  creation).
- Heavy tracked-lot / serial returns (I.6a scope-out;
  `PHASE_I6_RUNBOOK.md` §5 #1).

---

## 3. Pre-flight checklist

### Gate 1 — Phase I rail is green on this company

```sql
SELECT id, name, shipment_required
FROM companies
WHERE id = :pilot_company_id;
-- shipment_required MUST be true
```

And per `PHASE_I_PILOT_ENABLEMENT.md` §7 exit criteria, the main
Phase I pilot for this company must have passed.

### Gate 2 — Zero pending coverage shortfalls

```sql
-- Any draft CN under controlled mode with stock-item lines that
-- would currently fail the Q6 coverage check? (Manual inventory
-- of operator backlog before starting pilot — they should be
-- cleared or understood.)
SELECT cn.id, cn.credit_note_number, cnl.id AS cn_line_id,
       cnl.qty AS cn_qty,
       COALESCE(SUM(arrl.qty), 0) AS posted_arr_coverage
FROM credit_notes cn
JOIN credit_note_lines cnl ON cnl.credit_note_id = cn.id
JOIN product_services ps ON ps.id = cnl.product_service_id AND ps.is_stock_item
LEFT JOIN ar_return_receipt_lines arrl ON arrl.credit_note_line_id = cnl.id
LEFT JOIN ar_return_receipts arr ON arr.id = arrl.ar_return_receipt_id AND arr.status = 'posted'
WHERE cn.company_id = :pilot_company_id
  AND cn.status = 'draft'
GROUP BY cn.id, cn.credit_note_number, cnl.id, cnl.qty
HAVING cnl.qty != COALESCE(SUM(arrl.qty), 0);
-- Empty result set = clean baseline.
```

### Gate 3 — Operator training complete

- Customer's AR operator has read `PHASE_I6_RUNBOOK.md` §§1–4.
- Customer confirms understanding of the Step 1–6 sequence.
- Customer has a test sandbox environment (or a permissively
  voidable pilot document) to dry-run once before pilot starts.

### Gate 4 — CS + Engineering coverage

- CS ops lead assigned for the observation window (3 business
  weeks minimum).
- Engineering on-call rotation covers the window, with a named
  Phase I.6 SME as first escalation.

### Gate 5 — Rollback plan

If I.6a shows operational issues during the pilot:

- **Rollback is NOT a code flip** — I.6a can't be turned off once
  code ships.
- **Per-company rollback** = flip `shipment_required=false` on the
  pilot company. This reverts to IN.5 legacy Credit-Note-forms-
  inventory behavior and bypasses Return Receipt entirely. Any
  draft Return Receipts the operator has in-flight become inert
  (can be deleted).
- `PHASE_I_PILOT_ENABLEMENT.md` §6 documents the
  `shipment_required=false` flip procedure; follow that exactly.

---

## 4. Enablement workflow

### Step 1 — Pilot kickoff (Ops + Customer operator, T-0)

- Brief operator in a 30-minute walk-through using a test sandbox
  document.
- Confirm the **More → Create matching Return Receipt** button is
  visible on the CN detail page.
- Confirm the Return Receipt list page and detail page render.
- Confirm the Q4 pre-fill from CN actually pre-fills the line grid.

### Step 2 — First live cycle (Customer operator, T+0.5d)

Operator runs the full sequence on a real small stock return (1
item, low value):

1. Draft CN → expect: status `draft`, CN line carries
   `OriginalInvoiceLineID`.
2. Create matching Return Receipt → expect: form pre-filled,
   warehouse defaulted.
3. Post Return Receipt → expect:
   - Status → `posted`.
   - `inventory_movements` row with
     `source_type='ar_return_receipt'`.
   - JE visible with Dr Inventory / Cr COGS at original cost.
4. Post CN → expect:
   - Status → `issued` (or `fully_applied` if auto-applied to a
     linked invoice).
   - JE visible with Dr Revenue / Cr AR ONLY (no Inventory /
     COGS touch).
5. Verify inventory balance reflects the return quantity.

### Step 3 — Scaled live traffic (T+1d onward)

- Operator runs the full workflow on real customer returns as they
  come in.
- Daily checks per §5 begin immediately.

---

## 5. Observation window — daily checks (every business day)

Run each at 09:00 customer-TZ. Any non-zero / non-expected result
triggers the escalation path.

### Check 1 — Rule #4 invariant failures (hard stop)

```sql
-- Scan the last 24h of application logs for any rule4 violation.
-- Searches vary by log aggregator; shape the query as:
--   level=error AND message LIKE '%rule4 violation%'
--   AND company_id = :pilot_company_id
--   AND timestamp > now() - interval '24 hours'
```

Expected: **zero rows**. Any row = immediate Engineering
escalation and pause the pilot.

### Check 2 — Balance invariant

```sql
-- Compare movement sum to balances for items with recent ARR
-- activity.
WITH active_items AS (
    SELECT DISTINCT arrl.product_service_id AS item_id, arr.warehouse_id
    FROM ar_return_receipts arr
    JOIN ar_return_receipt_lines arrl ON arrl.ar_return_receipt_id = arr.id
    WHERE arr.company_id = :pilot_company_id
      AND arr.created_at > now() - interval '24 hours'
)
SELECT ai.item_id, ai.warehouse_id,
       (SELECT COALESCE(SUM(quantity_delta), 0)
        FROM inventory_movements
        WHERE company_id = :pilot_company_id
          AND item_id = ai.item_id
          AND warehouse_id = ai.warehouse_id) AS mov_sum,
       (SELECT COALESCE(quantity_on_hand, 0)
        FROM inventory_balances
        WHERE company_id = :pilot_company_id
          AND item_id = ai.item_id
          AND warehouse_id = ai.warehouse_id) AS on_hand
FROM active_items ai;
-- Expected: mov_sum == on_hand for every row.
```

Any drift = Engineering escalation.

### Check 3 — Double-count guard

```sql
-- For every ar_return_receipt posted in the last 24h, verify there
-- is NO credit_note-sourced movement for the same CN line.
SELECT arr.id AS arr_id, arrl.credit_note_line_id, cnl.credit_note_id
FROM ar_return_receipts arr
JOIN ar_return_receipt_lines arrl ON arrl.ar_return_receipt_id = arr.id
JOIN credit_note_lines cnl ON cnl.id = arrl.credit_note_line_id
WHERE arr.company_id = :pilot_company_id
  AND arr.status = 'posted'
  AND arr.created_at > now() - interval '24 hours'
  AND EXISTS (
      SELECT 1 FROM inventory_movements im
      WHERE im.company_id = arr.company_id
        AND im.source_type = 'credit_note'
        AND im.source_id = cnl.credit_note_id
  );
-- Expected: empty. Any row = double-count bug.
```

### Check 4 — Coverage shortfall backlog

```sql
-- Same query as Gate 2 — but run daily to see if the backlog is
-- growing.
-- Healthy signal: count stable or decreasing.
-- Unhealthy signal: count growing day-over-day — operator
-- confusion; schedule a refresher session.
```

### Check 5 — Return Receipt → CN post lag

```sql
SELECT arr.id AS arr_id, arr.posted_at AS arr_posted,
       cn.id AS cn_id,
       cn.issued_at AS cn_issued,
       EXTRACT(EPOCH FROM (cn.issued_at - arr.posted_at)) / 3600 AS lag_hours
FROM ar_return_receipts arr
JOIN credit_notes cn ON cn.id = arr.credit_note_id
WHERE arr.company_id = :pilot_company_id
  AND arr.status = 'posted'
  AND cn.status IN ('issued', 'partially_applied', 'fully_applied')
  AND arr.posted_at > now() - interval '7 days'
ORDER BY lag_hours DESC
LIMIT 20;
-- Expected: most lags < 2 business days.
-- Investigate any > 7 days — possible operator confusion.
```

---

## 6. Weekly review (every Friday EOB)

Roll up the daily checks, plus:

- Count of posted Return Receipts in the week (volume signal).
- Count of voided Return Receipts (should be rare; any pattern =
  operator training issue).
- Count of Credit Notes posted under controlled mode with
  stock-item lines (primary success metric).
- Customer operator feedback — qualitative via a 15-minute sync.

---

## 7. Pass / fail criteria

### Pass (ALL required)

1. Three business weeks of clean daily checks (zero Rule #4
   violations, zero balance drift, zero double-counts).
2. At least five Credit Notes posted end-to-end via the Return
   Receipt workflow.
3. Coverage-shortfall backlog at end of observation window ≤
   backlog at start (no growing debt).
4. Customer operator signs off on the workflow.
5. No escalations requiring `shipment_required=false` rollback.

### On pass

- Company is considered I.6a-stable.
- Pilot flag in the internal CS tracker flips to "stable".
- CS can onboard the next pilot candidate (respecting Q9 —
  different company, not another layer on the same one).

### On fail

- `shipment_required=false` per §3 Gate 5.
- Post-mortem within one week of rollback.
- Root cause filed as a bug or runbook gap; fix before re-trying.
- Do NOT retry the same company's pilot until the root cause is
  fixed AND the fix has been validated on a different company.

---

## Change log

| Date | Change |
|---|---|
| 2026-04-21 | Initial draft — I.6a.5. Models the Phase H / Phase I pilot playbooks but scoped to the AR return layer. |
