package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---- Workspace Integrations ----

func (s *PostgresStore) RevokeWorkspaceIntegrationTokensForWorkspace(ctx context.Context, workspaceID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines
		 SET workspace_integration_tokens_valid_after = now()
		 WHERE workspace_id = $1`, workspaceID)
	return err
}

func (s *PostgresStore) ListWorkspacesByAccount(ctx context.Context, accountID int) ([]Workspace, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, slug, name, created_at, updated_at
		 FROM workspaces
		 WHERE account_id = $1
		 ORDER BY created_at, slug`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.AccountID, &w.Slug, &w.Name, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, w)
	}
	return workspaces, rows.Err()
}

func (s *PostgresStore) GetWorkspace(ctx context.Context, accountID int, workspaceID string) (*Workspace, error) {
	var w Workspace
	err := s.pool.QueryRow(ctx,
		`SELECT id, account_id, slug, name, created_at, updated_at
		 FROM workspaces
		 WHERE account_id = $1 AND id = $2`, accountID, workspaceID).
		Scan(&w.ID, &w.AccountID, &w.Slug, &w.Name, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *PostgresStore) CreateWorkspace(ctx context.Context, workspace *Workspace) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspaces (account_id, slug, name)
		 VALUES ($1, $2, $3)
		 RETURNING id, created_at, updated_at`,
		workspace.AccountID, workspace.Slug, workspace.Name,
	).Scan(&workspace.ID, &workspace.CreatedAt, &workspace.UpdatedAt)
}

func (s *PostgresStore) GetOrCreateDefaultWorkspace(ctx context.Context, accountID int) (*Workspace, error) {
	var w Workspace
	err := s.pool.QueryRow(ctx,
		`WITH inserted AS (
			INSERT INTO workspaces (account_id, slug, name)
			VALUES ($1, 'default', 'Default')
			ON CONFLICT (account_id, slug) DO NOTHING
			RETURNING id, account_id, slug, name, created_at, updated_at
		)
		SELECT id, account_id, slug, name, created_at, updated_at FROM inserted
		UNION ALL
		SELECT id, account_id, slug, name, created_at, updated_at
		FROM workspaces
		WHERE account_id = $1 AND slug = 'default'
		LIMIT 1`, accountID).
		Scan(&w.ID, &w.AccountID, &w.Slug, &w.Name, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *PostgresStore) GetWorkspaceForMachine(ctx context.Context, machineID string) (*Workspace, error) {
	var w Workspace
	err := s.pool.QueryRow(ctx,
		`SELECT w.id, w.account_id, w.slug, w.name, w.created_at, w.updated_at
		 FROM machines m
		 JOIN workspaces w ON w.id = m.workspace_id
		 WHERE m.id = $1`, machineID).
		Scan(&w.ID, &w.AccountID, &w.Slug, &w.Name, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func scanWorkspaceIntegration(scan func(dest ...any) error) (*WorkspaceIntegration, error) {
	var wi WorkspaceIntegration
	err := scan(&wi.ID, &wi.WorkspaceID, &wi.Slug, &wi.DisplayName, &wi.Kind,
		&wi.Transport, &wi.Endpoint, &wi.Enabled, &wi.ToolManifest, &wi.Config,
		&wi.AllowedTools, &wi.DeniedTools, &wi.ApprovedBy, &wi.ApprovedAt,
		&wi.ConnectedBy, &wi.ConnectedAt, &wi.CreatedAt, &wi.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if wi.ToolManifest == nil {
		wi.ToolManifest = json.RawMessage("[]")
	}
	if wi.Config == nil {
		wi.Config = json.RawMessage("{}")
	}
	return &wi, nil
}

func (s *PostgresStore) ListWorkspaceIntegrations(ctx context.Context, workspaceID string) ([]WorkspaceIntegration, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, workspace_id, slug, display_name, kind,
		        transport, endpoint, enabled, tool_manifest, config,
		        COALESCE(allowed_tools, ARRAY[]::text[]),
		        COALESCE(denied_tools, ARRAY[]::text[]),
		        approved_by_user_id, approved_at,
		        connected_by_user_id, connected_at,
		        created_at, updated_at
		 FROM workspace_integrations
		 WHERE workspace_id = $1
		 ORDER BY display_name, slug`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var integrations []WorkspaceIntegration
	for rows.Next() {
		wi, err := scanWorkspaceIntegration(rows.Scan)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, *wi)
	}
	return integrations, rows.Err()
}

func (s *PostgresStore) ListEnabledWorkspaceIntegrationsForMachine(ctx context.Context, machineID string) ([]WorkspaceIntegration, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT wi.id, wi.workspace_id, wi.slug, wi.display_name, wi.kind,
		        wi.transport, wi.endpoint, wi.enabled, wi.tool_manifest, wi.config,
		        COALESCE(wi.allowed_tools, ARRAY[]::text[]),
		        COALESCE(wi.denied_tools, ARRAY[]::text[]),
		        wi.approved_by_user_id, wi.approved_at,
		        wi.connected_by_user_id, wi.connected_at,
		        wi.created_at, wi.updated_at
		 FROM machines m
		 JOIN workspace_integrations wi ON wi.workspace_id = m.workspace_id
		 WHERE m.id = $1 AND wi.enabled = true
		 ORDER BY wi.slug`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var integrations []WorkspaceIntegration
	for rows.Next() {
		wi, err := scanWorkspaceIntegration(rows.Scan)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, *wi)
	}
	return integrations, rows.Err()
}

func (s *PostgresStore) CreateWorkspaceIntegration(ctx context.Context, integration *WorkspaceIntegration) error {
	if integration.ToolManifest == nil {
		integration.ToolManifest = json.RawMessage("[]")
	}
	if integration.Config == nil {
		integration.Config = json.RawMessage("{}")
	}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integrations (
			workspace_id, slug, display_name, kind, transport, endpoint, enabled,
			tool_manifest, config, allowed_tools, denied_tools,
			approved_by_user_id, approved_at, connected_by_user_id, connected_at
		)
		 VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, true), $8, $9, $10, $11, $12, $13, $14, $15)
		 RETURNING id, created_at, updated_at`,
		integration.WorkspaceID, integration.Slug, integration.DisplayName, integration.Kind,
		integration.Transport, integration.Endpoint, integration.Enabled, integration.ToolManifest,
		integration.Config, integration.AllowedTools, integration.DeniedTools,
		integration.ApprovedBy, integration.ApprovedAt, integration.ConnectedBy, integration.ConnectedAt,
	).Scan(&integration.ID, &integration.CreatedAt, &integration.UpdatedAt); err != nil {
		return err
	}
	return s.upsertWorkspaceIntegrationConnectorProjectionFromV1(ctx, integration)
}

func (s *PostgresStore) UpsertWorkspaceIntegration(ctx context.Context, integration *WorkspaceIntegration) error {
	if integration.ToolManifest == nil {
		integration.ToolManifest = json.RawMessage("[]")
	}
	if integration.Config == nil {
		integration.Config = json.RawMessage("{}")
	}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integrations (
			workspace_id, slug, display_name, kind, transport, endpoint, enabled,
			tool_manifest, config, allowed_tools, denied_tools,
			approved_by_user_id, approved_at, connected_by_user_id, connected_at
		)
		 VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, true), $8, $9, $10, $11, $12, $13, $14, $15)
		 ON CONFLICT (workspace_id, slug) DO UPDATE SET
		     display_name = EXCLUDED.display_name,
		     kind = EXCLUDED.kind,
		     transport = EXCLUDED.transport,
		     endpoint = EXCLUDED.endpoint,
		     enabled = EXCLUDED.enabled,
		     tool_manifest = EXCLUDED.tool_manifest,
		     config = EXCLUDED.config,
		     allowed_tools = EXCLUDED.allowed_tools,
		     denied_tools = EXCLUDED.denied_tools,
		     approved_by_user_id = COALESCE(EXCLUDED.approved_by_user_id, workspace_integrations.approved_by_user_id),
		     approved_at = COALESCE(EXCLUDED.approved_at, workspace_integrations.approved_at),
		     connected_by_user_id = COALESCE(EXCLUDED.connected_by_user_id, workspace_integrations.connected_by_user_id),
		     connected_at = COALESCE(EXCLUDED.connected_at, workspace_integrations.connected_at),
		     updated_at = NOW()
		 RETURNING id, created_at, updated_at`,
		integration.WorkspaceID, integration.Slug, integration.DisplayName, integration.Kind,
		integration.Transport, integration.Endpoint, integration.Enabled, integration.ToolManifest,
		integration.Config, integration.AllowedTools, integration.DeniedTools,
		integration.ApprovedBy, integration.ApprovedAt, integration.ConnectedBy, integration.ConnectedAt,
	).Scan(&integration.ID, &integration.CreatedAt, &integration.UpdatedAt); err != nil {
		return err
	}
	return s.upsertWorkspaceIntegrationConnectorProjectionFromV1(ctx, integration)
}

func (s *PostgresStore) DeleteWorkspaceIntegration(ctx context.Context, workspaceID, slug string) (*WorkspaceIntegration, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	integration, err := scanWorkspaceIntegration(tx.QueryRow(ctx,
		`SELECT id, workspace_id, slug, display_name, kind,
		        transport, endpoint, enabled, tool_manifest, config,
		        COALESCE(allowed_tools, ARRAY[]::text[]),
		        COALESCE(denied_tools, ARRAY[]::text[]),
		        approved_by_user_id, approved_at,
		        connected_by_user_id, connected_at,
		        created_at, updated_at
		   FROM workspace_integrations
		  WHERE workspace_id = $1 AND slug = $2
		  FOR UPDATE`, workspaceID, slug).Scan)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workspace_integration_connections WHERE legacy_integration_id = $1`, integration.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workspace_integrations WHERE id = $1`, integration.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return integration, nil
}

func (s *PostgresStore) GetWorkspaceIntegrationCredential(ctx context.Context, integrationID string) (*WorkspaceIntegrationCredential, error) {
	var credential WorkspaceIntegrationCredential
	err := s.pool.QueryRow(ctx,
		`SELECT id, integration_id, secret_enc, refresh_enc, token_type, expires_at, created_at, updated_at
		 FROM workspace_integration_credentials
		 WHERE integration_id = $1`, integrationID).
		Scan(&credential.ID, &credential.IntegrationID, &credential.SecretEnc, &credential.RefreshEnc,
			&credential.TokenType, &credential.ExpiresAt, &credential.CreatedAt, &credential.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

func (s *PostgresStore) SetWorkspaceIntegrationCredential(ctx context.Context, credential *WorkspaceIntegrationCredential) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integration_credentials (
			integration_id, secret_enc, refresh_enc, token_type, expires_at
		)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (integration_id)
		 DO UPDATE SET secret_enc = EXCLUDED.secret_enc,
		               refresh_enc = EXCLUDED.refresh_enc,
		               token_type = EXCLUDED.token_type,
		               expires_at = EXCLUDED.expires_at,
		               updated_at = NOW()
		 RETURNING id, created_at, updated_at`,
		credential.IntegrationID, credential.SecretEnc, credential.RefreshEnc,
		credential.TokenType, credential.ExpiresAt,
	).Scan(&credential.ID, &credential.CreatedAt, &credential.UpdatedAt)
}

func (s *PostgresStore) GetWorkspaceIntegrationConnectionCredential(ctx context.Context, connectionID string) (*WorkspaceIntegrationConnectionCredential, error) {
	var credential WorkspaceIntegrationConnectionCredential
	err := s.pool.QueryRow(ctx,
		`SELECT id, connection_id, secret_enc, refresh_enc, token_type, expires_at, created_at, updated_at
		 FROM workspace_integration_connection_credentials
		 WHERE connection_id = $1`, connectionID).
		Scan(&credential.ID, &credential.ConnectionID, &credential.SecretEnc, &credential.RefreshEnc,
			&credential.TokenType, &credential.ExpiresAt, &credential.CreatedAt, &credential.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

func (s *PostgresStore) SetWorkspaceIntegrationConnectionCredential(ctx context.Context, credential *WorkspaceIntegrationConnectionCredential) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integration_connection_credentials (
			connection_id, secret_enc, refresh_enc, token_type, expires_at
		)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (connection_id)
		 DO UPDATE SET secret_enc = EXCLUDED.secret_enc,
		               refresh_enc = EXCLUDED.refresh_enc,
		               token_type = EXCLUDED.token_type,
		               expires_at = EXCLUDED.expires_at,
		               updated_at = NOW()
		 RETURNING id, created_at, updated_at`,
		credential.ConnectionID, credential.SecretEnc, credential.RefreshEnc,
		credential.TokenType, credential.ExpiresAt,
	).Scan(&credential.ID, &credential.CreatedAt, &credential.UpdatedAt)
}

func scanWorkspaceIntegrationConnectionCredential(scan func(dest ...any) error) (*WorkspaceIntegrationConnectionCredential, error) {
	var credential WorkspaceIntegrationConnectionCredential
	err := scan(&credential.ID, &credential.ConnectionID, &credential.SecretEnc, &credential.RefreshEnc,
		&credential.TokenType, &credential.ExpiresAt, &credential.CreatedAt, &credential.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

func scanWorkspaceIntegrationSource(scan func(dest ...any) error) (*WorkspaceIntegrationSource, error) {
	var source WorkspaceIntegrationSource
	var config []byte
	err := scan(&source.ID, &source.WorkspaceID, &source.Slug, &source.DisplayName,
		&source.Kind, &source.Importer, &config, &source.CreatedAt, &source.UpdatedAt)
	if err != nil {
		return nil, err
	}
	source.Config = json.RawMessage(config)
	if source.Config == nil {
		source.Config = json.RawMessage("{}")
	}
	return &source, nil
}

func scanWorkspaceIntegrationConnection(scan func(dest ...any) error) (*WorkspaceIntegrationConnection, error) {
	var connection WorkspaceIntegrationConnection
	var config []byte
	err := scan(&connection.ID, &connection.WorkspaceID, &connection.SourceID,
		&connection.LegacyIntegrationID, &connection.Slug, &connection.DisplayName,
		&connection.Scope, &connection.OwnerUserID, &connection.CredentialState,
		&connection.Enabled, &config, &connection.CreatedAt, &connection.UpdatedAt)
	if err != nil {
		return nil, err
	}
	connection.Config = json.RawMessage(config)
	if connection.Config == nil {
		connection.Config = json.RawMessage("{}")
	}
	return &connection, nil
}

func scanWorkspaceIntegrationToolSnapshot(scan func(dest ...any) error) (*WorkspaceIntegrationToolSnapshot, error) {
	var snapshot WorkspaceIntegrationToolSnapshot
	var inputSchema, outputSchema, annotations, provenance []byte
	err := scan(&snapshot.ID, &snapshot.WorkspaceID, &snapshot.ConnectionID,
		&snapshot.ToolName, &snapshot.ToolAddress, &snapshot.LegacyToolID,
		&snapshot.Description, &inputSchema, &outputSchema, &annotations,
		&snapshot.Access, &snapshot.Source, &provenance, &snapshot.ToolsSyncedAt,
		&snapshot.StaleAfter, &snapshot.CreatedAt, &snapshot.UpdatedAt)
	if err != nil {
		return nil, err
	}
	snapshot.InputSchema = json.RawMessage(inputSchema)
	if snapshot.InputSchema == nil {
		snapshot.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
	}
	if outputSchema != nil {
		snapshot.OutputSchema = json.RawMessage(outputSchema)
	}
	snapshot.Annotations = json.RawMessage(annotations)
	if snapshot.Annotations == nil {
		snapshot.Annotations = json.RawMessage("{}")
	}
	snapshot.Provenance = json.RawMessage(provenance)
	if snapshot.Provenance == nil {
		snapshot.Provenance = json.RawMessage("{}")
	}
	return &snapshot, nil
}

func scanWorkspaceIntegrationToolPolicy(scan func(dest ...any) error) (*WorkspaceIntegrationToolPolicy, error) {
	var policy WorkspaceIntegrationToolPolicy
	err := scan(&policy.ID, &policy.WorkspaceID, &policy.ConnectionID,
		&policy.ToolName, &policy.Policy, &policy.Source,
		&policy.CreatedAt, &policy.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

func (s *PostgresStore) UpsertWorkspaceIntegrationSource(ctx context.Context, source *WorkspaceIntegrationSource) error {
	if source.Config == nil {
		source.Config = json.RawMessage("{}")
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integration_sources (
			workspace_id, slug, display_name, kind, importer, config
		)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (workspace_id, slug) DO UPDATE SET
		     display_name = EXCLUDED.display_name,
		     kind = EXCLUDED.kind,
		     importer = EXCLUDED.importer,
		     config = EXCLUDED.config,
		     updated_at = NOW()
		 RETURNING id, created_at, updated_at`,
		source.WorkspaceID, source.Slug, source.DisplayName, source.Kind, source.Importer, source.Config,
	).Scan(&source.ID, &source.CreatedAt, &source.UpdatedAt)
}

func (s *PostgresStore) UpsertWorkspaceIntegrationConnection(ctx context.Context, connection *WorkspaceIntegrationConnection) error {
	if connection.Scope == "" {
		connection.Scope = "workspace"
	}
	if connection.CredentialState == "" {
		connection.CredentialState = "connected"
	}
	if connection.Config == nil {
		connection.Config = json.RawMessage("{}")
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integration_connections (
			workspace_id, source_id, legacy_integration_id, slug, display_name,
			scope, owner_user_id, credential_state, enabled, config
		)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (source_id, scope, slug) DO UPDATE SET
		     legacy_integration_id = COALESCE(EXCLUDED.legacy_integration_id, workspace_integration_connections.legacy_integration_id),
		     display_name = EXCLUDED.display_name,
		     owner_user_id = EXCLUDED.owner_user_id,
		     credential_state = EXCLUDED.credential_state,
		     enabled = EXCLUDED.enabled,
		     config = EXCLUDED.config,
		     updated_at = NOW()
		 RETURNING id, created_at, updated_at`,
		connection.WorkspaceID, connection.SourceID, connection.LegacyIntegrationID,
		connection.Slug, connection.DisplayName, connection.Scope, connection.OwnerUserID,
		connection.CredentialState, connection.Enabled, connection.Config,
	).Scan(&connection.ID, &connection.CreatedAt, &connection.UpdatedAt)
}

func (s *PostgresStore) ListWorkspaceIntegrationConnectionsByLegacyIntegration(ctx context.Context, integrationID string) ([]WorkspaceIntegrationConnection, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, workspace_id, source_id, legacy_integration_id, slug, display_name,
		        scope, owner_user_id, credential_state, enabled, config, created_at, updated_at
		   FROM workspace_integration_connections
		  WHERE legacy_integration_id = $1
		  ORDER BY slug, id`, integrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var connections []WorkspaceIntegrationConnection
	for rows.Next() {
		connection, err := scanWorkspaceIntegrationConnection(rows.Scan)
		if err != nil {
			return nil, err
		}
		connections = append(connections, *connection)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return connections, nil
}

func (s *PostgresStore) DeleteWorkspaceIntegrationConnection(ctx context.Context, workspaceID, connectionSlug string) (*WorkspaceIntegrationConnectorProjection, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var source WorkspaceIntegrationSource
	var connection WorkspaceIntegrationConnection
	var sourceConfig, connectionConfig []byte
	if err := tx.QueryRow(ctx,
		`SELECT src.id, src.workspace_id, src.slug, src.display_name,
		        src.kind, src.importer, src.config, src.created_at, src.updated_at,
		        conn.id, conn.workspace_id, conn.source_id, conn.legacy_integration_id,
		        conn.slug, conn.display_name, conn.scope, conn.owner_user_id,
		        conn.credential_state, conn.enabled, conn.config, conn.created_at, conn.updated_at
		   FROM workspace_integration_connections conn
		   JOIN workspace_integration_sources src ON src.id = conn.source_id
		  WHERE conn.workspace_id = $1
		    AND (conn.slug = $2 OR conn.id::text = $2)
		  FOR UPDATE OF conn`, workspaceID, connectionSlug).Scan(
		&source.ID, &source.WorkspaceID, &source.Slug, &source.DisplayName,
		&source.Kind, &source.Importer, &sourceConfig, &source.CreatedAt, &source.UpdatedAt,
		&connection.ID, &connection.WorkspaceID, &connection.SourceID, &connection.LegacyIntegrationID,
		&connection.Slug, &connection.DisplayName, &connection.Scope, &connection.OwnerUserID,
		&connection.CredentialState, &connection.Enabled, &connectionConfig, &connection.CreatedAt, &connection.UpdatedAt,
	); err != nil {
		return nil, err
	}
	source.Config = json.RawMessage(sourceConfig)
	if source.Config == nil {
		source.Config = json.RawMessage("{}")
	}
	connection.Config = json.RawMessage(connectionConfig)
	if connection.Config == nil {
		connection.Config = json.RawMessage("{}")
	}

	snapshotRows, err := tx.Query(ctx,
		`SELECT id, workspace_id, connection_id, tool_name, tool_address, legacy_tool_id,
		        description, input_schema, output_schema, annotations, access, source,
		        provenance, tools_synced_at, stale_after, created_at, updated_at
		   FROM workspace_integration_tool_snapshots
		  WHERE connection_id = $1
		  ORDER BY tool_name`, connection.ID)
	if err != nil {
		return nil, err
	}
	var snapshots []WorkspaceIntegrationToolSnapshot
	for snapshotRows.Next() {
		snapshot, err := scanWorkspaceIntegrationToolSnapshot(snapshotRows.Scan)
		if err != nil {
			snapshotRows.Close()
			return nil, err
		}
		snapshots = append(snapshots, *snapshot)
	}
	if err := snapshotRows.Err(); err != nil {
		snapshotRows.Close()
		return nil, err
	}
	snapshotRows.Close()

	policyRows, err := tx.Query(ctx,
		`SELECT id, workspace_id, connection_id, tool_name, policy, source, created_at, updated_at
		   FROM workspace_integration_tool_policies
		  WHERE connection_id = $1
		  ORDER BY tool_name`, connection.ID)
	if err != nil {
		return nil, err
	}
	var policies []WorkspaceIntegrationToolPolicy
	for policyRows.Next() {
		policy, err := scanWorkspaceIntegrationToolPolicy(policyRows.Scan)
		if err != nil {
			policyRows.Close()
			return nil, err
		}
		policies = append(policies, *policy)
	}
	if err := policyRows.Err(); err != nil {
		policyRows.Close()
		return nil, err
	}
	policyRows.Close()

	if _, err := tx.Exec(ctx, `DELETE FROM workspace_integration_connections WHERE id = $1`, connection.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &WorkspaceIntegrationConnectorProjection{
		Source:     source,
		Connection: connection,
		Tools:      snapshots,
		Policies:   policies,
	}, nil
}

func (s *PostgresStore) ReplaceWorkspaceIntegrationToolSnapshots(ctx context.Context, connectionID string, snapshots []WorkspaceIntegrationToolSnapshot) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM workspace_integration_tool_snapshots WHERE connection_id = $1`, connectionID); err != nil {
		return err
	}
	for i := range snapshots {
		snapshot := &snapshots[i]
		if snapshot.InputSchema == nil {
			snapshot.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
		}
		if snapshot.Annotations == nil {
			snapshot.Annotations = json.RawMessage("{}")
		}
		if snapshot.Provenance == nil {
			snapshot.Provenance = json.RawMessage("{}")
		}
		if snapshot.ToolsSyncedAt.IsZero() {
			snapshot.ToolsSyncedAt = time.Now().UTC()
		}
		if snapshot.ConnectionID == "" {
			snapshot.ConnectionID = connectionID
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO workspace_integration_tool_snapshots (
				workspace_id, connection_id, tool_name, tool_address, legacy_tool_id,
				description, input_schema, output_schema, annotations, access,
				source, provenance, tools_synced_at, stale_after
			)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			 RETURNING id, created_at, updated_at`,
			snapshot.WorkspaceID, snapshot.ConnectionID, snapshot.ToolName,
			snapshot.ToolAddress, snapshot.LegacyToolID, snapshot.Description,
			snapshot.InputSchema, nullableRawMessage(snapshot.OutputSchema), snapshot.Annotations,
			snapshot.Access, snapshot.Source, snapshot.Provenance, snapshot.ToolsSyncedAt,
			snapshot.StaleAfter,
		).Scan(&snapshot.ID, &snapshot.CreatedAt, &snapshot.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) SetWorkspaceIntegrationToolPolicy(ctx context.Context, policy *WorkspaceIntegrationToolPolicy) error {
	if policy.Source == "" {
		policy.Source = "api"
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integration_tool_policies (
			workspace_id, connection_id, tool_name, policy, source
		)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (connection_id, tool_name) DO UPDATE SET
		     policy = EXCLUDED.policy,
		     source = EXCLUDED.source,
		     updated_at = NOW()
		 RETURNING id, created_at, updated_at`,
		policy.WorkspaceID, policy.ConnectionID, policy.ToolName, policy.Policy, policy.Source,
	).Scan(&policy.ID, &policy.CreatedAt, &policy.UpdatedAt)
}

func (s *PostgresStore) ReplaceWorkspaceIntegrationToolPolicies(ctx context.Context, connectionID string, policies []WorkspaceIntegrationToolPolicy) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM workspace_integration_tool_policies WHERE connection_id = $1`, connectionID); err != nil {
		return err
	}
	for i := range policies {
		policy := &policies[i]
		if policy.Source == "" {
			policy.Source = "api"
		}
		if policy.ConnectionID == "" {
			policy.ConnectionID = connectionID
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO workspace_integration_tool_policies (
				workspace_id, connection_id, tool_name, policy, source
			)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id, created_at, updated_at`,
			policy.WorkspaceID, policy.ConnectionID, policy.ToolName, policy.Policy, policy.Source,
		).Scan(&policy.ID, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ListWorkspaceIntegrationConnectorProjections(ctx context.Context, workspaceID string) ([]WorkspaceIntegrationConnectorProjection, error) {
	sources, err := s.listWorkspaceIntegrationSources(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	connections, err := s.listWorkspaceIntegrationConnections(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	tools, err := s.listWorkspaceIntegrationToolSnapshots(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	policies, err := s.listWorkspaceIntegrationToolPolicies(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	sourcesByID := make(map[string]WorkspaceIntegrationSource, len(sources))
	for _, source := range sources {
		sourcesByID[source.ID] = source
	}
	indexByConnectionID := make(map[string]int, len(connections))
	projections := make([]WorkspaceIntegrationConnectorProjection, 0, len(connections))
	for _, connection := range connections {
		source, ok := sourcesByID[connection.SourceID]
		if !ok {
			continue
		}
		indexByConnectionID[connection.ID] = len(projections)
		projections = append(projections, WorkspaceIntegrationConnectorProjection{
			Source:     source,
			Connection: connection,
		})
	}
	for _, tool := range tools {
		if index, ok := indexByConnectionID[tool.ConnectionID]; ok {
			projections[index].Tools = append(projections[index].Tools, tool)
		}
	}
	for _, policy := range policies {
		if index, ok := indexByConnectionID[policy.ConnectionID]; ok {
			projections[index].Policies = append(projections[index].Policies, policy)
		}
	}
	return projections, nil
}

func (s *PostgresStore) RecordWorkspaceIntegrationCallEvent(ctx context.Context, event *WorkspaceIntegrationCallEvent) error {
	if event == nil {
		return errors.New("workspace integration call event is nil")
	}
	if event.WorkspaceID == "" {
		return errors.New("workspace integration call event workspace_id is required")
	}
	if event.IntegrationSlug == "" {
		event.IntegrationSlug = "unknown"
	}
	if event.ToolName == "" {
		event.ToolName = "unknown"
	}
	if event.ToolID == "" {
		event.ToolID = event.IntegrationSlug + "." + event.ToolName
	}
	if event.CallMode == "" {
		event.CallMode = "direct"
	}
	if event.Transport == "" {
		event.Transport = "unknown"
	}
	if event.Access != "write" {
		event.Access = "read"
	}
	if event.Status != "error" {
		event.Status = "success"
	}
	if event.LatencyMS < 0 {
		event.LatencyMS = 0
	}
	if event.RetryCount < 0 {
		event.RetryCount = 0
	}
	if event.ArgShape == nil {
		event.ArgShape = json.RawMessage("{}")
	}
	if event.SampleRate <= 0 || event.SampleRate > 1 {
		event.SampleRate = 1
	}
	if event.DetailLevel != "audit" {
		event.DetailLevel = "telemetry"
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integration_call_events (
			account_id, workspace_id, machine_id, integration_id, integration_slug,
			tool_name, tool_id, tool_address, call_mode, transport, access, status,
			failure_class, upstream_status, latency_ms, ocm_overhead_ms,
			upstream_latency_ms, retry_count, retry_after_ms, retryable, terminal,
			arg_keys, arg_shape, sample_rate, detail_level
		)
		 VALUES (
			COALESCE(NULLIF($1, 0), (SELECT account_id FROM workspaces WHERE id = $2)),
			$2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25
		)
		 RETURNING id, account_id, created_at`,
		event.AccountID, event.WorkspaceID, event.MachineID, event.IntegrationID,
		event.IntegrationSlug, event.ToolName, event.ToolID, event.ToolAddress,
		event.CallMode, event.Transport, event.Access, event.Status,
		event.FailureClass, event.UpstreamStatus, event.LatencyMS,
		event.OCMOverheadMS, event.UpstreamLatencyMS, event.RetryCount,
		event.RetryAfterMS, event.Retryable, event.Terminal, event.ArgKeys,
		event.ArgShape, event.SampleRate, event.DetailLevel,
	).Scan(&event.ID, &event.AccountID, &event.CreatedAt)
}

func (s *PostgresStore) ListWorkspaceIntegrationToolHealth(ctx context.Context, accountID int, workspaceID string, query WorkspaceIntegrationHealthQuery) ([]WorkspaceIntegrationToolHealth, error) {
	since := query.Since
	if since.IsZero() {
		since = time.Now().UTC().Add(-24 * time.Hour)
	}
	limit := query.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`WITH scoped AS (
			SELECT *
			  FROM workspace_integration_call_events
			 WHERE account_id = $1
			   AND workspace_id = $2
			   AND created_at >= $3
			   AND detail_level = 'telemetry'
		),
		weighted AS (
			SELECT *,
			       CASE
			           WHEN status = 'success' AND sample_rate > 0 AND sample_rate < 1 THEN 1.0 / sample_rate
			           ELSE 1.0
			       END AS call_weight
			  FROM scoped
		),
		aggregates AS (
			SELECT
				COALESCE(tool_address, tool_id) AS aggregate_key,
				tool_id,
				tool_address,
				integration_slug,
				tool_name,
				transport,
				access,
				ROUND(COALESCE(SUM(call_weight), 0))::BIGINT AS total_calls,
				ROUND(COALESCE(SUM(call_weight) FILTER (WHERE status = 'success'), 0))::BIGINT AS success_calls,
				COUNT(*) FILTER (WHERE status = 'error') AS error_calls,
				COALESCE(
					(SUM(call_weight) FILTER (WHERE status = 'success'))::DOUBLE PRECISION /
					NULLIF(SUM(call_weight)::DOUBLE PRECISION, 0),
					0
				) AS success_rate,
				COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms), 0)::DOUBLE PRECISION AS p50_latency_ms,
				COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::DOUBLE PRECISION AS p95_latency_ms,
				COALESCE(AVG(retry_count), 0)::DOUBLE PRECISION AS avg_retry_count
			  FROM weighted
			 GROUP BY COALESCE(tool_address, tool_id), tool_id, tool_address, integration_slug, tool_name, transport, access
		),
		failure_counts AS (
			SELECT COALESCE(tool_address, tool_id) AS aggregate_key, failure_class, COUNT(*) AS count
			  FROM scoped
			 WHERE status = 'error'
			   AND failure_class IS NOT NULL
			   AND failure_class <> ''
			 GROUP BY COALESCE(tool_address, tool_id), failure_class
		),
		failures AS (
			SELECT aggregate_key,
			       jsonb_agg(
			           jsonb_build_object('class', failure_class, 'count', count)
			           ORDER BY count DESC, failure_class
			       ) AS top_failure_classes
			  FROM failure_counts
			 GROUP BY aggregate_key
		)
		SELECT
			a.tool_id, a.tool_address, a.integration_slug, a.tool_name,
			a.transport, a.access, a.total_calls, a.success_calls, a.error_calls,
			a.success_rate, a.p50_latency_ms, a.p95_latency_ms, a.avg_retry_count,
			COALESCE(f.top_failure_classes, '[]'::jsonb)
		  FROM aggregates a
		  LEFT JOIN failures f ON f.aggregate_key = a.aggregate_key
		 ORDER BY a.error_calls DESC, a.total_calls DESC, a.tool_id
		 LIMIT $4`,
		accountID, workspaceID, since, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkspaceIntegrationToolHealth
	for rows.Next() {
		var item WorkspaceIntegrationToolHealth
		var failuresRaw []byte
		if err := rows.Scan(
			&item.ToolID, &item.ToolAddress, &item.IntegrationSlug, &item.ToolName,
			&item.Transport, &item.Access, &item.TotalCalls, &item.SuccessCalls,
			&item.ErrorCalls, &item.SuccessRate, &item.P50LatencyMS,
			&item.P95LatencyMS, &item.AvgRetryCount, &failuresRaw,
		); err != nil {
			return nil, err
		}
		if len(failuresRaw) > 0 {
			if err := json.Unmarshal(failuresRaw, &item.TopFailureClasses); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListWorkspaceIntegrationGuidanceOverlays(ctx context.Context, accountID int, workspaceID, status string) ([]WorkspaceIntegrationGuidanceOverlay, error) {
	status = strings.TrimSpace(status)
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, workspace_id, tool_id, tool_address,
		        integration_slug, tool_name, status, version, guidance,
		        source_failure_class, sanitized_pattern, created_by,
		        approved_by, approved_at, created_at, updated_at
		   FROM workspace_integration_guidance_overlays
		  WHERE account_id = $1
		    AND workspace_id = $2
		    AND ($3 = '' OR status = $3)
		  ORDER BY tool_id, version DESC, created_at DESC`,
		accountID, workspaceID, status,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkspaceIntegrationGuidanceOverlay
	for rows.Next() {
		overlay, err := scanWorkspaceIntegrationGuidanceOverlay(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *overlay)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateWorkspaceIntegrationGuidanceOverlay(ctx context.Context, overlay *WorkspaceIntegrationGuidanceOverlay) error {
	if overlay == nil {
		return errors.New("workspace integration guidance overlay is nil")
	}
	if overlay.WorkspaceID == "" {
		return errors.New("workspace integration guidance overlay workspace_id is required")
	}
	if strings.TrimSpace(overlay.ToolID) == "" {
		return errors.New("workspace integration guidance overlay tool_id is required")
	}
	if strings.TrimSpace(overlay.Guidance) == "" {
		return errors.New("workspace integration guidance overlay guidance is required")
	}
	if overlay.IntegrationSlug == "" || overlay.ToolName == "" {
		parts := strings.SplitN(overlay.ToolID, ".", 2)
		if overlay.IntegrationSlug == "" && len(parts) > 0 {
			overlay.IntegrationSlug = parts[0]
		}
		if overlay.ToolName == "" && len(parts) == 2 {
			overlay.ToolName = parts[1]
		}
	}
	if overlay.IntegrationSlug == "" {
		overlay.IntegrationSlug = "unknown"
	}
	if overlay.ToolName == "" {
		overlay.ToolName = overlay.ToolID
	}
	if overlay.Status == "" {
		overlay.Status = "draft"
	}
	if overlay.Status != "draft" && overlay.Status != "approved" && overlay.Status != "rejected" && overlay.Status != "archived" {
		return fmt.Errorf("unsupported workspace integration guidance status %q", overlay.Status)
	}
	if overlay.SanitizedPattern == nil {
		overlay.SanitizedPattern = json.RawMessage("{}")
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_integration_guidance_overlays (
			account_id, workspace_id, tool_id, tool_address, integration_slug,
			tool_name, status, version, guidance, source_failure_class,
			sanitized_pattern, created_by, approved_by, approved_at
		)
		VALUES (
			COALESCE(NULLIF($1, 0), (SELECT account_id FROM workspaces WHERE id = $2)),
			$2, $3, $4, $5, $6, $7,
			COALESCE(NULLIF($8, 0), (
				SELECT COALESCE(MAX(version), 0) + 1
				  FROM workspace_integration_guidance_overlays
				 WHERE workspace_id = $2
				   AND tool_id = $3
				   AND COALESCE(tool_address, tool_id) = COALESCE($4, $3)
			)),
			$9, $10, $11, $12, $13, $14
		)
		RETURNING id, account_id, workspace_id, tool_id, tool_address,
		          integration_slug, tool_name, status, version, guidance,
		          source_failure_class, sanitized_pattern, created_by,
		          approved_by, approved_at, created_at, updated_at`,
		overlay.AccountID, overlay.WorkspaceID, overlay.ToolID, overlay.ToolAddress,
		overlay.IntegrationSlug, overlay.ToolName, overlay.Status, overlay.Version,
		overlay.Guidance, overlay.SourceFailureClass, overlay.SanitizedPattern,
		overlay.CreatedBy, overlay.ApprovedBy, overlay.ApprovedAt,
	).Scan(
		&overlay.ID, &overlay.AccountID, &overlay.WorkspaceID, &overlay.ToolID,
		&overlay.ToolAddress, &overlay.IntegrationSlug, &overlay.ToolName,
		&overlay.Status, &overlay.Version, &overlay.Guidance,
		&overlay.SourceFailureClass, &overlay.SanitizedPattern, &overlay.CreatedBy,
		&overlay.ApprovedBy, &overlay.ApprovedAt, &overlay.CreatedAt, &overlay.UpdatedAt,
	)
}

func (s *PostgresStore) CreateWorkspaceIntegrationGuidanceDraftsFromTelemetry(ctx context.Context, accountID int, workspaceID string, since time.Time, limit int, createdBy *int) ([]WorkspaceIntegrationGuidanceOverlay, error) {
	if since.IsZero() {
		since = time.Now().UTC().Add(-7 * 24 * time.Hour)
	}
	if limit <= 0 || limit > 25 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx,
		`WITH candidates AS (
			SELECT
				COALESCE(tool_address, tool_id) AS aggregate_key,
				tool_id,
				tool_address,
				integration_slug,
				tool_name,
				failure_class,
				COUNT(*)::INT AS failure_count,
				BOOL_OR(arg_shape::text LIKE '%"repo_format"%"bare_name"%') AS repo_bare_name,
				BOOL_OR(arg_shape::text LIKE '%"date_parse"%"failed"%') AS date_parse_failed
			  FROM workspace_integration_call_events
			 WHERE account_id = $1
			   AND workspace_id = $2
			   AND created_at >= $3
			   AND detail_level = 'telemetry'
			   AND status = 'error'
			   AND failure_class IS NOT NULL
			   AND failure_class <> ''
			 GROUP BY COALESCE(tool_address, tool_id), tool_id, tool_address, integration_slug, tool_name, failure_class
			 ORDER BY COUNT(*) DESC, tool_id, aggregate_key, failure_class
			 LIMIT $4
		),
		drafts AS (
			SELECT
				$1::INT AS account_id,
				$2::UUID AS workspace_id,
				tool_id,
				tool_address,
				integration_slug,
				tool_name,
				failure_class,
				COALESCE((
					SELECT MAX(version)
					  FROM workspace_integration_guidance_overlays existing
					 WHERE existing.workspace_id = $2
					   AND existing.tool_id = candidates.tool_id
					   AND COALESCE(existing.tool_address, existing.tool_id) = candidates.aggregate_key
				), 0) + ROW_NUMBER() OVER (
					PARTITION BY candidates.tool_id, candidates.aggregate_key
					ORDER BY failure_class
				)::INT AS version,
				CASE
					WHEN repo_bare_name THEN 'Repository arguments should use owner/name format; bare repository names have failed for this tool.'
					WHEN date_parse_failed THEN 'Date and time arguments should use RFC3339 timestamps or YYYY-MM-DD dates accepted by the tool schema.'
					WHEN failure_class = 'invalid_arguments' THEN 'Check required arguments, enum values, and schema types before calling this tool.'
					WHEN failure_class = 'rate_limited' THEN 'Narrow or batch requests where possible, then retry only after the returned retry_after delay.'
					WHEN failure_class = 'credential_not_configured' THEN 'Reconnect this integration before retrying the tool.'
					ELSE 'Review the tool schema and the recorded failure class before retrying this tool.'
				END AS guidance,
				jsonb_build_object(
					'failure_class', failure_class,
					'failure_count', failure_count,
					'repo_format_bare_name', repo_bare_name,
					'date_parse_failed', date_parse_failed
				) AS sanitized_pattern
			  FROM candidates
		)
		INSERT INTO workspace_integration_guidance_overlays (
			account_id, workspace_id, tool_id, tool_address, integration_slug,
			tool_name, status, version, guidance, source_failure_class,
			sanitized_pattern, created_by
		)
		SELECT
			account_id, workspace_id, tool_id, tool_address, integration_slug,
			tool_name, 'draft', version, guidance, failure_class,
			sanitized_pattern, $5
		  FROM drafts
		RETURNING id, account_id, workspace_id, tool_id, tool_address,
		          integration_slug, tool_name, status, version, guidance,
		          source_failure_class, sanitized_pattern, created_by,
		          approved_by, approved_at, created_at, updated_at`,
		accountID, workspaceID, since, limit, createdBy,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var overlays []WorkspaceIntegrationGuidanceOverlay
	for rows.Next() {
		overlay, err := scanWorkspaceIntegrationGuidanceOverlay(rows.Scan)
		if err != nil {
			return nil, err
		}
		overlays = append(overlays, *overlay)
	}
	return overlays, rows.Err()
}

func (s *PostgresStore) ApproveWorkspaceIntegrationGuidanceOverlay(ctx context.Context, accountID int, workspaceID, overlayID string, approvedBy int) (*WorkspaceIntegrationGuidanceOverlay, error) {
	overlay, err := scanWorkspaceIntegrationGuidanceOverlay(func(dest ...interface{}) error {
		return s.pool.QueryRow(ctx,
			`UPDATE workspace_integration_guidance_overlays
			    SET status = 'approved',
			        approved_by = $4,
			        approved_at = NOW(),
			        updated_at = NOW()
			  WHERE account_id = $1
			    AND workspace_id = $2
			    AND id = $3
			  RETURNING id, account_id, workspace_id, tool_id, tool_address,
			            integration_slug, tool_name, status, version, guidance,
			            source_failure_class, sanitized_pattern, created_by,
			            approved_by, approved_at, created_at, updated_at`,
			accountID, workspaceID, overlayID, approvedBy,
		).Scan(dest...)
	})
	if err != nil {
		return nil, err
	}
	return overlay, nil
}

func scanWorkspaceIntegrationGuidanceOverlay(scan func(dest ...interface{}) error) (*WorkspaceIntegrationGuidanceOverlay, error) {
	var overlay WorkspaceIntegrationGuidanceOverlay
	if err := scan(
		&overlay.ID, &overlay.AccountID, &overlay.WorkspaceID, &overlay.ToolID,
		&overlay.ToolAddress, &overlay.IntegrationSlug, &overlay.ToolName,
		&overlay.Status, &overlay.Version, &overlay.Guidance,
		&overlay.SourceFailureClass, &overlay.SanitizedPattern, &overlay.CreatedBy,
		&overlay.ApprovedBy, &overlay.ApprovedAt, &overlay.CreatedAt, &overlay.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if overlay.SanitizedPattern == nil {
		overlay.SanitizedPattern = json.RawMessage("{}")
	}
	return &overlay, nil
}

func (s *PostgresStore) listWorkspaceIntegrationSources(ctx context.Context, workspaceID string) ([]WorkspaceIntegrationSource, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, workspace_id, slug, display_name, kind, importer, config, created_at, updated_at
		 FROM workspace_integration_sources
		 WHERE workspace_id = $1
		 ORDER BY display_name, slug`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []WorkspaceIntegrationSource
	for rows.Next() {
		source, err := scanWorkspaceIntegrationSource(rows.Scan)
		if err != nil {
			return nil, err
		}
		sources = append(sources, *source)
	}
	return sources, rows.Err()
}

func (s *PostgresStore) listWorkspaceIntegrationConnections(ctx context.Context, workspaceID string) ([]WorkspaceIntegrationConnection, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, workspace_id, source_id, legacy_integration_id, slug, display_name,
		        scope, owner_user_id, credential_state, enabled, config, created_at, updated_at
		 FROM workspace_integration_connections
		 WHERE workspace_id = $1
		 ORDER BY display_name, slug`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connections []WorkspaceIntegrationConnection
	for rows.Next() {
		connection, err := scanWorkspaceIntegrationConnection(rows.Scan)
		if err != nil {
			return nil, err
		}
		connections = append(connections, *connection)
	}
	return connections, rows.Err()
}

func (s *PostgresStore) listWorkspaceIntegrationToolSnapshots(ctx context.Context, workspaceID string) ([]WorkspaceIntegrationToolSnapshot, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, workspace_id, connection_id, tool_name, tool_address, legacy_tool_id,
		        description, input_schema, output_schema, annotations, access, source,
		        provenance, tools_synced_at, stale_after, created_at, updated_at
		 FROM workspace_integration_tool_snapshots
		 WHERE workspace_id = $1
		 ORDER BY connection_id, tool_name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snapshots []WorkspaceIntegrationToolSnapshot
	for rows.Next() {
		snapshot, err := scanWorkspaceIntegrationToolSnapshot(rows.Scan)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, *snapshot)
	}
	return snapshots, rows.Err()
}

func (s *PostgresStore) listWorkspaceIntegrationToolPolicies(ctx context.Context, workspaceID string) ([]WorkspaceIntegrationToolPolicy, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, workspace_id, connection_id, tool_name, policy, source, created_at, updated_at
		 FROM workspace_integration_tool_policies
		 WHERE workspace_id = $1
		 ORDER BY connection_id, tool_name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []WorkspaceIntegrationToolPolicy
	for rows.Next() {
		policy, err := scanWorkspaceIntegrationToolPolicy(rows.Scan)
		if err != nil {
			return nil, err
		}
		policies = append(policies, *policy)
	}
	return policies, rows.Err()
}

func nullableRawMessage(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

type workspaceIntegrationV1ManifestTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Parameters   json.RawMessage `json:"parameters,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	Schema       json.RawMessage `json:"schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	Request      json.RawMessage `json:"request,omitempty"`
	Access       string          `json:"access,omitempty"`
	Source       string          `json:"source,omitempty"`
}

func (s *PostgresStore) upsertWorkspaceIntegrationConnectorProjectionFromV1(ctx context.Context, integration *WorkspaceIntegration) error {
	if integration == nil || integration.ID == "" || integration.WorkspaceID == "" {
		return nil
	}
	var manifestTools []workspaceIntegrationV1ManifestTool
	if len(integration.ToolManifest) > 0 {
		if err := json.Unmarshal(integration.ToolManifest, &manifestTools); err != nil {
			return fmt.Errorf("parse workspace integration tool manifest projection: %w", err)
		}
	}
	groups := workspaceIntegrationConnectorProjectionGroups(*integration, manifestTools)
	legacyIntegrationID := integration.ID
	keptConnectionIDs := map[string]struct{}{}
	for _, group := range groups {
		source := group.Source
		if err := s.UpsertWorkspaceIntegrationSource(ctx, &source); err != nil {
			return fmt.Errorf("upsert workspace integration source projection: %w", err)
		}
		connectionConfigPayload := workspaceIntegrationConnectorConnectionConfigPayload(*integration, source.Slug)
		connectionConfig, err := json.Marshal(connectionConfigPayload)
		if err != nil {
			return fmt.Errorf("marshal workspace integration connection projection config: %w", err)
		}
		connection := WorkspaceIntegrationConnection{
			WorkspaceID:         integration.WorkspaceID,
			SourceID:            source.ID,
			LegacyIntegrationID: &legacyIntegrationID,
			Slug:                integration.Slug,
			DisplayName:         workspaceIntegrationConnectorConnectionDisplayName(*integration, source.Slug),
			Scope:               "workspace",
			CredentialState:     workspaceIntegrationConnectorCredentialState(*integration),
			Enabled:             integration.Enabled,
			Config:              connectionConfig,
		}
		if err := s.UpsertWorkspaceIntegrationConnection(ctx, &connection); err != nil {
			return fmt.Errorf("upsert workspace integration connection projection: %w", err)
		}
		keptConnectionIDs[connection.ID] = struct{}{}

		snapshots := make([]WorkspaceIntegrationToolSnapshot, 0, len(group.Tools))
		policies := make([]WorkspaceIntegrationToolPolicy, 0, len(group.Tools))
		now := time.Now().UTC()
		staleAfter := now.Add(7 * 24 * time.Hour)
		for _, tool := range group.Tools {
			tool.Name = strings.TrimSpace(tool.Name)
			if tool.Name == "" {
				continue
			}
			legacyToolID := integration.Slug + "." + tool.Name
			provenancePayload := workspaceIntegrationConnectorToolProvenancePayload(*integration, source.Slug, legacyToolID, tool)
			provenance, err := json.Marshal(provenancePayload)
			if err != nil {
				return fmt.Errorf("marshal workspace integration snapshot provenance: %w", err)
			}
			snapshots = append(snapshots, WorkspaceIntegrationToolSnapshot{
				WorkspaceID:   integration.WorkspaceID,
				ConnectionID:  connection.ID,
				ToolName:      tool.Name,
				ToolAddress:   workspaceIntegrationConnectorToolAddress(integration.WorkspaceID, source.Slug, connection.Slug, tool.Name),
				LegacyToolID:  &legacyToolID,
				Description:   tool.Description,
				InputSchema:   workspaceIntegrationConnectorToolInputSchema(tool),
				OutputSchema:  tool.OutputSchema,
				Annotations:   workspaceIntegrationConnectorToolAnnotations(tool),
				Access:        workspaceIntegrationConnectorToolAccess(tool),
				Source:        workspaceIntegrationConnectorToolSource(*integration, tool),
				Provenance:    provenance,
				ToolsSyncedAt: now,
				StaleAfter:    &staleAfter,
			})
			policies = append(policies, WorkspaceIntegrationToolPolicy{
				WorkspaceID:  integration.WorkspaceID,
				ConnectionID: connection.ID,
				ToolName:     tool.Name,
				Policy:       workspaceIntegrationConnectorToolPolicy(*integration, tool.Name),
				Source:       "v1_projection",
			})
		}
		if err := s.ReplaceWorkspaceIntegrationToolSnapshots(ctx, connection.ID, snapshots); err != nil {
			return fmt.Errorf("replace workspace integration tool snapshot projection: %w", err)
		}
		if err := s.ReplaceWorkspaceIntegrationToolPolicies(ctx, connection.ID, policies); err != nil {
			return fmt.Errorf("replace workspace integration tool policy projection: %w", err)
		}
	}
	return s.deleteStaleWorkspaceIntegrationConnectorProjectionConnections(ctx, integration.ID, keptConnectionIDs)
}

func (s *PostgresStore) deleteStaleWorkspaceIntegrationConnectorProjectionConnections(ctx context.Context, legacyIntegrationID string, keep map[string]struct{}) error {
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace_integration_connections WHERE legacy_integration_id = $1`, legacyIntegrationID)
	if err != nil {
		return fmt.Errorf("list stale workspace integration projection connections: %w", err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan stale workspace integration projection connection: %w", err)
		}
		if _, ok := keep[id]; !ok {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate stale workspace integration projection connections: %w", err)
	}
	rows.Close()
	for _, id := range stale {
		if _, err := s.pool.Exec(ctx, `DELETE FROM workspace_integration_connections WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete stale workspace integration projection connection: %w", err)
		}
	}
	return nil
}

type workspaceIntegrationConnectorProjectionGroup struct {
	Source WorkspaceIntegrationSource
	Tools  []workspaceIntegrationV1ManifestTool
}

func workspaceIntegrationConnectorProjectionGroups(integration WorkspaceIntegration, tools []workspaceIntegrationV1ManifestTool) []workspaceIntegrationConnectorProjectionGroup {
	if len(tools) == 0 {
		sourceSlug := workspaceIntegrationConnectorSourceSlug(integration)
		return []workspaceIntegrationConnectorProjectionGroup{{Source: workspaceIntegrationConnectorProjectionSource(integration, sourceSlug)}}
	}
	ordered := make([]workspaceIntegrationConnectorProjectionGroup, 0, 4)
	indexBySlug := map[string]int{}
	for _, tool := range tools {
		tool.Name = strings.TrimSpace(tool.Name)
		if tool.Name == "" {
			continue
		}
		sourceSlug := workspaceIntegrationConnectorToolSourceSlug(integration, tool)
		index, ok := indexBySlug[sourceSlug]
		if !ok {
			index = len(ordered)
			indexBySlug[sourceSlug] = index
			ordered = append(ordered, workspaceIntegrationConnectorProjectionGroup{
				Source: workspaceIntegrationConnectorProjectionSource(integration, sourceSlug),
			})
		}
		ordered[index].Tools = append(ordered[index].Tools, tool)
	}
	if len(ordered) == 0 {
		sourceSlug := workspaceIntegrationConnectorSourceSlug(integration)
		return []workspaceIntegrationConnectorProjectionGroup{{Source: workspaceIntegrationConnectorProjectionSource(integration, sourceSlug)}}
	}
	return ordered
}

func workspaceIntegrationConnectorProjectionSource(integration WorkspaceIntegration, sourceSlug string) WorkspaceIntegrationSource {
	return WorkspaceIntegrationSource{
		WorkspaceID: integration.WorkspaceID,
		Slug:        sourceSlug,
		DisplayName: workspaceIntegrationConnectorSourceDisplayNameForSlug(integration, sourceSlug),
		Kind:        integration.Kind,
		Importer:    workspaceIntegrationConnectorImporter(integration),
		Config:      json.RawMessage("{}"),
	}
}

func workspaceIntegrationConnectorConnectionConfigPayload(integration WorkspaceIntegration, sourceSlug string) map[string]interface{} {
	payload := map[string]interface{}{
		"transport":        integration.Transport,
		"endpoint_present": integration.Endpoint != nil && strings.TrimSpace(*integration.Endpoint) != "",
		"projection":       "v1_workspace_integrations",
		"source_slug":      sourceSlug,
	}
	if integration.Endpoint != nil && strings.TrimSpace(*integration.Endpoint) != "" {
		payload["endpoint"] = strings.TrimSpace(*integration.Endpoint)
	}
	return payload
}

func workspaceIntegrationConnectorToolProvenancePayload(integration WorkspaceIntegration, sourceSlug, legacyToolID string, tool workspaceIntegrationV1ManifestTool) map[string]interface{} {
	payload := map[string]interface{}{
		"projection":            "v1_workspace_integrations",
		"legacy_integration_id": integration.ID,
		"legacy_tool_id":        legacyToolID,
		"transport":             integration.Transport,
		"endpoint_present":      integration.Endpoint != nil && strings.TrimSpace(*integration.Endpoint) != "",
		"source_slug":           sourceSlug,
	}
	if len(tool.Request) > 0 && string(tool.Request) != "null" {
		payload["request"] = tool.Request
	}
	return payload
}

func workspaceIntegrationConnectorSourceSlug(integration WorkspaceIntegration) string {
	if kind := workspaceIntegrationConnectorAddressSegment(strings.ReplaceAll(integration.Kind, "_", "-"), ""); kind != "" {
		return kind
	}
	return workspaceIntegrationConnectorAddressSegment(integration.Slug, "integration")
}

func workspaceIntegrationConnectorSourceDisplayNameForSlug(integration WorkspaceIntegration, sourceSlug string) string {
	if workspaceIntegrationConnectorIsGoogleWorkspace(integration) {
		switch sourceSlug {
		case "gmail":
			return "Gmail"
		case "google-drive":
			return "Google Drive"
		case "google-calendar":
			return "Google Calendar"
		case "google-docs":
			return "Google Docs"
		}
	}
	switch strings.TrimSpace(integration.Kind) {
	case "":
		return integration.DisplayName
	default:
		return workspaceIntegrationConnectorTitle(strings.ReplaceAll(integration.Kind, "_", " "))
	}
}

func workspaceIntegrationConnectorTitle(value string) string {
	parts := strings.Fields(value)
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}

func workspaceIntegrationConnectorConnectionDisplayName(integration WorkspaceIntegration, sourceSlug string) string {
	if !workspaceIntegrationConnectorIsGoogleWorkspace(integration) {
		return integration.DisplayName
	}
	email := workspaceIntegrationConnectorGoogleEmail(integration)
	if email == "" {
		return integration.DisplayName
	}
	return workspaceIntegrationConnectorSourceDisplayNameForSlug(integration, sourceSlug) + " - " + email
}

func workspaceIntegrationConnectorGoogleEmail(integration WorkspaceIntegration) string {
	var cfg struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(integration.Config, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Email)
}

func workspaceIntegrationConnectorToolSourceSlug(integration WorkspaceIntegration, tool workspaceIntegrationV1ManifestTool) string {
	if source, ok := workspaceIntegrationConnectorGoogleServiceToolSourceSlug(integration, tool.Name); ok {
		return source
	}
	if source := strings.ToLower(strings.TrimSpace(tool.Source)); source != "" && workspaceIntegrationConnectorIsGoogleWorkspace(integration) {
		return workspaceIntegrationConnectorAddressSegment(source, workspaceIntegrationConnectorSourceSlug(integration))
	}
	return workspaceIntegrationConnectorSourceSlug(integration)
}

func workspaceIntegrationConnectorGoogleServiceToolSourceSlug(integration WorkspaceIntegration, toolName string) (string, bool) {
	if !workspaceIntegrationConnectorIsGoogleWorkspace(integration) {
		return "", false
	}
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case strings.HasPrefix(name, "gmail_"):
		return "gmail", true
	case strings.HasPrefix(name, "drive_"):
		return "google-drive", true
	case strings.HasPrefix(name, "calendar_"):
		return "google-calendar", true
	case strings.HasPrefix(name, "docs_"):
		return "google-docs", true
	default:
		return "", false
	}
}

func workspaceIntegrationConnectorIsGoogleWorkspace(integration WorkspaceIntegration) bool {
	return strings.EqualFold(strings.TrimSpace(integration.Kind), "google_workspace") ||
		strings.EqualFold(strings.TrimSpace(integration.Slug), "google-workspace")
}

func workspaceIntegrationConnectorImporter(integration WorkspaceIntegration) string {
	switch strings.ToLower(strings.TrimSpace(integration.Transport)) {
	case "mcp-remote":
		return "mcp"
	case "http", "rest":
		switch strings.ToLower(strings.TrimSpace(integration.Kind)) {
		case "openapi", "graphql":
			return strings.ToLower(strings.TrimSpace(integration.Kind))
		default:
			return "http"
		}
	case "":
		return "http"
	default:
		return strings.ToLower(strings.TrimSpace(integration.Transport))
	}
}

func workspaceIntegrationConnectorCredentialState(integration WorkspaceIntegration) string {
	if !integration.Enabled {
		return "disconnected"
	}
	return "connected"
}

func workspaceIntegrationConnectorToolAddress(workspaceID, sourceSlug, connectionSlug, toolName string) string {
	return "wi." +
		workspaceIntegrationConnectorAddressSegment(workspaceID, "workspace") + "." +
		workspaceIntegrationConnectorAddressSegment(sourceSlug, "source") + "." +
		workspaceIntegrationConnectorAddressSegment(connectionSlug, "connection") + "." +
		workspaceIntegrationConnectorAddressSegment(toolName, "tool")
}

func workspaceIntegrationConnectorAddressSegment(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(fallback))
	}
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			out.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			out.WriteRune(r)
			lastDash = false
		case r == '_':
			out.WriteRune(r)
			lastDash = false
		case r == '-':
			if !lastDash {
				out.WriteRune('-')
				lastDash = true
			}
		default:
			if !lastDash {
				out.WriteRune('-')
				lastDash = true
			}
		}
	}
	segment := strings.Trim(out.String(), "-")
	if segment == "" {
		return "item"
	}
	return segment
}

func workspaceIntegrationConnectorToolInputSchema(tool workspaceIntegrationV1ManifestTool) json.RawMessage {
	switch {
	case len(tool.Parameters) > 0:
		return tool.Parameters
	case len(tool.InputSchema) > 0:
		return tool.InputSchema
	case len(tool.Schema) > 0:
		return tool.Schema
	default:
		return json.RawMessage(`{"type":"object","additionalProperties":true}`)
	}
}

func workspaceIntegrationConnectorToolAnnotations(tool workspaceIntegrationV1ManifestTool) json.RawMessage {
	if len(tool.Annotations) > 0 {
		return tool.Annotations
	}
	return json.RawMessage("{}")
}

func workspaceIntegrationConnectorToolAccess(tool workspaceIntegrationV1ManifestTool) string {
	switch strings.ToLower(strings.TrimSpace(tool.Access)) {
	case "read", "write":
		return strings.ToLower(strings.TrimSpace(tool.Access))
	}
	name := strings.ToLower(tool.Name)
	for _, marker := range []string{"create", "update", "delete", "remove", "send", "write", "patch", "post"} {
		if strings.Contains(name, marker) {
			return "write"
		}
	}
	return "read"
}

func workspaceIntegrationConnectorToolSource(integration WorkspaceIntegration, tool workspaceIntegrationV1ManifestTool) string {
	if source := strings.ToLower(strings.TrimSpace(tool.Source)); source != "" {
		return source
	}
	if source, ok := workspaceIntegrationConnectorGoogleServiceToolSourceSlug(integration, tool.Name); ok {
		return source
	}
	return workspaceIntegrationConnectorImporter(integration)
}

func workspaceIntegrationConnectorToolPolicy(integration WorkspaceIntegration, toolName string) string {
	fullName := integration.Slug + "." + toolName
	for _, denied := range integration.DeniedTools {
		if denied == toolName || denied == fullName {
			return "block"
		}
	}
	if policy, ok := workspaceIntegrationConnectorConfiguredToolPolicy(integration, toolName); ok {
		return policy
	}
	if policy, ok := workspaceIntegrationConnectorConfiguredToolPolicy(integration, fullName); ok {
		return policy
	}
	if len(integration.AllowedTools) == 0 {
		return "allow"
	}
	for _, allowed := range integration.AllowedTools {
		if allowed == toolName || allowed == fullName {
			return "allow"
		}
	}
	return "block"
}

func workspaceIntegrationConnectorConfiguredToolPolicy(integration WorkspaceIntegration, toolName string) (string, bool) {
	if len(integration.Config) == 0 {
		return "", false
	}
	var raw struct {
		ToolPolicy   map[string]string `json:"tool_policy"`
		ToolPolicies map[string]string `json:"tool_policies"`
	}
	if err := json.Unmarshal(integration.Config, &raw); err != nil {
		return "", false
	}
	if policy, ok := raw.ToolPolicy[toolName]; ok {
		return workspaceIntegrationConnectorNormalizePolicy(policy)
	}
	if policy, ok := raw.ToolPolicies[toolName]; ok {
		return workspaceIntegrationConnectorNormalizePolicy(policy)
	}
	return "", false
}

func workspaceIntegrationConnectorNormalizePolicy(policy string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "allow", "allowed":
		return "allow", true
	case "require_approval", "approval_required", "require-approval", "approval-required":
		return "require_approval", true
	case "block", "blocked", "deny", "denied":
		return "block", true
	default:
		return "", false
	}
}
