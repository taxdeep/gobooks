# Phase H Runbook — Receipt-First Inbound & GR/IR Bridge

**Audience:** Customer Success, Operations, Account Management
**Status:** Active after H.5 merge + H-hardening-1 + staging verification
**Supersedes:** `PHASE_G_TRANSITIONAL_RUNBOOK.md` §3 and §4 (customer
decision framework + misuse triage — both were phrased for a
receiving-only world where Receipt did not exist yet).
**Retains:** The Phase G runbook's §7 limitations reference card for
lot / serial / expiry policy. Those policies did not change in Phase H
and G runbook is still the canonical pointer for them.
**Expires:** When Phase I ships (shipment-driven sell-side flow).

This is the authoritative internal reference for what
`receipt_required=true` means right now. Do not produce customer-facing
messaging that contradicts this document.

---

## TL;DR

Phase H added three company-level surfaces on top of Phase G:

- `companies.receipt_required` — capability rail (dormant until
  explicitly flipped per company).
- `companies.gr_ir_clearing_account_id` — liability clearing account.
- `companies.purchase_price_variance_account_id` — P&L variance account.

When `receipt_required=false` (the default, and the state every
company has been in through the entire Phase H rollout), behaviour
is **byte-identical to Phase G**. Nothing changes for customers who
are not explicitly opted in.

When `receipt_required=true`, the company switches to the
**Receipt-first inbound model**:

- **Receipt** becomes the document that records "goods physically
  arrived." Posting a Receipt forms inventory truth **and** books
  `Dr Inventory / Cr GR/IR`.
- **Bill** becomes a purely financial document (AP claim). It no
  longer forms inventory. Posting a Bill books `Dr GR/IR / Cr AP`
  for stock lines — on matched lines at the Receipt's unit cost with
  the difference flowing to **PPV**; on unmatched lines at the Bill's
  unit price (blind clearing).

The **only operational unlock** is H.5. Everything else (UI, bulk
backfill, auto-migration) is intentionally NOT part of Phase H.

---

## 1. When `receipt_required` can be enabled

Technically permitted after H.5 ship + H-hardening-1 lock-in. In
practice, a company must clear **all five** of the following before
the flip is approved:

1. **Catalog readiness.** Every stock-item `ProductService` in the
   company must already have `InventoryAccountID` set. This was
   always required from Phase G onward; it is not new work introduced
   by Phase H, but it is now a **hard** precondition for Receipt post
   — items without it will fail with
   `ErrInboundReceiptInventoryAccountMissing`. Re-check the catalog
   before flipping the rail in case any items changed since the last
   audit. This gate lives at the **item level**, separately from the
   two company-level accounts in gate 2.

2. **Company-level accounts configured and wired.** Two accounts must
   exist in the Chart of Accounts AND be wired to the company via
   their dedicated admin surfaces:
   - A **Liability** account to hold GR/IR accrual (good name:
     `GR/IR Clearing` or `Goods Received Not Invoiced`). Wire with
     `services.ChangeCompanyGRIRClearingAccount(…)` — validates
     Liability root type. Audited as
     `company.gr_ir_clearing_account.set`.
   - An **Expense or Cost-of-Sales** account to absorb PPV (good
     name: `Purchase Price Variance`). Wire with
     `services.ChangeCompanyPPVAccount(…)` — validates P&L root type
     (Expense or CostOfSales). Audited as
     `company.ppv_account.set`.

   These two are **company-level configuration**, distinct from gate
   1's item-level catalog readiness. Both must be persisted on
   `companies.gr_ir_clearing_account_id` and
   `companies.purchase_price_variance_account_id` before step 3 is
   even attempted.

3. **Flip the rail.** `services.ChangeCompanyReceiptRequired(…)`
   with `Required=true`. One audit row written.

4. **Operator training, minimum.** The people posting Bills and
   Receipts in this company must understand:
   - A stock purchase needs **two** documents, not one.
   - Receipt posts first (creates inventory + GR/IR accrual).
   - Bill posts second (clears GR/IR + surfaces PPV).
   - A Bill line can be linked to at most one Receipt line via
     `bill_lines.receipt_line_id`.

5. **Historical context.** Existing `source_type='bill'` inventory
   movements from the pre-flip era remain in the ledger and are
   correct as of that era. They are **not** retroactively migrated
   to `source_type='receipt'`. That's by design — see §5.

Do not flip `receipt_required=true` on a real company before all five
gates are clear. The capability can be flipped back off
(`Required=false`) at any time, but any Bills and Receipts created
while it was on stay exactly as they were posted — voiding and
re-posting is the only supported backout, and see §7 for the
procedure.

---

## 2. What each surface represents under `receipt_required=true`

Say these to teammates. Say these to customers when they ask what
each thing means.

### Receipt

> A **Receipt** records that physical goods arrived at a warehouse.
> Posting it makes stock exist in the system and accrues
> `Dr Inventory / Cr GR/IR`. The Bill has not arrived yet; GR/IR is
> where the debt sits until it does.

Receipts are company-scoped, warehouse-bound, and carry the
line-level unit cost at which the goods were received (what the
vendor charged you for this delivery). Lot tracking captured here —
not on the Bill.

### Bill

> A **Bill** is a purchase invoice from a vendor: the financial
> claim for goods already received. Posting it books
> `Dr GR/IR / Cr AP` — the AP side is the liability to the vendor,
> the GR/IR debit clears (all or part of) what the Receipt accrued.

Bill no longer forms inventory under `receipt_required=true`. If the
Bill's unit price differs from the Receipt's unit cost (the common
case), that difference lands in PPV. If the Bill references no
specific Receipt line, the GR/IR clearing is done "blind" — at the
Bill's own unit price against the company's single GR/IR account.

### GR/IR (Goods Received / Invoice Received) clearing

> **GR/IR** is the liability account that bridges "we got the goods"
> and "we got the invoice." Receipt credits it (accrued liability);
> Bill debits it (liability claimed).

In a perfectly-matched world with identical unit costs, the account
balances to zero per transaction pair. In practice it will carry
balance during normal operation because:

- Receipts arrive before their Bills (Dr Inventory / Cr GR/IR with
  no matching Bill yet).
- Bills arrive for goods not yet received (rare, but see §4).
- Bills and Receipts have price deltas (cleared precisely to PPV
  when matched).
- Some Bills are posted unmatched (the blind-clearing path, §4).

The GR/IR account is **one per company**. No per-vendor or per-book
variation in Phase H.

### PPV (Purchase Price Variance)

> **PPV** absorbs the per-unit dollar difference between what the
> Receipt accrued and what the Bill claims, for the matched portion
> only. It's a P&L account.

- Bill price **higher** than Receipt cost → **Dr PPV** (unfavorable
  variance — the company paid more than expected; expense goes up).
- Bill price **lower** than Receipt cost → **Cr PPV** (favorable
  variance — the company paid less than expected; expense goes down).
- Bill price **equal** to Receipt cost → no PPV posting at all
  (zero-variance short-circuit; no noise lines in the JE).

PPV only appears on **matched** lines. Unmatched Bill lines cannot
produce PPV — there's no Receipt cost to compare to.

---

## 3. Receipt-line matching — how to read it

### The semantics

One Bill line points at **at most one** Receipt line via
`bill_lines.receipt_line_id`. No reverse pointer exists on the
Receipt side; Bill is authoritative.

One Receipt line may be referenced by **multiple** Bill lines over
time — this supports partial settlements, where a single delivery is
billed across multiple invoices. Each additional Bill line takes the
next slice of the Receipt's remaining unmatched quantity.

### What each scenario does to the JE

| Scenario | Receipt | Bill | Matched portion | Unmatched portion | PPV |
|---|---|---|---|---|---|
| Exact match, identical price | 10 @ $5 | 10 @ $5 (→ rl) | 10 × $5 = $50 to Dr GR/IR | 0 | 0 (zero-variance) |
| Exact match, unfavorable | 10 @ $5 | 10 @ $6 (→ rl) | 10 × $5 = $50 to Dr GR/IR | 0 | **Dr PPV $10** |
| Exact match, favorable | 10 @ $8 | 10 @ $5 (→ rl) | 10 × $8 = $80 to Dr GR/IR | 0 | **Cr PPV $30** |
| Partial (first of two) | 10 @ $5 | 6 @ $6 (→ rl) | 6 × $5 = $30 to Dr GR/IR | 0 | Dr PPV $6 |
| Partial (follow-up) | same rl | 4 @ $7 (→ rl) | 4 × $5 = $20 to Dr GR/IR | 0 | Dr PPV $8 |
| Over-match | 10 @ $5 | 12 @ $7 (→ rl) | 10 × $5 = $50 to Dr GR/IR | 2 × $7 = $14 to Dr GR/IR (blind) | Dr PPV $20 (on matched only) |
| Unlinked (no rl) | 10 @ $5 | 10 @ $6 (no rl) | — | 10 × $6 = $60 to Dr GR/IR (blind) | 0 (no baseline to compare) |

### Expected operator workflow

1. Vendor delivers goods; warehouse posts a Receipt with lot /
   expiry / qty / unit cost.
2. Vendor's invoice arrives days/weeks later.
3. Operator creates a Bill, picks the matching Receipt line for each
   stock line (UI for this is **not** in Phase H — see §8 — so until
   the UI ships, the operator edits `bill_lines.receipt_line_id` via
   the direct API / admin tooling).
4. Post the Bill. GR/IR clears cleanly on matched lines; PPV shows
   the per-unit variance.

### What the operator does NOT have to do

- Reconcile quantities by hand. The matcher sums prior posted
  matches against the Receipt line automatically.
- Worry about PPV sign. The service picks Dr / Cr based on the
  variance direction.
- Re-edit past Receipts. The Receipt's `unit_cost` is frozen at
  post time — that's the clearing price.

---

## 4. When blind GR/IR balance is expected (by design)

GR/IR is **not expected to sit at zero every day**. Residual balance
is normal in the following cases, none of which are bugs:

1. **Receipt posted, Bill not yet arrived.** Credit sits on GR/IR
   until the Bill comes. Example: receive goods on the 3rd, vendor
   bills on the 15th. Between those dates the Receipt's $50 credit
   is a real accrued liability.

2. **Bill posted unlinked to a Receipt.** Bill debits GR/IR at its
   own unit price (the blind / H.4 path). If no Receipt ever gets
   linked, the balance sits as a free-standing debit until the
   operator either links retroactively (future feature — not in
   Phase H) or the balance is cleared via a manual journal entry as
   part of period-end adjustments.

3. **Partial Bill against a Receipt.** Receipt accrued $50; Bill
   clears $30 of it. The remaining $20 GR/IR credit is valid — the
   company still owes the vendor $20 against that delivery.

4. **Over-matched Bill.** Bill qty exceeds the Receipt line's
   remaining qty. The matched portion clears at receipt cost; the
   excess qty clears blind at bill price. Common cause: the vendor
   shipped the right goods but in two deliveries the warehouse
   recorded as one Receipt. Operationally correct.

5. **Void of a matched Bill.** The reversal JE debits AP and
   credits GR/IR at the original debit amounts. After a void, the
   Receipt's accrual effectively comes back to GR/IR and is
   available to match against a replacement Bill.

### What does the GR/IR aging report look like?

It does not ship in Phase H. A CS-facing aging report (Receipts with
unmatched credit, Bills with unmatched debit, both time-windowed) is
explicitly backlogged as a future slice. Until then, CS pulls GR/IR
balance via the ledger reports and compares against the Receipt and
Bill lists manually.

---

## 5. Bug vs by-design — triage

Use this table when a customer reports unexpected behaviour.

| Symptom | Bug or by-design? | What to say |
|---|---|---|
| GR/IR has non-zero balance | **By design.** | Normal. See §4 for the five scenarios. |
| PPV account has a credit balance | **By design.** | Favorable variance. Vendor charged less than expected over the aggregate. |
| PPV account has a debit balance | **By design.** | Unfavorable variance. Vendor charged more than expected. |
| Bill posted under flag=true but no inventory movement appeared | **By design.** | Phase H: Bill is financial-only. Inventory lives on the Receipt. |
| Historical `source_type='bill'` inventory movements still exist after flip | **By design.** | Pre-flip history is preserved. Only new Bills posted after the flip skip inventory formation. |
| Bill posted but GR/IR still has balance against the related Receipt | **Almost always by design — diagnose the matching state first.** | Walk three checks before assuming anything is wrong: <br>**(a)** `bill_lines.receipt_line_id` IS NULL → Bill used the blind-clearing path by design; no precise clearing was requested. Operator can link the Receipt line on a follow-up Bill (there is no retro-link in Phase H). <br>**(b)** `receipt_line_id` is set but `bill_line.qty` < `receipt_line.qty − prior_matched` → this is a **partial** match. Only the matched portion cleared; the Receipt's remaining credit is legitimate and awaits a follow-up Bill. <br>**(c)** `bill_line.qty` > `receipt_line.qty − prior_matched` → this is an **over-match**. The matched portion cleared at receipt cost (with PPV); the overflow added its own blind debit (at bill price) to GR/IR. Net residual on the Receipt side is zero; residual on the blind side is the overflow amount, which will stay until a future Bill or manual JE clears it. <br>Only if none of (a)/(b)/(c) explain the residual → escalate per §10. |
| `ErrPPVAccountNotConfigured` on Bill post | **Configuration miss.** | Call `ChangeCompanyPPVAccount`. §1 gate 2. |
| `ErrGRIRAccountNotConfigured` on Bill or Receipt post | **Configuration miss.** | Call `ChangeCompanyGRIRClearingAccount`. §1 gate 2. |
| `ErrBillLineReceiptRefInvalid` | **Operator error.** | Receipt line is either cross-tenant, on a non-posted Receipt, or not a stock line. |
| `ErrInboundReceiptInventoryAccountMissing` | **Catalog error.** | The product has no `InventoryAccountID`. Fix on the product record. |
| Two Bills posted concurrently to the same Receipt line → cumulative match > receipt qty | **BUG. Should not happen post H-hardening-1.** | Escalate to engineering. H-hardening-1 added `FOR UPDATE` to prevent this. A confirmed reproduction means the lock regressed. |
| Receipt with only service lines did not book inventory or JE after post | **By design.** | Service-only receipts have nothing to accrue. Status flips to posted; no side effects. |
| After `ChangeCompanyReceiptRequired(false)`, old Bills still show their H.4/H.5 journal shapes | **By design.** | History is permanent. The flag change only affects future posts. |

---

## 6. Enablement procedure (pilot company)

Step 1. **Confirm the five gates from §1.**
- **Catalog readiness** (gate 1 in §1): every stock item has
  `InventoryAccountID` set. Item-level, not company-level — spot-check
  the catalog with a list query if the company has more than a
  handful of stock items.
- **Company-level accounts exist in the CoA** (gate 2 in §1): a
  Liability account for GR/IR clearing, an Expense or
  Cost-of-Sales account for PPV.
- Operator training done.
- Customer understands this is the Receipt-first model.
- Historical context understood (no retroactive migration of
  pre-flip Bills).

Step 2. **Wire the two company-level accounts.**

This is distinct from gate 1's item-level work in §1. Only the two
accounts listed here are wired via dedicated setters:

- `ChangeCompanyGRIRClearingAccount(companyID, grirAccountID, actor)`.
- `ChangeCompanyPPVAccount(companyID, ppvAccountID, actor)`.
- Verify `audit_logs` captured both with actions
  `company.gr_ir_clearing_account.set` and `company.ppv_account.set`.

If the catalog gate from Step 1 is not yet clean (any stock item
missing `InventoryAccountID`), fix that **before** moving to Step 3
— PostReceipt will fail loud on the first stock line otherwise.

Step 3. **Flip the capability rail.**
- `ChangeCompanyReceiptRequired(companyID, true, actor)`.
- Verify `audit_logs` captured it with action
  `company.receipt_required.enabled`.

Step 4. **Smoke-test with one receive cycle.**
- Receive a single low-risk shipment. Post the Receipt. Confirm:
  - Inventory movement row with `source_type='receipt'`.
  - Journal entry with `Dr Inventory / Cr GR/IR`.
  - Receipt row linked to the JE via `journal_entry_id`.
- Create the matching Bill with `bill_lines.receipt_line_id` set.
  Post it. Confirm:
  - No inventory movement sourced from this Bill.
  - Journal entry with `Dr GR/IR / Cr AP`, and if there's a price
    delta, the appropriate PPV line.
  - Bill row linked to its own JE.

Step 5. **Monitor for two weeks.**
- Daily: eyeball the GR/IR balance. Confirm it moves in the
  expected directions (receipts add credit; bills add debit).
- Daily: eyeball the PPV balance. Small variances are normal; large
  unexpected ones deserve a look at which Bills / Receipts posted.
- Weekly: pull the ledger reports for both accounts. Spot-check a
  handful of entries against the source documents.

Step 6. **Decide on expanding the pilot.**
- If week-2 checks are clean, consider enabling one or two more
  companies with similar purchasing patterns.
- If anything on the §5 triage table turns out to need escalation,
  pause further enablements until it's resolved.

---

## 7. Disablement procedure

A customer who enabled `receipt_required=true` may want to turn it
off. This is supported, with some care.

Step 1. **Understand the implication.**
- Bills and Receipts posted while the flag was ON keep their H.4 /
  H.5 journal shapes forever. Disabling does not rewrite history.
- Future Bills (posted after disable) revert to Phase G behaviour
  — Bill-forms-inventory, `Dr Inventory-Asset / Cr AP`.
- Any in-flight Receipts that haven't been billed yet still work
  fine. Future Bills against them will not reference the
  `receipt_line_id` because the flag is off — the Receipt's GR/IR
  credit would sit indefinitely unless cleared manually via a JE.

Step 2. **Flip the rail back.**
- `ChangeCompanyReceiptRequired(companyID, false, actor)`.
- Audit row written with action `company.receipt_required.disabled`.

Step 3. **Decide on the GR/IR / PPV accounts.**
- The accounts remain configured. They can stay configured
  indefinitely; nothing in the legacy path uses them.
- If the customer wants a clean decommission, clear them with
  `ChangeCompanyGRIRClearingAccount(companyID, nil, actor)` and
  `ChangeCompanyPPVAccount(companyID, nil, actor)`.

Step 4. **GR/IR closeout is operational, not engineering.**

Disablement is allowed regardless of GR/IR balance. A non-zero GR/IR
balance at disable time is, on its own, **not a bug** — it is the
expected state of any company with open receipts awaiting bills, or
open bills posted blind. The disable itself must not be blocked on
this.

Three layers to walk, in order:

1. **Disable is permitted.** `ChangeCompanyReceiptRequired(false)`
   succeeds regardless of GR/IR state. Do not gate it on the
   account balance.

2. **If the customer wants a clean closeout**, reconcile GR/IR
   operationally before or after disable:
   - Post the missing Bills against the outstanding Receipts
     (preferred — it preserves the Phase H audit trail).
   - Or book a manual journal entry to clear the residual to a
     reconciliation account, following the customer's
     period-end practice. This is an **operational** reconciliation
     owned by accounting, not an engineering action.
   - Document the reason on the manual JE memo so the period-end
     review has context.

3. **Escalate to engineering only when:**
   - The residual **cannot be explained** from the open Receipts
     and Bills list (balance doesn't reconcile to document state).
   - An H-hardening invariant appears broken — e.g. cumulative
     matched qty on a receipt line exceeds the receipt line's qty
     (H-hardening-1 lock regression).
   - The customer is asking for retroactive history rewrite
     (pre-flip Bills converted into synthetic Receipts) — this is a
     product decision, not a support ticket.

A non-zero GR/IR balance by itself does not qualify for escalation.
The triage table in §5 and the escalation list in §10 both treat
GR/IR residual as by-design; this step keeps §7 consistent with
them.

---

## 8. Known limitations reference card

Print-ready list for quick CS reference.

**Supported right now:**
- ✅ Receipt as a first-class document (create, post, void)
- ✅ Bill-to-Receipt line-to-line matching via `receipt_line_id`
- ✅ Precise GR/IR clearing on matched lines at receipt unit cost
- ✅ PPV posting on price variance (signed Dr/Cr)
- ✅ Cumulative partial matching — one receipt line, multiple bills
- ✅ Over-match is **tolerated** technically — any excess qty beyond
  the Receipt line's remaining unmatched qty falls back to blind
  GR/IR rather than corrupting inventory truth or rejecting the
  post. This is a safety net, **not** a recommended workflow: the
  clean operational pattern is one Receipt matched by Bill qty ≤
  Receipt qty, with multiple partial Bills if needed. Over-match
  usually signals the warehouse consolidated two physical deliveries
  into one Receipt — if it becomes routine, the fix is on the
  receiving side, not the billing side.
- ✅ Lot-tracked receipts via Receipt (not just via Bill anymore)
- ✅ Void symmetry: voiding a matched Bill reverses its GR/IR debit
  and any PPV, releases the receipt's matched qty for re-matching
- ✅ Disable `receipt_required=true` back to `false` without forcing
  a data migration
- ✅ `receipt_required=false` byte-identical to Phase G

**Unsupported right now** (by design, deferred to later phases):
- ❌ UI surfaces for `receipt_required` / `receipt_line_id` / PPV
  account assignment — admin-tool or API-only in Phase H
- ❌ GR/IR aging report (pull balances via ledger reports today)
- ❌ One Bill line spanning two Receipt lines — one-pointer rule is
  hard; operator must split the Bill line if goods came in two
  deliveries
- ❌ Reverse pointer on Receipt side (lookup: "which Bills claim
  this Receipt?" requires querying `bill_lines WHERE receipt_line_id = ?`)
- ❌ Per-vendor or per-book PPV routing — single company-level account
- ❌ Historical Bill backfill into synthetic Receipts — pre-flip
  history stays as-is
- ❌ Automatic `receipt_required` flipping for new companies — every
  flip is a deliberate admin action
- ❌ Serial-tracked item receipts via Bill (unchanged from G: serial
  items come in via Receipt only)

**Policy defaults locked** (will not change without an explicit
product decision):
- `receipt_required` default = FALSE on every new company
- Single GR/IR account per company
- Single PPV account per company
- PPV accepts Expense OR Cost-of-Sales root types; liability / asset
  / equity are rejected
- ON DELETE SET NULL on `bill_lines.receipt_line_id` FK (defensive;
  in practice Receipt lines on posted Receipts cannot be deleted)

---

## 9. What Phase I will add

Do not commit specific dates. Language for customer expectations:

> "The sell-side flow — Shipment as a first-class document,
> tracked invoicing, and customer returns — is planned as the next
> major inventory phase. Until that ships, selling tracked items
> follows the existing non-tracked invoice path."

High-level Phase I scope (internal, not customer promise):
- Shipment as a first-class document, separate from Invoice
- Invoice amount derived from shipped-eligible quantity, not raw
  Sales-Order qty
- Tracked sales: lot / serial selection at shipment time
- Source identity linkage: SO line ↔ Shipment line ↔ Sales-Issue ↔
  Invoice line for partial / split / return flows
- Customer return workflow: return-receive → inspect → disposition

Tracked transfer and tracked assembly come as dedicated slices
**after** Phase I.

---

## 10. Escalation

Loop in engineering if:

- A customer reports GR/IR over-match (cumulative matched qty
  exceeds the receipt line's qty). Post-H-hardening-1, this should
  be impossible. A real reproduction = regressed lock.
- A Bill post left inventory movements with `source_type='bill'`
  under `receipt_required=true`. H.4's guard should have prevented
  this; reproduction = regressed guard.
- A Receipt post left no inventory movement but flipped status to
  posted under `receipt_required=true` with stock lines. H.3's
  guard should have prevented this.
- `audit_logs` has gaps in the expected capability-flip actions for
  this company (we should see at least one each of the three set
  actions).
- PPV balance direction is counter-intuitive (e.g. a vendor that
  always under-charges producing a Dr rather than Cr PPV). Usually
  a sign error on a single Bill or Receipt; engineering can walk
  the JE trail.
- A customer insists on retroactive history migration (converting
  pre-flip Bills into synthetic Receipts). That's a product
  decision, not a support ticket.

Do not escalate for:
- GR/IR has a non-zero balance — see §4.
- PPV is non-zero — see §2.
- A customer asking "when can my other companies enable this?" —
  answer from §1 (gates, not dates).
- A customer wanting the UI for matching — "not in Phase H; admin /
  API path is the current workflow."

---

## 11. Change log

| Date | Change |
|---|---|
| 2026-04-19 | Initial draft after H.5 merge + H-hardening-1 + staging verification. Supersedes `PHASE_G_TRANSITIONAL_RUNBOOK.md` §3-§4; retains the rest. |
| (future) | Phase I ship → supersede §2-§7 for the sell-side half; rewrite §9. |

---

**One-line summary for CS dashboards:**

*Phase H `receipt_required=true`: Receipt is inventory truth; Bill is
financial claim; GR/IR is the bridge; PPV absorbs the delta on matched
lines. Still opt-in per company, not auto-rolled out. `receipt_required=false`
is byte-identical to Phase G.*
