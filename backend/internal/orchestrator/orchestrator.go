package orchestrator

import (
	"context"
	"errors"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

// ErrNotLinux is returned when Firecracker operations are called on non-Linux platforms.
var ErrNotLinux = errors.New("orchestrator: Firecracker requires Linux")

// ErrUpgradeRestoredPreviousVM indicates an in-place recreate failed but the prior VM
// was successfully recreated and should remain the active instance.
var ErrUpgradeRestoredPreviousVM = errors.New("orchestrator: upgrade failed and previous VM was restored")

// ErrVMNotFound is returned when an operation references a machine ID that
// the orchestrator does not have a VM for.
var ErrVMNotFound = errors.New("orchestrator: VM not found")

// Orchestrator manages the lifecycle of Firecracker MicroVMs on a Host.
type Orchestrator interface {
	// Create boots a Firecracker MicroVM for the given config.
	Create(ctx context.Context, cfg VMConfig) error

	// Upgrade recreates a MicroVM in place while preserving its data volume.
	Upgrade(ctx context.Context, cfg VMConfig) error

	// Stop gracefully stops a MicroVM but preserves the data volume for restart.
	Stop(ctx context.Context, machineID string) error

	// Destroy stops and cleans up a MicroVM, including its data volume.
	Destroy(ctx context.Context, machineID string) error

	// Get returns the state of a running VM.
	Get(ctx context.Context, machineID string) (*VMInstance, error)

	// List returns all running VMs.
	List(ctx context.Context) ([]VMInstance, error)

	// UpdateSecrets pushes updated secrets to a running VM's metadata.
	UpdateSecrets(ctx context.Context, machineID string, secrets map[string]string) error

	// UpdateConfig pushes an updated OpenClaw config to a running VM's metadata.
	UpdateConfig(ctx context.Context, machineID string, openClawConf []byte) error

	// UpdateLLMKeys pushes refreshed LLM credentials to a running VM's metadata.
	UpdateLLMKeys(ctx context.Context, machineID string, llmKeys map[string]metadata.CredentialEntry) error

	// ReplaceLLMKeys replaces the full credential set for a running VM's metadata.
	// Unlike UpdateLLMKeys (which merges), this removes any key not in the new map.
	ReplaceLLMKeys(ctx context.Context, machineID string, llmKeys map[string]metadata.CredentialEntry) error

	// RegisterPending pre-registers a VM in "creating" state before async creation.
	RegisterPending(cfg VMConfig)

	// SetError marks a VM as failed with an error message.
	SetError(machineID, errMsg string)

	// SetMetadataRegistrar connects the orchestrator to the metadata server
	// so VMs are registered/unregistered automatically during create/destroy.
	SetMetadataRegistrar(registrar MetadataRegistrar)

	// Recover reconstructs in-memory state for persisted VMs after host restart.
	Recover(ctx context.Context) error

	// RollbackDataVolume restores the pre-upgrade backup of a data volume.
	RollbackDataVolume(machineID string) error

	// Drain gracefully stops all VMs and cleans up ephemeral resources.
	Drain(ctx context.Context) error

	// Shutdown persists state and releases non-VM resources without stopping VMs.
	Shutdown(ctx context.Context) error

	// CreateBrowserVM boots a standalone browser VM.
	CreateBrowserVM(ctx context.Context, cfg BrowserVMConfig) error

	// DestroyBrowserVM stops and cleans up a standalone browser VM.
	DestroyBrowserVM(ctx context.Context, browserVMID string) error

	// PairBrowserVM adds firewall rules to allow traffic between a machine VM and a browser VM.
	PairBrowserVM(machineVMIP, browserVMIP string) error

	// UnpairBrowserVM removes firewall rules between a machine VM and a browser VM.
	UnpairBrowserVM(machineVMIP, browserVMIP string) error

	// ListBrowserVMs returns all standalone browser VMs.
	ListBrowserVMs(ctx context.Context) ([]BrowserVMInstance, error)
}

// BrowserVMInstance represents a running standalone browser VM.
type BrowserVMInstance struct {
	ID         string `json:"id"`
	VMIP       string `json:"vm_ip"`
	Status     string `json:"status"`
	CDPPort    int    `json:"cdp_port"`
	TapDevice  string `json:"tap_device"`
	SocketPath string `json:"socket_path"`
	CDPError   string `json:"cdp_error,omitempty"`
}

// MetadataRegistrar is implemented by the metadata server.
type MetadataRegistrar interface {
	RegisterMachine(vmIP string, config metadata.MachineConfig)
	UnregisterMachine(vmIP string)
	UpdateMachineSecrets(vmIP string, secrets map[string]string)
	UpdateMachineConfig(vmIP string, openClawConf []byte)
	UpdateMachineLLMKeys(vmIP string, llmKeys map[string]metadata.CredentialEntry)
	ReplaceMachineLLMKeys(vmIP string, llmKeys map[string]metadata.CredentialEntry)
}
