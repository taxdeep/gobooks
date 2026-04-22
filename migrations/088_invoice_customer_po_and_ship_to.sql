-- 088_invoice_customer_po_and_ship_to.sql
-- Customer PO# on the AR sell-side chain + per-customer multi-shipping-address
-- catalogue + Invoice bill-to / ship-to / email snapshots for historical fidelity.
--
-- Why
-- ---
-- Real-world AR flow: customers send us a PO quoting their reference number.
-- That number must appear on the Quote → SO → Invoice → Shipment chain so
-- AR can reconcile AR aging against customer AP aging. Current schema has
-- no `customer_po_number` column anywhere.
--
-- Separately, Customer model carries a single (billing) address. Real
-- customers often ship to multiple warehouses / stores from one billing
-- entity. The new `customer_shipping_addresses` table is a simple 1-many
-- companion to `customers`. The billing address stays on `customers` (its
-- semantic is "where bills go"); shipping addresses are distinct.
--
-- Finally, Invoice needs snapshot columns for bill-to / ship-to / email at
-- print time. Users may tweak these in the editor "for this invoice only"
-- (without updating the customer record). Historical invoices must also
-- continue to print exactly the address that was chosen at save time even
-- if the customer's record is later edited. Snapshot columns satisfy both.
-- Empty snapshots mean "read current customer values" (back-compat for
-- pre-088 invoices).
--
-- What this installs
-- ------------------
-- 1. `customer_shipping_addresses` — per-customer shipping-address catalogue.
-- 2. `sales_orders.customer_po_number`    — SO-level PO#.
-- 3. `invoices.customer_po_number`        — Invoice-level PO# (prefilled
--                                            from SO on conversion; editable).
-- 4. `invoices.ship_to_snapshot`          — free-form ship-to block saved at
--                                            save/post time. Distinct from
--                                            the pre-existing customer_address_snapshot
--                                            which holds the billing address.
-- 5. `invoices.ship_to_label`             — which named shipping address was
--                                            selected (e.g. "Warehouse A").
--
-- Invoice.customer_email_snapshot / customer_address_snapshot already exist
-- from earlier migrations; 088 does NOT re-declare those columns.
--
-- Shipment intentionally does NOT get a PO# column: per spec, Shipment
-- follows its SO (shipment.sales_order.customer_po_number) rather than
-- storing its own copy. The display layer performs the join.
--
-- Safety: all new columns nullable or DEFAULT ''; no backfill required.
-- Existing documents print with empty PO# and fall back to live customer
-- address/email via the model helpers, identical to pre-088 behavior.

-- 1. Multi-shipping-address catalogue ----------------------------------------
CREATE TABLE IF NOT EXISTS customer_shipping_addresses (
    id               BIGSERIAL PRIMARY KEY,
    customer_id      BIGINT NOT NULL,
    label            VARCHAR(64) NOT NULL DEFAULT '',
    addr_street1     TEXT NOT NULL DEFAULT '',
    addr_street2     TEXT NOT NULL DEFAULT '',
    addr_city        TEXT NOT NULL DEFAULT '',
    addr_province    TEXT NOT NULL DEFAULT '',
    addr_postal_code TEXT NOT NULL DEFAULT '',
    addr_country     TEXT NOT NULL DEFAULT '',
    is_default       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_customer_shipping_addresses_customer_id
    ON customer_shipping_addresses(customer_id);

-- 2. Customer PO# on sales_orders --------------------------------------------
ALTER TABLE sales_orders
    ADD COLUMN IF NOT EXISTS customer_po_number VARCHAR(64) NOT NULL DEFAULT '';

-- 3. Customer PO# + ship-to snapshots on invoices ----------------------------
ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS customer_po_number VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS ship_to_snapshot TEXT NOT NULL DEFAULT '';

ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS ship_to_label VARCHAR(64) NOT NULL DEFAULT '';
