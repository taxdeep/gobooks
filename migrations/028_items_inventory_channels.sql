-- Migration 028: Items extensibility, inventory tracking, and channel integration layer.
--
-- This migration extends the product_services table with capability flags, structure
-- type, and additional account links for future inventory support. It also creates
-- foundational tables for inventory movements/balances, BOM components, and external
-- sales channel integration (Amazon, Shopify, etc.).
--
-- All tables are company-scoped. All new columns have safe defaults so existing
-- rows are unaffected. All CREATE/ALTER statements use IF NOT EXISTS for idempotency.

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 1: Extend product_services with capability flags and additional accounts
-- ═══════════════════════════════════════════════════════════════════════════════

ALTER TABLE product_services ADD COLUMN IF NOT EXISTS sku                  TEXT         NOT NULL DEFAULT '';
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS can_be_sold          BOOLEAN      NOT NULL DEFAULT true;
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS can_be_purchased     BOOLEAN      NOT NULL DEFAULT false;
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS is_stock_item        BOOLEAN      NOT NULL DEFAULT false;
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS item_structure_type  TEXT         NOT NULL DEFAULT 'single';
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS purchase_price       NUMERIC(18,4) NOT NULL DEFAULT 0;
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS cogs_account_id      BIGINT       REFERENCES accounts(id) ON DELETE RESTRICT;
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS inventory_account_id BIGINT       REFERENCES accounts(id) ON DELETE RESTRICT;

-- Backfill capability flags for existing rows based on current type.
-- service:       can_be_sold=true,  can_be_purchased=false, is_stock_item=false
-- non_inventory: can_be_sold=true,  can_be_purchased=true,  is_stock_item=false
-- (The defaults handle service correctly; only non_inventory needs a fix.)
UPDATE product_services
SET    can_be_purchased = true
WHERE  type = 'non_inventory'
  AND  can_be_purchased = false;

CREATE INDEX IF NOT EXISTS idx_product_services_sku ON product_services(company_id, sku) WHERE sku <> '';
CREATE INDEX IF NOT EXISTS idx_product_services_stock ON product_services(company_id) WHERE is_stock_item = true;

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 2: Item components (BOM / Bundle / Assembly — schema reservation)
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- parent_item_id: the bundle/assembly/kit product
-- component_item_id: one of its constituent items
-- quantity: how many of the component are needed per 1 parent
--
-- Usage:
--   single item:  has zero rows in this table
--   bundle/kit:   parent is the sellable bundle, components are existing items
--   assembly:     parent is the finished good, components are raw materials / sub-assemblies

CREATE TABLE IF NOT EXISTS item_components (
    id                 BIGSERIAL   PRIMARY KEY,
    company_id         BIGINT      NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    parent_item_id     BIGINT      NOT NULL REFERENCES product_services(id) ON DELETE RESTRICT,
    component_item_id  BIGINT      NOT NULL REFERENCES product_services(id) ON DELETE RESTRICT,
    quantity           NUMERIC(10,4) NOT NULL DEFAULT 1,
    sort_order         INT         NOT NULL DEFAULT 0,
    effective_from     DATE,
    effective_to       DATE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_item_components_company   ON item_components(company_id);
CREATE INDEX IF NOT EXISTS idx_item_components_parent    ON item_components(company_id, parent_item_id);
CREATE INDEX IF NOT EXISTS idx_item_components_component ON item_components(company_id, component_item_id);

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 3: Inventory movements (source-traceable stock changes)
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- Every stock change is recorded as a signed movement (positive = in, negative = out).
-- source_type + source_id trace the originating document/event.
--
-- Current movement_type values: opening, adjustment
-- Future: purchase, sale, refund, amazon_order, amazon_refund,
--         assembly_build, assembly_unbuild, manufacturing_issue, manufacturing_receipt

CREATE TABLE IF NOT EXISTS inventory_movements (
    id              BIGSERIAL      PRIMARY KEY,
    company_id      BIGINT         NOT NULL REFERENCES companies(id)          ON DELETE RESTRICT,
    item_id         BIGINT         NOT NULL REFERENCES product_services(id)   ON DELETE RESTRICT,
    movement_type   TEXT           NOT NULL,
    quantity_delta   NUMERIC(18,4) NOT NULL,
    unit_cost       NUMERIC(18,4),
    total_cost      NUMERIC(18,2),
    source_type     TEXT           NOT NULL DEFAULT '',
    source_id       BIGINT,
    reference_note  TEXT           NOT NULL DEFAULT '',
    movement_date   DATE           NOT NULL DEFAULT CURRENT_DATE,
    created_at      TIMESTAMPTZ    NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_inv_movements_company  ON inventory_movements(company_id);
CREATE INDEX IF NOT EXISTS idx_inv_movements_item     ON inventory_movements(company_id, item_id, movement_date);
CREATE INDEX IF NOT EXISTS idx_inv_movements_source   ON inventory_movements(source_type, source_id) WHERE source_type <> '';

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 4: Inventory balances (materialized stock levels per item + location)
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- location_type + location_ref support future multi-warehouse / FBA scenarios.
-- Current default: location_type='internal', location_ref='' (single location).
-- Future: amazon_fba, third_party, adjustment_bucket

CREATE TABLE IF NOT EXISTS inventory_balances (
    id               BIGSERIAL      PRIMARY KEY,
    company_id       BIGINT         NOT NULL REFERENCES companies(id)          ON DELETE RESTRICT,
    item_id          BIGINT         NOT NULL REFERENCES product_services(id)   ON DELETE RESTRICT,
    location_type    TEXT           NOT NULL DEFAULT 'internal',
    location_ref     TEXT           NOT NULL DEFAULT '',
    quantity_on_hand NUMERIC(18,4)  NOT NULL DEFAULT 0,
    average_cost     NUMERIC(18,4)  NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ    NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_inv_balances_item_location
    ON inventory_balances(company_id, item_id, location_type, location_ref);

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 5: Sales channel accounts (Amazon Seller, Shopify, etc.)
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- Each row represents one external sales channel account connected by a company.
-- auth_status tracks the OAuth/credential lifecycle; actual tokens are NOT stored here.

CREATE TABLE IF NOT EXISTS sales_channel_accounts (
    id                   BIGSERIAL   PRIMARY KEY,
    company_id           BIGINT      NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    channel_type         TEXT        NOT NULL,
    display_name         TEXT        NOT NULL DEFAULT '',
    region               TEXT        NOT NULL DEFAULT '',
    external_account_ref TEXT,
    auth_status          TEXT        NOT NULL DEFAULT 'pending',
    last_sync_at         TIMESTAMPTZ,
    is_active            BOOLEAN     NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_channel_accounts_company ON sales_channel_accounts(company_id);

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 6: Item ↔ channel SKU mappings
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- Maps a Balanciz item to one or more external platform listings/SKUs.
-- One item may have different ASINs across multiple Amazon marketplaces.

CREATE TABLE IF NOT EXISTS item_channel_mappings (
    id                  BIGSERIAL   PRIMARY KEY,
    company_id          BIGINT      NOT NULL REFERENCES companies(id)               ON DELETE RESTRICT,
    item_id             BIGINT      NOT NULL REFERENCES product_services(id)        ON DELETE RESTRICT,
    channel_account_id  BIGINT      NOT NULL REFERENCES sales_channel_accounts(id)  ON DELETE RESTRICT,
    channel_type        TEXT        NOT NULL,
    marketplace_id      TEXT,
    external_sku        TEXT        NOT NULL DEFAULT '',
    asin                TEXT,
    fnsku               TEXT,
    listing_status      TEXT,
    is_active           BOOLEAN     NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_item_channel_mappings_company ON item_channel_mappings(company_id);
CREATE INDEX IF NOT EXISTS idx_item_channel_mappings_item    ON item_channel_mappings(company_id, item_id);
CREATE INDEX IF NOT EXISTS idx_item_channel_mappings_ext_sku ON item_channel_mappings(company_id, channel_account_id, external_sku);

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 7: Channel orders — raw import layer
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- External orders land here first. They are NOT invoices. They must go through
-- mapping + validation before entering the Balanciz business flow.

CREATE TABLE IF NOT EXISTS channel_orders (
    id                  BIGSERIAL   PRIMARY KEY,
    company_id          BIGINT      NOT NULL REFERENCES companies(id)               ON DELETE RESTRICT,
    channel_account_id  BIGINT      NOT NULL REFERENCES sales_channel_accounts(id)  ON DELETE RESTRICT,
    external_order_id   TEXT        NOT NULL DEFAULT '',
    marketplace_id      TEXT,
    order_date          DATE,
    order_status        TEXT        NOT NULL DEFAULT 'imported',
    currency_code       TEXT        NOT NULL DEFAULT '',
    raw_payload         JSONB       NOT NULL DEFAULT '{}',
    imported_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    synced_at           TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_channel_orders_company ON channel_orders(company_id);
CREATE INDEX IF NOT EXISTS idx_channel_orders_ext     ON channel_orders(company_id, channel_account_id, external_order_id);

CREATE TABLE IF NOT EXISTS channel_order_lines (
    id                BIGSERIAL    PRIMARY KEY,
    company_id        BIGINT       NOT NULL REFERENCES companies(id)        ON DELETE RESTRICT,
    channel_order_id  BIGINT       NOT NULL REFERENCES channel_orders(id)   ON DELETE CASCADE,
    external_line_id  TEXT         NOT NULL DEFAULT '',
    external_sku      TEXT         NOT NULL DEFAULT '',
    asin              TEXT,
    quantity          NUMERIC(10,4) NOT NULL DEFAULT 0,
    item_price        NUMERIC(18,2),
    tax_amount        NUMERIC(18,2),
    discount_amount   NUMERIC(18,2),
    mapped_item_id    BIGINT       REFERENCES product_services(id)          ON DELETE SET NULL,
    mapping_status    TEXT         NOT NULL DEFAULT 'unmapped',
    raw_payload       JSONB        NOT NULL DEFAULT '{}',
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_channel_order_lines_order  ON channel_order_lines(channel_order_id);
CREATE INDEX IF NOT EXISTS idx_channel_order_lines_company ON channel_order_lines(company_id);

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 8: Channel accounting mappings (settlement / fee account mapping)
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- Tells the system which GL accounts to use when posting channel-originated
-- transactions (Amazon fees, refunds, shipping, marketplace tax, etc.).
-- One mapping row per channel account. All account FKs must be company-scoped.

CREATE TABLE IF NOT EXISTS channel_accounting_mappings (
    id                           BIGSERIAL   PRIMARY KEY,
    company_id                   BIGINT      NOT NULL REFERENCES companies(id)               ON DELETE RESTRICT,
    channel_account_id           BIGINT      NOT NULL REFERENCES sales_channel_accounts(id)  ON DELETE RESTRICT,
    clearing_account_id          BIGINT      REFERENCES accounts(id) ON DELETE RESTRICT,
    fee_expense_account_id       BIGINT      REFERENCES accounts(id) ON DELETE RESTRICT,
    refund_account_id            BIGINT      REFERENCES accounts(id) ON DELETE RESTRICT,
    shipping_income_account_id   BIGINT      REFERENCES accounts(id) ON DELETE RESTRICT,
    shipping_expense_account_id  BIGINT      REFERENCES accounts(id) ON DELETE RESTRICT,
    marketplace_tax_account_id   BIGINT      REFERENCES accounts(id) ON DELETE RESTRICT,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_channel_acct_mappings
    ON channel_accounting_mappings(company_id, channel_account_id);
