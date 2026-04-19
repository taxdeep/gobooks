# Phase G Transitional Runbook

**Audience:** Customer Success, Operations, Account Management
**Status:** Active after Phase G merge + staging smoke green
**Expires:** When Phase I ships (shipment-driven sell-side flow)

This is the authoritative internal reference for what
`tracking_enabled=true` means right now. Do not produce
customer-facing messaging that contradicts this document.

---

## TL;DR

Phase G added a new company-level setting, `tracking_enabled`.

When `tracking_enabled=true`, the company enters a **receiving-only
tracking state**. This is **not** partial selling support. It is not
"tracking, mostly." The state is narrow on purpose and will remain
narrow until Phase I ships the sell-side flow.

**What works today:**
- Receiving lot-tracked items via purchase bills

**What does NOT work today** (and will surface a clear error if
attempted):
- Invoicing / selling any tracked item (lot or serial)
- Transferring tracked items between warehouses
- Building / assembling with tracked components
- Receiving serial-tracked items via bills

If a customer needs to sell tracked items today, they should **not**
enable tracking yet. This is a hard answer, not a soft one.

---

## 1. What shipped in Phase G

Five slices, concise form:

| Slice | Impact on customer |
|---|---|
| G.1 | New company setting `tracking_enabled` (default OFF). Only the company admin can flip it. Audit trail on every flip. |
| G.2 | Invoices involving tracked items fail at preview with a clear error message pointing at the future flow (Phase I). |
| G.3 | Bill lifecycle states clarified internally. No customer-visible change. |
| G.4 | Purchase bills for lot-tracked items can now carry a lot number + optional expiry, and the inventory lot is created correctly on post. |
| G.5 | Internal permission naming standard. No customer-visible change. |

Everything else (serial-via-bill, tracked invoicing, tracked transfer,
tracked build) continues to reject by design.

---

## 2. The "receiving-only tracking state" principle

Say this to teammates. Say this to customers when they ask "can I
enable tracking now?"

> **Enabling tracking today means you can *receive* lot-tracked items
> into inventory with lot numbers and expiry dates. You cannot yet
> *sell* tracked items, *transfer* them, or *assemble* with them.
> The sell-side flow lands in a future release.**

Do not soften this with language like:
- ❌ "Tracking is mostly ready" — it is ready on one side only
- ❌ "Most tracking features work" — the counting is misleading; the
  most commercially important feature (selling) does not work
- ❌ "You can try it out" — customers who enable it expecting full
  flow will hit errors on their first invoice

Say this instead:
- ✅ "Tracking's receiving side is ready. Selling with tracking
  lands in a later release. If your main use case is tracking items
  you sell, wait until that release."
- ✅ "If you only receive lot-tracked stock (e.g. food, pharma with
  expiry dates) and your sales still flow through non-tracked items
  today, receiving-side tracking is usable now."

---

## 3. Customer decision framework

Use this decision tree when a customer asks "should I enable tracking?"

```
Does the customer need to SELL tracked items (lot or serial) on invoices?
├── YES → Do NOT enable tracking yet. Wait for the shipment-driven flow.
│         Set expectation: "receiving works now, selling lands in Phase I."
│
└── NO → Does the customer need to TRANSFER tracked items between warehouses?
    ├── YES → Do NOT enable tracking yet. Tracked transfers are not yet wired.
    │
    └── NO → Does the customer need to BUILD / ASSEMBLE tracked items?
        ├── YES → Do NOT enable tracking yet. Tracked assembly is not yet wired.
        │
        └── NO → Does the customer only need to RECEIVE lot-tracked items
                 (e.g. pharma batches with expiry, food batches with lot codes)?
            ├── YES → Enabling tracking is appropriate.
            │         Confirm they understand selling remains in non-tracked flow.
            │
            └── NO (serial-tracked receiving) → Do NOT enable tracking yet.
                 Serial-via-bill is not wired; serialized goods arrive through
                 a dedicated receipt flow that lands with Phase I's document
                 model.
```

---

## 4. What customers will see if they misuse the state

Each row below maps "what the customer tried to do" to "what they'll
see" so CS can triage quickly.

| Customer action | What happens | What to tell them |
|---|---|---|
| Enable tracking on a company | Audit row written; setting flips to ON | Normal |
| Change an item's tracking mode with on-hand > 0 | Error: `cannot change tracking_mode while item has on-hand` | Drain stock first, then flip the mode. No conversion tool exists. |
| Try to disable tracking while any item is still tracked | Error: `cannot disable company tracking capability while any item still has tracking_mode != none` | Set all items back to `none` first. Confirm they understand lots/serials will be abandoned. |
| Receive a lot-tracked item on a bill with lot number filled | Success | Normal |
| Receive a lot-tracked item on a bill WITHOUT lot number | Error: `tracking data missing for tracked item` | Tell them to fill the lot number on the bill line |
| Receive a serial-tracked item on a bill | Error: `tracking data missing for tracked item` | **Not yet supported.** Serial receiving lands with the dedicated receipt flow in Phase I. Customer should keep serial items on tracking_mode=none for now. |
| Try to post an invoice containing any tracked item | Error: `tracked items are not yet supported in the invoice flow; use the shipment-driven path when available (Phase I)` | **Not yet supported.** Tell them the sell-side flow is Phase I. Invoice can still go through if they set the items back to non-tracked, but that loses the lot/serial capture. |
| Try to transfer a tracked item between warehouses | Error: `lot selections required for lot-tracked outbound` (or serial equivalent) | **Not yet supported.** Tracked transfers land as part of Phase I follow-up. |
| Try to build an assembly with a tracked component | Error: `lot selections required` / `serial selections required` | **Not yet supported.** Tracked assembly is after Phase I. |
| Customer emails "I enabled tracking, my invoice fails, is this a bug?" | Not a bug, expected | See the invoice row above. Offer to help them flip items back to non-tracked if blocking their sales. |

---

## 5. Procedure — enabling tracking for a customer

Step 1. **Confirm the customer fits the decision tree above.** If they
need to sell / transfer / build tracked items, do not proceed. Set
expectation for Phase I.

Step 2. **Customer admin flips the setting.** Through the
product's admin UI (whichever surface exposes it — engineering can
confirm if unclear). The setting is `tracking_enabled=true`.

Step 3. **Confirm audit trail wrote.** The `audit_logs` table will
have a row with action `company.tracking_capability.enabled`
referencing the company. If unsure, loop in engineering.

Step 4. **Walk the customer through their first lot-tracked receipt.**
They will need to:
- Mark the relevant stock item's tracking_mode as `lot`
- On the next bill for that item, fill in the lot number and
  (optionally) the expiry date on the bill line
- Post the bill normally

Step 5. **Verify one lot receipt lands correctly.** Check in the
customer's inventory reports that a lot row exists with expected
quantity and expiry.

---

## 6. Procedure — disabling tracking for a customer

Customers who enabled tracking and want to turn it off must first
walk back each tracked item individually.

Step 1. **Identify every item with tracking_mode ≠ none.** Inventory
report or direct query.

Step 2. **For each item**:
- Drain stock to zero (consume / write off / transfer out, whichever
  matches reality — write-off requires approval, do not shortcut
  through SQL)
- Once on-hand and layer-remaining are both zero, flip
  `tracking_mode` back to `none`

Step 3. **Only then**, flip `tracking_enabled=false`.

Customer cannot skip steps. The system will refuse. This is by design
— silent cleanup would abandon lot history and traceability.

If a customer insists on a shortcut and the situation is business-
critical, escalate to engineering. Do not reach into the database
yourself.

---

## 7. Known limitations reference card

Print-ready list for quick reference:

**Supported right now:**
- ✅ Lot-tracked receiving via purchase bill
- ✅ Lot + expiry capture on bill line
- ✅ Lot creation / top-up on receipt
- ✅ Reading lot state via reports (on-hand by lot, expiring soon, etc.)

**Unsupported right now** (will surface clear errors if attempted):
- ❌ Invoicing any tracked item (lot or serial)
- ❌ Receiving serial-tracked items via bills
- ❌ Transferring tracked items between warehouses
- ❌ Building / assembling with tracked components
- ❌ Reserving a specific serial (reservation stays item-level)
- ❌ Flipping tracking mode on an item with existing on-hand
- ❌ Disabling company tracking while tracked items still exist

**Policy defaults locked** (will not change without a product
decision):
- Expired stock is surfaced via reports, not blocked at issue
- FEFO (first-expired-first-out) is advisory via reports, not
  automatic
- Serial expiry is optional, not required
- Only stock-type items can be lot/serial tracked (not services,
  not non-inventory, not other-charge)

---

## 8. What Phase I will add

Do not commit specific dates. Language for setting expectations:

> "The sell-side tracking flow is planned as the next major
> inventory phase. It will introduce shipment as a first-class
> document and wire tracked items into the sales flow. Until that
> ships, selling tracked items is not supported."

High-level scope of Phase I (internal knowledge, not customer
promise):
- Shipment as a first-class document separate from invoice
- Invoice built on shipped-eligible quantity, not directly on sales
  order quantity
- Tracked sales: lot / serial selection at shipment time
- Source identity linkage (SO line ↔ shipment line ↔ sales issue ↔
  invoice line) for partial / split / return flows
- Customer return workflow: return receive → inspect → disposition

Tracked transfer and tracked assembly come as dedicated slices after
Phase I.

---

## 9. Escalation

Loop in engineering if:
- An enablement / disablement action returned an error the table in
  §4 doesn't cover
- A customer reports inventory state that contradicts their bill
  history (possible data anomaly)
- A customer needs a workaround not listed in §6 and the business
  case is urgent
- A bug is suspected (e.g. preview said it was fine, then post
  failed — G.2 should prevent this; if it happens, there's a
  regression to investigate)

Do not escalate for:
- Customers asking "when's Phase I" — answer from §8
- Customers asking "can you just enable it in the DB" — answer: no,
  the audit trail matters
- Customers wanting to partially support tracked sales — answer:
  that's exactly what Phase I is for

---

## 10. Change log

| Date | Change |
|---|---|
| (today) | Initial issue after Phase G merge + staging smoke green |
| (Phase I) | Supersede or retire sections 3–4 as sell-side flow ships |

---

**One-line summary for CS dashboards:**
*Phase G tracking = receiving-only. Selling tracked items is Phase I.
Customers with sell-side tracking needs should wait.*
