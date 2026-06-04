-- Forward migration for the ON DELETE actions backfilled into 072.
--
-- Databases that applied 072 before the branch-review fix landed still carry
-- the original FK actions (NO ACTION everywhere), so the cleanup paths in
-- handleDeleteBrowserVM / account teardown / host decommissioning would still
-- fail with FK violations. This migration brings those dbs in line with the
-- edited 072 file without re-running it.
--
-- Each constraint is dropped and re-created so the behavior change is
-- explicit and auditable. Postgres auto-names FK constraints as
-- <table>_<column>_fkey; we drop by that name and recreate with the
-- desired action.

BEGIN;

-- machines.browser_vm_id → ON DELETE SET NULL
-- Critical: without this, DeleteBrowserVM hits FK when
-- cleanupBrowserPairing's agent-side unpair failed and left the pairing
-- pointer in place.
ALTER TABLE machines
    DROP CONSTRAINT IF EXISTS machines_browser_vm_id_fkey;
ALTER TABLE machines
    ADD CONSTRAINT machines_browser_vm_id_fkey
    FOREIGN KEY (browser_vm_id) REFERENCES browser_vms(id) ON DELETE SET NULL;

-- browser_vms.account_id → ON DELETE CASCADE
ALTER TABLE browser_vms
    DROP CONSTRAINT IF EXISTS browser_vms_account_id_fkey;
ALTER TABLE browser_vms
    ADD CONSTRAINT browser_vms_account_id_fkey
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;

-- browser_vms.host_id → ON DELETE SET NULL
ALTER TABLE browser_vms
    DROP CONSTRAINT IF EXISTS browser_vms_host_id_fkey;
ALTER TABLE browser_vms
    ADD CONSTRAINT browser_vms_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts(id) ON DELETE SET NULL;

-- browser_vm_placements.host_id → ON DELETE CASCADE
ALTER TABLE browser_vm_placements
    DROP CONSTRAINT IF EXISTS browser_vm_placements_host_id_fkey;
ALTER TABLE browser_vm_placements
    ADD CONSTRAINT browser_vm_placements_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts(id) ON DELETE CASCADE;

COMMIT;
