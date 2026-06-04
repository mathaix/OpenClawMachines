package composio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client wraps the Composio REST API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Connection represents a Composio connected account (normalized from API response).
type Connection struct {
	ID        string `json:"id"`
	Toolkit   string `json:"toolkit"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// ConnectLinkResponse is the response from creating a connect link.
type ConnectLinkResponse struct {
	URL string `json:"url"`
}

// ToolToolkit is the nested toolkit object returned by the Composio API.
type ToolToolkit struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Logo string `json:"logo"`
}

// Tool represents a Composio tool definition.
type Tool struct {
	Slug        string                 `json:"slug"` // Action ID (e.g. "GMAIL_SEND_EMAIL") — used for execute calls
	Name        string                 `json:"name"` // Display name (e.g. "Send email")
	Description string                 `json:"description"`
	Toolkit     ToolToolkit            `json:"toolkit"`
	Parameters  map[string]interface{} `json:"input_parameters"` // JSON field is "input_parameters"
}

// ActionResult represents the result of executing a Composio action.
type ActionResult struct {
	Data       interface{} `json:"data"`
	Successful bool        `json:"successful"`
	Error      string      `json:"error,omitempty"`
}

// NewClient creates a new Composio API client.
// baseURL is typically "https://backend.composio.dev" for production.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SetHTTPClient replaces the default HTTP client. Used by tests to avoid real network listeners.
func (c *Client) SetHTTPClient(hc *http.Client) {
	if c != nil && hc != nil {
		c.httpClient = hc
	}
}

// Enabled returns true if the client has an API key configured.
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != ""
}

// ListConnections returns all connected accounts for a machine.
func (c *Client) ListConnections(ctx context.Context, machineID string) ([]Connection, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v3/connected_accounts?user_id="+machineID, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("composio: list connections: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.apiError("list connections", resp)
	}

	var result struct {
		Items []struct {
			ID        string `json:"id"`
			UserID    string `json:"user_id"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
			Toolkit   struct {
				Slug string `json:"slug"`
			} `json:"toolkit"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("composio: decode list response: %w", err)
	}

	conns := make([]Connection, 0, len(result.Items))
	for _, item := range result.Items {
		// Only return connections that belong to this machine.
		// CreateConnectLink stores our machineID as user_id in Composio.
		if item.UserID != machineID {
			continue
		}
		conns = append(conns, Connection{
			ID:        item.ID,
			Toolkit:   item.Toolkit.Slug,
			Status:    strings.ToLower(item.Status),
			CreatedAt: item.CreatedAt,
		})
	}
	return conns, nil
}

// CreateConnectLink generates an OAuth connect URL for a specific integration.
func (c *Client) CreateConnectLink(ctx context.Context, machineID, authConfigID, callbackURL string) (*ConnectLinkResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"user_id":        machineID,
		"auth_config_id": authConfigID,
		"callback_url":   callbackURL,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v3/connected_accounts/link",
		strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("composio: create connect link: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, c.apiError("create connect link", resp)
	}

	var result struct {
		RedirectURL string `json:"redirect_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("composio: decode connect link response: %w", err)
	}

	return &ConnectLinkResponse{URL: result.RedirectURL}, nil
}

// DeleteConnection removes a single connected account by ID.
func (c *Client) DeleteConnection(ctx context.Context, connectionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/api/v3/connected_accounts/"+connectionID, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("composio: delete connection: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return c.apiError("delete connection", resp)
	}
	return nil
}

// DeleteAllConnections removes all connected accounts for a machine (used on machine delete).
func (c *Client) DeleteAllConnections(ctx context.Context, machineID string) error {
	conns, err := c.ListConnections(ctx, machineID)
	if err != nil {
		return fmt.Errorf("composio: list for cleanup: %w", err)
	}
	for _, conn := range conns {
		if delErr := c.DeleteConnection(ctx, conn.ID); delErr != nil {
			return fmt.Errorf("composio: delete %s: %w", conn.ID, delErr)
		}
	}
	return nil
}

// ListTools returns available tools for a user, optionally filtered by toolkit.
func (c *Client) ListTools(ctx context.Context, userID, toolkitSlug string) ([]Tool, error) {
	u, _ := url.Parse(c.baseURL + "/api/v3/tools")
	q := u.Query()
	q.Set("user_id", userID)
	if toolkitSlug != "" {
		q.Set("toolkit_slug", toolkitSlug)
	}
	q.Set("limit", "100")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("composio: list tools: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.apiError("list tools", resp)
	}

	var result struct {
		Items      []Tool `json:"items"`
		TotalItems int    `json:"total_items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("composio: decode list tools response: %w", err)
	}
	return result.Items, nil
}

// ListToolsForConnected fetches tools only for toolkits the user has active connections for.
func (c *Client) ListToolsForConnected(ctx context.Context, userID string) ([]Tool, error) {
	conns, err := c.ListConnections(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("composio: list connections for tools: %w", err)
	}

	// Deduplicate toolkit slugs from active connections
	seen := make(map[string]bool)
	for _, conn := range conns {
		if conn.Status == "active" && conn.Toolkit != "" {
			seen[conn.Toolkit] = true
		}
	}
	if len(seen) == 0 {
		return nil, nil // no connected toolkits
	}

	// Fetch tools for each connected toolkit
	var allTools []Tool
	for slug := range seen {
		tools, err := c.ListTools(ctx, userID, slug)
		if err != nil {
			return nil, fmt.Errorf("composio: list tools for %s: %w", slug, err)
		}
		allTools = append(allTools, tools...)
	}
	return allTools, nil
}

// ExecuteAction executes a Composio action by name for a given user.
// Uses the v3 API which resolves the connected account via user_id
// (matching how CreateConnectLink stores the connection).
func (c *Client) ExecuteAction(ctx context.Context, actionName, userID, appName string, params map[string]interface{}) (*ActionResult, error) {
	bodyMap := map[string]interface{}{
		"user_id": userID,
	}
	if params != nil {
		bodyMap["arguments"] = params
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v3/tools/execute/"+url.PathEscape(actionName),
		bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("composio: execute action: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, c.apiError("execute action", resp)
	}

	var result ActionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("composio: decode execute action response: %w", err)
	}
	return &result, nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")
}

func (c *Client) apiError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("composio: %s: HTTP %d: %s", op, resp.StatusCode, string(body))
}
