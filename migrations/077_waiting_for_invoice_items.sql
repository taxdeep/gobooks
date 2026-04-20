-- 077_waiting_for_invoice_items.sql
-- Phase I slice I.3: the `waiting_for_invoice_items` operational queue.
--
-- Purpose
-- -------
-- Under Phase I (current scope I.B), a posted Shipment books inventory
-- issue truth (Dr COGS / Cr Inventory) at the moment goods leave the
-- warehouse. Revenue recognition does NOT happen at the same moment —
-- it is the customer Invoice that books Dr AR / Cr Revenue later. The
-- gap between "shipped" and "invoiced" is an operational state that
-- must be visible to operations / finance: this table is that state.
--
-- Not a journal entry
-- -------------------
-- This table is deliberately NOT part of the ledger. No GL posting
-- is driven by rows here; no balance sheet account is affected by
-- inserting or closing a row. The GL side of the Shipment→Invoice
-- gap is simply:
--    - Shipment post:  Dr COGS       Cr Inventory    (I.3)
--    - Invoice post:   Dr AR         Cr Revenue      (I.4, under flag)
-- Revenue catches up independently of cost. Any apparent "COGS without
-- Revenue" during the gap is the real operational truth (the goods
-- really have left the warehouse); finance sees the gap through this
-- queue and through period-end reports, not through a clearing
-- account.
--
-- Lifecycle
-- ---------
--   created  (status='open')  at Shipment post (I.3), one row per
--                              stock-item line with positive qty
--   closed   (status='closed') at Invoice post under
--                              shipment_required=true, when an
--                              invoice_line carries matching
--                              shipment_line_id (I.5)
--   voided   (status='voided') at Shipment void (I.3) — the whole
--                              queue entry collapses with its parent;
--                              also on Invoice void under flag=true
--                              if the invoice had closed the entry
--                              (I.5 reopens to 'open' rather than
--                              writing a new 'open' row)
--
-- Resolution model (1:1, atomic)
-- ------------------------------
-- I.3/I.5 treat this as a 1:1 queue: each shipment line produces at
-- most one waiting_for_invoice row, and Invoice matching either
-- fully closes that row (I.5) or fails loudly. Partial invoicing of a
-- shipment line is NOT supported in this slice — if a business needs
-- it (e.g. split into two invoices), that requires a dedicated slice
-- that reshapes this table's qty semantics. Reviewer contract: any PR
-- that introduces qty_pending decrement logic without a scope trigger
-- is out of slice.
--
-- Status column, not deletion
-- ---------------------------
-- Closed and voided rows are NEVER deleted — they remain as the
-- audit trail for the shipment-to-invoice match history. Operational
-- dashboards filter on status='open'. Forensics traces the full
-- chain (open → closed by invoice X, or open → voided by void
-- shipment) via the resolved_* + status fields.

CREATE TABLE IF NOT EXISTS waiting_for_invoice_items (
    id                        BIGSERIAL PRIMARY KEY,
    company_id                BIGINT         NOT NULL,

    -- Source shipment identity (always set, never nullable — a WFI row
    -- without a source shipment is nonsensical).
    shipment_id               BIGINT         NOT NULL,
    shipment_line_id          BIGINT         NOT NULL,
    product_service_id        BIGINT         NOT NULL,
    warehouse_id              BIGINT         NOT NULL,

    -- Denormalised customer / SO identity for operational dashboards
    -- without joining back through shipments. Customer may be absent
    -- (shipment allows nullable customer in I.2), so this is nullable.
    customer_id               BIGINT,
    sales_order_id            BIGINT,
    sales_order_line_id       BIGINT,

    -- Pending quantity + authoritative cost captured at Shipment post
    -- time. qty_pending is set once and, in I.3/I.5, is not mutated
    -- partially: the row closes atomically at Invoice match.
    qty_pending               NUMERIC(18,6) NOT NULL DEFAULT 0,
    unit_cost_base            NUMERIC(18,6) NOT NULL DEFAULT 0,

    ship_date                 DATE          NOT NULL,

    -- Lifecycle status: 'open' | 'closed' | 'voided'.
    status                    TEXT          NOT NULL DEFAULT 'open',

    -- Resolution identity (set when status→closed by Invoice match in I.5).
    resolved_invoice_id       BIGINT,
    resolved_invoice_line_id  BIGINT,
    resolved_at               TIMESTAMPTZ,

    created_at                TIMESTAMPTZ,
    updated_at                TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_wfi_company_id            ON waiting_for_invoice_items(company_id);
CREATE INDEX IF NOT EXISTS idx_wfi_shipment_id           ON waiting_for_invoice_items(shipment_id);
CREATE INDEX IF NOT EXISTS idx_wfi_shipment_line_id      ON waiting_for_invoice_items(shipment_line_id);
CREATE INDEX IF NOT EXISTS idx_wfi_status                ON waiting_for_invoice_items(status);
CREATE INDEX IF NOT EXISTS idx_wfi_customer_id           ON waiting_for_invoice_items(customer_id);
CREATE INDEX IF NOT EXISTS idx_wfi_company_open_status   ON waiting_for_invoice_items(company_id, status);
