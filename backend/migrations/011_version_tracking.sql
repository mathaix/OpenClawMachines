-- Track rootfs version and OpenClaw version per machine.
ALTER TABLE machines ADD COLUMN IF NOT EXISTS rootfs_snapshot TEXT;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS openclaw_version TEXT;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS last_started_at TIMESTAMPTZ;
