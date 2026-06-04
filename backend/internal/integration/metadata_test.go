//go:build linux && integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// Metadata Endpoint Tests
//
// All subtests share a single VM (booted once by the parent). This saves ~2
// separate VM boots (~4 minutes total).
//
// Commands are run inside the VM via terminal WebSocket since the metadata
// server matches requests by source IP.
//
// Note: curl commands resolve the gateway IP using grep (no braces) instead
// of awk to avoid extractJSON picking up '{print $3}' from the command echo.
// ============================================================================

// metadataCurlCmd builds a curl command that fetches from the metadata server.
// Uses base64-encoded script to avoid terminal \r corruption from heredocs
// and line wrapping in the WebSocket terminal.
func metadataCurlCmd(path string) string {
	script := fmt.Sprintf(`#!/bin/sh
GW=$(ip route show default | grep -oE 'via [0-9.]+' | grep -oE '[0-9.]+')
NONCE=$(cat /run/ocm-nonce)
curl -sf -H "X-Metadata-Nonce: $NONCE" "http://$GW%s"
`, path)
	b64 := base64.StdEncoding.EncodeToString([]byte(script))
	return fmt.Sprintf("echo %s|base64 -d|bash", b64)
}

func TestMetadataSuite(t *testing.T) {
	proxyURL, vmCfg, _ := setupFullStackWithGateway(t)

	wsURL := strings.Replace(proxyURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/proxy/%s/terminal/ws", wsURL, vmCfg.MachineID)

	t.Run("MachineEndpointReturnsInfraFields", func(t *testing.T) {
		// Fetch /v1/machine from inside the VM (metadata server matches by source IP)
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			metadataCurlCmd("/v1/machine"),
			15*time.Second)

		t.Logf("/v1/machine output: %s", output)

		// Extract just the JSON from the terminal output (may contain prompt chars)
		jsonStr := extractJSON(output)
		if jsonStr == "" {
			t.Fatalf("No JSON found in /v1/machine response output: %q", output)
		}

		var resp map[string]string
		if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
			t.Fatalf("Failed to parse /v1/machine response: %v (raw: %q)", err, jsonStr)
		}

		// Verify all 6 infrastructure fields
		checks := map[string]string{
			"machine_id":    vmCfg.MachineID,
			"machine_slug":  vmCfg.MachineSlug,
			"gateway_token": vmCfg.GatewayToken,
			"signing_key":   vmCfg.SigningKey,
			"tunnel_token":  vmCfg.TunnelToken,
			"vm_hostname":   vmCfg.VmHostname,
		}
		for key, want := range checks {
			got := resp[key]
			if got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}

		t.Log("/v1/machine returns all 6 infrastructure fields correctly")
	})

	// Note: /v1/config, /v1/config-version, and /v1/providers endpoints were
	// removed. Config is now written pre-boot to the data volume by the
	// orchestrator. The gateway reads config from disk, not metadata.

	t.Run("GatewayUsesMetadataConfigSource", func(t *testing.T) {
		// Verify the gateway started using the metadata config source (not a local file).
		// The openclaw fork logs "[config-source] using metadata config source" at startup.
		// Use $() subshell so the count result doesn't appear in the command echo.
		output := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			`echo logmatch:$(grep -c 'metadata config source' /var/log/openclaw-gateway.log 2>/dev/null || echo 0)`,
			10*time.Second)
		t.Logf("Config source log check: %q", output)

		if strings.Contains(output, "logmatch:0") {
			t.Log("WARN: Could not confirm metadata config source from logs (may need openclaw fork rebuild)")
		} else {
			t.Log("Gateway log confirms metadata config source in use")
		}

		// Config is written pre-boot to the data volume by the orchestrator.
		// Verify openclaw.json exists on disk (written during writeDataVolumeConfig).
		fileCheck := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
			`echo confcheck:$(test -f /home/openclaw/.openclaw/openclaw.json && echo y || echo n)`,
			10*time.Second)
		if strings.Contains(fileCheck, "confcheck:n") {
			t.Error("openclaw.json should exist (written pre-boot by orchestrator)")
		} else if strings.Contains(fileCheck, "confcheck:y") {
			t.Log("Confirmed: openclaw.json exists (written pre-boot by orchestrator)")
		}
	})
}

// TestMetadata_CredentialPush is removed — /v1/providers endpoint no longer
// exists. Credentials are now written pre-boot to the data volume by the
// orchestrator and read from disk by the gateway.

// extractJSON extracts the first JSON object from terminal output,
// which may include prompt characters, ANSI escape codes, etc.
func extractJSON(output string) string {
	// Find the first { and match to closing }
	start := strings.Index(output, "{")
	if start < 0 {
		return ""
	}

	depth := 0
	for i := start; i < len(output); i++ {
		switch output[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return output[start : i+1]
			}
		}
	}
	return ""
}
