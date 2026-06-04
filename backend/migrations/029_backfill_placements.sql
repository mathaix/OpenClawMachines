-- Backfill active placements for running/provisioning machines
INSERT INTO machine_placements (machine_id, host_id, vm_ip, state, activated_at)
SELECT id, host_id, vm_ip, 'active', now()
FROM machines
WHERE host_id IS NOT NULL AND vm_ip IS NOT NULL AND status IN ('running', 'provisioning')
ON CONFLICT (machine_id) WHERE released_at IS NULL DO NOTHING;

-- Backfill home_host_id for stopped machines with host affinity
UPDATE machines SET home_host_id = host_id
WHERE host_id IS NOT NULL AND status = 'stopped';
