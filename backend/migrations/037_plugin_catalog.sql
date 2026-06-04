CREATE TABLE IF NOT EXISTS plugin_catalog (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT,
    slot            TEXT NOT NULL,
    version         TEXT NOT NULL DEFAULT '1',
    install_kind    TEXT NOT NULL DEFAULT 'bundled',
    artifact_url    TEXT,
    artifact_sha256 TEXT,
    config_template JSONB,
    status          TEXT NOT NULL DEFAULT 'active',
    sort_order      INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_plugin_catalog_slot ON plugin_catalog(slot);

-- Seed memory-core plugin
INSERT INTO plugin_catalog (id, name, description, slot, version, install_kind, config_template)
VALUES (
    'memory-core',
    'Memory Core',
    'Default bundled memory plugin — conversation memory and context management',
    'memory',
    '1',
    'bundled',
    '{"plugins": {"slots": {"memory": "memory-core"}}}'
) ON CONFLICT (id) DO NOTHING;
