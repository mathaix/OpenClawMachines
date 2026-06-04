-- Enable browser capability (managed mode) on all existing machines.
-- Uses ON CONFLICT to skip machines that already have it enabled.
INSERT INTO machine_capabilities (machine_id, entry_id, mode, enabled)
SELECT id, 'browser', 'managed', true
FROM machines
ON CONFLICT (machine_id, entry_id) DO NOTHING;
