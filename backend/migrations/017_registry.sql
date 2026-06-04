-- Configuration registry: catalog of channels, skills, and tools
CREATE TABLE IF NOT EXISTS registry_entries (
    id                  TEXT PRIMARY KEY,
    type                TEXT NOT NULL,
    name                TEXT NOT NULL,
    description         TEXT,
    version             TEXT NOT NULL DEFAULT '1',
    tier                TEXT NOT NULL DEFAULT 'free',
    modes               JSONB,
    config_template     JSONB NOT NULL,
    infrastructure      JSONB,
    -- Future: UI hints for required credentials
    required_credentials TEXT[],
    status              TEXT NOT NULL DEFAULT 'active',
    sort_order          INT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS registry_entries_type_status_idx ON registry_entries (type, status);

-- Per-machine enabled capabilities with optional overrides
CREATE TABLE IF NOT EXISTS machine_capabilities (
    id                SERIAL PRIMARY KEY,
    machine_id        UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    entry_id          TEXT NOT NULL REFERENCES registry_entries(id),
    mode              TEXT,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    config_overrides  JSONB,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(machine_id, entry_id)
);

CREATE INDEX IF NOT EXISTS machine_capabilities_machine_id_idx ON machine_capabilities (machine_id);

-- Per-machine identity and assembled config
CREATE TABLE IF NOT EXISTS machine_config (
    machine_id          UUID PRIMARY KEY REFERENCES machines(id) ON DELETE CASCADE,
    identity_name       TEXT,
    identity_avatar     TEXT,
    platform_overrides  JSONB,
    user_config         JSONB,
    config_version      INT NOT NULL DEFAULT 1,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed: Channels
INSERT INTO registry_entries (id, type, name, description, config_template, required_credentials, status, sort_order)
VALUES
    ('telegram', 'channel', 'Telegram', 'Telegram messaging integration',
     '{"channels":{"telegram":{"enabled":true,"dmPolicy":"pairing"}}}'::jsonb,
     ARRAY['telegram'], 'active', 1),
    ('discord', 'channel', 'Discord', 'Discord messaging integration',
     '{"channels":{"discord":{"enabled":true,"dmPolicy":"pairing","groupPolicy":"open"}}}'::jsonb,
     ARRAY['discord'], 'active', 2),
    ('whatsapp', 'channel', 'WhatsApp', 'WhatsApp messaging integration',
     '{"channels":{"whatsapp":{"enabled":true}}}'::jsonb,
     ARRAY['whatsapp'], 'coming_soon', 3)
-- Idempotent seeds: existing entries preserved
ON CONFLICT (id) DO NOTHING;

-- Seed: Skills
INSERT INTO registry_entries (id, type, name, description, config_template, sort_order)
VALUES
    ('web-search', 'skill', 'Web Search', 'Search the web for information',
     '{"skills":{"entries":{"web-search":{"enabled":true}}}}'::jsonb, 1),
    ('code-runner', 'skill', 'Code Runner', 'Execute code snippets',
     '{"skills":{"entries":{"code-runner":{"enabled":true}}}}'::jsonb, 2),
    ('file-manager', 'skill', 'File Manager', 'Manage files and documents',
     '{"skills":{"entries":{"file-manager":{"enabled":true}}}}'::jsonb, 3),
    ('image-gen', 'skill', 'Image Generation', 'Generate images from text prompts',
     '{"skills":{"entries":{"image-gen":{"enabled":true}}}}'::jsonb, 4)
-- Idempotent seeds: existing entries preserved
ON CONFLICT (id) DO NOTHING;

-- Seed: Tools
INSERT INTO registry_entries (id, type, name, description, config_template, modes, infrastructure, sort_order)
VALUES
    ('browser', 'tool', 'Browser', 'Web browsing capability',
     '{"browser":{"enabled":true}}'::jsonb,
     '["managed","local","remote"]'::jsonb,
     '{"type":"managed-chromium"}'::jsonb, 1)
-- Idempotent seeds: existing entries preserved
ON CONFLICT (id) DO NOTHING;
