# Phase G — Staging Smoke Script

Verifies that the Phase G merge (slices G.1–G.5) behaves correctly on
staging in BOTH its positive path AND its guarded negative paths. Per
the addendum rule: *"smoke is pass + fail-guard verification, not just
happy-path proof."*

Green on this script is one of the three hard entry gates for Phase H.

---

## 0. Pre-flight

Confirm each item before running the scenarios. Do not improvise around
any missing item.

- [ ] Migrations `061` → `067` applied to staging **in order**. Verify
  with:
  ```sql
  SELECT migration_name FROM schema_migrations ORDER BY migration_name DESC LIMIT 10;
  ```
  The last three should include `067_bill_lines_tracking`,
  `066_company_tracking_enabled`, `065_inventory_tracking_consumption`.
- [ ] Isolated staging test identity available (dedicated company or
  clearly-marked test company). **Do not run against a staging company
  that also carries live customer data.**
- [ ] Service layer reachable — either via staging admin CLI, a test
  harness invoking `services.*`, or a throwaway Go binary. SQL-only
  execution is NOT sufficient: several checks require service calls
  (`ChangeTrackingMode`, `PostBill`, `ValidateStockForInvoice`) because
  gate + audit + validation logic lives there, not in SQL.
- [ ] Ability to roll back staging DB (snapshot / restore point) in
  case a scenario leaves partial state.

Staging smoke is **pass/fail as a whole**. A partial pass is a fail.

---

## 1. Scenario A — lot-tracked Bill inbound succeeds end-to-end (POSITIVE)

### Setup

| # | Action | Notes |
|---|---|---|
| A.1 | Create test company `COMPANY_ID` (or reuse) | Keep ID handy for later queries |
| A.2 | `services.ChangeCompanyTrackingCapability({CompanyID: COMPANY_ID, Enabled: true, Actor: "smoke"})` | Must go through service — direct SQL bypasses the audit |
| A.3 | Create a Revenue `Account`, then a stock `ProductService` linked to it. Keep `ITEM_ID`. | Default `tracking_mode` is `'none'`; that's fine for now |
| A.4 | `services.ChangeTrackingMode({CompanyID, ItemID, NewMode: "lot"})` | Should succeed; the G.1 gate is now open |
| A.5 | Create a `Vendor`. Keep `VENDOR_ID`. | |
| A.6 | Create a `Bill` (status=`draft`) with one `BillLine`: `item=ITEM_ID`, `qty=10`, `unit_price=5.00`, `lot_number="SMOKE-A-LOT"`, `lot_expiry_date="2027-12-31"` | Keep `BILL_ID` |
| A.7 | `services.PostBill(db, COMPANY_ID, BILL_ID, "smoke", nil)` | Expected: returns `nil` |

### Verification SQL

```sql
-- 1) Bill posted.
SELECT status FROM bills WHERE id = :BILL_ID;
-- PASS if status = 'posted'

-- 2) One inventory movement, correct shape.
SELECT quantity_delta, source_type, source_id, unit_cost_base
FROM inventory_movements
WHERE company_id = :COMPANY_ID AND item_id = :ITEM_ID;
-- PASS if exactly 1 row AND quantity_delta = 10 AND source_type = 'bill'
--   AND source_id = :BILL_ID AND unit_cost_base = 5.0000

-- 3) Balance reflects the receipt.
SELECT quantity_on_hand, average_cost
FROM inventory_balances
WHERE company_id = :COMPANY_ID AND item_id = :ITEM_ID;
-- PASS if quantity_on_hand = 10 AND average_cost = 5.0000

-- 4) Lot materialised with the bill line's tracking data.
SELECT lot_number, original_quantity, remaining_quantity, expiry_date
FROM inventory_lots
WHERE company_id = :COMPANY_ID AND item_id = :ITEM_ID;
-- PASS if exactly 1 row AND lot_number = 'SMOKE-A-LOT'
--   AND original_quantity = 10 AND remaining_quantity = 10
--   AND expiry_date = '2027-12-31'

-- 5) FIFO cost layer written (E2 invariant; applies under every costing method).
SELECT original_quantity, remaining_quantity, provenance_type, is_synthetic
FROM inventory_cost_layers
WHERE company_id = :COMPANY_ID AND item_id = :ITEM_ID;
-- PASS if exactly 1 row AND original_quantity = 10 AND remaining_quantity = 10
--   AND provenance_type = 'receipt' AND is_synthetic = FALSE
```

### Pass criteria
All 5 queries return the expected values.

### Fail modes worth calling out
- **Missing `inventory_lots` row** → G.4 forwarding broke; hotfix needed
- **`source_type != 'bill'`** → facade wiring regressed
- **`is_synthetic = TRUE`** on a fresh receipt → provenance path is wrong; do not proceed

---

## 2. Scenario B — serial-via-Bill must fail loudly (NEGATIVE GUARD)

### Setup

| # | Action | Notes |
|---|---|---|
| B.1 | Reuse `COMPANY_ID` from A (already has `tracking_enabled=true`) | |
| B.2 | Create a second stock `ProductService`, call `ChangeTrackingMode({…, NewMode: "serial"})`. Keep `SERIAL_ITEM_ID`. | |
| B.3 | Create a `Bill` (status=`draft`) with one line: `item=SERIAL_ITEM_ID`, `qty=1`, `unit_price=100.00`, **no serial numbers** (BillLine has no serial capture — that's the point of the guard) | Keep `BILL_B_ID` |
| B.4 | `services.PostBill(db, COMPANY_ID, BILL_B_ID, "smoke", nil)` | **Expected: non-nil error that `errors.Is(err, inventory.ErrTrackingDataMissing)` unwraps to true** |

### Verification SQL

```sql
-- 1) Bill did NOT post.
SELECT status FROM bills WHERE id = :BILL_B_ID;
-- PASS if status = 'draft'

-- 2) No inventory movement for this serial item.
SELECT COUNT(*) FROM inventory_movements
WHERE company_id = :COMPANY_ID AND item_id = :SERIAL_ITEM_ID;
-- PASS if count = 0

-- 3) No serial units created.
SELECT COUNT(*) FROM inventory_serial_units
WHERE company_id = :COMPANY_ID AND item_id = :SERIAL_ITEM_ID;
-- PASS if count = 0
```

### Pass criteria
- `PostBill` surfaced an error wrapping `ErrTrackingDataMissing`
- Bill stayed `draft`
- Zero side-effects

### Fail modes worth calling out
- **Bill becomes `posted`** → guard evaporated; critical regression, STAGING RED
- **Error surfaced but is a different sentinel** → upstream behaviour changed; investigate before declaring pass
- **Any inventory_movements / inventory_serial_units row appears** → tx didn't roll back; critical

---

## 3. Scenario C — tracked-invoice preview must reject (NEGATIVE GUARD)

### Setup

| # | Action | Notes |
|---|---|---|
| C.1 | Reuse `COMPANY_ID` and `ITEM_ID` from A (lot-tracked, has `quantity_on_hand = 10`) | |
| C.2 | Create a `Customer`. Keep `CUSTOMER_ID`. | |
| C.3 | Create an `Invoice` (status=`draft`) with one line: `product_service=ITEM_ID`, `qty=1`, `unit_price=50.00` | Keep `INVOICE_ID` |
| C.4 | Call `services.ValidateStockForInvoice(db, COMPANY_ID, invoice.Lines, nil)` | Expected: non-nil error that `errors.Is(err, services.ErrTrackedItemNotSupportedByInvoice)` unwraps to true |
| C.5 | Also attempt `services.PostInvoice(db, COMPANY_ID, INVOICE_ID, "smoke", nil)` | Should surface the same sentinel; invoice should stay `draft` |

### Verification SQL

```sql
-- 1) Invoice did NOT post.
SELECT status FROM invoices WHERE id = :INVOICE_ID;
-- PASS if status IN ('draft', 'issued') — whichever the pre-post state was.
-- Must NOT be 'posted'.

-- 2) No sale-source inventory movement.
SELECT COUNT(*) FROM inventory_movements
WHERE company_id = :COMPANY_ID AND item_id = :ITEM_ID AND source_type = 'invoice';
-- PASS if count = 0

-- 3) No JE created for this invoice.
SELECT COUNT(*) FROM journal_entries
WHERE company_id = :COMPANY_ID AND source_type = 'invoice' AND source_id = :INVOICE_ID;
-- PASS if count = 0

-- 4) Lot remaining unchanged from A (no partial draw).
SELECT remaining_quantity FROM inventory_lots
WHERE company_id = :COMPANY_ID AND item_id = :ITEM_ID;
-- PASS if remaining_quantity = 10  (unchanged from Scenario A)
```

### Pass criteria
- `ValidateStockForInvoice` returned `ErrTrackedItemNotSupportedByInvoice`
- `PostInvoice` did not succeed
- Invoice did not transition to `posted`
- Zero inventory / JE side-effects
- Lot remaining still 10

### Fail modes worth calling out
- **Invoice posts successfully** → G.2 guard dead; serious regression
- **Lot remaining drops to 9** → side-effect leaked despite error; tx wrapping broken

---

## 4. Sanity checks (fast, cross-cutting)

Run after the three main scenarios. Any failure here is equivalent to a
main-scenario failure.

### Sanity S1 — capability gate OFF blocks mode change

| # | Action | Expected |
|---|---|---|
| S1.1 | Create fresh company `COMPANY_S1_ID`, do NOT enable tracking | `tracking_enabled = FALSE` by default |
| S1.2 | Create a stock `ProductService` | |
| S1.3 | `services.ChangeTrackingMode({…, NewMode: "lot"})` | Returns `ErrTrackingCapabilityNotEnabled` |
| S1.4 | `SELECT tracking_mode FROM product_services WHERE id = :ID` | `'none'` (unchanged) |

### Sanity S2 — disable blocked while tracked items exist

| # | Action | Expected |
|---|---|---|
| S2.1 | Reuse `COMPANY_ID` from Scenario A (has tracked items) | |
| S2.2 | `services.ChangeCompanyTrackingCapability({CompanyID, Enabled: false, Actor: "smoke"})` | Returns `ErrTrackingCapabilityHasTrackedItems` |
| S2.3 | `SELECT tracking_enabled FROM companies WHERE id = :COMPANY_ID` | `TRUE` (unchanged) |

---

## 5. Overall pass / fail

| Result | Meaning | Next action |
|---|---|---|
| All 3 scenarios + 2 sanity checks pass | Staging gate cleared | Publish runbook; then Phase H trigger is green-lit |
| Any scenario fails | Staging RED | Investigate; file correctness hotfix slice per addendum rule 4. **No feature work permitted under hotfix label.** |
| Migration fails to apply | Staging RED before scenarios run | Roll back migration, snapshot-restore, investigate migration in isolation |

---

## 6. Cleanup (optional)

If the staging company was created fresh for this smoke, clean up when
done:

```sql
-- WARNING: run ONLY on isolated staging test company.
-- Order matters — child tables first.
DELETE FROM inventory_tracking_consumption WHERE company_id = :COMPANY_ID;
DELETE FROM inventory_layer_consumption    WHERE company_id = :COMPANY_ID;
DELETE FROM inventory_cost_layers          WHERE company_id = :COMPANY_ID;
DELETE FROM inventory_serial_units         WHERE company_id = :COMPANY_ID;
DELETE FROM inventory_lots                 WHERE company_id = :COMPANY_ID;
DELETE FROM inventory_movements            WHERE company_id = :COMPANY_ID;
DELETE FROM inventory_balances             WHERE company_id = :COMPANY_ID;
DELETE FROM bill_lines                     WHERE company_id = :COMPANY_ID;
DELETE FROM bills                          WHERE company_id = :COMPANY_ID;
DELETE FROM invoice_lines                  WHERE company_id = :COMPANY_ID;
DELETE FROM invoices                       WHERE company_id = :COMPANY_ID;
DELETE FROM journal_lines                  WHERE company_id = :COMPANY_ID;
DELETE FROM journal_entries                WHERE company_id = :COMPANY_ID;
DELETE FROM audit_logs                     WHERE company_id = :COMPANY_ID;
DELETE FROM product_services               WHERE company_id = :COMPANY_ID;
DELETE FROM accounts                       WHERE company_id = :COMPANY_ID;
DELETE FROM vendors                        WHERE company_id = :COMPANY_ID;
DELETE FROM customers                      WHERE company_id = :COMPANY_ID;
DELETE FROM companies                      WHERE id = :COMPANY_ID;
```

Alternative: keep the company marked `smoke-YYYY-MM-DD` so the next
smoke can reuse the same identity scope.

---

## 7. If staging fails

Per the addendum rule 4, failures are treated as **correctness hotfix
slices only**.

- Isolate the exact failing assertion (which query, which expected
  value, which actual value)
- File the hotfix with a narrow scope that touches only the failing
  code path
- Re-run the full smoke (not just the previously-failing scenario) —
  hotfixes sometimes reveal neighbouring regressions
- **Do not bundle "while we're in there" enhancements**. Any expansion
  is a separate slice with its own approval

Staging smoke is a gate, not a feature-planning opportunity.
