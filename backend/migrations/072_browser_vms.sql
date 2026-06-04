-- browser_vms table
--
-- ON DELETE semantics:
--   - account_id: CASCADE so deleting an account drops its browser VM records
--     instead of blocking the account teardown on dangling rows.
--   - host_id: SET NULL so retiring a host clears the pointer rather than
--     blocking host decommissioning. The agent-side VM is already gone at
--     that point; the DB just needs to stop pointing at the missing host.
CREATE TABLE browser_vms (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    slug            TEXT NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    host_id         INT REFERENCES hosts(id) ON DELETE SET NULL,
    vm_ip           TEXT,
    status          TEXT NOT NULL DEFAULT 'stopped',
    vcpus           INT NOT NULL DEFAULT 1,
    memory_mb       INT NOT NULL DEFAULT 1024,
    cdp_port        INT NOT NULL DEFAULT 9222,
    rootfs_version  TEXT,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT browser_vms_status_check CHECK (status IN ('stopped', 'provisioning', 'running', 'error')),
    UNIQUE(account_id, slug)
);

CREATE INDEX idx_browser_vms_account ON browser_vms(account_id);
CREATE INDEX idx_browser_vms_host ON browser_vms(host_id) WHERE host_id IS NOT NULL;

-- browser_vm_placements table
--
-- host_id CASCADE: placements are ephemeral capacity accounting, so
-- decommissioning a host should drop their placement history rather than
-- block the host deletion. browser_vm_id is already CASCADE for the same
-- reason — the placement has no meaning without its owning browser VM.
CREATE TABLE browser_vm_placements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    browser_vm_id   UUID NOT NULL REFERENCES browser_vms(id) ON DELETE CASCADE,
    host_id         INT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    vm_ip           TEXT,
    state           TEXT NOT NULL DEFAULT 'reserved',
    reserved_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at    TIMESTAMPTZ,
    released_at     TIMESTAMPTZ,
    CONSTRAINT browser_vm_placements_state_check CHECK (state IN ('reserved', 'active', 'released'))
);

CREATE UNIQUE INDEX idx_bvp_active_browser_vm
ON browser_vm_placements(browser_vm_id) WHERE released_at IS NULL;

CREATE UNIQUE INDEX idx_bvp_active_host_ip
ON browser_vm_placements(host_id, vm_ip) WHERE released_at IS NULL;

CREATE INDEX idx_bvp_host
ON browser_vm_placements(host_id) WHERE released_at IS NULL;

-- pairing column on machines
--
-- ON DELETE SET NULL: deleting a browser VM must not be blocked by a stale
-- pairing pointer on a machine row. handleDeleteBrowserVM runs the agent-side
-- unpair first (best effort), and this constraint guarantees the DB delete
-- succeeds even if the agent unpair failed and left the pointer in place.
ALTER TABLE machines ADD COLUMN browser_vm_id UUID REFERENCES browser_vms(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX idx_machines_browser_vm ON machines(browser_vm_id) WHERE browser_vm_id IS NOT NULL;
