-- Migration 006: Change machine slug uniqueness from global to per-account.
-- Machine slugs are path segments under account subdomains, so they only
-- need to be unique within an account, not globally.

-- Drop global unique constraint on machine slug
ALTER TABLE machines DROP CONSTRAINT IF EXISTS machines_slug_key;

-- Add per-account unique constraint
DO $$ BEGIN
    ALTER TABLE machines ADD CONSTRAINT machines_account_slug_unique UNIQUE (account_id, slug);
EXCEPTION WHEN duplicate_object THEN NULL; WHEN duplicate_table THEN NULL;
END $$;
