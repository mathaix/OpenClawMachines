package store

import (
	"context"
	"encoding/json"
	"time"
)

// ---- Entity types ----

type User struct {
	ID              int       `json:"id"`
	Email           string    `json:"email"`
	Name            string    `json:"name"`
	AvatarURL       *string   `json:"avatar_url,omitempty"`
	AuthProvider    string    `json:"auth_provider"`
	AuthProviderID  string    `json:"auth_provider_id"`
	CfSub           string    `json:"cf_sub,omitempty"`
	IdentityIssuer  *string   `json:"identity_issuer,omitempty"`
	IdentitySubject *string   `json:"identity_subject,omitempty"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

type Account struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedBy int       `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type AccountMember struct {
	AccountID int       `json:"account_id"`
	UserID    int       `json:"user_id"`
	Role      string    `json:"role"`
	Email     string    `json:"email,omitempty"`
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type AccountInvitation struct {
	ID          int        `json:"id"`
	AccountID   int        `json:"account_id"`
	AccountName string     `json:"account_name,omitempty"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Token       string     `json:"-"`
	Status      string     `json:"status"`
	InvitedBy   int        `json:"invited_by"`
	AcceptedBy  *int       `json:"accepted_by,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	RespondedAt *time.Time `json:"responded_at,omitempty"`
}

type Machine struct {
	ID                      string     `json:"id"`
	AccountID               int        `json:"account_id"`
	Kind                    string     `json:"kind"`
	Name                    string     `json:"name"`
	Slug                    string     `json:"slug"`
	PreferredRegion         *string    `json:"preferred_region,omitempty"`
	Status                  string     `json:"status"`
	StatusMessage           *string    `json:"status_message,omitempty"`
	VCPUs                   int        `json:"vcpus"`
	MemoryMB                int        `json:"memory_mb"`
	HostID                  *int       `json:"host_id,omitempty"`
	VMIP                    *string    `json:"vm_ip,omitempty"`
	TunnelHostname          *string    `json:"tunnel_hostname,omitempty"`
	CustomDomain            *string    `json:"custom_domain,omitempty"`
	GatewayToken            *string    `json:"gateway_token,omitempty"`
	ProxyToken              *string    `json:"proxy_token,omitempty"`
	ProvisionStep           *string    `json:"provision_step,omitempty"`
	ProvisioningStartedAt   *time.Time `json:"provisioning_started_at,omitempty"`
	ProvisioningCompletedAt *time.Time `json:"provisioning_completed_at,omitempty"`
	BudgetMicrocents        *int64     `json:"budget_microcents,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	StartedAt               *time.Time `json:"started_at,omitempty"`
	StoppedAt               *time.Time `json:"stopped_at,omitempty"`
	DataVolumeGB            int        `json:"data_volume_gb"`
	DesiredRootfsVersion    *string    `json:"desired_rootfs_version,omitempty"`
	DesiredOpenclawVersion  *string    `json:"desired_openclaw_version,omitempty"`
	DesiredChannel          *string    `json:"desired_channel,omitempty"`
	ResolvedRootfsVersion   *string    `json:"resolved_rootfs_version,omitempty"`
	ResolvedOpenclawVersion *string    `json:"resolved_openclaw_version,omitempty"`
	ActualRootfsVersion     *string    `json:"actual_rootfs_version,omitempty"`
	ActualOpenclawVersion   *string    `json:"actual_openclaw_version,omitempty"`
	VersionSource           *string    `json:"version_source,omitempty"`
	RuntimeSource           *string    `json:"runtime_source,omitempty"`
	RootfsSnapshot          *string    `json:"rootfs_snapshot,omitempty"`
	OpenclawVersion         *string    `json:"openclaw_version,omitempty"`
	LastStartedAt           *time.Time `json:"last_started_at,omitempty"`
	TunnelID                *string    `json:"tunnel_id,omitempty"`
	SigningKey              *string    `json:"-"`
	TunnelToken             string     `json:"-"` // transient: not persisted, passed to VM at boot
	HomeHostID              *int       `json:"home_host_id,omitempty"`
	StorageMode             string     `json:"storage_mode"`
	BackupsEnabled          bool       `json:"backups_enabled"`
	BackupKey               []byte     `json:"-"` // encrypted with platform master key
	BrowserVMID             *string    `json:"browser_vm_id,omitempty"`
}

const (
	MachineKindOpenClaw = "openclaw"
	MachineKindHermes   = "hermes"
)

func NormalizeMachineKind(kind string) string {
	if kind == "" {
		return MachineKindOpenClaw
	}
	return kind
}

func ValidMachineKind(kind string) bool {
	switch NormalizeMachineKind(kind) {
	case MachineKindOpenClaw, MachineKindHermes:
		return true
	default:
		return false
	}
}

// BrowserVM represents a standalone browser VM instance.
type BrowserVM struct {
	ID                    string                  `json:"id"`
	AccountID             int                     `json:"account_id"`
	Slug                  string                  `json:"slug"`
	Name                  string                  `json:"name"`
	HostID                *int                    `json:"host_id,omitempty"`
	VMIP                  *string                 `json:"vm_ip,omitempty"`
	Status                string                  `json:"status"`
	VCPUs                 int                     `json:"vcpus"`
	MemoryMB              int                     `json:"memory_mb"`
	CDPPort               int                     `json:"cdp_port"`
	DesiredRootfsManifest *string                 `json:"desired_rootfs_manifest,omitempty"`
	DesiredRootfsVersion  *string                 `json:"desired_rootfs_version,omitempty"`
	RootfsVersion         *string                 `json:"rootfs_version,omitempty"`
	ErrorMessage          *string                 `json:"error_message,omitempty"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
	PairedMachine         *BrowserVMPairedMachine `json:"paired_machine,omitempty"`
}

// BrowserVMPairedMachine identifies the machine currently paired with a
// browser VM. Populated by list queries that LEFT JOIN machines on
// machine.browser_vm_id; nil when the browser VM is unpaired.
type BrowserVMPairedMachine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Host struct {
	ID                   int             `json:"id"`
	VMName               string          `json:"vm_name"`
	VMID                 *string         `json:"vm_id,omitempty"`
	Zone                 string          `json:"zone"`
	Region               string          `json:"region"`
	MachineType          string          `json:"machine_type"`
	ExternalIP           *string         `json:"external_ip,omitempty"`
	InternalIP           *string         `json:"internal_ip,omitempty"`
	TunnelURL            *string         `json:"tunnel_url,omitempty"`
	Status               string          `json:"status"`
	StatusMessage        *string         `json:"status_message,omitempty"`
	SourceImage          *string         `json:"source_image,omitempty"` // snapshot name used to create this host
	CapacityVCPUs        int             `json:"capacity_vcpus"`
	CapacityMemoryMB     int             `json:"capacity_memory_mb"`
	CapacityDiskMB       int             `json:"capacity_disk_mb"`
	UsedVCPUs            int             `json:"used_vcpus"`
	UsedMemoryMB         int             `json:"used_memory_mb"`
	UsedDiskMB           int             `json:"used_disk_mb"`
	MachineCount         int             `json:"machine_count"`
	AgentVersion         *string         `json:"agent_version,omitempty"`
	OpenclawVersion      *string         `json:"openclaw_version,omitempty"`
	RootfsVersion        string          `json:"rootfs_version,omitempty"`
	BrowserRootfsVersion string          `json:"browser_rootfs_version,omitempty"`
	LastHeartbeat        *time.Time      `json:"last_heartbeat,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	Provider             string          `json:"provider"`
	ProviderClass        string          `json:"provider_class"`
	LifecycleMode        string          `json:"lifecycle_mode"`
	AgentEndpoint        *string         `json:"agent_endpoint,omitempty"`
	AgentEndpointType    string          `json:"agent_endpoint_type"`
	ProviderHostID       *string         `json:"provider_host_id,omitempty"`
	ProviderMetadata     json.RawMessage `json:"provider_metadata"`
	Capabilities         json.RawMessage `json:"capabilities"`
	Labels               json.RawMessage `json:"labels"`
	AgentToken           *string         `json:"-"`
	MaintenanceMode      bool            `json:"maintenance_mode"`
}

// ArtifactRelease is an immutable record of a published artifact version.
type ArtifactRelease struct {
	ID               int             `json:"id"`
	Kind             string          `json:"kind"`
	Version          string          `json:"version"`
	Channel          string          `json:"channel"`
	URL              string          `json:"url"`
	SHA256           string          `json:"sha256"`
	SizeBytes        *int64          `json:"size_bytes,omitempty"`
	Metadata         json.RawMessage `json:"metadata"`
	CreatedAt        time.Time       `json:"created_at"`
	MinRootfsVersion *string         `json:"min_rootfs_version,omitempty"`
}

// HostArtifactState tracks per-host artifact staging for a single kind.
type HostArtifactState struct {
	ID              int        `json:"id"`
	HostID          int        `json:"host_id"`
	Kind            string     `json:"kind"`
	StagedVersion   *string    `json:"staged_version,omitempty"`
	ActiveVersion   *string    `json:"active_version,omitempty"`
	DefaultVersion  *string    `json:"default_version,omitempty"`
	LastStagedAt    *time.Time `json:"last_staged_at,omitempty"`
	LastActivatedAt *time.Time `json:"last_activated_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// EnrollmentToken represents a one-time use token for host enrollment.
type EnrollmentToken struct {
	ID            string          `json:"id"`
	Token         string          `json:"token"`
	Provider      string          `json:"provider"`
	ProviderClass string          `json:"provider_class"`
	Labels        json.RawMessage `json:"labels"`
	UsedByHostID  *int            `json:"used_by_host_id,omitempty"`
	ExpiresAt     time.Time       `json:"expires_at"`
	CreatedAt     time.Time       `json:"created_at"`
}

// RouteData contains the data needed to populate a KV route entry.
type RouteData struct {
	AccountSlug  string
	MachineID    string
	MachineSlug  string
	HostHostname string // TunnelURL from host
	ProxyToken   string
}

type LLMUsage struct {
	ID           int64     `json:"id"`
	AccountID    int       `json:"account_id"`
	MachineID    string    `json:"machine_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	RequestID    *string   `json:"request_id,omitempty"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
}

type UsageBucketEntry struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Source       string `json:"source"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	RequestCount int    `json:"request_count"`
}

type UsageBucket struct {
	Timestamp time.Time          `json:"timestamp"`
	Entries   []UsageBucketEntry `json:"entries"`
}

type Secret struct {
	ID             int       `json:"id"`
	MachineID      string    `json:"machine_id"`
	Key            string    `json:"key"`
	EncryptedValue string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Credential struct {
	ID             int        `json:"id"`
	AccountID      int        `json:"account_id"`
	MachineID      string     `json:"machine_id"`
	Provider       string     `json:"provider"`
	CredentialType string     `json:"credential_type"`
	EncryptedValue string     `json:"-"`
	Label          string     `json:"label"`
	LastValidated  *time.Time `json:"last_validated,omitempty"`
	LastFour       *string    `json:"last_four,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	RefreshToken   *string    `json:"-"`
	Status         string     `json:"status"`
	StatusDetail   *string    `json:"status_detail,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type RegistryEntry struct {
	ID                  string          `json:"id"`
	Type                string          `json:"type"`
	Name                string          `json:"name"`
	Description         *string         `json:"description,omitempty"`
	Version             string          `json:"version"`
	Tier                string          `json:"tier"`
	Modes               json.RawMessage `json:"modes,omitempty"`
	ConfigTemplate      json.RawMessage `json:"config_template"`
	Infrastructure      json.RawMessage `json:"infrastructure,omitempty"`
	RequiredCredentials []string        `json:"required_credentials,omitempty"`
	Status              string          `json:"status"`
	SortOrder           int             `json:"sort_order"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type MachineCapability struct {
	ID              int             `json:"id"`
	MachineID       string          `json:"machine_id"`
	EntryID         string          `json:"entry_id"`
	Mode            *string         `json:"mode,omitempty"`
	Enabled         bool            `json:"enabled"`
	ConfigOverrides json.RawMessage `json:"config_overrides,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	// Joined from registry_entries for convenience
	EntryName string `json:"entry_name,omitempty"`
	EntryType string `json:"entry_type,omitempty"`
}

// MachineAgent represents an OpenClaw persona (AI agent) within a machine.
type MachineAgent struct {
	ID             string    `json:"id"`
	MachineID      string    `json:"machine_id"`
	AgentID        string    `json:"agent_id"`
	Name           string    `json:"name"`
	Model          *string   `json:"model,omitempty"`
	IdentityEmoji  *string   `json:"identity_emoji,omitempty"`
	IdentityAvatar *string   `json:"identity_avatar,omitempty"`
	Soul           *string   `json:"soul,omitempty"`
	IsDefault      bool      `json:"is_default"`
	SortOrder      int       `json:"sort_order"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// PluginCatalogEntry represents a plugin in the global catalog.
type PluginCatalogEntry struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Description    *string         `json:"description,omitempty"`
	Slot           string          `json:"slot"`
	Version        string          `json:"version"`
	InstallKind    string          `json:"install_kind"`
	ArtifactURL    *string         `json:"artifact_url,omitempty"`
	ArtifactSHA256 *string         `json:"artifact_sha256,omitempty"`
	ConfigTemplate json.RawMessage `json:"config_template,omitempty"`
	Status         string          `json:"status"`
	SortOrder      int             `json:"sort_order"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// MachinePlugin tracks which plugin is enabled on a specific machine.
type MachinePlugin struct {
	ID               string          `json:"id"`
	MachineID        string          `json:"machine_id"`
	PluginID         string          `json:"plugin_id"`
	Slot             string          `json:"slot"`
	Enabled          bool            `json:"enabled"`
	ConfigOverrides  json.RawMessage `json:"config_overrides,omitempty"`
	InstallStatus    string          `json:"install_status"`
	InstalledVersion *string         `json:"installed_version,omitempty"`
	InstalledAt      *time.Time      `json:"installed_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// MachinePluginWithCatalog extends MachinePlugin with catalog info needed for reconciliation.
type MachinePluginWithCatalog struct {
	MachinePlugin
	InstallKind    string  `json:"install_kind"`
	ArtifactURL    *string `json:"artifact_url,omitempty"`
	CatalogVersion string  `json:"catalog_version"`
}

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

type MachineConfigRecord struct {
	MachineID         string          `json:"machine_id"`
	IdentityName      *string         `json:"identity_name,omitempty"`
	IdentityAvatar    *string         `json:"identity_avatar,omitempty"`
	PlatformOverrides json.RawMessage `json:"platform_overrides,omitempty"`
	AssembledConfig   json.RawMessage `json:"assembled_config,omitempty"`
	ConfigVersion     int             `json:"config_version"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// ResolvedRoute contains the data needed to route a request to a running machine.
type ResolvedRoute struct {
	AccountID    int    `json:"account_id"`
	MachineID    string `json:"machine_id"`
	ProxyToken   string `json:"proxy_token"`
	HostHostname string `json:"host_hostname"`
	UserIDs      []int  `json:"user_ids"`
}

// MachineOperation tracks an in-flight lifecycle operation (start/stop/delete)
// for idempotency and race prevention.
type MachineOperation struct {
	ID             string     `json:"id"`
	MachineID      string     `json:"machine_id"`
	Kind           string     `json:"kind"`
	State          string     `json:"state"`
	IdempotencyKey *string    `json:"idempotency_key,omitempty"`
	ErrorCode      *string    `json:"error_code,omitempty"`
	ErrorMessage   *string    `json:"error_message,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// Placement represents a machine-to-host placement record.
type Placement struct {
	ID                   string     `json:"id"`
	MachineID            string     `json:"machine_id"`
	HostID               int        `json:"host_id"`
	VMIP                 string     `json:"vm_ip"`
	State                string     `json:"state"`
	ReservedAt           time.Time  `json:"reserved_at"`
	ActivatedAt          *time.Time `json:"activated_at,omitempty"`
	ReleasedAt           *time.Time `json:"released_at,omitempty"`
	CreatedByOperationID *string    `json:"created_by_operation_id,omitempty"`
}

// CapacityPolicy defines overcommit ratios and resource reserves for a scope.
type CapacityPolicy struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	ScopeType             string    `json:"scope_type"`
	ScopeRef              *string   `json:"scope_ref,omitempty"`
	CPUOvercommitRatio    float64   `json:"cpu_overcommit_ratio"`
	MemoryOvercommitRatio float64   `json:"memory_overcommit_ratio"`
	ReserveVCPUs          int       `json:"reserve_vcpus"`
	ReserveMemoryMB       int       `json:"reserve_memory_mb"`
	MaxMachineCount       *int      `json:"max_machine_count,omitempty"`
	Enabled               bool      `json:"enabled"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// BackupRecord stores metadata about a machine backup.
type BackupRecord struct {
	ID              int       `json:"id"`
	MachineID       string    `json:"machine_id"`
	Timestamp       time.Time `json:"timestamp"`
	GCSPath         string    `json:"gcs_path"`
	SizeBytes       int64     `json:"size_bytes"`
	CompressedBytes int64     `json:"compressed_bytes"`
	ChecksumSHA256  string    `json:"checksum_sha256"`
	HMACSHA256      []byte    `json:"-"`
	Nonce           []byte    `json:"-"`
	Trigger         string    `json:"trigger"`
	HostID          *int      `json:"host_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type TokenUsageRecord struct {
	ID           int64     `json:"id"`
	AccountID    int       `json:"account_id"`
	MachineID    string    `json:"machine_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
}

type WorkflowRun struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	ScopeType    string          `json:"scope_type"`
	ScopeID      string          `json:"scope_id"`
	Status       string          `json:"status"`
	CurrentPhase *string         `json:"current_phase,omitempty"`
	RequestedBy  *int            `json:"requested_by,omitempty"`
	AccountID    *int            `json:"account_id,omitempty"`
	Priority     string          `json:"priority"`
	InputJSON    json.RawMessage `json:"input_json"`
	OutputJSON   json.RawMessage `json:"output_json,omitempty"`
	SummaryJSON  json.RawMessage `json:"summary_json,omitempty"`
	ErrorCode    *string         `json:"error_code,omitempty"`
	ErrorMessage *string         `json:"error_message,omitempty"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type StalledWorkflow struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	ScopeType   string    `json:"scope_type"`
	ScopeID     string    `json:"scope_id"`
	LastEventAt time.Time `json:"last_event_at"`
}

type WorkflowEvent struct {
	ID          int64           `json:"id"`
	WorkflowID  string          `json:"workflow_id"`
	Phase       *string         `json:"phase,omitempty"`
	Level       string          `json:"level"`
	EventType   string          `json:"event_type"`
	Message     string          `json:"message"`
	DetailsJSON json.RawMessage `json:"details_json,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

type WorkflowLock struct {
	ResourceType   string    `json:"resource_type"`
	ResourceID     string    `json:"resource_id"`
	LockKind       string    `json:"lock_kind"`
	WorkflowID     string    `json:"workflow_id"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ---- Domain repository interfaces ----

// UserRepo handles user persistence.
type UserRepo interface {
	CreateUser(ctx context.Context, user *User) error
	GetUser(ctx context.Context, id int) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByProvider(ctx context.Context, provider, providerID string) (*User, error)
	GetUserByCfSub(ctx context.Context, cfSub string) (*User, error)
	GetUserByIdentity(ctx context.Context, issuer, subject string) (*User, error)
	UpdateUserProvider(ctx context.Context, userID int, provider, providerID string, avatarURL *string) error
	UpdateUserCfSub(ctx context.Context, userID int, cfSub string) error
}

// AccountRepo handles account persistence.
type AccountRepo interface {
	CreateAccount(ctx context.Context, account *Account) error
	CreateAccountWithOwner(ctx context.Context, account *Account, ownerUserID int) error
	GetAccount(ctx context.Context, id int) (*Account, error)
	GetAccountBySlug(ctx context.Context, slug string) (*Account, error)
	ListAccountsByUser(ctx context.Context, userID int) ([]Account, error)
	AddAccountMember(ctx context.Context, member *AccountMember) error
	RemoveAccountMember(ctx context.Context, accountID, userID int) error
	ListAccountMembers(ctx context.Context, accountID int) ([]AccountMember, error)
	GetAccountMember(ctx context.Context, accountID, userID int) (*AccountMember, error)
	UpdateAccountMemberRole(ctx context.Context, accountID, userID int, role string) error
	UpdateAccountName(ctx context.Context, accountID int, name string) error
}

// InvitationRepo handles account invitations.
type InvitationRepo interface {
	CreateInvitation(ctx context.Context, inv *AccountInvitation) error
	GetInvitationByToken(ctx context.Context, token string) (*AccountInvitation, error)
	ListInvitationsByAccount(ctx context.Context, accountID int) ([]AccountInvitation, error)
	ListPendingInvitationsByEmail(ctx context.Context, email string) ([]AccountInvitation, error)
	AcceptInvitation(ctx context.Context, token string, userID int) (int64, error)
	DeclineInvitation(ctx context.Context, token string) error
	RevokeInvitation(ctx context.Context, accountID, id int) error
	ExpireOldInvitations(ctx context.Context) (int64, error)
}

// MachineRepo handles machine persistence.
type MachineRepo interface {
	CreateMachine(ctx context.Context, machine *Machine) error
	GetMachine(ctx context.Context, id string) (*Machine, error)
	GetMachineBySlug(ctx context.Context, slug string) (*Machine, error)
	ListMachinesByAccount(ctx context.Context, accountID int) ([]Machine, error)
	ListMachinesByHost(ctx context.Context, hostID int) ([]Machine, error)
	ListAllMachines(ctx context.Context) ([]Machine, error)
	UpdateMachine(ctx context.Context, machine *Machine) error
	DeleteMachine(ctx context.Context, id string) error
	UpdateMachineStatus(ctx context.Context, id, status string, message *string) error
	AssignMachineToHost(ctx context.Context, machineID string, hostID int, vmIP string) error
	UnassignMachineFromHost(ctx context.Context, machineID string) error
	SetMachineTokens(ctx context.Context, machineID string, gatewayToken, proxyToken string) error
	UpdateMachineVersion(ctx context.Context, id string, snapshot, version string) error
	UpdateMachineTunnel(ctx context.Context, machineID, tunnelID, signingKey string) error
	ClearMachineTunnel(ctx context.Context, machineID string) error
	IsMachineActive(ctx context.Context, slug string) (bool, error)
	MarkMachinesOnHostError(ctx context.Context, hostID int, message string) ([]string, error)
	// UpdateMachineStatusCAS updates machine status only if current status matches expectedStatus.
	// Returns false if the status didn't match (no rows updated).
	UpdateMachineStatusCAS(ctx context.Context, id, expectedStatus, newStatus string, message *string) (bool, error)
	// FindStuckMachines returns machines stuck in a transitional status (provisioning, stopping)
	// for longer than the given threshold.
	FindStuckMachines(ctx context.Context, stuckThreshold time.Duration) ([]Machine, error)
}

// HostFilter describes the minimum resources and constraints for host selection.
type HostFilter struct {
	MinVCPUs              int
	MinMemoryMB           int
	MinDiskMB             int
	Region                string
	Image                 string
	RequireBrowserReflink bool
}

// HostRepo handles host persistence.
type HostRepo interface {
	CreateHost(ctx context.Context, host *Host) error
	GetHost(ctx context.Context, id int) (*Host, error)
	ListHosts(ctx context.Context) ([]Host, error)
	FindHostWithCapacity(ctx context.Context, vcpus, memoryMB int, region, expectedImage string) (*Host, error)
	// ListEligibleHosts returns hosts that are ready, have fresh heartbeats,
	// and have sufficient resources across all three dimensions (vCPU, memory, disk).
	// Read-only, no locks. Returns all candidates for Go-side ranking.
	ListEligibleHosts(ctx context.Context, filter HostFilter) ([]Host, error)
	// PlaceOnHost atomically claims capacity, allocates a VM IP, and assigns
	// the machine — all in one transaction. Uses FOR UPDATE SKIP LOCKED on
	// the host row. Returns error if host is contended or capacity changed.
	PlaceOnHost(ctx context.Context, hostID int, machineID string, vcpus, memoryMB, diskMB int) (vmIP string, err error)
	AllocateHostCapacity(ctx context.Context, hostID, vcpus, memoryMB int) error
	ReleaseHostCapacity(ctx context.Context, hostID, vcpus, memoryMB int) error
	UpdateHostStatus(ctx context.Context, id int, status string) error
	UpdateHostStatusMessage(ctx context.Context, id int, status, message string) error
	UpdateHostDetails(ctx context.Context, h *Host) error
	DeleteHost(ctx context.Context, id int) error
	UpdateHostSourceImage(ctx context.Context, id int, sourceImage string) error
	UpdateHostVersions(ctx context.Context, id int, agentVersion, openclawVersion string) error
	UpdateHostHeartbeat(ctx context.Context, id int, externalIP, agentVersion, rootfsVersion, browserRootfsVersion, openclawVersion string, capabilities json.RawMessage) (ipChanged bool, err error)
	ListStaleHosts(ctx context.Context, threshold time.Duration) ([]Host, error)
	ListUnreachableHosts(ctx context.Context) ([]Host, error)
	SetHostMaintenanceMode(ctx context.Context, id int, enabled bool) error
	// ReconcileHostCapacityVCPUs recalculates capacity_vcpus for all non-terminated
	// hosts by extracting cpu_threads from the capabilities JSON and multiplying
	// by the given oversubscription ratio. Returns the number of hosts updated.
	ReconcileHostCapacityVCPUs(ctx context.Context, ratio int) (int, error)
}

// PlacementRepo handles atomic machine-to-host placement.
type PlacementRepo interface {
	// Legacy methods (kept for dual-write during migration)
	PlaceMachineOnHost(ctx context.Context, machineID string, vcpus, memoryMB int, region, expectedImage string) (*Host, string, error)
	PlaceMachineOnSpecificHost(ctx context.Context, machineID string, hostID, vcpus, memoryMB int) (*Host, string, error)
	SoftStopMachine(ctx context.Context, machineID string, hostID, vcpus, memoryMB, diskMB int) error
	ReAllocateHostCapacity(ctx context.Context, machineID string, hostID, vcpus, memoryMB, diskMB int) (string, error)

	// New placement record methods
	ReservePlacement(ctx context.Context, machineID string, hostID int, vmIP, operationID string) (*Placement, error)
	ActivatePlacement(ctx context.Context, placementID string) (bool, error)
	ReleasePlacement(ctx context.Context, machineID string) error
	GetActivePlacement(ctx context.Context, machineID string) (*Placement, error)
	ListActivePlacementsByHost(ctx context.Context, hostID int) ([]Placement, error)
	ResolveCapacityPolicy(ctx context.Context, hostID int, hostPool string) (*CapacityPolicy, error)
	UpdateMachineHomeHost(ctx context.Context, machineID string, homeHostID *int) error
	ReleaseMigrationSource(ctx context.Context, machineID string, expectedHostID int, vcpus, memoryMB int, releaseCapacity bool) error
	// ReleaseHostCapacityWithDisk releases all three resource dimensions.
	ReleaseHostCapacityWithDisk(ctx context.Context, hostID, vcpus, memoryMB, diskMB int) error
}

// RouteRepo handles route resolution.
type RouteRepo interface {
	ListRunningRoutes(ctx context.Context) ([]RouteData, error)
	ResolveRoute(ctx context.Context, accountSlug, machineSlug string) (*ResolvedRoute, error)
}

// CredentialRepo handles secrets and credentials.
type CredentialRepo interface {
	SetSecret(ctx context.Context, machineID, key, encryptedValue string) error
	ListSecrets(ctx context.Context, machineID string) ([]Secret, error)
	ListSecretsWithValues(ctx context.Context, machineID string) ([]Secret, error)
	DeleteSecret(ctx context.Context, machineID, key string) error
	ListMachineCredentials(ctx context.Context, machineID string) ([]Credential, error)
	ListMachineCredentialsWithValues(ctx context.Context, machineID string) ([]Credential, error)
	SetMachineCredential(ctx context.Context, machineID string, cred *Credential) error
	DeleteMachineCredential(ctx context.Context, machineID string, provider string) error
	GetMachineCredentialWithValue(ctx context.Context, machineID string, provider string) (*Credential, error)
	UpdateCredentialStatus(ctx context.Context, machineID string, provider string, status string, detail *string) error
	UpdateCredentialTokens(ctx context.Context, credID int, encryptedValue, encryptedRefreshToken string, expiresAt time.Time) error
}

// ActivityLog represents a single business-level event in the activity log.
type ActivityLog struct {
	ID            int64           `json:"id"`
	EventID       string          `json:"event_id"`
	Category      string          `json:"category"`
	Action        string          `json:"action"`
	Status        string          `json:"status"`
	ActorType     string          `json:"actor_type"`
	ActorID       *int            `json:"actor_id,omitempty"`
	ActorName     *string         `json:"actor_name,omitempty"`
	AccountID     *int            `json:"account_id,omitempty"`
	AccountName   *string         `json:"account_name,omitempty"`
	MachineID     *string         `json:"machine_id,omitempty"`
	MachineName   *string         `json:"machine_name,omitempty"`
	HostID        *int            `json:"host_id,omitempty"`
	HostName      *string         `json:"host_name,omitempty"`
	AgentVersion  *string         `json:"agent_version,omitempty"`
	RootfsVersion *string         `json:"rootfs_version,omitempty"`
	Summary       string          `json:"summary"`
	Detail        json.RawMessage `json:"detail,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	DurationMS    *int            `json:"duration_ms,omitempty"`
	ErrorCode     *string         `json:"error_code,omitempty"`
	ErrorMessage  *string         `json:"error_message,omitempty"`
}

// ActivityFilter controls which activity log entries are returned.
type ActivityFilter struct {
	Category   string
	Action     string
	Status     string
	AccountID  *int
	MachineID  *string
	HostID     *int
	ActorType  string
	ActorID    *int
	CursorTime *time.Time
	CursorID   *int64
	Limit      int
}

// ActivityRepo handles the unified activity log.
type ActivityRepo interface {
	CreateActivity(ctx context.Context, a *ActivityLog) error
	ListActivities(ctx context.Context, accountID int, filter ActivityFilter) ([]ActivityLog, error)
	ListActivitiesByMachine(ctx context.Context, machineID string, filter ActivityFilter) ([]ActivityLog, error)
	ListActivitiesAdmin(ctx context.Context, filter ActivityFilter) ([]ActivityLog, error)
}

// RegistryRepo handles registry and capabilities.
type RegistryRepo interface {
	ListRegistryEntries(ctx context.Context, entryType string) ([]RegistryEntry, error)
	ListRegistryEntriesAdmin(ctx context.Context, kind string) ([]RegistryEntry, error)
	GetRegistryEntry(ctx context.Context, id string) (*RegistryEntry, error)
	CreateRegistryEntry(ctx context.Context, entry *RegistryEntry) error
	UpdateRegistryEntry(ctx context.Context, entry *RegistryEntry) error
	DeleteRegistryEntry(ctx context.Context, id string) error
	ListMachineCapabilities(ctx context.Context, machineID string) ([]MachineCapability, error)
	EnableMachineCapability(ctx context.Context, machineID, entryID string, mode *string) error
	UpdateMachineCapabilityOverrides(ctx context.Context, machineID, entryID string, overrides json.RawMessage) error
	DisableMachineCapability(ctx context.Context, machineID, entryID string) error
}

// ConfigRepo handles machine config.
type ConfigRepo interface {
	GetMachineConfig(ctx context.Context, machineID string) (*MachineConfigRecord, error)
	SetMachineIdentity(ctx context.Context, machineID string, name, avatar *string) error
	SetMachineAssembledConfig(ctx context.Context, machineID string, config json.RawMessage, version int) error
	SetMachinePreferredModel(ctx context.Context, machineID, model string) error
	SetMachineModelFallbacks(ctx context.Context, machineID string, fallbacks []string) error
	GetMachinePreferredModel(ctx context.Context, machineID string) (string, error)
	UpdateMachinePlatformOverride(ctx context.Context, machineID, key, value string) error
	DeleteMachinePlatformOverride(ctx context.Context, machineID, key string) error
	CreateCustomProvider(ctx context.Context, provider *CustomProvider) error
	DeleteCustomProvider(ctx context.Context, accountID int, name string) error
	ListCustomProviders(ctx context.Context, accountID int) ([]CustomProvider, error)
	GetCustomProvider(ctx context.Context, accountID int, name string) (*CustomProvider, error)
}

// VMMetricsRepo handles ingest and read of per-VM CPU/memory samples.
// See backend/migrations/084_vm_metrics.sql and design rev. 3.
type VMMetricsRepo interface {
	InsertVMMetrics(ctx context.Context, points []VMMetricPoint) error
	QueryVMMetricsRange(ctx context.Context, accountID int, vmID string, kind VMMetricKind, from, to time.Time, res VMMetricResolution) ([]VMMetricPoint, error)
	QueryVMMetricsSince(ctx context.Context, accountID int, vmID string, kind VMMetricKind, since time.Time, limit int) ([]VMMetricPoint, error)
}

// BrowserVMRepo handles browser VM CRUD and pairing.
type BrowserVMRepo interface {
	CreateBrowserVM(ctx context.Context, bvm *BrowserVM) error
	GetBrowserVM(ctx context.Context, id string) (*BrowserVM, error)
	ListBrowserVMsByAccount(ctx context.Context, accountID int) ([]BrowserVM, error)
	ListBrowserVMsRequiringReconciliation(ctx context.Context) ([]BrowserVM, error)
	UpdateBrowserVMStatus(ctx context.Context, id, status string, errMsg *string) error
	AssignBrowserVMToHost(ctx context.Context, id string, hostID int, vmIP string) error
	UnassignBrowserVMFromHost(ctx context.Context, id string) error
	DeleteBrowserVM(ctx context.Context, id string) error
	PairBrowserVM(ctx context.Context, machineID, browserVMID string) error
	UnpairBrowserVM(ctx context.Context, machineID string) error
	GetMachineByBrowserVMID(ctx context.Context, browserVMID string) (*Machine, error)
	PlaceBrowserVMOnHost(ctx context.Context, hostID int, browserVMID string, vcpus, memoryMB int) (vmIP string, err error)
	ActivateBrowserVMPlacement(ctx context.Context, browserVMID string) error
	ReleaseBrowserVMPlacement(ctx context.Context, browserVMID string) error
}

// OperationRepo handles machine operation tracking for idempotency/race prevention.
type OperationRepo interface {
	CreateMachineOperation(ctx context.Context, op *MachineOperation) error
	GetActiveMachineOperation(ctx context.Context, machineID string) (*MachineOperation, error)
	CompleteMachineOperation(ctx context.Context, operationID, state string, errMsg *string) error
	ExpireStaleOperations(ctx context.Context, olderThan time.Duration) (int, error)
}

// WorkflowRepo handles workflow projection and lock state.
type WorkflowRepo interface {
	CreateWorkflowRun(ctx context.Context, run *WorkflowRun) error
	GetWorkflowRun(ctx context.Context, id string) (*WorkflowRun, error)
	UpdateWorkflowRunStatus(ctx context.Context, id, status string, phase *string, summary json.RawMessage, errCode, errMsg *string) error
	CompleteWorkflowRun(ctx context.Context, id, status string, output json.RawMessage, errCode, errMsg *string) error
	ListWorkflowRunsByScope(ctx context.Context, scopeType, scopeID string, limit int) ([]WorkflowRun, error)
	ListAllWorkflowRuns(ctx context.Context, kind, status string, limit int) ([]WorkflowRun, error)

	CreateWorkflowEvent(ctx context.Context, event *WorkflowEvent) error
	ListWorkflowEvents(ctx context.Context, workflowID string, limit int) ([]WorkflowEvent, error)

	AcquireWorkflowLock(ctx context.Context, lock *WorkflowLock) error
	RenewWorkflowLock(ctx context.Context, workflowID, resourceType, resourceID, lockKind string, leaseUntil time.Time) error
	ReleaseWorkflowLocks(ctx context.Context, workflowID string) error

	FindStalledWorkflows(ctx context.Context, staleThreshold time.Duration) ([]StalledWorkflow, error)
}

// EnrollmentRepo handles host enrollment tokens and registered host creation.
type EnrollmentRepo interface {
	CreateEnrollmentToken(ctx context.Context, token *EnrollmentToken) error
	GetEnrollmentToken(ctx context.Context, token string) (*EnrollmentToken, error)
	ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error)
	MarkEnrollmentTokenUsed(ctx context.Context, token string, hostID int) error
	CreateRegisteredHost(ctx context.Context, host *Host) error
	UpdateHostAgentEndpoint(ctx context.Context, hostID int, endpoint, endpointType string) error
	GetHostByAgentToken(ctx context.Context, agentToken string) (*Host, error)
	UpdateHostProviderMetadata(ctx context.Context, hostID int, metadata json.RawMessage) error
}

// BackupRepo handles backup record persistence.
type BackupRepo interface {
	CreateBackupRecord(ctx context.Context, b *BackupRecord) error
	ListBackupRecords(ctx context.Context, machineID string) ([]BackupRecord, error)
	GetBackupRecord(ctx context.Context, id int) (*BackupRecord, error)
	DeleteBackupRecord(ctx context.Context, id int) error
	DeleteAllBackupRecords(ctx context.Context, machineID string) error
	EnableBackups(ctx context.Context, machineID string, backupKey []byte) error
	DisableBackups(ctx context.Context, machineID string) error
	ListMachinesWithoutBackupKey(ctx context.Context) ([]string, error)
}

// MachineAgentRepo handles CRUD for OpenClaw personas within machines.
type MachineAgentRepo interface {
	CreateMachineAgent(ctx context.Context, agent *MachineAgent) error
	ListMachineAgents(ctx context.Context, machineID string) ([]MachineAgent, error)
	GetMachineAgent(ctx context.Context, machineID, agentID string) (*MachineAgent, error)
	UpdateMachineAgent(ctx context.Context, agent *MachineAgent) error
	DeleteMachineAgent(ctx context.Context, machineID, agentID string) error
	DemoteDefaultAgents(ctx context.Context, machineID string) error
}

// ModelCatalogEntry represents a single model in the platform model catalog.
type ModelCatalogEntry struct {
	ID             string
	Label          string
	Description    string
	Source         string  // "platform", "byok", "subscription"
	Tier           *string // nullable
	GatewayModelID *string // nullable
	Provider       string
	Enabled        bool
	SortOrder      int
	CreatedAt      time.Time
}

// ---- Integration types ----

type IntegrationCatalogEntry struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Icon         string    `json:"icon"`
	Toolkit      string    `json:"toolkit"`
	AuthConfigID *string   `json:"auth_config_id,omitempty"`
	Category     string    `json:"category"`
	SortOrder    int       `json:"sort_order"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type IntegrationEvent struct {
	ID            string          `json:"id"`
	AccountID     int             `json:"account_id"`
	MachineID     *string         `json:"machine_id,omitempty"`
	IntegrationID string          `json:"integration_id"`
	Event         string          `json:"event"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// PluginCatalogRepo handles CRUD for the global plugin catalog.
type PluginCatalogRepo interface {
	ListPluginCatalog(ctx context.Context) ([]PluginCatalogEntry, error)
	GetPluginCatalogEntry(ctx context.Context, id string) (*PluginCatalogEntry, error)
	CreatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error
	UpdatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error
	DeletePluginCatalogEntry(ctx context.Context, id string) error
}

// IntegrationCatalogRepo handles CRUD for the curated integration catalog.
type IntegrationCatalogRepo interface {
	ListIntegrationCatalog(ctx context.Context) ([]IntegrationCatalogEntry, error)
	ListConfiguredIntegrations(ctx context.Context) ([]IntegrationCatalogEntry, error) // enabled + auth_config_id IS NOT NULL
	GetIntegrationCatalogEntry(ctx context.Context, id string) (*IntegrationCatalogEntry, error)
	CreateIntegrationCatalogEntry(ctx context.Context, e IntegrationCatalogEntry) error
	UpdateIntegrationCatalogEntry(ctx context.Context, e IntegrationCatalogEntry) error
	DeleteIntegrationCatalogEntry(ctx context.Context, id string) error
}

// IntegrationEventRepo handles audit trail for integration connect/disconnect actions.
type IntegrationEventRepo interface {
	LogIntegrationEvent(ctx context.Context, accountID int, machineID, integrationID, event string, metadata json.RawMessage) error
	HasIntegrationEvent(ctx context.Context, machineID, integrationID, event string) (bool, error)
}

// MachinePluginRepo handles per-machine plugin state.
type MachinePluginRepo interface {
	ListMachinePlugins(ctx context.Context, machineID string) ([]MachinePlugin, error)
	ListMachinePluginsWithCatalog(ctx context.Context, machineID string) ([]MachinePluginWithCatalog, error)
	EnableMachinePlugin(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error
	DisableMachinePlugin(ctx context.Context, machineID, pluginID string) error
	UpdateMachinePluginOverrides(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error
	UpdateMachinePluginStatus(ctx context.Context, machineID, pluginID, status, version string) error
}

// TokenUsageRepo handles token usage recording.
type TokenUsageRepo interface {
	RecordTokenUsage(ctx context.Context, record *TokenUsageRecord) error
}

// ModelCatalogRepo handles the global model catalog.
type ModelCatalogRepo interface {
	ListModelCatalog(ctx context.Context) ([]ModelCatalogEntry, error)
	GetModelCatalogEntry(ctx context.Context, modelID string) (*ModelCatalogEntry, error)
}

// ProviderCatalogEntry represents a provider in the data-driven provider catalog.
type ProviderCatalogEntry struct {
	ID                 string          `json:"id"`
	Label              string          `json:"label"`
	Category           string          `json:"category"`
	UpstreamHost       *string         `json:"upstream_host,omitempty"`
	UpstreamPathPrefix string          `json:"upstream_path_prefix"`
	Scheme             string          `json:"scheme"`
	AuthMethod         *string         `json:"auth_method,omitempty"`
	AuthHeader         *string         `json:"auth_header,omitempty"`
	AllowedHosts       []string        `json:"allowed_hosts,omitempty"`
	EnvVar             *string         `json:"env_var,omitempty"`
	CredentialType     string          `json:"credential_type"`
	Placeholder        *string         `json:"placeholder,omitempty"`
	IconLetter         *string         `json:"icon_letter,omitempty"`
	IconBg             *string         `json:"icon_bg,omitempty"`
	Validation         json.RawMessage `json:"validation,omitempty"`
	AutodetectOrder    *int            `json:"autodetect_order,omitempty"`
	SortOrder          int             `json:"sort_order"`
	Enabled            bool            `json:"enabled"`
	ExecSecretID       *string         `json:"exec_secret_id,omitempty"`
	RuntimeProviderID  *string         `json:"runtime_provider_id,omitempty"`
	PluginID           *string         `json:"plugin_id,omitempty"`
	CreatedAt          time.Time       `json:"created_at,omitempty"`
}

// ProviderCatalogRepo handles the data-driven provider catalog.
type ProviderCatalogRepo interface {
	ListProviderCatalog(ctx context.Context) ([]ProviderCatalogEntry, error)
	ListProviderCatalogByCategory(ctx context.Context, category string) ([]ProviderCatalogEntry, error)
	GetProviderCatalogEntry(ctx context.Context, id string) (*ProviderCatalogEntry, error)
}

// OpikProject represents a tracing project scoped to an account.
type OpikProject struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	AccountID   int       `json:"account_id,omitempty"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// OpikTrace represents a single agent conversation turn trace.
type OpikTrace struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id,omitempty"`
	ProjectName   string          `json:"project_name,omitempty"`
	AccountID     int             `json:"-"`
	MachineID     string          `json:"-"`
	Name          *string         `json:"name,omitempty"`
	ThreadID      *string         `json:"thread_id,omitempty"`
	StartTime     time.Time       `json:"start_time"`
	EndTime       *time.Time      `json:"end_time,omitempty"`
	Input         json.RawMessage `json:"input,omitempty"`
	Output        json.RawMessage `json:"output,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	Tags          []string        `json:"tags,omitempty"`
	ErrorInfo     json.RawMessage `json:"error_info,omitempty"`
	Source        *string         `json:"source,omitempty"`
	CreatedAt     time.Time       `json:"created_at,omitempty"`
	LastUpdatedAt *time.Time      `json:"last_updated_at,omitempty"`
}

// OpikSpan represents an LLM call, tool execution, or subagent invocation.
type OpikSpan struct {
	ID                 string          `json:"id"`
	TraceID            string          `json:"trace_id"`
	ProjectID          string          `json:"project_id,omitempty"`
	ProjectName        string          `json:"project_name,omitempty"`
	AccountID          int             `json:"-"`
	MachineID          string          `json:"-"`
	ParentSpanID       *string         `json:"parent_span_id,omitempty"`
	Name               *string         `json:"name,omitempty"`
	Type               string          `json:"type"`
	StartTime          time.Time       `json:"start_time"`
	EndTime            *time.Time      `json:"end_time,omitempty"`
	Input              json.RawMessage `json:"input,omitempty"`
	Output             json.RawMessage `json:"output,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	Model              *string         `json:"model,omitempty"`
	Provider           *string         `json:"provider,omitempty"`
	Tags               []string        `json:"tags,omitempty"`
	Usage              json.RawMessage `json:"usage,omitempty"`
	PromptTokens       *int            `json:"-"`
	CompletionTokens   *int            `json:"-"`
	TotalTokens        *int            `json:"-"`
	ErrorInfo          json.RawMessage `json:"error_info,omitempty"`
	TotalEstimatedCost *float64        `json:"total_estimated_cost,omitempty"`
	Duration           *float64        `json:"duration,omitempty"`
	TTFT               *float64        `json:"ttft,omitempty"`
	Source             *string         `json:"source,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	LastUpdatedAt      *time.Time      `json:"last_updated_at,omitempty"`
}

// OpikTraceListItem is the dashboard-facing trace shape with lightweight span
// aggregates for list and detail views.
type OpikTraceListItem struct {
	ID                 string          `json:"id"`
	ProjectID          string          `json:"project_id,omitempty"`
	ProjectName        string          `json:"project_name,omitempty"`
	MachineID          string          `json:"machine_id,omitempty"`
	MachineName        string          `json:"machine_name,omitempty"`
	Name               *string         `json:"name,omitempty"`
	ThreadID           *string         `json:"thread_id,omitempty"`
	StartTime          time.Time       `json:"start_time"`
	EndTime            *time.Time      `json:"end_time,omitempty"`
	Input              json.RawMessage `json:"input,omitempty"`
	Output             json.RawMessage `json:"output,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	Tags               []string        `json:"tags,omitempty"`
	ErrorInfo          json.RawMessage `json:"error_info,omitempty"`
	Source             *string         `json:"source,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	LastUpdatedAt      *time.Time      `json:"last_updated_at,omitempty"`
	SpanCount          int             `json:"span_count"`
	ErrorCount         int             `json:"error_count"`
	DurationMS         int64           `json:"duration_ms"`
	TotalTokens        int             `json:"total_tokens"`
	TotalEstimatedCost float64         `json:"total_estimated_cost"`
	FeedbackCount      int             `json:"feedback_count"`
	AvgFeedbackScore   *float64        `json:"avg_feedback_score,omitempty"`
}

// OpikTraceSpan is a dashboard-facing span shape. Unlike OpikSpan, it exposes
// derived token columns and the emitting machine so cross-agent traces can be
// inspected while keeping account scope enforced by the query.
type OpikTraceSpan struct {
	ID                 string          `json:"id"`
	TraceID            string          `json:"trace_id"`
	MachineID          string          `json:"machine_id"`
	MachineName        string          `json:"machine_name,omitempty"`
	ParentSpanID       *string         `json:"parent_span_id,omitempty"`
	Name               *string         `json:"name,omitempty"`
	Type               string          `json:"type"`
	StartTime          time.Time       `json:"start_time"`
	EndTime            *time.Time      `json:"end_time,omitempty"`
	Input              json.RawMessage `json:"input,omitempty"`
	Output             json.RawMessage `json:"output,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	Model              *string         `json:"model,omitempty"`
	Provider           *string         `json:"provider,omitempty"`
	Tags               []string        `json:"tags,omitempty"`
	Usage              json.RawMessage `json:"usage,omitempty"`
	PromptTokens       int             `json:"prompt_tokens"`
	CompletionTokens   int             `json:"completion_tokens"`
	TotalTokens        int             `json:"total_tokens"`
	ErrorInfo          json.RawMessage `json:"error_info,omitempty"`
	TotalEstimatedCost float64         `json:"total_estimated_cost"`
	Duration           *float64        `json:"duration,omitempty"`
	TTFT               *float64        `json:"ttft,omitempty"`
	Source             *string         `json:"source,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	LastUpdatedAt      *time.Time      `json:"last_updated_at,omitempty"`
}

// OpikFeedbackScore records a human or automated judgment for a trace or span.
type OpikFeedbackScore struct {
	ID        string    `json:"id"`
	AccountID int       `json:"account_id"`
	TraceID   string    `json:"trace_id"`
	SpanID    *string   `json:"span_id,omitempty"`
	Name      string    `json:"name"`
	Value     float64   `json:"value"`
	Reason    *string   `json:"reason,omitempty"`
	Source    string    `json:"source"`
	CreatedBy *int      `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// OpikTraceDetail includes one trace and its spans for the dashboard trace panel.
type OpikTraceDetail struct {
	Trace    OpikTraceListItem   `json:"trace"`
	Spans    []OpikTraceSpan     `json:"spans"`
	Feedback []OpikFeedbackScore `json:"feedback"`
}

// OpikTraceSearchFilters describes account-level trace search filters.
// Empty optional fields are ignored by the store implementation.
type OpikTraceSearchFilters struct {
	Since         time.Time
	Limit         int
	MachineID     string
	Project       string
	Status        string
	Query         string
	ThreadID      string
	Tags          []string
	Feedback      string
	MaxFeedback   *float64
	MinTokens     *int
	MinDurationMS *int64
}

// extractTokenCounts parses the Usage JSONB and populates the dedicated token
// count columns. Called before insert/update so the DB columns stay in sync.
func (s *OpikSpan) extractTokenCounts() {
	if len(s.Usage) == 0 {
		return
	}
	var usage struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
		TotalTokens      *int `json:"total_tokens"`
	}
	if err := json.Unmarshal(s.Usage, &usage); err != nil {
		return
	}
	if usage.PromptTokens != nil {
		s.PromptTokens = usage.PromptTokens
	}
	if usage.CompletionTokens != nil {
		s.CompletionTokens = usage.CompletionTokens
	}
	if usage.TotalTokens != nil {
		s.TotalTokens = usage.TotalTokens
	}
}

// OpikRepo handles Opik-compatible trace and span storage.
type OpikRepo interface {
	GetOrCreateOpikProject(ctx context.Context, accountID int, name string) (*OpikProject, error)
	GetOpikProjectByName(ctx context.Context, accountID int, name string) (*OpikProject, error)
	ListOpikProjects(ctx context.Context, accountID int) ([]OpikProject, error)
	ListOpikTraces(ctx context.Context, accountID int, filters OpikTraceSearchFilters) ([]OpikTraceListItem, error)
	ListOpikTracesByMachine(ctx context.Context, accountID int, machineID string, since time.Time, limit int) ([]OpikTraceListItem, error)
	GetOpikTraceDetailByAccount(ctx context.Context, accountID int, traceID string) (*OpikTraceDetail, error)
	GetOpikTraceDetail(ctx context.Context, accountID int, machineID, traceID string) (*OpikTraceDetail, error)
	ListOpikFeedbackScores(ctx context.Context, accountID int, traceID string) ([]OpikFeedbackScore, error)
	CreateOpikFeedbackScore(ctx context.Context, score *OpikFeedbackScore) (*OpikFeedbackScore, error)
	UpdateOpikTraceTags(ctx context.Context, accountID int, traceID string, tags []string) error
	UpsertOpikTrace(ctx context.Context, trace *OpikTrace) error
	UpsertOpikTraces(ctx context.Context, traces []OpikTrace) error
	UpdateOpikTrace(ctx context.Context, accountID int, machineID, id string, trace *OpikTrace) error
	UpsertOpikSpan(ctx context.Context, span *OpikSpan) error
	UpsertOpikSpans(ctx context.Context, spans []OpikSpan) error
	UpdateOpikSpan(ctx context.Context, accountID int, machineID, id string, span *OpikSpan) error
	GetMachineByGatewayToken(ctx context.Context, token string) (*Machine, error)
}

// ArtifactReleaseRepo handles artifact release persistence.
type ArtifactReleaseRepo interface {
	CreateArtifactRelease(ctx context.Context, r *ArtifactRelease) error
	GetArtifactRelease(ctx context.Context, kind, version string) (*ArtifactRelease, error)
	ListArtifactReleases(ctx context.Context, kind, channel string, limit int) ([]ArtifactRelease, error)
	GetLatestArtifactRelease(ctx context.Context, kind, channel string) (*ArtifactRelease, error)
}

// HostArtifactStateRepo handles per-host artifact staging state.
type HostArtifactStateRepo interface {
	UpsertHostArtifactState(ctx context.Context, s *HostArtifactState) error
	GetHostArtifactState(ctx context.Context, hostID int, kind string) (*HostArtifactState, error)
	ListHostArtifactStates(ctx context.Context, hostID int) ([]HostArtifactState, error)
}

// BrowseSessionRepo manages browse session lifecycle.
type BrowseSessionRepo interface {
	CreateBrowseSession(ctx context.Context, session *BrowseSession) error
	GetBrowseSession(ctx context.Context, id string) (*BrowseSession, error)
	HeartbeatBrowseSession(ctx context.Context, id string, expiresAt time.Time) error
	DeleteBrowseSession(ctx context.Context, id string) error
	ListExpiredBrowseSessions(ctx context.Context) ([]BrowseSession, error)
}

// ---- Store interface ----

// Store is the aggregate interface. Consumers that need the full store use this.
// New code should depend on narrow domain interfaces instead.
type Store interface {
	UserRepo
	AccountRepo
	InvitationRepo
	MachineRepo
	HostRepo
	PlacementRepo
	RouteRepo
	CredentialRepo
	ActivityRepo
	RegistryRepo
	ConfigRepo
	OperationRepo
	EnrollmentRepo
	BackupRepo
	WorkflowRepo
	MachineAgentRepo
	PluginCatalogRepo
	MachinePluginRepo
	IntegrationCatalogRepo
	IntegrationEventRepo
	TokenUsageRepo
	ModelCatalogRepo
	ProviderCatalogRepo
	OpikRepo
	ArtifactReleaseRepo
	HostArtifactStateRepo
	BrowseSessionRepo
	BrowserVMRepo
	VMMetricsRepo
}
