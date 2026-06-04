CREATE TABLE IF NOT EXISTS machine_plugins (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id        UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    plugin_id         TEXT NOT NULL REFERENCES plugin_catalog(id) ON DELETE RESTRICT,
    slot              TEXT NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    config_overrides  JSONB,
    install_status    TEXT NOT NULL DEFAULT 'pending',
    installed_version TEXT,
    installed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(machine_id, plugin_id)
);

CREATE INDEX IF NOT EXISTS idx_machine_plugins_machine_id ON machine_plugins(machine_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_machine_plugins_slot_exclusive
    ON machine_plugins(machine_id, slot) WHERE enabled = true;
