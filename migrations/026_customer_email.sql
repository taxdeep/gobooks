-- Migration 026: Add email field to customers table.
-- Required for invoice email sending: customer email is snapshotted onto invoices
-- at creation time and used as the default recipient for invoice emails.

ALTER TABLE customers
    ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';
