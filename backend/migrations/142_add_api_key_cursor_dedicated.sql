ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS cursor_dedicated BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_api_keys_user_cursor_dedicated
  ON api_keys(user_id, cursor_dedicated)
  WHERE deleted_at IS NULL;
