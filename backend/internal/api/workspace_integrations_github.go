package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

var githubAPIBaseURL = "https://api.github.com"

type githubWorkspaceIntegrationConfig struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

func githubWorkspaceIntegrationConnectionSlug(owner, repo, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		if !isValidSlug(requested) {
			return "", fmt.Errorf("invalid connection_slug")
		}
		return requested, nil
	}
	slug := "github-" + strings.ReplaceAll(workspaceIntegrationAddressSegment(owner+"-"+repo, "repo"), "_", "-")
	if len(slug) <= 63 {
		if !isValidSlug(slug) {
			return "", fmt.Errorf("could not derive valid github connection slug")
		}
		return slug, nil
	}
	slug = strings.TrimRight(slug[:63], "-")
	if slug == "" || !isValidSlug(slug) {
		return "", fmt.Errorf("could not derive valid github connection slug")
	}
	return slug, nil
}

func githubToolManifest() json.RawMessage {
	baseURL := strings.TrimRight(githubAPIBaseURL, "/")
	commonHeaders := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "openclaw-workspace-integrations",
	}
	auth := &workspaceIntegrationHTTPAuth{Type: "bearer", Scheme: "Bearer", Required: false}
	return mustWorkspaceIntegrationToolManifest([]workspaceIntegrationManifestTool{
		{
			Name:        "get_repo",
			Description: "Get the configured GitHub repository summary",
			Parameters: json.RawMessage(`{
				"type": "object",
				"additionalProperties": false
			}`),
			Request: &workspaceIntegrationHTTPRequest{
				Method:  http.MethodGet,
				URL:     baseURL + "/repos/{owner}/{repo}",
				Headers: commonHeaders,
				PathParams: map[string]workspaceIntegrationHTTPValue{
					"owner": {Source: "config", Name: "owner"},
					"repo":  {Source: "config", Name: "repo"},
				},
				Auth: auth,
				Response: &workspaceIntegrationHTTPResponseTransform{Fields: map[string]string{
					"full_name":         "full_name",
					"description":       "description",
					"private":           "private",
					"html_url":          "html_url",
					"default_branch":    "default_branch",
					"open_issues_count": "open_issues_count",
					"stargazers_count":  "stargazers_count",
					"forks_count":       "forks_count",
					"updated_at":        "updated_at",
				}},
			},
		},
		{
			Name:        "list_issues",
			Description: "List issues for the configured GitHub repository",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"state": {
						"type": "string",
						"enum": ["open", "closed", "all"]
					},
					"per_page": {
						"type": "integer",
						"minimum": 1,
						"maximum": 30
					}
				},
				"additionalProperties": false
			}`),
			Request: &workspaceIntegrationHTTPRequest{
				Method:  http.MethodGet,
				URL:     baseURL + "/repos/{owner}/{repo}/issues",
				Headers: commonHeaders,
				PathParams: map[string]workspaceIntegrationHTTPValue{
					"owner": {Source: "config", Name: "owner"},
					"repo":  {Source: "config", Name: "repo"},
				},
				Query: map[string]workspaceIntegrationHTTPValue{
					"state":    {Source: "arg", Name: "state", Default: "open", Enum: []string{"open", "closed", "all"}},
					"per_page": {Source: "arg", Name: "per_page", Default: 10, Min: workspaceIntegrationIntPtr(1), Max: workspaceIntegrationIntPtr(30)},
				},
				Auth: auth,
				Response: &workspaceIntegrationHTTPResponseTransform{
					Wrap:             "issues",
					ExcludeIfPresent: "pull_request",
					Fields: map[string]string{
						"number":     "number",
						"title":      "title",
						"state":      "state",
						"html_url":   "html_url",
						"user":       "user.login",
						"created_at": "created_at",
						"updated_at": "updated_at",
					},
				},
			},
		},
	})
}

func githubWorkspaceIntegrationTarget(integration store.WorkspaceIntegration) string {
	cfg, err := parseGitHubWorkspaceIntegrationConfig(integration)
	if err != nil {
		return ""
	}
	return cfg.Owner + "/" + cfg.Repo
}

func parseGitHubWorkspaceIntegrationConfig(integration store.WorkspaceIntegration) (githubWorkspaceIntegrationConfig, error) {
	var cfg githubWorkspaceIntegrationConfig
	if len(integration.Config) == 0 {
		return cfg, errors.New("github integration config is empty")
	}
	if err := json.Unmarshal(integration.Config, &cfg); err != nil {
		return cfg, fmt.Errorf("parse github integration config: %w", err)
	}
	cfg.Owner = strings.TrimSpace(cfg.Owner)
	cfg.Repo = strings.TrimSpace(cfg.Repo)
	if cfg.Owner == "" || cfg.Repo == "" {
		return cfg, errors.New("github owner and repo are required")
	}
	return cfg, nil
}
