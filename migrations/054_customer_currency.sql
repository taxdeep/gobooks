-- 054_customer_currency.sql
-- Add default currency to customers (mirrors vendors.currency_code).
ALTER TABLE customers
    ADD COLUMN IF NOT EXISTS currency_code TEXT NOT NULL DEFAULT '';
