-- Business-user login lockout state.
-- AutoMigrate handles fresh installs; this migration protects deployed databases
-- where SQL migrations are the primary schema change path.

ALTER TABLE users ADD COLUMN IF NOT EXISTS failed_login_attempts integer NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS login_locked_until timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS login_lock_window_started_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS login_lock_count integer NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS permanently_locked_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS login_lock_reason text NOT NULL DEFAULT '';
