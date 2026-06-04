-- migrate: no-transaction
-- Add unique constraint on machines.browser_vm_id to prevent multiple machines
-- from being paired to the same browser VM. The API handler checks this at
-- application level but a concurrent race could bypass it without a DB guard.
-- NULLs are excluded by default (multiple machines can have browser_vm_id=NULL).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS machines_browser_vm_id_unique
    ON machines (browser_vm_id) WHERE browser_vm_id IS NOT NULL;
