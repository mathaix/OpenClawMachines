package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

type workspaceIntegrationCatalogItem struct {
	Slug           string `json:"slug"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	Category       string `json:"category"`
	AuthKind       string `json:"auth_kind"`
	Transport      string `json:"transport"`
	ConnectionSlug string `json:"connection_slug,omitempty"`
	GoogleService  string `json:"google_service,omitempty"`
	// RemoteURL is the fixed hosted MCP endpoint for curated mcp-remote entries.
	// The connect flow probes this URL (bearer) or runs OAuth against it; the user
	// never types it. Empty for http/bespoke entries.
	RemoteURL string                            `json:"remote_url,omitempty"`
	Developer string                            `json:"developer"`
	Website   string                            `json:"website"`
	Privacy   string                            `json:"privacy"`
	Terms     string                            `json:"terms"`
	Tools     []workspaceIntegrationCatalogTool `json:"tools"`
}

type workspaceIntegrationCatalogTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Access      string `json:"access"`
	Mode        string `json:"mode"`
	Source      string `json:"source,omitempty"`
}

func (s *Server) handleListWorkspaceIntegrationCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"integrations": workspaceIntegrationCatalogItems(),
	})
}

func workspaceIntegrationCatalogItems() []workspaceIntegrationCatalogItem {
	return []workspaceIntegrationCatalogItem{
		{
			Slug:        "github",
			DisplayName: "GitHub",
			Description: "Official GitHub MCP server — repos, issues, PRs, code search, and more.",
			Category:    "Featured",
			AuthKind:    "bearer",
			Transport:   "mcp-remote",
			RemoteURL:   "https://api.githubcopilot.com/mcp/",
			Developer:   "GitHub",
			Website:     "github.com",
			Privacy:     "github.com/site/privacy",
			Terms:       "docs.github.com/site-policy",
			Tools: []workspaceIntegrationCatalogTool{
				{Name: "list_pull_requests", Description: "List pull requests in a repository.", Access: "Read", Mode: "Interactive"},
				{Name: "get_file_contents", Description: "Read file contents from a repository.", Access: "Read", Mode: "Interactive"},
				{Name: "search_code", Description: "Search code across repositories.", Access: "Read", Mode: "Interactive"},
				{Name: "create_issue", Description: "Open an issue (requires write).", Access: "Write", Mode: "Interactive"},
			},
		},
		{
			Slug:           "gmail",
			DisplayName:    "Gmail",
			Description:    "Read Gmail messages and send email through workspace-approved tools.",
			Category:       "Business & Operations",
			AuthKind:       "oauth",
			Transport:      "http",
			ConnectionSlug: googleWorkspaceIntegrationSlug,
			GoogleService:  "gmail",
			Developer:      "Google",
			Website:        "workspace.google.com/products/gmail",
			Privacy:        "policies.google.com/privacy",
			Terms:          "policies.google.com/terms",
			Tools: []workspaceIntegrationCatalogTool{
				{Name: "gmail_list_messages", Description: "List Gmail messages, optionally by search query.", Access: "Read", Mode: "Interactive", Source: "google_workspace"},
				{Name: "gmail_get_message", Description: "Read a single Gmail message.", Access: "Read", Mode: "Interactive", Source: "google_workspace"},
				{Name: "gmail_send_message", Description: "Send an email.", Access: "Write", Mode: "Interactive", Source: "google_workspace"},
			},
		},
		{
			Slug:           "google-drive",
			DisplayName:    "Google Drive",
			Description:    "List Drive metadata and create approved files through workspace tools.",
			Category:       "Business & Operations",
			AuthKind:       "oauth",
			Transport:      "http",
			ConnectionSlug: googleWorkspaceIntegrationSlug,
			GoogleService:  "drive",
			Developer:      "Google",
			Website:        "workspace.google.com/products/drive",
			Privacy:        "policies.google.com/privacy",
			Terms:          "policies.google.com/terms",
			Tools: []workspaceIntegrationCatalogTool{
				{Name: "drive_list_files", Description: "List visible Google Drive file metadata.", Access: "Read", Mode: "Interactive", Source: "google_workspace"},
				{Name: "drive_create_file", Description: "Create a Drive file.", Access: "Write", Mode: "Interactive", Source: "google_workspace"},
			},
		},
		{
			Slug:           "google-calendar",
			DisplayName:    "Google Calendar",
			Description:    "Read and create calendar events through workspace-approved tools.",
			Category:       "Business & Operations",
			AuthKind:       "oauth",
			Transport:      "http",
			ConnectionSlug: googleWorkspaceIntegrationSlug,
			GoogleService:  "calendar",
			Developer:      "Google",
			Website:        "workspace.google.com/products/calendar",
			Privacy:        "policies.google.com/privacy",
			Terms:          "policies.google.com/terms",
			Tools: []workspaceIntegrationCatalogTool{
				{Name: "calendar_list_events", Description: "List upcoming Google Calendar events.", Access: "Read", Mode: "Interactive", Source: "google_workspace"},
				{Name: "calendar_create_event", Description: "Create a calendar event.", Access: "Write", Mode: "Interactive", Source: "google_workspace"},
			},
		},
		{
			Slug:        googleWorkspaceIntegrationSlug,
			DisplayName: "Google Workspace",
			Description: "OAuth connection for Gmail, Drive, Calendar, and Docs.",
			Category:    "Business & Operations",
			AuthKind:    "oauth",
			Transport:   "http",
			Developer:   "Google",
			Website:     "workspace.google.com",
			Privacy:     "policies.google.com/privacy",
			Terms:       "policies.google.com/terms",
			Tools:       workspaceIntegrationCatalogToolsFromManifest(googleWorkspaceToolManifest(nil)),
		},
		{
			Slug:        "notion",
			DisplayName: "Notion",
			Description: "Official Notion MCP — search, read, and update pages and databases.",
			Category:    "Business & Operations",
			AuthKind:    "oauth",
			Transport:   "mcp-remote",
			RemoteURL:   "https://mcp.notion.com/mcp",
			Developer:   "Notion",
			Website:     "notion.so",
			Privacy:     "notion.so/privacy",
			Terms:       "notion.so/terms",
			Tools: []workspaceIntegrationCatalogTool{
				{Name: "search", Description: "Search Notion pages and databases.", Access: "Read", Mode: "Interactive"},
				{Name: "fetch", Description: "Fetch a Notion page or database.", Access: "Read", Mode: "Interactive"},
				{Name: "create-pages", Description: "Create Notion pages (requires write).", Access: "Write", Mode: "Interactive"},
			},
		},
		{
			Slug:        "apollo",
			DisplayName: "Apollo.io",
			Description: "Prospecting and account research through the hosted Apollo MCP.",
			Category:    "Business & Operations",
			AuthKind:    "oauth",
			Transport:   "mcp-remote",
			RemoteURL:   "https://mcp.apollo.io/mcp",
			Developer:   "Apollo.io",
			Website:     "apollo.io",
			Privacy:     "apollo.io/privacy-policy",
			Terms:       "apollo.io/terms",
			Tools: []workspaceIntegrationCatalogTool{
				{Name: "people_search", Description: "Search people records through Apollo.", Access: "Read", Mode: "Interactive"},
				{Name: "organization_search", Description: "Search organization records through Apollo.", Access: "Read", Mode: "Interactive"},
			},
		},
		{
			Slug:        "brevo",
			DisplayName: "Brevo",
			Description: "Official Brevo MCP — contacts, email campaigns, and transactional email.",
			Category:    "Business & Operations",
			AuthKind:    "bearer",
			Transport:   "mcp-remote",
			RemoteURL:   "https://mcp.brevo.com/v1/brevo/mcp",
			Developer:   "Brevo",
			Website:     "brevo.com",
			Privacy:     "brevo.com/legal/privacypolicy",
			Terms:       "brevo.com/legal/termsofuse",
			Tools: []workspaceIntegrationCatalogTool{
				{Name: "get_contacts", Description: "List Brevo contacts.", Access: "Read", Mode: "Interactive"},
				{Name: "send_transac_email", Description: "Send a transactional email (requires write).", Access: "Write", Mode: "Interactive"},
			},
		},
		{
			Slug:        "cloudflare",
			DisplayName: "Cloudflare",
			Description: "Cloudflare Workers bindings MCP — manage KV, R2, D1, and Workers.",
			Category:    "Business & Operations",
			AuthKind:    "oauth",
			Transport:   "mcp-remote",
			RemoteURL:   "https://bindings.mcp.cloudflare.com/mcp",
			Developer:   "Cloudflare",
			Website:     "cloudflare.com",
			Privacy:     "cloudflare.com/privacypolicy",
			Terms:       "cloudflare.com/website-terms",
			Tools: []workspaceIntegrationCatalogTool{
				{Name: "kv_namespaces_list", Description: "List Workers KV namespaces.", Access: "Read", Mode: "Interactive"},
				{Name: "r2_buckets_list", Description: "List R2 buckets.", Access: "Read", Mode: "Interactive"},
				{Name: "d1_databases_list", Description: "List D1 databases.", Access: "Read", Mode: "Interactive"},
			},
		},
		{
			Slug:        "openapi-import",
			DisplayName: "OpenAPI import",
			Description: "Import an OpenAPI 3.x spec into workspace-reviewed tools.",
			Category:    "Custom",
			AuthKind:    "bearer",
			Transport:   "http",
			Developer:   "Custom",
			Website:     "OpenAPI spec",
			Privacy:     "Provider policy",
			Terms:       "Provider terms",
			Tools:       []workspaceIntegrationCatalogTool{},
		},
		{
			Slug:        "graphql-import",
			DisplayName: "GraphQL import",
			Description: "Import a GraphQL schema into fixed query and mutation tools.",
			Category:    "Custom",
			AuthKind:    "bearer",
			Transport:   "http",
			Developer:   "Custom",
			Website:     "GraphQL endpoint",
			Privacy:     "Provider policy",
			Terms:       "Provider terms",
			Tools:       []workspaceIntegrationCatalogTool{},
		},
	}
}

func workspaceIntegrationCatalogSlugReserved(slug string) bool {
	for _, item := range workspaceIntegrationCatalogItems() {
		if item.Slug == slug {
			return true
		}
	}
	return false
}

// workspaceIntegrationCatalogRemoteURL returns the fixed remote MCP endpoint for a
// curated catalog slug (empty if the slug is not catalog-curated or is not a
// remote-MCP entry). Used to allow a curated entry to be saved under its own slug
// while still blocking an arbitrary custom server from squatting that slug.
func workspaceIntegrationCatalogRemoteURL(slug string) string {
	for _, item := range workspaceIntegrationCatalogItems() {
		if item.Slug == slug {
			return item.RemoteURL
		}
	}
	return ""
}

func workspaceIntegrationCatalogToolsFromManifest(manifest json.RawMessage) []workspaceIntegrationCatalogTool {
	tools, err := parseWorkspaceIntegrationManifest(manifest)
	if err != nil {
		return nil
	}
	out := make([]workspaceIntegrationCatalogTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		out = append(out, workspaceIntegrationCatalogTool{
			Name:        tool.Name,
			Description: tool.Description,
			Access:      workspaceIntegrationCatalogToolAccessFromManifest(tool),
			Mode:        "Interactive",
			Source:      strings.ToLower(strings.TrimSpace(tool.Source)),
		})
	}
	return out
}

func workspaceIntegrationCatalogToolAccessFromManifest(tool workspaceIntegrationManifestTool) string {
	switch workspaceIntegrationManifestToolAccess(tool) {
	case "write":
		return "Write"
	default:
		return "Read"
	}
}

func workspaceIntegrationCatalogToolAccess(name string) string {
	switch name {
	case "gmail_send_message", "drive_create_file", "calendar_create_event", "docs_create_document", "create_record":
		return "Write"
	default:
		return "Read"
	}
}
