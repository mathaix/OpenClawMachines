-- Ensure persisted connector-kernel tool addresses are workspace-qualified.
--
-- Migration 100 now generates addresses as:
--   wi.<workspace_id>.<source_slug>.<connection_slug>.<tool_name>
--
-- This forward migration repairs databases where migration 100 may already
-- have run with the older workspace-ambiguous shape:
--   wi.<source_slug>.<connection_slug>.<tool_name>

WITH address_map AS (
    SELECT
        snap.id AS snapshot_id,
        snap.workspace_id,
        conn.legacy_integration_id,
        snap.tool_name,
        'wi.' ||
            src.slug || '.' || conn.slug || '.' ||
            TRIM(BOTH '-' FROM regexp_replace(lower(snap.tool_name), '[^a-z0-9_]+', '-', 'g')) AS legacy_tool_address,
        'wi.' ||
            TRIM(BOTH '-' FROM regexp_replace(lower(snap.workspace_id::text), '[^a-z0-9_]+', '-', 'g')) || '.' ||
            src.slug || '.' || conn.slug || '.' ||
            TRIM(BOTH '-' FROM regexp_replace(lower(snap.tool_name), '[^a-z0-9_]+', '-', 'g')) AS workspace_tool_address
    FROM workspace_integration_tool_snapshots snap
    JOIN workspace_integration_connections conn
      ON conn.id = snap.connection_id
    JOIN workspace_integration_sources src
      ON src.id = conn.source_id
)
UPDATE workspace_integration_tool_snapshots snap
SET tool_address = address_map.workspace_tool_address,
    updated_at = NOW()
FROM address_map
WHERE snap.id = address_map.snapshot_id
  AND snap.tool_address IS DISTINCT FROM address_map.workspace_tool_address;

WITH address_map AS (
    SELECT
        snap.workspace_id,
        conn.legacy_integration_id,
        snap.tool_name,
        snap.legacy_tool_id,
        'wi.' ||
            src.slug || '.' || conn.slug || '.' ||
            TRIM(BOTH '-' FROM regexp_replace(lower(snap.tool_name), '[^a-z0-9_]+', '-', 'g')) AS legacy_tool_address,
        snap.tool_address AS workspace_tool_address
    FROM workspace_integration_tool_snapshots snap
    JOIN workspace_integration_connections conn
      ON conn.id = snap.connection_id
    JOIN workspace_integration_sources src
      ON src.id = conn.source_id
)
UPDATE workspace_integration_call_events events
SET tool_address = address_map.workspace_tool_address
FROM address_map
WHERE events.workspace_id = address_map.workspace_id
  AND events.tool_address = address_map.legacy_tool_address
  AND events.tool_name = address_map.tool_name
  AND (
      events.integration_id = address_map.legacy_integration_id
      OR events.tool_id = address_map.legacy_tool_id
  );

WITH address_map AS (
    SELECT
        snap.workspace_id,
        snap.tool_name,
        snap.legacy_tool_id,
        'wi.' ||
            src.slug || '.' || conn.slug || '.' ||
            TRIM(BOTH '-' FROM regexp_replace(lower(snap.tool_name), '[^a-z0-9_]+', '-', 'g')) AS legacy_tool_address,
        snap.tool_address AS workspace_tool_address
    FROM workspace_integration_tool_snapshots snap
    JOIN workspace_integration_connections conn
      ON conn.id = snap.connection_id
    JOIN workspace_integration_sources src
      ON src.id = conn.source_id
)
UPDATE workspace_integration_guidance_overlays guidance
SET tool_address = address_map.workspace_tool_address,
    updated_at = NOW()
FROM address_map
WHERE guidance.workspace_id = address_map.workspace_id
  AND guidance.tool_address = address_map.legacy_tool_address
  AND guidance.tool_name = address_map.tool_name
  AND guidance.tool_id = address_map.legacy_tool_id;
