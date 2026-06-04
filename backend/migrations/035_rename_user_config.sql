-- Rename misleading column: user_config is actually a cache of the assembled
-- platform config, not a user-owned overlay. See docs/configuration_architecture.md.
ALTER TABLE machine_config RENAME COLUMN user_config TO assembled_config;
