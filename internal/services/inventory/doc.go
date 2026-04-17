// 遵循project_guide.md
//
// Package inventory is GoBooks' inventory bounded context.
//
// The package is the single writer and authoritative reader for stock
// quantities and costs. Other modules (AP / AR / GL) do not touch
// inventory_movements or inventory_balances directly — they go through
// the IN / OUT contract exposed here.
//
// See INVENTORY_MODULE_API.md at the repository root for the full
// architectural rationale, the seven IN functions, the seven OUT
// functions, the error taxonomy, and the migration plan.
//
// Quick reference:
//
//	IN (writes)
//	  ReceiveStock    — stock inflow (bill, opening, transfer-in, build-produce, customer return)
//	  IssueStock      — stock outflow (invoice, build-consume, transfer-out, scrap)
//	  AdjustStock     — gain/loss/damage (signed delta)
//	  TransferStock   — atomic Issue+Receive pair between warehouses
//	  ReverseMovement — reverse a prior movement using its original cost snapshot
//	  ReserveStock    — Phase E
//	  ReleaseStock    — Phase E
//
//	OUT (reads)
//	  GetOnHand            — point-in-time balance and avg cost
//	  GetMovements         — filtered movement ledger
//	  GetItemLedger        — per-item report with opening/closing
//	  ExplodeBOM           — multi-level component expansion
//	  GetValuationSnapshot — total inventory valuation at a date
//	  GetAvailableForBuild — max buildable quantity of an assembled item
//	  GetCostingPreview    — hypothetical issue cost (for quotations)
//
// Invariants enforced by this package:
//
//	• SUM(QuantityDelta) per (item, warehouse) == inventory_balances.quantity_on_hand
//	• Reversal.UnitCostBase == Original.UnitCostBase (snapshot preservation)
//	• Transfer legs share a single UnitCostBase (cost-neutral)
//	• IdempotencyKey unique per company (partial index on idempotency_key IS NOT NULL)
//	• History is append-only; reversals are new rows, never UPDATEs
//
// All functions accept *gorm.DB so the caller controls the transaction
// boundary. Inventory never commits on its own.
package inventory
