-- Versioned, human-gated guidance overlays for workspace integration tools.
--
-- Guidance is derived only from sanitized telemetry or operator-authored text.
-- It is layered over bundled skills/tool metadata and can be reverted by
-- status/version changes without rebuilding the runtime image.

CREATE TABLE IF NOT EXISTS workspace_integration_guidance_overlays (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id            INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tool_id               TEXT NOT NULL,
    tool_address          TEXT,
    integration_slug      TEXT NOT NULL,
    tool_name             TEXT NOT NULL,
    status                TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'approved', 'rejected', 'archived')),
    version               INT NOT NULL DEFAULT 1,
    guidance              TEXT NOT NULL,
    source_failure_class  TEXT,
    sanitized_pattern     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by            INT REFERENCES users(id) ON DELETE SET NULL,
    approved_by           INT REFERENCES users(id) ON DELETE SET NULL,
    approved_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_guidance_workspace_tool_status
    ON workspace_integration_guidance_overlays(workspace_id, tool_id, status, version DESC);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_guidance_workspace_address_status
    ON workspace_integration_guidance_overlays(workspace_id, tool_address, status, version DESC);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_guidance_workspace_created
    ON workspace_integration_guidance_overlays(workspace_id, created_at DESC);
