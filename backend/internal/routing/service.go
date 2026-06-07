// Package routing owns all routing side-effects: Cloudflare Tunnel creation,
// DNS provisioning, KV cache writes, and database updates.
package routing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
)

const defaultDataPlaneDomain = "localhost"

// TunnelManager creates and manages Cloudflare Tunnels and DNS records.
type TunnelManager interface {
	// CreateVMTunnel creates a new tunnel for a VM and returns the tunnel ID and connector token.
	CreateVMTunnel(ctx context.Context, slug string) (tunnelID, token string, err error)
	// ConfigureVMTunnel sets ingress rules for HTTP and SSH hostnames.
	ConfigureVMTunnel(ctx context.Context, tunnelID, httpHost, sshHost string) error
	// CreateDNSRoute creates a proxied CNAME record pointing hostname to the tunnel.
	CreateDNSRoute(ctx context.Context, tunnelID, hostname string) error
	// DeleteTunnelAndDNS removes DNS records for all given hostnames and deletes the tunnel.
	DeleteTunnelAndDNS(ctx context.Context, tunnelID string, hostnames ...string) error
}

// KVWriter writes routing and account entries to the Cloudflare KV cache.
type KVWriter interface {
	// PutRouteSync writes a route entry for the given account+machine slugs.
	PutRouteSync(ctx context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error
	// DeleteRouteSync removes the route entry for the given account+machine slugs.
	DeleteRouteSync(ctx context.Context, accountSlug, machineSlug string) error
	// PutAccount writes an account entry keyed by slug.
	PutAccount(ctx context.Context, slug string, entry KVAccountEntry) error
}

// RouteSnapshot captures the current routing state of a running machine.
type RouteSnapshot struct {
	AccountSlug  string
	MachineID    string
	MachineSlug  string
	HostHostname string
	ProxyToken   string
}

// RouteReader reads route state from the database for reconciliation.
type RouteReader interface {
	// ListRunningRoutes returns snapshots for all currently-running machines.
	ListRunningRoutes(ctx context.Context) ([]RouteSnapshot, error)
}

// RouteStore is the subset of the database required by Service.
type RouteStore interface {
	// UpdateMachineTunnel persists the tunnel ID and signing key for a machine.
	UpdateMachineTunnel(ctx context.Context, machineID, tunnelID, signingKey string) error
	// ClearMachineTunnel removes the tunnel ID and signing key for a machine.
	ClearMachineTunnel(ctx context.Context, machineID string) error
}

// KVRouteEntry is the value stored in KV under route:{accountSlug}:{machineSlug}.
type KVRouteEntry struct {
	MachineID    string `json:"machine_id"`
	HostHostname string `json:"host_hostname"`
	ProxyToken   string `json:"proxy_token"`
}

// KVAccountEntry is the value stored in KV under account:{accountSlug}.
type KVAccountEntry struct {
	AccountID int   `json:"account_id"`
	UserIDs   []int `json:"user_ids"`
}

// SetupRequest holds the parameters needed to provision routing for a machine.
type SetupRequest struct {
	MachineID   string
	MachineSlug string
}

// SetupResult contains the routing artifacts created for a machine.
type SetupResult struct {
	TunnelID    string
	TunnelToken string
	SigningKey  string
	VMHostname  string
	SSHHostname string
}

// TeardownRequest holds the parameters needed to remove routing for a machine.
type TeardownRequest struct {
	MachineID   string
	MachineSlug string
	AccountSlug string
	TunnelID    string
}

// SyncKVRequest holds the parameters needed to write a route entry to KV.
type SyncKVRequest struct {
	AccountSlug  string
	MachineSlug  string
	MachineID    string
	HostHostname string
	ProxyToken   string
}

// Service owns all routing side-effects for a machine.
type Service struct {
	tunnel          TunnelManager
	kv              KVWriter
	store           RouteStore
	dataPlaneDomain string
}

// New creates a Service. tunnel, kv, and store may be nil (operations are skipped if so).
func New(tunnel TunnelManager, kv KVWriter, store RouteStore) *Service {
	return NewWithDomain(tunnel, kv, store, defaultDataPlaneDomain)
}

// NewWithDomain creates a Service using an operator-supplied data-plane domain.
func NewWithDomain(tunnel TunnelManager, kv KVWriter, store RouteStore, dataPlaneDomain string) *Service {
	domain := strings.Trim(strings.TrimSpace(dataPlaneDomain), ".")
	if domain == "" {
		domain = defaultDataPlaneDomain
	}
	return &Service{
		tunnel:          tunnel,
		kv:              kv,
		store:           store,
		dataPlaneDomain: domain,
	}
}

func (s *Service) hostnames(machineSlug string) (vmHostname, sshHostname string) {
	return "m-" + machineSlug + "." + s.dataPlaneDomain, "ssh-" + machineSlug + "." + s.dataPlaneDomain
}

// Hostnames returns the data-plane HTTP and SSH hostnames for a machine slug.
func (s *Service) Hostnames(machineSlug string) (vmHostname, sshHostname string) {
	return s.hostnames(machineSlug)
}

// SetupRoute provisions Cloudflare Tunnel, DNS records, and stores tunnel info in the database.
//
// If tunnel creation fails, a partial result is returned (VMHostname set, TunnelID empty).
// The machine can still work via the proxy chain in that case.
func (s *Service) SetupRoute(ctx context.Context, req SetupRequest) (*SetupResult, error) {
	vmHostname, sshHostname := s.hostnames(req.MachineSlug)

	result := &SetupResult{
		VMHostname:  vmHostname,
		SSHHostname: sshHostname,
	}

	// Generate a 32-byte signing key up front. The in-VM gateway needs it to
	// verify signed proxy/gateway tokens, so it must exist even when no
	// Cloudflare tunnel is configured (local/operator profiles).
	signingKeyBytes := make([]byte, 32)
	if _, err := rand.Read(signingKeyBytes); err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	signingKey := hex.EncodeToString(signingKeyBytes)
	result.SigningKey = signingKey

	if s.tunnel == nil {
		return result, nil
	}

	// Create the per-VM tunnel.
	tunnelID, tunnelToken, err := s.tunnel.CreateVMTunnel(ctx, req.MachineSlug)
	if err != nil {
		slog.Error("routing.setup.tunnel.create_failed",
			"machine_id", req.MachineID,
			"machine_slug", req.MachineSlug,
			"error", err)
		// Non-fatal: return partial result with VMHostname set.
		return result, nil
	}
	result.TunnelID = tunnelID
	result.TunnelToken = tunnelToken

	// Configure tunnel ingress rules (HTTP + SSH).
	if err := s.tunnel.ConfigureVMTunnel(ctx, tunnelID, vmHostname, sshHostname); err != nil {
		slog.Error("routing.setup.tunnel.configure_failed",
			"machine_id", req.MachineID,
			"tunnel_id", tunnelID,
			"error", err)
	}

	// Create DNS records for both hostnames.
	if err := s.tunnel.CreateDNSRoute(ctx, tunnelID, vmHostname); err != nil {
		slog.Error("routing.setup.tunnel.dns_failed",
			"machine_id", req.MachineID,
			"hostname", vmHostname,
			"error", err)
	}
	if err := s.tunnel.CreateDNSRoute(ctx, tunnelID, sshHostname); err != nil {
		slog.Error("routing.setup.tunnel.ssh_dns_failed",
			"machine_id", req.MachineID,
			"hostname", sshHostname,
			"error", err)
	}

	// Persist tunnel info to the database.
	if s.store != nil {
		if err := s.store.UpdateMachineTunnel(ctx, req.MachineID, tunnelID, signingKey); err != nil {
			slog.Error("routing.setup.tunnel.store_failed",
				"machine_id", req.MachineID,
				"tunnel_id", tunnelID,
				"error", err)
		}
	}

	slog.Info("routing.setup.done",
		"machine_id", req.MachineID,
		"tunnel_id", tunnelID,
		"vm_hostname", vmHostname,
		"ssh_hostname", sshHostname)

	return result, nil
}

// TeardownRoute removes the KV route entry, deletes the Cloudflare Tunnel and DNS records,
// and clears the tunnel info from the database. All steps are best-effort: errors are logged
// but do not prevent subsequent cleanup steps.
func (s *Service) TeardownRoute(ctx context.Context, req TeardownRequest) error {
	// Always try to remove the KV entry, even if the tunnel is missing.
	if s.kv != nil && req.AccountSlug != "" && req.MachineSlug != "" {
		if err := s.kv.DeleteRouteSync(ctx, req.AccountSlug, req.MachineSlug); err != nil {
			slog.Error("routing.teardown.kv.delete_failed",
				"machine_id", req.MachineID,
				"account_slug", req.AccountSlug,
				"machine_slug", req.MachineSlug,
				"error", err)
		}
	}

	// Delete the tunnel and DNS records if we have a tunnel ID.
	if s.tunnel != nil && req.TunnelID != "" {
		vmHostname, sshHostname := s.hostnames(req.MachineSlug)

		if err := s.tunnel.DeleteTunnelAndDNS(ctx, req.TunnelID, vmHostname, sshHostname); err != nil {
			slog.Error("routing.teardown.tunnel.delete_failed",
				"machine_id", req.MachineID,
				"tunnel_id", req.TunnelID,
				"error", err)
		}
	}

	// Clear tunnel info from the database.
	if s.store != nil {
		if err := s.store.ClearMachineTunnel(ctx, req.MachineID); err != nil {
			slog.Error("routing.teardown.store.clear_failed",
				"machine_id", req.MachineID,
				"error", err)
		}
	}

	return nil
}

// SyncRouteToKV writes the route entry for a machine to the KV cache.
// Returns nil if the KV writer is nil (backward compatibility).
func (s *Service) SyncRouteToKV(ctx context.Context, req SyncKVRequest) error {
	if s.kv == nil {
		return nil
	}
	entry := KVRouteEntry{
		MachineID:    req.MachineID,
		HostHostname: req.HostHostname,
		ProxyToken:   req.ProxyToken,
	}
	return s.kv.PutRouteSync(ctx, req.AccountSlug, req.MachineSlug, entry)
}

// SyncAccountToKV writes the account entry for an account to the KV cache.
// Returns nil if the KV writer is nil.
func (s *Service) SyncAccountToKV(ctx context.Context, slug string, accountID int, userIDs []int) error {
	if s.kv == nil {
		return nil
	}
	entry := KVAccountEntry{
		AccountID: accountID,
		UserIDs:   userIDs,
	}
	return s.kv.PutAccount(ctx, slug, entry)
}
