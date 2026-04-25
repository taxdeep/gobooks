-- 091_over_shipment_buffer.sql
-- Over-shipment buffer policy on Company (default) + Warehouse (override).
--
-- Why
-- ---
-- Stock-item contracts often allow shipping a small percentage above the
-- SO-line qty to absorb breakage / process tolerance. Operators need to
-- raise SO-line Qty above the contracted amount post-confirm without
-- having to issue a new SO. The buffer ceiling is configurable per
-- company; a warehouse may override (e.g. partner warehouses with stricter
-- contracts run buffer=0, our own warehouses run buffer=5%).
--
-- Two columns mode + value: mode='percent' interprets value as a percentage
-- of the original line qty; mode='qty' interprets value as a fixed extra
-- unit count.
--
-- Resolution: warehouse override wins when its enabled=true; otherwise the
-- company default applies. See services.ResolveOverShipmentPolicy.
--
-- Safety: all new columns nullable or DEFAULT-bearing; no backfill needed.
-- Existing companies / warehouses inherit enabled=false (no buffer).

ALTER TABLE companies
    ADD COLUMN IF NOT EXISTS over_shipment_enabled BOOLEAN        NOT NULL DEFAULT FALSE;
ALTER TABLE companies
    ADD COLUMN IF NOT EXISTS over_shipment_mode    VARCHAR(16)    NOT NULL DEFAULT 'percent';
ALTER TABLE companies
    ADD COLUMN IF NOT EXISTS over_shipment_value   NUMERIC(10,4)  NOT NULL DEFAULT 0;

ALTER TABLE warehouses
    ADD COLUMN IF NOT EXISTS over_shipment_enabled BOOLEAN        NOT NULL DEFAULT FALSE;
ALTER TABLE warehouses
    ADD COLUMN IF NOT EXISTS over_shipment_mode    VARCHAR(16)    NOT NULL DEFAULT 'percent';
ALTER TABLE warehouses
    ADD COLUMN IF NOT EXISTS over_shipment_value   NUMERIC(10,4)  NOT NULL DEFAULT 0;
