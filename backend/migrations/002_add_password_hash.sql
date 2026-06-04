-- Migration 002: Add password_hash column for email/password auth
-- Allows NULL for users who signed up via OAuth (google/github)

ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT;

-- Make auth_provider_id nullable since email/password users won't have one
ALTER TABLE users ALTER COLUMN auth_provider_id DROP NOT NULL;
