-- Backfill plugin entries for channels already configured on existing machines.
-- Migration 057 fixed the registry templates for new connections, but machines
-- that already had Telegram/Discord/Slack connected are missing the plugin
-- entries in their assembled config, so the gateway never loads the plugin.

-- Add plugins.entries.<channel>.enabled = true for each channel that exists
-- in the assembled config but has no plugin entry.

-- Telegram
UPDATE machine_config
SET assembled_config = jsonb_set(
    jsonb_set(
        assembled_config,
        '{plugins,entries,telegram}',
        '{"enabled":true}'::jsonb,
        true
    ),
    '{plugins,allow}',
    (
        SELECT jsonb_agg(DISTINCT val)
        FROM (
            SELECT val FROM jsonb_array_elements_text(
                COALESCE(assembled_config->'plugins'->'allow', '[]'::jsonb)
            ) AS val
            UNION
            SELECT 'telegram'
        ) sub
    )
)
WHERE assembled_config->'channels'->'telegram' IS NOT NULL
  AND assembled_config->'plugins'->'entries'->'telegram' IS NULL;

-- Discord
UPDATE machine_config
SET assembled_config = jsonb_set(
    jsonb_set(
        assembled_config,
        '{plugins,entries,discord}',
        '{"enabled":true}'::jsonb,
        true
    ),
    '{plugins,allow}',
    (
        SELECT jsonb_agg(DISTINCT val)
        FROM (
            SELECT val FROM jsonb_array_elements_text(
                COALESCE(assembled_config->'plugins'->'allow', '[]'::jsonb)
            ) AS val
            UNION
            SELECT 'discord'
        ) sub
    )
)
WHERE assembled_config->'channels'->'discord' IS NOT NULL
  AND assembled_config->'plugins'->'entries'->'discord' IS NULL;

-- Slack
UPDATE machine_config
SET assembled_config = jsonb_set(
    jsonb_set(
        assembled_config,
        '{plugins,entries,slack}',
        '{"enabled":true}'::jsonb,
        true
    ),
    '{plugins,allow}',
    (
        SELECT jsonb_agg(DISTINCT val)
        FROM (
            SELECT val FROM jsonb_array_elements_text(
                COALESCE(assembled_config->'plugins'->'allow', '[]'::jsonb)
            ) AS val
            UNION
            SELECT 'slack'
        ) sub
    )
)
WHERE assembled_config->'channels'->'slack' IS NOT NULL
  AND assembled_config->'plugins'->'entries'->'slack' IS NULL;

-- Also remove the "groups" key from any existing Slack channel configs
-- (same fix as migration 058 but for assembled configs, not templates)
UPDATE machine_config
SET assembled_config = assembled_config #- '{channels,slack,groups}'
WHERE assembled_config->'channels'->'slack'->'groups' IS NOT NULL;
