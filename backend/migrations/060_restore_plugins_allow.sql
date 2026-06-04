-- Fix: migration 059 may have overwritten plugins.allow when the key didn't
-- previously exist, dropping non-channel plugins (composio, opik-openclaw).
-- This migration ensures every plugin in plugins.entries is also in plugins.allow.

UPDATE machine_config
SET assembled_config = jsonb_set(
    assembled_config,
    '{plugins,allow}',
    (
        SELECT jsonb_agg(DISTINCT val)
        FROM (
            -- Keep everything currently in allow
            SELECT val FROM jsonb_array_elements_text(
                COALESCE(assembled_config->'plugins'->'allow', '[]'::jsonb)
            ) AS val
            UNION
            -- Add every key from plugins.entries that has enabled=true
            SELECT key FROM jsonb_each(
                COALESCE(assembled_config->'plugins'->'entries', '{}'::jsonb)
            ) AS e(key, value)
            WHERE (value->>'enabled')::boolean = true
        ) sub
    )
)
WHERE assembled_config->'plugins'->'entries' IS NOT NULL;
