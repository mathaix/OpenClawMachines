-- Channels (Telegram, Discord, Slack) are gateway plugins since OpenClaw 2026.3.28.
-- Add plugins.entries and plugins.allow to config templates so the gateway loads them.
UPDATE registry_entries
SET config_template = '{"channels":{"telegram":{"enabled":true,"dmPolicy":"pairing"}},"plugins":{"entries":{"telegram":{"enabled":true}},"allow":["telegram"]}}'::jsonb
WHERE id = 'telegram';

UPDATE registry_entries
SET config_template = '{"channels":{"discord":{"enabled":true,"dmPolicy":"pairing","groupPolicy":"open"}},"plugins":{"entries":{"discord":{"enabled":true}},"allow":["discord"]}}'::jsonb
WHERE id = 'discord';

UPDATE registry_entries
SET config_template = '{"channels":{"slack":{"enabled":true,"mode":"socket","dmPolicy":"pairing","groupPolicy":"open","groups":{"*":{"requireMention":true}}}},"plugins":{"entries":{"slack":{"enabled":true}},"allow":["slack"]}}'::jsonb
WHERE id = 'slack';
