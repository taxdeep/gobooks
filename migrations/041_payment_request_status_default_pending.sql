-- Migration 041: align payment_requests.status default with the current
-- business truth. This does not rewrite historical rows; it only changes the
-- schema default for future inserts that omit status.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA()
      AND table_name = 'payment_requests'
      AND column_name = 'status'
  ) THEN
    ALTER TABLE payment_requests
      ALTER COLUMN status SET DEFAULT 'pending';
  END IF;
END $$;
