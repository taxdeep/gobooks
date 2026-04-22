# Phase I.6b Pilot Enablement — AP Returns to Vendor

> **Layered pilot on top of Phase H.** I.6b does NOT need its own
> `receipt_required` flip — that happens in the main Phase H pilot
> (see `PHASE_H_PILOT_ENABLEMENT.md`). This playbook covers the
> **layered** pilot where a Phase-H-green company starts actually
> using AP stock-return Vendor Credit Notes under controlled mode —
> which only became possible when I.6b.3 shipped the VCN retrofit.
>
> Before I.6b, Phase-H-green companies that needed stock returns to
> a vendor had to drop `receipt_required=false` temporarily, run the
> IN.6a legacy path, and flip back. I.6b closes that gap.

---

## 1. Purpose / Audience / Non-goals

### Purpose

Guide CS + Engineering through enabling I.6b behavior on a specific
pilot company: the company is already `receipt_required=true`
(Phase H pilot-green) and now begins exercising the stock-return
Vendor Credit Note path that I.6b.3 enabled, with the
`VendorReturnShipment` (UI label "Return to Vendor") physical-truth
document as the Rule #4 movement owner.

### Audience

- **CS ops lead** — schedules the pilot, owns customer
  communication, runs the daily checklist.
- **Engineering on-call** — Rule #4 invariant failures, traced-cost
  trace misses, `IssueVendorReturn` edge cases.
- **Pilot customer's AP operator(s)** — trained on
  `PHASE_I6_RUNBOOK.md` AP body (operator workflow §§9–12).

### Non-goals

- NOT about flipping `receipt_required` ON — that's Phase H's pilot.
- NOT about the AR side (I.6a `ARReturnReceipt`) — covered by
  `PHASE_I6A_PILOT_ENABLEMENT.md`.
- NOT a global "enablement" — no capability flag flips. The code
  path activates automatically for any `receipt_required=true`
  company once I.6b.3 shipped. This playbook governs the
  **operational rollout** per company.

---

## 2. Pilot candidate selection criteria

A company is a **good I.6b pilot candidate** when ALL hold:

1. **Phase H pilot-green.** `receipt_required=true` stable for at
   least one full business week, zero GR/IR reconciliation anomalies,
   zero Rule #4 invariant escalations.
2. **Real stock-return-to-vendor volume.** Historical VCN volume
   with stock-item lines (or pre-existing operator workflow of
   manual JEs for vendor returns that can now migrate to VCN+VRS).
3. **Fully-trained AP operator team.** The Bill → VCN draft → VRS
   post → VCN post sequence is new; operators must understand each
   step, especially the Q6 exact-coverage rule and Q3's implication
   that partial returns work via multiple VRS summing per line.
4. **NOT in a Q9 pilot-stacking window.** If the company is ALSO
   in an active I.6a pilot observation window (AR side), **delay
   I.6b pilot on that company** until I.6a stabilises. Two
   controlled-mode return pilots on one company at the same time
   pollute incident attribution — charter Q9 operational rule.
5. **NOT in an active Phase I pilot.** Similarly, if
   `shipment_required` is still in its observation window, defer
   I.6b. Two rail-pilot windows on one company simultaneously is
   the same Q9 issue applied differently.

A company is **NOT** a good candidate when ANY hold:

- Active I.6a or Phase I pilot window.
- High volume of pre-IN.6a VCNs (header-only) without
  `OriginalBillLineID` trace data — back-data blocks VRS creation
  against historical VCNs.
- Heavy tracked-lot/serial purchases with return potential (I.6b
  scope-out; documented in `PHASE_I6_RUNBOOK.md` §5 item #1).

---

## 3. Pre-flight checklist

### Gate 1 — Phase H rail is green on this company

```sql
SELECT id, name, receipt_required, shipment_required
FROM companies
WHERE id = :pilot_company_id;
-- receipt_required MUST be true; shipment_required informational.
```

And per `PHASE_H_PILOT_ENABLEMENT.md` §7 exit criteria, the main
Phase H pilot for this company must have passed.

### Gate 2 — Zero pending VCN coverage shortfalls

```sql
-- Any draft VCN under controlled mode with stock-item lines that
-- would currently fail the Q6 coverage check at post time? Clear
-- or understand before starting pilot.
SELECT vcn.id, vcn.credit_note_number, vcnl.id AS vcn_line_id,
       vcnl.qty AS vcn_qty,
       COALESCE(SUM(vrsl.qty), 0) AS posted_vrs_coverage
FROM vendor_credit_notes vcn
JOIN vendor_credit_note_lines vcnl ON vcnl.vendor_credit_note_id = vcn.id
JOIN product_services ps ON ps.id = vcnl.product_service_id AND ps.is_stock_item
LEFT JOIN vendor_return_shipment_lines vrsl ON vrsl.vendor_credit_note_line_id = vcnl.id
LEFT JOIN vendor_return_shipments vrs ON vrs.id = vrsl.vendor_return_shipment_id AND vrs.status = 'posted'
WHERE vcn.company_id = :pilot_company_id
  AND vcn.status = 'draft'
GROUP BY vcn.id, vcn.credit_note_number, vcnl.id, vcnl.qty
HAVING vcnl.qty != COALESCE(SUM(vrsl.qty), 0);
-- Empty result set = clean baseline.
```

### Gate 3 — Operator training complete

- AP operator has read `PHASE_I6_RUNBOOK.md` AP body (§§9–12 when
  published; until then, this playbook's §4 doubles as the
  operator workflow reference).
- Operator can articulate the difference between:
  - **Partial return** (qty < original Bill qty — use one VRS
    with smaller qty; VCN post requires matching coverage).
  - **Split return** (full qty arrives in multiple physical
    shipments — multiple VRS summing to VCN line qty).
- Sandbox dry-run completed.

### Gate 4 — CS + Engineering coverage

- CS ops lead assigned for 3-week observation window.
- Engineering on-call rotation with named Phase I.6 SME as first
  escalation.
- Traced-cost lineage queries from PHASE_I6_RUNBOOK §7 escalation
  list are accessible (Engineering can pull original Bill movement
  for a given VCN line on demand).

### Gate 5 — Rollback plan

- **Rollback is NOT a code flip.** I.6b cannot be turned off once
  shipped.
- **Per-company rollback** = flip `receipt_required=false` on the
  pilot company. Reverts to IN.6a legacy VCN-forms-inventory path
  and bypasses VRS entirely. Any draft VRSs the operator has in
  flight become inert (can be deleted).
- `PHASE_H_PILOT_ENABLEMENT.md` §6 documents the rollback procedure;
  follow it exactly. Note: posted VRSs prior to rollback remain in
  the ledger as completed return events; they do NOT get unwound
  by the rollback.

---

## 4. Enablement workflow

### Step 1 — Pilot kickoff (Ops + Customer operator, T-0)

- 30-minute walkthrough using a sandbox document.
- Confirm **More → Create Return to Vendor** button visible on VCN
  detail pages with stock-item lines.
- Confirm `/vendor-return-shipments` list + detail pages render.
- Confirm Q4 pre-fill actually pre-fills the line grid from VCN
  stock-item lines.

### Step 2 — First live cycle (Customer operator, T+0.5d)

Operator runs the full sequence on a real small stock return (1
item, low value):

1. Draft VCN linked to the original Bill. Per stock line, set
   `OriginalBillLineID`.
2. On VCN detail → More → **Create Return to Vendor**. Form
   pre-fills; warehouse defaulted.
3. Adjust VRS qty if needed (partial return scenario), save draft.
4. Post VRS. Expect:
   - Status → `posted`.
   - `inventory_movements` row with
     `source_type='vendor_return_shipment'` and
     `movement_type='vendor_return'`.
   - Cost = original Bill's `unit_cost_base` (traced).
   - JE: `Dr AP / Cr Inventory` at traced qty × cost.
5. Post VCN. Expect:
   - Coverage check passes (Σ VRS = VCN line qty).
   - If VCN is stock-only at traced cost: **NO JE produced**
     (VRS owned everything).
   - Otherwise: Dr AP / Cr Offset for the non-stock portion.
   - Status → `posted`.
6. Verify inventory balance reflects the outflow.

### Step 3 — Scaled live traffic (T+1d onward)

- Operator runs the workflow on real vendor returns.
- Daily checks per §5 begin immediately.

---

## 5. Observation window — daily checks (every business day)

Run at 09:00 customer-TZ.

### Check 1 — Rule #4 invariant failures (hard stop)

```
Search application logs: level=error AND message LIKE '%rule4 violation%'
AND company_id = :pilot_company_id AND timestamp > now() - 24h
```

Expected: **zero rows**. Any row = immediate Engineering
escalation, pause the pilot.

### Check 2 — Balance invariant for items with VRS activity

```sql
WITH active_items AS (
    SELECT DISTINCT vrsl.product_service_id AS item_id, vrs.warehouse_id
    FROM vendor_return_shipments vrs
    JOIN vendor_return_shipment_lines vrsl ON vrsl.vendor_return_shipment_id = vrs.id
    WHERE vrs.company_id = :pilot_company_id
      AND vrs.created_at > now() - interval '24 hours'
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
-- Expected: mov_sum == on_hand per row. Drift = escalation.
```

### Check 3 — Traced-cost correctness (spot check)

For each VRS posted in the last 24h, the VRS movement's
`unit_cost_base` must equal the referenced Bill movement's
`unit_cost_base`. A drift here means the narrow verb lost its
authoritative cost.

```sql
SELECT vrs.id AS vrs_id,
       vrs_mov.unit_cost_base AS vrs_cost,
       bill_mov.unit_cost_base AS bill_cost,
       vrs_mov.unit_cost_base = bill_mov.unit_cost_base AS cost_match
FROM vendor_return_shipments vrs
JOIN vendor_return_shipment_lines vrsl ON vrsl.vendor_return_shipment_id = vrs.id
JOIN vendor_credit_note_lines vcnl ON vcnl.id = vrsl.vendor_credit_note_line_id
JOIN bill_lines bl ON bl.id = vcnl.original_bill_line_id
JOIN inventory_movements vrs_mov ON vrs_mov.company_id = vrs.company_id
    AND vrs_mov.source_type = 'vendor_return_shipment'
    AND vrs_mov.source_id = vrs.id
    AND vrs_mov.source_line_id = vrsl.id
JOIN inventory_movements bill_mov ON bill_mov.company_id = vrs.company_id
    AND bill_mov.source_type = 'bill'
    AND bill_mov.source_line_id = bl.id
WHERE vrs.company_id = :pilot_company_id
  AND vrs.status = 'posted'
  AND vrs.created_at > now() - interval '24 hours';
-- Expected: every cost_match = true.
```

### Check 4 — Double-count guard

```sql
-- For every VRS posted in the last 24h, verify there is NO
-- vendor_credit_note-sourced movement for the same VCN line (the
-- paired VCN surrendered ownership).
SELECT vrs.id AS vrs_id, vrsl.vendor_credit_note_line_id, vcnl.vendor_credit_note_id
FROM vendor_return_shipments vrs
JOIN vendor_return_shipment_lines vrsl ON vrsl.vendor_return_shipment_id = vrs.id
JOIN vendor_credit_note_lines vcnl ON vcnl.id = vrsl.vendor_credit_note_line_id
WHERE vrs.company_id = :pilot_company_id
  AND vrs.status = 'posted'
  AND vrs.created_at > now() - interval '24 hours'
  AND EXISTS (
      SELECT 1 FROM inventory_movements im
      WHERE im.company_id = vrs.company_id
        AND im.source_type = 'vendor_credit_note'
        AND im.source_id = vcnl.vendor_credit_note_id
  );
-- Expected: empty. Any row = double-count bug.
```

### Check 5 — Coverage shortfall backlog trend

Same query as Gate 2 — run daily.
Healthy: count stable / decreasing. Unhealthy: count growing →
operator confusion.

### Check 6 — VRS → VCN post lag

```sql
SELECT vrs.id AS vrs_id, vrs.posted_at AS vrs_posted,
       vcn.id AS vcn_id, vcn.posted_at AS vcn_posted,
       EXTRACT(EPOCH FROM (vcn.posted_at - vrs.posted_at)) / 3600 AS lag_hours
FROM vendor_return_shipments vrs
JOIN vendor_credit_notes vcn ON vcn.id = vrs.vendor_credit_note_id
WHERE vrs.company_id = :pilot_company_id
  AND vrs.status = 'posted'
  AND vcn.status IN ('posted', 'partially_applied', 'fully_applied')
  AND vrs.posted_at > now() - interval '7 days'
ORDER BY lag_hours DESC LIMIT 20;
-- Expected: most lags < 2 business days.
```

---

## 6. Weekly review (every Friday EOB)

Roll up daily checks, plus:

- Count of posted VRSs + associated posted VCNs.
- Count of voided VRSs (rare; investigate patterns).
- Count of partial-qty vendor returns via split VRS — the
  previously-deferred IN.6a gap should now see real volume.
- Qualitative operator feedback sync.

---

## 7. Pass / fail criteria

### Pass (ALL required)

1. Three weeks of clean daily checks.
2. ≥ 5 VCNs posted end-to-end via VRS workflow.
3. ≥ 1 partial-qty AP return successfully posted (validates
   Q3-keystone and IN.6a gap closure).
4. Coverage-shortfall backlog ≤ starting baseline.
5. Operator sign-off.
6. No `receipt_required=false` rollback.

### On pass

- Company is I.6b-stable.
- CS can onboard next candidate (respecting Q9 — different
  company).

### On fail

- `receipt_required=false` per §3 Gate 5.
- Post-mortem within one week.
- Root cause filed, fixed, validated on a different company
  before retrying.

---

## Change log

| Date | Change |
|---|---|
| 2026-04-21 | Initial draft — I.6b.5 final slice of Phase I.6. Mirrors `PHASE_I6A_PILOT_ENABLEMENT.md` scoped to AP side. |
