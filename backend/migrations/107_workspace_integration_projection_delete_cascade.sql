-- Make v1-backed connector projections disappear when the v1 integration is
-- deleted. Discovery now prefers connector projections when present, so a
-- deleted workspace_integrations row must not leave stale searchable tools.

DELETE FROM workspace_integration_connections conn
WHERE conn.legacy_integration_id IS NULL
  AND (
      conn.config ->> 'projection' = 'v1_workspace_integrations'
      OR EXISTS (
          SELECT 1
          FROM workspace_integration_tool_snapshots snap
          WHERE snap.connection_id = conn.id
            AND snap.provenance ->> 'projection' = 'v1_workspace_integrations'
      )
  );

ALTER TABLE workspace_integration_connections
    DROP CONSTRAINT IF EXISTS workspace_integration_connections_legacy_integration_id_fkey;

ALTER TABLE workspace_integration_connections
    ADD CONSTRAINT workspace_integration_connections_legacy_integration_id_fkey
    FOREIGN KEY (legacy_integration_id)
    REFERENCES workspace_integrations(id)
    ON DELETE CASCADE;
