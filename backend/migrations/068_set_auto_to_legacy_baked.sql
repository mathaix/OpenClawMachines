-- Migrate existing machines from "auto" runtime_source to "legacy_baked".
-- "auto" is no longer a valid runtime_source. Machines are either
-- "artifact" (decoupled runtime) or "legacy_baked" (baked in rootfs).
UPDATE machines
SET runtime_source = 'legacy_baked'
WHERE runtime_source = 'auto';
