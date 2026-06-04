-- Add credential_type default and expires_at column to account_credentials.
-- credential_type already exists but had no DEFAULT; add one for backward compatibility.
-- expires_at is nullable, used for OAuth token expiry tracking.

ALTER TABLE account_credentials ALTER COLUMN credential_type SET DEFAULT 'api_key';

ALTER TABLE account_credentials ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
