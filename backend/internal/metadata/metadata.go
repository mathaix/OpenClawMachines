package metadata

import (
	"crypto/subtle"
	"log/slog"
	"sync"
	"time"
)

// Server provides a metadata service for MicroVMs, similar to cloud metadata services.
// MicroVMs call http://169.254.169.253/v1/instance to get their config.
// Binds to the bridge gateway IP on port 80.
type Server struct {
	BindAddr string
	Port     int

	mu             sync.RWMutex
	configs        map[string]MachineConfig // keyed by VM IP
	configVersions map[string]int           // keyed by VM IP, incremented on config update
	browserTargets map[string]string        // keyed by VM IP, CDP target override

	LogCallback func(machineID, source, line string)

	BackendURL   string // backend API URL for admin proxy
	AgentToken   string // for authenticating to backend API
	AgentVersion string // version string exposed to VMs via /v1/machine and /health

	SecretFetcher SecretFetcher // pulls secrets from backend on cache miss
}

// CredentialEntry holds an API key value along with its credential type.
// The credential type determines how the key is injected into upstream requests
// (e.g., Anthropic subscription_key uses Bearer auth, api_key uses x-api-key).
type CredentialEntry struct {
	Value          string     `json:"value"`
	CredentialType string     `json:"credential_type"` // api_key, subscription_key, token, oauth
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// SecretCacheTTL is how long pulled secrets are cached before re-fetching.
const SecretCacheTTL = 60 * time.Second

// SecretFetcher pulls secrets from the backend API on cache miss.
type SecretFetcher interface {
	FetchSecrets(machineID string) (map[string]string, error)
}

// SecretFetcherFunc adapts a plain function to the SecretFetcher interface.
type SecretFetcherFunc func(machineID string) (map[string]string, error)

func (f SecretFetcherFunc) FetchSecrets(machineID string) (map[string]string, error) {
	return f(machineID)
}

// SecretCacheEntry holds pulled secrets with a timestamp for TTL expiry.
type SecretCacheEntry struct {
	Secrets   map[string]string
	FetchedAt time.Time
}

// RuntimeSelection carries the resolved runtime information used at boot time.
type RuntimeSelection struct {
	Kind                      string `json:"kind,omitempty"`
	ResolvedRootfsVersion     string `json:"resolved_rootfs_version,omitempty"`
	ResolvedOpenClawVersion   string `json:"resolved_openclaw_version,omitempty"`
	ResolvedHermesVersion     string `json:"resolved_hermes_version,omitempty"`
	VersionSource             string `json:"version_source,omitempty"`
	RuntimeSource             string `json:"runtime_source,omitempty"` // Always "artifact"
	RootfsManifestURI         string `json:"rootfs_manifest_uri,omitempty"`
	OpenClawBin               string `json:"openclaw_bin,omitempty"`
	OpenClawBundledPluginsDir string `json:"openclaw_bundled_plugins_dir,omitempty"`
	OpenClawManifestURI       string `json:"openclaw_manifest_uri,omitempty"` // GCS manifest for artifact staging (set by control plane)
	HermesBin                 string `json:"hermes_bin,omitempty"`
	HermesVenvDir             string `json:"hermes_venv_dir,omitempty"`
	HermesManifestURI         string `json:"hermes_manifest_uri,omitempty"`
}

// MachineConfig is the metadata served to a MicroVM.
type MachineConfig struct {
	MachineID        string                     `json:"machine_id"`
	MachineKind      string                     `json:"machine_kind,omitempty"`
	MachineName      string                     `json:"machine_name"`
	MachineSlug      string                     `json:"machine_slug"`
	GatewayToken     string                     `json:"gateway_token"`
	OpenClawConf     []byte                     `json:"openclaw_config"`
	HermesConfigYAML []byte                     `json:"hermes_config_yaml,omitempty"`
	HermesEnv        []byte                     `json:"hermes_env,omitempty"`
	HermesAuthJSON   []byte                     `json:"hermes_auth_json,omitempty"`
	HermesSoul       []byte                     `json:"hermes_soul,omitempty"`
	LLMEndpoint      string                     `json:"llm_endpoint"`
	Secrets          map[string]string          `json:"-"`
	LLMKeys          map[string]CredentialEntry `json:"-"`
	Nonce            string                     `json:"-"` // not exposed via metadata API; used for request authentication
	AccountID        int                        `json:"-"`
	SigningKey       string                     `json:"signing_key,omitempty"`
	VmHostname       string                     `json:"vm_hostname,omitempty"`
	TunnelToken      string                     `json:"tunnel_token,omitempty"`
	OwnerEmails      []string                   `json:"-"` // emails allowed for SSH cert validation
	CfCaPubKey       string                     `json:"-"` // CF Access SSH CA public key

	CustomProviders  []CustomProviderConfig `json:"-"` // custom providers from account settings
	Souls            []SoulEntry            `json:"-"` // served via /v1/souls, not /v1/config
	SecretCache      SecretCacheEntry       `json:"-"` // pull-through cache for channel secrets
	RuntimeSelection *RuntimeSelection      `json:"-"` // boot-time runtime resolution for the guest
}

// SoulEntry carries agent soul (personality) data for cold-start delivery.
type SoulEntry struct {
	AgentID string `json:"agent_id"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// CustomProviderConfig defines a user-registered custom API provider.
type CustomProviderConfig struct {
	Name         string   `json:"name"`
	UpstreamHost string   `json:"upstream_host"`
	Scheme       string   `json:"scheme"`
	AuthMethod   string   `json:"auth_method"` // bearer_header, api_key_header, query_param, none
	AuthHeader   string   `json:"auth_header"` // custom header name (optional)
	PathPrefix   string   `json:"path_prefix"`
	AllowedHosts []string `json:"allowed_hosts"`
	IsLLM        bool     `json:"is_llm"`
}

func New(bindAddr string, port int) *Server {
	return &Server{
		BindAddr:       bindAddr,
		Port:           port,
		configs:        make(map[string]MachineConfig),
		configVersions: make(map[string]int),
		browserTargets: make(map[string]string),
	}
}

// DefaultServer returns the standard metadata server config.
// Binds to the link-local metadata address on port 80, same pattern as cloud providers.
func DefaultServer() *Server {
	return New("169.254.169.253", 80)
}

// RegisterMachine registers metadata for a MicroVM by its IP.
func (s *Server) RegisterMachine(vmIP string, config MachineConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[vmIP] = config
	s.configVersions[vmIP] = 1
	slog.Info("metadata.registered",
		"vm_ip", vmIP,
		"machine_id", config.MachineID,
		"nonce_prefix", truncate(config.Nonce, 8),
		"has_llm_keys", len(config.LLMKeys) > 0,
		"llm_key_count", len(config.LLMKeys),
	)
}

// UnregisterMachine zeros sensitive fields and removes metadata for a MicroVM.
func (s *Server) UnregisterMachine(vmIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg, ok := s.configs[vmIP]; ok {
		cfg.LLMKeys = nil
		cfg.Secrets = nil
		cfg.CustomProviders = nil
		cfg.GatewayToken = ""
		cfg.Nonce = ""
		s.configs[vmIP] = cfg
	}
	delete(s.browserTargets, vmIP)
	delete(s.configs, vmIP)
	delete(s.configVersions, vmIP)
	slog.Info("metadata.unregistered", "vm_ip", vmIP)
}

// GetBrowserTarget returns the CDP target for a VM.
// Returns the override target set via SetBrowserTarget, or empty string if none is set.
func (s *Server) GetBrowserTarget(vmIP string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.browserTargets[vmIP]
}

// SetBrowserTarget overrides the CDP target for a VM.
func (s *Server) SetBrowserTarget(vmIP, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.browserTargets == nil {
		s.browserTargets = make(map[string]string)
	}
	s.browserTargets[vmIP] = target
	slog.Info("metadata.browser_target.set", "vm_ip", vmIP, "target", target)
}

// ResetBrowserTarget removes the CDP target override for a VM.
func (s *Server) ResetBrowserTarget(vmIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.browserTargets == nil {
		return
	}
	delete(s.browserTargets, vmIP)
	slog.Info("metadata.browser_target.reset", "vm_ip", vmIP)
}

// UpdateMachineSecrets updates the secrets for a running VM and bumps the config version.
func (s *Server) UpdateMachineSecrets(vmIP string, secrets map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, ok := s.configs[vmIP]
	if !ok {
		slog.Warn("metadata.secrets.unknown_vm", "vm_ip", vmIP)
		return
	}
	cfg.Secrets = secrets
	s.configs[vmIP] = cfg
	s.configVersions[vmIP]++
	slog.Info("metadata.secrets.updated",
		"vm_ip", vmIP,
		"machine_id", cfg.MachineID,
		"key_count", len(secrets),
		"version", s.configVersions[vmIP])
}

// GetConfig returns the config for a VM by its source IP.
func (s *Server) GetConfig(vmIP string) (MachineConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.configs[vmIP]
	return cfg, ok
}

// GetConfigWithNonce returns the config for a VM by its source IP,
// but only if the provided nonce matches the registered nonce.
// Uses constant-time comparison to prevent timing side-channels.
func (s *Server) GetConfigWithNonce(vmIP, nonce string) (MachineConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, ok := s.configs[vmIP]
	if !ok {
		// Log all registered IPs so we can see what the server knows about.
		registered := make([]string, 0, len(s.configs))
		for ip := range s.configs {
			registered = append(registered, ip)
		}
		slog.Warn("metadata.vm_not_found",
			"vm_ip", vmIP,
			"registered_ips", registered,
			"total_vms", len(s.configs),
		)
		return MachineConfig{}, false
	}

	// If the VM has a nonce registered, the caller must provide the correct one.
	if cfg.Nonce != "" {
		if nonce == "" || subtle.ConstantTimeCompare([]byte(nonce), []byte(cfg.Nonce)) != 1 {
			slog.Warn("metadata.nonce_mismatch",
				"machine_id", cfg.MachineID,
				"vm_ip", vmIP,
				"provided_nonce_prefix", truncate(nonce, 8),
				"registered_nonce_prefix", truncate(cfg.Nonce, 8),
			)
			return MachineConfig{}, false
		}
	}

	return cfg, true
}

// UpdateMachineConfig updates the OpenClaw config for a running VM and bumps the config version.
func (s *Server) UpdateMachineConfig(vmIP string, openClawConf []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, ok := s.configs[vmIP]
	if !ok {
		slog.Warn("metadata.config.unknown_vm", "vm_ip", vmIP)
		return
	}
	cfg.OpenClawConf = openClawConf
	s.configs[vmIP] = cfg
	s.configVersions[vmIP]++
	slog.Info("metadata.config.updated",
		"vm_ip", vmIP,
		"machine_id", cfg.MachineID,
		"config_bytes", len(openClawConf),
		"version", s.configVersions[vmIP])
}

// UpdateMachineLLMKeys updates the LLM credentials for a running VM.
// Unlike secrets/config updates, this does NOT bump the config version because
// the gateway doesn't need to restart for fresh API keys.
func (s *Server) UpdateMachineLLMKeys(vmIP string, llmKeys map[string]CredentialEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, ok := s.configs[vmIP]
	if !ok {
		slog.Warn("metadata.llmkeys.unknown_vm", "vm_ip", vmIP)
		return
	}
	// Merge into existing keys rather than replacing. This preserves platform
	// keys (e.g. nebius) that were set at boot and aren't in the user credential
	// tables. Only user-provided keys are updated.
	if cfg.LLMKeys == nil {
		cfg.LLMKeys = make(map[string]CredentialEntry)
	}
	for k, v := range llmKeys {
		cfg.LLMKeys[k] = v
	}
	s.configs[vmIP] = cfg
	slog.Info("metadata.llmkeys.updated",
		"vm_ip", vmIP,
		"machine_id", cfg.MachineID,
		"key_count", len(cfg.LLMKeys))
}

// ReplaceMachineLLMKeys replaces the entire LLM credentials map for a running VM.
// Unlike UpdateMachineLLMKeys (which merges), this does a full replace — any key
// not in the new map is removed. Used when the backend pushes the authoritative
// credential set after a credential is deleted.
func (s *Server) ReplaceMachineLLMKeys(vmIP string, llmKeys map[string]CredentialEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, ok := s.configs[vmIP]
	if !ok {
		slog.Warn("metadata.llmkeys.replace.unknown_vm", "vm_ip", vmIP)
		return
	}

	// Log what's changing
	oldProviders := make([]string, 0, len(cfg.LLMKeys))
	for k := range cfg.LLMKeys {
		oldProviders = append(oldProviders, k)
	}
	newProviders := make([]string, 0, len(llmKeys))
	for k := range llmKeys {
		newProviders = append(newProviders, k)
	}

	cfg.LLMKeys = llmKeys
	s.configs[vmIP] = cfg
	slog.Info("metadata.llmkeys.replaced",
		"vm_ip", vmIP,
		"machine_id", cfg.MachineID,
		"old_key_count", len(oldProviders),
		"new_key_count", len(newProviders),
		"old_providers", oldProviders,
		"new_providers", newProviders)
}

// GetConfigVersion returns the config version for a VM. Returns 0 if unknown.
func (s *Server) GetConfigVersion(vmIP string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configVersions[vmIP]
}

// truncate returns at most the first n characters of s, for safe logging of secrets.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
