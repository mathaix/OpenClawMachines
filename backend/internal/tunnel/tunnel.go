package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Manager handles Cloudflare Tunnel lifecycle — create, configure, DNS, delete.
type Manager struct {
	apiToken  string
	accountID string
	zoneID    string
	client    *http.Client
}

// New creates a tunnel Manager. Returns nil if required config is missing.
func New(apiToken, accountID, zoneID string) *Manager {
	if apiToken == "" || accountID == "" {
		return nil
	}
	return &Manager{
		apiToken:  apiToken,
		accountID: accountID,
		zoneID:    zoneID,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// cfResponse is the generic Cloudflare API response envelope.
type cfResponse struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (r *cfResponse) err(action string, statusCode int) error {
	if len(r.Errors) > 0 {
		return fmt.Errorf("%s: %s (status %d)", action, r.Errors[0].Message, statusCode)
	}
	return fmt.Errorf("%s: status %d", action, statusCode)
}

// CreateTunnel creates a new Cloudflare Tunnel and returns its ID and connector token.
func (m *Manager) CreateTunnel(ctx context.Context, name string) (tunnelID string, token string, err error) {
	tunnelName := "ocm-" + name

	body, _ := json.Marshal(map[string]interface{}{
		"name":       tunnelName,
		"config_src": "cloudflare",
	})

	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel", m.accountID)
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("create tunnel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var cfResp cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return "", "", fmt.Errorf("decode create tunnel response: %w", err)
	}
	if !cfResp.Success {
		return "", "", cfResp.err("create tunnel", resp.StatusCode)
	}

	var result struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(cfResp.Result, &result); err != nil {
		return "", "", fmt.Errorf("unmarshal tunnel result: %w", err)
	}

	slog.Info("tunnel.created", "tunnel_name", tunnelName, "tunnel_id", result.ID)
	return result.ID, result.Token, nil
}

// ConfigureTunnel sets ingress rules: hostname → http://localhost:9091, catch-all → 404.
func (m *Manager) ConfigureTunnel(ctx context.Context, tunnelID, hostname string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"config": map[string]interface{}{
			"ingress": []map[string]interface{}{
				{
					"hostname": hostname,
					"service":  "http://localhost:9091",
				},
				{
					"service": "http_status:404",
				},
			},
		},
	})

	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel/%s/configurations",
		m.accountID, tunnelID)
	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("configure tunnel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var cfResp cfResponse
		_ = json.NewDecoder(resp.Body).Decode(&cfResp)
		return cfResp.err("configure tunnel", resp.StatusCode)
	}

	slog.Info("tunnel.configured", "tunnel_id", tunnelID, "hostname", hostname)
	return nil
}

// CreateDNSRoute creates a proxied CNAME record: hostname → {tunnelID}.cfargotunnel.com.
func (m *Manager) CreateDNSRoute(ctx context.Context, tunnelID, hostname string) error {
	if m.zoneID == "" {
		return fmt.Errorf("zone ID not configured — cannot create DNS record")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"type":    "CNAME",
		"name":    hostname,
		"content": tunnelID + ".cfargotunnel.com",
		"proxied": true,
		"ttl":     1, // auto
	})

	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records", m.zoneID)
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("create DNS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var cfResp cfResponse
		_ = json.NewDecoder(resp.Body).Decode(&cfResp)
		return cfResp.err("create DNS record", resp.StatusCode)
	}

	slog.Info("tunnel.dns.created", "hostname", hostname, "tunnel_id", tunnelID)
	return nil
}

// DeleteTunnel deletes a Cloudflare Tunnel by ID.
func (m *Manager) DeleteTunnel(ctx context.Context, tunnelID string) error {
	if tunnelID == "" {
		return nil
	}

	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel/%s?cascade=true",
		m.accountID, tunnelID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete tunnel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete tunnel: status %d: %s", resp.StatusCode, string(body))
	}

	slog.Info("tunnel.deleted", "tunnel_id", tunnelID)
	return nil
}

// DeleteDNSRoute finds and deletes the CNAME record for a hostname.
func (m *Manager) DeleteDNSRoute(ctx context.Context, hostname string) error {
	if m.zoneID == "" || hostname == "" {
		return nil
	}

	// Find the record
	findURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?name=%s&type=CNAME",
		m.zoneID, hostname)
	req, err := http.NewRequestWithContext(ctx, "GET", findURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("find DNS record: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var cfResp cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return fmt.Errorf("decode DNS list: %w", err)
	}

	var records []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(cfResp.Result, &records); err != nil {
		return fmt.Errorf("unmarshal DNS records: %w", err)
	}

	for _, rec := range records {
		delURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s",
			m.zoneID, rec.ID)
		delReq, err := http.NewRequestWithContext(ctx, "DELETE", delURL, nil)
		if err != nil {
			return err
		}
		delReq.Header.Set("Authorization", "Bearer "+m.apiToken)

		delResp, err := m.client.Do(delReq)
		if err != nil {
			return fmt.Errorf("delete DNS record: %w", err)
		}
		_ = delResp.Body.Close()
		slog.Info("tunnel.dns.deleted", "record_id", rec.ID, "hostname", hostname)
	}

	return nil
}

// TunnelInfo contains basic metadata about a Cloudflare Tunnel.
type TunnelInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateVMTunnel creates a Cloudflare Tunnel for a VM and returns its ID and connector token.
// If a tunnel with the same name already exists (orphaned from a failed stop/delete),
// it is deleted first and creation is retried.
func (m *Manager) CreateVMTunnel(ctx context.Context, machineSlug string) (tunnelID string, token string, err error) {
	tunnelName := "ocm-vm-" + machineSlug
	return m.createVMTunnelOnce(ctx, tunnelName, true)
}

func (m *Manager) createVMTunnelOnce(ctx context.Context, tunnelName string, retryOnConflict bool) (string, string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"name":       tunnelName,
		"config_src": "cloudflare",
	})

	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel", m.accountID)
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("create VM tunnel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var cfResp cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return "", "", fmt.Errorf("decode create VM tunnel response: %w", err)
	}

	// Handle 409 conflict: orphaned tunnel with the same name exists in Cloudflare.
	// Delete it and retry once.
	if resp.StatusCode == http.StatusConflict && retryOnConflict {
		slog.Warn("tunnel.vm.conflict", "tunnel_name", tunnelName, "action", "deleting_orphaned_tunnel")
		if err := m.deleteOrphanedTunnel(ctx, tunnelName); err != nil {
			slog.Error("tunnel.vm.orphan_cleanup_failed", "tunnel_name", tunnelName, "error", err)
			return "", "", cfResp.err("create VM tunnel", resp.StatusCode)
		}
		return m.createVMTunnelOnce(ctx, tunnelName, false)
	}

	if !cfResp.Success {
		return "", "", cfResp.err("create VM tunnel", resp.StatusCode)
	}

	var result struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(cfResp.Result, &result); err != nil {
		return "", "", fmt.Errorf("unmarshal VM tunnel result: %w", err)
	}

	slog.Info("tunnel.vm.created", "tunnel_name", tunnelName, "tunnel_id", result.ID)
	return result.ID, result.Token, nil
}

// deleteOrphanedTunnel finds and deletes a tunnel by name. Used to clean up
// tunnels that exist in Cloudflare but were lost from the database.
func (m *Manager) deleteOrphanedTunnel(ctx context.Context, tunnelName string) error {
	// Find tunnel ID by name
	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel?name=%s&is_deleted=false",
		m.accountID, tunnelName)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("list tunnels: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var cfResp cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return fmt.Errorf("decode tunnel list: %w", err)
	}

	var tunnels []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(cfResp.Result, &tunnels); err != nil {
		return fmt.Errorf("unmarshal tunnels: %w", err)
	}

	if len(tunnels) == 0 {
		// Tunnel was already cleaned up (e.g., by the reaper) — not an error.
		slog.Info("tunnel.vm.orphan_already_gone", "tunnel_name", tunnelName)
		return nil
	}

	// Force-delete the orphaned tunnel (cascade=true cleans up active connections)
	tunnelID := tunnels[0].ID
	slog.Info("tunnel.vm.deleting_orphan", "tunnel_name", tunnelName, "tunnel_id", tunnelID)

	u = fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel/%s?cascade=true",
		m.accountID, tunnelID)
	req, err = http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)

	resp, err = m.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete orphaned tunnel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete orphaned tunnel: status %d: %s", resp.StatusCode, string(body))
	}

	slog.Info("tunnel.vm.orphan_deleted", "tunnel_name", tunnelName, "tunnel_id", tunnelID)
	return nil
}

// ConfigureVMTunnel sets ingress rules for a VM tunnel:
//   - httpHostname → http://localhost:8080 (authproxy)
//   - sshHostname  → ssh://localhost:22
//   - catch-all    → 404
func (m *Manager) ConfigureVMTunnel(ctx context.Context, tunnelID, httpHostname, sshHostname string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"config": map[string]interface{}{
			"ingress": []map[string]interface{}{
				{
					"hostname": httpHostname,
					"service":  "http://localhost:8080",
				},
				{
					"hostname": sshHostname,
					"service":  "ssh://localhost:22",
				},
				{
					"service": "http_status:404",
				},
			},
		},
	})

	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel/%s/configurations",
		m.accountID, tunnelID)
	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("configure VM tunnel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var cfResp cfResponse
		_ = json.NewDecoder(resp.Body).Decode(&cfResp)
		return cfResp.err("configure VM tunnel", resp.StatusCode)
	}

	slog.Info("tunnel.vm.configured", "tunnel_id", tunnelID, "http_hostname", httpHostname, "ssh_hostname", sshHostname)
	return nil
}

// ListTunnels returns all non-deleted tunnels in the account.
func (m *Manager) ListTunnels(ctx context.Context) ([]TunnelInfo, error) {
	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel?is_deleted=false&per_page=1000", m.accountID)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list tunnels: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var cfResp cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return nil, fmt.Errorf("decode tunnel list: %w", err)
	}
	if !cfResp.Success {
		return nil, cfResp.err("list tunnels", resp.StatusCode)
	}

	var tunnels []TunnelInfo
	if err := json.Unmarshal(cfResp.Result, &tunnels); err != nil {
		return nil, fmt.Errorf("unmarshal tunnel list: %w", err)
	}
	return tunnels, nil
}

// DeleteTunnelAndDNS removes DNS routes for all given hostnames and then deletes the tunnel.
func (m *Manager) DeleteTunnelAndDNS(ctx context.Context, tunnelID string, hostnames ...string) error {
	for _, hostname := range hostnames {
		if err := m.DeleteDNSRoute(ctx, hostname); err != nil {
			return fmt.Errorf("delete DNS route for %s: %w", hostname, err)
		}
	}
	if err := m.DeleteTunnel(ctx, tunnelID); err != nil {
		return fmt.Errorf("delete tunnel: %w", err)
	}
	return nil
}

// FindTunnelIDByName looks up a tunnel by name and returns its ID.
func (m *Manager) FindTunnelIDByName(ctx context.Context, name string) (string, error) {
	tunnelName := "ocm-" + name

	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/cfd_tunnel?name=%s&is_deleted=false",
		m.accountID, tunnelName)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("list tunnels: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var cfResp cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return "", fmt.Errorf("decode tunnel list: %w", err)
	}

	var tunnels []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(cfResp.Result, &tunnels); err != nil {
		return "", fmt.Errorf("unmarshal tunnels: %w", err)
	}

	if len(tunnels) == 0 {
		return "", fmt.Errorf("tunnel %s not found", tunnelName)
	}
	return tunnels[0].ID, nil
}
