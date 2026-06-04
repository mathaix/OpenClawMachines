//go:build !linux

package orchestrator

import (
	"context"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/network"
)

// stubOrchestrator is a no-op implementation for non-Linux platforms.
type stubOrchestrator struct{}

// New returns a stub orchestrator on non-Linux platforms.
func New(cfg FirecrackerConfig, bridge *network.Bridge) (Orchestrator, error) {
	return &stubOrchestrator{}, nil
}

func (o *stubOrchestrator) Create(ctx context.Context, cfg VMConfig) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) Upgrade(ctx context.Context, cfg VMConfig) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) Stop(ctx context.Context, machineID string) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) Destroy(ctx context.Context, machineID string) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) Get(ctx context.Context, machineID string) (*VMInstance, error) {
	return nil, ErrNotLinux
}

func (o *stubOrchestrator) List(ctx context.Context) ([]VMInstance, error) {
	return nil, ErrNotLinux
}

func (o *stubOrchestrator) UpdateSecrets(ctx context.Context, machineID string, secrets map[string]string) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) UpdateConfig(ctx context.Context, machineID string, openClawConf []byte) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) UpdateLLMKeys(ctx context.Context, machineID string, llmKeys map[string]metadata.CredentialEntry) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) ReplaceLLMKeys(ctx context.Context, machineID string, llmKeys map[string]metadata.CredentialEntry) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) RegisterPending(cfg VMConfig) {}

func (o *stubOrchestrator) SetError(machineID, errMsg string) {}

func (o *stubOrchestrator) SetMetadataRegistrar(registrar MetadataRegistrar) {}

func (o *stubOrchestrator) Recover(ctx context.Context) error { return ErrNotLinux }

func (o *stubOrchestrator) RollbackDataVolume(machineID string) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) Drain(ctx context.Context) error {
	return nil
}

func (o *stubOrchestrator) Shutdown(ctx context.Context) error {
	return nil
}

func (o *stubOrchestrator) CreateBrowserVM(ctx context.Context, cfg BrowserVMConfig) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) DestroyBrowserVM(ctx context.Context, browserVMID string) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) PairBrowserVM(machineVMIP, browserVMIP string) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) UnpairBrowserVM(machineVMIP, browserVMIP string) error {
	return ErrNotLinux
}

func (o *stubOrchestrator) ListBrowserVMs(ctx context.Context) ([]BrowserVMInstance, error) {
	return nil, ErrNotLinux
}
