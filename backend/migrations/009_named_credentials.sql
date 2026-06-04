-- Allow multiple credentials per provider per account (named credentials).
-- Change unique constraint from (account_id, provider) to (account_id, provider, label).
-- Backfill existing NULL labels to empty string before adding NOT NULL.

UPDATE account_credentials SET label = '' WHERE label IS NULL;

ALTER TABLE account_credentials DROP CONSTRAINT IF EXISTS account_credentials_account_id_provider_key;
ALTER TABLE account_credentials ALTER COLUMN label SET NOT NULL;
ALTER TABLE account_credentials ALTER COLUMN label SET DEFAULT '';
DO $$ BEGIN
    ALTER TABLE account_credentials ADD CONSTRAINT account_credentials_account_id_provider_label_key
        UNIQUE(account_id, provider, label);
EXCEPTION WHEN duplicate_object THEN NULL; WHEN duplicate_table THEN NULL;
END $$;

-- Machine-credential association (junction table).
-- Links specific account credentials to specific machines.
CREATE TABLE IF NOT EXISTS machine_credentials (
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    credential_id   INT  NOT NULL REFERENCES account_credentials(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, credential_id)
);

CREATE INDEX IF NOT EXISTS idx_machine_credentials_machine_id ON machine_credentials(machine_id);
CREATE INDEX IF NOT EXISTS idx_machine_credentials_credential_id ON machine_credentials(credential_id);
