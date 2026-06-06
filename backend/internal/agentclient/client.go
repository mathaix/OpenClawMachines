package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/tracing"
)

// Client is an HTTP client for the Host Agent control API (port 9090).
type Client struct {
	httpClient *http.Client
	agentToken string
}

// ErrOutcomeUnknown marks agent calls that timed out or were cancelled after
// the request was sent. The caller must assume the agent-side operation may
// still be running and avoid releasing DB-side reservations until explicit
// cleanup confirms the resource is gone.
var ErrOutcomeUnknown = errors.New("agent operation outcome unknown")
var ErrBrowserVMNotFound = errors.New("browser VM not found on agent")

func IsOutcomeUnknown(err error) bool {
	return errors.Is(err, ErrOutcomeUnknown)
}

func IsBrowserVMNotFound(err error) bool {
	return errors.Is(err, ErrBrowserVMNotFound)
}

type HTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("status %d: %s", e.StatusCode, e.Body)
}

func IsHTTPStatus(err error, statusCode int) bool {
	var statusErr *HTTPStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == statusCode
}

var browserVMCreateHTTPTimeout = 4 * time.Minute

// New creates a new agent client with the given auth token.
func New(agentToken string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		agentToken: agentToken,
	}
}

// SetHTTPClient replaces the default HTTP client (used in tests).
func (c *Client) SetHTTPClient(hc *http.Client) { c.httpClient = hc }

// HealthResponse is the response from the agent /health endpoint.
type HealthResponse struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	VMCount int    `json:"vm_count"`
	Version string `json:"version"`
}

// VMStatsResponse is a VM entry returned by the agent /vms endpoint with disk stats.
type VMStatsResponse struct {
	MachineID   string `json:"machine_id"`
	DiskTotalMB int64  `json:"disk_total_mb"`
	DiskUsedMB  int64  `json:"disk_used_mb"`
}

// VMRequest is the request body for creating a VM on the agent.
type VMRequest struct {
	MachineID        string                              `json:"machine_id"`
	MachineKind      string                              `json:"machine_kind,omitempty"`
	MachineName      string                              `json:"machine_name"`
	MachineSlug      string                              `json:"machine_slug"`
	VCPUs            int                                 `json:"vcpus"`
	MemoryMB         int                                 `json:"memory_mb"`
	VMIP             string                              `json:"vm_ip"`
	GatewayToken     string                              `json:"gateway_token"` // for OpenClaw gateway auth
	ProxyToken       string                              `json:"proxy_token"`   // for proxy API auth (browser/terminal/logs)
	OpenClawConf     []byte                              `json:"openclaw_config,omitempty"`
	HermesConfigYAML []byte                              `json:"hermes_config_yaml,omitempty"`
	HermesEnv        []byte                              `json:"hermes_env,omitempty"`
	HermesAuthJSON   []byte                              `json:"hermes_auth_json,omitempty"`
	HermesSoul       []byte                              `json:"hermes_soul,omitempty"`
	LLMEndpoint      string                              `json:"llm_endpoint,omitempty"`
	Secrets          map[string]string                   `json:"secrets,omitempty"`
	LLMKeys          map[string]metadata.CredentialEntry `json:"llm_keys,omitempty"`
	AccountID        int                                 `json:"account_id"`
	BudgetMicrocents *int64                              `json:"budget_microcents,omitempty"`
	DataVolumeGB     int                                 `json:"data_volume_gb"`
	DataVersion      int                                 `json:"data_version"`
	SigningKey       string                              `json:"signing_key,omitempty"`
	TunnelToken      string                              `json:"tunnel_token,omitempty"`
	VmHostname       string                              `json:"vm_hostname,omitempty"`
	OwnerEmails      []string                            `json:"owner_emails,omitempty"`
	CfCaPubKey       string                              `json:"cf_ca_pubkey,omitempty"`
	CustomProviders  []metadata.CustomProviderConfig     `json:"custom_providers,omitempty"`
	Souls            []metadata.SoulEntry                `json:"souls,omitempty"`
	RuntimeSelection *metadata.RuntimeSelection          `json:"runtime_selection,omitempty"`
}

// VMResponse is the response from VM create/get operations.
type VMResponse struct {
	MachineID    string  `json:"machine_id"`
	VMIP         string  `json:"vm_ip"`
	Status       string  `json:"status"`
	ErrorMessage string  `json:"error_message,omitempty"`
	VCPUs        int     `json:"vcpus"`
	MemoryMB     int     `json:"memory_mb"`
	ProxyToken   *string `json:"proxy_token,omitempty"`
}

func buildVMRequest(machine *store.Machine, vmIP string, secrets map[string]string, llmKeys map[string]metadata.CredentialEntry, dataVersion int, ownerEmails []string, cfCaPubKey string, openClawConf, hermesConfigYAML, hermesEnv, hermesAuthJSON, hermesSoul []byte, customProviders []metadata.CustomProviderConfig, souls []metadata.SoulEntry, runtimeSelection *metadata.RuntimeSelection) (VMRequest, error) {
	if machine.GatewayToken == nil || *machine.GatewayToken == "" {
		return VMRequest{}, fmt.Errorf("gateway token is required for VM creation")
	}
	if machine.ProxyToken == nil || *machine.ProxyToken == "" {
		return VMRequest{}, fmt.Errorf("proxy token is required for VM creation")
	}

	req := VMRequest{
		MachineID:        machine.ID,
		MachineKind:      store.NormalizeMachineKind(machine.Kind),
		MachineName:      machine.Name,
		MachineSlug:      machine.Slug,
		VCPUs:            machine.VCPUs,
		MemoryMB:         machine.MemoryMB,
		VMIP:             vmIP,
		GatewayToken:     *machine.GatewayToken,
		ProxyToken:       *machine.ProxyToken,
		OpenClawConf:     openClawConf,
		HermesConfigYAML: hermesConfigYAML,
		HermesEnv:        hermesEnv,
		HermesAuthJSON:   hermesAuthJSON,
		HermesSoul:       hermesSoul,
		Secrets:          secrets,
		LLMKeys:          llmKeys,
		AccountID:        machine.AccountID,
		BudgetMicrocents: machine.BudgetMicrocents,
		DataVolumeGB:     machine.DataVolumeGB,
		DataVersion:      dataVersion,
		RuntimeSelection: runtimeSelection,
	}

	if machine.SigningKey != nil {
		req.SigningKey = *machine.SigningKey
	}
	if machine.TunnelHostname != nil {
		req.VmHostname = *machine.TunnelHostname
	}
	if machine.TunnelToken != "" {
		req.TunnelToken = machine.TunnelToken
	}

	req.OwnerEmails = ownerEmails
	req.CfCaPubKey = cfCaPubKey
	req.CustomProviders = customProviders
	req.Souls = souls

	return req, nil
}

// Health checks the agent's health.
func (c *Client) Health(ctx context.Context, host *store.Host) (*HealthResponse, error) {
	url := c.agentURL(host) + "/health"

	resp, err := c.doRequest(ctx, "GET", url, nil, c.tokenForHost(host))
	if err != nil {
		return nil, fmt.Errorf("agent health check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent health check: status %d", resp.StatusCode)
	}

	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decode health response: %w", err)
	}

	return &health, nil
}

// CreateVM tells the agent to boot a Firecracker VM for the given machine.
func (c *Client) CreateVM(ctx context.Context, host *store.Host, machine *store.Machine, vmIP string, secrets map[string]string, llmKeys map[string]metadata.CredentialEntry, dataVersion int, ownerEmails []string, cfCaPubKey string, openClawConf, hermesConfigYAML, hermesEnv, hermesAuthJSON, hermesSoul []byte, customProviders []metadata.CustomProviderConfig, souls []metadata.SoulEntry, runtimeSelection *metadata.RuntimeSelection) (*VMResponse, error) {
	url := c.agentURL(host) + "/vms"
	req, err := buildVMRequest(machine, vmIP, secrets, llmKeys, dataVersion, ownerEmails, cfCaPubKey, openClawConf, hermesConfigYAML, hermesEnv, hermesAuthJSON, hermesSoul, customProviders, souls, runtimeSelection)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal VM request: %w", err)
	}

	resp, err := c.doRequest(ctx, "POST", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return nil, fmt.Errorf("create VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	var vmResp VMResponse
	if err := json.NewDecoder(resp.Body).Decode(&vmResp); err != nil {
		return nil, fmt.Errorf("decode create VM response: %w", err)
	}

	return &vmResp, nil
}

// UpgradeVM tells the agent to recreate a Firecracker VM in place while preserving its data volume.
func (c *Client) UpgradeVM(ctx context.Context, host *store.Host, machine *store.Machine, vmIP string, secrets map[string]string, llmKeys map[string]metadata.CredentialEntry, dataVersion int, ownerEmails []string, cfCaPubKey string, openClawConf, hermesConfigYAML, hermesEnv, hermesAuthJSON, hermesSoul []byte, customProviders []metadata.CustomProviderConfig, souls []metadata.SoulEntry, runtimeSelection *metadata.RuntimeSelection) (*VMResponse, error) {
	req, err := buildVMRequest(machine, vmIP, secrets, llmKeys, dataVersion, ownerEmails, cfCaPubKey, openClawConf, hermesConfigYAML, hermesEnv, hermesAuthJSON, hermesSoul, customProviders, souls, runtimeSelection)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal upgrade VM request: %w", err)
	}

	url := c.agentURL(host) + "/vms/" + machine.ID + "/upgrade"
	resp, err := c.doRequest(ctx, "POST", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return nil, fmt.Errorf("upgrade VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upgrade VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	var vmResp VMResponse
	if err := json.NewDecoder(resp.Body).Decode(&vmResp); err != nil {
		return nil, fmt.Errorf("decode upgrade VM response: %w", err)
	}

	return &vmResp, nil
}

// DestroyVM tells the agent to stop and clean up a Firecracker VM.
func (c *Client) DestroyVM(ctx context.Context, host *store.Host, machineID string) error {
	url := c.agentURL(host) + "/vms/" + machineID

	resp, err := c.doRequest(ctx, "DELETE", url, nil, c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("destroy VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		// VM already gone on the agent (e.g. after agent restart) — treat as success
		slog.Info("agent.destroy_vm.already_gone", "machine_id", machineID, "host", host.ID)
		return nil
	}
	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("destroy VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// StopVM tells the agent to gracefully stop a Firecracker VM but preserve its data volume.
func (c *Client) StopVM(ctx context.Context, host *store.Host, machineID string) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/stop"

	resp, err := c.doRequest(ctx, "POST", url, nil, c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("stop VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		// VM already gone on the agent (e.g. after agent restart) — treat as success
		slog.Info("agent.stop_vm.already_gone", "machine_id", machineID, "host", host.ID)
		return nil
	}
	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stop VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// SetCDPTarget remaps the CDP proxy target for a VM to the given address.
func (c *Client) SetCDPTarget(ctx context.Context, host *store.Host, machineID, target string) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/cdp-target"

	body, _ := json.Marshal(map[string]string{"target": target})
	resp, err := c.doRequest(ctx, "POST", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("set CDP target on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set CDP target: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ResetCDPTarget reverts the CDP proxy target for a VM to the companion browser VM.
func (c *Client) ResetCDPTarget(ctx context.Context, host *store.Host, machineID string) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/cdp-target"

	resp, err := c.doRequest(ctx, "DELETE", url, nil, c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("reset CDP target on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reset CDP target: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// RollbackVM tells the agent to restore a pre-upgrade data volume backup.
func (c *Client) RollbackVM(ctx context.Context, host *store.Host, machineID string) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/rollback"

	resp, err := c.doRequest(ctx, "POST", url, nil, c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("rollback VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rollback VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ---- Backup methods ----

// BackupAgentRequest is the request body for creating a backup on the agent.
type BackupAgentRequest struct {
	EncryptionKey []byte `json:"encryption_key"`
}

// BackupAgentResponse is the response from the agent backup endpoint.
type BackupAgentResponse struct {
	GCSPath         string `json:"gcs_path"`
	SizeBytes       int64  `json:"size_bytes"`
	CompressedBytes int64  `json:"compressed_bytes"`
	ChecksumSHA256  string `json:"checksum_sha256"`
	HMAC            []byte `json:"hmac"`
	Nonce           []byte `json:"nonce"`
	Timestamp       string `json:"timestamp"`
}

// RestoreAgentRequest is the request body for restoring a backup on the agent.
type RestoreAgentRequest struct {
	GCSPath       string `json:"gcs_path"`
	EncryptionKey []byte `json:"encryption_key"`
	Nonce         []byte `json:"nonce"`
	ExpectedHMAC  []byte `json:"expected_hmac"`
}

// BackupVM tells the agent to create a backup of a machine's data volume.
func (c *Client) BackupVM(ctx context.Context, host *store.Host, machineID string, encryptionKey []byte) (*BackupAgentResponse, error) {
	agentURL := c.agentURL(host) + "/vms/" + machineID + "/backup"

	body, _ := json.Marshal(BackupAgentRequest{EncryptionKey: encryptionKey})

	// Use longer timeout for backup uploads — 10GB at 100Mbps takes ~15 min
	longClient := &http.Client{Timeout: 30 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, "POST", agentURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create backup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.tokenForHost(host))
	req.Header.Set("Content-Type", "application/json")

	resp, err := longClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("backup VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("backup VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result BackupAgentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode backup response: %w", err)
	}
	return &result, nil
}

// RestoreVM tells the agent to restore a machine's data volume from a backup.
func (c *Client) RestoreVM(ctx context.Context, host *store.Host, machineID string, gcsPath string, encryptionKey, nonce, expectedHMAC []byte) error {
	agentURL := c.agentURL(host) + "/vms/" + machineID + "/restore"

	body, _ := json.Marshal(RestoreAgentRequest{
		GCSPath:       gcsPath,
		EncryptionKey: encryptionKey,
		Nonce:         nonce,
		ExpectedHMAC:  expectedHMAC,
	})

	longClient := &http.Client{Timeout: 30 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, "POST", agentURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create restore request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.tokenForHost(host))
	req.Header.Set("Content-Type", "application/json")

	resp, err := longClient.Do(req)
	if err != nil {
		return fmt.Errorf("restore VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("restore VM: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// AgentURL returns the base URL for reaching an agent on the given host.
func (c *Client) AgentURL(host *store.Host) string {
	return c.agentURL(host)
}

// TokenForHost returns the auth token for the given host.
func (c *Client) TokenForHost(host *store.Host) string {
	return c.tokenForHost(host)
}

// DeleteBackupObject deletes a backup object from GCS via the agent.
func (c *Client) DeleteBackupObject(ctx context.Context, host *store.Host, machineID, gcsPath string) error {
	agentURL := c.agentURL(host) + "/vms/" + machineID + "/backup-object?gcs_path=" + url.QueryEscape(gcsPath)

	req, err := http.NewRequestWithContext(ctx, "DELETE", agentURL, nil)
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.tokenForHost(host))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete backup object: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("agent returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// GetVM gets the status of a VM from the agent.
func (c *Client) GetVM(ctx context.Context, host *store.Host, machineID string) (*VMResponse, error) {
	url := c.agentURL(host) + "/vms/" + machineID

	resp, err := c.doRequest(ctx, "GET", url, nil, c.tokenForHost(host))
	if err != nil {
		return nil, fmt.Errorf("get VM from agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get VM: status %d", resp.StatusCode)
	}

	var vm VMResponse
	if err := json.NewDecoder(resp.Body).Decode(&vm); err != nil {
		return nil, fmt.Errorf("decode VM response: %w", err)
	}

	return &vm, nil
}

// UpdateVMSecrets pushes updated secrets to a running VM via the agent.
func (c *Client) UpdateVMSecrets(ctx context.Context, host *store.Host, machineID string, secrets map[string]string) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/secrets"

	body, err := json.Marshal(secrets)
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}

	resp, err := c.doRequest(ctx, "PATCH", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("update VM secrets on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update VM secrets: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// UpdateVMCredentials pushes refreshed LLM credentials to a running VM via the agent.
func (c *Client) UpdateVMCredentials(ctx context.Context, host *store.Host, machineID string, llmKeys map[string]metadata.CredentialEntry) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/credentials"

	body, err := json.Marshal(llmKeys)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	resp, err := c.doRequest(ctx, "PATCH", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("update VM credentials on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update VM credentials: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ReplaceVMCredentials replaces the full credential set for a running VM.
// Unlike UpdateVMCredentials (PATCH, merge), this uses PUT for full replace.
func (c *Client) ReplaceVMCredentials(ctx context.Context, host *store.Host, machineID string, llmKeys map[string]metadata.CredentialEntry) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/credentials"

	body, err := json.Marshal(llmKeys)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	resp, err := c.doRequest(ctx, "PUT", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("replace VM credentials on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("replace VM credentials: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// UpdateVMConfig pushes an updated OpenClaw config to a running VM via the agent.
func (c *Client) UpdateVMConfig(ctx context.Context, host *store.Host, machineID string, config []byte) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/config"

	resp, err := c.doRequest(ctx, "PATCH", url, bytes.NewReader(config), c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("update VM config on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update VM config: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// PluginReconcileResult is returned by the agent after reconciling plugins.
type PluginReconcileResult struct {
	PluginID string `json:"plugin_id"`
	Status   string `json:"status"` // "installed", "uninstalled", "failed"
	Version  string `json:"version"`
	Error    string `json:"error,omitempty"`
}

// ReconcilePlugins sends desired plugin state to the agent and returns results.
func (c *Client) ReconcilePlugins(ctx context.Context, host *store.Host, machineID string, plugins []store.MachinePluginWithCatalog) ([]PluginReconcileResult, error) {
	url := c.agentURL(host) + "/vms/" + machineID + "/reconcile-plugins"

	type pluginState struct {
		PluginID       string  `json:"plugin_id"`
		Enabled        bool    `json:"enabled"`
		InstallKind    string  `json:"install_kind"`
		ArtifactURL    *string `json:"artifact_url,omitempty"`
		CatalogVersion string  `json:"catalog_version"`
		InstallStatus  string  `json:"install_status"`
	}

	var states []pluginState
	for _, p := range plugins {
		states = append(states, pluginState{
			PluginID:       p.PluginID,
			Enabled:        p.Enabled,
			InstallKind:    p.InstallKind,
			ArtifactURL:    p.ArtifactURL,
			CatalogVersion: p.CatalogVersion,
			InstallStatus:  p.InstallStatus,
		})
	}

	body, err := json.Marshal(map[string]interface{}{"plugins": states})
	if err != nil {
		return nil, fmt.Errorf("marshal reconcile request: %w", err)
	}

	resp, err := c.doRequest(ctx, "POST", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return nil, fmt.Errorf("reconcile plugins on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("reconcile plugins: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Results []PluginReconcileResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode reconcile response: %w", err)
	}

	return result.Results, nil
}

// ListVMs fetches live VM stats (including disk usage) from the agent.
func (c *Client) ListVMs(ctx context.Context, host *store.Host) ([]VMStatsResponse, error) {
	url := c.agentURL(host) + "/vms"

	resp, err := c.doRequest(ctx, "GET", url, nil, c.tokenForHost(host))
	if err != nil {
		return nil, fmt.Errorf("list VMs on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list VMs: status %d", resp.StatusCode)
	}

	var vms []VMStatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&vms); err != nil {
		return nil, fmt.Errorf("decode VMs response: %w", err)
	}

	return vms, nil
}

// RefreshRootfs tells the agent to update its cached rootfs from the source image.
func (c *Client) RefreshRootfs(ctx context.Context, host *store.Host) error {
	url := c.agentURL(host) + "/refresh-rootfs"

	resp, err := c.doRequest(ctx, "POST", url, nil, c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("refresh rootfs on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("refresh rootfs: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// TriggerUpdate tells the agent to stop all VMs and self-update.
func (c *Client) TriggerUpdate(ctx context.Context, host *store.Host) error {
	url := c.agentURL(host) + "/trigger-update"

	resp, err := c.doRequest(ctx, "POST", url, nil, c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("trigger update on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 202 = accepted, 409 = already updating — both are "success" from backend perspective
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("trigger update: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ConfigureBrowserStorage asks the agent to mount or format dedicated XFS
// storage for browser VM rootfs/state, then optionally schedule an agent
// restart so BROWSER_STATE_DIR takes effect.
func (c *Client) ConfigureBrowserStorage(ctx context.Context, host *store.Host, device, mountPoint string, format, restartAgent bool) error {
	url := c.agentURL(host) + "/configure-browser-storage"

	body, err := json.Marshal(map[string]interface{}{
		"device":        device,
		"mount_point":   mountPoint,
		"format":        format,
		"restart_agent": restartAgent,
	})
	if err != nil {
		return fmt.Errorf("marshal configure browser storage request: %w", err)
	}

	resp, err := c.doRequest(ctx, "POST", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("configure browser storage on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("configure browser storage: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetLogs fetches the agent's recent logs.
func (c *Client) GetLogs(ctx context.Context, host *store.Host, lines int) (string, error) {
	url := fmt.Sprintf("%s/logs?lines=%d", c.agentURL(host), lines)

	resp, err := c.doRequest(ctx, "GET", url, nil, c.tokenForHost(host))
	if err != nil {
		return "", fmt.Errorf("get agent logs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get logs: status %d", resp.StatusCode)
	}

	var result struct {
		Logs string `json:"logs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode logs response: %w", err)
	}

	return result.Logs, nil
}

// BrowserVMHealthResponse is returned by the agent for a standalone browser VM.
type BrowserVMHealthResponse struct {
	BrowserVMID   string `json:"browser_vm_id"`
	VMIP          string `json:"vm_ip"`
	Status        string `json:"status"`
	CDPError      string `json:"cdp_error"`
	CDPReachable  bool   `json:"cdp_reachable"`
	CDPVersion    string `json:"cdp_version"`
	CDPLatencyMS  int64  `json:"cdp_latency_ms"`
	LiveReachable bool   `json:"live_reachable"`
	LiveLatencyMS int64  `json:"live_latency_ms"`
	LivePort      int    `json:"live_port"`
}

// CreateBrowserVM tells the agent to provision a new browser VM.
// The agent returns after Firecracker startup is durably recorded; CDP readiness
// is observed through GetBrowserVMHealth.
func (c *Client) CreateBrowserVM(ctx context.Context, host *store.Host, browserVMID, vmIP string, vcpus, memoryMB int, rootfsManifest, rootfsVersion string) error {
	url := c.agentURL(host) + "/browser-vms"

	payload := map[string]interface{}{
		"browser_vm_id": browserVMID,
		"vm_ip":         vmIP,
		"vcpus":         vcpus,
		"memory_mb":     memoryMB,
	}
	if strings.TrimSpace(rootfsManifest) != "" {
		payload["rootfs_manifest"] = strings.TrimSpace(rootfsManifest)
	}
	if strings.TrimSpace(rootfsVersion) != "" {
		payload["rootfs_version"] = strings.TrimSpace(rootfsVersion)
	}
	body, _ := json.Marshal(payload)

	// Use a dedicated HTTP client with a longer timeout for Firecracker startup.
	// CDP readiness is polled separately, but staging the rootfs and starting the
	// VM can still take longer than the default 30s client timeout.
	longClient := &http.Client{Timeout: browserVMCreateHTTPTimeout}
	if c.httpClient != nil {
		longClient.Transport = c.httpClient.Transport
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create browser VM request: %w", err)
	}
	token := c.tokenForHost(host)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := longClient.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(ctx.Err(), context.Canceled) ||
			errors.Is(ctx.Err(), context.DeadlineExceeded) ||
			(errors.As(err, &netErr) && netErr.Timeout()) {
			return fmt.Errorf("%w: create browser VM on agent: %v", ErrOutcomeUnknown, err)
		}
		return fmt.Errorf("create browser VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create browser VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetBrowserVMHealth fetches agent-local readiness for a browser VM.
func (c *Client) GetBrowserVMHealth(ctx context.Context, host *store.Host, browserVMID string) (*BrowserVMHealthResponse, error) {
	url := c.agentURL(host) + "/browser-vms/" + browserVMID + "/health"

	resp, err := c.doRequest(ctx, "GET", url, nil, c.tokenForHost(host))
	if err != nil {
		return nil, fmt.Errorf("browser VM health on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("browser VM health: %w", &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(respBody)})
	}

	var health BrowserVMHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decode browser VM health: %w", err)
	}
	return &health, nil
}

// DestroyBrowserVM tells the agent to stop and clean up a browser VM.
func (c *Client) DestroyBrowserVM(ctx context.Context, host *store.Host, browserVMID string) error {
	url := c.agentURL(host) + "/browser-vms/" + browserVMID

	resp, err := c.doRequest(ctx, "DELETE", url, nil, c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("destroy browser VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		slog.Info("agent.destroy_browser_vm.already_gone", "browser_vm_id", browserVMID, "host", host.ID)
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("destroy browser VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// PairBrowserVM tells the agent to associate a browser VM with a machine VM.
func (c *Client) PairBrowserVM(ctx context.Context, host *store.Host, browserVMID, machineVMIP string) error {
	url := c.agentURL(host) + "/browser-vms/" + browserVMID + "/pair"

	body, _ := json.Marshal(map[string]string{
		"machine_vm_ip": machineVMIP,
	})

	resp, err := c.doRequest(ctx, "POST", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("pair browser VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return ErrBrowserVMNotFound
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pair browser VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// UnpairBrowserVM tells the agent to disassociate a browser VM from a machine VM.
func (c *Client) UnpairBrowserVM(ctx context.Context, host *store.Host, browserVMID, machineVMIP string) error {
	url := c.agentURL(host) + "/browser-vms/" + browserVMID + "/unpair"

	body, _ := json.Marshal(map[string]string{
		"machine_vm_ip": machineVMIP,
	})

	resp, err := c.doRequest(ctx, "POST", url, bytes.NewReader(body), c.tokenForHost(host))
	if err != nil {
		return fmt.Errorf("unpair browser VM on agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unpair browser VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// WriteFile writes a file to a VM via the agent proxy's /write-file endpoint.
// Uses port 9091 (ProxyRouter) with X-Proxy-Token auth.
func (c *Client) WriteFile(ctx context.Context, host *store.Host, machineID, proxyToken, path, content string) error {
	targetURL := fmt.Sprintf("%s/proxy/%s/write-file", c.proxyURL(host), machineID)
	body, _ := json.Marshal(map[string]string{"path": path, "content": content})
	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Proxy-Token", proxyToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("write-file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("write-file: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// tokenForHost returns the appropriate auth token for the given host.
// Uses per-host token if set, falls back to fleet-wide token.
func (c *Client) tokenForHost(host *store.Host) string {
	if host.AgentToken != nil && *host.AgentToken != "" {
		return *host.AgentToken
	}
	return c.agentToken
}

// agentURL returns the base URL for the agent control API on the given host.
// Uses agent_endpoint if set (registered hosts), falls back to external/internal IP.
func (c *Client) agentURL(host *store.Host) string {
	if host.AgentEndpoint != nil && *host.AgentEndpoint != "" {
		ep := *host.AgentEndpoint
		u, err := url.Parse(ep)
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			// Reject known internal/metadata endpoints
			hostname := u.Hostname()
			if hostname != "metadata.google.internal" &&
				hostname != "169.254.169.254" &&
				hostname != "localhost" &&
				hostname != "127.0.0.1" {
				return ep
			}
		}
		// Fall through to IP-based URL if endpoint is invalid
	}
	// Legacy fallback: construct from ExternalIP or InternalIP
	ip := ""
	if host.ExternalIP != nil {
		ip = *host.ExternalIP
	} else if host.InternalIP != nil {
		ip = *host.InternalIP
	}
	return fmt.Sprintf("http://%s:9090", ip)
}

// ProxyURL returns the base URL for the agent proxy API on the given host.
// Exported for use by other packages (e.g., machine_exec).
func (c *Client) ProxyURL(host *store.Host) string { return c.proxyURL(host) }

// proxyURL returns the base URL for the agent proxy API on the given host.
// The proxy router runs on port 9091 (vs 9090 for the control API).
// For AgentEndpoint hosts, derives the proxy URL by replacing port 9090 with 9091.
func (c *Client) proxyURL(host *store.Host) string {
	if host.AgentEndpoint != nil && *host.AgentEndpoint != "" {
		ep := *host.AgentEndpoint
		u, err := url.Parse(ep)
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			hostname := u.Hostname()
			if hostname != "metadata.google.internal" &&
				hostname != "169.254.169.254" &&
				hostname != "localhost" &&
				hostname != "127.0.0.1" {
				// Replace port 9090 with 9091 for proxy router
				if u.Port() == "9090" {
					u.Host = hostname + ":9091"
				}
				return u.String()
			}
		}
	}
	ip := ""
	if host.ExternalIP != nil {
		ip = *host.ExternalIP
	} else if host.InternalIP != nil {
		ip = *host.InternalIP
	}
	return fmt.Sprintf("http://%s:9091", ip)
}

// doRequest executes an HTTP request with the given auth token.
func (c *Client) doRequest(ctx context.Context, method, url string, body io.Reader, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if traceID := tracing.TraceIDFromContext(ctx); traceID != "" {
		req.Header.Set("X-Trace-ID", traceID)
	}

	return c.httpClient.Do(req)
}

// ExecResult holds the output of a command executed inside a VM.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// ExecCommand runs a command inside a VM via the agent proxy exec endpoint.
func (c *Client) ExecCommand(ctx context.Context, host *store.Host, machineID, proxyToken string, command []string) (*ExecResult, error) {
	reqBody, err := json.Marshal(map[string]interface{}{"command": command})
	if err != nil {
		return nil, fmt.Errorf("marshal exec request: %w", err)
	}

	execURL := fmt.Sprintf("%s/proxy/%s/exec", c.proxyURL(host), machineID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, execURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create exec request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if proxyToken != "" {
		req.Header.Set("X-Proxy-Token", proxyToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exec request to agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read exec response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exec returned status %d: %s", resp.StatusCode, string(body))
	}

	var result ExecResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode exec response: %w", err)
	}
	return &result, nil
}

// RestartGateway signals the VM supervisor to restart the gateway process.
// Channel config changes require a gateway restart since hot-reload ignores channel mutations.
func (c *Client) RestartGateway(ctx context.Context, host *store.Host, machineID, proxyToken string) error {
	restartURL := fmt.Sprintf("%s/proxy/%s/restart-gateway", c.proxyURL(host), machineID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, restartURL, nil)
	if err != nil {
		return fmt.Errorf("create restart-gateway request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if proxyToken != "" {
		req.Header.Set("X-Proxy-Token", proxyToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("restart-gateway request to agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("restart-gateway: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// CheckGatewayHealth checks if the gateway inside the VM is healthy.
func (c *Client) CheckGatewayHealth(ctx context.Context, host *store.Host, machineID, proxyToken string) error {
	healthURL := fmt.Sprintf("%s/proxy/%s/gateway-health", c.proxyURL(host), machineID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return fmt.Errorf("create gateway-health request: %w", err)
	}
	if proxyToken != "" {
		req.Header.Set("X-Proxy-Token", proxyToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gateway-health request to agent: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway not healthy: status %d", resp.StatusCode)
	}
	return nil
}
