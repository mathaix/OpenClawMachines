package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// TunnelCreator is the subset of tunnel.Manager used by enrollment.
type TunnelCreator interface {
	CreateTunnel(ctx context.Context, name string) (tunnelID string, token string, err error)
	ConfigureTunnel(ctx context.Context, tunnelID, hostname string) error
	CreateDNSRoute(ctx context.Context, tunnelID, hostname string) error
	DeleteTunnel(ctx context.Context, tunnelID string) error
	DeleteTunnelAndDNS(ctx context.Context, tunnelID string, hostnames ...string) error
}

// handleCreateEnrollmentToken creates a new enrollment token for host registration.
func (s *Server) handleCreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider       string          `json:"provider"`
		ProviderClass  string          `json:"provider_class"`
		Labels         json.RawMessage `json:"labels"`
		ExpiresInHours int             `json:"expires_in_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Provider == "" {
		req.Provider = "ovhcloud"
	}
	if req.ProviderClass == "" {
		req.ProviderClass = "registered"
	}
	if req.ExpiresInHours <= 0 {
		req.ExpiresInHours = 24
	}
	if req.Labels == nil {
		req.Labels = json.RawMessage(`{}`)
	}

	token := &store.EnrollmentToken{
		Provider:      req.Provider,
		ProviderClass: req.ProviderClass,
		Labels:        req.Labels,
		ExpiresAt:     time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour),
	}

	if err := s.store.CreateEnrollmentToken(r.Context(), token); err != nil {
		slog.Error("enrollment.create_token.failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create enrollment token")
		return
	}

	installCmd := fmt.Sprintf("curl -sL %s/api/agent/install | bash -s -- %s", s.backendURL, token.Token)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"token":           token.Token,
		"expires_at":      token.ExpiresAt,
		"install_command": installCmd,
	})
}

// handleListEnrollmentTokens lists all enrollment tokens.
func (s *Server) handleListEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListEnrollmentTokens(r.Context())
	if err != nil {
		slog.Error("enrollment.list_tokens.failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list enrollment tokens")
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

// handleAgentRegister handles agent self-registration with an enrollment token.
func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnrollmentToken   string          `json:"enrollment_token"`
		AgentEndpoint     string          `json:"agent_endpoint"`
		AgentEndpointType string          `json:"agent_endpoint_type"`
		Capabilities      json.RawMessage `json:"capabilities"`
		Labels            json.RawMessage `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.EnrollmentToken == "" {
		writeError(w, http.StatusBadRequest, "enrollment_token is required")
		return
	}

	ctx := r.Context()

	// 1. Validate enrollment token
	enrollToken, err := s.store.GetEnrollmentToken(ctx, req.EnrollmentToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid enrollment token")
		return
	}
	if enrollToken.UsedByHostID != nil {
		writeError(w, http.StatusConflict, "enrollment token already used")
		return
	}
	if time.Now().After(enrollToken.ExpiresAt) {
		writeError(w, http.StatusUnauthorized, "enrollment token expired")
		return
	}

	// 2. Generate per-host agent token
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate agent token")
		return
	}
	agentToken := "ocm_agent_" + fmt.Sprintf("%x", tokenBytes)

	// 3. Set defaults
	if req.AgentEndpointType == "" {
		req.AgentEndpointType = "public_http"
	}
	if req.Capabilities == nil {
		req.Capabilities = json.RawMessage(`{}`)
	}
	if req.Labels == nil {
		req.Labels = json.RawMessage(`{}`)
	}

	// 4. Create host record
	//
	// Set source_image from the placement service's expected image so that
	// enrolled hosts are immediately eligible for placement without manual
	// DB fixup (see docs/ovh-host-setup.md Step 7).
	var sourceImage *string
	if s.placement != nil {
		if img := s.placement.ExpectedImage(); img != "" {
			sourceImage = &img
		}
	}

	host := &store.Host{
		VMName:            fmt.Sprintf("ocm-host-%d", time.Now().UnixMilli()),
		Zone:              "external",
		Region:            "external",
		MachineType:       "registered",
		Status:            "provisioning",
		SourceImage:       sourceImage,
		Provider:          enrollToken.Provider,
		ProviderClass:     enrollToken.ProviderClass,
		LifecycleMode:     "registered",
		AgentEndpoint:     &req.AgentEndpoint,
		AgentEndpointType: req.AgentEndpointType,
		ProviderMetadata:  json.RawMessage(`{}`),
		Capabilities:      req.Capabilities,
		Labels:            req.Labels,
		AgentToken:        &agentToken,
	}

	// Extract capacity from capabilities if provided.
	// Physical CPU threads are multiplied by the oversubscription ratio to
	// derive capacity_vcpus, matching the behaviour in handleProvisionHost.
	var caps struct {
		CPUThreads int `json:"cpu_threads"`
		MemoryMB   int `json:"memory_mb"`
		DiskMB     int `json:"disk_mb"`
	}
	if err := json.Unmarshal(req.Capabilities, &caps); err == nil {
		if caps.CPUThreads > 0 {
			host.CapacityVCPUs = caps.CPUThreads * s.vcpuOversubRatio
		}
		if caps.MemoryMB > 0 {
			host.CapacityMemoryMB = caps.MemoryMB
		}
		if caps.DiskMB > 0 {
			host.CapacityDiskMB = caps.DiskMB
		}
	}

	if err := s.store.CreateRegisteredHost(ctx, host); err != nil {
		slog.Error("enrollment.register.create_host_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create host record")
		return
	}

	// 4b. Create Cloudflare tunnel for the host
	var tunnelToken string
	if s.tunnelCreator != nil {
		tunnelHostname := host.VMName + ".openclawmachines.com"
		tunnelID, tToken, err := s.tunnelCreator.CreateTunnel(ctx, host.VMName)
		if err != nil {
			slog.Error("enrollment.tunnel.create_failed", "host_id", host.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create tunnel")
			return
		}

		if err := s.tunnelCreator.ConfigureTunnel(ctx, tunnelID, tunnelHostname); err != nil {
			slog.Error("enrollment.tunnel.configure_failed", "host_id", host.ID, "error", err)
			_ = s.tunnelCreator.DeleteTunnel(ctx, tunnelID) // cleanup
			writeError(w, http.StatusInternalServerError, "failed to configure tunnel")
			return
		}

		if err := s.tunnelCreator.CreateDNSRoute(ctx, tunnelID, tunnelHostname); err != nil {
			slog.Error("enrollment.tunnel.dns_failed", "host_id", host.ID, "error", err)
			_ = s.tunnelCreator.DeleteTunnel(ctx, tunnelID) // cleanup
			writeError(w, http.StatusInternalServerError, "failed to create DNS route")
			return
		}

		// Store tunnel_url on host record
		host.TunnelURL = &tunnelHostname
		if err := s.store.UpdateHostDetails(ctx, host); err != nil {
			slog.Error("enrollment.tunnel.store_failed", "host_id", host.ID, "error", err)
			// Non-fatal: tunnel exists, host will work
		}

		// Store tunnel_id in provider_metadata
		var metadata map[string]interface{}
		if err := json.Unmarshal(host.ProviderMetadata, &metadata); err != nil {
			metadata = map[string]interface{}{}
		}
		metadata["tunnel_id"] = tunnelID
		if pmBytes, err := json.Marshal(metadata); err == nil {
			host.ProviderMetadata = pmBytes
			if err := s.store.UpdateHostProviderMetadata(ctx, host.ID, host.ProviderMetadata); err != nil {
				slog.Error("enrollment.tunnel.metadata_failed", "host_id", host.ID, "error", err)
				// Non-fatal
			}
		}

		tunnelToken = tToken
	}

	// 5. Mark enrollment token as used
	if err := s.store.MarkEnrollmentTokenUsed(ctx, req.EnrollmentToken, host.ID); err != nil {
		slog.Error("enrollment.register.mark_token_failed", "error", err, "host_id", host.ID)
		// Don't fail the registration — host is already created
	}

	// 6. Build response

	slog.Info("enrollment.register.success", "host_id", host.ID, "provider", host.Provider)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"host_id":                     host.ID,
		"agent_token":                 agentToken,
		"backend_url":                 s.backendURL,
		"rootfs_gcs_manifest":         s.rootfsGCSManifest,
		"agent_gcs_manifest":          s.agentGCSManifest,
		"browser_rootfs_gcs_manifest": s.browserRootfsGCSManifest,
		"browser_rootfs_version":      s.browserRootfsVersion,
		"gcs_credentials":             s.gcsServiceAccountKey,
		"tunnel_token":                tunnelToken,
	})
}

// handleDeregisterHost deregisters a host, cleaning up its tunnel if present.
func (s *Server) handleDeregisterHost(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	// Delete tunnel if exists
	if s.tunnelCreator != nil && host.ProviderMetadata != nil {
		var metadata map[string]interface{}
		if err := json.Unmarshal(host.ProviderMetadata, &metadata); err == nil {
			if tunnelID, ok := metadata["tunnel_id"].(string); ok && tunnelID != "" {
				hostnames := []string{}
				if host.TunnelURL != nil {
					hostnames = append(hostnames, *host.TunnelURL)
				}
				if err := s.tunnelCreator.DeleteTunnelAndDNS(r.Context(), tunnelID, hostnames...); err != nil {
					slog.Error("deregister.tunnel.cleanup_failed", "host_id", hostID, "error", err)
					// Continue — don't block deregistration on tunnel cleanup
				}
			}
		}
	}

	// Mark all machines on this host as error
	if _, err := s.store.MarkMachinesOnHostError(r.Context(), hostID, "host deregistered"); err != nil {
		slog.Error("deregister.machines.cleanup_failed", "host_id", hostID, "error", err)
		// Continue — don't block deregistration
	}

	// Mark host terminated
	if err := s.store.UpdateHostStatus(r.Context(), hostID, "terminated"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update host status")
		return
	}

	slog.Info("enrollment.deregister.success", "host_id", hostID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deregistered"})
}

// handleInstallScript returns a bash install script for host enrollment.
func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	backendURL := s.backendURL
	if backendURL == "" {
		backendURL = "https://api.openclawmachines.com"
	}

	script := strings.ReplaceAll(installScriptTemplate, "{{BACKEND_URL}}", backendURL)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script)) //nolint:errcheck
}

const installScriptTemplate = `#!/bin/bash
set -euo pipefail

# OpenClaw Machines Agent Install Script
# Usage: curl -sL {{BACKEND_URL}}/api/agent/install | bash -s -- <ENROLLMENT_TOKEN>

BACKEND_URL="{{BACKEND_URL}}"
ENROLLMENT_TOKEN="${1:-}"

if [ -z "$ENROLLMENT_TOKEN" ]; then
    echo "Usage: $0 <enrollment_token>"
    echo "Get a token from the admin panel or API."
    exit 1
fi

echo "==> Registering host with OCM control plane..."

# Detect agent endpoint (external IPv4)
EXTERNAL_IP=$(curl -s -4 --connect-timeout 5 https://ifconfig.me || curl -s -4 --connect-timeout 5 https://api.ipify.org || echo "")
# Bracket IPv6 addresses in URLs (fallback if only IPv6 available)
if echo "$EXTERNAL_IP" | grep -q ':'; then
    AGENT_ENDPOINT="http://[${EXTERNAL_IP}]:9090"
else
    AGENT_ENDPOINT="http://${EXTERNAL_IP}:9090"
fi

# Detect capabilities
CPU_THREADS=$(nproc 2>/dev/null || echo 0)
MEMORY_MB=$(awk '/MemTotal/ {printf "%d", $2/1024}' /proc/meminfo 2>/dev/null || echo 0)

# Register with control plane
RESPONSE=$(curl -sS -X POST "${BACKEND_URL}/api/agent/register" \
    -H "Content-Type: application/json" \
    -d "{
        \"enrollment_token\": \"${ENROLLMENT_TOKEN}\",
        \"agent_endpoint\": \"${AGENT_ENDPOINT}\",
        \"agent_endpoint_type\": \"public_http\",
        \"capabilities\": {\"kvm\": true, \"arch\": \"$(uname -m)\", \"cpu_threads\": ${CPU_THREADS}, \"memory_mb\": ${MEMORY_MB}},
        \"labels\": {}
    }")

HOST_ID=$(echo "$RESPONSE" | jq -r '.host_id // empty')
AGENT_TOKEN=$(echo "$RESPONSE" | jq -r '.agent_token // empty')
ROOTFS_MANIFEST=$(echo "$RESPONSE" | jq -r '.rootfs_gcs_manifest // empty')
AGENT_MANIFEST=$(echo "$RESPONSE" | jq -r '.agent_gcs_manifest // empty')
BROWSER_ROOTFS_MANIFEST=$(echo "$RESPONSE" | jq -r '.browser_rootfs_gcs_manifest // empty')
BROWSER_ROOTFS_VERSION=$(echo "$RESPONSE" | jq -r '.browser_rootfs_version // empty')
GCS_CREDS=$(echo "$RESPONSE" | jq -r '.gcs_credentials // empty')
TUNNEL_TOKEN=$(echo "$RESPONSE" | jq -r '.tunnel_token // empty')

if [ -z "$HOST_ID" ] || [ -z "$AGENT_TOKEN" ]; then
    echo "ERROR: Registration failed. Response:"
    echo "$RESPONSE"
    exit 1
fi

echo "==> Registered as host ${HOST_ID}"

# Write config
mkdir -p /etc/ocm-agent
cat > /etc/ocm-agent/agent.env <<EOF
FC_AGENT_TOKEN=${AGENT_TOKEN}
BACKEND_URL=${BACKEND_URL}
HOST_ID=${HOST_ID}
AGENT_ENDPOINT=${AGENT_ENDPOINT}
ROOTFS_GCS_MANIFEST=${ROOTFS_MANIFEST}
AGENT_GCS_MANIFEST=${AGENT_MANIFEST}
BROWSER_ROOTFS_GCS_MANIFEST=${BROWSER_ROOTFS_MANIFEST}
BROWSER_ROOTFS_VERSION=${BROWSER_ROOTFS_VERSION}
TUNNEL_TOKEN=${TUNNEL_TOKEN}
EOF

for ENV_FILE in /etc/ocm-agent/vm-state.env /etc/ocm-agent/browser-state.env; do
    if [ -f "$ENV_FILE" ]; then
        cat "$ENV_FILE" >> /etc/ocm-agent/agent.env
    fi
done

# Write GCS credentials if provided
if [ -n "$GCS_CREDS" ]; then
    echo "$GCS_CREDS" | base64 -d > /etc/ocm-agent/gcs-key.json 2>/dev/null || true
    echo "GOOGLE_APPLICATION_CREDENTIALS=/etc/ocm-agent/gcs-key.json" >> /etc/ocm-agent/agent.env
fi

# Install cloudflared binary (the agent manages the tunnel process)
if [ -n "$TUNNEL_TOKEN" ]; then
    if [ ! -f /usr/local/bin/cloudflared ]; then
        echo "==> Installing cloudflared..."
        ARCH=$(uname -m)
        if [ "$ARCH" = "aarch64" ]; then CF_ARCH="arm64"; else CF_ARCH="amd64"; fi
        curl -sL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${CF_ARCH}" \
          -o /usr/local/bin/cloudflared
        chmod +x /usr/local/bin/cloudflared
    else
        echo "==> cloudflared already installed"
    fi
fi

# Install agent systemd unit
cat > /etc/systemd/system/ocm-agent.service <<EOF
[Unit]
Description=OCM Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/ocm-agent/agent.env
ExecStart=/usr/local/bin/ocm-agent
Restart=always
RestartSec=5
LimitNOFILE=65536
# Only kill the agent process on stop; leave Firecracker children alive so
# Recover() can reattach them on the next start. Default KillMode=control-group
# would kill all running VMs during agent self-updates.
KillMode=process
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
EOF

# Download agent binary from GCS
download_agent() {
    if [ -z "$AGENT_MANIFEST" ]; then
        echo "WARNING: No agent manifest URL provided. Skipping agent download."
        return 1
    fi

    echo "==> Downloading agent manifest..."

    # Use gsutil if available; fall back to GCS JSON API with service account
    if command -v gsutil >/dev/null 2>&1 && [ -f /etc/ocm-agent/gcs-key.json ]; then
        gcloud auth activate-service-account --key-file=/etc/ocm-agent/gcs-key.json 2>/dev/null || true

        MANIFEST_JSON=$(gsutil cat "$AGENT_MANIFEST" 2>/dev/null)
    elif [ -f /etc/ocm-agent/gcs-key.json ]; then
        # Derive an OAuth2 access token from the service account key using curl + openssl
        SA_EMAIL=$(jq -r '.client_email' /etc/ocm-agent/gcs-key.json)
        SA_KEY=$(jq -r '.private_key' /etc/ocm-agent/gcs-key.json)
        TOKEN_URI=$(jq -r '.token_uri' /etc/ocm-agent/gcs-key.json)

        NOW=$(date +%s)
        EXP=$((NOW + 3600))
        HEADER=$(printf '{"alg":"RS256","typ":"JWT"}' | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')
        CLAIMS=$(printf '{"iss":"%s","scope":"https://www.googleapis.com/auth/devstorage.read_only","aud":"%s","iat":%d,"exp":%d}' \
            "$SA_EMAIL" "$TOKEN_URI" "$NOW" "$EXP" | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')
        SIGN_INPUT="${HEADER}.${CLAIMS}"
        SIGNATURE=$(printf '%s' "$SIGN_INPUT" | openssl dgst -sha256 -sign <(printf '%s' "$SA_KEY") | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')
        JWT="${SIGN_INPUT}.${SIGNATURE}"

        ACCESS_TOKEN=$(curl -sS -X POST "$TOKEN_URI" \
            -H "Content-Type: application/x-www-form-urlencoded" \
            -d "grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer&assertion=${JWT}" | jq -r '.access_token')

        if [ -z "$ACCESS_TOKEN" ] || [ "$ACCESS_TOKEN" = "null" ]; then
            echo "WARNING: Failed to obtain GCS access token. Skipping agent download."
            return 1
        fi

        # Parse gs://bucket/object from manifest URI
        MANIFEST_BUCKET=$(echo "$AGENT_MANIFEST" | sed 's|gs://||' | cut -d/ -f1)
        MANIFEST_OBJECT=$(echo "$AGENT_MANIFEST" | sed 's|gs://[^/]*/||' | jq -sRr '@uri')

        MANIFEST_JSON=$(curl -sS \
            "https://storage.googleapis.com/storage/v1/b/${MANIFEST_BUCKET}/o/${MANIFEST_OBJECT}?alt=media" \
            -H "Authorization: Bearer ${ACCESS_TOKEN}")
    else
        echo "WARNING: No GCS credentials available. Skipping agent download."
        return 1
    fi

    if [ -z "$MANIFEST_JSON" ]; then
        echo "WARNING: Could not fetch agent manifest. Skipping agent download."
        return 1
    fi

    AGENT_URL=$(echo "$MANIFEST_JSON" | jq -r '.url // empty')
    AGENT_SHA256=$(echo "$MANIFEST_JSON" | jq -r '.sha256 // empty')
    AGENT_VERSION=$(echo "$MANIFEST_JSON" | jq -r '.version // empty')
    AGENT_SIZE=$(echo "$MANIFEST_JSON" | jq -r '.size_bytes // empty')

    if [ -z "$AGENT_URL" ] || [ -z "$AGENT_SHA256" ]; then
        echo "WARNING: Invalid agent manifest. Skipping agent download."
        return 1
    fi

    echo "==> Downloading agent ${AGENT_VERSION} ($(( ${AGENT_SIZE:-0} / 1048576 )) MB)..."

    # Download agent binary
    AGENT_BUCKET=$(echo "$AGENT_URL" | sed 's|gs://||' | cut -d/ -f1)
    AGENT_OBJECT=$(echo "$AGENT_URL" | sed 's|gs://[^/]*/||')

    if command -v gsutil >/dev/null 2>&1 && [ -f /etc/ocm-agent/gcs-key.json ]; then
        gsutil cp "$AGENT_URL" /tmp/ocm-agent-download
    else
        AGENT_OBJECT_ENC=$(echo "$AGENT_OBJECT" | jq -sRr '@uri')
        curl -sS -o /tmp/ocm-agent-download \
            "https://storage.googleapis.com/storage/v1/b/${AGENT_BUCKET}/o/${AGENT_OBJECT_ENC}?alt=media" \
            -H "Authorization: Bearer ${ACCESS_TOKEN}"
    fi

    # Verify SHA256
    echo "==> Verifying SHA256..."
    GOT_SHA256=$(sha256sum /tmp/ocm-agent-download | awk '{print $1}')
    if [ "$GOT_SHA256" != "$AGENT_SHA256" ]; then
        echo "ERROR: SHA256 mismatch!"
        echo "  Expected: ${AGENT_SHA256}"
        echo "  Got:      ${GOT_SHA256}"
        rm -f /tmp/ocm-agent-download
        return 1
    fi
    echo "    SHA256 OK: ${GOT_SHA256}"

    # Install binary
    mv /tmp/ocm-agent-download /usr/local/bin/ocm-agent
    chmod +x /usr/local/bin/ocm-agent

    echo "==> Agent ${AGENT_VERSION} installed to /usr/local/bin/ocm-agent"
    return 0
}

# Download kernel from GCS if not present
if [ ! -f /var/lib/ocm/vmlinux ] && [ -f /etc/ocm-agent/gcs-key.json ]; then
    echo "==> Downloading kernel from GCS..."
    mkdir -p /var/lib/ocm/images
    if command -v gsutil >/dev/null 2>&1; then
        gcloud auth activate-service-account --key-file=/etc/ocm-agent/gcs-key.json 2>/dev/null || true
        gsutil cp gs://openclawmachines/vmlinux /var/lib/ocm/images/vmlinux 2>/dev/null && \
            ln -sf /var/lib/ocm/images/vmlinux /var/lib/ocm/vmlinux && \
            echo "    Kernel installed" || \
            echo "WARNING: Failed to download kernel from GCS"
    else
        echo "WARNING: gsutil not available. Kernel must be installed manually."
    fi
elif [ -f /var/lib/ocm/vmlinux ]; then
    echo "==> Kernel already present"
fi

download_agent
AGENT_DOWNLOADED=$?

# Start services
systemctl daemon-reload

if [ "$AGENT_DOWNLOADED" -eq 0 ]; then
    echo "==> Starting agent..."
    systemctl enable --now ocm-agent
    echo ""
    echo "=========================================="
    echo "  Host enrolled and agent started!"
    echo "  Host ID:  ${HOST_ID}"
    echo "  Endpoint: ${AGENT_ENDPOINT}"
    echo "=========================================="
else
    echo ""
    echo "=========================================="
    echo "  Host enrolled but agent not downloaded."
    echo "  Host ID:  ${HOST_ID}"
    echo ""
    echo "  To finish setup manually:"
    echo "    1. Download agent to /usr/local/bin/ocm-agent"
    echo "    2. sudo systemctl enable --now ocm-agent"
    echo "=========================================="
fi
`
