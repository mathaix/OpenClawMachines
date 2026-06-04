# Tunnel Automation for 3rd-Party Hosts — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Automatically create Cloudflare tunnels during host enrollment so 3rd-party hosts have the same tunnel architecture as GCP hosts.

**Architecture:** During `handleAgentRegister`, after creating the host record, create a Cloudflare tunnel + DNS + ingress via `tunnel.Manager`. Return `tunnel_token` in the registration response. The install script writes it to `agent.env`, and the agent starts cloudflared. Tunnel creation is mandatory — registration fails if it fails.

**Tech Stack:** Go (backend), Cloudflare Tunnel API (via `tunnel.Manager`), bash (install script template)

---

### Task 1: Agent reads TUNNEL_TOKEN from environment

The agent currently reads the tunnel token exclusively from GCE metadata. Add env var support so enrolled hosts can pass it via `agent.env`.

**Files:**
- Modify: `backend/internal/config/config.go:148-182` (LoadAgent function)
- Modify: `backend/cmd/agent/main.go:245-248` (fatal check)

**Step 1: Update `LoadAgent` to read TUNNEL_TOKEN from env**

In `backend/internal/config/config.go`, find the `LoadAgent` function. Add `TUNNEL_TOKEN` env var reading before the return:

```go
// In LoadAgent(), before the return statement:
if token := os.Getenv("TUNNEL_TOKEN"); token != "" {
    cfg.TunnelToken = token
}
```

This runs before `prefetchGCPMetadata` in `main.go`, but GCE metadata overwrites it. We need the env var to take precedence. So instead, add a check AFTER metadata fetch in `main.go`.

Actually, the cleaner approach: in `main.go`, after `prefetchGCPMetadata(cfg)` returns (around line 240), check the env var as override:

```go
// After prefetchGCPMetadata:
if envToken := os.Getenv("TUNNEL_TOKEN"); envToken != "" && cfg.TunnelToken == "" {
    cfg.TunnelToken = envToken
}
```

**Step 2: Change fatal to warning in `main.go:245-248`**

Replace:
```go
if cfg.TunnelToken == "" {
    slog.Error("config.missing_tunnel_token", "error", "tunnel-token is required — cannot start agent without Cloudflare Tunnel")
    os.Exit(1)
}
```

With:
```go
if cfg.TunnelToken == "" {
    slog.Warn("config.missing_tunnel_token", "msg", "no tunnel token — cloudflared will not start")
}
```

The existing `if cfg.TunnelToken != ""` check at line 272 already gates cloudflared startup.

**Step 3: Run tests**

Run: `make test-go`
Expected: All pass (no agent tests depend on fatal behavior)

**Step 4: Commit**

```
feat: agent reads TUNNEL_TOKEN from env, non-fatal if missing
```

---

### Task 2: Change registered host naming to match GCP pattern

**Files:**
- Modify: `backend/internal/api/enrollment.go:131`

**Step 1: Change VMName generation**

In `enrollment.go`, line 131, change:
```go
VMName: fmt.Sprintf("registered-%d", time.Now().UnixNano()),
```
To:
```go
VMName: fmt.Sprintf("ocm-host-%d", time.Now().UnixMilli()),
```

This matches the GCP provisioner pattern at `provisioner.go:119`.

**Step 2: Update test expectations**

In `enrollment_test.go`, update any assertions that check for `registered-` prefix in VMName to check for `ocm-host-` prefix.

**Step 3: Run tests**

Run: `make test-go`
Expected: All pass

**Step 4: Commit**

```
refactor: align registered host naming with GCP pattern
```

---

### Task 3: Add tunnel creation to handleAgentRegister

This is the core change. The Server struct already has `tunnelMgr *tunnel.Manager` at line 45 of server.go.

**Files:**
- Modify: `backend/internal/api/enrollment.go:75-186` (handleAgentRegister)
- Modify: `backend/internal/api/enrollment_test.go` (add tunnel mock + tests)

**Step 1: Write failing tests**

Add to `enrollment_test.go`:

```go
// mockTunnelManager for testing tunnel creation during registration
type mockTunnelManager struct {
    createCalled    bool
    configureCalled bool
    dnsCalled       bool
    deleteCalled    bool
    shouldFail      string // "create", "configure", "dns", or ""
    tunnelID        string
    tunnelToken     string
}

func (m *mockTunnelManager) CreateTunnel(ctx context.Context, name string) (string, string, error) {
    m.createCalled = true
    if m.shouldFail == "create" {
        return "", "", fmt.Errorf("tunnel creation failed")
    }
    return m.tunnelID, m.tunnelToken, nil
}

func (m *mockTunnelManager) ConfigureTunnel(ctx context.Context, tunnelID, hostname string) error {
    m.configureCalled = true
    if m.shouldFail == "configure" {
        return fmt.Errorf("tunnel configure failed")
    }
    return nil
}

func (m *mockTunnelManager) CreateDNSRoute(ctx context.Context, tunnelID, hostname string) error {
    m.dnsCalled = true
    if m.shouldFail == "dns" {
        return fmt.Errorf("dns creation failed")
    }
    return nil
}

func (m *mockTunnelManager) DeleteTunnel(ctx context.Context, tunnelID string) error {
    m.deleteCalled = true
    return nil
}
```

Then add test cases:

```go
func TestHandleAgentRegister_TunnelCreation(t *testing.T) {
    // Test: tunnel created and token returned
    // Test: registration fails when tunnel creation fails
    // Test: partial failure cleans up tunnel
}
```

Note: `tunnel.Manager` is a concrete struct, not an interface. You'll need to either:
- Extract an interface from `tunnel.Manager` for the methods used by enrollment (CreateTunnel, ConfigureTunnel, CreateDNSRoute, DeleteTunnel)
- OR add the interface to enrollment.go

Create a `TunnelCreator` interface in `enrollment.go`:

```go
// TunnelCreator is the subset of tunnel.Manager used by enrollment.
type TunnelCreator interface {
    CreateTunnel(ctx context.Context, name string) (tunnelID string, token string, err error)
    ConfigureTunnel(ctx context.Context, tunnelID, hostname string) error
    CreateDNSRoute(ctx context.Context, tunnelID, hostname string) error
    DeleteTunnel(ctx context.Context, tunnelID string) error
}
```

**Step 2: Run tests to verify they fail**

Run: `make test-go`
Expected: Tests fail (tunnel creation not implemented in handler)

**Step 3: Implement tunnel creation in handleAgentRegister**

In `enrollment.go`, after the host record is created (line ~165) and before the response is written, add:

```go
// Create Cloudflare tunnel for the host
var tunnelToken string
if s.tunnelMgr != nil {
    tunnelHostname := host.VMName + ".openclawmachines.com"
    tunnelID, tToken, err := s.tunnelMgr.CreateTunnel(ctx, host.VMName)
    if err != nil {
        slog.Error("enrollment.tunnel.create_failed", "host_id", host.ID, "error", err)
        writeError(w, http.StatusInternalServerError, "failed to create tunnel")
        return
    }

    // Configure ingress and DNS
    if err := s.tunnelMgr.ConfigureTunnel(ctx, tunnelID, tunnelHostname); err != nil {
        slog.Error("enrollment.tunnel.configure_failed", "host_id", host.ID, "error", err)
        _ = s.tunnelMgr.DeleteTunnel(ctx, tunnelID) // cleanup
        writeError(w, http.StatusInternalServerError, "failed to configure tunnel")
        return
    }
    if err := s.tunnelMgr.CreateDNSRoute(ctx, tunnelID, tunnelHostname); err != nil {
        slog.Error("enrollment.tunnel.dns_failed", "host_id", host.ID, "error", err)
        _ = s.tunnelMgr.DeleteTunnel(ctx, tunnelID) // cleanup
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
    _ = json.Unmarshal(host.ProviderMetadata, &metadata)
    metadata["tunnel_id"] = tunnelID
    host.ProviderMetadata, _ = json.Marshal(metadata)
    _, _ = s.store... // update provider_metadata

    tunnelToken = tToken
}
```

Note: `UpdateHostDetails` at `postgres.go:622` updates `vm_id, external_ip, internal_ip, tunnel_url`. For `provider_metadata` with the `tunnel_id`, you may need a separate store method or update `UpdateHostDetails` to also write `provider_metadata`. Check what's simplest — may just add a `UpdateHostProviderMetadata` method or extend `UpdateHostDetails`.

**Step 4: Add tunnel_token to the registration response**

In the response JSON (around line 175-185), add:
```go
"tunnel_token": tunnelToken,
```

**Step 5: Run tests**

Run: `make test-go`
Expected: All pass

**Step 6: Commit**

```
feat: create Cloudflare tunnel during host enrollment
```

---

### Task 4: Update install script to handle tunnel token and cloudflared

**Files:**
- Modify: `backend/internal/api/enrollment.go:202-295` (installScriptTemplate)

**Step 1: Add tunnel_token extraction to install script**

In the install script template, after the existing `jq` extractions (around line 246-250), add:

```bash
TUNNEL_TOKEN=$(echo "$RESPONSE" | jq -r '.tunnel_token // empty')
```

**Step 2: Add TUNNEL_TOKEN to agent.env**

In the `cat > /etc/ocm-agent/agent.env` block (around line 254), add:

```bash
TUNNEL_TOKEN=${TUNNEL_TOKEN}
```

**Step 3: Add cloudflared installation**

Before the systemd unit creation, add:

```bash
# Install cloudflared
echo "==> Installing cloudflared..."
curl -sL https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \
  -o /usr/local/bin/cloudflared
chmod +x /usr/local/bin/cloudflared
```

**Step 4: Update install script test**

In `enrollment_test.go`, the `TestHandleInstallScript` test checks script content. Add assertions that the script contains `TUNNEL_TOKEN` and `cloudflared`.

**Step 5: Run tests**

Run: `make test-go`
Expected: All pass

**Step 6: Commit**

```
feat: install script installs cloudflared and writes tunnel token
```

---

### Task 5: Add admin deregister host endpoint

Registered hosts never auto-terminate, so tunnel cleanup needs an explicit admin action.

**Files:**
- Modify: `backend/internal/api/server.go` (add route + handler)
- Modify: `backend/internal/api/enrollment.go` (add handler)
- Modify: `backend/internal/api/enrollment_test.go` (add tests)

**Step 1: Write failing test**

```go
func TestHandleDeregisterHost(t *testing.T) {
    // Test: deregisters host, deletes tunnel, marks terminated
    // Test: 404 for unknown host
    // Test: handles host with no tunnel gracefully
}
```

**Step 2: Add route**

In `server.go`, in the admin routes group (around line 253), add:

```go
r.Delete("/hosts/{hostId}", srv.handleDeregisterHost)
```

**Step 3: Implement handler**

In `enrollment.go`, add:

```go
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
    if s.tunnelMgr != nil && host.ProviderMetadata != nil {
        var metadata map[string]interface{}
        if err := json.Unmarshal(host.ProviderMetadata, &metadata); err == nil {
            if tunnelID, ok := metadata["tunnel_id"].(string); ok && tunnelID != "" {
                hostnames := []string{}
                if host.TunnelURL != nil {
                    hostnames = append(hostnames, *host.TunnelURL)
                }
                if err := s.tunnelMgr.DeleteTunnelAndDNS(r.Context(), tunnelID, hostnames...); err != nil {
                    slog.Error("deregister.tunnel.cleanup_failed", "host_id", hostID, "error", err)
                    // Continue — don't block deregistration on tunnel cleanup
                }
            }
        }
    }

    // Mark machines as error and release placements
    machines, _ := s.store.ListMachinesOnHost(r.Context(), hostID)
    for _, m := range machines {
        _ = s.store.ReleasePlacement(r.Context(), m.ID)
    }
    _ = s.store.MarkMachinesOnHostError(r.Context(), hostID)

    // Mark host terminated
    if err := s.store.UpdateHostStatus(r.Context(), hostID, "terminated"); err != nil {
        writeError(w, http.StatusInternalServerError, "failed to update host status")
        return
    }

    writeJSON(w, http.StatusOK, map[string]string{"status": "deregistered"})
}
```

Note: Check if `ListMachinesOnHost` exists on the store. If not, use the existing `MarkMachinesOnHostError` which handles the bulk update, and skip per-machine placement release (or add a store method).

**Step 4: Run tests**

Run: `make test-go`
Expected: All pass

**Step 5: Commit**

```
feat: add admin endpoint to deregister hosts and clean up tunnels
```

---

### Task 6: Update enrollment guide and CurrentFeature.md

**Files:**
- Modify: `docs/host-enrollment.md` (remove manual tunnel step)
- Modify: `docs/CurrentFeature.md` (update status)

**Step 1: Remove Step 5 from host-enrollment.md**

Remove the entire "Step 5: Set Up Cloudflare Tunnel (Manual)" section (lines 79-94). Tunnel creation is now automated during registration.

Add a note after Step 4 (Verify Enrollment):
```markdown
The Cloudflare tunnel is automatically created during registration. The agent starts
cloudflared using the token provided during enrollment. Per-VM tunnels for workspace
access are created when machines are started.
```

**Step 2: Update CurrentFeature.md**

Add tunnel automation to the implemented list.

**Step 3: Commit**

```
docs: update enrollment guide — tunnel creation is now automated
```

---

### Task 7: Full verification

**Step 1: Run full test suite**

Run: `make test-go && make typecheck`
Expected: All pass

**Step 2: Deploy backend**

Run: `make deploy-backend`

**Step 3: Test with OVH host**

1. Create enrollment token via admin UI
2. Run install script on OVH server
3. Verify: agent registers, tunnel is created, host appears in admin panel
4. Verify: `sudo journalctl -u ocm-agent | grep cloudflared` shows tunnel connected
5. Start a machine on the host
6. Verify: workspace accessible via `m-{slug}.openclawmachines.com`

**Step 4: Commit any fixes**

```
fix: [description of any issues found during E2E]
```
