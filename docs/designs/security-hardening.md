# Design: Security Hardening

**Status:** Proposed
**Branch:** openclaworking
**References:** `docs/security-review.md`, `docs/SECURITY_HARDENING.md`

## Problem

Five classes of security gaps exist between the current implementation and production readiness:

1. **Metadata dependency** — NAT and metadata server failures are soft warnings; VMs boot without isolation or config
2. **Proxy and backend auth gaps** — file proxy missing token, no resource bounds, path traversal risk
3. **CORS/origin** — agent WebSocket upgrader accepts all origins
4. **Secrets exposure** — metadata serves tokens/keys in cleartext, authenticated only by source IP
5. **Default config assumptions** — security-critical configs don't all hard-fail

## Current State vs. Actual Risk

Several concerns from earlier reviews have already been mitigated. This section clarifies what's fixed and what remains open.

### Already Mitigated

| Concern | Status | Evidence |
|---------|--------|----------|
| FC_AGENT_TOKEN missing | Fixed | `agent/main.go:48-49` — `log.Fatalf` if empty |
| Tunnel token missing | Fixed | `agent/main.go:109-110` — `log.Fatalf` if empty |
| Account authorization | Fixed | `api/server.go:143-172` — `AccountMiddleware` checks membership via `store.GetAccountMember()` |
| Machine ownership | Fixed | Every handler checks `machine.AccountID != accountID` → 403 |
| CORS * + credentials | Fixed | `api/server.go:78-82` — disables credentials when wildcard is used |
| Worker CORS | Fixed | `worker.js:14-23` — validates against `*.openclawmachines.com`, HTTPS only |
| Proxy API binding | Fixed | `agent/main.go:108` — proxy binds `127.0.0.1`, only reachable via Cloudflare Tunnel |
| Bearer/proxy token timing | Fixed | Both use `subtle.ConstantTimeCompare` |
| Empty proxy token | Fixed | `proxy.go:60-64` — rejects with 403 if VM has no token configured |
| VM-to-VM isolation | Fixed | iptables blocks inter-VM traffic on bridge |
| GCP metadata blocked | Fixed | iptables drops `169.254.169.254` from bridge subnet |
| Agent ports blocked | Fixed | iptables drops ports 9090/9091 from bridge |
| Secrets at rest | Fixed | AES-256-GCM encryption in `pkg/crypto/` |

### Still Open (This Design Addresses)

| # | Issue | Severity | Location |
|---|-------|----------|----------|
| H1 | NAT failure is soft warning | High | `agent/main.go:64-66` |
| H2 | Metadata server failure is soft warning | High | `agent/main.go:71-75` |
| H3 | Metadata has no per-VM auth | High | `metadata/server_linux.go:107-110` |
| H4 | Metadata serves secrets in cleartext HTTP | Medium | `metadata/server_linux.go:95-103` |
| H5 | Missing proxy token on file operations | High | `api/machine_files.go:48-49,88-89,127-128` |
| H6 | No resource bounds on machine sizing | Medium | `api/server.go:356-380` |
| H7 | File path traversal in proxy | High | `api/machine_files.go:43-49` |
| H8 | No slug format validation | Medium | `api/server.go:277,358` |
| H9 | Agent WS CheckOrigin accepts all origins | Low | `agentapi/proxy.go:20-24` |
| H10 | API keys in GCE instance metadata | Medium | `provisioner/provisioner.go:115-127` |

---

## Changes

### 1. Make NAT Setup Fatal (H1)

**File:** `backend/cmd/agent/main.go`

NAT rules provide critical VM isolation (blocks VM-to-VM, GCP metadata, agent ports). Without them, a compromised VM can reach other VMs, the host metadata service, and the agent control API.

**Change:** Replace soft warning with `log.Fatalf`.

```
Before:  log.Printf("WARNING: NAT setup failed: %v", err)
After:   log.Fatalf("NAT setup failed (required for VM isolation): %v", err)
```

### 2. Make Metadata Server Startup Fatal on Linux (H2)

**File:** `backend/cmd/agent/main.go`

The metadata server must be running before any VM boots. Currently it's a fire-and-forget goroutine with a WARNING on failure. On Linux (production), this should be fatal. On non-Linux (dev), it's expected to fail.

**Change:** Start metadata server synchronously with a readiness check. Use a startup channel pattern:

```
metaSrv := metadata.New(bridgeGateway, 80)
ready := make(chan error, 1)
go func() {
    ready <- metaSrv.Start(ctx)
}()

// Give it 2 seconds to bind
select {
case err := <-ready:
    log.Fatalf("metadata server failed: %v", err)
case <-time.After(2 * time.Second):
    // Server is running (ListenAndServe blocks until error)
}
```

Actually simpler: add a `WaitReady()` method to the metadata server that polls `/health` on itself, similar to how the VM init script does it. If it doesn't respond within 2s on Linux, fatal.

### 3. Add Per-VM Metadata Nonce (H3)

**Files:** `metadata/metadata.go`, `metadata/server_linux.go`, `orchestrator/firecracker_linux.go`, `scripts/init-openclaw.sh`

Source IP alone is insufficient authentication. A compromised VM can't spoof IPs (iptables drops inter-bridge traffic), but defense-in-depth requires a second factor.

**Change:** Generate a random nonce per VM at creation time. Pass it via kernel cmdline (already used for IP config). Metadata server validates nonce on every request.

**Flow:**
```
VM Creation:
  1. Generate 32-char hex nonce (crypto/rand)
  2. Append to kernel boot_args: "metadata_nonce=<nonce>"
  3. RegisterMachine(vmIP, nonce, config)

VM Boot (init-openclaw.sh):
  1. Parse nonce from /proc/cmdline: metadata_nonce=<value>
  2. Include in all metadata requests: curl -H "X-Metadata-Nonce: <nonce>" ...

Metadata Server:
  1. Store nonce alongside config: configs[vmIP] = {nonce, config}
  2. On request: extract source IP + X-Metadata-Nonce header
  3. Validate both match → serve config
  4. Mismatch → 403 + log alert
```

**Data structures:**
```go
// metadata.go
type registeredVM struct {
    Nonce  string
    Config MachineConfig
}

type Server struct {
    mu      sync.RWMutex
    configs map[string]registeredVM // keyed by VM IP
}

func (s *Server) RegisterMachine(vmIP, nonce string, config MachineConfig)
```

**Why not encrypt responses (H4)?** The bridge network is host-local and iptables prevents cross-VM traffic. The nonce provides authentication. Encryption would require key exchange, adding complexity without meaningful benefit given the isolation guarantees. The nonce solves the actual threat (unauthenticated access).

### 4. Add Proxy Token to File Operations (H5)

**File:** `backend/internal/api/machine_files.go`

Three file proxy handlers (`handleListFiles`, `handleDownloadFile`, `handleZipFiles`) don't include `X-Proxy-Token` when proxying to the agent. All other proxy routes include it.

**Change:** Add proxy token header to all three handlers, matching the pattern used in terminal/browser/logs proxy handlers:

```go
proxyReq.Header.Set("X-Proxy-Token", machine.ProxyToken)
```

### 5. Add Resource Bounds on Machine Sizing (H6)

**File:** `backend/internal/api/server.go` (machine create/update handlers)

VCPUs and MemoryMB accepted with no upper limit.

**Change:** Validate at API boundary:

```go
const (
    MinVCPUs    = 1
    MaxVCPUs    = 8
    MinMemoryMB = 512
    MaxMemoryMB = 8192
)
```

Return 400 if outside bounds.

### 6. Add File Path Validation (H7)

**File:** `backend/internal/api/machine_files.go`

`filePath` from query params is directly concatenated into agent URL without escaping or traversal checks.

**Change:**
1. `url.QueryEscape(filePath)` when building agent URL
2. Reject paths containing `..` or absolute paths starting with `/etc`, `/proc`, `/sys`
3. Normalize path with `path.Clean()` before use

```go
filePath := r.URL.Query().Get("path")
cleaned := path.Clean(filePath)
if strings.Contains(cleaned, "..") {
    writeError(w, http.StatusBadRequest, "path traversal not allowed")
    return
}
```

### 7. Add Slug Format Validation (H8)

**File:** `backend/internal/api/server.go` (machine create, account create)

Slugs are used in KV keys, DNS records, and tunnel routes.

**Change:** Validate with regex at creation time:

```go
var slugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

func isValidSlug(s string) bool {
    return len(s) >= 2 && len(s) <= 63 && slugRegex.MatchString(s)
}
```

Reject invalid slugs with 400.

### 8. Tighten Agent WebSocket Origin (H9)

**File:** `backend/internal/agentapi/proxy.go`

The `CheckOrigin` currently returns `true` for all origins. While proxy token auth provides the primary security, defense-in-depth means validating origins too.

**Change:** Accept empty origin (non-browser clients) and any `*.openclawmachines.com` origin. Reject all others.

```go
var wsUpgrader = websocket.Upgrader{
    CheckOrigin: func(r *http.Request) bool {
        origin := r.Header.Get("Origin")
        if origin == "" {
            return true // non-browser (curl, agent client)
        }
        // Accept *.openclawmachines.com
        u, err := url.Parse(origin)
        if err != nil {
            return false
        }
        return u.Scheme == "https" &&
            (u.Host == "openclawmachines.com" ||
             strings.HasSuffix(u.Host, ".openclawmachines.com"))
    },
}
```

The domain could be made configurable via agent config if needed.

### 9. Migrate API Keys from GCE Metadata to GCP Secret Manager (H10)

**File:** `backend/internal/provisioner/provisioner.go`

Anthropic, OpenAI, and Google API keys are currently stored as GCE instance metadata items. Any host-level compromise exposes all provider keys.

**Change:** Instead of injecting API keys via GCE metadata at VM provisioning time, the agent should fetch them from GCP Secret Manager at startup (same pattern as the encryption key change in `docs/designs/gcp-secret-manager.md`).

The agent already has `prefetchGCPMetadata()` — extend the `secrets.FetchSecret()` function (from the GCP SM design) to also fetch LLM API keys. Remove the API key metadata items from the provisioner.

---

## Startup Flow (After All Changes)

```
Agent startup
  |
  1. Load config
  2. Prefetch GCP metadata (tokens, backend URL)
  3. Fetch API keys from GCP Secret Manager (new)
  |
  +-- FC_AGENT_TOKEN empty? → FATAL
  +-- TUNNEL_TOKEN empty?   → FATAL
  |
  4. Bridge setup
  |   +-- Failure? → FATAL
  |
  5. NAT setup
  |   +-- Failure? → FATAL (changed from WARNING)
  |
  6. Metadata server start + readiness check
  |   +-- Not ready in 2s? → FATAL (changed from WARNING)
  |
  7. Orchestrator init (with metadata nonce support)
  8. Agent API servers (control :9090, proxy 127.0.0.1:9091)
  9. Cloudflare Tunnel
  10. Ready
```

## Impact on Existing Flows

### VM Creation (with nonce)

```
Before:
  orchestrator.Create(cfg) → metadata.RegisterMachine(vmIP, config) → boot VM

After:
  orchestrator.Create(cfg) → generate nonce → metadata.RegisterMachine(vmIP, nonce, config)
    → append "metadata_nonce=<nonce>" to kernel boot_args → boot VM
```

### VM Init Script (with nonce)

```
Before:
  curl -sf http://192.168.100.1/v1/config

After:
  NONCE=$(grep -oP 'metadata_nonce=\K[^ ]+' /proc/cmdline)
  curl -sf -H "X-Metadata-Nonce: $NONCE" http://192.168.100.1/v1/config
```

### File Operations (with proxy token)

```
Before:
  proxy request to agent without X-Proxy-Token header

After:
  proxy request to agent with X-Proxy-Token: <machine.ProxyToken>
```

### Machine Create/Update (with bounds)

```
Before:
  accept any VCPUs/MemoryMB value

After:
  reject VCPUs outside [1, 8], MemoryMB outside [512, 8192] → 400
```

## What Does NOT Change

- Encryption (AES-256-GCM) — untouched
- JWT auth flow — untouched
- AccountMiddleware / machine ownership checks — untouched (already working)
- CORS on backend and worker — untouched (already correct)
- Proxy token validation logic — untouched
- Database schema — untouched
- Cloudflare Tunnel routing — untouched
- Bridge subnet (192.168.100.0/24) — untouched

## Verification

| Change | Test |
|--------|------|
| NAT fatal | Agent must not start if iptables fails (test with non-root) |
| Metadata fatal | Agent must not start if port 80 bind fails (test with non-root) |
| Metadata nonce | VM must fail to get config without correct nonce |
| File proxy token | File list/download must work end-to-end (was silently broken) |
| Resource bounds | `POST /machines` with VCPUs=100 → 400 |
| Path traversal | `GET /files?path=../../etc/passwd` → 400 |
| Slug validation | `POST /machines` with slug `../evil` → 400 |
| WS origin | Agent rejects WS upgrade from `https://evil.com` |
| API keys from SM | Agent logs "fetched X from GCP Secret Manager" at startup |

## Implementation Order

1. **H5 + H7** — File proxy token + path traversal (bug fixes, ship immediately)
2. **H1 + H2** — NAT/metadata fatal (one-line changes, low risk)
3. **H6 + H8** — Resource bounds + slug validation (input validation, low risk)
4. **H3** — Metadata nonce (touches orchestrator + init script, needs snapshot rebuild)
5. **H9** — WS origin check (low risk, defense-in-depth)
6. **H10** — API keys from GCP SM (pairs with `docs/designs/gcp-secret-manager.md`)
