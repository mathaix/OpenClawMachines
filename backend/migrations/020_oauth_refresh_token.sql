-- Add refresh_token column to account_credentials for OAuth refresh token support.
-- The refresh_token is stored encrypted (same as encrypted_value) and is nullable.

ALTER TABLE account_credentials ADD COLUMN IF NOT EXISTS refresh_token TEXT;
