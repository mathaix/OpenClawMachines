-- 032: Machine backups

ALTER TABLE machines ADD COLUMN IF NOT EXISTS backups_enabled BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS backup_key BYTEA;

CREATE TABLE IF NOT EXISTS machine_backups (
    id               SERIAL PRIMARY KEY,
    machine_id       UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    timestamp        TIMESTAMPTZ NOT NULL,
    gcs_path         TEXT NOT NULL,
    size_bytes       BIGINT NOT NULL,
    compressed_bytes BIGINT NOT NULL,
    checksum_sha256  TEXT NOT NULL,
    hmac_sha256      BYTEA NOT NULL,
    nonce            BYTEA NOT NULL,
    trigger          TEXT NOT NULL DEFAULT 'manual',
    host_id          INT REFERENCES hosts(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_machine_backups_machine_id ON machine_backups(machine_id);
CREATE INDEX IF NOT EXISTS idx_machine_backups_latest ON machine_backups(machine_id, timestamp DESC);
