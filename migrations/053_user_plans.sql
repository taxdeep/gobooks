-- Migration 053: User Plan quota system.
--
-- Creates the user_plans table (SysAdmin-managed subscription tiers),
-- seeds the three default plans, and adds plan_id to users.
--
-- Quota semantics:
--   max_owned_companies    : max companies a user may create as owner.  -1 = unlimited.
--   max_members_per_company: max invited team members per company (excl. owner). -1 = unlimited.

-- 1. Create user_plans table.
CREATE TABLE IF NOT EXISTS user_plans (
    id                     SERIAL PRIMARY KEY,
    name                   TEXT    NOT NULL,
    max_owned_companies    INTEGER NOT NULL DEFAULT 3,
    max_members_per_company INTEGER NOT NULL DEFAULT 5,
    is_active              BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order             INTEGER NOT NULL DEFAULT 0,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_plans_name ON user_plans (name);

-- 2. Seed default plans (idempotent via ON CONFLICT DO NOTHING).
INSERT INTO user_plans (id, name, max_owned_companies, max_members_per_company, is_active, sort_order)
VALUES
    (1, 'Starter',      3,  5,  TRUE, 10),
    (2, 'Professional', 5,  15, TRUE, 20),
    (3, 'Business',     -1, -1, TRUE, 30)
ON CONFLICT (id) DO NOTHING;

-- Reset sequence so future INSERTs start above the seeded IDs.
SELECT setval('user_plans_id_seq', (SELECT MAX(id) FROM user_plans));

-- 3. Add plan_id to users (DEFAULT 1 = Starter; existing rows get Starter automatically).
ALTER TABLE users ADD COLUMN IF NOT EXISTS plan_id INTEGER NOT NULL DEFAULT 1 REFERENCES user_plans(id);
