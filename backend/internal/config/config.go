package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	ProfileLocal    = "local"
	ProfileOperator = "operator"
	ProfileHosted   = "hosted"

	defaultHermesManifestURI             = ""
	defaultHermesRootfsManifestURI       = ""
	StableBrowserRootfsManifestURI       = ""
	ExperimentalKernelBrowserManifestURI = ""
	StableKernelBrowserRootfsVersion     = ""
	DefaultBrowserRootfsManifestURI      = ExperimentalKernelBrowserManifestURI
	DefaultBrowserRootfsVersion          = StableKernelBrowserRootfsVersion
)

type Config struct {
	ControlPlaneProfile          string
	Port                         string
	DatabaseURL                  string
	JWTSecret                    string
	AgentToken                   string
	CORSOrigins                  string
	GCPProject                   string
	GCPZone                      string
	GCPRegion                    string
	LiteLLMMasterKey             string
	BackendURL                   string
	SnapshotName                 string
	SecretEncryptionKey          string
	CloudflareAccountID          string
	CloudflareKVNamespaceID      string
	CloudflareAPIToken           string
	CFServiceTokenID             string
	CFServiceTokenSecret         string
	CloudflareZoneID             string
	GCPSecretName                string
	DataDiskSizeGB               int64
	RootfsGCSManifest            string
	AgentGCSManifest             string
	BrowserRootfsGCSManifest     string
	BrowserRootfsVersion         string
	RootfsDataVersion            int
	AuthMode                     string
	CfAccessTeamDomain           string
	CfAccessAUD                  string
	DevUserEmail                 string
	CfSSHCAPubKey                string
	ProxyBaseURL                 string
	OAuthClientID                string
	OAuthClientSecret            string
	GCSServiceAccountKey         string
	BackupMasterKey              string
	BackupGCSBucket              string
	BackupGCSPrefix              string
	EnableDurableWorkflows       bool
	RunMode                      string // "api" (Cloud Run, enqueue-only) or "worker" (fleet, executor-only)
	ResendAPIKey                 string
	FrontendURL                  string
	ExecutorID                   string // unique worker ID
	NebiusAPIKey                 string // platform Nebius Token Factory key
	FirebaseProjectID            string // Firebase project ID for ID token verification
	DataPlaneDomain              string // domain for data-plane hostnames (e.g. "openclawmachines.com")
	SSHCAPrivateKey              string // platform SSH CA private key (signs SSH certs) (pushed to VMs)
	EnableRuntimeVersionResolver bool
	OpenClawManifestURI          string
	HermesManifestURI            string
	HermesRootfsManifestURI      string
}

func Load() (*Config, error) {
	profile, err := loadControlPlaneProfile()
	if err != nil {
		return nil, err
	}
	defaults := defaultsForProfile(profile)

	zone := getEnv("GCP_ZONE", defaults.GCPZone)

	// Derive region from zone by stripping the last "-X" suffix (e.g. "us-central1-b" → "us-central1")
	region := zone
	if idx := len(zone) - 2; idx > 0 && zone[idx] == '-' {
		region = zone[:idx]
	}

	cfg := &Config{
		ControlPlaneProfile:     profile,
		Port:                    getEnv("PORT", "8080"),
		DatabaseURL:             os.Getenv("DATABASE_URL"),
		JWTSecret:               os.Getenv("JWT_SECRET"),
		AgentToken:              os.Getenv("FC_AGENT_TOKEN"),
		CORSOrigins:             getEnv("CORS_ORIGINS", "http://localhost:5173"),
		GCPProject:              getEnv("GCP_PROJECT", defaults.GCPProject),
		GCPZone:                 zone,
		GCPRegion:               region,
		LiteLLMMasterKey:        os.Getenv("LITELLM_MASTER_KEY"),
		BackendURL:              os.Getenv("BACKEND_URL"),
		SnapshotName:            getEnv("FC_SNAPSHOT_NAME", os.Getenv("OCM_SNAPSHOT")),
		SecretEncryptionKey:     os.Getenv("SECRET_ENCRYPTION_KEY"),
		CloudflareAccountID:     os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
		CloudflareKVNamespaceID: os.Getenv("CLOUDFLARE_KV_NAMESPACE_ID"),
		CloudflareAPIToken:      os.Getenv("CLOUDFLARE_API_TOKEN"),
		CFServiceTokenID:        os.Getenv("CF_SERVICE_TOKEN_ID"),
		CFServiceTokenSecret:    os.Getenv("CF_SERVICE_TOKEN_SECRET"),
		CloudflareZoneID:        os.Getenv("CLOUDFLARE_ZONE_ID"),
		GCPSecretName:           os.Getenv("GCP_SECRET_NAME"),
	}

	cfg.DataDiskSizeGB = int64(getEnvInt("DATA_DISK_SIZE_GB", 50))
	cfg.RootfsGCSManifest = os.Getenv("ROOTFS_GCS_MANIFEST")
	cfg.AgentGCSManifest = os.Getenv("AGENT_GCS_MANIFEST")
	// Preserve the "empty means disabled" contract: explicit
	// BROWSER_ROOTFS_GCS_MANIFEST="" keeps browser VM support off. An unset
	// env uses the hosted rootfs default only in CONTROL_PLANE_PROFILE=hosted.
	cfg.BrowserRootfsGCSManifest = getEnvOrDefault("BROWSER_ROOTFS_GCS_MANIFEST", defaults.BrowserRootfsManifestURI)
	cfg.BrowserRootfsVersion = getBrowserRootfsVersionOrDefault(cfg.BrowserRootfsGCSManifest)
	cfg.RootfsDataVersion = getEnvInt("ROOTFS_DATA_VERSION", 1)

	cfg.AuthMode = getEnv("AUTH_MODE", defaults.AuthMode)
	cfg.CfAccessTeamDomain = strings.TrimSpace(os.Getenv("CF_ACCESS_TEAM_DOMAIN"))
	cfg.CfAccessAUD = strings.TrimSpace(os.Getenv("CF_ACCESS_AUD"))
	cfg.DevUserEmail = getEnv("DEV_USER_EMAIL", "dev@localhost")
	cfg.CfSSHCAPubKey = os.Getenv("CF_SSH_CA_PUBKEY")
	cfg.ProxyBaseURL = getEnv("OCM_PROXY_BASE_URL", "http://192.168.100.1:4000")
	cfg.OAuthClientID = os.Getenv("OAUTH_CLIENT_ID")
	cfg.OAuthClientSecret = os.Getenv("OAUTH_CLIENT_SECRET")
	cfg.GCSServiceAccountKey = os.Getenv("GCS_SERVICE_ACCOUNT_KEY")
	cfg.BackupMasterKey = os.Getenv("BACKUP_MASTER_KEY")
	cfg.BackupGCSBucket = getEnv("BACKUP_GCS_BUCKET", defaults.BackupGCSBucket)
	cfg.BackupGCSPrefix = getEnv("BACKUP_GCS_PREFIX", "backups")
	cfg.EnableDurableWorkflows = getEnv("ENABLE_DURABLE_WORKFLOWS", "") == "1"
	cfg.RunMode = getEnv("RUN_MODE", "")
	cfg.ResendAPIKey = os.Getenv("RESEND_API_KEY")
	cfg.FrontendURL = getEnv("FRONTEND_URL", "http://localhost:5173")
	cfg.ExecutorID = getEnv("EXECUTOR_ID", "")
	cfg.NebiusAPIKey = os.Getenv("NEBIUS_API_KEY")
	cfg.FirebaseProjectID = os.Getenv("FIREBASE_PROJECT_ID")
	cfg.DataPlaneDomain = getEnv("DATA_PLANE_DOMAIN", defaults.DataPlaneDomain)
	cfg.SSHCAPrivateKey = os.Getenv("OCM_SSH_CA_PRIVATE_KEY")
	cfg.EnableRuntimeVersionResolver = getEnv("FF_RUNTIME_VERSION_RESOLVER", "") == "1"
	cfg.OpenClawManifestURI = os.Getenv("OPENCLAW_GCS_MANIFEST")
	cfg.HermesManifestURI = getEnv("HERMES_GCS_MANIFEST", defaults.HermesManifestURI)
	cfg.HermesRootfsManifestURI = getEnv("HERMES_ROOTFS_GCS_MANIFEST", defaults.HermesRootfsManifestURI)
	if cfg.ExecutorID == "" {
		hostname, _ := os.Hostname()
		cfg.ExecutorID = hostname
	}

	if cfg.ControlPlaneProfile == ProfileOperator && cfg.AuthMode == "" {
		return nil, fmt.Errorf("AUTH_MODE is required when CONTROL_PLANE_PROFILE=operator")
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	return cfg, nil
}

type profileDefaults struct {
	AuthMode                 string
	GCPProject               string
	GCPZone                  string
	DataPlaneDomain          string
	BackupGCSBucket          string
	BrowserRootfsManifestURI string
	HermesManifestURI        string
	HermesRootfsManifestURI  string
}

func defaultsForProfile(profile string) profileDefaults {
	switch profile {
	case ProfileHosted:
		return profileDefaults{
			AuthMode: "cfaccess",
		}
	case ProfileOperator:
		return profileDefaults{}
	default:
		return profileDefaults{
			AuthMode:        "dev",
			DataPlaneDomain: "localhost",
		}
	}
}

func loadControlPlaneProfile() (string, error) {
	profile := strings.ToLower(strings.TrimSpace(getEnv("CONTROL_PLANE_PROFILE", ProfileLocal)))
	switch profile {
	case ProfileLocal, ProfileOperator, ProfileHosted:
		return profile, nil
	default:
		return "", fmt.Errorf("invalid CONTROL_PLANE_PROFILE %q (valid: %s, %s, %s)", profile, ProfileLocal, ProfileOperator, ProfileHosted)
	}
}

func (c *Config) RequiresHostedIntegrations() bool {
	return c.ControlPlaneProfile == ProfileHosted
}

func (c *Config) CloudflareTunnelConfigured() bool {
	return c.CloudflareAPIToken != "" && c.CloudflareAccountID != "" && c.CloudflareZoneID != ""
}

func (c *Config) CloudflareKVConfigured() bool {
	return c.CloudflareAPIToken != "" && c.CloudflareAccountID != "" && c.CloudflareKVNamespaceID != ""
}

func (c *Config) HasCloudflareConfig() bool {
	return c.CloudflareAPIToken != "" ||
		c.CloudflareAccountID != "" ||
		c.CloudflareZoneID != "" ||
		c.CloudflareKVNamespaceID != ""
}

// AgentConfig is the config for the Worker Agent process.
type AgentConfig struct {
	ControlPort string
	ProxyPort   string
	AgentToken  string

	// Network
	BridgeName string
	BridgeIP   string

	// Paths
	ImagesDir           string
	StateDir            string
	BrowserStateDir     string
	SocketDir           string
	DataDir             string
	RuntimeStateDir     string
	OpenClawRuntimeDir  string
	OpenClawManifestURI string
	HermesRuntimeDir    string
	HermesManifestURI   string
	KernelPath          string

	// VM defaults
	VCPUs    int
	MemoryMB int

	// Backend connection
	BackendURL string
	HostID     string // host DB ID (from GCP metadata)

	// Cloudflare Tunnel token (prefetched from GCP metadata)
	TunnelToken string

	// CIDR allowlist for the control API (comma-separated, empty = allow all)
	ControlAllowedCIDRs string

	// GCS rootfs distribution (empty = use embedded rootfs from boot disk)
	RootfsGCSManifest     string // ROOTFS_GCS_MANIFEST - GCS URI
	RootfsDownloadTimeout int    // ROOTFS_DOWNLOAD_TIMEOUT - seconds (default 600)
	RootfsRetryAttempts   int    // ROOTFS_RETRY_ATTEMPTS (default 3)

	// GCS browser rootfs distribution (empty = no browser companion VM support)
	BrowserRootfsGCSManifest   string // BROWSER_ROOTFS_GCS_MANIFEST - GCS URI
	BrowserRootfsVersion       string // BROWSER_ROOTFS_VERSION - optional release version pin
	AllowKernelBrowserFullCopy bool   // OCM_ALLOW_KERNEL_BROWSER_FULL_COPY - unsafe canary escape hatch

	// OpenClaw runtime artifact distribution (empty = local cache only)
	OpenClawDownloadTimeout int // OPENCLAW_DOWNLOAD_TIMEOUT - seconds (default 300)
	OpenClawRetryAttempts   int // OPENCLAW_RETRY_ATTEMPTS (default 3)
	HermesDownloadTimeout   int // HERMES_DOWNLOAD_TIMEOUT - seconds (default 300)
	HermesRetryAttempts     int // HERMES_RETRY_ATTEMPTS (default 3)

	// VM runtime owner (default "direct", optional "systemd-unit")
	RuntimeOwnerKind string // VM_RUNTIME_OWNER

	// Agent self-update (empty = disabled). Updates are driven by the
	// startup check and the admin-triggered /trigger-update endpoint —
	// there is no background polling.
	AgentGCSManifest string // AGENT_GCS_MANIFEST - GCS URI for agent manifest

	// Agent endpoint (reported in heartbeat so control plane knows how to reach us)
	AgentEndpoint string // AGENT_ENDPOINT - e.g. "http://203.0.113.10:9090"

	// Backup
	BackupGCSBucket      string // BACKUP_GCS_BUCKET (empty = disabled)
	BackupGCSPrefix      string // BACKUP_GCS_PREFIX (default "backups")
	BackupMaxVolumeGB    int    // BACKUP_MAX_VOLUME_GB (default 10)
	BackupMasterKey      string // BACKUP_MASTER_KEY - 32-byte hex-encoded platform master key
	GCSServiceAccountKey string // GCS_SERVICE_ACCOUNT_KEY - JSON key for non-GCP hosts
}

func LoadAgent() (*AgentConfig, error) {
	stateDir := getEnv("STATE_DIR", "/var/lib/ocm/vms")
	browserRootfsManifest := getEnvOrDefault("BROWSER_ROOTFS_GCS_MANIFEST", "")
	return &AgentConfig{
		ControlPort: getEnv("CONTROL_PORT", "9090"),
		ProxyPort:   getEnv("PROXY_PORT", "9091"),
		AgentToken:  os.Getenv("FC_AGENT_TOKEN"),

		BridgeName: getEnv("BRIDGE_NAME", "ocm-br0"),
		BridgeIP:   getEnv("BRIDGE_IP", "192.168.100.1/24"),

		ImagesDir:           getEnv("IMAGES_DIR", "/var/lib/ocm/images"),
		StateDir:            stateDir,
		BrowserStateDir:     getEnv("BROWSER_STATE_DIR", stateDir),
		SocketDir:           getEnv("SOCKET_DIR", "/var/lib/ocm/sockets"),
		DataDir:             getEnv("DATA_DIR", "/var/lib/ocm/data"),
		RuntimeStateDir:     getEnv("RUNTIME_STATE_DIR", "/var/lib/ocm/runtime"),
		OpenClawRuntimeDir:  getEnv("OPENCLAW_RUNTIME_DIR", "/var/lib/ocm/openclaw"),
		OpenClawManifestURI: os.Getenv("OPENCLAW_GCS_MANIFEST"),
		HermesRuntimeDir:    getEnv("HERMES_RUNTIME_DIR", "/var/lib/ocm/hermes"),
		HermesManifestURI:   os.Getenv("HERMES_GCS_MANIFEST"),
		KernelPath:          getEnv("KERNEL_PATH", "/var/lib/ocm/vmlinux"),

		VCPUs:    getEnvInt("DEFAULT_VCPUS", 2),
		MemoryMB: getEnvInt("DEFAULT_MEMORY_MB", 3072),

		BackendURL:          os.Getenv("BACKEND_URL"),
		HostID:              os.Getenv("HOST_ID"),
		ControlAllowedCIDRs: os.Getenv("CONTROL_ALLOWED_CIDRS"),

		RootfsGCSManifest:     os.Getenv("ROOTFS_GCS_MANIFEST"),
		RootfsDownloadTimeout: getEnvInt("ROOTFS_DOWNLOAD_TIMEOUT", 600),
		RootfsRetryAttempts:   getEnvInt("ROOTFS_RETRY_ATTEMPTS", 3),

		BrowserRootfsGCSManifest:   browserRootfsManifest,
		BrowserRootfsVersion:       getBrowserRootfsVersionOrDefault(browserRootfsManifest),
		AllowKernelBrowserFullCopy: getEnvBool("OCM_ALLOW_KERNEL_BROWSER_FULL_COPY", false),
		OpenClawDownloadTimeout:    getEnvInt("OPENCLAW_DOWNLOAD_TIMEOUT", 300),
		OpenClawRetryAttempts:      getEnvInt("OPENCLAW_RETRY_ATTEMPTS", 3),
		HermesDownloadTimeout:      getEnvInt("HERMES_DOWNLOAD_TIMEOUT", 300),
		HermesRetryAttempts:        getEnvInt("HERMES_RETRY_ATTEMPTS", 3),
		RuntimeOwnerKind:           getEnv("VM_RUNTIME_OWNER", "systemd-unit"),

		AgentGCSManifest: os.Getenv("AGENT_GCS_MANIFEST"),

		AgentEndpoint: os.Getenv("AGENT_ENDPOINT"),

		BackupGCSBucket:      os.Getenv("BACKUP_GCS_BUCKET"),
		BackupGCSPrefix:      getEnv("BACKUP_GCS_PREFIX", "backups"),
		BackupMaxVolumeGB:    getEnvInt("BACKUP_MAX_VOLUME_GB", 10),
		BackupMasterKey:      os.Getenv("BACKUP_MASTER_KEY"),
		GCSServiceAccountKey: os.Getenv("GCS_SERVICE_ACCOUNT_KEY"),
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getEnvOrDefault returns the env var's value when the var is SET (even to
// an empty string), or the fallback when the var is unset. It differs from
// getEnv by preserving explicit empty values as a meaningful signal. Used
// for BROWSER_ROOTFS_GCS_MANIFEST where "" means "browser VM support is
// disabled" and must not be silently overwritten with the default.
func getEnvOrDefault(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

func getBrowserRootfsVersionOrDefault(manifest string) string {
	if v, ok := os.LookupEnv("BROWSER_ROOTFS_VERSION"); ok {
		return strings.TrimSpace(v)
	}
	if ExperimentalKernelBrowserManifestURI != "" && strings.TrimSpace(manifest) == ExperimentalKernelBrowserManifestURI {
		return DefaultBrowserRootfsVersion
	}
	return ""
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off", "":
		return false
	default:
		return fallback
	}
}
