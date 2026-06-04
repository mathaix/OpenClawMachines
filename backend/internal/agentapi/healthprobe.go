package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// StartHealthProbes runs a background goroutine that periodically checks
// the health of each running VM's gateway and PTY server.
func (s *Server) StartHealthProbes(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if s.orchestrator == nil {
				continue
			}
			vms, err := s.orchestrator.List(ctx)
			if err != nil {
				continue
			}
			for _, vm := range vms {
				if vm.Status != "running" {
					continue
				}
				// Check auth proxy (port 8080) is reachable
				authProxyOk := probePort(vm.VMIP, 8080, 2*time.Second)
				// Check actual gateway health via PTY supervisor endpoint
				gwOk := probeGatewayHealth(vm.VMIP, 2*time.Second)
				if !authProxyOk || !gwOk {
					slog.Warn("vm.health_degraded",
						"machine_id", vm.MachineID,
						"auth_proxy_ok", authProxyOk,
						"gateway_ok", gwOk,
					)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// probePort tests if a TCP port is reachable within the given timeout.
func probePort(ip string, port int, timeout time.Duration) bool {
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func probeCDPVersion(ip string, timeout time.Duration) (string, error) {
	url := fmt.Sprintf("http://%s:9222/json/version", ip)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cdp version status %d", resp.StatusCode)
	}
	var version struct {
		Browser string `json:"Browser"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		return "", err
	}
	return version.Browser, nil
}

// probeGatewayHealth checks the actual gateway status via the PTY server's
// /health endpoint (port 7681, binds to 0.0.0.0). The PTY supervisor reports
// whether the gateway process is running, in a crash loop, or unknown.
// Returns true only if the gateway is reported as "running".
func probeGatewayHealth(ip string, timeout time.Duration) bool {
	url := fmt.Sprintf("http://%s:7681/health", ip)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		slog.Debug("probe.gateway_health_unreachable", "url", url, "error", err)
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		slog.Debug("probe.gateway_health_bad_status", "url", url, "status", resp.StatusCode)
		return false
	}
	var health struct {
		Gateway string `json:"gateway"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		slog.Debug("probe.gateway_health_decode_error", "url", url, "error", err)
		return false
	}
	if health.Gateway != "running" {
		slog.Debug("probe.gateway_not_running", "url", url, "gateway", health.Gateway, "status", health.Status)
	}
	return health.Gateway == "running"
}
