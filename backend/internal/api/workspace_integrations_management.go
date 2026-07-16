package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

const workspaceIntegrationPluginID = "ocm-integrations"
const workspaceIntegrationMaxDiscoveredTools = 100
const workspaceIntegrationMaxEnabledTools = 25

var errWorkspaceIntegrationConnectionSlugConflict = errors.New("workspace integration connection slug already exists")

type workspaceIntegrationDeleter interface {
	DeleteWorkspaceIntegration(ctx context.Context, workspaceID, slug string) (*store.WorkspaceIntegration, error)
}

type workspaceIntegrationConnectionDeleter interface {
	DeleteWorkspaceIntegrationConnection(ctx context.Context, workspaceID, connectionSlug string) (*store.WorkspaceIntegrationConnectorProjection, error)
}

type workspaceIntegrationTokenRevoker interface {
	RevokeWorkspaceIntegrationTokensForWorkspace(ctx context.Context, workspaceID string) error
}

type workspaceIntegrationLegacyConnectionLister interface {
	ListWorkspaceIntegrationConnectionsByLegacyIntegration(ctx context.Context, integrationID string) ([]store.WorkspaceIntegrationConnection, error)
}

type workspaceIntegrationToolPolicyReplacer interface {
	ReplaceWorkspaceIntegrationToolPolicies(ctx context.Context, connectionID string, policies []store.WorkspaceIntegrationToolPolicy) error
}

type workspaceIntegrationManagementResponse struct {
	Workspace    store.Workspace                       `json:"workspace"`
	Integrations []workspaceIntegrationManagementItem  `json:"integrations"`
	Machines     []workspaceIntegrationMachineConsumer `json:"machines"`
}

type workspaceIntegrationHealthResponse struct {
	Workspace store.Workspace                        `json:"workspace"`
	Since     time.Time                              `json:"since"`
	Tools     []store.WorkspaceIntegrationToolHealth `json:"tools"`
}

type workspaceIntegrationGuidanceResponse struct {
	Workspace store.Workspace                             `json:"workspace"`
	Overlays  []store.WorkspaceIntegrationGuidanceOverlay `json:"overlays"`
}

type workspaceIntegrationGuidanceCreateRequest struct {
	ToolID             string          `json:"tool_id"`
	ToolAddress        *string         `json:"tool_address"`
	IntegrationSlug    string          `json:"integration_slug"`
	ToolName           string          `json:"tool_name"`
	Guidance           string          `json:"guidance"`
	SourceFailureClass *string         `json:"source_failure_class"`
	SanitizedPattern   json.RawMessage `json:"sanitized_pattern"`
}

type workspaceIntegrationManagementItem struct {
	ID               string                                  `json:"id"`
	WorkspaceID      string                                  `json:"workspace_id"`
	Slug             string                                  `json:"slug"`
	DisplayName      string                                  `json:"display_name"`
	Kind             string                                  `json:"kind"`
	Transport        string                                  `json:"transport"`
	Target           string                                  `json:"target,omitempty"`
	Enabled          bool                                    `json:"enabled"`
	ToolCount        int                                     `json:"tool_count"`
	Tools            []workspaceIntegrationCatalogTool       `json:"tools,omitempty"`
	Approved         bool                                    `json:"approved"`
	Scopes           []string                                `json:"scopes,omitempty"`
	PermissionLevels map[string]string                       `json:"permission_levels,omitempty"`
	ServiceStatus    map[string]googleWorkspaceServiceStatus `json:"service_status,omitempty"`
	Snapshot         *workspaceIntegrationSnapshot           `json:"snapshot,omitempty"`
	AllowedTools     []string                                `json:"allowed_tools,omitempty"`
	DeniedTools      []string                                `json:"denied_tools,omitempty"`
	ApprovedBy       *int                                    `json:"approved_by_user_id,omitempty"`
	ApprovedAt       *time.Time                              `json:"approved_at,omitempty"`
	ConnectedBy      *int                                    `json:"connected_by_user_id,omitempty"`
	ConnectedAt      *time.Time                              `json:"connected_at,omitempty"`
	CreatedAt        time.Time                               `json:"created_at"`
	UpdatedAt        time.Time                               `json:"updated_at"`
}

type workspaceIntegrationSnapshot struct {
	ServerName      string     `json:"server_name,omitempty"`
	ServerVersion   string     `json:"server_version,omitempty"`
	ProtocolVersion string     `json:"protocol_version,omitempty"`
	ProbedAt        *time.Time `json:"probed_at,omitempty"`
}

type workspaceIntegrationMachineConsumer struct {
	MachineID        string  `json:"machine_id"`
	Name             string  `json:"name"`
	Status           string  `json:"status"`
	PluginEnabled    bool    `json:"plugin_enabled"`
	InstallStatus    *string `json:"install_status,omitempty"`
	InstalledVersion *string `json:"installed_version,omitempty"`
}

type genericWorkspaceIntegrationCreateRequest struct {
	DisplayName  string          `json:"display_name"`
	Kind         string          `json:"kind"`
	Transport    string          `json:"transport"`
	Endpoint     string          `json:"endpoint"`
	ToolManifest json.RawMessage `json:"tool_manifest"`
	Config       json.RawMessage `json:"config"`
	Token        *string         `json:"token,omitempty"`
	TokenType    string          `json:"token_type,omitempty"`
}

func (s *Server) handleListWorkspaceIntegrations(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}

	integrations, err := s.store.ListWorkspaceIntegrations(r.Context(), workspace.ID)
	if err != nil {
		slog.Error("workspace_integrations.list_failed", "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list workspace integrations")
		return
	}

	machines, err := s.store.ListMachinesByAccount(r.Context(), accountID)
	if err != nil {
		slog.Error("workspace_integrations.machines.list_failed", "account_id", accountID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list machines")
		return
	}

	workspaceMachines := make([]store.Machine, 0, len(machines))
	for _, machine := range machines {
		if machine.WorkspaceID == nil {
			continue
		}
		if *machine.WorkspaceID != workspace.ID {
			continue
		}
		workspaceMachines = append(workspaceMachines, machine)
	}

	resp := workspaceIntegrationManagementResponse{
		Workspace:    *workspace,
		Integrations: make([]workspaceIntegrationManagementItem, 0, len(integrations)),
		Machines:     make([]workspaceIntegrationMachineConsumer, 0, len(workspaceMachines)),
	}
	for _, integration := range integrations {
		resp.Integrations = append(resp.Integrations, workspaceIntegrationManagementItemFromStore(integration))
	}
	resp.Integrations = append(resp.Integrations, s.workspaceIntegrationNormalizedManagementItems(r.Context(), workspace.ID, integrations)...)
	for _, machine := range workspaceMachines {
		consumer := workspaceIntegrationMachineConsumer{
			MachineID: machine.ID,
			Name:      machine.Name,
			Status:    machine.Status,
		}
		plugins, err := s.store.ListMachinePlugins(r.Context(), machine.ID)
		if err != nil {
			slog.Warn("workspace_integrations.machine_plugins.list_failed", "machine_id", machine.ID, "error", err)
			resp.Machines = append(resp.Machines, consumer)
			continue
		}
		for _, plugin := range plugins {
			if plugin.PluginID != workspaceIntegrationPluginID {
				continue
			}
			installStatus := plugin.InstallStatus
			consumer.PluginEnabled = plugin.Enabled
			consumer.InstallStatus = &installStatus
			consumer.InstalledVersion = plugin.InstalledVersion
			break
		}
		resp.Machines = append(resp.Machines, consumer)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWorkspaceIntegrationHealth(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceFromRequest(w, r, accountID)
	if !ok {
		return
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC3339 timestamp")
			return
		}
		since = parsed.UTC()
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	tools, err := s.store.ListWorkspaceIntegrationToolHealth(r.Context(), accountID, workspace.ID, store.WorkspaceIntegrationHealthQuery{
		Since: since,
		Limit: limit,
	})
	if err != nil {
		slog.Error("workspace_integrations.health_failed", "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load workspace integration health")
		return
	}
	writeJSON(w, http.StatusOK, workspaceIntegrationHealthResponse{
		Workspace: *workspace,
		Since:     since,
		Tools:     tools,
	})
}

func (s *Server) handleListWorkspaceIntegrationGuidance(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceFromRequest(w, r, accountID)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	overlays, err := s.store.ListWorkspaceIntegrationGuidanceOverlays(r.Context(), accountID, workspace.ID, status)
	if err != nil {
		slog.Error("workspace_integrations.guidance_list_failed", "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list workspace integration guidance")
		return
	}
	writeJSON(w, http.StatusOK, workspaceIntegrationGuidanceResponse{
		Workspace: *workspace,
		Overlays:  overlays,
	})
}

func (s *Server) handleCreateWorkspaceIntegrationGuidance(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceFromRequest(w, r, accountID)
	if !ok {
		return
	}
	var req workspaceIntegrationGuidanceCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid guidance request body")
		return
	}
	req.ToolID = strings.TrimSpace(req.ToolID)
	req.IntegrationSlug = strings.TrimSpace(req.IntegrationSlug)
	req.ToolName = strings.TrimSpace(req.ToolName)
	req.Guidance = strings.TrimSpace(req.Guidance)
	if req.ToolID == "" {
		writeError(w, http.StatusBadRequest, "tool_id is required")
		return
	}
	if req.Guidance == "" {
		writeError(w, http.StatusBadRequest, "guidance is required")
		return
	}
	var createdBy *int
	if claims := auth.UserFromContext(r.Context()); claims != nil && claims.UserID > 0 {
		createdBy = &claims.UserID
	}
	if len(req.SanitizedPattern) == 0 {
		req.SanitizedPattern = json.RawMessage("{}")
	}
	overlay := &store.WorkspaceIntegrationGuidanceOverlay{
		AccountID:          accountID,
		WorkspaceID:        workspace.ID,
		ToolID:             req.ToolID,
		ToolAddress:        req.ToolAddress,
		IntegrationSlug:    req.IntegrationSlug,
		ToolName:           req.ToolName,
		Status:             "draft",
		Guidance:           req.Guidance,
		SourceFailureClass: req.SourceFailureClass,
		SanitizedPattern:   req.SanitizedPattern,
		CreatedBy:          createdBy,
	}
	if err := s.store.CreateWorkspaceIntegrationGuidanceOverlay(r.Context(), overlay); err != nil {
		slog.Error("workspace_integrations.guidance_create_failed", "workspace_id", workspace.ID, "tool_id", req.ToolID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create workspace integration guidance")
		return
	}
	writeJSON(w, http.StatusCreated, overlay)
}

func (s *Server) handleDraftWorkspaceIntegrationGuidance(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceFromRequest(w, r, accountID)
	if !ok {
		return
	}
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC3339 timestamp")
			return
		}
		since = parsed.UTC()
	}
	limit := 10
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 25 {
			writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 25")
			return
		}
		limit = parsed
	}
	var createdBy *int
	if claims := auth.UserFromContext(r.Context()); claims != nil && claims.UserID > 0 {
		createdBy = &claims.UserID
	}
	overlays, err := s.store.CreateWorkspaceIntegrationGuidanceDraftsFromTelemetry(r.Context(), accountID, workspace.ID, since, limit, createdBy)
	if err != nil {
		slog.Error("workspace_integrations.guidance_draft_failed", "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to draft workspace integration guidance")
		return
	}
	writeJSON(w, http.StatusCreated, workspaceIntegrationGuidanceResponse{
		Workspace: *workspace,
		Overlays:  overlays,
	})
}

func (s *Server) handleApproveWorkspaceIntegrationGuidance(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceFromRequest(w, r, accountID)
	if !ok {
		return
	}
	overlayID := strings.TrimSpace(chi.URLParam(r, "overlayID"))
	if overlayID == "" {
		writeError(w, http.StatusBadRequest, "overlay id is required")
		return
	}
	approvedBy := 0
	if claims := auth.UserFromContext(r.Context()); claims != nil {
		approvedBy = claims.UserID
	}
	overlay, err := s.store.ApproveWorkspaceIntegrationGuidanceOverlay(r.Context(), accountID, workspace.ID, overlayID, approvedBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "guidance overlay not found")
			return
		}
		slog.Error("workspace_integrations.guidance_approve_failed", "workspace_id", workspace.ID, "overlay_id", overlayID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to approve workspace integration guidance")
		return
	}
	writeJSON(w, http.StatusOK, overlay)
}

func (s *Server) handleCreateWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	slug := strings.TrimSpace(chi.URLParam(r, "integrationSlug"))
	if slug == "" {
		writeError(w, http.StatusBadRequest, "integration slug is required")
		return
	}
	if !isValidSlug(slug) {
		writeError(w, http.StatusBadRequest, "invalid integration slug")
		return
	}

	var req genericWorkspaceIntegrationCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	s.handleCreateWorkspaceIntegrationWithRequest(w, r, accountID, slug, req)
}

func (s *Server) handleCreateWorkspaceIntegrationWithRequest(w http.ResponseWriter, r *http.Request, accountID int, slug string, req genericWorkspaceIntegrationCreateRequest) {
	integration, err := s.workspaceIntegrationFromGenericCreateRequest(slug, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Token != nil && strings.TrimSpace(*req.Token) != "" && s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}

	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}

	now := time.Now().UTC()
	actorID := userIDFromContext(r.Context())
	hasToken := req.Token != nil && strings.TrimSpace(*req.Token) != ""

	existing, lookupErr := s.findWorkspaceIntegrationBySlug(r.Context(), workspace.ID, slug)
	if lookupErr != nil && !errors.Is(lookupErr, store.ErrPluginNotFound) {
		slog.Error("workspace_integrations.generic.lookup_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", lookupErr)
		writeError(w, http.StatusInternalServerError, "failed to load workspace integration")
		return
	}
	if err := s.ensureWorkspaceIntegrationConnectionSlugAvailable(r.Context(), workspace.ID, slug, existing); err != nil {
		if errors.Is(err, errWorkspaceIntegrationConnectionSlugConflict) {
			writeError(w, http.StatusConflict, errWorkspaceIntegrationConnectionSlugConflict.Error())
			return
		}
		slog.Error("workspace_integrations.generic.connection_slug_check_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to inspect workspace integration connections")
		return
	}

	integration.WorkspaceID = workspace.ID
	if existing == nil {
		// First connect: stamp approval + connection provenance and enable.
		integration.Enabled = true
		integration.ApprovedBy = actorID
		integration.ApprovedAt = &now
		integration.ConnectedBy = actorID
		integration.ConnectedAt = &now
	} else {
		// Update (reconnect / edit): preserve the original approver and the
		// enabled state — nil provenance values are kept by the upsert's
		// COALESCE, and enabled is set explicitly from the existing row so a
		// reconnect never silently re-enables a disabled integration. Only
		// refresh the connection provenance when a new credential is supplied.
		integration.Enabled = existing.Enabled
		integration.ApprovedBy = nil
		integration.ApprovedAt = nil
		if hasToken {
			integration.ConnectedBy = actorID
			integration.ConnectedAt = &now
		} else {
			integration.ConnectedBy = nil
			integration.ConnectedAt = nil
		}
	}
	if err := s.store.UpsertWorkspaceIntegration(r.Context(), integration); err != nil {
		slog.Error("workspace_integrations.generic.upsert_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save workspace integration")
		return
	}

	// The remaining steps (credential, token revocation, runtime assignment) are
	// not in a shared DB transaction. To avoid leaving a freshly-created,
	// enabled integration without its credential/assignment, roll back the
	// insert (only on a new insert — never delete an existing integration) if a
	// subsequent step fails.
	failCreate := func(status int, clientMsg, logKey string, cause error) {
		slog.Error(logKey, "workspace_id", workspace.ID, "integration_slug", slug, "error", cause)
		if existing == nil {
			if deleter, ok := s.store.(workspaceIntegrationDeleter); ok {
				if _, derr := deleter.DeleteWorkspaceIntegration(r.Context(), workspace.ID, slug); derr != nil {
					slog.Error("workspace_integrations.generic.rollback_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", derr)
				}
			}
		}
		writeError(w, status, clientMsg)
	}

	// Persist the credential only when a non-empty token was supplied. An empty
	// token means "leave the existing credential unchanged" — matching the UI's
	// "leave blank to keep existing token" — so we must not overwrite it.
	if hasToken {
		if err := s.saveGenericWorkspaceIntegrationToken(r.Context(), integration.ID, req); err != nil {
			failCreate(http.StatusInternalServerError, "failed to save workspace integration credential", "workspace_integrations.generic.credential_set_failed", err)
			return
		}
	}
	// Adding an integration is additive: the per-machine token is workspace-scoped
	// auth only (it pins no tool set), and the gateway derives enabled integrations
	// + policy live on every call. Revoking here would invalidate every running
	// machine's token for no security benefit, dropping all their tools until they
	// restart. See the token-revocation regression in the remote-MCP review.
	if err := s.ensureWorkspaceIntegrationRuntimeForWorkspace(r.Context(), accountID, workspace.ID); err != nil {
		failCreate(http.StatusInternalServerError, "failed to assign workspace integration to machines", "workspace_integrations.runtime.ensure_failed", err)
		return
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.workspace_integration_created",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Configured workspace integration '%s'", integration.DisplayName),
		Detail:    map[string]any{"workspace_id": workspace.ID, "integration_slug": integration.Slug, "transport": integration.Transport},
	})

	writeJSON(w, http.StatusOK, workspaceIntegrationManagementItemFromStore(*integration))
}

func (s *Server) handleCreateMockWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}

	now := time.Now().UTC()
	actorID := userIDFromContext(r.Context())
	integration := store.WorkspaceIntegration{
		WorkspaceID:  workspace.ID,
		Slug:         "mock-echo",
		DisplayName:  "Mock Echo",
		Kind:         "mock",
		Transport:    "mock",
		Enabled:      true,
		ToolManifest: mockEchoToolManifest(),
		Config:       json.RawMessage(`{"mode":"echo"}`),
		ApprovedBy:   actorID,
		ApprovedAt:   &now,
		ConnectedBy:  actorID,
		ConnectedAt:  &now,
	}
	if err := s.store.UpsertWorkspaceIntegration(r.Context(), &integration); err != nil {
		slog.Error("workspace_integrations.mock.upsert_failed", "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create mock integration")
		return
	}
	// Additive create: no token revocation (see generic create handler).
	if err := s.ensureWorkspaceIntegrationRuntimeForWorkspace(r.Context(), accountID, workspace.ID); err != nil {
		slog.Error("workspace_integrations.runtime.ensure_failed", "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to assign workspace integration to machines")
		return
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.workspace_integration_created",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		Summary:   "Created workspace integration 'Mock Echo'",
		Detail:    map[string]any{"workspace_id": workspace.ID, "integration_slug": integration.Slug},
	})

	writeJSON(w, http.StatusOK, workspaceIntegrationManagementItemFromStore(integration))
}

func (s *Server) handleCreateGitHubWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	var req struct {
		DisplayName    string `json:"display_name"`
		Owner          string `json:"owner"`
		Repo           string `json:"repo"`
		ConnectionSlug string `json:"connection_slug,omitempty"`
		Slug           string `json:"slug,omitempty"`
		Token          string `json:"token,omitempty"`
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var genericReq genericWorkspaceIntegrationCreateRequest
	if err := json.Unmarshal(raw, &genericReq); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if genericWorkspaceIntegrationCreateRequestPresent(genericReq) && strings.TrimSpace(req.Owner) == "" && strings.TrimSpace(req.Repo) == "" {
		s.handleCreateWorkspaceIntegrationWithRequest(w, r, accountID, "github", genericReq)
		return
	}
	owner, err := normalizeGitHubIdentifier(req.Owner, "owner")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := normalizeGitHubIdentifier(req.Repo, "repo")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token != "" && s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}

	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}

	target := owner + "/" + repo
	connectionSlug, err := githubWorkspaceIntegrationConnectionSlug(owner, repo, firstNonEmpty(req.ConnectionSlug, req.Slug))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	targetChanged := false
	existing, err := s.findWorkspaceIntegrationBySlug(r.Context(), workspace.ID, connectionSlug)
	if err == nil {
		previousTarget := githubWorkspaceIntegrationTarget(*existing)
		targetChanged = previousTarget != "" && previousTarget != target
	} else if !errors.Is(err, store.ErrPluginNotFound) {
		slog.Error("workspace_integrations.github.lookup_failed", "workspace_id", workspace.ID, "integration_slug", connectionSlug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to inspect existing github integration")
		return
	}
	if err := s.ensureWorkspaceIntegrationConnectionSlugAvailable(r.Context(), workspace.ID, connectionSlug, existing); err != nil {
		if errors.Is(err, errWorkspaceIntegrationConnectionSlugConflict) {
			writeError(w, http.StatusConflict, errWorkspaceIntegrationConnectionSlugConflict.Error())
			return
		}
		slog.Error("workspace_integrations.github.connection_slug_check_failed", "workspace_id", workspace.ID, "integration_slug", connectionSlug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to inspect workspace integration connections")
		return
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = "GitHub " + target
	}
	now := time.Now().UTC()
	actorID := userIDFromContext(r.Context())
	config, err := json.Marshal(githubWorkspaceIntegrationConfig{Owner: owner, Repo: repo})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build github config")
		return
	}
	integration := store.WorkspaceIntegration{
		WorkspaceID:  workspace.ID,
		Slug:         connectionSlug,
		DisplayName:  displayName,
		Kind:         "github",
		Transport:    "http",
		Enabled:      true,
		ToolManifest: githubToolManifest(),
		Config:       config,
		ApprovedBy:   actorID,
		ApprovedAt:   &now,
		ConnectedBy:  actorID,
		ConnectedAt:  &now,
	}
	if err := s.store.UpsertWorkspaceIntegration(r.Context(), &integration); err != nil {
		slog.Error("workspace_integrations.github.upsert_failed", "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save github integration")
		return
	}

	if req.Token != "" {
		encrypted, err := crypto.Encrypt(req.Token, s.secretKey)
		if err != nil {
			slog.Error("workspace_integrations.github.encrypt_failed", "workspace_id", workspace.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to encrypt github token")
			return
		}
		tokenType := "token"
		if err := s.saveWorkspaceIntegrationCredentialAndConnectionCredentials(r.Context(), integration.ID, encrypted, nil, &tokenType, nil); err != nil {
			slog.Error("workspace_integrations.github.credential_set_failed", "workspace_id", workspace.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to save github credential")
			return
		}
	} else if targetChanged {
		tokenType := "token"
		if err := s.saveWorkspaceIntegrationCredentialAndConnectionCredentials(r.Context(), integration.ID, "", nil, &tokenType, nil); err != nil {
			slog.Error("workspace_integrations.github.credential_clear_failed", "workspace_id", workspace.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to clear stale github credential")
			return
		}
	}
	// Additive create: no token revocation (see generic create handler).
	if err := s.ensureWorkspaceIntegrationRuntimeForWorkspace(r.Context(), accountID, workspace.ID); err != nil {
		slog.Error("workspace_integrations.runtime.ensure_failed", "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to assign workspace integration to machines")
		return
	}

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.workspace_integration_created",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Configured workspace integration '%s'", integration.DisplayName),
		Detail:    map[string]any{"workspace_id": workspace.ID, "integration_slug": integration.Slug, "target": target},
	})

	writeJSON(w, http.StatusOK, workspaceIntegrationManagementItemFromStore(integration))
}

func genericWorkspaceIntegrationCreateRequestPresent(req genericWorkspaceIntegrationCreateRequest) bool {
	return strings.TrimSpace(req.Transport) != "" || strings.TrimSpace(req.Endpoint) != "" || len(req.ToolManifest) > 0
}

func (s *Server) handleTestWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "integrationSlug"))
	if slug == "" {
		writeError(w, http.StatusBadRequest, "integration slug is required")
		return
	}

	integration, err := s.findWorkspaceIntegrationBySlug(r.Context(), workspace.ID, slug)
	if err != nil {
		if errors.Is(err, store.ErrPluginNotFound) {
			if s.handleTestNormalizedWorkspaceIntegrationConnection(w, r, *workspace, slug) {
				return
			}
		}
		writeError(w, http.StatusNotFound, "workspace integration not configured")
		return
	}
	tool, found := firstWorkspaceIntegrationTestTool(*integration)
	if !found {
		writeError(w, http.StatusNotFound, "workspace integration test tool not configured")
		return
	}
	result, err := s.callWorkspaceIntegrationTool(r.Context(), *integration, *tool, map[string]interface{}{})
	if err != nil {
		slog.Warn("workspace_integrations.test_failed", "workspace_id", workspace.ID, "integration", integration.Slug, "error", err)
		writeError(w, http.StatusBadGateway, "workspace integration test failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"result": result,
	})
}

func (s *Server) handleTestNormalizedWorkspaceIntegrationConnection(w http.ResponseWriter, r *http.Request, workspace store.Workspace, connectionSlug string) bool {
	reader, ok := s.store.(workspaceIntegrationConnectorProjectionReader)
	if !ok {
		return false
	}
	projections, err := reader.ListWorkspaceIntegrationConnectorProjections(r.Context(), workspace.ID)
	if err != nil {
		slog.Error("workspace_integrations.normalized_test.projections_failed", "workspace_id", workspace.ID, "connection_slug", connectionSlug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load workspace integration connection")
		return true
	}
	projection, ok := findWorkspaceIntegrationProjectionByConnectionSlug(projections, connectionSlug)
	if !ok {
		return false
	}
	integration, tool, ok := firstWorkspaceIntegrationProjectionTestTool(projection)
	if !ok {
		writeError(w, http.StatusNotFound, "workspace integration test tool not configured")
		return true
	}
	result, err := s.callWorkspaceIntegrationTool(r.Context(), integration, tool, map[string]interface{}{})
	if err != nil {
		slog.Warn("workspace_integrations.normalized_test_failed", "workspace_id", workspace.ID, "connection_slug", projection.Connection.Slug, "error", err)
		writeError(w, http.StatusBadGateway, "workspace integration test failed")
		return true
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"result": result,
	})
	return true
}

func firstWorkspaceIntegrationTestTool(integration store.WorkspaceIntegration) (*workspaceIntegrationManifestTool, bool) {
	manifestTools, err := parseWorkspaceIntegrationManifest(integration.ToolManifest)
	if err != nil {
		return nil, false
	}
	for i := range manifestTools {
		tool := &manifestTools[i]
		if strings.TrimSpace(tool.Name) == "" || !workspaceIntegrationToolAllowed(integration, tool.Name) {
			continue
		}
		return tool, true
	}
	return nil, false
}

func firstWorkspaceIntegrationProjectionTestTool(projection store.WorkspaceIntegrationConnectorProjection) (store.WorkspaceIntegration, workspaceIntegrationManifestTool, bool) {
	if !projection.Connection.Enabled {
		return store.WorkspaceIntegration{}, workspaceIntegrationManifestTool{}, false
	}
	policies := make(map[string]string, len(projection.Policies))
	for _, policy := range projection.Policies {
		name := strings.TrimSpace(policy.ToolName)
		if name == "" {
			continue
		}
		if normalized, ok := workspaceIntegrationNormalizePolicyState(policy.Policy); ok {
			policies[name] = normalized
		}
	}
	for _, snapshot := range projection.Tools {
		name := strings.TrimSpace(snapshot.ToolName)
		if name == "" || policies[name] == workspaceIntegrationPolicyBlock {
			continue
		}
		descriptor := workspaceIntegrationToolDescriptor{
			ToolAddress:     strings.TrimSpace(snapshot.ToolAddress),
			SnapshotID:      strings.TrimSpace(snapshot.ID),
			Name:            name,
			Source:          strings.TrimSpace(snapshot.Source),
			SourceSlug:      workspaceIntegrationProjectionDescriptorSourceSlug(projection.Source, snapshot),
			SourceKind:      firstNonEmpty(projection.Source.Kind, projection.Source.Importer),
			ConnectionID:    strings.TrimSpace(projection.Connection.ID),
			ConnectionSlug:  strings.TrimSpace(projection.Connection.Slug),
			ConnectionScope: strings.TrimSpace(projection.Connection.Scope),
			PolicyState:     firstNonEmpty(policies[name], workspaceIntegrationPolicyAllow),
			AuthState:       workspaceIntegrationConnectionAuthState(projection.Connection),
			LegacyToolID:    stringPtrValue(snapshot.LegacyToolID),
		}
		transport := workspaceIntegrationNormalizedRuntimeTransport(projection.Source, projection.Connection, snapshot, descriptor)
		tool, ok := workspaceIntegrationNormalizedRuntimeManifestTool(snapshot, transport)
		if !ok {
			continue
		}
		integration := workspaceIntegrationNormalizedRuntimeIntegration(projection.Source, projection.Connection, descriptor, transport)
		return integration, tool, true
	}
	return store.WorkspaceIntegration{}, workspaceIntegrationManifestTool{}, false
}

func workspaceIntegrationFromGenericCreateRequest(slug string, req genericWorkspaceIntegrationCreateRequest) (*store.WorkspaceIntegration, error) {
	return (&Server{}).workspaceIntegrationFromGenericCreateRequest(slug, req)
}

func (s *Server) workspaceIntegrationFromGenericCreateRequest(slug string, req genericWorkspaceIntegrationCreateRequest) (*store.WorkspaceIntegration, error) {
	transport := normalizeWorkspaceIntegrationTransport(req.Transport)
	if transport != "http" && transport != "mcp-remote" {
		return nil, fmt.Errorf("transport must be http or mcp-remote")
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if transport == "mcp-remote" && workspaceIntegrationCatalogSlugReserved(slug) {
		// A curated catalog entry may be saved under its own slug only when it
		// targets that entry's own fixed endpoint; an arbitrary custom server may
		// not squat a reserved slug.
		catalogURL := strings.TrimSpace(workspaceIntegrationCatalogRemoteURL(slug))
		if catalogURL == "" || !strings.EqualFold(endpoint, catalogURL) {
			return nil, fmt.Errorf("custom MCP integration slug %q is reserved by a known app", slug)
		}
	}
	if endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if !isAbsoluteHTTPURL(endpoint) {
		return nil, fmt.Errorf("endpoint must be an absolute http or https URL")
	}
	if err := workspaceIntegrationEndpointSafe(endpoint, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return nil, err
	}
	manifestTools, err := parseWorkspaceIntegrationManifest(req.ToolManifest)
	if err != nil {
		return nil, fmt.Errorf("workspace integration manifest is invalid")
	}
	if err := validateGenericWorkspaceIntegrationManifest(transport, manifestTools); err != nil {
		return nil, err
	}
	config, err := normalizeGenericWorkspaceIntegrationConfig(req.Config)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = slug
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = slug
	}
	endpointCopy := endpoint
	return &store.WorkspaceIntegration{
		Slug:         slug,
		DisplayName:  displayName,
		Kind:         kind,
		Transport:    transport,
		Endpoint:     &endpointCopy,
		ToolManifest: req.ToolManifest,
		Config:       config,
	}, nil
}

// workspaceIntegrationEndpointSafe rejects endpoints whose host is loopback,
// private, link-local, or an obvious internal name. The gateway dials these
// endpoints server-side with injected credentials, so an admin must not be able
// to point an integration at internal infrastructure or the cloud metadata
// service (169.254.169.254). This blocks IP-literal hosts and internal
// hostnames; DNS-rebinding-grade protection (a blocking dialer) is a separate
// hardening step.
func workspaceIntegrationEndpointSafe(endpoint string, allowInsecure bool) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL")
	}
	if !isAbsoluteHTTPURL(endpoint) {
		return fmt.Errorf("endpoint must be an absolute http or https URL")
	}
	if parsed.Scheme == "http" && !allowInsecure {
		return fmt.Errorf("endpoint must use https")
	}
	if parsed.User != nil {
		return fmt.Errorf("endpoint must not include userinfo")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("endpoint host is required")
	}
	if allowInsecure {
		return nil
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return fmt.Errorf("endpoint host %q is not allowed", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("endpoint host %q is not allowed", host)
		}
	}
	return nil
}

func validateGenericWorkspaceIntegrationManifest(transport string, manifestTools []workspaceIntegrationManifestTool) error {
	if len(manifestTools) == 0 {
		return fmt.Errorf("tool_manifest must include at least one tool")
	}
	if transport == "mcp-remote" && len(manifestTools) > workspaceIntegrationMaxEnabledTools {
		return fmt.Errorf("custom MCP integrations can enable at most %d tools", workspaceIntegrationMaxEnabledTools)
	}
	for _, tool := range manifestTools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return fmt.Errorf("tool_manifest tool names must not be empty")
		}
		if strings.Contains(name, ".") || strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("tool_manifest tool %q must not contain dots or whitespace", name)
		}
		switch transport {
		case "http":
			if tool.Request == nil {
				return fmt.Errorf("http tool %q must include a request mapping", name)
			}
		case "mcp-remote":
			if tool.Request != nil {
				return fmt.Errorf("mcp-remote tool %q must not include an http request mapping", name)
			}
		}
	}
	return nil
}

func normalizeGenericWorkspaceIntegrationConfig(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var config map[string]interface{}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("config must be a JSON object")
	}
	if config == nil {
		return nil, fmt.Errorf("config must be a JSON object")
	}
	if len(collectWorkspaceIntegrationConfigSecrets(raw)) > 0 {
		return nil, fmt.Errorf("secret config values must be stored through token")
	}
	return raw, nil
}

func (s *Server) saveGenericWorkspaceIntegrationToken(ctx context.Context, integrationID string, req genericWorkspaceIntegrationCreateRequest) error {
	token := strings.TrimSpace(*req.Token)
	encrypted := ""
	if token != "" {
		var err error
		encrypted, err = crypto.Encrypt(token, s.secretKey)
		if err != nil {
			return fmt.Errorf("encrypt workspace integration token: %w", err)
		}
	}
	tokenType := strings.TrimSpace(req.TokenType)
	if tokenType == "" {
		tokenType = "bearer"
	}
	return s.saveWorkspaceIntegrationCredentialAndConnectionCredentials(ctx, integrationID, encrypted, nil, &tokenType, nil)
}

func (s *Server) saveWorkspaceIntegrationCredentialAndConnectionCredentials(ctx context.Context, integrationID, secretEnc string, refreshEnc *string, tokenType *string, expiresAt *time.Time) error {
	if err := s.store.SetWorkspaceIntegrationCredential(ctx, &store.WorkspaceIntegrationCredential{
		IntegrationID: integrationID,
		SecretEnc:     secretEnc,
		RefreshEnc:    refreshEnc,
		TokenType:     tokenType,
		ExpiresAt:     expiresAt,
	}); err != nil {
		return err
	}

	lister, canList := s.store.(workspaceIntegrationLegacyConnectionLister)
	writer, canWrite := s.store.(workspaceIntegrationConnectionCredentialWriter)
	if !canList || !canWrite {
		return nil
	}
	connections, err := lister.ListWorkspaceIntegrationConnectionsByLegacyIntegration(ctx, integrationID)
	if err != nil {
		return fmt.Errorf("list workspace integration projection connections for credential: %w", err)
	}
	for _, connection := range connections {
		connectionID := strings.TrimSpace(connection.ID)
		if connectionID == "" {
			continue
		}
		if err := writer.SetWorkspaceIntegrationConnectionCredential(ctx, &store.WorkspaceIntegrationConnectionCredential{
			ConnectionID: connectionID,
			SecretEnc:    secretEnc,
			RefreshEnc:   refreshEnc,
			TokenType:    tokenType,
			ExpiresAt:    expiresAt,
		}); err != nil {
			return fmt.Errorf("save workspace integration connection credential: %w", err)
		}
	}
	return nil
}

func (s *Server) handleRevokeWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "integrationSlug"))
	if slug == "" {
		writeError(w, http.StatusBadRequest, "integration slug is required")
		return
	}

	deleter, ok := s.store.(workspaceIntegrationDeleter)
	if !ok {
		if s.handleRevokeNormalizedWorkspaceIntegrationConnection(w, r, accountID, *workspace, slug) {
			return
		}
		slog.Error("workspace_integrations.revoke_unavailable", "workspace_id", workspace.ID, "integration_slug", slug)
		writeError(w, http.StatusInternalServerError, "workspace integration revoke is not available")
		return
	}

	integration, err := deleter.DeleteWorkspaceIntegration(r.Context(), workspace.ID, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if s.handleRevokeNormalizedWorkspaceIntegrationConnection(w, r, accountID, *workspace, slug) {
				return
			}
			writeError(w, http.StatusNotFound, "workspace integration not found")
			return
		}
		slog.Error("workspace_integrations.revoke_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to revoke workspace integration")
		return
	}
	// Removing an integration is enforced live: the gateway no longer lists or
	// dispatches it on the next call. No token revocation — that would also drop
	// every OTHER integration's tools on all running machines until restart.
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.workspace_integration_revoked",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Revoked workspace integration '%s'", integration.DisplayName),
		Detail:    map[string]any{"workspace_id": workspace.ID, "integration_slug": integration.Slug},
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "revoked",
		"integration": workspaceIntegrationManagementItemFromStore(*integration),
	})
}

func (s *Server) handleRevokeNormalizedWorkspaceIntegrationConnection(w http.ResponseWriter, r *http.Request, accountID int, workspace store.Workspace, connectionSlug string) bool {
	deleter, ok := s.store.(workspaceIntegrationConnectionDeleter)
	if !ok {
		return false
	}
	projection, err := deleter.DeleteWorkspaceIntegrationConnection(r.Context(), workspace.ID, connectionSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workspace integration not found")
			return true
		}
		slog.Error("workspace_integrations.normalized_revoke_failed", "workspace_id", workspace.ID, "connection_slug", connectionSlug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to revoke workspace integration")
		return true
	}
	item := workspaceIntegrationManagementItemFromProjection(*projection, nil, nil)
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.workspace_integration_revoked",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Revoked workspace integration '%s'", item.DisplayName),
		Detail: map[string]any{
			"workspace_id":     workspace.ID,
			"connection_id":    projection.Connection.ID,
			"connection_slug":  projection.Connection.Slug,
			"integration_slug": projection.Connection.Slug,
		},
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "revoked",
		"integration": item,
	})
	return true
}

func (s *Server) handleUpdateWorkspaceIntegrationPolicy(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "integrationSlug"))
	if slug == "" {
		writeError(w, http.StatusBadRequest, "integration slug is required")
		return
	}

	var req struct {
		AllowedTools []string `json:"allowed_tools"`
		DeniedTools  []string `json:"denied_tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	integration, err := s.findWorkspaceIntegrationBySlug(r.Context(), workspace.ID, slug)
	if err != nil {
		if errors.Is(err, store.ErrPluginNotFound) {
			s.handleUpdateNormalizedWorkspaceIntegrationPolicy(w, r, accountID, *workspace, slug, req.AllowedTools, req.DeniedTools)
			return
		}
		writeError(w, http.StatusNotFound, "workspace integration not found")
		return
	}
	manifestTools, err := parseWorkspaceIntegrationManifest(integration.ToolManifest)
	if err != nil {
		writeError(w, http.StatusBadRequest, "workspace integration manifest is invalid")
		return
	}
	allowedTools, err := normalizeWorkspaceIntegrationPolicyTools(integration.Slug, manifestTools, req.AllowedTools)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(allowedTools) > workspaceIntegrationMaxEnabledTools {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("workspace integration policy can enable at most %d tools", workspaceIntegrationMaxEnabledTools))
		return
	}
	deniedTools, err := normalizeWorkspaceIntegrationPolicyTools(integration.Slug, manifestTools, req.DeniedTools)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	integration.AllowedTools = allowedTools
	integration.DeniedTools = deniedTools
	if err := s.store.UpsertWorkspaceIntegration(r.Context(), integration); err != nil {
		slog.Error("workspace_integrations.policy.upsert_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update workspace integration policy")
		return
	}
	// Policy (allow/deny) is read live by the gateway on every tools/list and
	// tools/call, so a tightened policy takes effect on the next call. No token
	// revocation — that would drop all tools on every running machine until restart.
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.workspace_integration_policy_updated",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Updated workspace integration policy for '%s'", integration.DisplayName),
		Detail: map[string]any{
			"workspace_id":     workspace.ID,
			"integration_slug": integration.Slug,
			"allowed_tools":    allowedTools,
			"denied_tools":     deniedTools,
		},
	})

	writeJSON(w, http.StatusOK, workspaceIntegrationManagementItemFromStore(*integration))
}

func (s *Server) handleUpdateNormalizedWorkspaceIntegrationPolicy(w http.ResponseWriter, r *http.Request, accountID int, workspace store.Workspace, connectionSlug string, allowedValues, deniedValues []string) {
	reader, ok := s.store.(workspaceIntegrationConnectorProjectionReader)
	if !ok {
		writeError(w, http.StatusNotFound, "workspace integration not found")
		return
	}
	projections, err := reader.ListWorkspaceIntegrationConnectorProjections(r.Context(), workspace.ID)
	if err != nil {
		slog.Error("workspace_integrations.normalized_policy.projections_failed", "workspace_id", workspace.ID, "connection_slug", connectionSlug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load workspace integration connection")
		return
	}
	projection, ok := findWorkspaceIntegrationProjectionByConnectionSlug(projections, connectionSlug)
	if !ok {
		writeError(w, http.StatusNotFound, "workspace integration not found")
		return
	}
	allowedTools, err := normalizeWorkspaceIntegrationProjectionPolicyTools(projection, allowedValues)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(allowedTools) > workspaceIntegrationMaxEnabledTools {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("workspace integration policy can enable at most %d tools", workspaceIntegrationMaxEnabledTools))
		return
	}
	deniedTools, err := normalizeWorkspaceIntegrationProjectionPolicyTools(projection, deniedValues)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writer, ok := s.store.(workspaceIntegrationToolPolicyReplacer)
	if !ok {
		slog.Error("workspace_integrations.normalized_policy.writer_unavailable", "workspace_id", workspace.ID, "connection_slug", projection.Connection.Slug)
		writeError(w, http.StatusInternalServerError, "workspace integration policy update is not available")
		return
	}
	policies := workspaceIntegrationProjectionPolicyRows(projection, allowedTools, deniedTools)
	if err := writer.ReplaceWorkspaceIntegrationToolPolicies(r.Context(), projection.Connection.ID, policies); err != nil {
		slog.Error("workspace_integrations.normalized_policy.replace_failed", "workspace_id", workspace.ID, "connection_id", projection.Connection.ID, "connection_slug", projection.Connection.Slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update workspace integration policy")
		return
	}
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.workspace_integration_policy_updated",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		Summary:   fmt.Sprintf("Updated workspace integration policy for '%s'", firstNonEmpty(projection.Connection.DisplayName, projection.Connection.Slug)),
		Detail: map[string]any{
			"workspace_id":     workspace.ID,
			"connection_id":    projection.Connection.ID,
			"connection_slug":  projection.Connection.Slug,
			"integration_slug": projection.Connection.Slug,
			"allowed_tools":    allowedTools,
			"denied_tools":     deniedTools,
		},
	})
	writeJSON(w, http.StatusOK, workspaceIntegrationManagementItemFromProjection(projection, allowedTools, deniedTools))
}

func (s *Server) ensureWorkspaceIntegrationRuntimeForWorkspace(ctx context.Context, accountID int, workspaceID string) error {
	machines, err := s.store.ListMachinesByAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("list machines: %w", err)
	}
	for i := range machines {
		machine := machines[i]
		if machine.WorkspaceID == nil || *machine.WorkspaceID != workspaceID {
			continue
		}
		if store.NormalizeMachineKind(machine.Kind) != store.MachineKindOpenClaw {
			continue
		}
		if err := s.store.EnableMachinePlugin(ctx, machine.ID, workspaceIntegrationPluginID, nil); err != nil {
			if errors.Is(err, store.ErrPluginNotFound) {
				return err
			}
			return fmt.Errorf("enable workspace integration runtime for machine %s: %w", machine.ID, err)
		}
		if machine.Status != "running" {
			continue
		}
		if err := s.reconcileMachinePlugins(&machine); err != nil {
			slog.Warn("workspace_integrations.runtime.reconcile_after_assign_failed", "machine_id", machine.ID, "error", err)
		}
		go s.pushMachineConfigAsync(machine.ID)
	}
	return nil
}

// revokeWorkspaceIntegrationTokensForWorkspace force-invalidates every machine's
// capability token in a workspace (the gateway rejects tokens issued before the
// new watermark). It is the single chokepoint for that operation and is
// deliberately NOT called by routine integration mutations (create / connect /
// revoke / policy / disable): those are all enforced live by the gateway, and
// revoking here drops EVERY integration's tools on EVERY running machine in the
// workspace until it restarts (the runtime does not hot-reload config). See the
// token-revocation regression. Retained for a future explicit admin "rotate
// tokens" action and so tests can assert routine handlers never trigger it.
func (s *Server) revokeWorkspaceIntegrationTokensForWorkspace(ctx context.Context, workspaceID string) error {
	revoker, ok := s.store.(workspaceIntegrationTokenRevoker)
	if !ok {
		return nil
	}
	return revoker.RevokeWorkspaceIntegrationTokensForWorkspace(ctx, workspaceID)
}

func (s *Server) handleEnableWorkspaceIntegrationRuntime(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}
	machineID := chi.URLParam(r, "machineID")
	if machineID == "" {
		writeError(w, http.StatusBadRequest, "machine_id is required")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if machine.WorkspaceID == nil || *machine.WorkspaceID != workspace.ID {
		writeError(w, http.StatusForbidden, "machine does not belong to this workspace")
		return
	}
	if !requireMutableMachineConfig(w, machine) {
		return
	}

	if err := s.store.EnableMachinePlugin(r.Context(), machineID, workspaceIntegrationPluginID, nil); err != nil {
		slog.Error("workspace_integrations.runtime.enable_failed", "machine_id", machineID, "error", err)
		if errors.Is(err, store.ErrPluginNotFound) {
			writeError(w, http.StatusNotFound, "workspace integration plugin not found or inactive")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	reconcileErr := s.reconcileMachinePlugins(machine)
	s.activity.Log(r.Context(), events.LogParams{
		Category:  "config",
		Action:    "config.workspace_integration_runtime_enabled",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Enabled workspace integrations on '%s'", machine.Name),
		Detail:    map[string]any{"plugin_id": workspaceIntegrationPluginID},
	})

	resp := map[string]string{"status": "ok"}
	if reconcileErr != nil {
		resp["reconcile_warning"] = reconcileErr.Error()
	}
	go s.pushMachineConfigAsync(machineID)
	writeJSON(w, http.StatusOK, resp)
}

func mockEchoToolManifest() json.RawMessage {
	return json.RawMessage(`[
		{
			"name": "echo",
			"description": "Echo input back through the workspace integration gateway",
			"parameters": {
				"type": "object",
				"properties": {
					"message": { "type": "string" }
				},
				"additionalProperties": true
			}
		}
	]`)
}

func workspaceIntegrationManagementItemFromStore(integration store.WorkspaceIntegration) workspaceIntegrationManagementItem {
	item := workspaceIntegrationManagementItem{
		ID:           integration.ID,
		WorkspaceID:  integration.WorkspaceID,
		Slug:         integration.Slug,
		DisplayName:  integration.DisplayName,
		Kind:         integration.Kind,
		Transport:    integration.Transport,
		Enabled:      integration.Enabled,
		ToolCount:    countWorkspaceIntegrationManifestTools(integration.ToolManifest),
		Tools:        workspaceIntegrationCatalogToolsFromManifest(integration.ToolManifest),
		Approved:     integration.ApprovedAt != nil || integration.Enabled,
		AllowedTools: append([]string(nil), integration.AllowedTools...),
		DeniedTools:  append([]string(nil), integration.DeniedTools...),
		ApprovedBy:   integration.ApprovedBy,
		ApprovedAt:   integration.ApprovedAt,
		ConnectedBy:  integration.ConnectedBy,
		ConnectedAt:  integration.ConnectedAt,
		CreatedAt:    integration.CreatedAt,
		UpdatedAt:    integration.UpdatedAt,
	}
	item.Target = workspaceIntegrationTarget(integration)
	item.Scopes, item.PermissionLevels = workspaceIntegrationOAuthMetadata(integration)
	item.ServiceStatus = workspaceIntegrationGoogleServiceStatusMetadata(integration)
	item.Snapshot = workspaceIntegrationSnapshotMetadata(integration)
	return item
}

func workspaceIntegrationManagementItemFromProjection(projection store.WorkspaceIntegrationConnectorProjection, allowedTools, deniedTools []string) workspaceIntegrationManagementItem {
	connection := projection.Connection
	source := projection.Source
	kind := strings.TrimSpace(source.Kind)
	if kind == "" {
		kind = strings.TrimSpace(source.Importer)
	}
	tools := make([]workspaceIntegrationCatalogTool, 0, len(projection.Tools))
	for _, tool := range projection.Tools {
		name := strings.TrimSpace(tool.ToolName)
		if name == "" {
			continue
		}
		access := "Read"
		if strings.EqualFold(strings.TrimSpace(tool.Access), "write") {
			access = "Write"
		}
		tools = append(tools, workspaceIntegrationCatalogTool{
			Name:        name,
			Description: tool.Description,
			Access:      access,
			Mode:        "Interactive",
			Source:      strings.ToLower(strings.TrimSpace(firstNonEmpty(tool.Source, source.Importer, source.Kind))),
		})
	}
	transport := workspaceIntegrationConnectionConfigString(connection.Config, "transport")
	if transport == "" {
		transport = strings.TrimSpace(source.Importer)
	}
	return workspaceIntegrationManagementItem{
		ID:           connection.ID,
		WorkspaceID:  connection.WorkspaceID,
		Slug:         connection.Slug,
		DisplayName:  firstNonEmpty(connection.DisplayName, source.DisplayName, connection.Slug),
		Kind:         kind,
		Transport:    transport,
		Target:       workspaceIntegrationProjectionTarget(projection),
		Enabled:      connection.Enabled,
		ToolCount:    len(tools),
		Tools:        tools,
		Approved:     connection.Enabled,
		AllowedTools: append([]string(nil), allowedTools...),
		DeniedTools:  append([]string(nil), deniedTools...),
		Snapshot:     workspaceIntegrationProjectionSnapshotMetadata(projection),
		CreatedAt:    connection.CreatedAt,
		UpdatedAt:    connection.UpdatedAt,
	}
}

func (s *Server) workspaceIntegrationNormalizedManagementItems(ctx context.Context, workspaceID string, integrations []store.WorkspaceIntegration) []workspaceIntegrationManagementItem {
	reader, ok := s.store.(workspaceIntegrationConnectorProjectionReader)
	if !ok {
		return nil
	}
	projections, err := reader.ListWorkspaceIntegrationConnectorProjections(ctx, workspaceID)
	if err != nil {
		slog.Warn("workspace_integrations.normalized_list_failed", "workspace_id", workspaceID, "error", err)
		return nil
	}
	legacyIDs := make(map[string]struct{}, len(integrations))
	legacySlugs := make(map[string]struct{}, len(integrations))
	for _, integration := range integrations {
		if id := strings.TrimSpace(integration.ID); id != "" {
			legacyIDs[id] = struct{}{}
		}
		if slug := strings.TrimSpace(integration.Slug); slug != "" {
			legacySlugs[slug] = struct{}{}
		}
	}
	out := make([]workspaceIntegrationManagementItem, 0, len(projections))
	for _, projection := range projections {
		connection := projection.Connection
		connectionSlug := strings.TrimSpace(connection.Slug)
		if _, ok := legacySlugs[connectionSlug]; ok {
			continue
		}
		if connection.LegacyIntegrationID != nil {
			if _, ok := legacyIDs[strings.TrimSpace(*connection.LegacyIntegrationID)]; ok {
				continue
			}
		}
		allowedTools, deniedTools := workspaceIntegrationProjectionPolicyLists(projection)
		out = append(out, workspaceIntegrationManagementItemFromProjection(projection, allowedTools, deniedTools))
	}
	return out
}

func (s *Server) ensureWorkspaceIntegrationConnectionSlugAvailable(ctx context.Context, workspaceID, connectionSlug string, existing *store.WorkspaceIntegration) error {
	connectionSlug = strings.TrimSpace(connectionSlug)
	if connectionSlug == "" {
		return nil
	}
	reader, ok := s.store.(workspaceIntegrationConnectorProjectionReader)
	if !ok {
		return nil
	}
	projections, err := reader.ListWorkspaceIntegrationConnectorProjections(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("list workspace integration connector projections: %w", err)
	}
	existingID := ""
	if existing != nil {
		existingID = strings.TrimSpace(existing.ID)
	}
	for _, projection := range projections {
		if strings.TrimSpace(projection.Connection.Slug) != connectionSlug {
			continue
		}
		legacyID := ""
		if projection.Connection.LegacyIntegrationID != nil {
			legacyID = strings.TrimSpace(*projection.Connection.LegacyIntegrationID)
		}
		if existingID != "" && legacyID == existingID {
			continue
		}
		return errWorkspaceIntegrationConnectionSlugConflict
	}
	return nil
}

func workspaceIntegrationProjectionPolicyLists(projection store.WorkspaceIntegrationConnectorProjection) ([]string, []string) {
	policies := make(map[string]string, len(projection.Policies))
	for _, policy := range projection.Policies {
		name := strings.TrimSpace(policy.ToolName)
		if name == "" {
			continue
		}
		if normalized, ok := workspaceIntegrationNormalizePolicyState(policy.Policy); ok {
			policies[name] = normalized
		}
	}
	allowed := make([]string, 0, len(projection.Tools))
	denied := make([]string, 0, len(projection.Tools))
	seen := make(map[string]struct{}, len(projection.Tools))
	for _, tool := range projection.Tools {
		name := strings.TrimSpace(tool.ToolName)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		switch policies[name] {
		case workspaceIntegrationPolicyAllow:
			allowed = append(allowed, name)
		case workspaceIntegrationPolicyBlock:
			denied = append(denied, name)
		}
	}
	return allowed, denied
}

func workspaceIntegrationConnectionConfigString(config json.RawMessage, key string) string {
	if len(config) == 0 {
		return ""
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(config, &raw); err != nil {
		return ""
	}
	return workspaceIntegrationConfigString(raw, key)
}

func workspaceIntegrationOAuthMetadata(integration store.WorkspaceIntegration) ([]string, map[string]string) {
	if len(integration.Config) == 0 {
		return nil, nil
	}
	var cfg struct {
		Scopes           []string          `json:"scopes"`
		PermissionLevels map[string]string `json:"permission_levels"`
	}
	if err := json.Unmarshal(integration.Config, &cfg); err != nil {
		return nil, nil
	}
	var scopes []string
	if len(cfg.Scopes) > 0 {
		scopes = append([]string(nil), cfg.Scopes...)
	}
	var permissionLevels map[string]string
	if len(cfg.PermissionLevels) > 0 {
		permissionLevels = make(map[string]string, len(cfg.PermissionLevels))
		for key, value := range cfg.PermissionLevels {
			permissionLevels[key] = value
		}
	}
	return scopes, permissionLevels
}

func workspaceIntegrationGoogleServiceStatusMetadata(integration store.WorkspaceIntegration) map[string]googleWorkspaceServiceStatus {
	if integration.Slug != googleWorkspaceIntegrationSlug && integration.Kind != "google_workspace" {
		return nil
	}
	cfg, err := parseGoogleWorkspaceIntegrationConfig(integration)
	if err != nil || len(cfg.ServiceStatus) == 0 {
		return nil
	}
	out := make(map[string]googleWorkspaceServiceStatus, len(cfg.ServiceStatus))
	for service, status := range cfg.ServiceStatus {
		out[service] = status
	}
	return out
}

func workspaceIntegrationProjectionTarget(projection store.WorkspaceIntegrationConnectorProjection) string {
	if target := workspaceIntegrationTargetFromConfig(projection.Connection.Config); target != "" {
		return target
	}
	return workspaceIntegrationTargetFromConfig(projection.Source.Config)
}

func workspaceIntegrationTarget(integration store.WorkspaceIntegration) string {
	return workspaceIntegrationTargetFromConfig(integration.Config)
}

func workspaceIntegrationTargetFromConfig(configRaw json.RawMessage) string {
	if len(configRaw) == 0 {
		return ""
	}
	var config map[string]interface{}
	if err := json.Unmarshal(configRaw, &config); err != nil {
		return ""
	}
	if target := workspaceIntegrationConfigString(config, "display_target"); target != "" {
		return target
	}
	if email := workspaceIntegrationConfigString(config, "email"); email != "" {
		return email
	}
	owner := workspaceIntegrationConfigString(config, "owner")
	repo := workspaceIntegrationConfigString(config, "repo")
	if owner != "" && repo != "" {
		return owner + "/" + repo
	}
	return ""
}

func workspaceIntegrationSnapshotMetadata(integration store.WorkspaceIntegration) *workspaceIntegrationSnapshot {
	return workspaceIntegrationSnapshotMetadataFromConfig(integration.Config)
}

func workspaceIntegrationProjectionSnapshotMetadata(projection store.WorkspaceIntegrationConnectorProjection) *workspaceIntegrationSnapshot {
	if snapshot := workspaceIntegrationSnapshotMetadataFromConfig(projection.Connection.Config); snapshot != nil {
		return snapshot
	}
	return workspaceIntegrationSnapshotMetadataFromConfig(projection.Source.Config)
}

func workspaceIntegrationSnapshotMetadataFromConfig(configRaw json.RawMessage) *workspaceIntegrationSnapshot {
	if len(configRaw) == 0 {
		return nil
	}
	var config map[string]interface{}
	if err := json.Unmarshal(configRaw, &config); err != nil {
		return nil
	}
	probedAtRaw := workspaceIntegrationConfigString(config, "probed_at")
	serverName := workspaceIntegrationConfigString(config, "server_name")
	serverVersion := workspaceIntegrationConfigString(config, "server_version")
	protocolVersion := workspaceIntegrationConfigString(config, "protocol_version")
	if probedAtRaw == "" && serverName == "" && serverVersion == "" && protocolVersion == "" {
		return nil
	}
	var probedAt *time.Time
	if probedAtRaw != "" {
		if parsed, err := time.Parse(time.RFC3339, probedAtRaw); err == nil {
			parsed = parsed.UTC()
			probedAt = &parsed
		}
	}
	return &workspaceIntegrationSnapshot{
		ServerName:      serverName,
		ServerVersion:   serverVersion,
		ProtocolVersion: protocolVersion,
		ProbedAt:        probedAt,
	}
}

func workspaceIntegrationConfigString(config map[string]interface{}, key string) string {
	value, ok := config[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *Server) findWorkspaceIntegrationBySlug(ctx context.Context, workspaceID string, slug string) (*store.WorkspaceIntegration, error) {
	integrations, err := s.store.ListWorkspaceIntegrations(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range integrations {
		if integrations[i].Slug == slug {
			return &integrations[i], nil
		}
	}
	return nil, store.ErrPluginNotFound
}

func findWorkspaceIntegrationProjectionByConnectionSlug(projections []store.WorkspaceIntegrationConnectorProjection, connectionSlug string) (store.WorkspaceIntegrationConnectorProjection, bool) {
	connectionSlug = strings.TrimSpace(connectionSlug)
	for _, projection := range projections {
		if projection.Connection.Slug == connectionSlug || projection.Connection.ID == connectionSlug {
			return projection, true
		}
	}
	return store.WorkspaceIntegrationConnectorProjection{}, false
}

func normalizeWorkspaceIntegrationPolicyTools(integrationSlug string, manifestTools []workspaceIntegrationManifestTool, values []string) ([]string, error) {
	available := make(map[string]struct{}, len(manifestTools))
	for _, tool := range manifestTools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		available[name] = struct{}{}
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, errors.New("policy tool names must not be empty")
		}
		if prefixedSlug, name, ok := strings.Cut(value, "."); ok {
			if prefixedSlug != integrationSlug {
				return nil, fmt.Errorf("policy tool %q does not belong to integration %q", value, integrationSlug)
			}
			value = name
		}
		if _, ok := available[value]; !ok {
			return nil, fmt.Errorf("unknown policy tool %q for integration %q", value, integrationSlug)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func normalizeWorkspaceIntegrationProjectionPolicyTools(projection store.WorkspaceIntegrationConnectorProjection, values []string) ([]string, error) {
	available := make(map[string]struct{}, len(projection.Tools))
	aliases := make(map[string]string, len(projection.Tools)*3)
	connectionSlug := strings.TrimSpace(projection.Connection.Slug)
	for _, tool := range projection.Tools {
		name := strings.TrimSpace(tool.ToolName)
		if name == "" {
			continue
		}
		available[name] = struct{}{}
		aliases[name] = name
		if connectionSlug != "" {
			aliases[connectionSlug+"."+name] = name
		}
		if toolAddress := strings.TrimSpace(tool.ToolAddress); toolAddress != "" {
			aliases[toolAddress] = name
		}
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, errors.New("policy tool names must not be empty")
		}
		if alias, ok := aliases[value]; ok {
			value = alias
		} else if prefixedSlug, name, ok := strings.Cut(value, "."); ok {
			if prefixedSlug != connectionSlug {
				return nil, fmt.Errorf("policy tool %q does not belong to connection %q", value, connectionSlug)
			}
			value = name
		}
		if _, ok := available[value]; !ok {
			return nil, fmt.Errorf("unknown policy tool %q for connection %q", raw, connectionSlug)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func workspaceIntegrationProjectionPolicyRows(projection store.WorkspaceIntegrationConnectorProjection, allowedTools, deniedTools []string) []store.WorkspaceIntegrationToolPolicy {
	allowed := make(map[string]struct{}, len(allowedTools))
	for _, tool := range allowedTools {
		allowed[tool] = struct{}{}
	}
	denied := make(map[string]struct{}, len(deniedTools))
	for _, tool := range deniedTools {
		denied[tool] = struct{}{}
	}
	policies := make([]store.WorkspaceIntegrationToolPolicy, 0, len(projection.Tools))
	seen := make(map[string]struct{}, len(projection.Tools))
	for _, tool := range projection.Tools {
		name := strings.TrimSpace(tool.ToolName)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		policy := workspaceIntegrationPolicyAllow
		if _, blocked := denied[name]; blocked {
			policy = workspaceIntegrationPolicyBlock
		} else if len(allowed) > 0 {
			if _, ok := allowed[name]; !ok {
				policy = workspaceIntegrationPolicyBlock
			}
		}
		policies = append(policies, store.WorkspaceIntegrationToolPolicy{
			WorkspaceID:  projection.Connection.WorkspaceID,
			ConnectionID: projection.Connection.ID,
			ToolName:     name,
			Policy:       policy,
			Source:       "api",
		})
	}
	return policies
}

func normalizeGitHubIdentifier(value string, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("github %s is required", field)
	}
	if strings.ContainsAny(value, `/\`) {
		return "", fmt.Errorf("github %s must not contain slashes", field)
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("github %s must not contain whitespace", field)
	}
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return "", fmt.Errorf("github %s must not start or end with '.'", field)
	}
	return value, nil
}
