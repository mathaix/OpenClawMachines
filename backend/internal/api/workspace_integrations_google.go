package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/crypto"
)

const (
	googleWorkspaceIntegrationSlug        = "google-workspace"
	workspaceIntegrationOAuthCallbackPath = "/api/workspace-integrations/oauth/callback"
)

var (
	googleOAuthAuthorizeURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleOAuthTokenURL      = "https://oauth2.googleapis.com/token"
	googleUserInfoURL        = "https://openidconnect.googleapis.com/v1/userinfo"
	googleGmailAPIBaseURL    = "https://gmail.googleapis.com/gmail/v1"
	googleDriveAPIBaseURL    = "https://www.googleapis.com/drive/v3"
	googleCalendarAPIBaseURL = "https://www.googleapis.com/calendar/v3"
	googleDocsAPIBaseURL     = "https://docs.googleapis.com/v1"
)

var googleWorkspaceScopes = googleWorkspaceDefaultScopes()

var googleWorkspaceBaseScopes = []string{
	"openid",
	"email",
	"profile",
}

var googleWorkspaceDefaultPermissionLevels = map[string]string{
	"gmail":    "read",
	"drive":    "read",
	"calendar": "read",
	"docs":     "off",
}

var googleWorkspaceScopesByPermissionLevel = map[string]map[string][]string{
	"gmail": {
		"off":        nil,
		"read":       {"https://www.googleapis.com/auth/gmail.readonly"},
		"read_write": {"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/gmail.send"},
	},
	"drive": {
		"off":        nil,
		"read":       {"https://www.googleapis.com/auth/drive.metadata.readonly"},
		"read_write": {"https://www.googleapis.com/auth/drive.metadata.readonly", "https://www.googleapis.com/auth/drive.file"},
	},
	"calendar": {
		"off":        nil,
		"read":       {"https://www.googleapis.com/auth/calendar.events.readonly"},
		"read_write": {"https://www.googleapis.com/auth/calendar.events"},
	},
	"docs": {
		"off":        nil,
		"read":       {"https://www.googleapis.com/auth/documents.readonly"},
		"read_write": {"https://www.googleapis.com/auth/drive.file"},
	},
}

type googleWorkspaceIntegrationConfig struct {
	Email                     string                                  `json:"email,omitempty"`
	Scopes                    []string                                `json:"scopes,omitempty"`
	PermissionLevels          map[string]string                       `json:"permission_levels,omitempty"`
	RequestedPermissionLevels map[string]string                       `json:"requested_permission_levels,omitempty"`
	ServiceStatus             map[string]googleWorkspaceServiceStatus `json:"service_status,omitempty"`
	AllowedDriveFolderIDs     []string                                `json:"allowed_drive_folder_ids,omitempty"`
	AllowedCalendarIDs        []string                                `json:"allowed_calendar_ids,omitempty"`
}

type googleWorkspaceServiceStatus struct {
	Service    string     `json:"service"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail,omitempty"`
	Action     string     `json:"action,omitempty"`
	HTTPStatus *int       `json:"http_status,omitempty"`
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
}

func googleWorkspaceIntegrationConnectionSlug(email string) string {
	slug := "google-" + strings.ReplaceAll(workspaceIntegrationAddressSegment(email, "account"), "_", "-")
	if len(slug) <= 63 {
		if isValidSlug(slug) {
			return slug
		}
		return googleWorkspaceIntegrationSlug
	}
	slug = strings.TrimRight(slug[:63], "-")
	if !isValidSlug(slug) {
		return googleWorkspaceIntegrationSlug
	}
	return slug
}

type workspaceIntegrationOAuthProvider struct {
	Slug         string
	DisplayName  string
	Kind         string
	Transport    string
	AuthorizeURL string
	TokenURL     string
	UserInfoURL  string
	Scopes       []string
	ToolManifest func(permissionLevels map[string]string) json.RawMessage
}

type workspaceIntegrationOAuthState struct {
	Provider         string               `json:"provider"`
	AccountID        int                  `json:"account_id"`
	WorkspaceID      string               `json:"workspace_id"`
	UserID           int                  `json:"user_id"`
	ReturnPath       string               `json:"return_path"`
	Scopes           []string             `json:"scopes,omitempty"`
	PermissionLevels map[string]string    `json:"permission_levels,omitempty"`
	CustomMCP        *customMCPOAuthState `json:"custom_mcp,omitempty"`
	ExpiresAt        int64                `json:"expires_at"`
	Nonce            string               `json:"nonce"`
}

type customMCPOAuthState struct {
	Endpoint             string   `json:"endpoint"`
	DisplayName          string   `json:"display_name,omitempty"`
	Resource             string   `json:"resource,omitempty"`
	AuthorizationServer  string   `json:"authorization_server,omitempty"`
	Issuer               string   `json:"issuer,omitempty"`
	TokenURL             string   `json:"token_url"`
	ClientID             string   `json:"client_id"`
	CodeVerifier         string   `json:"code_verifier"`
	Scopes               []string `json:"scopes,omitempty"`
	ResourceMetadataURL  string   `json:"resource_metadata_url,omitempty"`
	RegistrationEndpoint string   `json:"registration_endpoint,omitempty"`
}

type workspaceIntegrationOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope,omitempty"`
}

type googleUserInfoResponse struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

type workspaceIntegrationOAuthStartRequest struct {
	Permissions map[string]string `json:"permissions,omitempty"`
	URL         string            `json:"url,omitempty"`
	DisplayName string            `json:"display_name,omitempty"`
	Scopes      []string          `json:"scopes,omitempty"`
	ClientID    string            `json:"client_id,omitempty"`
}

func googleWorkspaceOAuthProvider() workspaceIntegrationOAuthProvider {
	return workspaceIntegrationOAuthProvider{
		Slug:         googleWorkspaceIntegrationSlug,
		DisplayName:  "Google Workspace",
		Kind:         "google_workspace",
		Transport:    "http",
		AuthorizeURL: googleOAuthAuthorizeURL,
		TokenURL:     googleOAuthTokenURL,
		UserInfoURL:  googleUserInfoURL,
		Scopes:       googleWorkspaceScopes,
		ToolManifest: googleWorkspaceToolManifest,
	}
}

func workspaceIntegrationOAuthProviderForSlug(slug string) (workspaceIntegrationOAuthProvider, bool) {
	switch slug {
	case googleWorkspaceIntegrationSlug:
		return googleWorkspaceOAuthProvider(), true
	default:
		return workspaceIntegrationOAuthProvider{}, false
	}
}

func googleWorkspaceDefaultScopes() []string {
	scopes, err := googleWorkspaceScopesForPermissionLevels(nil)
	if err != nil {
		return append([]string(nil), googleWorkspaceBaseScopes...)
	}
	return scopes
}

func googleWorkspaceScopesForPermissionLevels(permissions map[string]string) ([]string, error) {
	levels := make(map[string]string, len(googleWorkspaceDefaultPermissionLevels))
	for service, level := range googleWorkspaceDefaultPermissionLevels {
		levels[service] = level
	}
	for service, level := range permissions {
		service = strings.ToLower(strings.TrimSpace(service))
		level = strings.ToLower(strings.TrimSpace(level))
		if _, ok := googleWorkspaceScopesByPermissionLevel[service]; !ok {
			return nil, fmt.Errorf("unsupported Google Workspace permission service %q", service)
		}
		if level == "" {
			level = "off"
		}
		if _, ok := googleWorkspaceScopesByPermissionLevel[service][level]; !ok {
			return nil, fmt.Errorf("unsupported Google Workspace permission level %q for %s", level, service)
		}
		levels[service] = level
	}

	seen := make(map[string]struct{}, len(googleWorkspaceBaseScopes)+len(levels)*2)
	scopes := make([]string, 0, len(googleWorkspaceBaseScopes)+len(levels)*2)
	appendScope := func(scope string) {
		if scope == "" {
			return
		}
		if _, ok := seen[scope]; ok {
			return
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	for _, scope := range googleWorkspaceBaseScopes {
		appendScope(scope)
	}
	for _, service := range []string{"gmail", "drive", "calendar", "docs"} {
		level := levels[service]
		for _, scope := range googleWorkspaceScopesByPermissionLevel[service][level] {
			appendScope(scope)
		}
	}
	return scopes, nil
}

func googleWorkspacePermissionLevels(permissions map[string]string) (map[string]string, error) {
	levels := make(map[string]string, len(googleWorkspaceDefaultPermissionLevels))
	for service, level := range googleWorkspaceDefaultPermissionLevels {
		levels[service] = level
	}
	for service, level := range permissions {
		service = strings.ToLower(strings.TrimSpace(service))
		level = strings.ToLower(strings.TrimSpace(level))
		if _, ok := googleWorkspaceScopesByPermissionLevel[service]; !ok {
			return nil, fmt.Errorf("unsupported Google Workspace permission service %q", service)
		}
		if level == "" {
			level = "off"
		}
		if _, ok := googleWorkspaceScopesByPermissionLevel[service][level]; !ok {
			return nil, fmt.Errorf("unsupported Google Workspace permission level %q for %s", level, service)
		}
		levels[service] = level
	}
	return levels, nil
}

func workspaceIntegrationOAuthScopes(provider workspaceIntegrationOAuthProvider, permissions map[string]string) ([]string, map[string]string, error) {
	if provider.Slug != googleWorkspaceIntegrationSlug {
		if len(permissions) > 0 {
			return nil, nil, fmt.Errorf("%s does not support configurable permissions", provider.DisplayName)
		}
		return append([]string(nil), provider.Scopes...), nil, nil
	}
	levels, err := googleWorkspacePermissionLevels(permissions)
	if err != nil {
		return nil, nil, err
	}
	scopes, err := googleWorkspaceScopesForPermissionLevels(levels)
	if err != nil {
		return nil, nil, err
	}
	return scopes, levels, nil
}

// googleWorkspaceToolManifest emits only the tools each effective permission
// level supports. Off -> no tools for that service; read -> read tools;
// read_write -> adds the write tool(s).
func googleWorkspaceToolManifest(permissionLevels map[string]string) json.RawMessage {
	levels, err := googleWorkspacePermissionLevels(permissionLevels)
	if err != nil {
		levels, _ = googleWorkspacePermissionLevels(nil)
	}
	auth := &workspaceIntegrationHTTPAuth{Type: "oauth", TokenURL: googleOAuthTokenURL, Required: true}
	tools := make([]workspaceIntegrationManifestTool, 0, 8)

	switch levels["gmail"] {
	case "read":
		tools = append(tools, gmailProfileTool(auth), gmailListMessagesTool(auth), gmailGetMessageTool(auth))
	case "read_write":
		tools = append(tools, gmailProfileTool(auth), gmailListMessagesTool(auth), gmailGetMessageTool(auth), gmailSendMessageTool(auth))
	}

	switch levels["drive"] {
	case "read":
		tools = append(tools, driveListFilesTool(auth))
	case "read_write":
		tools = append(tools, driveListFilesTool(auth), driveCreateFileTool(auth))
	}

	switch levels["calendar"] {
	case "read":
		tools = append(tools, calendarListEventsTool(auth))
	case "read_write":
		tools = append(tools, calendarListEventsTool(auth), calendarCreateEventTool(auth))
	}

	switch levels["docs"] {
	case "read":
		tools = append(tools, docsGetDocumentTool(auth))
	case "read_write":
		tools = append(tools, docsGetDocumentTool(auth), docsCreateDocumentTool(auth))
	}

	return mustWorkspaceIntegrationToolManifest(tools)
}

func googleWorkspaceToolManifestForServiceStatus(permissionLevels map[string]string, serviceStatus map[string]googleWorkspaceServiceStatus) json.RawMessage {
	levels, err := googleWorkspacePermissionLevels(permissionLevels)
	if err != nil {
		levels, _ = googleWorkspacePermissionLevels(nil)
	}
	for service, status := range serviceStatus {
		if googleWorkspaceServiceStatusBlocksTools(status.Status) {
			levels[service] = "off"
		}
	}
	return googleWorkspaceToolManifest(levels)
}

func gmailProfileTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "gmail_profile",
		Description: "Get the connected Gmail profile summary",
		Source:      "gmail",
		Parameters:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			URL:    strings.TrimRight(googleGmailAPIBaseURL, "/") + "/users/me/profile",
			Auth:   auth,
		},
	}
}

func gmailListMessagesTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "gmail_list_messages",
		Description: "List Gmail message IDs, optionally filtered by a Gmail search query",
		Source:      "gmail",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": { "type": "string" },
				"max_results": { "type": "integer", "minimum": 1, "maximum": 25 }
			},
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			URL:    strings.TrimRight(googleGmailAPIBaseURL, "/") + "/users/me/messages",
			Query: map[string]workspaceIntegrationHTTPValue{
				"q":          {Source: "arg", Name: "query", OmitEmpty: true},
				"maxResults": {Source: "arg", Name: "max_results", Default: 10, Min: workspaceIntegrationIntPtr(1), Max: workspaceIntegrationIntPtr(25)},
			},
			Auth: auth,
		},
	}
}

func gmailGetMessageTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "gmail_get_message",
		Description: "Fetch a single Gmail message by ID",
		Source:      "gmail",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"message_id": { "type": "string" },
				"format": { "type": "string", "enum": ["full", "metadata", "minimal"] }
			},
			"required": ["message_id"],
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			URL:    strings.TrimRight(googleGmailAPIBaseURL, "/") + "/users/me/messages/{message_id}",
			PathParams: map[string]workspaceIntegrationHTTPValue{
				"message_id": {Source: "arg", Name: "message_id"},
			},
			Query: map[string]workspaceIntegrationHTTPValue{
				"format": {Source: "arg", Name: "format", Default: "full", Enum: []string{"full", "metadata", "minimal"}},
			},
			Auth: auth,
		},
	}
}

func gmailSendMessageTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "gmail_send_message",
		Description: "Send an email from the connected Gmail account",
		Source:      "gmail",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"to": { "type": "string" },
				"subject": { "type": "string" },
				"body": { "type": "string" }
			},
			"required": ["to"],
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodPost,
			URL:    strings.TrimRight(googleGmailAPIBaseURL, "/") + "/users/me/messages/send",
			Body:   &workspaceIntegrationHTTPBody{Encoding: "gmail_raw"},
			Auth:   auth,
		},
	}
}

func driveListFilesTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "drive_list_files",
		Description: "List metadata for files visible in Google Drive",
		Source:      "google-drive",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"folder_id": { "type": "string" },
				"page_size": { "type": "integer", "minimum": 1, "maximum": 20 }
			},
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			URL:    strings.TrimRight(googleDriveAPIBaseURL, "/") + "/files",
			Query: map[string]workspaceIntegrationHTTPValue{
				"pageSize": {Source: "arg", Name: "page_size", Default: 10, Min: workspaceIntegrationIntPtr(1), Max: workspaceIntegrationIntPtr(20)},
				"fields":   {Value: "files(id,name,mimeType,webViewLink,modifiedTime),nextPageToken"},
				"q": {
					Source:      "arg",
					Name:        "folder_id",
					Default:     "root",
					Allow:       []string{"root"},
					AllowConfig: "allowed_drive_folder_ids",
					Template:    "'{value}' in parents and trashed = false",
				},
			},
			Auth: auth,
		},
	}
}

func driveCreateFileTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "drive_create_file",
		Description: "Create a Google Drive file (metadata only) in an allowed folder",
		Source:      "google-drive",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": { "type": "string" },
				"mime_type": { "type": "string" },
				"folder_id": { "type": "string" }
			},
			"required": ["name"],
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodPost,
			URL:    strings.TrimRight(googleDriveAPIBaseURL, "/") + "/files",
			Body: &workspaceIntegrationHTTPBody{
				Fields: []workspaceIntegrationHTTPBodyField{
					{Path: "name", Required: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "name"}},
					{Path: "mimeType", Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "mime_type", Default: "application/vnd.google-apps.document"}},
					{Path: "parents", Array: true, Value: workspaceIntegrationHTTPValue{
						Source:      "arg",
						Name:        "folder_id",
						Default:     "root",
						Allow:       []string{"root"},
						AllowConfig: "allowed_drive_folder_ids",
					}},
				},
			},
			Auth: auth,
		},
	}
}

func calendarListEventsTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "calendar_list_events",
		Description: "List upcoming Google Calendar events",
		Source:      "google-calendar",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"calendar_id": { "type": "string" },
				"max_results": { "type": "integer", "minimum": 1, "maximum": 20 }
			},
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			URL:    strings.TrimRight(googleCalendarAPIBaseURL, "/") + "/calendars/{calendar_id}/events",
			PathParams: map[string]workspaceIntegrationHTTPValue{
				"calendar_id": {
					Source:      "arg",
					Name:        "calendar_id",
					Default:     "primary",
					Allow:       []string{"primary"},
					AllowConfig: "allowed_calendar_ids",
				},
			},
			Query: map[string]workspaceIntegrationHTTPValue{
				"singleEvents": {Value: "true"},
				"orderBy":      {Value: "startTime"},
				"timeMin":      {Source: "now", Format: "rfc3339"},
				"maxResults":   {Source: "arg", Name: "max_results", Default: 10, Min: workspaceIntegrationIntPtr(1), Max: workspaceIntegrationIntPtr(20)},
			},
			Auth: auth,
		},
	}
}

func calendarCreateEventTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "calendar_create_event",
		Description: "Create a Google Calendar event in an allowed calendar",
		Source:      "google-calendar",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"summary": { "type": "string" },
				"description": { "type": "string" },
				"start": { "type": "string", "description": "RFC3339 start, e.g. 2026-07-01T10:00:00-07:00" },
				"end": { "type": "string", "description": "RFC3339 end" },
				"calendar_id": { "type": "string" }
			},
			"required": ["summary", "start", "end"],
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodPost,
			URL:    strings.TrimRight(googleCalendarAPIBaseURL, "/") + "/calendars/{calendar_id}/events",
			PathParams: map[string]workspaceIntegrationHTTPValue{
				"calendar_id": {
					Source:      "arg",
					Name:        "calendar_id",
					Default:     "primary",
					Allow:       []string{"primary"},
					AllowConfig: "allowed_calendar_ids",
				},
			},
			Body: &workspaceIntegrationHTTPBody{
				Fields: []workspaceIntegrationHTTPBodyField{
					{Path: "summary", Required: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "summary"}},
					{Path: "description", Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "description", OmitEmpty: true}},
					{Path: "start.dateTime", Required: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "start"}},
					{Path: "end.dateTime", Required: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "end"}},
				},
			},
			Auth: auth,
		},
	}
}

func docsGetDocumentTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "docs_get_document",
		Description: "Fetch a Google Docs document by ID",
		Source:      "google-docs",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"document_id": { "type": "string" }
			},
			"required": ["document_id"],
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodGet,
			URL:    strings.TrimRight(googleDocsAPIBaseURL, "/") + "/documents/{document_id}",
			PathParams: map[string]workspaceIntegrationHTTPValue{
				"document_id": {Source: "arg", Name: "document_id"},
			},
			Auth: auth,
		},
	}
}

func docsCreateDocumentTool(auth *workspaceIntegrationHTTPAuth) workspaceIntegrationManifestTool {
	return workspaceIntegrationManifestTool{
		Name:        "docs_create_document",
		Description: "Create a new Google Docs document",
		Source:      "google-docs",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": { "type": "string" }
			},
			"required": ["title"],
			"additionalProperties": false
		}`),
		Request: &workspaceIntegrationHTTPRequest{
			Method: http.MethodPost,
			URL:    strings.TrimRight(googleDocsAPIBaseURL, "/") + "/documents",
			Body: &workspaceIntegrationHTTPBody{
				Fields: []workspaceIntegrationHTTPBodyField{
					{Path: "title", Required: true, Value: workspaceIntegrationHTTPValue{Source: "arg", Name: "title"}},
				},
			},
			Auth: auth,
		},
	}
}

func parseGoogleWorkspaceIntegrationConfig(integration store.WorkspaceIntegration) (googleWorkspaceIntegrationConfig, error) {
	var cfg googleWorkspaceIntegrationConfig
	if len(integration.Config) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(integration.Config, &cfg); err != nil {
		return cfg, fmt.Errorf("parse google workspace integration config: %w", err)
	}
	cfg.Email = strings.TrimSpace(cfg.Email)
	return cfg, nil
}

func (s *Server) googleWorkspacePreflightServiceStatus(ctx context.Context, requestedLevels, effectiveLevels map[string]string, grantedScopes []string, accessToken string) map[string]googleWorkspaceServiceStatus {
	statuses := make(map[string]googleWorkspaceServiceStatus, 3)
	granted := make(map[string]struct{}, len(grantedScopes))
	for _, scope := range grantedScopes {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			granted[scope] = struct{}{}
		}
	}
	now := time.Now().UTC()
	for _, service := range []string{"gmail", "drive", "calendar"} {
		requestedLevel := strings.ToLower(strings.TrimSpace(requestedLevels[service]))
		if requestedLevel == "" {
			requestedLevel = "off"
		}
		effectiveLevel := strings.ToLower(strings.TrimSpace(effectiveLevels[service]))
		if effectiveLevel == "" {
			effectiveLevel = "off"
		}
		switch {
		case requestedLevel == "off":
			statuses[service] = googleWorkspaceServiceStatus{
				Service: service,
				Status:  "off",
				Detail:  "Service was not requested.",
				Action:  "connect_service",
			}
		case effectiveLevel == "off" || !googleWorkspaceScopesGranted(googleWorkspaceScopesByPermissionLevel[service][effectiveLevel], granted):
			statuses[service] = googleWorkspaceServiceStatus{
				Service: service,
				Status:  "missing_scope",
				Detail:  "Google did not grant the required " + googleWorkspaceServiceDisplayName(service) + " scope.",
				Action:  "grant_scope_or_reconnect",
			}
		default:
			statuses[service] = s.googleWorkspaceProbeService(ctx, service, accessToken, now)
		}
	}
	return statuses
}

func (s *Server) googleWorkspaceProbeService(ctx context.Context, service, accessToken string, checkedAt time.Time) googleWorkspaceServiceStatus {
	status := googleWorkspaceServiceStatus{
		Service:   service,
		CheckedAt: &checkedAt,
	}
	if strings.TrimSpace(accessToken) == "" {
		status.Status = "token_revoked"
		status.Detail = "Google access token is missing."
		status.Action = "reconnect_integration"
		return status
	}
	endpoint := googleWorkspacePreflightURL(service, checkedAt)
	if endpoint == "" {
		status.Status = "upstream_unavailable"
		status.Detail = "No Google preflight probe is configured for " + googleWorkspaceServiceDisplayName(service) + "."
		status.Action = "check_upstream_request"
		return status
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.Status = "upstream_unavailable"
		status.Detail = "Google preflight request could not be created."
		status.Action = "check_upstream_request"
		return status
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := workspaceIntegrationHTTPClient(s.allowInsecureWorkspaceIntegrationEndpoints).Do(req)
	if err != nil {
		status.Status = "upstream_unavailable"
		status.Detail = "Google preflight request failed."
		status.Action = "retry_later"
		return status
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		status.Status = "upstream_unavailable"
		status.Detail = "Google preflight response could not be read."
		status.Action = "retry_later"
		return status
	}
	status.HTTPStatus = &resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		status.Status = "connected"
		status.Detail = googleWorkspaceServiceDisplayName(service) + " preflight succeeded."
		return status
	}
	snippet := workspaceIntegrationUpstreamSnippet(body)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		status.Status = "token_revoked"
		status.Detail = "Google rejected the access token."
		status.Action = "reconnect_integration"
	case http.StatusForbidden:
		switch workspaceIntegrationGoogleWorkspaceForbiddenReason(snippet) {
		case "admin_policy":
			status.Status = "admin_policy_blocked"
			status.Detail = "Google Workspace admin policy blocked " + googleWorkspaceServiceDisplayName(service) + " API access."
			status.Action = "ask_google_workspace_admin"
		default:
			status.Status = "missing_scope"
			status.Detail = "Google denied " + googleWorkspaceServiceDisplayName(service) + " API access; reconnect and grant the required scope."
			status.Action = "grant_scope_or_reconnect"
		}
	case http.StatusTooManyRequests:
		status.Status = "rate_limited"
		status.Detail = "Google rate limited the " + googleWorkspaceServiceDisplayName(service) + " preflight."
		status.Action = "retry_later"
	default:
		status.Status = "upstream_unavailable"
		status.Detail = fmt.Sprintf("Google %s preflight returned HTTP %d.", googleWorkspaceServiceDisplayName(service), resp.StatusCode)
		if resp.StatusCode >= 500 {
			status.Action = "retry_later"
		} else {
			status.Action = "check_upstream_request"
		}
	}
	return status
}

func googleWorkspacePreflightURL(service string, now time.Time) string {
	switch service {
	case "gmail":
		return strings.TrimRight(googleGmailAPIBaseURL, "/") + "/users/me/profile"
	case "drive":
		values := url.Values{}
		values.Set("pageSize", "1")
		values.Set("fields", "files(id)")
		return strings.TrimRight(googleDriveAPIBaseURL, "/") + "/files?" + values.Encode()
	case "calendar":
		values := url.Values{}
		values.Set("maxResults", "1")
		values.Set("singleEvents", "true")
		values.Set("timeMin", now.Format(time.RFC3339))
		return strings.TrimRight(googleCalendarAPIBaseURL, "/") + "/calendars/primary/events?" + values.Encode()
	default:
		return ""
	}
}

func googleWorkspaceServiceDisplayName(service string) string {
	switch service {
	case "gmail":
		return "Gmail"
	case "drive":
		return "Google Drive"
	case "calendar":
		return "Google Calendar"
	default:
		return service
	}
}

func googleWorkspaceServiceStatusBlocksTools(status string) bool {
	switch status {
	case "missing_scope", "admin_policy_blocked", "token_revoked":
		return true
	// Transient provider conditions stay visible in service_status but must not
	// hide tools whose OAuth scopes were actually granted.
	default:
		return false
	}
}

func googleWorkspaceEffectiveGrantedScopes(requestedScopes []string, tokenResp workspaceIntegrationOAuthTokenResponse) []string {
	if strings.TrimSpace(tokenResp.Scope) == "" {
		return workspaceIntegrationUniqueScopes(requestedScopes)
	}
	return workspaceIntegrationUniqueScopes(strings.Fields(tokenResp.Scope))
}

func workspaceIntegrationUniqueScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func googleWorkspaceEffectivePermissionLevels(requested map[string]string, grantedScopes []string) (map[string]string, error) {
	levels, err := googleWorkspacePermissionLevels(requested)
	if err != nil {
		return nil, err
	}
	granted := make(map[string]struct{}, len(grantedScopes))
	for _, scope := range grantedScopes {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			granted[scope] = struct{}{}
		}
	}
	effective := make(map[string]string, len(levels))
	for service, requestedLevel := range levels {
		effective[service] = googleWorkspaceEffectivePermissionLevelForService(service, requestedLevel, granted)
	}
	return effective, nil
}

func googleWorkspaceEffectivePermissionLevelForService(service, requestedLevel string, granted map[string]struct{}) string {
	requestedLevel = strings.ToLower(strings.TrimSpace(requestedLevel))
	if requestedLevel == "" || requestedLevel == "off" {
		return "off"
	}
	levels := googleWorkspaceScopesByPermissionLevel[service]
	if requestedLevel == "read_write" && googleWorkspaceScopesGranted(levels["read_write"], granted) {
		return "read_write"
	}
	if (requestedLevel == "read" || requestedLevel == "read_write") && googleWorkspaceScopesGranted(levels["read"], granted) {
		return "read"
	}
	return "off"
}

func googleWorkspaceScopesGranted(required []string, granted map[string]struct{}) bool {
	if len(required) == 0 {
		return false
	}
	for _, scope := range required {
		if _, ok := granted[scope]; !ok {
			return false
		}
	}
	return true
}

func (s *Server) handleStartWorkspaceIntegrationOAuthBySlug(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "integrationSlug")
	provider, ok := workspaceIntegrationOAuthProviderForSlug(slug)
	if !ok {
		s.handleStartCustomMCPWorkspaceIntegrationOAuth(w, r, slug)
		return
	}
	s.handleStartWorkspaceIntegrationOAuth(w, r, provider)
}

func (s *Server) handleStartCustomMCPWorkspaceIntegrationOAuth(w http.ResponseWriter, r *http.Request, slug string) {
	accountID := accountIDFromContext(r.Context())
	if !isValidSlug(slug) {
		writeError(w, http.StatusBadRequest, "invalid integration slug")
		return
	}
	userID := userIDFromContext(r.Context())
	if userID == nil {
		writeError(w, http.StatusUnauthorized, "authenticated user required")
		return
	}
	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}
	var req workspaceIntegrationOAuthStartRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	endpoint := strings.TrimSpace(req.URL)
	if endpoint == "" {
		writeError(w, http.StatusNotFound, "workspace integration oauth provider not found")
		return
	}
	if err := workspaceIntegrationProbeEndpointSafe(r.Context(), endpoint, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	probe, err := s.probeWorkspaceIntegrationMCPRemote(r.Context(), endpoint, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to discover remote MCP OAuth metadata")
		return
	}
	if probe.OAuth == nil || !probe.OAuth.Available {
		writeError(w, http.StatusBadRequest, "remote MCP server did not advertise usable OAuth metadata")
		return
	}
	scopes, err := validateWorkspaceIntegrationCustomMCPScopes(req.Scopes, probe.OAuth.ScopesSupported)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	redirectURI := s.workspaceIntegrationOAuthRedirectURL(r)
	clientID := ""
	registrationEndpoint := strings.TrimSpace(probe.OAuth.RegistrationEndpoint)
	if registrationEndpoint != "" {
		clientID, err = s.registerWorkspaceIntegrationCustomMCPOAuthClient(r.Context(), registrationEndpoint, redirectURI)
		if err != nil {
			slog.Error("workspace_integrations.oauth.custom_mcp.registration_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", err)
			writeError(w, http.StatusBadGateway, "failed to register remote MCP OAuth client")
			return
		}
	} else {
		clientID = strings.TrimSpace(req.ClientID)
		if clientID == "" {
			writeError(w, http.StatusBadRequest, "remote MCP OAuth requires dynamic client registration or a static client_id")
			return
		}
	}
	if s.secretKey == "" {
		writeError(w, http.StatusInternalServerError, "SECRET_ENCRYPTION_KEY not configured")
		return
	}
	verifierA, err := randomStateNonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate oauth verifier")
		return
	}
	verifierB, err := randomStateNonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate oauth verifier")
		return
	}
	codeVerifier := verifierA + verifierB
	codeChallenge := workspaceIntegrationOAuthCodeChallenge(codeVerifier)
	// The PKCE verifier must stay confidential. The state round-trips through the
	// browser (and the untrusted remote authorization server), so encrypt the
	// verifier rather than relying on the state's HMAC, which only guards integrity.
	encryptedVerifier, err := crypto.Encrypt(codeVerifier, s.secretKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to protect oauth verifier")
		return
	}
	nonce, err := randomStateNonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create oauth state")
		return
	}
	returnPath := "/integrations"
	if workspace.ID != "" {
		returnPath = "/workspaces/" + url.PathEscape(workspace.ID) + "/integrations"
	}
	state, err := s.signWorkspaceIntegrationOAuthState(workspaceIntegrationOAuthState{
		Provider:    slug,
		AccountID:   accountID,
		WorkspaceID: workspace.ID,
		UserID:      *userID,
		ReturnPath:  returnPath,
		Scopes:      scopes,
		CustomMCP: &customMCPOAuthState{
			Endpoint:             endpoint,
			DisplayName:          strings.TrimSpace(req.DisplayName),
			Resource:             firstNonEmpty(probe.OAuth.Resource, endpoint),
			AuthorizationServer:  probe.OAuth.AuthorizationServer,
			Issuer:               probe.OAuth.Issuer,
			TokenURL:             probe.OAuth.TokenEndpoint,
			ClientID:             clientID,
			CodeVerifier:         encryptedVerifier,
			Scopes:               scopes,
			ResourceMetadataURL:  probe.OAuth.ResourceMetadataURL,
			RegistrationEndpoint: registrationEndpoint,
		},
		ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
		Nonce:     nonce,
	})
	if err != nil {
		slog.Error("workspace_integrations.oauth.custom_mcp.state_failed", "workspace_id", workspace.ID, "integration_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create oauth state")
		return
	}
	authURL, err := url.Parse(probe.OAuth.AuthorizationEndpoint)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid oauth authorization endpoint")
		return
	}
	if authURL.Scheme != "https" && (!s.allowInsecureWorkspaceIntegrationEndpoints || authURL.Scheme != "http") {
		writeError(w, http.StatusBadGateway, "remote MCP authorization endpoint must use https")
		return
	}
	values := authURL.Query()
	values.Set("client_id", clientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("response_type", "code")
	values.Set("state", state)
	values.Set("code_challenge", codeChallenge)
	values.Set("code_challenge_method", "S256")
	values.Set("resource", firstNonEmpty(probe.OAuth.Resource, endpoint))
	if len(scopes) > 0 {
		values.Set("scope", strings.Join(scopes, " "))
	}
	authURL.RawQuery = values.Encode()
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL.String()})
}

func (s *Server) handleStartWorkspaceIntegrationOAuth(w http.ResponseWriter, r *http.Request, provider workspaceIntegrationOAuthProvider) {
	accountID := accountIDFromContext(r.Context())
	if !s.workspaceIntegrationOAuthConfiguredForProvider(provider.Slug) {
		writeError(w, http.StatusServiceUnavailable, provider.DisplayName+" OAuth client is not configured")
		return
	}
	clientID, _ := s.oauthClientCredentialsForProvider(provider.Slug)
	userID := userIDFromContext(r.Context())
	if userID == nil {
		writeError(w, http.StatusUnauthorized, "authenticated user required")
		return
	}
	workspace, ok := s.workspaceForIntegrationRequest(w, r, accountID)
	if !ok {
		return
	}
	var req workspaceIntegrationOAuthStartRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	scopes, permissionLevels, err := workspaceIntegrationOAuthScopes(provider, req.Permissions)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	returnPath := "/integrations"
	if workspace.ID != "" {
		returnPath = "/workspaces/" + url.PathEscape(workspace.ID) + "/integrations"
	}
	nonce, err := randomStateNonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create oauth state")
		return
	}
	state, err := s.signWorkspaceIntegrationOAuthState(workspaceIntegrationOAuthState{
		Provider:         provider.Slug,
		AccountID:        accountID,
		WorkspaceID:      workspace.ID,
		UserID:           *userID,
		ReturnPath:       returnPath,
		Scopes:           scopes,
		PermissionLevels: permissionLevels,
		ExpiresAt:        time.Now().Add(10 * time.Minute).Unix(),
		Nonce:            nonce,
	})
	if err != nil {
		slog.Error("workspace_integrations.oauth.state_failed", "provider", provider.Slug, "workspace_id", workspace.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create oauth state")
		return
	}

	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("redirect_uri", s.workspaceIntegrationOAuthRedirectURL(r))
	values.Set("response_type", "code")
	values.Set("scope", strings.Join(scopes, " "))
	values.Set("access_type", "offline")
	values.Set("include_granted_scopes", "true")
	values.Set("prompt", "consent")
	values.Set("state", state)

	authURL, err := url.Parse(provider.AuthorizeURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid oauth authorization endpoint")
		return
	}
	authURL.RawQuery = values.Encode()
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL.String()})
}

func (s *Server) handleWorkspaceIntegrationOAuthCallback(w http.ResponseWriter, r *http.Request) {
	state, err := s.verifyWorkspaceIntegrationOAuthState(r.URL.Query().Get("state"))
	if err != nil {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, "", "/integrations", "error", "Invalid or expired OAuth state.")
		return
	}
	if state.CustomMCP != nil {
		s.handleCustomMCPWorkspaceIntegrationOAuthCallback(w, r, state)
		return
	}
	provider, ok := workspaceIntegrationOAuthProviderForSlug(state.Provider)
	if !ok {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Unsupported OAuth provider.")
		return
	}
	if len(state.Scopes) > 0 {
		provider.Scopes = append([]string(nil), state.Scopes...)
	}
	userID := userIDFromContext(r.Context())
	if userID == nil || state.UserID != *userID {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", fmt.Sprintf("Sign in as the user who started this %s OAuth flow.", provider.DisplayName))
		return
	}
	member, err := s.store.GetAccountMember(r.Context(), state.AccountID, *userID)
	if err != nil {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", "You no longer have access to this account.")
		return
	}
	if member.Role != "owner" && member.Role != "admin" {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", fmt.Sprintf("Owner or admin access is required to connect %s.", provider.DisplayName))
		return
	}
	workspace, err := s.store.GetWorkspace(r.Context(), state.AccountID, state.WorkspaceID)
	if err != nil {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", "Workspace not found.")
		return
	}
	if oauthErr := strings.TrimSpace(r.URL.Query().Get("error")); oauthErr != "" {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", oauthErr)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", fmt.Sprintf("Missing %s authorization code.", provider.DisplayName))
		return
	}

	tokenResp, err := s.exchangeWorkspaceIntegrationOAuthCode(r.Context(), provider, code, s.workspaceIntegrationOAuthRedirectURL(r))
	if err != nil {
		slog.Error("workspace_integrations.oauth.token_exchange_failed", "provider", provider.Slug, "workspace_id", workspace.ID, "error", err)
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", fmt.Sprintf("Failed to complete %s OAuth.", provider.DisplayName))
		return
	}
	email, err := s.workspaceIntegrationOAuthUserEmail(r.Context(), provider, tokenResp.AccessToken)
	if err != nil {
		slog.Warn("workspace_integrations.oauth.userinfo_failed", "provider", provider.Slug, "workspace_id", workspace.ID, "error", err)
	}
	if err := s.saveWorkspaceOAuthIntegration(r.Context(), provider, workspace.ID, email, state.PermissionLevels, tokenResp); err != nil {
		slog.Error("workspace_integrations.oauth.save_failed", "provider", provider.Slug, "workspace_id", workspace.ID, "error", err)
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", fmt.Sprintf("Failed to save %s connection.", provider.DisplayName))
		return
	}
	// Connecting an integration is additive (workspace-scoped token, live policy);
	// no token revocation. See the token-revocation regression note.
	if err := s.ensureWorkspaceIntegrationRuntimeForWorkspace(r.Context(), state.AccountID, workspace.ID); err != nil {
		slog.Error("workspace_integrations.oauth.runtime_ensure_failed", "provider", provider.Slug, "workspace_id", workspace.ID, "error", err)
		s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "error", fmt.Sprintf("Failed to assign %s to machines.", provider.DisplayName))
		return
	}

	if s.activity != nil {
		s.activity.Log(r.Context(), events.LogParams{
			Category:  "config",
			Action:    "config.workspace_integration_created",
			Status:    "success",
			ActorType: "user",
			ActorID:   userID,
			AccountID: &state.AccountID,
			Summary:   fmt.Sprintf("Configured workspace integration '%s'", provider.DisplayName),
			Detail:    map[string]any{"workspace_id": workspace.ID, "integration_slug": provider.Slug, "target": email},
		})
	}

	s.redirectWorkspaceIntegrationOAuthResult(w, r, provider.Slug, state.ReturnPath, "connected", "")
}

func (s *Server) handleCustomMCPWorkspaceIntegrationOAuthCallback(w http.ResponseWriter, r *http.Request, state workspaceIntegrationOAuthState) {
	userID := userIDFromContext(r.Context())
	if userID == nil || state.UserID != *userID {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Sign in as the user who started this remote MCP OAuth flow.")
		return
	}
	member, err := s.store.GetAccountMember(r.Context(), state.AccountID, *userID)
	if err != nil {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "You no longer have access to this account.")
		return
	}
	if member.Role != "owner" && member.Role != "admin" {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Owner or admin access is required to connect this remote MCP server.")
		return
	}
	workspace, err := s.store.GetWorkspace(r.Context(), state.AccountID, state.WorkspaceID)
	if err != nil {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Workspace not found.")
		return
	}
	if oauthErr := strings.TrimSpace(r.URL.Query().Get("error")); oauthErr != "" {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", oauthErr)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Missing remote MCP authorization code.")
		return
	}
	tokenResp, err := s.exchangeCustomMCPWorkspaceIntegrationOAuthCode(r.Context(), state.CustomMCP, code, s.workspaceIntegrationOAuthRedirectURL(r))
	if err != nil {
		slog.Error("workspace_integrations.oauth.custom_mcp.token_exchange_failed", "workspace_id", workspace.ID, "integration_slug", state.Provider, "error", err)
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Failed to complete remote MCP OAuth.")
		return
	}
	probe, err := s.probeWorkspaceIntegrationMCPRemote(r.Context(), state.CustomMCP.Endpoint, "Bearer "+tokenResp.AccessToken)
	if err != nil {
		slog.Error("workspace_integrations.oauth.custom_mcp.reprobe_failed", "workspace_id", workspace.ID, "integration_slug", state.Provider, "error", err)
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Failed to verify remote MCP server after OAuth.")
		return
	}
	if err := s.saveCustomMCPWorkspaceOAuthIntegration(r.Context(), workspace.ID, state, probe, tokenResp); err != nil {
		slog.Error("workspace_integrations.oauth.custom_mcp.save_failed", "workspace_id", workspace.ID, "integration_slug", state.Provider, "error", err)
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Failed to save remote MCP OAuth connection.")
		return
	}
	// Saving a custom MCP server is additive (workspace-scoped token, live policy);
	// no token revocation. See the token-revocation regression note.
	if err := s.ensureWorkspaceIntegrationRuntimeForWorkspace(r.Context(), state.AccountID, workspace.ID); err != nil {
		slog.Error("workspace_integrations.oauth.custom_mcp.runtime_ensure_failed", "workspace_id", workspace.ID, "integration_slug", state.Provider, "error", err)
		s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "error", "Failed to assign remote MCP server to machines.")
		return
	}
	s.redirectWorkspaceIntegrationOAuthResult(w, r, state.Provider, state.ReturnPath, "connected", "")
}

func (s *Server) saveCustomMCPWorkspaceOAuthIntegration(ctx context.Context, workspaceID string, state workspaceIntegrationOAuthState, probe workspaceIntegrationProbeResponse, tokenResp workspaceIntegrationOAuthTokenResponse) error {
	if s.secretKey == "" {
		return errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	if state.CustomMCP == nil {
		return errors.New("custom MCP oauth state missing")
	}
	manifestTools := customMCPWorkspaceIntegrationManifestFromProbe(probe.Tools, state.CustomMCP.TokenURL, state.CustomMCP.ClientID, state.CustomMCP.Resource)
	manifest, err := json.Marshal(manifestTools)
	if err != nil {
		return err
	}
	allowedTools, deniedTools := customMCPWorkspaceIntegrationInitialPolicy(probe.Tools)
	displayName := strings.TrimSpace(state.CustomMCP.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(probe.Server.Name)
	}
	if displayName == "" {
		displayName = state.CustomMCP.Endpoint
	}
	config, err := json.Marshal(map[string]interface{}{
		"display_target":              state.CustomMCP.Endpoint,
		"server_name":                 probe.Server.Name,
		"server_version":              probe.Server.Version,
		"protocol_version":            probe.ProtocolVersion,
		"probed_at":                   probe.ProbedAt,
		"oauth_resource":              state.CustomMCP.Resource,
		"oauth_authorization_server":  state.CustomMCP.AuthorizationServer,
		"oauth_issuer":                state.CustomMCP.Issuer,
		"oauth_token_url":             state.CustomMCP.TokenURL,
		"oauth_client_id":             state.CustomMCP.ClientID,
		"oauth_scopes":                state.CustomMCP.Scopes,
		"oauth_resource_metadata_url": state.CustomMCP.ResourceMetadataURL,
	})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	actorID := userIDFromContext(ctx)
	endpoint := state.CustomMCP.Endpoint
	existing, err := s.findWorkspaceIntegrationBySlug(ctx, workspaceID, state.Provider)
	if err != nil && !errors.Is(err, store.ErrPluginNotFound) {
		return fmt.Errorf("load existing custom mcp oauth integration: %w", err)
	}
	if err := s.ensureWorkspaceIntegrationConnectionSlugAvailable(ctx, workspaceID, state.Provider, existing); err != nil {
		return err
	}
	integration := store.WorkspaceIntegration{
		WorkspaceID:  workspaceID,
		Slug:         state.Provider,
		DisplayName:  displayName,
		Kind:         "custom-mcp",
		Transport:    "mcp-remote",
		Endpoint:     &endpoint,
		Enabled:      true,
		ToolManifest: manifest,
		Config:       config,
		AllowedTools: allowedTools,
		DeniedTools:  deniedTools,
		ApprovedBy:   actorID,
		ApprovedAt:   &now,
		ConnectedBy:  actorID,
		ConnectedAt:  &now,
	}
	if err := s.store.UpsertWorkspaceIntegration(ctx, &integration); err != nil {
		return fmt.Errorf("upsert custom mcp oauth integration: %w", err)
	}
	return s.persistWorkspaceOAuthCredential(ctx, integration.ID, tokenResp, workspaceOAuthCredentialPersistOptions{
		PreserveRefreshOnEmpty: true,
	})
}

func (s *Server) saveWorkspaceOAuthIntegration(ctx context.Context, provider workspaceIntegrationOAuthProvider, workspaceID, email string, permissionLevels map[string]string, tokenResp workspaceIntegrationOAuthTokenResponse) error {
	if s.secretKey == "" {
		return errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	connectionSlug := provider.Slug
	if provider.Slug == googleWorkspaceIntegrationSlug {
		connectionSlug = googleWorkspaceIntegrationConnectionSlug(email)
	}
	if provider.Slug == googleWorkspaceIntegrationSlug && len(permissionLevels) == 0 {
		levels, err := googleWorkspacePermissionLevels(nil)
		if err != nil {
			return err
		}
		permissionLevels = levels
	}
	requestedPermissionLevels := permissionLevels
	if provider.Slug == googleWorkspaceIntegrationSlug {
		levels, err := googleWorkspacePermissionLevels(permissionLevels)
		if err != nil {
			return err
		}
		requestedPermissionLevels = levels
	}
	grantedScopes := googleWorkspaceEffectiveGrantedScopes(provider.Scopes, tokenResp)
	effectivePermissionLevels := permissionLevels
	if provider.Slug == googleWorkspaceIntegrationSlug {
		levels, err := googleWorkspaceEffectivePermissionLevels(requestedPermissionLevels, grantedScopes)
		if err != nil {
			return err
		}
		effectivePermissionLevels = levels
	}
	serviceStatus := map[string]googleWorkspaceServiceStatus(nil)
	if provider.Slug == googleWorkspaceIntegrationSlug {
		serviceStatus = s.googleWorkspacePreflightServiceStatus(ctx, requestedPermissionLevels, effectivePermissionLevels, grantedScopes, tokenResp.AccessToken)
	}
	cfg := googleWorkspaceIntegrationConfig{
		Email:                     email,
		Scopes:                    grantedScopes,
		PermissionLevels:          effectivePermissionLevels,
		RequestedPermissionLevels: requestedPermissionLevels,
		ServiceStatus:             serviceStatus,
		AllowedDriveFolderIDs:     []string{"root"},
		AllowedCalendarIDs:        []string{"primary"},
	}
	existing, err := s.findWorkspaceIntegrationBySlug(ctx, workspaceID, connectionSlug)
	if err == nil {
		existingCfg, parseErr := parseGoogleWorkspaceIntegrationConfig(*existing)
		if parseErr != nil {
			return parseErr
		}
		if len(existingCfg.AllowedDriveFolderIDs) > 0 {
			cfg.AllowedDriveFolderIDs = existingCfg.AllowedDriveFolderIDs
		}
		if len(existingCfg.AllowedCalendarIDs) > 0 {
			cfg.AllowedCalendarIDs = existingCfg.AllowedCalendarIDs
		}
	} else if !errors.Is(err, store.ErrPluginNotFound) {
		return fmt.Errorf("load existing workspace oauth integration: %w", err)
	} else if provider.Slug == googleWorkspaceIntegrationSlug && connectionSlug != provider.Slug {
		if existing, legacyErr := s.findWorkspaceIntegrationBySlug(ctx, workspaceID, provider.Slug); legacyErr == nil {
			existingCfg, parseErr := parseGoogleWorkspaceIntegrationConfig(*existing)
			if parseErr != nil {
				return parseErr
			}
			if strings.EqualFold(strings.TrimSpace(existingCfg.Email), strings.TrimSpace(email)) {
				if len(existingCfg.AllowedDriveFolderIDs) > 0 {
					cfg.AllowedDriveFolderIDs = existingCfg.AllowedDriveFolderIDs
				}
				if len(existingCfg.AllowedCalendarIDs) > 0 {
					cfg.AllowedCalendarIDs = existingCfg.AllowedCalendarIDs
				}
			}
		} else if !errors.Is(legacyErr, store.ErrPluginNotFound) {
			return fmt.Errorf("load legacy workspace oauth integration: %w", legacyErr)
		}
	}
	if err := s.ensureWorkspaceIntegrationConnectionSlugAvailable(ctx, workspaceID, connectionSlug, existing); err != nil {
		return err
	}
	config, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	actorID := userIDFromContext(ctx)
	integration := store.WorkspaceIntegration{
		WorkspaceID:  workspaceID,
		Slug:         connectionSlug,
		DisplayName:  provider.DisplayName,
		Kind:         provider.Kind,
		Transport:    provider.Transport,
		Enabled:      true,
		ToolManifest: provider.ToolManifest(effectivePermissionLevels),
		Config:       config,
		ApprovedBy:   actorID,
		ApprovedAt:   &now,
		ConnectedBy:  actorID,
		ConnectedAt:  &now,
	}
	if provider.Slug == googleWorkspaceIntegrationSlug {
		integration.ToolManifest = googleWorkspaceToolManifestForServiceStatus(effectivePermissionLevels, serviceStatus)
	}
	if err := s.store.UpsertWorkspaceIntegration(ctx, &integration); err != nil {
		return fmt.Errorf("upsert workspace oauth integration: %w", err)
	}

	return s.persistWorkspaceOAuthCredential(ctx, integration.ID, tokenResp, workspaceOAuthCredentialPersistOptions{
		PreserveRefreshOnEmpty: true,
	})
}

func (s *Server) exchangeWorkspaceIntegrationOAuthCode(ctx context.Context, provider workspaceIntegrationOAuthProvider, code, redirectURI string) (workspaceIntegrationOAuthTokenResponse, error) {
	clientID, clientSecret := s.oauthClientCredentialsForProvider(provider.Slug)
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	return s.workspaceIntegrationOAuthTokenRequest(ctx, provider.TokenURL, form)
}

func (s *Server) exchangeCustomMCPWorkspaceIntegrationOAuthCode(ctx context.Context, state *customMCPOAuthState, code, redirectURI string) (workspaceIntegrationOAuthTokenResponse, error) {
	if state == nil {
		return workspaceIntegrationOAuthTokenResponse{}, errors.New("custom MCP oauth state missing")
	}
	if s.secretKey == "" {
		return workspaceIntegrationOAuthTokenResponse{}, errors.New("SECRET_ENCRYPTION_KEY not configured")
	}
	codeVerifier, err := crypto.Decrypt(state.CodeVerifier, s.secretKey)
	if err != nil {
		return workspaceIntegrationOAuthTokenResponse{}, fmt.Errorf("decrypt pkce verifier: %w", err)
	}
	form := url.Values{}
	form.Set("client_id", state.ClientID)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	form.Set("code_verifier", codeVerifier)
	if strings.TrimSpace(state.Resource) != "" {
		form.Set("resource", strings.TrimSpace(state.Resource))
	}
	return s.workspaceIntegrationOAuthTokenRequest(ctx, state.TokenURL, form)
}

func (s *Server) registerWorkspaceIntegrationCustomMCPOAuthClient(ctx context.Context, registrationEndpoint, redirectURI string) (string, error) {
	if err := workspaceIntegrationProbeEndpointSafe(ctx, registrationEndpoint, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"client_name":                "OpenClaw Machines Workspace Integrations",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := workspaceIntegrationHTTPClient(s.allowInsecureWorkspaceIntegrationEndpoints).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("dynamic client registration returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var decoded struct {
		ClientID                string `json:"client_id"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method,omitempty"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("decode dynamic client registration: %w", err)
	}
	if strings.TrimSpace(decoded.ClientID) == "" {
		return "", errors.New("dynamic client registration missing client_id")
	}
	if method := strings.TrimSpace(decoded.TokenEndpointAuthMethod); method != "" && !strings.EqualFold(method, "none") {
		return "", fmt.Errorf("unsupported dynamic client token auth method %q", method)
	}
	return strings.TrimSpace(decoded.ClientID), nil
}

func validateWorkspaceIntegrationCustomMCPScopes(requested, supported []string) ([]string, error) {
	cleaned := make([]string, 0, len(requested))
	supportedSet := map[string]struct{}{}
	for _, scope := range supported {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			supportedSet[scope] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	for _, scope := range requested {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if len(supportedSet) > 0 {
			if _, ok := supportedSet[scope]; !ok {
				return nil, fmt.Errorf("remote MCP OAuth scope %q is not advertised by the server", scope)
			}
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		cleaned = append(cleaned, scope)
	}
	return cleaned, nil
}

func workspaceIntegrationOAuthCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func customMCPWorkspaceIntegrationManifestFromProbe(tools []workspaceIntegrationProbeTool, tokenURL, clientID, resource string) []workspaceIntegrationManifestTool {
	out := make([]workspaceIntegrationManifestTool, 0, len(tools))
	for _, tool := range tools {
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
		}
		out = append(out, workspaceIntegrationManifestTool{
			Name:        strings.TrimSpace(tool.Name),
			Description: tool.Description,
			InputSchema: schema,
			MCPRemote: &workspaceIntegrationMCPRemoteToolSpec{
				Name: strings.TrimSpace(tool.Name),
				Auth: &workspaceIntegrationHTTPAuth{
					Type:     "oauth",
					Required: true,
					TokenURL: strings.TrimSpace(tokenURL),
					ClientID: strings.TrimSpace(clientID),
					Resource: strings.TrimSpace(resource),
				},
			},
		})
	}
	return out
}

func customMCPWorkspaceIntegrationInitialPolicy(tools []workspaceIntegrationProbeTool) ([]string, []string) {
	var allowed []string
	var denied []string
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		if workspaceIntegrationProbeToolLooksReadOnly(name, tool.Description) {
			allowed = append(allowed, name)
		} else {
			denied = append(denied, name)
		}
	}
	return allowed, denied
}

// workspaceIntegrationProbeToolLooksReadOnly classifies a discovered tool as
// read-like (allowed by default) vs write-like (denied by default). Real MCP
// tools are namespaced (notion-search, kv_namespaces_list, get_record), so the
// verb can appear in any name segment — not just as a prefix. Write verbs win.
func workspaceIntegrationProbeToolLooksReadOnly(name, description string) bool {
	segments := workspaceIntegrationToolNameSegments(name)
	for _, seg := range segments {
		if workspaceIntegrationToolWriteVerbs[seg] {
			return false
		}
	}
	for _, seg := range segments {
		if workspaceIntegrationToolReadVerbs[seg] {
			return true
		}
	}
	return false
}

var workspaceIntegrationToolReadVerbs = map[string]bool{
	"get": true, "list": true, "search": true, "find": true, "read": true,
	"fetch": true, "lookup": true, "query": true, "describe": true, "view": true,
	"show": true, "count": true, "retrieve": true, "check": true, "browse": true,
}

var workspaceIntegrationToolWriteVerbs = map[string]bool{
	"create": true, "update": true, "delete": true, "remove": true, "send": true,
	"write": true, "patch": true, "post": true, "submit": true, "sync": true,
	"move": true, "duplicate": true, "insert": true, "upload": true, "publish": true,
	"archive": true, "revoke": true, "cancel": true, "edit": true, "add": true,
	"set": true, "put": true, "rename": true, "merge": true, "import": true,
}

func workspaceIntegrationToolNameSegments(name string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(name)), func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' ' || r == '/'
	})
}

func (s *Server) workspaceIntegrationOAuthTokenRequest(ctx context.Context, tokenURL string, form url.Values) (workspaceIntegrationOAuthTokenResponse, error) {
	if err := workspaceIntegrationOAuthTokenEndpointSafe(ctx, tokenURL, s.allowInsecureWorkspaceIntegrationEndpoints); err != nil {
		return workspaceIntegrationOAuthTokenResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return workspaceIntegrationOAuthTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := callWorkspaceIntegrationHTTPClient(s.allowInsecureWorkspaceIntegrationEndpoints).Do(req)
	if err != nil {
		return workspaceIntegrationOAuthTokenResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return workspaceIntegrationOAuthTokenResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return workspaceIntegrationOAuthTokenResponse{}, fmt.Errorf("oauth token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokenResp workspaceIntegrationOAuthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return workspaceIntegrationOAuthTokenResponse{}, fmt.Errorf("decode oauth token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return workspaceIntegrationOAuthTokenResponse{}, errors.New("oauth token response missing access_token")
	}
	return tokenResp, nil
}

func workspaceIntegrationOAuthTokenEndpointSafe(ctx context.Context, rawURL string, allowInsecure bool) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("oauth token endpoint is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("invalid oauth token endpoint")
	}
	if parsed.Scheme != "https" && (!allowInsecure || parsed.Scheme != "http") {
		return errors.New("oauth token endpoint must use https")
	}
	if parsed.User != nil {
		return errors.New("oauth token endpoint must not include userinfo")
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("oauth token endpoint host is required")
	}
	if allowInsecure {
		return nil
	}
	if err := workspaceIntegrationEndpointHostSafe(ctx, host); err != nil {
		return errors.New(strings.Replace(err.Error(), "endpoint host", "oauth token endpoint host", 1))
	}
	return nil
}

func (s *Server) workspaceIntegrationOAuthUserEmail(ctx context.Context, provider workspaceIntegrationOAuthProvider, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.UserInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := callWorkspaceIntegrationHTTPClient(s.allowInsecureWorkspaceIntegrationEndpoints).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("oauth userinfo returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var info googleUserInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	return strings.TrimSpace(info.Email), nil
}

// oauthClientCredentialsForProvider resolves the OAuth client_id/client_secret
// for a provider. It prefers per-provider env vars (e.g.
// GOOGLE_WORKSPACE_OAUTH_CLIENT_ID / _SECRET, derived from the slug) and falls
// back to the platform-wide OAuth client. This lets each OAuth provider use its
// own registered OAuth application instead of every provider sharing one global
// client.
func (s *Server) oauthClientCredentialsForProvider(slug string) (string, string) {
	prefix := oauthProviderEnvPrefix(slug)
	if prefix != "" {
		id := strings.TrimSpace(os.Getenv(prefix + "_OAUTH_CLIENT_ID"))
		secret := strings.TrimSpace(os.Getenv(prefix + "_OAUTH_CLIENT_SECRET"))
		if id != "" && secret != "" {
			return id, secret
		}
	}
	return strings.TrimSpace(s.oauthClientID), strings.TrimSpace(s.oauthClientSecret)
}

func oauthProviderEnvPrefix(slug string) string {
	upper := strings.ToUpper(strings.TrimSpace(slug))
	if upper == "" {
		return ""
	}
	return strings.NewReplacer("-", "_", ".", "_").Replace(upper)
}

func (s *Server) workspaceIntegrationOAuthConfiguredForProvider(slug string) bool {
	id, secret := s.oauthClientCredentialsForProvider(slug)
	return id != "" && secret != ""
}

func (s *Server) workspaceIntegrationOAuthStateKey() []byte {
	if s.secretKey != "" {
		return []byte(s.secretKey)
	}
	return []byte(s.oauthClientSecret)
}

func (s *Server) signWorkspaceIntegrationOAuthState(state workspaceIntegrationOAuthState) (string, error) {
	key := s.workspaceIntegrationOAuthStateKey()
	if len(key) == 0 {
		return "", errors.New("oauth state signing key not configured")
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encodedPayload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + signature, nil
}

func (s *Server) verifyWorkspaceIntegrationOAuthState(raw string) (workspaceIntegrationOAuthState, error) {
	var state workspaceIntegrationOAuthState
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return state, errors.New("invalid oauth state")
	}
	key := s.workspaceIntegrationOAuthStateKey()
	if len(key) == 0 {
		return state, errors.New("oauth state signing key not configured")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(actual, expected) {
		return state, errors.New("invalid oauth state signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return state, err
	}
	if state.ExpiresAt < time.Now().Unix() {
		return state, errors.New("oauth state expired")
	}
	return state, nil
}

func (s *Server) workspaceIntegrationOAuthRedirectURL(r *http.Request) string {
	base := strings.TrimRight(s.backendURL, "/")
	if base == "" {
		scheme := r.Header.Get("X-Forwarded-Proto")
		if scheme == "" {
			if r.TLS != nil {
				scheme = "https"
			} else {
				scheme = "http"
			}
		}
		base = scheme + "://" + r.Host
	}
	return base + workspaceIntegrationOAuthCallbackPath
}

func (s *Server) redirectWorkspaceIntegrationOAuthResult(w http.ResponseWriter, r *http.Request, providerSlug, returnPath, status, message string) {
	if strings.TrimSpace(returnPath) == "" || !strings.HasPrefix(returnPath, "/") || strings.HasPrefix(returnPath, "//") {
		returnPath = "/integrations"
	}
	base := s.workspaceIntegrationOAuthFrontendBaseURL(r)
	if base == "" {
		slog.Error("workspace_integrations.oauth.frontend_url_missing")
		writeError(w, http.StatusInternalServerError, "FRONTEND_URL is required for workspace integration OAuth redirects")
		return
	}
	target, err := url.Parse(base)
	if err != nil {
		slog.Error("workspace_integrations.oauth.frontend_url_invalid", "frontend_url", base, "error", err)
		writeError(w, http.StatusInternalServerError, "FRONTEND_URL is invalid")
		return
	}
	ref, _ := url.Parse(returnPath)
	target = target.ResolveReference(ref)
	values := target.Query()
	if status != "" {
		values.Set(workspaceIntegrationOAuthStatusParam(providerSlug), status)
	}
	if message != "" {
		values.Set("message", message)
	}
	target.RawQuery = values.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func workspaceIntegrationOAuthStatusParam(providerSlug string) string {
	providerSlug = strings.TrimSpace(providerSlug)
	if providerSlug == "" {
		return "workspace-integration"
	}
	return providerSlug
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (s *Server) workspaceIntegrationOAuthFrontendBaseURL(r *http.Request) string {
	base := strings.TrimRight(strings.TrimSpace(s.frontendURL()), "/")
	if isAbsoluteHTTPURL(base) {
		return base
	}
	return ""
}

func isAbsoluteHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// randomStateNonce returns a 128-bit URL-safe random token. It returns an error
// on RNG failure rather than falling back to a predictable value — this feeds
// the PKCE code_verifier, so a guessable fallback would defeat auth-code
// interception protection.
func randomStateNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate random nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
