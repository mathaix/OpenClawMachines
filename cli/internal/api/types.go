package api

import (
	"encoding/json"
	"time"
)

// Machine represents an OpenClaw Machine instance.
type Machine struct {
	ID              string     `json:"id"`
	AccountID       int        `json:"account_id"`
	Kind            string     `json:"kind,omitempty"`
	Name            string     `json:"name"`
	Slug            string     `json:"slug"`
	Status          string     `json:"status"`
	StatusMessage   *string    `json:"status_message,omitempty"`
	VCPUs           int        `json:"vcpus"`
	MemoryMB        int        `json:"memory_mb"`
	TunnelHostname  *string    `json:"tunnel_hostname,omitempty"`
	CustomDomain    *string    `json:"custom_domain,omitempty"`
	DataVolumeGB    int        `json:"data_volume_gb"`
	RootfsSnapshot  *string    `json:"rootfs_snapshot,omitempty"`
	OpenclawVersion *string    `json:"openclaw_version,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	StoppedAt       *time.Time `json:"stopped_at,omitempty"`
	LastStartedAt   *time.Time `json:"last_started_at,omitempty"`
}

// MachineSize represents an available machine size tier.
type MachineSize struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	VCPUs        int    `json:"vcpus"`
	MemoryMB     int    `json:"memory_mb"`
	SwapMB       int    `json:"swap_mb"`
	DiskGB       int    `json:"disk_gb"`
	PriceCentsMo int    `json:"price_cents_mo"`
	Description  string `json:"description"`
	IsDefault    bool   `json:"is_default,omitempty"`
}

// Account represents an OCM account (team/organization).
type Account struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	Plan         string    `json:"plan"`
	BillingEmail *string   `json:"billing_email,omitempty"`
	CreatedBy    int       `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// Credential represents a machine-scoped API credential.
type Credential struct {
	ID             int        `json:"id"`
	AccountID      int        `json:"account_id"`
	MachineID      string     `json:"machine_id"`
	Provider       string     `json:"provider"`
	CredentialType string     `json:"credential_type"`
	Label          string     `json:"label"`
	LastValidated  *time.Time `json:"last_validated,omitempty"`
	LastFour       *string    `json:"last_four,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// User represents a user returned by /api/auth/me.
type User struct {
	ID    int    `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// RegistryEntry represents an entry in the component registry (channel, skill, or tool).
type RegistryEntry struct {
	ID                  string   `json:"id"`
	Type                string   `json:"type"`
	Name                string   `json:"name"`
	Description         *string  `json:"description,omitempty"`
	Version             string   `json:"version"`
	Tier                string   `json:"tier"`
	Status              string   `json:"status"`
	Modes               []string `json:"modes,omitempty"`
	RequiredCredentials []string `json:"required_credentials,omitempty"`
}

// MachineCapability represents an enabled capability on a machine.
type MachineCapability struct {
	ID              int              `json:"id"`
	MachineID       string           `json:"machine_id"`
	EntryID         string           `json:"entry_id"`
	Mode            *string          `json:"mode,omitempty"`
	Enabled         bool             `json:"enabled"`
	ConfigOverrides *json.RawMessage `json:"config_overrides,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	// Joined from registry_entries
	EntryName string `json:"entry_name,omitempty"`
	EntryType string `json:"entry_type,omitempty"`
}

// ProviderEntry represents a provider (built-in or custom) in the list response.
type ProviderEntry struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"` // "builtin" or "custom"
	UpstreamHost string   `json:"upstream_host,omitempty"`
	Scheme       string   `json:"scheme,omitempty"`
	AuthMethod   string   `json:"auth_method,omitempty"`
	AuthHeader   *string  `json:"auth_header,omitempty"`
	PathPrefix   string   `json:"path_prefix,omitempty"`
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
	IsLLM        bool     `json:"is_llm,omitempty"`
}

// CustomProvider represents a custom API provider registered by the user.
type CustomProvider struct {
	ID           string    `json:"id"`
	AccountID    int       `json:"account_id"`
	Name         string    `json:"name"`
	UpstreamHost string    `json:"upstream_host"`
	Scheme       string    `json:"scheme"`
	AuthMethod   string    `json:"auth_method"`
	AuthHeader   *string   `json:"auth_header,omitempty"`
	PathPrefix   string    `json:"path_prefix"`
	AllowedHosts []string  `json:"allowed_hosts,omitempty"`
	IsLLM        bool      `json:"is_llm"`
	CreatedAt    time.Time `json:"created_at"`
}

// SecretEntry represents a secret stored on a machine (key only, no value).
type SecretEntry struct {
	ID        int       `json:"id"`
	MachineID string    `json:"machine_id"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UsageRecord represents an LLM usage entry.
type UsageRecord struct {
	ID             int64     `json:"id"`
	AccountID      int       `json:"account_id"`
	MachineID      string    `json:"machine_id"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	CostMicrocents int64     `json:"cost_microcents"`
	RequestID      *string   `json:"request_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Backup represents a machine backup.
type Backup struct {
	ID              int       `json:"id"`
	MachineID       string    `json:"machine_id"`
	Timestamp       time.Time `json:"timestamp"`
	SizeBytes       int64     `json:"size_bytes"`
	CompressedBytes int64     `json:"compressed_bytes"`
	Trigger         string    `json:"trigger"`
	CreatedAt       time.Time `json:"created_at"`
}
