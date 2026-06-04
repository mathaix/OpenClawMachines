package orchestrator

import (
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/rootfs"
)

// FirecrackerConfig holds the global Firecracker configuration for the host.
type FirecrackerConfig struct {
	// BridgeName is the name of the network bridge (e.g., "ocm-br0").
	BridgeName string

	// SocketDir is the directory for Firecracker API sockets.
	SocketDir string

	// RootfsDir is the directory containing base rootfs images.
	RootfsDir string

	// StateDir is the directory for VM state (rootfs copies, state file).
	StateDir string

	// BrowserStateDir is the directory for browser VM state and rootfs copies.
	// Empty means use StateDir for backward compatibility.
	BrowserStateDir string

	// DataDir is the directory for persistent data volumes.
	DataDir string

	// RuntimeStateDir is the host-side root for per-machine runtime pointer state.
	// Expected layout: <dir>/machines/<machine-id>/openclaw/{selected_version,resolved_version,current,previous}
	RuntimeStateDir string

	// OpenClawRuntimeDir is the host-side cache root for versioned OpenClaw runtimes.
	// Expected layout: <dir>/releases/<version>/...
	OpenClawRuntimeDir string

	// OpenClawManifestURI points to the OpenClaw release manifest base.
	// Supports gs:// URIs in production and file paths/URIs in tests.
	OpenClawManifestURI string

	// KernelPath is the path to the uncompressed Linux kernel.
	KernelPath string

	// DefaultVCPUs is the default number of vCPUs per VM.
	DefaultVCPUs int

	// DefaultMemoryMB is the default memory in MB per VM.
	DefaultMemoryMB int

	// GCS rootfs distribution (empty = use embedded rootfs from boot disk)
	GCSRootfsManifest  string        // GCS manifest URI
	GCSDownloadTimeout time.Duration // download timeout
	GCSRetryAttempts   int           // retry count

	// GCS browser rootfs distribution (empty = no browser companion VM support)
	GCSBrowserRootfsManifest string // GCS manifest URI for browser rootfs
	GCSBrowserRootfsVersion  string // optional release version pin for browser rootfs

	// AllowKernelBrowserFullCopy permits the experimental Kernel Images browser
	// rootfs to fall back to a full disk copy when reflink is unavailable.
	// Default false because that path has known cold-start cost on ext4.
	AllowKernelBrowserFullCopy bool

	// HostExternalIP is the public IP address of this host. Used to configure
	// Neko WebRTC NAT1TO1 so the browser VM can advertise a reachable address.
	// Empty means browser VM live view will fall back to best-effort WebRTC.
	HostExternalIP string

	// RuntimeOwnerKind selects who owns the Firecracker process lifecycle.
	// Supported values:
	// - "systemd-unit": agent launches Firecracker in a per-VM systemd unit (default for new hosts)
	// - "direct": agent launches Firecracker directly (legacy fallback / compatibility mode)
	RuntimeOwnerKind string

	// OpenClaw runtime artifact distribution (empty = local cache only)
	OpenClawDownloadTimeout time.Duration
	OpenClawRetryAttempts   int

	// Hermes runtime artifact distribution (empty = local cache only)
	HermesRuntimeDir      string
	HermesManifestURI     string
	HermesDownloadTimeout time.Duration
	HermesRetryAttempts   int

	// RootfsLock protects against concurrent rootfs refresh and VM create races.
	// If nil, no locking is performed.
	RootfsLock *rootfs.RootfsLock
}

// VMConfig is the configuration for creating a single VM.
type VMConfig struct {
	MachineID        string
	MachineKind      string
	MachineName      string
	MachineSlug      string
	VCPUs            int
	MemoryMB         int
	VMIP             string
	GatewayToken     string // used for OpenClaw gateway authentication
	ProxyToken       string // per-machine token for proxy API auth (browser/terminal/logs)
	MetadataNonce    string // per-VM nonce for metadata server authentication
	OpenClawConf     []byte
	HermesConfigYAML []byte
	HermesEnv        []byte
	HermesAuthJSON   []byte
	HermesSoul       []byte
	LLMEndpoint      string
	Secrets          map[string]string
	LLMKeys          map[string]metadata.CredentialEntry
	AccountID        int
	BudgetMicrocents *int64
	KernelExtraArgs  string // additional kernel cmdline args (e.g. "ocm_quick_start=1")
	DataVolumeGB     int
	DataVersion      int
	SigningKey       string                          // per-VM signing key for machine tokens
	TunnelToken      string                          // cloudflared tunnel token
	VmHostname       string                          // public hostname for per-VM tunnel
	OwnerEmails      []string                        // emails allowed for SSH cert validation
	CfCaPubKey       string                          // CF Access SSH CA public key
	CustomProviders  []metadata.CustomProviderConfig // custom providers from account settings
	Souls            []metadata.SoulEntry            // agent soul files for cold-start delivery
	RuntimeSelection *metadata.RuntimeSelection      // resolved guest runtime selection
}

// BrowserVMConfig is the configuration for a standalone browser VM.
type BrowserVMConfig struct {
	BrowserVMID    string
	VMIP           string
	VCPUs          int
	MemoryMB       int
	RootfsManifest string
	RootfsVersion  string
}

// VMInstance represents a running Firecracker MicroVM.
type VMInstance struct {
	MachineID        string    `json:"machine_id"`
	MachineSlug      string    `json:"machine_slug"`
	VMIP             string    `json:"vm_ip"`
	TapDevice        string    `json:"tap_device"`
	SocketPath       string    `json:"socket_path"`
	RootfsPath       string    `json:"rootfs_path"`
	RootfsVersion    string    `json:"rootfs_version,omitempty"`
	OpenClawVersion  string    `json:"openclaw_version,omitempty"`
	PID              int       `json:"pid"`
	VCPUs            int       `json:"vcpus"`
	MemoryMB         int       `json:"memory_mb"`
	ProxyToken       string    `json:"proxy_token"`   // per-machine token for proxy API auth
	GatewayToken     string    `json:"gateway_token"` // per-machine token for gateway auth
	SigningKey       string    `json:"-"`             // per-VM signing key (not exposed in JSON API)
	RuntimeOwnerKind string    `json:"-"`
	RuntimeOwnerRef  string    `json:"-"`
	Status           string    `json:"status"`           // "starting", "running", "stopping", "stopped", "error"
	ErrorMessage     string    `json:"error_message"`    // error details when Status == "error"
	DataVolumePath   string    `json:"data_volume_path"` // path to persistent data volume
	CreatedAt        time.Time `json:"created_at"`
}
