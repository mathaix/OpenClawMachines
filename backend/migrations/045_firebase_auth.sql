-- Add provider-neutral identity columns for Firebase (and future IdPs)
ALTER TABLE users ADD COLUMN IF NOT EXISTS identity_issuer TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS identity_subject TEXT;

-- Unique constraint: one user per issuer+subject pair
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_identity
  ON users(identity_issuer, identity_subject)
  WHERE identity_issuer IS NOT NULL AND identity_subject IS NOT NULL;

-- Backfill existing CF Access users
UPDATE users
  SET identity_issuer = 'cfaccess',
      identity_subject = cf_sub
  WHERE cf_sub IS NOT NULL
    AND cf_sub != ''
    AND identity_issuer IS NULL;
