-- Phase 3 connector-kernel storage.
--
-- This is additive: v1 workspace_integrations rows and
-- workspace_integration_credentials remain the runtime source of truth until
-- the normalized model is proven. Existing rows are projected into source,
-- connection, snapshot, and policy tables so later slices can migrate runtime
-- reads without losing compatibility.

CREATE TABLE IF NOT EXISTS workspace_integration_sources (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug          TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    kind          TEXT NOT NULL,
    importer      TEXT NOT NULL,
    config        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_sources_workspace
    ON workspace_integration_sources(workspace_id);

CREATE TABLE IF NOT EXISTS workspace_integration_connections (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    source_id             UUID NOT NULL REFERENCES workspace_integration_sources(id) ON DELETE CASCADE,
    legacy_integration_id UUID REFERENCES workspace_integrations(id) ON DELETE CASCADE,
    slug                  TEXT NOT NULL,
    display_name          TEXT NOT NULL,
    scope                 TEXT NOT NULL DEFAULT 'workspace'
        CHECK (scope IN ('workspace', 'user')),
    owner_user_id         INT REFERENCES users(id) ON DELETE SET NULL,
    credential_state      TEXT NOT NULL DEFAULT 'connected'
        CHECK (credential_state IN ('connected', 'disconnected', 'reauth_required', 'unknown')),
    enabled               BOOLEAN NOT NULL DEFAULT true,
    config                JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(source_id, scope, slug)
);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_connections_workspace
    ON workspace_integration_connections(workspace_id);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_connections_legacy
    ON workspace_integration_connections(legacy_integration_id);

CREATE TABLE IF NOT EXISTS workspace_integration_tool_snapshots (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    connection_id   UUID NOT NULL REFERENCES workspace_integration_connections(id) ON DELETE CASCADE,
    tool_name       TEXT NOT NULL,
    tool_address    TEXT NOT NULL,
    legacy_tool_id  TEXT,
    description     TEXT NOT NULL DEFAULT '',
    input_schema    JSONB NOT NULL DEFAULT '{"type":"object","additionalProperties":true}'::jsonb,
    output_schema   JSONB,
    annotations     JSONB NOT NULL DEFAULT '{}'::jsonb,
    access          TEXT NOT NULL DEFAULT 'read'
        CHECK (access IN ('read', 'write')),
    source          TEXT NOT NULL DEFAULT '',
    provenance      JSONB NOT NULL DEFAULT '{}'::jsonb,
    tools_synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    stale_after     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(connection_id, tool_name),
    UNIQUE(tool_address)
);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_tool_snapshots_workspace
    ON workspace_integration_tool_snapshots(workspace_id);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_tool_snapshots_connection
    ON workspace_integration_tool_snapshots(connection_id);

CREATE TABLE IF NOT EXISTS workspace_integration_tool_policies (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    connection_id UUID NOT NULL REFERENCES workspace_integration_connections(id) ON DELETE CASCADE,
    tool_name     TEXT NOT NULL,
    policy        TEXT NOT NULL
        CHECK (policy IN ('allow', 'require_approval', 'block')),
    source        TEXT NOT NULL DEFAULT 'v1_projection',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(connection_id, tool_name)
);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_tool_policies_workspace
    ON workspace_integration_tool_policies(workspace_id);

-- Backfill source/catalog definitions from existing v1 integration rows.
WITH source_rows AS (
    SELECT DISTINCT
        wi.workspace_id,
        COALESCE(
            NULLIF(TRIM(BOTH '-' FROM regexp_replace(lower(replace(wi.kind, '_', '-')), '[^a-z0-9_-]+', '-', 'g')), ''),
            wi.slug
        ) AS source_slug,
        initcap(replace(wi.kind, '_', ' ')) AS source_name,
        wi.kind,
        CASE
            WHEN lower(wi.transport) = 'mcp-remote' THEN 'mcp'
            WHEN lower(wi.kind) IN ('openapi', 'graphql') THEN lower(wi.kind)
            ELSE lower(wi.transport)
        END AS importer
    FROM workspace_integrations wi
)
INSERT INTO workspace_integration_sources (
    workspace_id, slug, display_name, kind, importer, config
)
SELECT
    workspace_id,
    source_slug,
    COALESCE(NULLIF(source_name, ''), source_slug),
    kind,
    COALESCE(NULLIF(importer, ''), 'http'),
    '{}'::jsonb
FROM source_rows
ON CONFLICT (workspace_id, slug) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    kind = EXCLUDED.kind,
    importer = EXCLUDED.importer,
    updated_at = NOW();

-- Backfill one workspace-scoped connection per v1 integration row.
INSERT INTO workspace_integration_connections (
    workspace_id, source_id, legacy_integration_id, slug, display_name,
    scope, credential_state, enabled, config, created_at, updated_at
)
SELECT
    wi.workspace_id,
    src.id,
    wi.id,
    wi.slug,
    wi.display_name,
    'workspace',
    CASE WHEN wi.enabled THEN 'connected' ELSE 'disconnected' END,
    wi.enabled,
    jsonb_build_object(
        'transport', wi.transport,
        'endpoint_present', wi.endpoint IS NOT NULL
    ),
    wi.created_at,
    wi.updated_at
FROM workspace_integrations wi
JOIN workspace_integration_sources src
  ON src.workspace_id = wi.workspace_id
 AND src.slug = COALESCE(
        NULLIF(TRIM(BOTH '-' FROM regexp_replace(lower(replace(wi.kind, '_', '-')), '[^a-z0-9_-]+', '-', 'g')), ''),
        wi.slug
    )
ON CONFLICT (source_id, scope, slug) DO UPDATE SET
    legacy_integration_id = COALESCE(EXCLUDED.legacy_integration_id, workspace_integration_connections.legacy_integration_id),
    display_name = EXCLUDED.display_name,
    credential_state = EXCLUDED.credential_state,
    enabled = EXCLUDED.enabled,
    config = EXCLUDED.config,
    updated_at = NOW();

-- Backfill tool snapshots by expanding v1 JSON manifests.
WITH tool_rows AS (
    SELECT
        wi.workspace_id,
        wi.id AS legacy_integration_id,
        wi.slug AS legacy_slug,
        wi.transport,
        wi.endpoint,
        conn.id AS connection_id,
        src.slug AS source_slug,
        conn.slug AS connection_slug,
        tool.value AS tool_json,
        tool.value ->> 'name' AS tool_name
    FROM workspace_integrations wi
    JOIN workspace_integration_connections conn
      ON conn.legacy_integration_id = wi.id
    JOIN workspace_integration_sources src
      ON src.id = conn.source_id
    CROSS JOIN LATERAL jsonb_array_elements(COALESCE(wi.tool_manifest, '[]'::jsonb)) AS tool(value)
    WHERE COALESCE(tool.value ->> 'name', '') <> ''
)
INSERT INTO workspace_integration_tool_snapshots (
    workspace_id, connection_id, tool_name, tool_address, legacy_tool_id,
    description, input_schema, output_schema, annotations, access, source,
    provenance, tools_synced_at, stale_after
)
SELECT
    tr.workspace_id,
    tr.connection_id,
    tr.tool_name,
    'wi.' ||
        TRIM(BOTH '-' FROM regexp_replace(lower(tr.workspace_id::text), '[^a-z0-9_]+', '-', 'g')) || '.' ||
        tr.source_slug || '.' || tr.connection_slug || '.' ||
        TRIM(BOTH '-' FROM regexp_replace(lower(tr.tool_name), '[^a-z0-9_]+', '-', 'g')) AS tool_address,
    tr.legacy_slug || '.' || tr.tool_name AS legacy_tool_id,
    COALESCE(tr.tool_json ->> 'description', ''),
    COALESCE(
        tr.tool_json -> 'parameters',
        tr.tool_json -> 'input_schema',
        tr.tool_json -> 'schema',
        '{"type":"object","additionalProperties":true}'::jsonb
    ),
    tr.tool_json -> 'output_schema',
    COALESCE(tr.tool_json -> 'annotations', '{}'::jsonb),
    CASE
        WHEN lower(COALESCE(tr.tool_json ->> 'access', '')) IN ('read', 'write')
            THEN lower(tr.tool_json ->> 'access')
        WHEN tr.tool_name ~* '(create|update|delete|remove|send|write|patch|post)'
            THEN 'write'
        ELSE 'read'
    END,
    CASE
        WHEN lower(tr.transport) = 'mcp-remote' THEN 'mcp'
        ELSE lower(tr.transport)
    END,
    jsonb_build_object(
        'projection', 'v1_workspace_integrations',
        'legacy_integration_id', tr.legacy_integration_id,
        'legacy_tool_id', tr.legacy_slug || '.' || tr.tool_name,
        'transport', tr.transport,
        'endpoint_present', tr.endpoint IS NOT NULL
    ),
    NOW(),
    NOW() + INTERVAL '7 days'
FROM tool_rows tr
ON CONFLICT (connection_id, tool_name) DO UPDATE SET
    tool_address = EXCLUDED.tool_address,
    legacy_tool_id = EXCLUDED.legacy_tool_id,
    description = EXCLUDED.description,
    input_schema = EXCLUDED.input_schema,
    output_schema = EXCLUDED.output_schema,
    annotations = EXCLUDED.annotations,
    access = EXCLUDED.access,
    source = EXCLUDED.source,
    provenance = EXCLUDED.provenance,
    tools_synced_at = EXCLUDED.tools_synced_at,
    stale_after = EXCLUDED.stale_after,
    updated_at = NOW();

-- Backfill tri-state policy from v1 allow/deny arrays plus optional
-- non-secret config.tool_policy/tool_policies metadata.
INSERT INTO workspace_integration_tool_policies (
    workspace_id, connection_id, tool_name, policy, source
)
SELECT
    wi.workspace_id,
    snap.connection_id,
    snap.tool_name,
    CASE
        WHEN snap.tool_name = ANY(COALESCE(wi.denied_tools, ARRAY[]::text[]))
          OR snap.legacy_tool_id = ANY(COALESCE(wi.denied_tools, ARRAY[]::text[]))
            THEN 'block'
        WHEN lower(COALESCE(
            wi.config -> 'tool_policy' ->> snap.tool_name,
            wi.config -> 'tool_policy' ->> snap.legacy_tool_id,
            wi.config -> 'tool_policies' ->> snap.tool_name,
            wi.config -> 'tool_policies' ->> snap.legacy_tool_id,
            ''
        )) IN ('require_approval', 'approval_required', 'require-approval', 'approval-required')
            THEN 'require_approval'
        WHEN COALESCE(array_length(wi.allowed_tools, 1), 0) = 0
            THEN 'allow'
        WHEN snap.tool_name = ANY(COALESCE(wi.allowed_tools, ARRAY[]::text[]))
          OR snap.legacy_tool_id = ANY(COALESCE(wi.allowed_tools, ARRAY[]::text[]))
            THEN 'allow'
        ELSE 'block'
    END AS policy,
    'v1_projection'
FROM workspace_integration_tool_snapshots snap
JOIN workspace_integration_connections conn
  ON conn.id = snap.connection_id
JOIN workspace_integrations wi
  ON wi.id = conn.legacy_integration_id
ON CONFLICT (connection_id, tool_name) DO UPDATE SET
    policy = EXCLUDED.policy,
    source = EXCLUDED.source,
    updated_at = NOW();
