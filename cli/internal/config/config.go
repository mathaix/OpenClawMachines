package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds CLI configuration persisted to disk.
type Config struct {
	APIURL             string `json:"api_url"`
	CfAppDomain        string `json:"cf_app_domain,omitempty"` // deprecated: use DataPlaneDomain
	Token              string `json:"token"`
	TokenExpires       string `json:"token_expires"`
	DefaultAccountID   int    `json:"default_account_id"`
	DefaultAccountSlug string `json:"default_account_slug"`
	DefaultMachineID   string `json:"default_machine_id,omitempty"`
	DefaultMachineName string `json:"default_machine_name,omitempty"`

	// Platform config (fetched from API, cached locally)
	DataPlaneDomain    string `json:"data_plane_domain,omitempty"`
	AuthMode           string `json:"auth_mode,omitempty"`
	FrontendURL        string `json:"frontend_url,omitempty"`
	CfAccessAuthDomain string `json:"cf_access_auth_domain,omitempty"`
	PlatformConfigAt   string `json:"platform_config_at,omitempty"` // RFC3339 timestamp of last fetch
}

// GetDataPlaneDomain returns the data plane domain, falling back to the default.
func (c *Config) GetDataPlaneDomain() string {
	if c.DataPlaneDomain != "" {
		return c.DataPlaneDomain
	}
	return "openclawmachines.com"
}

// GetCfAccessAuthDomain returns the CF Access auth domain, falling back to the default.
func (c *Config) GetCfAccessAuthDomain() string {
	if c.CfAccessAuthDomain != "" {
		return c.CfAccessAuthDomain
	}
	return "openclawmachines.cloudflareaccess.com"
}

// GetFrontendURL returns the frontend URL from platform config, or empty if not yet fetched.
func (c *Config) GetFrontendURL() string {
	if c.FrontendURL != "" {
		return c.FrontendURL
	}
	return ""
}

// DefaultPath returns the default config file path (~/.config/ocm/config.json).
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".config", "ocm", "config.json"), nil
}

const DefaultAPIURL = "https://api.openclawmachines.com"

// Load reads the config from the given path. If path is empty, it uses the default.
// Returns a zero-value Config if the file does not exist.
func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{APIURL: DefaultAPIURL}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	// Migrate deprecated CfAppDomain → DataPlaneDomain
	if cfg.CfAppDomain != "" && cfg.DataPlaneDomain == "" {
		cfg.DataPlaneDomain = cfg.CfAppDomain
	}
	return &cfg, nil
}

// Save writes the config to the given path. If path is empty, it uses the default.
// Creates the parent directory if it does not exist.
func Save(cfg *Config, path string) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}
