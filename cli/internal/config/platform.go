package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const platformConfigTTL = 24 * time.Hour

// PlatformConfigStale returns true if platform config has never been fetched
// or was fetched more than 24 hours ago.
func (c *Config) PlatformConfigStale() bool {
	if c.PlatformConfigAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, c.PlatformConfigAt)
	if err != nil {
		return true
	}
	return time.Since(t) > platformConfigTTL
}

// platformConfigResponse mirrors the backend response.
type platformConfigResponse struct {
	DataPlaneDomain    string `json:"data_plane_domain"`
	AuthMode           string `json:"auth_mode"`
	FrontendURL        string `json:"frontend_url"`
	CfAccessAuthDomain string `json:"cf_access_auth_domain"`
}

// FetchPlatformConfig fetches platform config from the API and updates the
// config fields. Returns an error if the request fails; callers should treat
// errors as non-fatal and use cached values.
func FetchPlatformConfig(cfg *Config) error {
	url := cfg.APIURL + "/api/platform/config"
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetching platform config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("platform config: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading platform config: %w", err)
	}

	var pc platformConfigResponse
	if err := json.Unmarshal(body, &pc); err != nil {
		return fmt.Errorf("parsing platform config: %w", err)
	}

	if pc.DataPlaneDomain != "" {
		cfg.DataPlaneDomain = pc.DataPlaneDomain
	}
	if pc.AuthMode != "" {
		cfg.AuthMode = pc.AuthMode
	}
	if pc.FrontendURL != "" {
		cfg.FrontendURL = pc.FrontendURL
	}
	if pc.CfAccessAuthDomain != "" {
		cfg.CfAccessAuthDomain = pc.CfAccessAuthDomain
	}
	cfg.PlatformConfigAt = time.Now().UTC().Format(time.RFC3339)

	return nil
}
