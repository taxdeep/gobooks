# Phase D — Overall Review

Review covers the inventory bounded-context work landed across Phase D.0
(slices 1–8), D.1 (BOM read queries), D.2 (build orchestrator) and the
final cleanup pass.

---

## Status — what shipped

**API contract** (`internal/services/inventory`)
- 7 IN verbs: `ReceiveStock`, `IssueStock`, `AdjustStock`, `TransferStock`,
  `ReverseMovement`, `PostInventoryBuild`, plus `ReserveStock`/`ReleaseStock`
  type stubs (Phase E).
- 7 OUT queries: `GetOnHand`, `GetMovements`, `GetItemLedger`,
  `GetValuationSnapshot`, `GetCostingPreview`, `ExplodeBOM`,
  `GetAvailableForBuild`.
- 58 tests, all green. ~5,100 LOC including tests.

**Schema**
- `056_inventory_movement_api_fields.sql` — added currency / cost / idempotency / actor / reversal-link columns plus partial-unique idempotency index.
- `057_drop_inventory_movement_journal_entry_id.sql` — retired the legacy GL reverse coupling.

**Facade migration**
- `CreatePurchaseMovements`, `CreateSaleMovements`,
  `ReverseSaleMovements`, `ReversePurchaseMovements` are now thin
  delegates to the new IN verbs.
- Unused `jeID` parameters removed from all four; 4 call sites updated.
- `ValidateStockForInvoice` rewired off `CostingEngine` onto
  `inventory.GetCostingPreview`. CostingEngine no longer touched by any
  production code — remaining references are test-only.

**Verification**
- `go build ./...` — clean.
- `go test ./internal/services/...` — green (services 14.6s, inventory 0.6s).

---

## Problems & known limitations

Listed in priority order. Each has the file/line where applicable so you can
jump straight to the code and decide.

### P1 — Idempotency keys are version-pinned to `:v1`

**Where:** [inventory_posting.go:248](internal/services/inventory_posting.go:248), [inventory_posting.go:294](internal/services/inventory_posting.go:294), [inventory_reversal.go:113](internal/services/inventory_reversal.go:113)

**Symptom:** if a bill or invoice is voided and then re-posted (a flow we
do not yet exercise but is plausible), the second post will hit the
partial-unique index on `(company_id, idempotency_key)` and fail with
`ErrDuplicateIdempotency`.

**Fix when needed:** before generating the key, query the max suffix in
use for this `(source_type, source_id)` and emit `:v<n+1>`. Cheap, but
requires a small helper.

---

### P2 — COGS preview vs. apply window  *(RESOLVED in Phase E0)*

**Where:** [invoice_post.go](internal/services/invoice_post.go)

`PostInvoice` now runs `CreateSaleMovements` inside the transaction
*before* the JE is created; the returned per-item cost map drives
`BuildCOGSFragments`. JE COGS and `inventory_movements.unit_cost_base`
come from the same `IssueStock` call, so they agree to the cent by
construction. Lock-in test:
`TestPostInvoice_COGSAgreesWithMovementUnitCostBase`. The authoritative-
cost principle is now documented as §2.9 in INVENTORY_MODULE_API.md.

---

### P3 — `PostInventoryBuild` is not transactional internally

**Where:** [build.go](internal/services/inventory/build.go)

**Symptom:** if the 2nd of 3 component issues fails (insufficient stock,
DB error), the 1st issue is already committed. Caller must wrap the
call in `db.Transaction(...)` to get atomic build semantics. Not
documented on the function. Same contract as `TransferStock` but worth
flagging.

**Fix:** add a `// Caller MUST wrap in a tx for atomicity.` line to the
function doc, and ideally add an integration test that exercises the
roll-back path.

---

### P4 — `CostingEngine` still alive as test-only fixture

**Where:** `internal/services/{costing_engine,moving_average_costing}.go`,
`internal/services/phase_{b,c,d,e,f}_inventory_test.go`,
`internal/services/costing_engine_test.go`.

**Symptom:** the legacy engine is no longer called by any production
path, but ~6 test files (~30 call sites) exercise it directly. Those
tests provide value as regression guards on the underlying balance
math, but they write directly to `inventory_balances` /
`inventory_movements` without populating the Phase-D fields
(`currency_code`, `unit_cost_base`, `idempotency_key`). A future test
that mixes both engines could see surprising state.

**Fix:** retire `CostingEngine` in a follow-up cleanup once we've
ported the legacy phase_*_inventory_test.go suites onto the new IN
verbs. ~1 day of work; pure test churn.

---

### P5 — ExplodeBOM cycle detection edge case

**Where:** [queries.go:519](internal/services/inventory/queries.go:519)

**Detail:** the `visited` map is mutated on entry to a sub-tree and
reverted on exit — this lets a component appear in two sibling sub-trees
without spuriously tripping as a cycle. Correct, but the depth cap
(`bomExplodeMaxDepth = 5`) is the real safety net. A pathological
graph with ≥6 nesting levels still gets `ErrBOMTooDeep`, which is the
intended behavior. No bug — just worth confirming the user-facing error
message is friendly when it happens.

---

### P6 — Historical valuation returns zero AverageCost

**Where:** [queries.go:122](internal/services/inventory/queries.go:122), [queries.go:297](internal/services/inventory/queries.go:297)

**Symptom:** `GetOnHand(AsOfDate=...)` and `GetItemLedger` (when ToDate
is in the past) leave `AverageCostBase` and `OpeningUnitCost` at zero
because we don't replay the weighted-average history. Quantity is
correct, value reads zero.

**Fix when needed:** Phase E task — replay movements forward and
maintain a running avg, OR materialize a periodic snapshot table.

---

### P7 — Bundle scrap percentage hardcoded to zero

**Where:** [queries.go:490](internal/services/inventory/queries.go:490)

**Detail:** `ExplodeBOM` returns `ScrapPct = 0` for every row.
`models.ItemComponent` has no scrap column. If we ever introduce one,
this is the single line to update.

---

### P8 — Documentation drift (INVENTORY_MODULE_API.md)

**Where:** repo root `INVENTORY_MODULE_API.md`

**Detail:** the design doc describes a future Build orchestrator but
doesn't capture the Phase D.2 decision to keep the Build event as a
pair of movements (no separate `inventory_builds` table). Should be
updated to document this and to add the `PostInventoryBuild`
signature alongside the other IN verbs.

---

### Non-issue: 4 pre-existing web test failures

`TestHandleBillSaveDraftAndPostFlow`,
`TestJournalEntryPost_BaseImbalanceRejected`,
`TestReportCacheInvalidatedAfterInvoicePostAndVoid`,
`TestReportCacheInvalidatedAfterBillPostAndVoid` were already failing
on the parent commit. Bisected to `84e1da4` which changed the bill
post handler's success redirect from `/bills?posted=1` to
`/bills/%d` without updating tests. Not a Phase D regression — out of
scope for this review.

---

## Recommended next moves

1. **P3 doc fix + transactional wrapper test** — 30 min, prevents
   a class of subtle correctness bugs in future builds.
2. **P1 versioned idempotency keys** — 1–2 hours, before we add a
   "re-post voided document" flow.
3. **P8 doc update** — 30 min while the design is fresh.
4. **P4 retire CostingEngine** — opportunistic; do when the legacy
   phase_* tests next need maintenance.
5. **P2** + **P6** + **P7** — defer to the next slice that touches
   the relevant area; not blocking.
