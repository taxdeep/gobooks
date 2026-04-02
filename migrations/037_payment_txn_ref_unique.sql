-- Migration 037: Payment transaction external ref uniqueness.
-- Prevents duplicate transactions from the same gateway with the same external reference.

CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_txn_ext_ref
    ON payment_transactions(company_id, gateway_account_id, external_txn_ref)
    WHERE external_txn_ref <> '';
