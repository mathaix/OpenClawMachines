-- Update browser registry entry config template for managed companion VM.
-- When mode is "managed", the orchestrator starts a companion Chromium VM
-- and the config assembly injects cdpUrl pointing to it. These flags tell
-- the gateway to use the remote CDP endpoint rather than launching its own browser.
UPDATE registry_entries
SET config_template = '{"browser":{"enabled":true,"attachOnly":true,"noSandbox":true,"headless":true}}'::jsonb,
    tier = 'pro',
    updated_at = NOW()
WHERE id = 'browser' AND type = 'tool';
