-- Update Telegram channel template to include group defaults.
-- OpenClaw supports per-group config under channels.telegram.groups;
-- without this, bots cannot respond in Telegram groups.
UPDATE registry_entries
SET config_template = '{"channels":{"telegram":{"enabled":true,"dmPolicy":"pairing","groups":{"*":{"requireMention":true}}}}}'::jsonb,
    updated_at = NOW()
WHERE id = 'telegram' AND type = 'channel';
