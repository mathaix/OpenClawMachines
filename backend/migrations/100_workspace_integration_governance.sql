-- Governance metadata for workspace-scoped integrations.
-- These fields let the UI and audit trail show who approved/connected a shared
-- workspace integration without exposing the credential itself.

ALTER TABLE workspace_integrations
    ADD COLUMN IF NOT EXISTS approved_by_user_id INT REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS connected_by_user_id INT REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS connected_at TIMESTAMPTZ;

UPDATE workspace_integrations
SET approved_at = COALESCE(approved_at, created_at),
    connected_at = COALESCE(connected_at, created_at)
WHERE approved_at IS NULL
   OR connected_at IS NULL;
