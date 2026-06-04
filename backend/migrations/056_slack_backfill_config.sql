-- Backfill Slack config template for environments where 055 already ran
-- with the old template (missing mode, groupPolicy). Migration 055 used
-- DO NOTHING initially, so those DBs have stale config_template values.
UPDATE registry_entries
SET config_template = '{"channels":{"slack":{"enabled":true,"mode":"socket","dmPolicy":"pairing","groupPolicy":"open","groups":{"*":{"requireMention":true}}}}}'::jsonb
WHERE id = 'slack' AND type = 'channel';
