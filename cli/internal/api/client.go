package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an HTTP client for the OCM API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	longClient *http.Client // for operations that may take longer (create, start, stop)
}

// NewClient creates a new API client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL:    baseURL,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		longClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// PostLong performs an HTTP POST with a longer timeout for operations
// like machine creation that involve provisioning.
func (c *Client) PostLong(path string, body []byte) (*http.Response, error) {
	return c.doWith(c.longClient, "POST", path, body)
}

// DeleteLong performs an HTTP DELETE with a longer timeout for operations
// like machine destruction that may take time.
func (c *Client) DeleteLong(path string) (*http.Response, error) {
	return c.doWith(c.longClient, "DELETE", path, nil)
}

// Get performs an HTTP GET request.
func (c *Client) Get(path string) (*http.Response, error) {
	return c.do("GET", path, nil)
}

// GetStream performs an HTTP GET request with no timeout, suitable for
// long-lived streaming connections (e.g. log following). The provided context
// controls cancellation; the caller should cancel it on Ctrl+C or when done.
func (c *Client) GetStream(ctx context.Context, path string) (*http.Response, error) {
	url := fmt.Sprintf("%s%s", c.baseURL, path)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	// Use a client with no timeout for streaming connections.
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return resp, nil
}

// Post performs an HTTP POST request with a JSON body.
func (c *Client) Post(path string, body []byte) (*http.Response, error) {
	return c.do("POST", path, body)
}

// Put performs an HTTP PUT request with a JSON body.
func (c *Client) Put(path string, body []byte) (*http.Response, error) {
	return c.do("PUT", path, body)
}

// Delete performs an HTTP DELETE request.
func (c *Client) Delete(path string) (*http.Response, error) {
	return c.do("DELETE", path, nil)
}

func (c *Client) do(method, path string, body []byte) (*http.Response, error) {
	return c.doWith(c.httpClient, method, path, body)
}

func (c *Client) doWith(client *http.Client, method, path string, body []byte) (*http.Response, error) {
	url := fmt.Sprintf("%s%s", c.baseURL, path)

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}
