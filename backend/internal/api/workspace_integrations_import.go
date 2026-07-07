package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

const (
	workspaceIntegrationImportMaxSpecBytes      = 1 << 20
	workspaceIntegrationImportMaxGeneratedTools = workspaceIntegrationMaxDiscoveredTools
)

type workspaceIntegrationImportRequest struct {
	Type        string                         `json:"type"`
	Slug        string                         `json:"slug"`
	DisplayName string                         `json:"display_name"`
	SpecURL     string                         `json:"spec_url,omitempty"`
	SpecText    string                         `json:"spec_text,omitempty"`
	Spec        json.RawMessage                `json:"spec,omitempty"`
	BaseURL     string                         `json:"base_url,omitempty"`
	Endpoint    string                         `json:"endpoint,omitempty"`
	Auth        workspaceIntegrationImportAuth `json:"auth,omitempty"`
	Token       *string                        `json:"token,omitempty"`
	TokenType   string                         `json:"token_type,omitempty"`
}

type workspaceIntegrationImportAuth struct {
	Type     string `json:"type,omitempty"`
	Header   string `json:"header,omitempty"`
	Scheme   string `json:"scheme,omitempty"`
	TokenURL string `json:"token_url,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Resource string `json:"resource,omitempty"`
}

type workspaceIntegrationImportBuild struct {
	Integration  store.WorkspaceIntegration
	Tools        []workspaceIntegrationManifestTool
	CatalogTools []workspaceIntegrationCatalogTool
	AllowedTools []string
	DeniedTools  []string
	AuthKind     string
}

type workspaceIntegrationImportPreviewResponse struct {
	Type         string                             `json:"type"`
	Slug         string                             `json:"slug"`
	DisplayName  string                             `json:"display_name"`
	Kind         string                             `json:"kind"`
	Transport    string                             `json:"transport"`
	Endpoint     string                             `json:"endpoint"`
	AuthKind     string                             `json:"auth_kind"`
	Tools        []workspaceIntegrationCatalogTool  `json:"tools"`
	AllowedTools []string                           `json:"allowed_tools"`
	DeniedTools  []string                           `json:"denied_tools"`
	ToolManifest []workspaceIntegrationManifestTool `json:"tool_manifest"`
}

func (s *Server) handlePreviewWorkspaceIntegrationImport(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	if _, ok := s.workspaceForIntegrationRequest(w, r, accountID); !ok {
		return
	}
	var req workspaceIntegrationImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	build, err := s.buildWorkspaceIntegrationImport(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, workspaceIntegrationImportPreview(build))
}

func (s *Server) handleCreateWorkspaceIntegrationImport(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	var req workspaceIntegrationImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	build, err := s.buildWorkspaceIntegrationImport(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if workspaceIntegrationImportAuthRequiresToken(build.AuthKind) && req.Token != nil && strings.TrimSpace(*req.Token) != "" && s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}
	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}
	slug := build.Integration.Slug
	existing, lookupErr := s.findWorkspaceIntegrationBySlug(r.Context(), workspace.ID, slug)
	if lookupErr != nil && !errors.Is(lookupErr, store.ErrPluginNotFound) {
		slog.Error("workspace_integrations.import.lookup_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", lookupErr)
		writeError(w, http.StatusInternalServerError, "failed to load workspace integration")
		return
	}
	if err := s.ensureWorkspaceIntegrationConnectionSlugAvailable(r.Context(), workspace.ID, slug, existing); err != nil {
		if errors.Is(err, errWorkspaceIntegrationConnectionSlugConflict) {
			writeError(w, http.StatusConflict, errWorkspaceIntegrationConnectionSlugConflict.Error())
			return
		}
		slog.Error("workspace_integrations.import.connection_slug_check_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to inspect workspace integration connections")
		return
	}
	hasToken := req.Token != nil && strings.TrimSpace(*req.Token) != ""
	if workspaceIntegrationImportAuthRequiresToken(build.AuthKind) && existing == nil && !hasToken {
		writeError(w, http.StatusBadRequest, "token is required for this auth mapping")
		return
	}

	now := time.Now().UTC()
	actorID := userIDFromContext(r.Context())
	integration := build.Integration
	integration.WorkspaceID = workspace.ID
	integration.Enabled = true
	integration.ApprovedBy = actorID
	integration.ApprovedAt = &now
	integration.ConnectedBy = actorID
	integration.ConnectedAt = &now
	if existing != nil {
		integration.Enabled = existing.Enabled
		integration.ApprovedBy = nil
		integration.ApprovedAt = nil
		if !hasToken {
			integration.ConnectedBy = nil
			integration.ConnectedAt = nil
		}
	}
	if err := s.store.UpsertWorkspaceIntegration(r.Context(), &integration); err != nil {
		slog.Error("workspace_integrations.import.upsert_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save workspace integration")
		return
	}

	failCreate := func(status int, clientMsg, logKey string, cause error) {
		slog.Error(logKey, "workspace_id", workspace.ID, "integration_slug", slug, "error", cause)
		if existing == nil {
			if deleter, ok := s.store.(workspaceIntegrationDeleter); ok {
				if _, derr := deleter.DeleteWorkspaceIntegration(r.Context(), workspace.ID, slug); derr != nil {
					slog.Error("workspace_integrations.import.rollback_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", derr)
				}
			}
		}
		writeError(w, status, clientMsg)
	}
	if hasToken {
		if err := s.saveGenericWorkspaceIntegrationToken(r.Context(), integration.ID, genericWorkspaceIntegrationCreateRequest{
			Token:     req.Token,
			TokenType: workspaceIntegrationImportTokenType(req),
		}); err != nil {
			failCreate(http.StatusInternalServerError, "failed to save workspace integration credential", "workspace_integrations.import.credential_set_failed", err)
			return
		}
	}
	if err := s.ensureWorkspaceIntegrationRuntimeForWorkspace(r.Context(), accountID, workspace.ID); err != nil {
		failCreate(http.StatusInternalServerError, "failed to assign workspace integration to machines", "workspace_integrations.runtime.ensure_failed", err)
		return
	}
	if s.activity != nil {
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "config",
			Action:    "config.workspace_integration_created",
			Status:    "success",
			ActorType: "user",
			ActorID:   actorID,
			AccountID: &accountID,
			Summary:   fmt.Sprintf("Imported workspace integration '%s'", integration.DisplayName),
			Detail: map[string]any{
				"workspace_id":     workspace.ID,
				"integration_slug": integration.Slug,
				"importer":         integration.Kind,
				"transport":        integration.Transport,
			},
		})
	}
	writeJSON(w, http.StatusOK, workspaceIntegrationManagementItemFromStore(integration))
}

func workspaceIntegrationImportPreview(build workspaceIntegrationImportBuild) workspaceIntegrationImportPreviewResponse {
	endpoint := ""
	if build.Integration.Endpoint != nil {
		endpoint = *build.Integration.Endpoint
	}
	return workspaceIntegrationImportPreviewResponse{
		Type:         build.Integration.Kind,
		Slug:         build.Integration.Slug,
		DisplayName:  build.Integration.DisplayName,
		Kind:         build.Integration.Kind,
		Transport:    build.Integration.Transport,
		Endpoint:     endpoint,
		AuthKind:     build.AuthKind,
		Tools:        build.CatalogTools,
		AllowedTools: build.AllowedTools,
		DeniedTools:  build.DeniedTools,
		ToolManifest: build.Tools,
	}
}

func (s *Server) buildWorkspaceIntegrationImport(ctx context.Context, req workspaceIntegrationImportRequest) (workspaceIntegrationImportBuild, error) {
	importType := strings.ToLower(strings.TrimSpace(req.Type))
	switch importType {
	case "openapi":
		return s.buildOpenAPIWorkspaceIntegrationImport(ctx, req)
	case "graphql":
		return s.buildGraphQLWorkspaceIntegrationImport(ctx, req)
	default:
		return workspaceIntegrationImportBuild{}, fmt.Errorf("type must be openapi or graphql")
	}
}

func (s *Server) buildOpenAPIWorkspaceIntegrationImport(ctx context.Context, req workspaceIntegrationImportRequest) (workspaceIntegrationImportBuild, error) {
	specBytes, specSourceURL, err := s.loadWorkspaceIntegrationImportSpec(ctx, req)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	spec, err := decodeWorkspaceIntegrationSpecObject(specBytes)
	if err != nil {
		return workspaceIntegrationImportBuild{}, fmt.Errorf("decode OpenAPI spec: %w", err)
	}
	version := stringFromMap(spec, "openapi")
	if !strings.HasPrefix(version, "3.") {
		return workspaceIntegrationImportBuild{}, fmt.Errorf("OpenAPI 3.x spec is required")
	}
	if err := rejectOpenAPIRemoteRefs(spec); err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	slug, err := workspaceIntegrationImportSlug(req.Slug)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = stringFromMap(mapFromMap(spec, "info"), "title")
	}
	if displayName == "" {
		displayName = slug
	}
	endpoint, err := openAPIWorkspaceIntegrationEndpoint(spec, req.BaseURL, specSourceURL)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	if err := workspaceIntegrationEndpointSafe(endpoint, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	authSpec, authKind, err := workspaceIntegrationHTTPRequestAuthFromImport(ctx, req.Auth, s.allowInsecureWorkspaceIntegrationEndpoints)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	tools, err := openAPIWorkspaceIntegrationTools(spec, authSpec)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	if len(tools) == 0 {
		return workspaceIntegrationImportBuild{}, fmt.Errorf("OpenAPI spec did not contain callable operations")
	}
	if len(tools) > workspaceIntegrationImportMaxGeneratedTools {
		return workspaceIntegrationImportBuild{}, fmt.Errorf("OpenAPI spec generated %d tools, max %d", len(tools), workspaceIntegrationImportMaxGeneratedTools)
	}
	manifest, err := json.Marshal(tools)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	config, err := workspaceIntegrationImportConfig("openapi", req, specSourceURL)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	endpointCopy := endpoint
	allowedTools, deniedTools := workspaceIntegrationImportedInitialPolicy(tools)
	return workspaceIntegrationImportBuild{
		Integration: store.WorkspaceIntegration{
			Slug:         slug,
			DisplayName:  displayName,
			Kind:         "openapi",
			Transport:    "http",
			Endpoint:     &endpointCopy,
			ToolManifest: manifest,
			Config:       config,
			AllowedTools: allowedTools,
			DeniedTools:  deniedTools,
		},
		Tools:        tools,
		CatalogTools: workspaceIntegrationCatalogToolsFromManifest(manifest),
		AllowedTools: allowedTools,
		DeniedTools:  deniedTools,
		AuthKind:     authKind,
	}, nil
}

func (s *Server) buildGraphQLWorkspaceIntegrationImport(ctx context.Context, req workspaceIntegrationImportRequest) (workspaceIntegrationImportBuild, error) {
	slug, err := workspaceIntegrationImportSlug(req.Slug)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = slug
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(req.BaseURL)
	}
	if endpoint == "" {
		return workspaceIntegrationImportBuild{}, fmt.Errorf("endpoint is required")
	}
	if !isAbsoluteHTTPURL(endpoint) {
		return workspaceIntegrationImportBuild{}, fmt.Errorf("endpoint must be an absolute http or https URL")
	}
	if err := workspaceIntegrationEndpointSafe(endpoint, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	authSpec, authKind, err := workspaceIntegrationHTTPRequestAuthFromImport(ctx, req.Auth, s.allowInsecureWorkspaceIntegrationEndpoints)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	specBytes, _, err := s.loadWorkspaceIntegrationImportSpec(ctx, req)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	tools, err := graphQLWorkspaceIntegrationTools(specBytes, authSpec)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	if len(tools) == 0 {
		return workspaceIntegrationImportBuild{}, fmt.Errorf("GraphQL schema did not contain query or mutation fields")
	}
	if len(tools) > workspaceIntegrationImportMaxGeneratedTools {
		return workspaceIntegrationImportBuild{}, fmt.Errorf("GraphQL schema generated %d tools, max %d", len(tools), workspaceIntegrationImportMaxGeneratedTools)
	}
	manifest, err := json.Marshal(tools)
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	config, err := workspaceIntegrationImportConfig("graphql", req, "")
	if err != nil {
		return workspaceIntegrationImportBuild{}, err
	}
	endpointCopy := endpoint
	allowedTools, deniedTools := workspaceIntegrationImportedInitialPolicy(tools)
	return workspaceIntegrationImportBuild{
		Integration: store.WorkspaceIntegration{
			Slug:         slug,
			DisplayName:  displayName,
			Kind:         "graphql",
			Transport:    "http",
			Endpoint:     &endpointCopy,
			ToolManifest: manifest,
			Config:       config,
			AllowedTools: allowedTools,
			DeniedTools:  deniedTools,
		},
		Tools:        tools,
		CatalogTools: workspaceIntegrationCatalogToolsFromManifest(manifest),
		AllowedTools: allowedTools,
		DeniedTools:  deniedTools,
		AuthKind:     authKind,
	}, nil
}

func (s *Server) loadWorkspaceIntegrationImportSpec(ctx context.Context, req workspaceIntegrationImportRequest) ([]byte, string, error) {
	if len(req.Spec) > 0 && string(req.Spec) != "null" {
		if len(req.Spec) > workspaceIntegrationImportMaxSpecBytes {
			return nil, "", fmt.Errorf("spec exceeds %d bytes", workspaceIntegrationImportMaxSpecBytes)
		}
		var asString string
		if err := json.Unmarshal(req.Spec, &asString); err == nil {
			return []byte(asString), "", nil
		}
		return req.Spec, "", nil
	}
	if text := strings.TrimSpace(req.SpecText); text != "" {
		if len(text) > workspaceIntegrationImportMaxSpecBytes {
			return nil, "", fmt.Errorf("spec exceeds %d bytes", workspaceIntegrationImportMaxSpecBytes)
		}
		return []byte(text), "", nil
	}
	specURL := strings.TrimSpace(req.SpecURL)
	if specURL == "" {
		return nil, "", fmt.Errorf("spec_text, spec, or spec_url is required")
	}
	if err := workspaceIntegrationImportURLSafe(ctx, specURL, s.allowInsecureWorkspaceIntegrationEndpoints, "spec URL"); err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, specURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build spec request: %w", err)
	}
	request.Header.Set("Accept", "application/json, application/yaml, text/yaml, */*")
	resp, err := workspaceIntegrationProbeHTTPClient(s.allowInsecureWorkspaceIntegrationEndpoints).Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("fetch spec: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("spec URL returned HTTP %d", resp.StatusCode)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(resp.Body, workspaceIntegrationImportMaxSpecBytes+1)); err != nil {
		return nil, "", fmt.Errorf("read spec: %w", err)
	}
	if buf.Len() > workspaceIntegrationImportMaxSpecBytes {
		return nil, "", fmt.Errorf("spec exceeds %d bytes", workspaceIntegrationImportMaxSpecBytes)
	}
	return buf.Bytes(), specURL, nil
}

func workspaceIntegrationImportURLSafe(ctx context.Context, rawURL string, allowInsecure bool, label string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("%s is required", label)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid %s", label)
	}
	if parsed.Scheme != "https" && (!allowInsecure || parsed.Scheme != "http") {
		return fmt.Errorf("%s must use https", label)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not include userinfo", label)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("%s host is required", label)
	}
	if allowInsecure {
		return nil
	}
	return workspaceIntegrationEndpointHostSafe(ctx, parsed.Hostname())
}

func decodeWorkspaceIntegrationSpecObject(data []byte) (map[string]interface{}, error) {
	var raw interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(normalized, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("spec must be an object")
	}
	return out, nil
}

func workspaceIntegrationImportSlug(slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", fmt.Errorf("slug is required")
	}
	if !isValidSlug(slug) {
		return "", fmt.Errorf("invalid integration slug")
	}
	if workspaceIntegrationCatalogSlugReserved(slug) {
		return "", fmt.Errorf("integration slug %q is reserved by a known app", slug)
	}
	return slug, nil
}

func workspaceIntegrationImportConfig(importer string, req workspaceIntegrationImportRequest, specURL string) (json.RawMessage, error) {
	source := "upload"
	if strings.TrimSpace(specURL) != "" {
		source = "url"
	}
	config := map[string]interface{}{
		"importer":    importer,
		"spec_source": source,
		"auth_type":   workspaceIntegrationImportAuthKind(req.Auth),
		"imported_at": time.Now().UTC().Format(time.RFC3339),
	}
	if specURL != "" {
		if parsed, err := url.Parse(specURL); err == nil {
			config["spec_host"] = parsed.Hostname()
			config["spec_path"] = parsed.EscapedPath()
		}
	}
	return json.Marshal(config)
}

func workspaceIntegrationImportAuthKind(auth workspaceIntegrationImportAuth) string {
	kind := strings.ToLower(strings.TrimSpace(auth.Type))
	if kind == "" {
		return "none"
	}
	return kind
}

func workspaceIntegrationImportAuthRequiresToken(authKind string) bool {
	switch strings.ToLower(strings.TrimSpace(authKind)) {
	case "bearer", "api_key_header", "oauth":
		return true
	default:
		return false
	}
}

func workspaceIntegrationImportTokenType(req workspaceIntegrationImportRequest) string {
	if tokenType := strings.TrimSpace(req.TokenType); tokenType != "" {
		return tokenType
	}
	return workspaceIntegrationImportAuthKind(req.Auth)
}

func workspaceIntegrationHTTPRequestAuthFromImport(ctx context.Context, auth workspaceIntegrationImportAuth, allowInsecure bool) (*workspaceIntegrationHTTPAuth, string, error) {
	kind := workspaceIntegrationImportAuthKind(auth)
	switch kind {
	case "none":
		return nil, "none", nil
	case "bearer":
		header := strings.TrimSpace(auth.Header)
		if header == "" {
			header = "Authorization"
		}
		scheme := strings.TrimSpace(auth.Scheme)
		if scheme == "" {
			scheme = "Bearer"
		}
		return &workspaceIntegrationHTTPAuth{Type: "bearer", Header: header, Scheme: scheme, Required: true}, kind, nil
	case "api_key_header":
		header := strings.TrimSpace(auth.Header)
		if header == "" {
			return nil, "", fmt.Errorf("auth.header is required for api_key_header")
		}
		return &workspaceIntegrationHTTPAuth{Type: "api_key_header", Header: header, Required: true}, kind, nil
	case "oauth":
		tokenURL := strings.TrimSpace(auth.TokenURL)
		if tokenURL == "" {
			return nil, "", fmt.Errorf("auth.token_url is required for oauth")
		}
		if err := workspaceIntegrationImportURLSafe(ctx, tokenURL, allowInsecure, "auth.token_url"); err != nil {
			return nil, "", err
		}
		return &workspaceIntegrationHTTPAuth{
			Type:     "oauth",
			Header:   firstNonEmpty(auth.Header, "Authorization"),
			TokenURL: tokenURL,
			ClientID: strings.TrimSpace(auth.ClientID),
			Resource: strings.TrimSpace(auth.Resource),
			Required: true,
		}, kind, nil
	default:
		return nil, "", fmt.Errorf("auth.type must be none, bearer, api_key_header, or oauth")
	}
}

func workspaceIntegrationImportedInitialPolicy(tools []workspaceIntegrationManifestTool) ([]string, []string) {
	denied := make([]string, 0)
	for _, tool := range tools {
		if workspaceIntegrationManifestToolAccess(tool) == "write" {
			denied = append(denied, tool.Name)
		}
	}
	sort.Strings(denied)
	return nil, denied
}

func openAPIWorkspaceIntegrationEndpoint(spec map[string]interface{}, override, specURL string) (string, error) {
	if endpoint := strings.TrimSpace(override); endpoint != "" {
		return endpoint, nil
	}
	servers, _ := spec["servers"].([]interface{})
	if len(servers) == 0 {
		return "", fmt.Errorf("base_url is required when the OpenAPI spec has no servers")
	}
	server, _ := servers[0].(map[string]interface{})
	raw := stringFromMap(server, "url")
	if raw == "" {
		return "", fmt.Errorf("OpenAPI server url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid OpenAPI server url: %w", err)
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	if strings.TrimSpace(specURL) == "" {
		return "", fmt.Errorf("base_url is required when the OpenAPI server url is relative")
	}
	base, err := url.Parse(specURL)
	if err != nil {
		return "", fmt.Errorf("invalid spec URL: %w", err)
	}
	return base.ResolveReference(parsed).String(), nil
}

func openAPIWorkspaceIntegrationTools(spec map[string]interface{}, authSpec *workspaceIntegrationHTTPAuth) ([]workspaceIntegrationManifestTool, error) {
	paths := mapFromMap(spec, "paths")
	if len(paths) == 0 {
		return nil, fmt.Errorf("OpenAPI paths are required")
	}
	var tools []workspaceIntegrationManifestTool
	usedNames := map[string]int{}
	pathKeys := sortedMapKeys(paths)
	for _, pathName := range pathKeys {
		pathItem, ok := paths[pathName].(map[string]interface{})
		if !ok {
			continue
		}
		pathParameters, err := openAPIParameters(spec, pathItem["parameters"])
		if err != nil {
			return nil, fmt.Errorf("%s parameters: %w", pathName, err)
		}
		for _, method := range []string{"get", "head", "options", "post", "put", "patch", "delete"} {
			rawOperation, ok := pathItem[method]
			if !ok {
				continue
			}
			operation, ok := rawOperation.(map[string]interface{})
			if !ok {
				continue
			}
			name := workspaceIntegrationImportToolName(stringFromMap(operation, "operationId"))
			if name == "" {
				name = workspaceIntegrationImportToolName(method + "_" + pathName)
			}
			name = uniqueWorkspaceIntegrationImportToolName(name, usedNames)
			parameters := append([]map[string]interface{}{}, pathParameters...)
			operationParameters, err := openAPIParameters(spec, operation["parameters"])
			if err != nil {
				return nil, fmt.Errorf("%s %s parameters: %w", strings.ToUpper(method), pathName, err)
			}
			parameters = append(parameters, operationParameters...)
			schema, request, err := openAPIOperationManifest(spec, strings.ToUpper(method), pathName, operation, parameters, authSpec)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), pathName, err)
			}
			tools = append(tools, workspaceIntegrationManifestTool{
				Name:        name,
				Description: firstNonEmpty(stringFromMap(operation, "summary"), stringFromMap(operation, "description"), strings.ToUpper(method)+" "+pathName),
				Parameters:  mustJSONRawMessage(schema),
				Access:      openAPIAccessForMethod(method),
				Source:      "openapi",
				Request:     request,
			})
		}
	}
	return tools, nil
}

func openAPIParameters(root map[string]interface{}, raw interface{}) ([]map[string]interface{}, error) {
	values, ok := raw.([]interface{})
	if !ok || len(values) == 0 {
		return nil, nil
	}
	out := make([]map[string]interface{}, 0, len(values))
	for _, value := range values {
		resolved, err := resolveOpenAPILocalRef(root, value, 0)
		if err != nil {
			return nil, err
		}
		param, ok := resolved.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("parameter must be an object")
		}
		out = append(out, param)
	}
	return out, nil
}

func openAPIOperationManifest(root map[string]interface{}, method, pathName string, operation map[string]interface{}, parameters []map[string]interface{}, authSpec *workspaceIntegrationHTTPAuth) (map[string]interface{}, *workspaceIntegrationHTTPRequest, error) {
	properties := map[string]interface{}{}
	var required []string
	pathParams := map[string]workspaceIntegrationHTTPValue{}
	queryParams := map[string]workspaceIntegrationHTTPValue{}
	headerParams := map[string]workspaceIntegrationHTTPValue{}
	for _, param := range parameters {
		name := strings.TrimSpace(stringFromMap(param, "name"))
		location := strings.ToLower(strings.TrimSpace(stringFromMap(param, "in")))
		if name == "" || location == "" {
			return nil, nil, fmt.Errorf("parameter name and in are required")
		}
		if location == "cookie" {
			return nil, nil, fmt.Errorf("cookie parameter %q is not supported", name)
		}
		schema, err := openAPIJSONSchema(root, param["schema"])
		if err != nil {
			return nil, nil, fmt.Errorf("parameter %q schema: %w", name, err)
		}
		if description := stringFromMap(param, "description"); description != "" {
			schema["description"] = description
		}
		properties[name] = schema
		valueSpec := workspaceIntegrationHTTPValue{Source: "arg", Name: name, OmitEmpty: location != "path" && !boolFromMap(param, "required")}
		openAPIApplyPrimitiveBounds(schema, &valueSpec)
		switch location {
		case "path":
			required = append(required, name)
			pathParams[name] = valueSpec
		case "query":
			if boolFromMap(param, "required") {
				required = append(required, name)
			}
			queryParams[name] = valueSpec
		case "header":
			if boolFromMap(param, "required") {
				required = append(required, name)
			}
			headerParams[name] = valueSpec
		default:
			return nil, nil, fmt.Errorf("parameter location %q is not supported", location)
		}
	}
	var body *workspaceIntegrationHTTPBody
	if rawBody, ok := operation["requestBody"]; ok {
		resolved, err := resolveOpenAPILocalRef(root, rawBody, 0)
		if err != nil {
			return nil, nil, err
		}
		requestBody, ok := resolved.(map[string]interface{})
		if !ok {
			return nil, nil, fmt.Errorf("requestBody must be an object")
		}
		bodySchema, err := openAPIRequestBodySchema(root, requestBody)
		if err != nil {
			return nil, nil, err
		}
		properties["body"] = bodySchema
		bodyRequired := boolFromMap(requestBody, "required")
		if bodyRequired {
			required = append(required, "body")
		}
		body = &workspaceIntegrationHTTPBody{Encoding: "json_arg", Name: "body", Required: bodyRequired}
	}
	sort.Strings(required)
	inputSchema := map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		inputSchema["required"] = required
	}
	request := &workspaceIntegrationHTTPRequest{
		Method:       method,
		Path:         pathName,
		HeaderParams: headerParams,
		PathParams:   pathParams,
		Query:        queryParams,
		Body:         body,
		Auth:         authSpec,
	}
	return inputSchema, request, nil
}

func openAPIRequestBodySchema(root map[string]interface{}, requestBody map[string]interface{}) (map[string]interface{}, error) {
	content := mapFromMap(requestBody, "content")
	if len(content) == 0 {
		return nil, fmt.Errorf("requestBody content is required")
	}
	var media map[string]interface{}
	if candidate := mapFromMap(content, "application/json"); len(candidate) > 0 {
		media = candidate
	} else {
		for _, key := range sortedMapKeys(content) {
			if strings.HasSuffix(strings.ToLower(key), "+json") {
				media = mapFromMap(content, key)
				break
			}
		}
	}
	if media == nil {
		return nil, fmt.Errorf("only JSON request bodies are supported")
	}
	return openAPIJSONSchema(root, media["schema"])
}

func openAPIJSONSchema(root map[string]interface{}, raw interface{}) (map[string]interface{}, error) {
	if raw == nil {
		return map[string]interface{}{"type": "object", "additionalProperties": true}, nil
	}
	resolved, err := resolveOpenAPILocalRef(root, raw, 0)
	if err != nil {
		return nil, err
	}
	schema, ok := resolved.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("schema must be an object")
	}
	return sanitizeJSONSchema(schema, 0), nil
}

func resolveOpenAPILocalRef(root map[string]interface{}, value interface{}, depth int) (interface{}, error) {
	if depth > 20 {
		return nil, fmt.Errorf("local $ref depth exceeded")
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return value, nil
	}
	ref := strings.TrimSpace(stringFromMap(object, "$ref"))
	if ref == "" {
		return value, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("remote $ref %q is not supported", ref)
	}
	resolved, ok := readJSONPointer(root, strings.TrimPrefix(ref, "#"))
	if !ok {
		return nil, fmt.Errorf("local $ref %q not found", ref)
	}
	return resolveOpenAPILocalRef(root, resolved, depth+1)
}

func rejectOpenAPIRemoteRefs(value interface{}) error {
	switch typed := value.(type) {
	case map[string]interface{}:
		if ref := strings.TrimSpace(stringFromMap(typed, "$ref")); ref != "" && !strings.HasPrefix(ref, "#") {
			return fmt.Errorf("remote $ref %q is not supported", ref)
		}
		for _, key := range sortedMapKeys(typed) {
			if err := rejectOpenAPIRemoteRefs(typed[key]); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, item := range typed {
			if err := rejectOpenAPIRemoteRefs(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func readJSONPointer(root interface{}, pointer string) (interface{}, bool) {
	if pointer == "" || pointer == "/" {
		return root, true
	}
	current := root
	for _, rawPart := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func sanitizeJSONSchema(schema map[string]interface{}, depth int) map[string]interface{} {
	if depth > 8 {
		return map[string]interface{}{"type": "object", "additionalProperties": true}
	}
	out := map[string]interface{}{}
	for _, key := range []string{"type", "description", "enum", "format", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "additionalProperties"} {
		if value, ok := schema[key]; ok {
			out[key] = value
		}
	}
	if properties := mapFromMap(schema, "properties"); len(properties) > 0 {
		next := map[string]interface{}{}
		for _, key := range sortedMapKeys(properties) {
			child, _ := properties[key].(map[string]interface{})
			if child == nil {
				next[key] = map[string]interface{}{"type": "string"}
				continue
			}
			next[key] = sanitizeJSONSchema(child, depth+1)
		}
		out["properties"] = next
	}
	if required, ok := schema["required"].([]interface{}); ok {
		var values []string
		for _, value := range required {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				values = append(values, text)
			}
		}
		if len(values) > 0 {
			sort.Strings(values)
			out["required"] = values
		}
	}
	if items, ok := schema["items"].(map[string]interface{}); ok {
		out["items"] = sanitizeJSONSchema(items, depth+1)
	}
	if len(out) == 0 {
		out["type"] = "string"
	}
	return out
}

func openAPIApplyPrimitiveBounds(schema map[string]interface{}, value *workspaceIntegrationHTTPValue) {
	if enumValues, ok := schema["enum"].([]interface{}); ok {
		for _, raw := range enumValues {
			value.Enum = append(value.Enum, fmt.Sprint(raw))
		}
	}
	if min, ok := intFromSchema(schema, "minimum"); ok {
		value.Min = &min
	}
	if max, ok := intFromSchema(schema, "maximum"); ok {
		value.Max = &max
	}
}

func intFromSchema(schema map[string]interface{}, key string) (int, bool) {
	switch value := schema[key].(type) {
	case int:
		return value, true
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func openAPIAccessForMethod(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return "read"
	default:
		return "write"
	}
}

func graphQLWorkspaceIntegrationTools(specBytes []byte, authSpec *workspaceIntegrationHTTPAuth) ([]workspaceIntegrationManifestTool, error) {
	trimmed := bytes.TrimSpace(specBytes)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("GraphQL schema is required")
	}
	if trimmed[0] == '{' {
		return graphQLWorkspaceIntegrationToolsFromIntrospection(trimmed, authSpec)
	}
	return graphQLWorkspaceIntegrationToolsFromSDL(string(specBytes), authSpec)
}

type graphQLField struct {
	Name       string
	Arguments  []graphQLArgument
	ReturnType string
	Mutation   bool
}

type graphQLArgument struct {
	Name string
	Type string
}

func graphQLWorkspaceIntegrationToolsFromSDL(schema string, authSpec *workspaceIntegrationHTTPAuth) ([]workspaceIntegrationManifestTool, error) {
	inputs, err := parseGraphQLInputObjects(schema)
	if err != nil {
		return nil, err
	}
	queryType, mutationType, err := parseGraphQLRootOperationTypes(schema)
	if err != nil {
		return nil, err
	}
	var fields []graphQLField
	if queryType != "" {
		queryFields, err := parseGraphQLObjectFields(schema, queryType, false)
		if err != nil {
			return nil, err
		}
		fields = append(fields, queryFields...)
	}
	if mutationType != "" {
		mutationFields, err := parseGraphQLObjectFields(schema, mutationType, true)
		if err != nil {
			return nil, err
		}
		fields = append(fields, mutationFields...)
	}
	if err := validateGraphQLImportFields(fields); err != nil {
		return nil, err
	}
	return graphQLFieldsToTools(fields, inputs, authSpec), nil
}

func parseGraphQLRootOperationTypes(schema string) (queryType, mutationType string, err error) {
	clean := stripGraphQLComments(schema)
	pattern := regexp.MustCompile(`(?s)\bschema\b[^{]*\{(.*?)\}`)
	match := pattern.FindStringSubmatch(clean)
	if len(match) != 2 {
		return "Query", "Mutation", nil
	}
	if err := validateGraphQLBlockDefinitionNames(match[1], "schema"); err != nil {
		return "", "", err
	}
	for _, line := range splitGraphQLFieldLines(match[1]) {
		name, typ, ok := parseGraphQLNameType(line)
		if !ok {
			continue
		}
		typ = graphQLRootTypeName(typ)
		switch name {
		case "query":
			queryType = typ
		case "mutation":
			mutationType = typ
		}
	}
	return queryType, mutationType, nil
}

func graphQLRootTypeName(rawType string) string {
	fields := strings.Fields(strings.TrimSpace(rawType))
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "[]!")
}

func validGraphQLName(name string) bool {
	return regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`).MatchString(strings.TrimSpace(name))
}

func validateGraphQLImportFields(fields []graphQLField) error {
	for _, field := range fields {
		if !validGraphQLName(field.Name) {
			return fmt.Errorf("invalid GraphQL field name %q", field.Name)
		}
		for _, arg := range field.Arguments {
			if !validGraphQLName(arg.Name) {
				return fmt.Errorf("invalid GraphQL argument name %q on field %q", arg.Name, field.Name)
			}
		}
	}
	return nil
}

func parseGraphQLInputObjects(schema string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, match := range graphqlBlockRegexp("input").FindAllStringSubmatch(stripGraphQLComments(schema), -1) {
		name := strings.TrimSpace(match[1])
		body := match[2]
		if err := validateGraphQLBlockDefinitionNames(body, "input"); err != nil {
			return nil, err
		}
		fields := map[string]string{}
		for _, line := range splitGraphQLFieldLines(body) {
			fieldName, fieldType, ok := parseGraphQLNameType(line)
			if ok {
				fields[fieldName] = fieldType
			}
		}
		if len(fields) > 0 {
			out[name] = fields
		}
	}
	return out, nil
}

func parseGraphQLObjectFields(schema, typeName string, mutation bool) ([]graphQLField, error) {
	clean := stripGraphQLComments(schema)
	pattern := regexp.MustCompile(`(?s)\btype\s+` + regexp.QuoteMeta(typeName) + `\b[^{]*\{(.*?)\}`)
	match := pattern.FindStringSubmatch(clean)
	if len(match) != 2 {
		return nil, nil
	}
	if err := validateGraphQLBlockDefinitionNames(match[1], "field"); err != nil {
		return nil, err
	}
	var fields []graphQLField
	for _, line := range splitGraphQLFieldLines(match[1]) {
		field, ok := parseGraphQLFieldLine(line, mutation)
		if ok {
			fields = append(fields, field)
		}
	}
	return fields, nil
}

func parseGraphQLFieldLine(line string, mutation bool) (graphQLField, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return graphQLField{}, false
	}
	nameEnd := strings.IndexAny(line, "(:")
	if nameEnd <= 0 {
		return graphQLField{}, false
	}
	name := strings.TrimSpace(line[:nameEnd])
	if !validGraphQLName(name) {
		return graphQLField{}, false
	}
	rest := strings.TrimSpace(line[nameEnd:])
	var args []graphQLArgument
	if strings.HasPrefix(rest, "(") {
		end := matchingParenIndex(rest)
		if end < 0 {
			return graphQLField{}, false
		}
		args = parseGraphQLArguments(rest[1:end])
		rest = strings.TrimSpace(rest[end+1:])
	}
	if !strings.HasPrefix(rest, ":") {
		return graphQLField{}, false
	}
	returnType := strings.TrimSpace(strings.TrimPrefix(rest, ":"))
	if cut := strings.IndexAny(returnType, " \t{"); cut >= 0 {
		returnType = strings.TrimSpace(returnType[:cut])
	}
	return graphQLField{Name: name, Arguments: args, ReturnType: returnType, Mutation: mutation}, true
}

func parseGraphQLArguments(raw string) []graphQLArgument {
	var args []graphQLArgument
	for _, part := range splitTopLevel(raw, ',') {
		name, typ, ok := parseGraphQLNameType(part)
		if ok {
			args = append(args, graphQLArgument{Name: name, Type: typ})
		}
	}
	return args
}

func parseGraphQLNameType(line string) (string, string, bool) {
	name, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	if cut := strings.IndexAny(name, " \t("); cut >= 0 {
		name = strings.TrimSpace(name[:cut])
	}
	typ := strings.TrimSpace(rest)
	if cut := strings.Index(typ, "="); cut >= 0 {
		typ = strings.TrimSpace(typ[:cut])
	}
	if name == "" || typ == "" {
		return "", "", false
	}
	if !validGraphQLName(name) {
		return "", "", false
	}
	return name, typ, true
}

func graphQLFieldsToTools(fields []graphQLField, inputs map[string]map[string]string, authSpec *workspaceIntegrationHTTPAuth) []workspaceIntegrationManifestTool {
	usedNames := map[string]int{}
	var tools []workspaceIntegrationManifestTool
	for _, field := range fields {
		name := uniqueWorkspaceIntegrationImportToolName(workspaceIntegrationImportToolName(field.Name), usedNames)
		schema := graphQLInputSchema(field.Arguments, inputs)
		query := graphQLQueryTemplate(field)
		access := "read"
		if field.Mutation {
			access = "write"
		}
		tools = append(tools, workspaceIntegrationManifestTool{
			Name:        name,
			Description: graphQLToolDescription(field),
			Parameters:  mustJSONRawMessage(schema),
			Access:      access,
			Source:      "graphql",
			Request: &workspaceIntegrationHTTPRequest{
				Method: http.MethodPost,
				Headers: map[string]string{
					"Accept": "application/json",
				},
				Body: &workspaceIntegrationHTTPBody{
					Encoding: "graphql",
					Query:    query,
				},
				Auth: authSpec,
			},
		})
	}
	return tools
}

func graphQLInputSchema(args []graphQLArgument, inputs map[string]map[string]string) map[string]interface{} {
	properties := map[string]interface{}{}
	var required []string
	for _, arg := range args {
		properties[arg.Name] = graphQLTypeSchema(arg.Type, inputs, 0)
		if graphQLTypeRequired(arg.Type) {
			required = append(required, arg.Name)
		}
	}
	sort.Strings(required)
	schema := map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func graphQLTypeSchema(rawType string, inputs map[string]map[string]string, depth int) map[string]interface{} {
	if depth > 6 {
		return map[string]interface{}{"type": "object", "additionalProperties": true}
	}
	typ := strings.TrimSpace(strings.TrimSuffix(rawType, "!"))
	if strings.HasPrefix(typ, "[") && strings.HasSuffix(typ, "]") {
		return map[string]interface{}{
			"type":  "array",
			"items": graphQLTypeSchema(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(typ, "["), "]")), inputs, depth+1),
		}
	}
	switch strings.TrimSuffix(typ, "!") {
	case "String", "ID":
		return map[string]interface{}{"type": "string"}
	case "Int":
		return map[string]interface{}{"type": "integer"}
	case "Float":
		return map[string]interface{}{"type": "number"}
	case "Boolean":
		return map[string]interface{}{"type": "boolean"}
	default:
		if fields, ok := inputs[typ]; ok {
			properties := map[string]interface{}{}
			var required []string
			for _, key := range sortedStringMapKeys(fields) {
				fieldType := fields[key]
				properties[key] = graphQLTypeSchema(fieldType, inputs, depth+1)
				if graphQLTypeRequired(fieldType) {
					required = append(required, key)
				}
			}
			schema := map[string]interface{}{"type": "object", "properties": properties, "additionalProperties": false}
			if len(required) > 0 {
				schema["required"] = required
			}
			return schema
		}
		return map[string]interface{}{"type": "string"}
	}
}

func graphQLTypeRequired(rawType string) bool {
	return strings.HasSuffix(strings.TrimSpace(rawType), "!")
}

func graphQLQueryTemplate(field graphQLField) string {
	operation := "query"
	if field.Mutation {
		operation = "mutation"
	}
	var defs []string
	var args []string
	for _, arg := range field.Arguments {
		defs = append(defs, "$"+arg.Name+": "+arg.Type)
		args = append(args, arg.Name+": $"+arg.Name)
	}
	selection := ""
	if !graphQLReturnTypeScalar(field.ReturnType) {
		selection = " { __typename }"
	}
	if len(defs) == 0 {
		return fmt.Sprintf("%s %s { %s%s }", operation, workspaceIntegrationGraphQLOperationName(field.Name), field.Name, selection)
	}
	return fmt.Sprintf("%s %s(%s) { %s(%s)%s }", operation, workspaceIntegrationGraphQLOperationName(field.Name), strings.Join(defs, ", "), field.Name, strings.Join(args, ", "), selection)
}

func workspaceIntegrationGraphQLOperationName(name string) string {
	clean := workspaceIntegrationImportToolName(name)
	if clean == "" {
		return "WorkspaceIntegrationOperation"
	}
	parts := strings.Split(clean, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func graphQLReturnTypeScalar(rawType string) bool {
	typ := strings.Trim(rawType, "[]!")
	switch typ {
	case "String", "ID", "Int", "Float", "Boolean":
		return true
	default:
		return false
	}
}

func graphQLToolDescription(field graphQLField) string {
	if field.Mutation {
		return "Run GraphQL mutation " + field.Name + "."
	}
	return "Run GraphQL query " + field.Name + "."
}

func graphQLWorkspaceIntegrationToolsFromIntrospection(data []byte, authSpec *workspaceIntegrationHTTPAuth) ([]workspaceIntegrationManifestTool, error) {
	var payload struct {
		Data struct {
			Schema struct {
				QueryType *struct {
					Name string `json:"name"`
				} `json:"queryType"`
				MutationType *struct {
					Name string `json:"name"`
				} `json:"mutationType"`
				Types []struct {
					Kind   string `json:"kind"`
					Name   string `json:"name"`
					Fields []struct {
						Name string `json:"name"`
						Args []struct {
							Name string      `json:"name"`
							Type interface{} `json:"type"`
						} `json:"args"`
						Type interface{} `json:"type"`
					} `json:"fields"`
					InputFields []struct {
						Name string      `json:"name"`
						Type interface{} `json:"type"`
					} `json:"inputFields"`
				} `json:"types"`
			} `json:"__schema"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode GraphQL introspection: %w", err)
	}
	inputs := map[string]map[string]string{}
	var fields []graphQLField
	queryType := "Query"
	if payload.Data.Schema.QueryType != nil {
		queryType = strings.TrimSpace(payload.Data.Schema.QueryType.Name)
	}
	mutationType := "Mutation"
	if payload.Data.Schema.MutationType != nil {
		mutationType = strings.TrimSpace(payload.Data.Schema.MutationType.Name)
	} else if payload.Data.Schema.QueryType != nil {
		mutationType = ""
	}
	for _, typ := range payload.Data.Schema.Types {
		switch {
		case typ.Name == queryType && queryType != "":
			for _, field := range typ.Fields {
				var args []graphQLArgument
				for _, arg := range field.Args {
					args = append(args, graphQLArgument{Name: arg.Name, Type: graphQLTypeString(arg.Type)})
				}
				fields = append(fields, graphQLField{
					Name:       field.Name,
					Arguments:  args,
					ReturnType: graphQLTypeString(field.Type),
					Mutation:   false,
				})
			}
		case typ.Name == mutationType && mutationType != "":
			for _, field := range typ.Fields {
				var args []graphQLArgument
				for _, arg := range field.Args {
					args = append(args, graphQLArgument{Name: arg.Name, Type: graphQLTypeString(arg.Type)})
				}
				fields = append(fields, graphQLField{
					Name:       field.Name,
					Arguments:  args,
					ReturnType: graphQLTypeString(field.Type),
					Mutation:   true,
				})
			}
		}
		if typ.Name != queryType && typ.Name != mutationType {
			if typ.Kind == "INPUT_OBJECT" && len(typ.InputFields) > 0 {
				inputFields := map[string]string{}
				for _, field := range typ.InputFields {
					inputFields[field.Name] = graphQLTypeString(field.Type)
				}
				inputs[typ.Name] = inputFields
			}
		}
	}
	if err := validateGraphQLImportFields(fields); err != nil {
		return nil, err
	}
	return graphQLFieldsToTools(fields, inputs, authSpec), nil
}

func graphQLTypeString(raw interface{}) string {
	object, ok := raw.(map[string]interface{})
	if !ok {
		return "String"
	}
	kind := strings.TrimSpace(fmt.Sprint(object["kind"]))
	name := strings.TrimSpace(fmt.Sprint(object["name"]))
	ofType := object["ofType"]
	switch kind {
	case "NON_NULL":
		return strings.TrimSuffix(graphQLTypeString(ofType), "!") + "!"
	case "LIST":
		return "[" + strings.TrimSuffix(graphQLTypeString(ofType), "!") + "]"
	default:
		if name == "" {
			return "String"
		}
		return name
	}
}

func stripGraphQLComments(schema string) string {
	var lines []string
	for _, line := range strings.Split(schema, "\n") {
		if index := strings.Index(line, "#"); index >= 0 {
			line = line[:index]
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func graphqlBlockRegexp(keyword string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)\b` + keyword + `\s+([A-Za-z_][A-Za-z0-9_]*)\b[^{]*\{(.*?)\}`)
}

func validateGraphQLBlockDefinitionNames(body, kind string) error {
	depth := 0
	for i := 0; i < len(body); {
		switch body[i] {
		case '"':
			i = skipGraphQLString(body, i)
			continue
		case '(', '[', '{':
			depth++
			i++
			continue
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			i++
			continue
		}
		if depth != 0 || !isGraphQLDefinitionTokenByte(body[i]) || (i > 0 && isGraphQLDefinitionTokenByte(body[i-1])) {
			i++
			continue
		}
		nameStart := i
		for i < len(body) && isGraphQLDefinitionTokenByte(body[i]) {
			i++
		}
		name := strings.TrimSpace(body[nameStart:i])
		index := skipGraphQLWhitespace(body, i)
		if index < len(body) && body[index] == '(' {
			end := matchingParenIndex(body[index:])
			if end < 0 {
				return fmt.Errorf("invalid GraphQL field arguments on %q", name)
			}
			afterArgs := skipGraphQLWhitespace(body, index+end+1)
			if afterArgs < len(body) && body[afterArgs] == ':' {
				if !validGraphQLName(name) {
					return graphQLInvalidDefinitionNameError(kind, name)
				}
				if err := validateGraphQLArgumentNames(body[index+1:index+end], name); err != nil {
					return err
				}
			}
			continue
		}
		if index < len(body) && body[index] == ':' && !validGraphQLName(name) {
			return graphQLInvalidDefinitionNameError(kind, name)
		}
	}
	return nil
}

func validateGraphQLArgumentNames(raw, fieldName string) error {
	depth := 0
	for i := 0; i < len(raw); {
		switch raw[i] {
		case '"':
			i = skipGraphQLString(raw, i)
			continue
		case '(', '[', '{':
			depth++
			i++
			continue
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			i++
			continue
		}
		if depth != 0 || !isGraphQLDefinitionTokenByte(raw[i]) || (i > 0 && isGraphQLDefinitionTokenByte(raw[i-1])) {
			i++
			continue
		}
		nameStart := i
		for i < len(raw) && isGraphQLDefinitionTokenByte(raw[i]) {
			i++
		}
		name := strings.TrimSpace(raw[nameStart:i])
		index := skipGraphQLWhitespace(raw, i)
		if index < len(raw) && raw[index] == ':' && !validGraphQLName(name) {
			return fmt.Errorf("invalid GraphQL argument name %q on field %q", name, fieldName)
		}
	}
	return nil
}

func graphQLInvalidDefinitionNameError(kind, name string) error {
	switch kind {
	case "schema":
		return fmt.Errorf("invalid GraphQL schema operation name %q", name)
	case "input":
		return fmt.Errorf("invalid GraphQL input field name %q", name)
	default:
		return fmt.Errorf("invalid GraphQL field name %q", name)
	}
}

func splitGraphQLFieldLines(body string) []string {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	starts := graphQLDefinitionStartIndexes(body)
	var out []string
	for i, start := range starts {
		end := len(body)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		line := strings.Trim(strings.TrimSpace(body[start:end]), ",")
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func graphQLDefinitionStartIndexes(body string) []int {
	var starts []int
	depth := 0
	for i := 0; i < len(body); {
		switch body[i] {
		case '"':
			i = skipGraphQLString(body, i)
			continue
		case '(', '[', '{':
			depth++
			i++
			continue
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			i++
			continue
		}
		if depth != 0 || !isGraphQLNameStartByte(body[i]) || (i > 0 && isGraphQLNameContinueByte(body[i-1])) {
			i++
			continue
		}
		nameEnd := i + 1
		for nameEnd < len(body) && isGraphQLNameContinueByte(body[nameEnd]) {
			nameEnd++
		}
		if graphQLDefinitionNameHasColon(body, nameEnd) {
			starts = append(starts, i)
		}
		i = nameEnd
	}
	return starts
}

func graphQLDefinitionNameHasColon(body string, nameEnd int) bool {
	index := skipGraphQLWhitespace(body, nameEnd)
	if index < len(body) && body[index] == '(' {
		end := matchingParenIndex(body[index:])
		if end < 0 {
			return false
		}
		index = skipGraphQLWhitespace(body, index+end+1)
	}
	return index < len(body) && body[index] == ':'
}

func skipGraphQLWhitespace(body string, index int) int {
	for index < len(body) {
		switch body[index] {
		case ' ', '\t', '\n', '\r', ',':
			index++
		default:
			return index
		}
	}
	return index
}

func skipGraphQLString(body string, start int) int {
	if strings.HasPrefix(body[start:], `"""`) {
		end := strings.Index(body[start+3:], `"""`)
		if end < 0 {
			return len(body)
		}
		return start + 3 + end + 3
	}
	escaped := false
	for i := start + 1; i < len(body); i++ {
		switch {
		case escaped:
			escaped = false
		case body[i] == '\\':
			escaped = true
		case body[i] == '"':
			return i + 1
		}
	}
	return len(body)
}

func isGraphQLNameStartByte(char byte) bool {
	return char == '_' || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')
}

func isGraphQLNameContinueByte(char byte) bool {
	return isGraphQLNameStartByte(char) || (char >= '0' && char <= '9')
}

func isGraphQLDefinitionTokenByte(char byte) bool {
	switch char {
	case ' ', '\t', '\n', '\r', ',', ':', '(', ')', '[', ']', '{', '}', '@', '=':
		return false
	default:
		return true
	}
}

func splitTopLevel(raw string, sep rune) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range raw {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		default:
			if r == sep && depth == 0 {
				out = append(out, raw[start:i])
				start = i + len(string(r))
			}
		}
	}
	out = append(out, raw[start:])
	return out
}

func matchingParenIndex(raw string) int {
	depth := 0
	for i, r := range raw {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func workspaceIntegrationImportToolName(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	lastUnderscore := false
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastUnderscore = false
		case r == '_' || r == '-':
			if !lastUnderscore && b.Len() > 0 {
				b.WriteRune('_')
				lastUnderscore = true
			}
		case r == '/' || r == '{' || r == '}':
			if !lastUnderscore && b.Len() > 0 {
				b.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func uniqueWorkspaceIntegrationImportToolName(name string, used map[string]int) string {
	if name == "" {
		name = "operation"
	}
	count := used[name]
	used[name] = count + 1
	if count == 0 {
		return name
	}
	return fmt.Sprintf("%s_%d", name, count+1)
}

func mustJSONRawMessage(value interface{}) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func stringFromMap(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	if value, ok := values[key]; ok {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

func boolFromMap(values map[string]interface{}, key string) bool {
	if values == nil {
		return false
	}
	value, ok := values[key]
	if !ok {
		return false
	}
	typed, ok := value.(bool)
	return ok && typed
}

func mapFromMap(values map[string]interface{}, key string) map[string]interface{} {
	if values == nil {
		return nil
	}
	out, _ := values[key].(map[string]interface{})
	return out
}

func sortedMapKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
