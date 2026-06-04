-- Add Cloudflare Access identity columns to users
ALTER TABLE users ADD COLUMN IF NOT EXISTS cf_sub TEXT UNIQUE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';

CREATE INDEX IF NOT EXISTS idx_users_cf_sub ON users(cf_sub);
